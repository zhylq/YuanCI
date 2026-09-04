package httpapi

import (
	"errors"
	"github.com/google/uuid"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"net/http"
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
