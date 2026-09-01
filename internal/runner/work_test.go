package runner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestWorkClientExecutesOnlyAfterLeaseHandshakeAndCompletes(t *testing.T) {
	fixture := newEnrollmentFixture(t)
	capabilities := credentialCapabilities()
	credentials, err := LoadOrEnroll(t.Context(), EnrollmentConfig{Address: fixture.address, ServerName: "server",
		RootCAFile: fixture.rootFile, StateDir: filepath.Join(t.TempDir(), "runner"), Token: fixture.token,
		Name: "work-runner", Capabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	record := storetest.RunnerRecord(t, 1, pipeline.RunnerRequirements{OS: "linux", Architecture: "amd64",
		Executor: "docker", Labels: map[string]string{"region": "test"}}, "1MiB")
	if _, err := fixture.jobs.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	client, err := NewWorkClient(WorkConfig{Address: fixture.address, ServerName: "server",
		Credentials: credentials, Capabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	executor := &recordingExecutor{started: make(chan uuid.UUID, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, executor) }()

	select {
	case jobID := <-executor.started:
		if jobID == uuid.Nil {
			t.Fatal("executor received an empty job ID")
		}
	case <-ctx.Done():
		t.Fatal("job was not executed")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		runs, listErr := fixture.jobs.List(t.Context(), 10)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(runs) == 1 && runs[0].Status == runmodel.StatusSucceeded {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("run did not complete: %+v", runs)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("work client did not stop cleanly")
	}
}

func TestDecodeAssignmentRejectsExpiredAndUnknownPlans(t *testing.T) {
	base := &runnerv1.JobAssignment{JobId: uuid.NewString(), RunId: uuid.NewString(), LeaseToken: "lease",
		LeaseExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), ExecutionPlanJson: []byte(`{"name":"job","unknown":true}`)}
	if _, err := decodeAssignment(base); err == nil {
		t.Fatal("unknown execution plan field accepted")
	}
	base.ExecutionPlanJson = []byte(`{"name":"job"}`)
	base.LeaseExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
	if _, err := decodeAssignment(base); err == nil {
		t.Fatal("expired assignment accepted")
	}
}

func TestReconnectJitterIsBounded(t *testing.T) {
	for _, backoff := range []time.Duration{time.Second, 5 * time.Second, 30 * time.Second} {
		for range 100 {
			got := jitter(backoff)
			if got < backoff/2 || got > backoff {
				t.Fatalf("jitter(%s)=%s outside expected bounds", backoff, got)
			}
		}
	}
}

type recordingExecutor struct {
	started chan uuid.UUID
}

func (executor *recordingExecutor) Execute(_ context.Context, jobID uuid.UUID, _ pipeline.PlanJob) error {
	executor.started <- jobID
	return nil
}
