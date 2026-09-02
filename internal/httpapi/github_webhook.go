package httpapi

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/integration"
	"github.com/yuanci/yuanci/internal/scm"
)

func (a *API) receiveGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(r.Header.Values("Content-Type")) != 1 {
		writeProblem(w, http.StatusUnsupportedMediaType, "unsupported media type", "Content-Type must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, githubhook.MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, http.StatusRequestEntityTooLarge, "request too large", "GitHub webhook body exceeds 2 MiB")
			return
		}
		writeProblem(w, http.StatusBadRequest, "invalid webhook", "could not read GitHub webhook body")
		return
	}
	receipt, err := a.githubHooks.Receive(r.Context(), r.Header, body)
	clear(body)
	if err == nil {
		writeJSON(w, http.StatusAccepted, receipt)
		return
	}
	switch {
	case errors.Is(err, scm.ErrInvalidHook):
		writeProblem(w, http.StatusUnauthorized, "invalid webhook signature", "GitHub webhook authentication failed")
	case errors.Is(err, scm.ErrUnsupportedEvent):
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
	case errors.Is(err, githubhook.ErrConflict):
		writeProblem(w, http.StatusConflict, "delivery conflict", "GitHub delivery ID was already used for different content")
	case errors.Is(err, githubhook.ErrRateLimited):
		w.Header().Set("Retry-After", "1")
		writeProblem(w, http.StatusTooManyRequests, "webhook rate limited", "GitHub webhook traffic temporarily exceeds the safe limit")
	case errors.Is(err, githubhook.ErrInvalidRequest):
		writeProblem(w, http.StatusBadRequest, "invalid webhook", "GitHub webhook fields are invalid")
	case errors.Is(err, integration.ErrWebhookUnavailable), errors.Is(err, integration.ErrConfig):
		writeProblem(w, http.StatusServiceUnavailable, "webhook unavailable", "GitHub webhook verification is not configured")
	default:
		a.logger.Error("receive GitHub webhook", "error", err)
		writeProblem(w, http.StatusServiceUnavailable, "webhook unavailable", "GitHub webhook could not be recorded")
	}
}
