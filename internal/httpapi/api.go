package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/buildinfo"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/project"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/webui"
)

type API struct {
	logger     *slog.Logger
	store      runmodel.Store
	bodyLimit  int64
	startedAt  time.Time
	sessions   identity.Sessions
	authorized runmodel.AuthorizedStore
	projects   project.Store
	origin     string
	oauth      *GitHubLogin
}

// NewEvaluation exposes the deliberately unauthenticated milestone API.
// The executable may call this only after the explicit evaluation config gate.
func NewEvaluation(logger *slog.Logger, store runmodel.Store, bodyLimit int64) http.Handler {
	api := &API{logger: logger, store: store, bodyLimit: bodyLimit, startedAt: time.Now().UTC()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("GET /readyz", api.ready)
	mux.HandleFunc("GET /api/v1/auth/status", api.authStatus)
	mux.HandleFunc("GET /api/v1/system/info", api.systemInfo)
	mux.HandleFunc("POST /api/v1/pipelines/validate", api.validatePipeline)
	mux.HandleFunc("GET /api/v1/runs", api.listRuns)
	mux.HandleFunc("POST /api/v1/runs", api.createRun)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "not found", "API endpoint does not exist")
	})
	mux.Handle("/", webui.Handler())
	return api.middleware(mux)
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "not ready", "database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) systemInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": "YuanCI", "version": buildinfo.Version, "commit": buildinfo.Commit,
		"go_version": runtime.Version(), "started_at": a.startedAt,
		"capabilities": []string{"pipeline-v1", "postgresql", "runner-protocol-v1"},
	})
}

type validatePipelineRequest struct {
	Content string `json:"content"`
}

func (a *API) validatePipeline(w http.ResponseWriter, r *http.Request) {
	var request validatePipelineRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if strings.TrimSpace(request.Content) == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid pipeline", "content is required")
		return
	}
	plan, err := pipeline.Compile([]byte(request.Content), time.Now())
	if err != nil {
		var validationErrors pipeline.ValidationErrors
		if errors.As(err, &validationErrors) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"valid": false, "errors": validationErrors})
			return
		}
		var validationError pipeline.ValidationError
		if errors.As(err, &validationError) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"valid": false, "errors": []pipeline.ValidationError{validationError}})
			return
		}
		writeProblem(w, http.StatusUnprocessableEntity, "invalid pipeline", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "plan": plan, "errors": []any{}})
}

type createRunRequest struct {
	ProjectID string `json:"project_id"`
	Pipeline  string `json:"pipeline"`
	Event     string `json:"event"`
	Ref       string `json:"ref"`
	Commit    string `json:"commit_sha"`
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	var request createRunRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if request.Event == "" {
		request.Event = "manual"
	}
	var projectID uuid.UUID
	if a.authorized != nil {
		var err error
		projectID, err = uuid.Parse(request.ProjectID)
		if err != nil || projectID == uuid.Nil || request.Event != "manual" {
			writeProblem(w, http.StatusUnprocessableEntity, "invalid run", "project_id is required and browser runs must use the manual event")
			return
		}
	}
	plan, err := pipeline.Compile([]byte(request.Pipeline), time.Now())
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid pipeline", err.Error())
		return
	}
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal error", "could not encode execution plan")
		return
	}
	record := runmodel.Record{
		ID: uuid.New(), PipelineName: plan.Name, Event: request.Event, Ref: request.Ref,
		CommitSHA: request.Commit, Status: runmodel.StatusQueued, ConfigSHA256: plan.ConfigSHA256,
		Plan: encodedPlan, CreatedAt: time.Now().UTC(),
	}
	if a.authorized != nil {
		record, err = a.authorized.CreateAuthorizedRun(r.Context(), browserFrom(r).token, projectID, record)
	} else {
		record, err = a.store.Create(r.Context(), record)
	}
	if err != nil {
		if accessError(w, err) {
			return
		}
		a.logger.Error("create run", "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal error", "could not create run")
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var records []runmodel.Record
	var err error
	if a.authorized != nil {
		projectID, parseErr := uuid.Parse(r.URL.Query().Get("project_id"))
		if parseErr != nil || projectID == uuid.Nil || len(r.URL.Query()["project_id"]) != 1 {
			writeProblem(w, http.StatusBadRequest, "invalid project", "one project_id is required")
			return
		}
		records, err = a.authorized.ListAuthorizedRuns(r.Context(), browserFrom(r).token, projectID, limit)
	} else {
		records, err = a.store.List(r.Context(), limit)
	}
	if err != nil {
		if accessError(w, err) {
			return
		}
		a.logger.Error("list runs", "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal error", "could not list runs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": records})
}

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = randomID()
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, a.bodyLimit)
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("request panic", "request_id", requestID)
				writeProblem(w, http.StatusInternalServerError, "internal error", "the request could not be completed")
			}
			a.logger.Info("request", "request_id", requestID, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
		}()
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(r.Header.Values("Content-Type")) != 1 {
		writeProblem(w, http.StatusUnsupportedMediaType, "unsupported media type", "Content-Type must be application/json")
		return errors.New("unsupported content type")
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeDecodeError(w, err)
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		writeDecodeError(w, err)
		return err
	}
	return nil
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeProblem(w, http.StatusRequestEntityTooLarge, "request too large", "request body exceeds the limit")
		return
	}
	writeProblem(w, http.StatusBadRequest, "invalid request", "body must contain one valid JSON object with known fields")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}

func randomID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return uuid.NewString()
	}
	return hex.EncodeToString(value[:])
}
