package postgres

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
)

type automationPipelineStub struct {
	proof githubapp.ValidationProof
	err   error
	calls int
}

func (s *automationPipelineStub) ValidateDefaultPipeline(context.Context, string, string) (githubapp.ValidationProof, error) {
	s.calls++
	return s.proof, s.err
}

func automationRequest(t *testing.T, handler http.Handler, method, path, body, token string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "https://ci.example.test"+path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
	}
	if csrf {
		request.Header.Set("Origin", "https://ci.example.test")
		request.Header.Set("X-CSRF-Token", identity.CSRFToken(token))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestProjectAutomationHTTPRBACCSRFFlowAndConflict(t *testing.T) {
	f := newAccessFixture(t)
	appRevision := bindGitHubAutomation(t, f)
	grantProject(t, f, authorization.Viewer)
	pipeline := &automationPipelineStub{proof: githubapp.ValidationProof{RepositoryID: f.project, AppRevision: appRevision,
		CommitSHA:    "0123456789abcdef0123456789abcdef01234567",
		ConfigSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", PipelineName: "validated"}}
	handler, err := httpapi.NewAuthenticated(slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, f.store, 1<<20,
		"https://ci.example.test", httpapi.GitHubLogin{Store: f.store, Provider: &loginProvider{}, Pipeline: pipeline})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + f.project.String() + "/pipeline/validate"
	if response := automationRequest(t, handler, http.MethodPost, path, `{"expected_revision":0}`, "", true); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous validation status=%d", response.Code)
	}
	if response := automationRequest(t, handler, http.MethodPost, path, `{"expected_revision":0}`, f.memberSession.Token, false); response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", response.Code)
	}
	if response := automationRequest(t, handler, http.MethodPost, path, `{"expected_revision":0}`, f.memberSession.Token, true); response.Code != http.StatusNotFound || pipeline.calls != 0 {
		t.Fatalf("viewer validation status=%d calls=%d", response.Code, pipeline.calls)
	}
	grantProject(t, f, authorization.Maintainer)
	response := automationRequest(t, handler, http.MethodPost, path, `{"expected_revision":0}`, f.memberSession.Token, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), pipeline.proof.CommitSHA) || pipeline.calls != 1 {
		t.Fatalf("validation status=%d body=%s calls=%d", response.Code, response.Body.String(), pipeline.calls)
	}
	updatePath := "/api/v1/projects/" + f.project.String() + "/automation"
	body := `{"enabled":true,"pipeline_path":".yuanci.yml","trigger_push":true,"trigger_tag":true,"trigger_pull_request":true,"cancel_older_commits":true,"expected_revision":0}`
	if response := automationRequest(t, handler, http.MethodPut, updatePath, body, f.memberSession.Token, true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":true`) {
		t.Fatalf("enable status=%d body=%s", response.Code, response.Body.String())
	}
	if response := automationRequest(t, handler, http.MethodPut, updatePath, body, f.memberSession.Token, true); response.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProjectAutomationHTTPValidationFailureIsSafeAndNonPersistent(t *testing.T) {
	f := newAccessFixture(t)
	bindGitHubAutomation(t, f)
	grantProject(t, f, authorization.Maintainer)
	pipeline := &automationPipelineStub{err: errors.New("sensitive-provider-response")}
	handler, err := httpapi.NewAuthenticated(slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, f.store, 1<<20,
		"https://ci.example.test", httpapi.GitHubLogin{Store: f.store, Provider: &loginProvider{}, Pipeline: pipeline})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + f.project.String() + "/pipeline/validate"
	response := automationRequest(t, handler, http.MethodPost, path, `{"expected_revision":0}`, f.memberSession.Token, true)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "sensitive") {
		t.Fatalf("unsafe failure status=%d body=%s", response.Code, response.Body.String())
	}
	var rows int
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM repository_automation_validations WHERE repository_id=$1`, f.project).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed validation persisted: rows=%d error=%v", rows, err)
	}
}
