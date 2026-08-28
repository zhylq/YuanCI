// Package storetest defines behavioral contracts shared by all Run stores.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

func Record(t *testing.T, jobs int, join bool) runmodel.Record {
	t.Helper()
	var source strings.Builder
	source.WriteString("version: v1\nname: contract\nstages:\n  - name: verify\n    jobs:\n")
	for i := 0; i < jobs; i++ {
		fmt.Fprintf(&source, "      - name: job%d\n        image: alpine:3.21\n        steps: [{name: test, commands: ['true']}]\n", i)
	}
	if join {
		source.WriteString("      - name: join\n        depends_on: [")
		for i := 0; i < jobs; i++ {
			if i != 0 {
				source.WriteString(", ")
			}
			fmt.Fprintf(&source, "job%d", i)
		}
		source.WriteString("]\n        image: alpine:3.21\n        steps: [{name: test, commands: ['true']}]\n")
	}
	now := time.Now().UTC()
	plan, err := pipeline.Compile([]byte(source.String()), now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return runmodel.Record{ID: uuid.New(), PipelineName: plan.Name, Event: "manual",
		Status: runmodel.StatusQueued, ConfigSHA256: plan.ConfigSHA256, Plan: encoded, CreatedAt: now}
}

func Claim(ctx context.Context, store runmodel.Store) (*runmodel.Assignment, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		assignment, err := store.ClaimJob(ctx, runmodel.ClaimRequest{RunnerName: "contract"})
		if err != nil || assignment != nil {
			return assignment, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func Exercise(t *testing.T, open func(*testing.T) runmodel.Store) {
	t.Helper()
	t.Run("invalid_plan_is_atomic", func(t *testing.T) {
		store := open(t)
		record := Record(t, 1, false)
		record.Plan = json.RawMessage(`{invalid`)
		if _, err := store.Create(t.Context(), record); err == nil {
			t.Fatal("accepted invalid plan")
		}
		items, err := store.List(t.Context(), 20)
		if err != nil || len(items) != 0 {
			t.Fatalf("partial run persisted: count=%d err=%v", len(items), err)
		}
	})
	t.Run("duplicate_run_rejected", func(t *testing.T) {
		store := open(t)
		record := Record(t, 1, false)
		if _, err := store.Create(t.Context(), record); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(t.Context(), record); err == nil {
			t.Fatal("accepted duplicate run")
		}
		items, err := store.List(t.Context(), 20)
		if err != nil || len(items) != 1 {
			t.Fatalf("duplicate persisted: count=%d err=%v", len(items), err)
		}
	})
	t.Run("stored_plan_is_immutable", func(t *testing.T) {
		store := open(t)
		record := Record(t, 1, false)
		created, err := store.Create(t.Context(), record)
		if err != nil {
			t.Fatal(err)
		}
		record.Plan[0] = '!'
		created.Plan[1] = '!'
		items, err := store.List(t.Context(), 20)
		if err != nil || len(items) != 1 {
			t.Fatalf("list: %v", err)
		}
		if !json.Valid(items[0].Plan) {
			t.Fatal("caller modified stored plan")
		}
		items[0].Plan[0] = '!'
		again, err := store.List(t.Context(), 20)
		if err != nil || !json.Valid(again[0].Plan) {
			t.Fatalf("list result modified stored plan: %v", err)
		}
	})
	t.Run("wrong_lease_and_replay_rejected", func(t *testing.T) {
		store := open(t)
		if _, err := store.Create(t.Context(), Record(t, 1, false)); err != nil {
			t.Fatal(err)
		}
		a, err := store.ClaimJob(t.Context(), runmodel.ClaimRequest{RunnerName: "contract"})
		if err != nil || a == nil {
			t.Fatalf("claim: %v", err)
		}
		if err := store.StartJob(t.Context(), a.JobID, "wrong"); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("wrong token: %v", err)
		}
		if err := store.CompleteJob(t.Context(), a.JobID, "wrong", runmodel.JobSucceeded); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("wrong completion: %v", err)
		}
		if err := store.StartJob(t.Context(), a.JobID, a.LeaseToken); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteJob(t.Context(), a.JobID, a.LeaseToken, runmodel.JobQueued); err == nil {
			t.Fatal("nonterminal completion accepted")
		}
		if err := store.CompleteJob(t.Context(), a.JobID, a.LeaseToken, runmodel.JobSucceeded); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteJob(t.Context(), a.JobID, a.LeaseToken, runmodel.JobSucceeded); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("replay accepted: %v", err)
		}
		assertRun(t, store, runmodel.StatusSucceeded)
	})
	t.Run("cancellation_is_not_failure", func(t *testing.T) {
		store := open(t)
		if _, err := store.Create(t.Context(), Record(t, 1, false)); err != nil {
			t.Fatal(err)
		}
		a, err := store.ClaimJob(t.Context(), runmodel.ClaimRequest{RunnerName: "contract"})
		if err != nil || a == nil {
			t.Fatalf("claim: %v", err)
		}
		if err := store.StartJob(t.Context(), a.JobID, a.LeaseToken); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteJob(t.Context(), a.JobID, a.LeaseToken, runmodel.JobCanceled); err != nil {
			t.Fatal(err)
		}
		assertRun(t, store, runmodel.StatusCanceled)
	})
	t.Run("twenty_concurrent_claims_and_completions", func(t *testing.T) {
		store := open(t)
		if _, err := store.Create(t.Context(), Record(t, 20, true)); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		type outcome struct {
			assignment *runmodel.Assignment
			err        error
		}
		claimed := make(chan outcome, 20)
		for i := 0; i < 20; i++ {
			go func() { a, err := Claim(ctx, store); claimed <- outcome{a, err} }()
		}
		seen := map[uuid.UUID]bool{}
		assignments := make([]*runmodel.Assignment, 0, 20)
		for i := 0; i < 20; i++ {
			result := <-claimed
			if result.err != nil || result.assignment == nil {
				t.Fatalf("claim: %v", result.err)
			}
			if seen[result.assignment.JobID] {
				t.Fatal("duplicate claim")
			}
			seen[result.assignment.JobID] = true
			assignments = append(assignments, result.assignment)
		}
		if a, err := store.ClaimJob(ctx, runmodel.ClaimRequest{RunnerName: "contract"}); err != nil || a != nil {
			t.Fatalf("join unlocked early: %v", err)
		}
		completed := make(chan error, 20)
		start := make(chan struct{})
		for _, a := range assignments {
			if err := store.StartJob(ctx, a.JobID, a.LeaseToken); err != nil {
				t.Fatal(err)
			}
			go func() { <-start; completed <- store.CompleteJob(ctx, a.JobID, a.LeaseToken, runmodel.JobSucceeded) }()
		}
		close(start)
		for range assignments {
			if err := <-completed; err != nil {
				t.Fatalf("completion: %v", err)
			}
		}
		joined, err := store.ClaimJob(ctx, runmodel.ClaimRequest{RunnerName: "contract"})
		if err != nil || joined == nil || joined.JobName != "join" {
			t.Fatalf("join stayed blocked: assignment=%v err=%v", joined, err)
		}
		if err := store.StartJob(ctx, joined.JobID, joined.LeaseToken); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteJob(ctx, joined.JobID, joined.LeaseToken, runmodel.JobSucceeded); err != nil {
			t.Fatal(err)
		}
		assertRun(t, store, runmodel.StatusSucceeded)
	})
}

func assertRun(t *testing.T, store runmodel.Store, status runmodel.Status) {
	t.Helper()
	items, err := store.List(t.Context(), 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: count=%d err=%v", len(items), err)
	}
	if items[0].Status != status {
		t.Fatalf("run status=%s want=%s", items[0].Status, status)
	}
	if items[0].StartedAt == nil || items[0].FinishedAt == nil || items[0].FinishedAt.Before(*items[0].StartedAt) {
		t.Fatal("invalid lifecycle timestamps")
	}
}
