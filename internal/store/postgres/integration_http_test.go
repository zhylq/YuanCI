package postgres

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
	"github.com/yuanci/yuanci/internal/secrets"
)

func TestGitHubImportHTTPBoundaries(t *testing.T) {
	s, service, session, _ := importFixture(t)
	cipher, _ := secrets.NewCipher([]byte(strings.Repeat("k", 32)))
	handler, err := httpapi.NewAuthenticated(slog.New(slog.NewTextHandler(io.Discard, nil)), s, s, 1<<20, "https://ci.example.test", httpapi.GitHubLogin{Store: s, Managed: provisioning.New(s, cipher, "https://ci.example.test"), Integrations: service})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, auth, csrf bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://ci.example.test"+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if auth {
			req.AddCookie(&http.Cookie{Name: identity.CookieName, Value: session.Token})
		}
		if csrf {
			req.Header.Set("Origin", "https://ci.example.test")
			req.Header.Set("X-CSRF-Token", identity.CSRFToken(session.Token))
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if res := call("GET", "/api/v1/integrations/github", "", false, false); res.Code != 401 {
		t.Fatal(res.Code)
	}
	if res := call("POST", "/api/v1/integrations/github/import", `{"installation_id":"34","repository_ids":["70"]}`, true, false); res.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	state, nonce := identity.NewToken(), identity.NewToken()
	_, _ = service.Start(t.Context(), session.Token, state, nonce)
	path := "/api/v1/integrations/github/callback?state=" + url.QueryEscape(state) + "&code=fixture&installation_id=999"
	req := httptest.NewRequest("GET", "https://ci.example.test"+path, nil)
	req.AddCookie(&http.Cookie{Name: identity.CookieName, Value: session.Token})
	req.AddCookie(&http.Cookie{Name: "__Host-yuanci_import", Value: nonce})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 303 || res.Header().Get("Location") != "/settings/repositories" {
		t.Fatal("callback failed", res.Code)
	}
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == identity.CookieName {
			t.Fatal("integration callback issued login session")
		}
	}
	if res := call("GET", "/api/v1/integrations/github", "", true, false); res.Code != 200 || strings.Contains(res.Body.String(), "fixture-user-token") || res.Header().Get("Cache-Control") == "" {
		t.Fatal("unsafe settings response")
	}
	if res := call("GET", "/api/v1/integrations/github/installations/34/repositories?page=1&page=2", "", true, false); res.Code != 422 {
		t.Fatal("ambiguous page accepted")
	}
	if res := call("POST", "/api/v1/integrations/github/import", `{"installation_id":"34","repository_ids":["70"],"owner":"attacker"}`, true, true); res.Code != 400 {
		t.Fatal("forged fields accepted", res.Code)
	}
	if res := call("POST", "/api/v1/integrations/github/import", `{"installation_id":"34","repository_ids":["70"]}`, true, true); res.Code != 200 {
		t.Fatal("import failed", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 400 {
		t.Fatal("callback replay accepted")
	}
	// A mere GitHub Setup URL visit never creates a project or authorizes access.
	if res := call("GET", "/settings/repositories?installation_id=777", "", true, false); res.Code != 200 {
		t.Fatal(res.Code)
	}
	var count int
	_ = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM repositories`).Scan(&count)
	if count != 1 {
		t.Fatal("setup URL side effect")
	}
}
