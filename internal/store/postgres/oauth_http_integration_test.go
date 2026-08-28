package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
)

type loginProvider struct {
	calls     atomic.Int32
	verifiers sync.Map
}

func (p *loginProvider) AuthorizationURL(state, verifier string) string {
	p.verifiers.Store(verifier, true)
	return "https://github.com/login/oauth/authorize?" + url.Values{"state": {state}, "code_challenge": {identity.PKCEChallenge(verifier)}}.Encode()
}

func (p *loginProvider) Exchange(_ context.Context, code, verifier string) (identity.ExternalUser, error) {
	p.calls.Add(1)
	if _, ok := p.verifiers.LoadAndDelete(verifier); !ok {
		return identity.ExternalUser{}, errors.New("incorrect PKCE verifier")
	}
	if _, err := identity.TokenDigest(verifier); err != nil || code == "provider-failure-sensitive-code" {
		return identity.ExternalUser{}, errors.New("sensitive-provider-body")
	}
	return githubUser(code), nil
}

func loginHandler(t *testing.T, s *Store) (http.Handler, *loginProvider, *bytes.Buffer) {
	t.Helper()
	provider := &loginProvider{}
	logs := &bytes.Buffer{}
	h, err := httpapi.NewAuthenticated(slog.New(slog.NewJSONHandler(logs, nil)), s, s, 1<<20, "https://ci.example.test",
		httpapi.GitHubLogin{Store: s, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return h, provider, logs
}

func startHTTPLogin(t *testing.T, h http.Handler) (string, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/auth/github/start", nil))
	if w.Code != 303 {
		t.Fatalf("start status %d", w.Code)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Host != "github.com" {
		t.Fatal("unsafe authorization URL")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != identity.FlowCookieName || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Path != "/" || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].MaxAge != 300 {
		t.Fatal("unsafe flow cookie")
	}
	state := u.Query().Get("state")
	if u.Query().Get("code_challenge") != identity.PKCEChallenge(identity.PKCEVerifier(state, cookies[0].Value)) {
		t.Fatal("PKCE not browser-bound")
	}
	return state, cookies[0]
}

func callbackHTTP(h http.Handler, state, code string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/v1/auth/github/callback?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOAuthHTTPBootstrapCookiesAndReplay(t *testing.T) {
	s := oauthStore(t)
	h, provider, logs := loginHandler(t, s)
	state, flow := startHTTPLogin(t, h)
	w := callbackHTTP(h, state, "100", flow)
	if w.Code != 303 || w.Header().Get("Location") != "/" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("callback status %d, headers %v", w.Code, w.Header())
	}
	var session *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == identity.CookieName {
			session = cookie
		}
	}
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Path != "/" || session.Domain != "" {
		t.Fatal("unsafe session cookie")
	}
	r := httptest.NewRequest("GET", "/api/v1/session", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "csrf_token") || strings.Contains(w.Body.String(), session.Value) {
		t.Fatal("session API invalid")
	}
	if replay := callbackHTTP(h, state, "100", flow); replay.Code != 400 || provider.calls.Load() != 1 {
		t.Fatal("replay reached provider")
	}
	for _, secret := range []string{state, flow.Value, session.Value} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("credentials leaked in request logs")
		}
	}
	for _, path := range []string{"/api/v1/runner/jobs/claim", "/api/v1/runs"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader("{}")))
		want := 401
		if strings.Contains(path, "runner") {
			want = 404
		}
		if w.Code != want {
			t.Fatalf("unprotected route %s: %d", path, w.Code)
		}
	}
}

func TestOAuthHTTPRejectsMalformedAndFailedCallbacks(t *testing.T) {
	s := oauthStore(t)
	h, provider, logs := loginHandler(t, s)
	state, flow := startHTTPLogin(t, h)
	for _, cookies := range [][]*http.Cookie{nil, {identity.FlowCookie(identity.NewToken())}, {flow, flow}, {flow, {Name: identity.CookieName, Value: "invalid"}}} {
		if w := callbackHTTP(h, state, "100", cookies...); w.Code != 400 {
			t.Fatalf("bad cookies accepted %d", w.Code)
		}
	}
	for _, query := range []string{"state=" + state + "&state=" + state + "&code=100", "state=" + state + "&code=100&code=100", "state=" + state + "&code=%ZZ"} {
		r := httptest.NewRequest("GET", "/api/v1/auth/github/callback?"+query, nil)
		r.AddCookie(flow)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("malformed query accepted")
		}
	}
	if provider.calls.Load() != 0 {
		t.Fatal("invalid browser reached provider")
	}
	w := callbackHTTP(h, state, "provider-failure-sensitive-code", flow)
	if w.Code != 502 || strings.Contains(w.Body.String()+logs.String(), "sensitive") {
		t.Fatal("provider failure unsanitized")
	}
	if w := callbackHTTP(h, state, "100", flow); w.Code != 400 || provider.calls.Load() != 1 {
		t.Fatal("failed exchange was replayable")
	}
	state, flow = startHTTPLogin(t, h)
	if w := callbackHTTP(h, state, "200", flow); w.Code != 403 {
		t.Fatal("first visitor became administrator")
	}
	for _, method := range []string{"HEAD", "POST"} {
		for _, path := range []string{"/api/v1/auth/github/start", "/api/v1/auth/github/callback"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			want := 405
			if method == "POST" {
				want = 404
			} // Unknown API methods use the closed fallback.
			if w.Code != want {
				t.Fatalf("unexpected %s %s status %d", method, path, w.Code)
			}
		}
	}
}

