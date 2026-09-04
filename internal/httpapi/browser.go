package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/project"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/webui"
)

type BrowserBackend interface {
	identity.Sessions
	runmodel.AuthorizedStore
	project.Store
}

// NewAuthenticated is the protected browser surface. It intentionally has no
// legacy Runner routes or public session issuer. OAuth/runtime activation is a
// separate gate; NewEvaluation must never be mounted alongside this handler.
func NewAuthenticated(logger *slog.Logger, store runmodel.Store, backend BrowserBackend, bodyLimit int64, publicOrigin string, login ...GitHubLogin) (http.Handler, error) {
	u, err := url.Parse(publicOrigin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("authenticated API requires a canonical HTTPS public origin")
	}
	if backend == nil || store == nil || logger == nil || bodyLimit < 1024 {
		return nil, errors.New("authenticated API requires all dependencies and a request limit")
	}
	a := &API{logger: logger, store: store, sessions: backend, authorized: backend, projects: backend, bodyLimit: bodyLimit, origin: u.Scheme + "://" + u.Host, startedAt: time.Now().UTC()}
	a.automation, _ = backend.(project.AutomationStore)
	if len(login) > 1 || (len(login) == 1 && (login[0].Store == nil || (login[0].Provider == nil && login[0].Managed == nil) || (login[0].Provider != nil && login[0].Managed != nil))) {
		return nil, errors.New("login requires one complete provider and flow store")
	}
	mux := http.NewServeMux()
	if len(login) == 1 {
		a.oauth = &login[0]
		if (a.oauth.Pipeline != nil || a.oauth.Gitee != nil) && a.automation == nil {
			return nil, errors.New("GitHub automation validation requires an automation store")
		}
		mux.HandleFunc("GET /api/v1/auth/github/start", a.startLogin)
		mux.HandleFunc("GET /api/v1/auth/github/callback", a.finishLogin)
		mux.HandleFunc("POST /api/v1/auth/github/link", a.browserAuth(a.linkIdentity))
		if a.oauth.Managed != nil {
			if a.oauth.Gitee != nil {
				mux.HandleFunc("POST /api/v1/webhooks/gitee/{repositoryID}", a.receiveGiteeWebhook)
				mux.HandleFunc("GET /api/v1/projects/{projectID}/gitee/webhook", a.browserAuth(a.giteeWebhookSettings))
				mux.HandleFunc("PUT /api/v1/projects/{projectID}/gitee/webhook", a.browserAuth(a.saveGiteeWebhook))
				mux.HandleFunc("GET /api/v1/integrations/gitee", a.browserAuth(a.giteeSettings))
				mux.HandleFunc("DELETE /api/v1/integrations/gitee", a.browserAuth(a.revokeGitee))
				mux.HandleFunc("POST /api/v1/integrations/gitee/authorize", a.browserAuth(a.authorizeGitee))
				mux.HandleFunc("GET /api/v1/integrations/gitee/repositories", a.browserAuth(a.giteeRepositories))
				mux.HandleFunc("POST /api/v1/integrations/gitee/import", a.browserAuth(a.importGitee))
			}
			mux.HandleFunc("GET /api/v1/auth/gitee/start", a.startLogin)
			mux.HandleFunc("GET /api/v1/auth/gitee/callback", a.finishLogin)
			mux.HandleFunc("POST /api/v1/auth/gitee/link", a.browserAuth(a.linkIdentity))
			mux.HandleFunc("POST /api/v1/settings/auth", a.browserAuth(a.saveLoginSettings))
			mux.HandleFunc("POST /api/v1/settings/auth/verify", a.browserAuth(a.verifyLoginSettings))
			mux.HandleFunc("POST /api/v1/setup/exchange", a.exchangeSetup)
			mux.HandleFunc("GET /api/v1/setup/settings", a.setupAccess(a.loginSettings))
			mux.HandleFunc("POST /api/v1/setup/settings", a.setupAccess(a.saveLoginSettings))
			mux.HandleFunc("POST /api/v1/setup/verify", a.setupAccess(a.verifyLoginSettings))
			mux.HandleFunc("GET /api/v1/settings/auth", a.browserAuth(a.loginSettings))
			mux.HandleFunc("POST /api/v1/settings/auth/github", a.browserAuth(a.saveLoginSettings))
			mux.HandleFunc("POST /api/v1/settings/auth/github/verify", a.browserAuth(a.verifyLoginSettings))
			if a.oauth.Integrations != nil {
				hookStore, ok := store.(githubhook.Store)
				if !ok {
					return nil, errors.New("managed GitHub integration requires a webhook inbox")
				}
				a.githubHooks, err = githubhook.New(a.oauth.Integrations, hookStore)
				if err != nil {
					return nil, err
				}
				mux.HandleFunc("POST /api/v1/webhooks/github", a.receiveGitHubWebhook)
				mux.HandleFunc("GET /api/v1/integrations/github", a.browserAuth(a.integrationSettings))
				mux.HandleFunc("POST /api/v1/integrations/github", a.browserAuth(a.saveIntegration))
				mux.HandleFunc("POST /api/v1/integrations/github/authorize", a.browserAuth(a.authorizeIntegration))
				mux.HandleFunc("GET /api/v1/integrations/github/callback", a.finishIntegration)
				mux.HandleFunc("GET /api/v1/integrations/github/installations", a.browserAuth(a.integrationInstallations))
				mux.HandleFunc("GET /api/v1/integrations/github/installations/{installationID}/repositories", a.browserAuth(a.integrationRepositories))
				mux.HandleFunc("POST /api/v1/integrations/github/import", a.browserAuth(a.importRepositories))
			}
		}
	}
	mux.HandleFunc("GET /api/v1/auth/status", a.authStatus)
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /readyz", a.ready)
	mux.HandleFunc("GET /api/v1/system/info", a.browserAuth(a.systemInfo))
	mux.HandleFunc("GET /api/v1/session", a.browserAuth(a.sessionInfo))
	mux.HandleFunc("GET /api/v1/projects", a.browserAuth(a.listProjects))
	mux.HandleFunc("GET /api/v1/projects/{projectID}", a.browserAuth(a.projectDetail))
	mux.HandleFunc("GET /api/v1/projects/{projectID}/runs", a.browserAuth(a.projectRuns))
	mux.HandleFunc("POST /api/v1/projects/{projectID}/runs/{runID}/cancel", a.browserAuth(a.cancelRun))
	mux.HandleFunc("POST /api/v1/projects/{projectID}/runs/{runID}/rerun", a.browserAuth(a.rerun))
	mux.HandleFunc("GET /api/v1/projects/{projectID}/runs/{runID}", a.browserAuth(a.runDetail))
	mux.HandleFunc("GET /api/v1/projects/{projectID}/runs/{runID}/jobs/{jobID}/logs", a.browserAuth(a.runLogs))
	if a.automation != nil {
		mux.HandleFunc("GET /api/v1/projects/{projectID}/automation", a.browserAuth(a.projectAutomation))
		mux.HandleFunc("PUT /api/v1/projects/{projectID}/automation", a.browserAuth(a.updateProjectAutomation))
		if a.oauth != nil && (a.oauth.Pipeline != nil || a.oauth.Gitee != nil) {
			mux.HandleFunc("POST /api/v1/projects/{projectID}/pipeline/validate", a.browserAuth(a.validateProjectAutomation))
		}
	}
	mux.HandleFunc("DELETE /api/v1/session", a.browserAuth(a.logout))
	mux.HandleFunc("POST /api/v1/pipelines/validate", a.browserAuth(a.validatePipeline))
	mux.HandleFunc("GET /api/v1/runs", a.browserAuth(a.listRuns))
	mux.HandleFunc("POST /api/v1/runs", a.browserAuth(a.createRun))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusNotFound, "not found", "API endpoint does not exist")
	})
	mux.Handle("/", webui.Handler())
	return a.middleware(mux), nil
}

