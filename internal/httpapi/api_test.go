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
	handler := NewEvaluation(slog.New(slog.NewTextHandler(io.Discard, nil)), runmodel.NewMemoryStore(), 1<<20, "test-runner-token")
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
	handler := NewEvaluation(slog.New(slog.NewTextHandler(io.Discard, nil)), runmodel.NewMemoryStore(), 1<<20, "test-runner-token")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/validate", bytes.NewBufferString(`{"content":"x","extra":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRunAndRunnerLeaseWorkflow(t *testing.T) {
	store := runmodel.NewMemoryStore()
	handler := NewEvaluation(slog.New(slog.NewTextHandler(io.Discard, nil)), store, 1<<20, "test-runner-token")
	pipelineSource := `version: v1
name: workflow
stages:
  - name: verify
    jobs:
      - name: unit
        image: alpine
        steps: [{name: test, commands: ["true"]}]
`
	runBody, _ := json.Marshal(map[string]string{"pipeline": pipelineSource, "event": "manual"})
	runRequest := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(runBody))
	runRequest.Header.Set("Content-Type", "application/json")
	runResponse := httptest.NewRecorder()
	handler.ServeHTTP(runResponse, runRequest)
	if runResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", runResponse.Code, runResponse.Body.String())
	}

	claimRequest := httptest.NewRequest(http.MethodPost, "/api/v1/runner/jobs/claim", bytes.NewBufferString(`{"runner_name":"test"}`))
	claimRequest.Header.Set("Content-Type", "application/json")
	claimRequest.Header.Set("Authorization", "Bearer test-runner-token")
	claimResponse := httptest.NewRecorder()
	handler.ServeHTTP(claimResponse, claimRequest)
	if claimResponse.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", claimResponse.Code, claimResponse.Body.String())
	}
	var assignment runmodel.Assignment
	if err := json.Unmarshal(claimResponse.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}

	callLeaseEndpoint := func(suffix, body string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/runner/jobs/"+assignment.JobID.String()+suffix, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer test-runner-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s status=%d body=%s", suffix, response.Code, response.Body.String())
		}
	}
	callLeaseEndpoint("/start", `{"lease_token":"`+assignment.LeaseToken+`"}`)
	callLeaseEndpoint("/complete", `{"lease_token":"`+assignment.LeaseToken+`","status":"succeeded"}`)

	records, err := store.List(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != runmodel.StatusSucceeded {
		t.Fatalf("unexpected records: %#v", records)
	}
	if records[0].StartedAt == nil || records[0].FinishedAt == nil {
		t.Fatalf("run timestamps were not recorded: %#v", records[0])
	}
}
