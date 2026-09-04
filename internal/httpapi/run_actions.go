package httpapi

import (
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
