package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

type RunnerProvision func(*testing.T, runmodel.RunnerDescriptor) runmodel.RunnerJobStore

func RunnerRecord(t *testing.T, jobs int, requirement pipeline.RunnerRequirements, disk string) runmodel.Record {
	t.Helper()
	stages := []pipeline.Stage{{Name: "verify", Jobs: make([]pipeline.Job, 0, jobs)}}
	for index := 0; index < jobs; index++ {
		stages[0].Jobs = append(stages[0].Jobs, pipeline.Job{Name: fmt.Sprintf("job%d", index),
			Image: "alpine:3.21", RunsOn: requirement, Resources: pipeline.Resources{Disk: disk},
			Steps: []pipeline.Step{{Name: "test", Commands: []string{"true"}}}})
	}
	source, err := json.Marshal(pipeline.Pipeline{Version: pipeline.APIVersion, Name: "runner-contract", Stages: stages})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pipeline.Compile(source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return runmodel.Record{ID: uuid.New(), PipelineName: plan.Name, Event: "manual", Status: runmodel.StatusQueued,
		ConfigSHA256: plan.ConfigSHA256, Plan: encoded, CreatedAt: time.Now().UTC()}
}

func ExerciseRunner(t *testing.T, provision RunnerProvision) {
	t.Helper()
	base := func() runmodel.RunnerDescriptor {
		return runmodel.RunnerDescriptor{ID: uuid.New(), PoolType: "standard", OS: "linux", Architecture: "amd64",
			Executor: "docker", Labels: map[string]string{"region/cn": "east"}, Capacity: 2, AvailableDiskBytes: 4 << 30,
			ProtocolVersion: 1}
	}

	t.Run("matching_matrix", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*runmodel.RunnerDescriptor)
			match  bool
		}{
			{"matches", func(*runmodel.RunnerDescriptor) {}, true},
			{"pool", func(r *runmodel.RunnerDescriptor) { r.PoolType = "privileged" }, false},
			{"os", func(r *runmodel.RunnerDescriptor) { r.OS = "windows" }, false},
			{"architecture", func(r *runmodel.RunnerDescriptor) { r.Architecture = "arm64" }, false},
			{"executor", func(r *runmodel.RunnerDescriptor) { r.Executor = "shell" }, false},
			{"label", func(r *runmodel.RunnerDescriptor) { r.Labels["region/cn"] = "west" }, false},
			{"disk", func(r *runmodel.RunnerDescriptor) { r.AvailableDiskBytes = 1 << 30 }, false},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				runner := base()
				test.mutate(&runner)
				store := provision(t, runner)
				creator := store.(interface {
					Create(context.Context, runmodel.Record) (runmodel.Record, error)
				})
				if _, err := creator.Create(t.Context(), RunnerRecord(t, 1, pipeline.RunnerRequirements{
					OS: "linux", Architecture: "amd64", Executor: "docker", Labels: map[string]string{"region/cn": "east"}}, "2GiB")); err != nil {
					t.Fatal(err)
				}
				assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner.ID})
				if err != nil || (assignment != nil) != test.match {
					t.Fatalf("claim match=%v assignment=%v err=%v", test.match, assignment, err)
				}
			})
		}
	})

	t.Run("unscoped_claim_is_rejected", func(t *testing.T) {
		runner := base()
		store := provision(t, runner)
		if _, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{}); !errors.Is(err, runmodel.ErrInvalidRunnerRequest) {
			t.Fatalf("unscoped claim accepted: %v", err)
		}
	})

	t.Run("assignment_release_is_lease_bound_and_requeues", func(t *testing.T) {
		runner := base()
		store := provision(t, runner)
		creator := store.(interface {
			Create(context.Context, runmodel.Record) (runmodel.Record, error)
		})
		if _, err := creator.Create(t.Context(), RunnerRecord(t, 1, pipeline.RunnerRequirements{}, "")); err != nil {
			t.Fatal(err)
		}
		assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner.ID})
		if err != nil || assignment == nil {
			t.Fatalf("claim: %#v %v", assignment, err)
		}
		wrong := runmodel.LeaseRequest{RunnerID: runner.ID, JobID: assignment.JobID, LeaseToken: "wrong"}
		if err := store.ReleaseRunnerJob(t.Context(), wrong); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("wrong lease released assignment: %v", err)
		}
		lease := runmodel.LeaseRequest{RunnerID: runner.ID, JobID: assignment.JobID, LeaseToken: assignment.LeaseToken}
		if err := store.ReleaseRunnerJob(t.Context(), lease); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AcknowledgeRunnerJob(t.Context(), lease); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("released lease remained usable: %v", err)
		}
		retry, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner.ID})
		if err != nil || retry == nil || retry.JobID != assignment.JobID || retry.LeaseToken == assignment.LeaseToken {
			t.Fatalf("released assignment was not safely requeued: %#v %v", retry, err)
		}
	})

	t.Run("capacity_is_atomic", func(t *testing.T) {
		runner := base()
		store := provision(t, runner)
		creator := store.(interface {
			Create(context.Context, runmodel.Record) (runmodel.Record, error)
		})
		if _, err := creator.Create(t.Context(), RunnerRecord(t, 8, pipeline.RunnerRequirements{}, "")); err != nil {
			t.Fatal(err)
		}
		var wait sync.WaitGroup
		assignments := make(chan *runmodel.Assignment, 8)
		errorsOut := make(chan error, 8)
		for range 8 {
			wait.Go(func() {
				assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner.ID})
				assignments <- assignment
				errorsOut <- err
			})
		}
		wait.Wait()
		close(assignments)
		close(errorsOut)
		for err := range errorsOut {
			if err != nil {
				t.Fatal(err)
			}
		}
		count := 0
		for assignment := range assignments {
			if assignment != nil {
				count++
			}
		}
		if count != runner.Capacity {
			t.Fatalf("claimed %d jobs with capacity %d", count, runner.Capacity)
		}
	})

	t.Run("identity_receipt_start_renewal", func(t *testing.T) {
		runner := base()
		store := provision(t, runner)
		creator := store.(interface {
			Create(context.Context, runmodel.Record) (runmodel.Record, error)
		})
		if _, err := creator.Create(t.Context(), RunnerRecord(t, 1, pipeline.RunnerRequirements{}, "")); err != nil {
			t.Fatal(err)
		}
		assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner.ID})
		if err != nil || assignment == nil {
			t.Fatalf("claim: %v", err)
		}
		lease := runmodel.LeaseRequest{RunnerID: runner.ID, JobID: assignment.JobID, LeaseToken: assignment.LeaseToken}
		if _, err := store.StartRunnerJob(t.Context(), lease); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("start before receipt accepted: %v", err)
		}
		wrong := lease
		wrong.RunnerID = uuid.New()
		if _, err := store.AcknowledgeRunnerJob(t.Context(), wrong); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("wrong Runner accepted: %v", err)
		}
		for range 2 {
			if _, err := store.AcknowledgeRunnerJob(t.Context(), lease); err != nil {
				t.Fatalf("idempotent receipt: %v", err)
			}
			if _, err := store.StartRunnerJob(t.Context(), lease); err != nil {
				t.Fatalf("idempotent start: %v", err)
			}
		}
		heartbeat := runmodel.HeartbeatRequest{Runner: runner, ActiveJobs: []runmodel.ActiveLease{{
			JobID: assignment.JobID, LeaseToken: assignment.LeaseToken, State: "running"}}}
		first, err := store.RenewRunnerLeases(t.Context(), heartbeat)
		if err != nil || len(first.Jobs) != 1 || !first.Jobs[0].Renewed || first.Jobs[0].LeaseExpires.Before(assignment.LeaseExpires) {
			t.Fatalf("renewal failed: %#v %v", first, err)
		}
		if _, err := store.RenewRunnerLeases(t.Context(), heartbeat); err != nil {
			t.Fatalf("duplicate heartbeat: %v", err)
		}
		duplicate := heartbeat
		duplicate.ActiveJobs = append(duplicate.ActiveJobs, duplicate.ActiveJobs[0])
		if _, err := store.RenewRunnerLeases(t.Context(), duplicate); !errors.Is(err, runmodel.ErrInvalidRunnerRequest) {
			t.Fatalf("duplicate active Job accepted: %v", err)
		}
		badToken := heartbeat
		badToken.ActiveJobs = []runmodel.ActiveLease{{JobID: assignment.JobID, LeaseToken: "wrong", State: "running"}}
		result, err := store.RenewRunnerLeases(t.Context(), badToken)
		if err != nil || len(result.Jobs) != 1 || result.Jobs[0].Renewed || result.Jobs[0].CancelReason == "" {
			t.Fatalf("wrong token heartbeat: %#v %v", result, err)
		}
		if err := store.CompleteRunnerJob(t.Context(), runmodel.RunnerCompletion{RunnerID: uuid.New(), JobID: assignment.JobID,
			LeaseToken: assignment.LeaseToken, Status: runmodel.JobSucceeded}); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("wrong Runner completion accepted: %v", err)
		}
		if err := store.CompleteRunnerJob(t.Context(), runmodel.RunnerCompletion{RunnerID: runner.ID, JobID: assignment.JobID,
			LeaseToken: assignment.LeaseToken, Status: runmodel.JobSucceeded}); err != nil {
			t.Fatal(err)
		}
	})
}
