package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/google/uuid"

	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
)

// GitHubLogin is installed only on the authenticated surface, never evaluation.
type GitHubLogin struct {
	Store    identity.OAuthStore
	Provider identity.OAuthProvider
	Managed  *provisioning.Service
}

func (a *API) startLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	a.beginLogin(w, r, "")
}

func (a *API) linkIdentity(w http.ResponseWriter, r *http.Request) {
	a.beginLogin(w, r, browserFrom(r).token)
}

func (a *API) beginLogin(w http.ResponseWriter, r *http.Request, linkToken string) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	state, nonce := identity.NewToken(), identity.NewToken()
	provider := a.oauth.Provider
	var err error
	if a.oauth.Managed != nil {
		provider, err = a.oauth.Managed.Start(r.Context(), state, nonce, linkToken, uuid.Nil, provisioning.Access{})
	} else {
		err = a.oauth.Store.BeginOAuth(r.Context(), state, nonce, linkToken)
	}
	if err != nil {
		oauthError(w, err)
		return
	}
	http.SetCookie(w, identity.FlowCookie(nonce))
	target := provider.AuthorizationURL(state, identity.PKCEVerifier(state, nonce))
	if linkToken != "" {
		writeJSON(w, http.StatusOK, map[string]string{"authorization_url": target})
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *API) finishLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	flows := r.CookiesNamed(identity.FlowCookieName)
	sessions := r.CookiesNamed(identity.CookieName)
	setupCookies := r.CookiesNamed(provisioning.CookieName)
	if err != nil || len(query["state"]) != 1 || len(query["code"]) > 1 || len(query["error"]) > 1 ||
		len(flows) != 1 || len(sessions) > 1 || len(setupCookies) > 1 || len(r.Header.Values("Authorization")) != 0 {
		oauthError(w, identity.ErrOAuthFlow)
		return
	}
	state, nonce := query.Get("state"), flows[0].Value
	if _, err := identity.TokenDigest(state); err != nil {
		oauthError(w, identity.ErrOAuthFlow)
		return
	}
	if _, err := identity.TokenDigest(nonce); err != nil {
		oauthError(w, identity.ErrOAuthFlow)
		return
	}
	currentToken := ""
	if len(sessions) == 1 {
		currentToken = sessions[0].Value
		if _, err := identity.TokenDigest(currentToken); err != nil {
			oauthError(w, identity.ErrOAuthFlow)
			return
		}
	}
	// Consume before any upstream request: replay cannot exchange a code twice.
	ticket, err := a.oauth.Store.ConsumeOAuth(r.Context(), state, nonce)
	if err != nil {
		oauthError(w, err)
		return
	}
	cookie := identity.FlowCookie("")
	cookie.MaxAge = -1
	http.SetCookie(w, cookie)
	code := query.Get("code")
	if len(query["error"]) != 0 || code == "" || len(code) > 1024 {
		oauthError(w, identity.ErrOAuthFlow)
		return
	}
	provider := a.oauth.Provider
	if a.oauth.Managed != nil {
		provider, err = a.oauth.Managed.Provider(r.Context(), ticket)
		if err != nil {
			oauthError(w, err)
			return
		}
	}
	user, err := provider.Exchange(r.Context(), code, identity.PKCEVerifier(state, nonce))
	if err != nil {
		oauthError(w, identity.ErrProvider)
		return
	}
	var credentials identity.Credentials
	if a.oauth.Managed != nil {
		setupToken := ""
		if len(setupCookies) == 1 {
			setupToken = setupCookies[0].Value
		}
		credentials, err = a.oauth.Managed.Repo.FinishManagedOAuth(r.Context(), ticket, user, currentToken, setupToken)
	} else {
		credentials, err = a.oauth.Store.FinishOAuth(r.Context(), ticket, user, currentToken)
	}
	if err != nil {
		oauthError(w, err)
		return
	}
	http.SetCookie(w, identity.SessionCookie(credentials))
	if a.oauth.Managed != nil {
		expired := provisioning.Cookie("")
		expired.MaxAge = -1
		http.SetCookie(w, expired)
	}
	// Fixed landing route; never accept a browser-supplied redirect destination.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func oauthError(w http.ResponseWriter, err error) {
	if accessError(w, err) {
		return
	}
	switch {
	case errors.Is(err, provisioning.ErrSetup):
		writeProblem(w, http.StatusUnauthorized, "setup unavailable", "设置码或设置会话无效、已过期，或初始化已完成。")
	case errors.Is(err, provisioning.ErrConfig):
		writeProblem(w, http.StatusUnprocessableEntity, "invalid configuration", "请检查 Client ID、Client Secret、管理员数字 ID 和主密钥配置。")
	case errors.Is(err, provisioning.ErrConflict):
		writeProblem(w, http.StatusConflict, "configuration changed", "配置未启用、已过期或被更新，请刷新设置并重新验证。")
	case errors.Is(err, identity.ErrOAuthFlow):
		writeProblem(w, http.StatusBadRequest, "invalid login", "login flow is invalid or expired; start again")
	case errors.Is(err, identity.ErrIdentityConflict):
		writeProblem(w, http.StatusConflict, "identity conflict", "identity is already linked to another account")
	case errors.Is(err, identity.ErrProvider):
		writeProblem(w, http.StatusBadGateway, "provider unavailable", "could not verify GitHub identity; start again")
	case errors.Is(err, identity.ErrFlowCapacity):
		w.Header().Set("Retry-After", "60")
		writeProblem(w, http.StatusTooManyRequests, "login capacity reached", "try again later")
	default:
		writeProblem(w, http.StatusServiceUnavailable, "login unavailable", "could not complete login")
	}
}
