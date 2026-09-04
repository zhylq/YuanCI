package httpapi

import (
	"errors"
	"github.com/google/uuid"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"net/http"
	"net/url"
	"strconv"
)

func (a *API) cancelRun(w http.ResponseWriter, r *http.Request) {
	project, err := projectID(r)
	if err != nil {
		accessError(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("runID"))
	if err != nil {
		writeProblem(w, 400, "invalid run", "invalid Run identifier")
		return
	}
	store, ok := a.authorized.(runmodel.CancellationStore)
	if !ok {
		writeProblem(w, 503, "unavailable", "Run cancellation unavailable")
		return
	}
	ctx, cancel := contextWithTimeout(r, 10e9)
	defer cancel()
	result, err := store.CancelAuthorizedRun(ctx, browserFrom(r).token, project, id)
	if err != nil {
		if !accessError(w, err) {
			writeProblem(w, 503, "unavailable", "could not cancel Run")
		}
		return
	}
	writeJSON(w, 200, map[string]any{"status": result})
}

func (a *API) runDetail(w http.ResponseWriter, r *http.Request) {
	project, err := projectID(r)
	if err != nil {
		accessError(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("runID"))
	if err != nil {
		writeProblem(w, 400, "invalid run", "invalid Run identifier")
		return
	}
	store, ok := a.authorized.(runmodel.DetailStore)
	if !ok {
		writeProblem(w, 503, "unavailable", "Run detail unavailable")
		return
	}
	ctx, cancel := contextWithTimeout(r, 10e9)
	defer cancel()
	result, err := store.GetAuthorizedRun(ctx, browserFrom(r).token, project, id)
	if err != nil {
		if !accessError(w, err) {
			writeProblem(w, 503, "unavailable", "could not read Run")
		}
		return
	}
	writeJSON(w, 200, result)
}
func (a *API) runLogs(w http.ResponseWriter, r *http.Request) {
	project, err := projectID(r)
	if err != nil {
		accessError(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("runID"))
	if err != nil {
		writeProblem(w, 400, "invalid run", "invalid Run identifier")
		return
	}
	job, err := uuid.Parse(r.PathValue("jobID"))
	if err != nil {
		writeProblem(w, 400, "invalid job", "invalid Job identifier")
		return
	}
	after := int64(0)
	values, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil || len(values) > 1 || len(values["after"]) > 1 || (len(values) == 1 && len(values["after"]) == 0) {
		writeProblem(w, 400, "invalid cursor", "invalid log cursor")
		return
	}
	if value := values.Get("after"); value != "" {
		after, err = strconv.ParseInt(value, 10, 64)
	}
	if err != nil || after < 0 || after > runmodel.MaxJobLogChunks {
		writeProblem(w, 400, "invalid cursor", "invalid log cursor")
		return
	}
	store, ok := a.authorized.(runmodel.DetailStore)
	if !ok {
		writeProblem(w, 503, "unavailable", "Run logs unavailable")
		return
	}
	ctx, cancel := contextWithTimeout(r, 10e9)
	defer cancel()
	result, err := store.ReadAuthorizedLogs(ctx, browserFrom(r).token, project, id, job, after)
	if err != nil {
		if !accessError(w, err) {
			writeProblem(w, 503, "unavailable", "could not read logs")
		}
		return
	}
	writeJSON(w, 200, result)
}

func (a *API) rerun(w http.ResponseWriter, r *http.Request) {
	project, err := projectID(r)
	if err != nil {
		accessError(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("runID"))
	if err != nil {
		writeProblem(w, 400, "invalid run", "invalid Run identifier")
		return
	}
	key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil || key == uuid.Nil {
		writeProblem(w, 400, "invalid request", "Idempotency-Key must be a UUID")
		return
	}
	var input struct {
		Mode string `json:"mode"`
	}
	if decodeJSON(w, r, &input) != nil {
		return
	}
	store, ok := a.authorized.(runmodel.RerunStore)
	if !ok {
		writeProblem(w, 503, "unavailable", "Run rerun unavailable")
		return
	}
	ctx, cancel := contextWithTimeout(r, 10e9)
	defer cancel()
	result, err := store.RerunAuthorizedRun(ctx, browserFrom(r).token, project, id, input.Mode, key)
	if err != nil {
		if errors.Is(err, runmodel.ErrRunConflict) {
			writeProblem(w, 409, "Run cannot be rerun", "rerun requires a terminal Run with executable Jobs and a valid mode")
		} else if !accessError(w, err) {
			writeProblem(w, 503, "unavailable", "could not rerun")
		}
		return
	}
	writeJSON(w, 201, result)
}
