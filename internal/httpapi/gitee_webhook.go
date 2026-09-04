package httpapi

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/scm"
)

func (a *API) receiveGiteeWebhook(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || len(r.Header.Values("Authorization")) != 0 {
		writeProblem(w, 400, "invalid webhook", "Expected a Gitee JSON webhook.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	defer clear(body)
	if err != nil {
		writeProblem(w, 413, "webhook too large", "Webhook body exceeds the limit.")
		return
	}
	receipt, err := a.oauth.Gitee.ReceiveWebhook(r.Context(), r.PathValue("repositoryID"), r.Header, body)
	switch {
	case err == nil:
		writeJSON(w, 202, receipt)
	case errors.Is(err, scm.ErrUnsupportedEvent):
		writeJSON(w, 202, map[string]bool{"ignored": true})
	case errors.Is(err, scm.ErrInvalidHook):
		writeProblem(w, 401, "invalid webhook", "Webhook authentication or repository binding failed.")
	case errors.Is(err, githubhook.ErrRateLimited):
		w.Header().Set("Retry-After", "1")
		writeProblem(w, 429, "webhook rate limited", "Retry later.")
	case errors.Is(err, githubhook.ErrConflict):
		writeProblem(w, 409, "webhook conflict", "Delivery identity conflicts with existing content.")
	default:
		writeProblem(w, 503, "webhook unavailable", "Could not persist the webhook.")
	}
}
func (a *API) giteeWebhookSettings(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		projectError(w, err)
		return
	}
	url, revision, err := a.oauth.Gitee.WebhookSettings(r.Context(), browserFrom(r).token, id)
	if err != nil {
		giteeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"webhook_url": url, "revision": revision, "configured": revision > 0, "mode": "password"})
}
func (a *API) saveGiteeWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		projectError(w, err)
		return
	}
	var input struct {
		Secret   string `json:"secret"`
		Expected int64  `json:"expected_revision"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	secret := []byte(input.Secret)
	defer clear(secret)
	if err := a.oauth.Gitee.SaveWebhook(r.Context(), browserFrom(r).token, id, input.Expected, secret); err != nil {
		automationError(w, err)
		return
	}
	w.WriteHeader(204)
}
