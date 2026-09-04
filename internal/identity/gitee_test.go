package identity

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yuanci/yuanci/internal/scm"
)

func TestGiteeCodeFlow(t *testing.T) {
	g, err := NewGitee("client", "fixture-client-secret", "https://ci.example.test/api/v1/auth/gitee/callback")
	if err != nil {
		t.Fatal(err)
	}
	state, nonce := NewToken(), NewToken()
	verifier := PKCEVerifier(state, nonce)
	u, _ := url.Parse(g.AuthorizationURL(state, verifier))
	if u.Host != "gitee.com" || u.Query().Get("state") != state || u.Query().Get("scope") != "user_info" || u.Query().Get("response_type") != "code" || u.Query().Has("code_challenge") {
		t.Fatal("incorrect Gitee authorization contract")
	}
	calls := 0
	g.client.Transport = oauthTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "gitee.com" || r.URL.RawQuery != "" {
			t.Fatal("wrong origin or credentials in URL")
		}
		if calls == 1 {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Method != "POST" || r.URL.Path != "/oauth/token" || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "code" || r.Form.Get("client_secret") != "fixture-client-secret" || r.Form.Get("redirect_uri") != g.callback || r.Form.Has("code_verifier") {
				t.Fatal("invalid token request")
			}
			return oauthReply(r, 200, `{"access_token":"fixture-access","refresh_token":"fixture-refresh","token_type":"bearer","scope":"user_info","expires_in":86400}`), nil
		}
		if r.URL.Path != "/api/v5/user" || r.Header.Get("Authorization") != "Bearer fixture-access" {
			t.Fatal("invalid identity request")
		}
		return oauthReply(r, 200, `{"id":42,"login":"fixture","name":"Fixture"}`), nil
	})
	user, err := g.Exchange(t.Context(), "code", verifier)
	if err != nil || user.Provider != "gitee" || user.Instance != GiteeInstance || user.Subject != "42" || calls != 2 {
		t.Fatalf("user=%+v calls=%d err=%v", user, calls, err)
	}
}

func TestGiteeFailuresAreBoundedAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"denied", 401, "fixture-secret", scm.ErrUnauthorized},
		{"limited", 429, "fixture-secret", scm.ErrRateLimited},
		{"error", 200, `{"error":"fixture-secret"}`, ErrProvider},
		{"token type", 200, `{"access_token":"x","token_type":"mac","scope":"user_info","expires_in":1}`, ErrProvider},
		{"scope", 200, `{"access_token":"x","token_type":"bearer","scope":"projects","expires_in":1}`, scm.ErrUnauthorized},
		{"expired", 200, `{"access_token":"x","token_type":"bearer","scope":"user_info","expires_in":0}`, ErrProvider},
		{"oversize", 200, strings.Repeat("x", (1<<20)+1), ErrProvider},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := NewGitee("client", "fixture-client-secret", "https://ci.example.test/api/v1/auth/gitee/callback")
			calls := 0
			g.client.Transport = oauthTransport(func(r *http.Request) (*http.Response, error) { calls++; return oauthReply(r, tc.status, tc.body), nil })
			_, err := g.Exchange(t.Context(), "code", NewToken())
			if !errors.Is(err, tc.want) || !errors.Is(err, ErrProvider) || strings.Contains(err.Error(), "fixture") || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
	g, _ := NewGitee("client", "fixture-client-secret", "https://ci.example.test/api/v1/auth/gitee/callback")
	calls := 0
	g.client.Transport = oauthTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		reply := oauthReply(r, 307, "")
		reply.Header.Set("Location", "https://attacker.test/")
		return reply, nil
	})
	if _, err := g.Exchange(t.Context(), "code", NewToken()); !errors.Is(err, ErrProvider) || calls != 1 {
		t.Fatal("redirect followed")
	}
	g.client.Transport = oauthTransport(func(r *http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
	if _, err := g.Exchange(t.Context(), "code", NewToken()); !errors.Is(err, ErrProvider) {
		t.Fatal("timeout ignored")
	}
	if g.client.Timeout <= 0 {
		t.Fatal("unbounded request")
	}
}

func TestGiteeRejectsInvalidConfiguration(t *testing.T) {
	for _, callback := range []string{"http://ci.test/api/v1/auth/gitee/callback", "https://ci.test/api/v1/auth/github/callback", "https://u:p@ci.test/api/v1/auth/gitee/callback", "https://ci.test/api/v1/auth/gitee/callback?x=y"} {
		if _, err := NewGitee("client", "fixture-client-secret", callback); err == nil {
			t.Fatal("unsafe callback accepted")
		}
	}
}
