package integration

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yuanci/yuanci/internal/identity"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}
func reply(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestAppJWTAndKeyValidation(t *testing.T) {
	key, encoded := testKey(t)
	jwt, err := appJWT("Iv1.test", encoded)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var decoded struct {
		Issuer string `json:"iss"`
		IAT    int64  `json:"iat"`
		EXP    int64  `json:"exp"`
	}
	if json.Unmarshal(claims, &decoded) != nil || decoded.Issuer != "Iv1.test" || decoded.EXP-decoded.IAT > 600 || decoded.IAT > time.Now().Unix() {
		t.Fatal("invalid JWT claims")
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		t.Fatal("invalid JWT signature")
	}
	for _, bad := range [][]byte{nil, []byte("not a key"), append(encoded, encoded...), []byte(strings.Repeat("x", 17000))} {
		if _, err := parseKey(bad); !errors.Is(err, ErrConfig) {
			t.Fatal("invalid key accepted")
		}
	}
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	if _, err := parseKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})); err != nil {
		t.Fatal(err)
	}
}
func TestGitHubFixedEndpointsErrorsAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *http.Response
		want     error
	}{
		{"rate", reply(429, "sensitive token"), ErrRate}, {"auth", reply(401, "sensitive token"), ErrAccess}, {"missing", reply(404, "sensitive token"), ErrAccess}, {"oversize", reply(200, strings.Repeat("x", (2<<20)+1)), ErrRemote}, {"invalid", reply(200, "not JSON secret"), ErrRemote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGitHub()
			g.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
					t.Fatal("unexpected host")
				}
				return tc.response, nil
			})
			_, err := g.Installations(t.Context(), "test-token")
			if !errors.Is(err, tc.want) {
				t.Fatal("unsafe or unexpected error")
			}
		})
	}
	g := NewGitHub()
	calls := 0
	g.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		res := reply(302, "")
		res.Header.Set("Location", "https://evil.test")
		return res, nil
	})
	if _, err := g.Installations(t.Context(), "test-token"); err == nil || calls != 1 {
		t.Fatal("redirect followed")
	}
	g.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
	if _, err := g.Installations(t.Context(), "test-token"); !errors.Is(err, ErrRemote) {
		t.Fatal("transport detail exposed")
	}
}
func TestGitHubAppAndInstallationBinding(t *testing.T) {
	_, key := testKey(t)
	g := NewGitHub()
	body := `{"id":12,"slug":"test-app","client_id":"Iv1.test"}`
	g.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Fatal("missing app JWT")
		}
		return reply(200, body), nil
	})
	app, err := g.VerifyApp(t.Context(), "Iv1.test", key)
	if err != nil || app.AppID != "12" {
		t.Fatal(err)
	}
	if _, err := g.VerifyApp(t.Context(), "Iv1.other", key); !errors.Is(err, ErrConfig) {
		t.Fatal("wrong login App accepted")
	}
	install := Installation{ID: "34", AccountID: "56", Account: "team"}
	body = `{"id":34,"app_id":12,"account":{"id":56,"login":"team"},"suspended_at":null}`
	if err := g.VerifyInstallation(t.Context(), app, key, install); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(body, `"app_id":12`, `"app_id":13`, 1), strings.Replace(body, `"id":56`, `"id":57`, 1), strings.Replace(body, `null`, `"2026-01-01"`, 1)} {
		body = bad
		if err := g.VerifyInstallation(t.Context(), app, key, install); !errors.Is(err, ErrAccess) {
			t.Fatal("unverified installation accepted")
		}
	}
}
func TestGitHubOAuthPKCEAndRepositoryIntersection(t *testing.T) {
	g := NewGitHub()
	verifier := identity.NewToken()
	target, _ := url.Parse(g.AuthorizationURL("Iv1.test", "https://ci.test/callback", "state", verifier))
	if target.Query().Get("code_challenge") != identity.PKCEChallenge(verifier) || target.Query().Get("scope") != "" {
		t.Fatal("invalid authorization URL")
	}
	g.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			if r.URL.Host != "github.com" {
				t.Fatal("wrong OAuth host")
			}
			_ = r.ParseForm()
			if r.Form.Get("code_verifier") != verifier {
				t.Fatal("missing PKCE")
			}
			return reply(200, `{"access_token":"temporary-test-token","token_type":"bearer","expires_in":60,"refresh_token":"must-not-be-kept"}`), nil
		case "/user":
			return reply(200, `{"id":100}`), nil
		default:
			return reply(200, `{"total_count":101,"repositories":[{"id":1,"name":"safe","owner":{"login":"team"},"default_branch":"main","permissions":{"admin":true}},{"id":2,"name":"read-only","owner":{"login":"team"},"permissions":{"admin":false}},{"id":3,"name":"../unsafe","owner":{"login":"team"},"permissions":{"admin":true}}]}`), nil
		}
	})
	subject, token, expiry, err := g.Exchange(t.Context(), "Iv1.test", "test-secret", "https://ci.test/callback", "code", verifier)
	if err != nil || subject != "100" || token == "" || time.Until(expiry) > time.Minute {
		t.Fatal("exchange failed")
	}
	page, err := g.Repositories(t.Context(), token, "34", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "1" || page.NextPage != 2 {
		t.Fatal("unsafe repository exposed")
	}
	if _, err := g.Repositories(t.Context(), token, "../x", 1); !errors.Is(err, ErrConfig) {
		t.Fatal("path injection")
	}
}

