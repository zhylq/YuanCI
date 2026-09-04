package gitee

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yuanci/yuanci/internal/scm"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func TestOAuthTokenLifecycleContract(t *testing.T) {
	c := NewClient()
	calls := 0
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "gitee.com" || r.URL.RawQuery != "" {
			t.Fatal("credential in URL")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if calls == 1 && (r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_secret") != "secret" || r.Form.Get("code") != "code") {
			t.Fatal("invalid exchange")
		}
		if calls == 2 && (r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh") {
			t.Fatal("invalid refresh")
		}
		return response(r, 200, `{"access_token":"access","refresh_token":"rotated","token_type":"bearer","scope":"user_info projects","expires_in":86400}`), nil
	})
	config := OAuthConfig{ClientID: "client", Secret: "secret", Callback: "https://ci.test/api/v1/integrations/gitee/callback"}
	token, err := c.Exchange(t.Context(), config, "code")
	if err != nil || token.Access != "access" || token.Refresh != "rotated" || !token.ExpiresAt.After(time.Now()) {
		t.Fatalf("exchange: %v", err)
	}
	if _, err := c.Refresh(t.Context(), config, "refresh"); err != nil || calls != 2 {
		t.Fatalf("refresh: %v calls=%d", err, calls)
	}
}
func TestOAuthRejectsScopeAndRateAndRedirect(t *testing.T) {
	c := NewClient()
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return response(r, 200, `{"access_token":"access","refresh_token":"refresh","token_type":"bearer","scope":"user_info","expires_in":3600}`), nil
	})
	if _, err := c.Exchange(t.Context(), OAuthConfig{}, "code"); !errors.Is(err, scm.ErrUnauthorized) {
		t.Fatalf("scope: %v", err)
	}
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		reply := response(r, 429, "secret-body")
		reply.Header.Set("Retry-After", "30")
		return reply, nil
	})
	if _, err := c.Refresh(t.Context(), OAuthConfig{}, "secret"); !errors.Is(err, scm.ErrRateLimited) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("rate: %v", err)
	}
	calls := 0
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		calls++
		reply := response(r, 307, "")
		reply.Header.Set("Location", "https://attacker.test")
		return reply, nil
	})
	if _, err := c.Refresh(t.Context(), OAuthConfig{}, "secret"); err == nil || calls != 1 {
		t.Fatal("followed redirect")
	}
}

func TestRepositoryIdentityAndPermissionContract(t *testing.T) {
	c := NewClient()
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.URL.Query().Get("access_token") != "access" {
			t.Fatal("incorrect credential transport")
		}
		return response(r, 200, `{"id":42,"path":"repo","namespace":{"id":7,"path":"owner"},"private":true,"default_branch":"main","html_url":"https://gitee.com/owner/repo","permission":{"admin":true}}`), nil
	})
	repo, err := c.Repository(t.Context(), "access", "owner", "repo")
	if err != nil || repo.ID != "42" || repo.AccountID != "7" || !repo.Private {
		t.Fatalf("repo=%+v err=%v", repo, err)
	}
	if _, err := c.Repository(t.Context(), "access", "other", "repo"); err == nil {
		t.Fatal("repository identity substitution")
	}
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return response(r, 200, `{"id":42,"path":"repo","namespace":{"id":7,"path":"owner"},"default_branch":"main","html_url":"https://gitee.com/owner/repo","permission":{"push":true}}`), nil
	})
	if _, err := c.Repository(t.Context(), "access", "owner", "repo"); !errors.Is(err, scm.ErrUnauthorized) {
		t.Fatalf("non-admin accepted: %v", err)
	}
}

func TestRepositoriesSkipEmptyRepositories(t *testing.T) {
	c := NewClient()
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return response(r, 200, `[
{"id":41,"path":"empty","namespace":{"id":7,"path":"owner"},"html_url":"https://gitee.com/owner/empty","permission":{"admin":true}},
{"id":42,"path":"ready","namespace":{"id":7,"path":"owner"},"default_branch":"main","html_url":"https://gitee.com/owner/ready","permission":{"admin":true}}
]`), nil
	})
	page, err := c.Repositories(t.Context(), "access", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "ready" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}
