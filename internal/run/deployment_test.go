package run

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

func TestMemoryStoreRejectsDeployment(t *testing.T) {
	for _, plan := range []pipeline.Plan{
		{Deployment: &pipeline.Deployment{Environment: "production"}},
		{Stages: []pipeline.PlanStage{{Jobs: []pipeline.PlanJob{{Deployment: "production"}}}}},
	} {
		store := NewMemoryStore()
		encoded, _ := json.Marshal(plan)
		if _, err := store.Create(t.Context(), Record{ID: uuid.New(), Plan: encoded}); !errors.Is(err, ErrDeploymentUnsupported) {
			t.Fatalf("deployment accepted: %v", err)
		}
		records, _ := store.List(t.Context(), 10)
		if len(records) != 0 {
			t.Fatal("rejected deployment persisted")
		}
	}
}

func TestDeploymentProtocolHeartbeatValidation(t *testing.T) {
	request := HeartbeatRequest{Runner: RunnerDescriptor{ID: uuid.New(), PoolType: "deployment", OS: "linux", Architecture: "amd64", Executor: "docker", Capacity: 1}}
	for _, protocol := range []int{1, 2, 3} {
		request.Runner.ProtocolVersion = protocol
		if err := ValidateHeartbeatRequest(request); err != nil {
			t.Fatalf("protocol %d: %v", protocol, err)
		}
	}
	request.Runner.ProtocolVersion = 4
	if !errors.Is(ValidateHeartbeatRequest(request), ErrInvalidRunnerRequest) {
		t.Fatal("unknown protocol accepted")
	}
}
