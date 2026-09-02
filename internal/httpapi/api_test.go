package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	runmodel "github.com/yuanci/yuanci/internal/run"
)

func TestValidatePipelineEndpoint(t *testing.T) {
	handler := NewEvaluation(slog.New(slog.NewTextHandler(io.Discard, nil)), runmodel.NewMemoryStore(), 1<<20)
	body, _ := json.Marshal(map[string]string{"content": `version: v1
name: test
stages:
  - name: verify
    jobs:
      - name: unit
        image: alpine
        steps:
          - name: test
            commands: ["true"]
`})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/validate", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRejectsUnknownJSONField(t *testing.T) {
	handler := NewEvaluation(slog.New(slog.NewTextHandler(io.Discard, nil)), runmodel.NewMemoryStore(), 1<<20)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/validate", bytes.NewBufferString(`{"content":"x","extra":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
