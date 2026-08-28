package identity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type oauthTransport func(*http.Request) (*http.Response, error)

func (f oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func oauthReply(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestGitHubCodeFlowProtocol(t *testing.T) {
	g, err := NewGitHub("fixture-client", "fixture-client-secret", "https://ci.example.test/api/v1/auth/github/callback")
	if err != nil {
		t.Fatal(err)
	}
	state, nonce := NewToken(), NewToken()
	verifier := PKCEVerifier(state, nonce)
	authorize, err := url.Parse(g.AuthorizationURL(state, verifier))
	if err != nil {
		t.Fatal(err)
	}
	q := authorize.Query()
	if authorize.Host != "github.com" || q.Get("state") != state || q.Get("code_challenge") != PKCEChallenge(verifier) || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "" {
		t.Fatal("unsafe authorization request")
	}
	calls := 0
	g.client.Transport = oauthTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if r.Method != "POST" || r.URL.Host != "github.com" || r.URL.RawQuery != "" {
				t.Fatal("credential in wrong endpoint or URL")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("code_verifier") != verifier || r.Form.Get("code") != "fixture-code" || r.Form.Get("client_secret") != "fixture-client-secret" || r.Form.Get("redirect_uri") != g.callback {
				t.Fatal("code exchange not bound to request")
			}
			return oauthReply(r, 200, `{"access_token":"fixture-access-token","token_type":"bearer"}`), nil
		}
		if r.Method != "GET" || r.URL.String() != "https://api.github.com/user" || r.Header.Get("Authorization") != "Bearer fixture-access-token" {
			t.Fatal("identity not verified with new token")
		}
		return oauthReply(r, 200, `{"id":100,"login":"test","name":"Test"}`), nil
	})
	user, err := g.Exchange(t.Context(), "fixture-code", verifier)
	if err != nil || user.Subject != "100" || calls != 2 {
		t.Fatalf("exchange err=%v calls=%d", err, calls)
	}
}

func TestGitHubRejectsErrorsOversizeAndRedirects(t *testing.T) {
	for name, reply := range map[string]string{"provider error": `{"error":"fixture-client-secret"}`, "invalid token": `{"access_token":"x","token_type":"mac"}`, "malformed": `{bad`, "oversize": strings.Repeat("x", (1<<20)+1)} {
		t.Run(name, func(t *testing.T) {
			g, _ := NewGitHub("client", "fixture-client-secret", "https://ci.example.test/api/v1/auth/github/callback")
			g.client.Transport = oauthTransport(func(r *http.Request) (*http.Response, error) { return oauthReply(r, 200, reply), nil })
			_, err := g.Exchange(t.Context(), "code", NewToken())
			if !errors.Is(err, ErrProvider) || strings.Contains(err.Error(), "fixture-client-secret") {
				t.Fatal("provider failure accepted or secret disclosed")
			}
		})
	}
	g, _ := NewGitHub("client", "fixture-client-secret", "https://ci.example.test/api/v1/auth/github/callback")
	calls := 0
	g.client.Transport = oauthTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		response := oauthReply(r, 307, "")
		response.Header.Set("Location", "https://attacker.test/collect")
		return response, nil
	})
	if _, err := g.Exchange(t.Context(), "code", NewToken()); !errors.Is(err, ErrProvider) || calls != 1 {
		t.Fatal("followed credential-bearing redirect")
	}
	g.client.Transport = oauthTransport(func(r *http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
	if _, err := g.Exchange(t.Context(), "code", NewToken()); !errors.Is(err, ErrProvider) {
		t.Fatal("timeout ignored")
	}
}