type browserKey struct{}
type browserIdentity struct {
	token   string
	session identity.Session
}

func browserFrom(r *http.Request) browserIdentity {
	value, _ := r.Context().Value(browserKey{}).(browserIdentity)
	return value
}

func (a *API) browserAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookies := r.CookiesNamed(identity.CookieName)
		if len(cookies) != 1 || len(r.Header.Values("Authorization")) != 0 {
			writeProblem(w, http.StatusUnauthorized, "unauthorized", "a browser session is required")
			return
		}
		token := cookies[0].Value
		if _, err := identity.TokenDigest(token); err != nil {
			accessError(w, err)
			return
		}
		session, err := a.sessions.AuthenticateSession(r.Context(), token)
		if err != nil {
			if !accessError(w, err) {
				writeProblem(w, http.StatusServiceUnavailable, "authentication unavailable", "could not validate session")
			}
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != a.origin ||
				len(r.Header.Values("X-CSRF-Token")) != 1 || !identity.ValidCSRF(token, r.Header.Get("X-CSRF-Token")) {
				writeProblem(w, http.StatusForbidden, "forbidden", "origin or CSRF validation failed")
				return
			}
		}
		ctx := context.WithValue(r.Context(), browserKey{}, browserIdentity{token: token, session: session})
		next(w, r.WithContext(ctx))
	}
}

func (a *API) sessionInfo(w http.ResponseWriter, r *http.Request) {
	browser := browserFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{"user_id": browser.session.UserID, "display_name": browser.session.DisplayName,
		"expires_at": browser.session.ExpiresAt, "csrf_token": identity.CSRFToken(browser.token)})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.sessions.RevokeSession(r.Context(), browserFrom(r).token); err != nil {
		if !accessError(w, err) {
			writeProblem(w, http.StatusServiceUnavailable, "logout unavailable", "could not revoke session")
		}
		return
	}
	http.SetCookie(w, identity.ExpiredCookie())
	w.WriteHeader(http.StatusNoContent)
}

func accessError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, identity.ErrUnauthenticated) {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "session is invalid or expired")
		return true
	}
	if errors.Is(err, authorization.ErrForbidden) {
		writeProblem(w, http.StatusForbidden, "forbidden", "access denied")
		return true
	}
	return false
}
