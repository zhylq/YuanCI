package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

func TestMemoryRecoveryRequeuesAssignedAndFailsRunning(t *testing.T) {
	t.Run("assigned", func(t *testing.T) {
		store, runner, now := recoveryFixture(t, false)
		assignment, err := store.ClaimRunnerJob(t.Context(), RunnerClaim{RunnerID: runner.ID})
		if err != nil || assignment == nil {
			t.Fatalf("claim: %v", err)
		}
		*now = now.Add(RunnerLeaseDuration)
		result, err := store.RecoverExpiredRunnerLeases(t.Context(), 100)
		if err != nil || result.Requeued != 1 || result.Failed != 0 {
			t.Fatalf("recovery: %#v %v", result, err)
		}
		replacement, err := store.ClaimRunnerJob(t.Context(), RunnerClaim{RunnerID: runner.ID})
		if err != nil || replacement == nil || replacement.JobID != assignment.JobID || replacement.LeaseToken == assignment.LeaseToken {
			t.Fatalf("requeue claim: %#v %v", replacement, err)
		}
	})

	t.Run("running", func(t *testing.T) {
		store, runner, now := recoveryFixture(t, true)
		assignment, err := store.ClaimRunnerJob(t.Context(), RunnerClaim{RunnerID: runner.ID})
		if err != nil || assignment == nil {
			t.Fatalf("claim: %v", err)
		}
		lease := LeaseRequest{RunnerID: runner.ID, JobID: assignment.JobID, LeaseToken: assignment.LeaseToken}
		if _, err := store.AcknowledgeRunnerJob(t.Context(), lease); err != nil {
			t.Fatal(err)
		}
		if _, err := store.StartRunnerJob(t.Context(), lease); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(RunnerLeaseDuration)
		result, err := store.RecoverExpiredRunnerLeases(t.Context(), 100)
		if err != nil || result.Failed != 1 || result.Requeued != 0 {
			t.Fatalf("recovery: %#v %v", result, err)
		}
		if err := store.CompleteRunnerJob(t.Context(), RunnerCompletion{RunnerID: runner.ID, JobID: assignment.JobID,
			LeaseToken: assignment.LeaseToken, Status: JobSucceeded}); !errors.Is(err, ErrLeaseInvalid) {
			t.Fatalf("late completion accepted: %v", err)
		}
		items, _ := store.List(t.Context(), 1)
		if len(items) != 1 || items[0].Status != StatusFailed || items[0].FinishedAt == nil {
			t.Fatalf("run not failed: %#v", items)
		}
		if store.jobs[1].status != JobSkipped {
			t.Fatalf("downstream status=%s", store.jobs[1].status)
		}
	})
}

func TestRecoveryRejectsUnboundedBatch(t *testing.T) {
	store := NewMemoryStore()
	for _, limit := range []int{0, MaximumRecoveryBatch + 1} {
		if _, err := store.RecoverExpiredRunnerLeases(t.Context(), limit); !errors.Is(err, ErrInvalidRecoveryLimit) {
			t.Fatalf("limit %d accepted: %v", limit, err)
		}
	}
}

func TestLeaseReconcilerStopsAndUsesBoundedBatch(t *testing.T) {
	fake := &recoveryStoreFake{called: make(chan int, 1)}
	var logs bytes.Buffer
	reconciler, err := NewLeaseReconciler(fake, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { reconciler.Run(ctx); close(done) }()
	if limit := <-fake.called; limit != MaximumRecoveryBatch {
		t.Fatalf("batch=%d", limit)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconciler did not stop")
	}
}

type recoveryStoreFake struct{ called chan int }

func (fake *recoveryStoreFake) RecoverExpiredRunnerLeases(_ context.Context, limit int) (RecoveryResult, error) {
	fake.called <- limit
	return RecoveryResult{}, nil
}

func recoveryFixture(t *testing.T, downstream bool) (*MemoryStore, RunnerDescriptor, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	store.now = func() time.Time { return now }
	runner := RunnerDescriptor{ID: uuid.New(), PoolType: "standard", OS: "linux", Architecture: "amd64",
		Executor: "docker", Labels: map[string]string{}, Capacity: 1, AvailableDiskBytes: 1 << 30, ProtocolVersion: 1}
	if _, err := store.RenewRunnerLeases(t.Context(), HeartbeatRequest{Runner: runner}); err != nil {
		t.Fatal(err)
	}
	job := pipeline.PlanJob{Name: "first", Image: "alpine", Timeout: time.Minute,
		RunsOn: pipeline.RunnerRequirements{OS: "linux", Executor: "docker"}, Steps: []pipeline.Step{{Name: "run", Commands: []string{"true"}}}}
	jobs := []pipeline.PlanJob{job}
	if downstream {
		second := job
		second.Name = "second"
		second.DependsOn = []string{"first"}
		jobs = append(jobs, second)
	}
	plan, err := json.Marshal(pipeline.Plan{Version: "v1", Name: "recovery", Stages: []pipeline.PlanStage{{Name: "test", Jobs: jobs}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(t.Context(), Record{ID: uuid.New(), PipelineName: "recovery", Event: "manual",
		Status: StatusQueued, ConfigSHA256: "test", Plan: plan, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return store, runner, &now
}
