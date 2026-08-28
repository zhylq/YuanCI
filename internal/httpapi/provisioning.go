package httpapi

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
)

func (a *API) authStatus(w http.ResponseWriter, r *http.Request) {
	mode := "evaluation"
	configured := false
	initialized := false
	if a.sessions != nil {
		mode = "file"
		configured = a.oauth != nil
	}
	if a.oauth != nil && a.oauth.Managed != nil {
		mode = "managed"
		status, err := a.oauth.Managed.Repo.ProvisioningStatus(r.Context())
		if err != nil {
			writeProblem(w, 503, "status unavailable", "无法读取初始化状态。")
			return
		}
		configured, initialized = status.Configured, status.Initialized
	}
	writeJSON(w, 200, map[string]any{"mode": mode, "configured": configured, "initialized": initialized, "callback_url": a.origin + "/api/v1/auth/github/callback"})
}
func (a *API) validOrigin(r *http.Request) bool {
	return len(r.Header.Values("Origin")) == 1 && r.Header.Get("Origin") == a.origin
}
func (a *API) exchangeSetup(w http.ResponseWriter, r *http.Request) {
	if !a.validOrigin(r) || len(r.Header.Values("Authorization")) != 0 {
		writeProblem(w, 403, "forbidden", "来源校验失败。")
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	token, err := a.oauth.Managed.Exchange(r.Context(), input.Code)
	if err != nil {
		oauthError(w, err)
		return
	}
	http.SetCookie(w, provisioning.Cookie(token))
	writeJSON(w, 200, map[string]string{"csrf_token": identity.CSRFToken(token)})
}

type setupKey struct{}

func (a *API) setupAccess(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookies := r.CookiesNamed(provisioning.CookieName)
		if len(cookies) != 1 || len(r.Header.Values("Authorization")) != 0 {
			oauthError(w, provisioning.ErrSetup)
			return
		}
		token := cookies[0].Value
		if _, err := identity.TokenDigest(token); err != nil {
			oauthError(w, provisioning.ErrSetup)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !a.validOrigin(r) || len(r.Header.Values("X-CSRF-Token")) != 1 || !identity.ValidCSRF(token, r.Header.Get("X-CSRF-Token")) {
				writeProblem(w, 403, "forbidden", "来源或 CSRF 校验失败。")
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), setupKey{}, token)))
	}
}
func settingsFrom(r *http.Request) provisioning.Access {
	if token, ok := r.Context().Value(setupKey{}).(string); ok {
		return provisioning.Access{SetupToken: token}
	}
	return provisioning.Access{SessionToken: browserFrom(r).token}
}
func (a *API) loginSettings(w http.ResponseWriter, r *http.Request) {
	access := settingsFrom(r)
	settings, err := a.oauth.Managed.Repo.LoginSettings(r.Context(), access)
	if err != nil {
		oauthError(w, err)
		return
	}
	token := access.SessionToken
	if access.SetupToken != "" {
		token = access.SetupToken
	}
	writeJSON(w, 200, map[string]any{"active": settings.Active, "candidate": settings.Candidate, "csrf_token": identity.CSRFToken(token), "callback_url": a.oauth.Managed.Callback})
}
func (a *API) saveLoginSettings(w http.ResponseWriter, r *http.Request) {
	var input provisioning.Input
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	id, err := a.oauth.Managed.Save(r.Context(), settingsFrom(r), input)
	if err != nil {
		oauthError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"candidate_id": id})
}
func (a *API) verifyLoginSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID uuid.UUID `json:"candidate_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	if input.ID == uuid.Nil {
		oauthError(w, provisioning.ErrConfig)
		return
	}
	state, nonce := identity.NewToken(), identity.NewToken()
	provider, err := a.oauth.Managed.Start(r.Context(), state, nonce, "", input.ID, settingsFrom(r))
	if err != nil {
		oauthError(w, err)
		return
	}
	http.SetCookie(w, identity.FlowCookie(nonce))
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, 200, map[string]string{"authorization_url": provider.AuthorizationURL(state, identity.PKCEVerifier(state, nonce))})
}
