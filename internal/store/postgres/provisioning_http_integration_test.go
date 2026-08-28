package postgres

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
)

func managedRequest(h http.Handler, method, path, body, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Origin", "https://ci.example.test")
	r.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestManagedHTTPWizardAndAuthorization(t *testing.T) {
	s, service, _ := managedFixture(t)
	provider := &loginProvider{}
	service.Factory = func(id, secret, callback string) (identity.OAuthProvider, error) {
		if secret != candidateInput(nil).ClientSecret {
			t.Error("wrong encrypted secret loaded")
		}
		return provider, nil
	}
	var logs bytes.Buffer
	h, err := httpapi.NewAuthenticated(slog.New(slog.NewJSONHandler(&logs, nil)), s, s, 1<<20, "https://ci.example.test", httpapi.GitHubLogin{Store: s, Managed: service})
	if err != nil {
		t.Fatal(err)
	}
	code := identity.NewToken()
	if err := s.IssueSetupCode(t.Context(), code); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/setup/exchange", strings.NewReader(`{"code":"`+code+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing Origin accepted")
	}
	w = managedRequest(h, "POST", "/api/v1/setup/exchange", `{"code":"`+code+`"}`, "")
	if w.Code != 200 {
		t.Fatalf("exchange status %d", w.Code)
	}
	setup := w.Result().Cookies()[0]
	csrf := identity.CSRFToken(setup.Value)
	if setup.Name != provisioning.CookieName || !setup.Secure || !setup.HttpOnly || strings.Contains(w.Body.String(), setup.Value) {
		t.Fatal("setup credential exposed")
	}
	input, _ := json.Marshal(candidateInput(nil))
	if w := managedRequest(h, "POST", "/api/v1/setup/settings", string(input), "", setup); w.Code != 403 {
		t.Fatal("setup missing CSRF accepted")
	}
	w = managedRequest(h, "POST", "/api/v1/setup/settings", string(input), csrf, setup)
	if w.Code != 201 {
		t.Fatalf("save status %d: %s", w.Code, w.Body.String())
	}
	var saved struct {
		ID uuid.UUID `json:"candidate_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	w = managedRequest(h, "GET", "/api/v1/setup/settings", "", "", setup)
	if w.Code != 200 || strings.Contains(w.Body.String(), candidateInput(nil).ClientSecret) {
		t.Fatal("settings exposed secret")
	}
	if w := managedRequest(h, "GET", "/api/v1/settings/auth", "", "", setup); w.Code != 401 {
		t.Fatal("setup bypassed administrator login")
	}
	w = managedRequest(h, "POST", "/api/v1/setup/verify", `{"candidate_id":"`+saved.ID.String()+`"}`, csrf, setup)
	if w.Code != 200 {
		t.Fatalf("verify status %d", w.Code)
	}
	var target struct {
		URL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	flow := w.Result().Cookies()[0]
	w = callbackHTTP(h, u.Query().Get("state"), "100", flow, setup)
	if w.Code != 303 {
		t.Fatalf("callback failed %d: %s", w.Code, w.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == identity.CookieName {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("missing administrator session")
	}
	if w := managedRequest(h, "GET", "/api/v1/settings/auth", "", "", session); w.Code != 200 {
		t.Fatal("admin cannot read settings")
	}
	if w := managedRequest(h, "GET", "/api/v1/setup/settings", "", "", setup); w.Code != 401 {
		t.Fatal("setup not closed")
	}
	if w := managedRequest(h, "POST", "/api/v1/setup/exchange", `{"code":"`+code+`"}`, ""); w.Code != 401 {
		t.Fatal("setup code replayed")
	}
	member := oauthLogin(t, s, "200")
	if w := managedRequest(h, "GET", "/api/v1/settings/auth", "", "", identity.SessionCookie(member)); w.Code != 403 {
		t.Fatal("member accessed settings")
	}
	for _, secret := range []string{code, setup.Value, session.Value, candidateInput(nil).ClientSecret} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("credential in logs")
		}
	}
}
func TestManagedHTTPProviderFailurePreservesCandidate(t *testing.T) {
	s, service, setup := managedFixture(t)
	provider := &loginProvider{}
	service.Factory = func(string, string, string) (identity.OAuthProvider, error) { return provider, nil }
	h, err := httpapi.NewAuthenticated(slog.New(slog.NewTextHandler(io.Discard, nil)), s, s, 1<<20, "https://ci.example.test", httpapi.GitHubLogin{Store: s, Managed: service})
	if err != nil {
		t.Fatal(err)
	}
	id, err := service.Save(t.Context(), provisioning.Access{SetupToken: setup}, candidateInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	w := managedRequest(h, "POST", "/api/v1/setup/verify", `{"candidate_id":"`+id.String()+`"}`, identity.CSRFToken(setup), provisioning.Cookie(setup))
	var target struct {
		URL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(target.URL)
	w = callbackHTTP(h, u.Query().Get("state"), "provider-failure-sensitive-code", w.Result().Cookies()[0], provisioning.Cookie(setup))
	if w.Code != 502 {
		t.Fatal("provider error ignored")
	}
	status, err := s.ProvisioningStatus(t.Context())
	if err != nil || status.Initialized || status.Configured {
		t.Fatal("failed verification initialized instance")
	}
}
