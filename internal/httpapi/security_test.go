package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/project"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

func TestJSONFramingAndSafeErrors(t *testing.T) {
	handler := NewEvaluation(slog.New(slog.NewTextHandler(io.Discard, nil)), runmodel.NewMemoryStore(), 1024)
	for _, tt := range []struct {
		name, body, content string
		status              int
	}{
		{"trailing object", `{"content":"x"} {}`, "application/json", 400},
		{"trailing garbage", `{"content":"x"} trailing`, "application/json", 400},
		{"unknown field", `{"sensitive-name":"value"}`, "application/json", 400},
		{"media type suffix", `{}`, "application/json-invalid", 415},
		{"oversize", `{"content":"` + strings.Repeat("a", 2000) + `"}`, "application/json", 413},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/validate", bytes.NewBufferString(tt.body))
			r.Header.Set("Content-Type", tt.content)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status=%d want=%d", w.Code, tt.status)
			}
			if strings.Contains(w.Body.String(), "sensitive-name") {
				t.Fatal("parse error reflected input")
			}
		})
	}
}

func TestEvaluationDoesNotExposeLegacyRunnerAPI(t *testing.T) {
	handler := NewEvaluation(slog.New(slog.NewTextHandler(io.Discard, nil)), runmodel.NewMemoryStore(), 1024)
	for _, path := range []string{
		"/api/v1/runner/jobs/claim",
		"/api/v1/runner/jobs/00000000-0000-4000-8000-000000000001/start",
		"/api/v1/runner/jobs/00000000-0000-4000-8000-000000000001/complete",
	} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Header().Get("Content-Type"), "application/problem+json") {
			t.Fatalf("path=%s status=%d content-type=%q body=%s", path, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
	}
}

func TestAuthenticatedConstructorRejectsUnsafeOrigin(t *testing.T) {
	for _, origin := range []string{"", "http://ci.example.test", "https://user@ci.example.test", "https://ci.example.test/path", "https://ci.example.test?token=x", "https://ci.example.test#x"} {
		if _, err := NewAuthenticated(slog.Default(), runmodel.NewMemoryStore(), closedBrowserBackend{}, 1024, origin); err == nil {
			t.Fatal("unsafe constructor configuration accepted")
		}
	}
}

type closedBrowserBackend struct{}

func (closedBrowserBackend) AuthenticateSession(context.Context, string) (identity.Session, error) {
	return identity.Session{}, identity.ErrUnauthenticated
}
func (closedBrowserBackend) RevokeSession(context.Context, string) error {
	return identity.ErrUnauthenticated
}
func (closedBrowserBackend) CreateAuthorizedRun(context.Context, string, uuid.UUID, runmodel.Record) (runmodel.Record, error) {
	return runmodel.Record{}, identity.ErrUnauthenticated
}
func (closedBrowserBackend) ListAuthorizedRuns(context.Context, string, uuid.UUID, int) ([]runmodel.Record, error) {
	return nil, identity.ErrUnauthenticated
}
func (closedBrowserBackend) ListProjects(context.Context, string, project.Query) (project.Page[project.Record], error) {
	return project.Page[project.Record]{}, identity.ErrUnauthenticated
}
func (closedBrowserBackend) GetProject(context.Context, string, uuid.UUID) (project.Record, error) {
	return project.Record{}, identity.ErrUnauthenticated
}
func (closedBrowserBackend) ListProjectRuns(context.Context, string, uuid.UUID, project.Query) (project.Page[project.Run], error) {
	return project.Page[project.Run]{}, identity.ErrUnauthenticated
}
