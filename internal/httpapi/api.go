package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/buildinfo"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/webui"
)

type API struct {
	logger      *slog.Logger
	store       runmodel.Store
	bodyLimit   int64
	runnerToken string
	startedAt   time.Time
}

func New(logger *slog.Logger, store runmodel.Store, bodyLimit int64, runnerToken string) http.Handler {
	api := &API{logger: logger, store: store, bodyLimit: bodyLimit, runnerToken: runnerToken, startedAt: time.Now().UTC()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("GET /readyz", api.ready)
	mux.HandleFunc("GET /api/v1/system/info", api.systemInfo)
	mux.HandleFunc("POST /api/v1/pipelines/validate", api.validatePipeline)
	mux.HandleFunc("GET /api/v1/runs", api.listRuns)
	mux.HandleFunc("POST /api/v1/runs", api.createRun)
	mux.HandleFunc("POST /api/v1/runner/jobs/claim", api.runnerAuth(api.claimJob))
	mux.HandleFunc("POST /api/v1/runner/jobs/{jobID}/start", api.runnerAuth(api.startJob))
	mux.HandleFunc("POST /api/v1/runner/jobs/{jobID}/complete", api.runnerAuth(api.completeJob))
	mux.Handle("/", webui.Handler())
	return api.middleware(mux)
}

func (a *API) claimJob(w http.ResponseWriter, r *http.Request) {
	var request runmodel.ClaimRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if strings.TrimSpace(request.RunnerName) == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid runner", "runner_name is required")
		return
	}
	assignment, err := a.store.ClaimJob(r.Context(), request)
	if err != nil {
		a.logger.Error("claim job", "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal error", "could not claim a job")
		return
	}
	if assignment == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, assignment)
}

type leaseRequest struct {
	LeaseToken string `json:"lease_token"`
}
type completeJobRequest struct {
	LeaseToken string             `json:"lease_token"`
	Status     runmodel.JobStatus `json:"status"`
}

func (a *API) startJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("jobID"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid job", "jobID must be a UUID")
		return
	}
	var request leaseRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if err := a.store.StartJob(r.Context(), id, request.LeaseToken); err != nil {
		if errors.Is(err, runmodel.ErrLeaseInvalid) {
			writeProblem(w, http.StatusConflict, "invalid lease", err.Error())
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal error", "could not start job")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) completeJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("jobID"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid job", "jobID must be a UUID")
		return
	}
	var request completeJobRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if err := a.store.CompleteJob(r.Context(), id, request.LeaseToken, request.Status); err != nil {
		if errors.Is(err, runmodel.ErrLeaseInvalid) {
			writeProblem(w, http.StatusConflict, "invalid lease", err.Error())
			return
		}
		writeProblem(w, http.StatusUnprocessableEntity, "invalid completion", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) runnerAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.runnerToken == "" {
			writeProblem(w, http.StatusServiceUnavailable, "runner protocol disabled", "runner authentication is not configured")
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		expectedDigest := sha256.Sum256([]byte(a.runnerToken))
		providedDigest := sha256.Sum256([]byte(provided))
		if subtle.ConstantTimeCompare(expectedDigest[:], providedDigest[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, http.StatusUnauthorized, "unauthorized", "runner token is invalid")
			return
		}
		next(w, r)
	}
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
	Pipeline string `json:"pipeline"`
	Event    string `json:"event"`
	Ref      string `json:"ref"`
	Commit   string `json:"commit_sha"`
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	var request createRunRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if request.Event == "" {
		request.Event = "manual"
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
	record, err = a.store.Create(r.Context(), record)
	if err != nil {
		a.logger.Error("create run", "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal error", "could not create run")
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	records, err := a.store.List(r.Context(), limit)
	if err != nil {
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
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("request panic", "request_id", requestID, "panic", recovered)
				writeProblem(w, http.StatusInternalServerError, "internal error", "the request could not be completed")
			}
			a.logger.Info("request", "request_id", requestID, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
		}()
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeProblem(w, http.StatusUnsupportedMediaType, "unsupported media type", "Content-Type must be application/json")
		return errors.New("unsupported content type")
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid request", err.Error())
		return err
	}
	return nil
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