func TestOAuthHTTPLinkRequiresCSRFAndSameSession(t *testing.T) {
	s := oauthStore(t)
	admin := oauthLogin(t, s, "100")
	member := oauthLogin(t, s, "200")
	h, _, _ := loginHandler(t, s)
	for _, csrf := range []string{"", identity.CSRFToken(member.Token)} {
		r := httptest.NewRequest("POST", "/api/v1/auth/github/link", nil)
		r.AddCookie(identity.SessionCookie(member))
		r.Header.Set("Origin", "https://ci.example.test")
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if csrf == "" {
			if w.Code != 403 {
				t.Fatal("link missing CSRF accepted")
			}
			continue
		}
		if w.Code != 200 {
			t.Fatalf("link start %d", w.Code)
		}
		var body struct {
			URL string `json:"authorization_url"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(body.URL)
		if err != nil {
			t.Fatal(err)
		}
		flow := w.Result().Cookies()[0]
		if w := callbackHTTP(h, u.Query().Get("state"), "300", flow, identity.SessionCookie(admin)); w.Code != 401 {
			t.Fatalf("session switch accepted %d", w.Code)
		}
	}
}

func TestOAuthHTTPConcurrentCallbackExchangesOnce(t *testing.T) {
	s := oauthStore(t)
	h, provider, _ := loginHandler(t, s)
	state, flow := startHTTPLogin(t, h)
	var wg sync.WaitGroup
	results := make(chan int, 10)
	for range 10 {
		wg.Go(func() { results <- callbackHTTP(h, state, "100", flow).Code })
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 303 {
			success++
		} else if status != 400 {
			t.Fatalf("unexpected concurrent status %d", status)
		}
	}
	if success != 1 || provider.calls.Load() != 1 {
		t.Fatal("concurrent callback replay")
	}
}

func TestOAuthHTTPCancellationAndSuccessfulLink(t *testing.T) {
	s := oauthStore(t)
	h, provider, _ := loginHandler(t, s)
	state, flow := startHTTPLogin(t, h)
	r := httptest.NewRequest("GET", "/api/v1/auth/github/callback?"+url.Values{"state": {state}, "error": {"access_denied"}, "error_description": {"sensitive-denial"}}.Encode(), nil)
	r.AddCookie(flow)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 || strings.Contains(w.Body.String(), "sensitive-denial") || provider.calls.Load() != 0 {
		t.Fatal("provider denial not handled safely")
	}
	if replay := callbackHTTP(h, state, "100", flow); replay.Code != 400 || provider.calls.Load() != 0 {
		t.Fatal("denied flow replayed")
	}
	admin := oauthLogin(t, s, "100")
	r = httptest.NewRequest("POST", "/api/v1/auth/github/link", nil)
	r.AddCookie(identity.SessionCookie(admin))
	r.Header.Set("Origin", "https://ci.example.test")
	r.Header.Set("X-CSRF-Token", identity.CSRFToken(admin.Token))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("link start failed")
	}
	var body struct {
		URL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(body.URL)
	if err != nil {
		t.Fatal(err)
	}
	w = callbackHTTP(h, u.Query().Get("state"), "300", w.Result().Cookies()[0], identity.SessionCookie(admin))
	if w.Code != 303 {
		t.Fatalf("link finish failed: %d", w.Code)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == identity.CookieName {
			session, err := s.AuthenticateSession(t.Context(), cookie.Value)
			if err != nil || session.UserID != admin.Session.UserID {
				t.Fatal("linked identity changed owner")
			}
		}
	}
	if _, err := s.AuthenticateSession(t.Context(), admin.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("link did not rotate session")
	}
	if credentials := oauthLogin(t, s, "300"); credentials.Session.UserID != admin.Session.UserID {
		t.Fatal("linked login did not resolve same user")
	}
}