func TestGitHubRepositoryScopedTokenAndImmutableFile(t *testing.T) {
	_, key := testKey(t)
	g := NewGitHub()
	token := "ghs_repository_token"
	expires := time.Now().UTC().Add(50 * time.Minute).Truncate(time.Second)
	calls := 0
	g.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.github.com" || r.URL.Scheme != "https" {
			t.Fatal("runtime request escaped fixed GitHub API host")
		}
		switch calls {
		case 1:
			if r.Method != http.MethodPost || r.URL.Path != "/app/installations/34/access_tokens" ||
				!strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") || r.Header.Get("Content-Type") != "application/json" {
				t.Fatal("invalid installation token request")
			}
			var body struct {
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.RepositoryIDs) != 1 || body.RepositoryIDs[0] != 70 || body.Permissions["contents"] != "read" {
				t.Fatalf("token scope widened: %#v", body)
			}
			return reply(http.StatusCreated, `{"token":"`+token+`","expires_at":"`+expires.Format(time.RFC3339)+`","repositories":[{"id":70}],"permissions":{"contents":"read","metadata":"read"}}`), nil
		case 2:
			if r.Method != http.MethodGet || r.URL.Path != "/repos/trusted/repo/commits/release/1" ||
				r.URL.EscapedPath() != "/repos/trusted/repo/commits/release%2F1" ||
				r.Header.Get("Authorization") != "Bearer "+token {
				t.Fatal("default-branch commit request was not authenticated")
			}
			return reply(http.StatusOK, `{"sha":"0123456789ABCDEF0123456789ABCDEF01234567"}`), nil
		case 3:
			if r.Method != http.MethodGet || r.URL.Path != "/repos/trusted/repo/contents/ci/pipeline.yml" ||
				r.URL.Query().Get("ref") != "0123456789abcdef0123456789abcdef01234567" ||
				r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Accept") != "application/vnd.github.raw+json" {
				t.Fatal("file request was not exact-SHA authenticated")
			}
			return reply(http.StatusOK, "version: v1"), nil
		default:
			t.Fatal("unexpected runtime request")
			return nil, errors.New("unexpected request")
		}
	})
	issued, expiry, err := g.InstallationToken(t.Context(), "Iv1.test", key, "34", "70")
	if err != nil || string(issued) != token || !expiry.Equal(expires) {
		t.Fatalf("token reply: %q %v %v", issued, expiry, err)
	}
	commit, err := g.RepositoryCommit(t.Context(), issued, "trusted", "repo", "release/1")
	if err != nil || commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("commit reply: %q error=%v", commit, err)
	}
	content, err := g.RepositoryFile(t.Context(), issued, "trusted", "repo", "ci/pipeline.yml", "0123456789abcdef0123456789abcdef01234567")
	if err != nil || string(content) != "version: v1" || calls != 3 {
		t.Fatalf("file reply: %q calls=%d error=%v", content, calls, err)
	}
}

func TestGitHubRuntimeCredentialResponseFailsClosed(t *testing.T) {
	_, key := testKey(t)
	for _, body := range []string{
		`{"token":"secret","expires_at":"2030-01-01T00:00:00Z","repositories":[{"id":71}],"permissions":{"contents":"read"}}`,
		`{"token":"secret","expires_at":"2030-01-01T00:00:00Z","repositories":[{"id":70}],"permissions":{"contents":"write"}}`,
		`{"token":"secret","expires_at":"2030-01-01T00:00:00Z","repositories":[{"id":70}],"permissions":{"contents":"read","issues":"read"}}`,
		`{"token":"secret value","expires_at":"2030-01-01T00:00:00Z","repositories":[{"id":70}],"permissions":{"contents":"read"}}`,
	} {
		g := NewGitHub()
		g.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return reply(http.StatusCreated, body), nil })
		if token, _, err := g.InstallationToken(t.Context(), "Iv1.test", key, "34", "70"); err == nil || len(token) != 0 || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe token response accepted or exposed: token=%q error=%v", token, err)
		}
	}
	g := NewGitHub()
	g.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return reply(http.StatusFound, "secret"), nil })
	if _, err := g.RepositoryFile(t.Context(), []byte("token"), "trusted", "repo", ".yuanci.yml", "0123456789abcdef0123456789abcdef01234567"); !errors.Is(err, ErrRemote) || strings.Contains(err.Error(), "secret") {
		t.Fatal("redirect or response detail exposed")
	}
}
