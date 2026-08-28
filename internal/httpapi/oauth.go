package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/yuanci/yuanci/internal/identity"
)

// GitHubLogin is installed only on the authenticated surface, never evaluation.
type GitHubLogin struct {
	Store    identity.OAuthStore
	Provider identity.OAuthProvider
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
	if err := a.oauth.Store.BeginOAuth(r.Context(), state, nonce, linkToken); err != nil {
		oauthError(w, err)
		return
	}
	http.SetCookie(w, identity.FlowCookie(nonce))
	target := a.oauth.Provider.AuthorizationURL(state, identity.PKCEVerifier(state, nonce))
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
	if err != nil || len(query["state"]) != 1 || len(query["code"]) > 1 || len(query["error"]) > 1 ||
		len(flows) != 1 || len(sessions) > 1 || len(r.Header.Values("Authorization")) != 0 {
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
	user, err := a.oauth.Provider.Exchange(r.Context(), code, identity.PKCEVerifier(state, nonce))
	if err != nil {
		oauthError(w, identity.ErrProvider)
		return
	}
	credentials, err := a.oauth.Store.FinishOAuth(r.Context(), ticket, user, currentToken)
	if err != nil {
		oauthError(w, err)
		return
	}
	http.SetCookie(w, identity.SessionCookie(credentials))
	// Fixed landing route; never accept a browser-supplied redirect destination.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func oauthError(w http.ResponseWriter, err error) {
	if accessError(w, err) {
		return
	}
	switch {
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
