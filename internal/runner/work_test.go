package runner

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
	"google.golang.org/grpc/metadata"
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

func TestWorkClientShutdownCancelsActiveExecutor(t *testing.T) {
	fixture := newEnrollmentFixture(t)
	capabilities := credentialCapabilities()
	credentials, err := LoadOrEnroll(t.Context(), EnrollmentConfig{Address: fixture.address, ServerName: "server",
		RootCAFile: fixture.rootFile, StateDir: filepath.Join(t.TempDir(), "runner"), Token: fixture.token,
		Name: "shutdown-runner", Capabilities: capabilities})
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
	started := make(chan struct{})
	canceled := make(chan struct{})
	executor := executorFunc(func(ctx context.Context, _ uuid.UUID, _ pipeline.PlanJob) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, executor) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not start")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Runner shutdown did not cancel executor")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Runner shutdown did not converge")
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

func TestLeaseDeadlineCancelsAndRenewalExtendsAuthority(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	job := &localJob{id: uuid.New(), leaseExpires: time.Now().Add(60 * time.Millisecond), ctx: ctx, cancel: cancel}
	losses := make(chan uuid.UUID, 1)
	if !armLeaseDeadline(job, losses) {
		t.Fatal("valid initial lease was rejected")
	}
	time.Sleep(20 * time.Millisecond)
	job.leaseExpires = time.Now().Add(120 * time.Millisecond)
	if !resetLeaseDeadline(job, losses) {
		t.Fatal("lease renewal was rejected")
	}
	select {
	case <-ctx.Done():
		t.Fatal("job canceled at the superseded deadline")
	case <-time.After(60 * time.Millisecond):
	}
	select {
	case lost := <-losses:
		if lost != job.id || !job.authorityLost.Load() {
			t.Fatal("lease loss did not bind the expected job")
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("renewed lease deadline did not cancel the job")
	}
}

func TestLeaseLossBeforeStartNeverInvokesExecutor(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	job := &localJob{id: uuid.New(), leaseToken: "lease", leaseExpires: time.Now().Add(time.Minute),
		phase: jobAwaitingStart, ctx: ctx, cancel: cancel}
	job.authorityLost.Store(true)
	active := map[uuid.UUID]*localJob{job.id: job}
	var executions atomic.Int32
	executor := executorFunc(func(context.Context, uuid.UUID, pipeline.PlanJob) error {
		executions.Add(1)
		return nil
	})
	client := &WorkClient{}
	response := &runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_LeaseRenewed{LeaseRenewed: &runnerv1.LeaseRenewed{
		JobId: job.id.String(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}}}
	if err := client.handleResponse(t.Context(), &fakeWorkStream{}, executor, active, make(chan executionResult, 1),
		make(chan uuid.UUID, 1), &sync.WaitGroup{}, response); err != nil {
		t.Fatal(err)
	}
	if executions.Load() != 0 || active[job.id] != nil {
		t.Fatal("lease-lost job started or remained active")
	}
}

type recordingExecutor struct {
	started chan uuid.UUID
}

type executorFunc func(context.Context, uuid.UUID, pipeline.PlanJob) error

func (function executorFunc) Execute(ctx context.Context, jobID uuid.UUID, plan pipeline.PlanJob) error {
	return function(ctx, jobID, plan)
}

type fakeWorkStream struct {
	sent []*runnerv1.WorkRequest
}

func (stream *fakeWorkStream) Send(request *runnerv1.WorkRequest) error {
	stream.sent = append(stream.sent, request)
	return nil
}
func (*fakeWorkStream) Recv() (*runnerv1.WorkResponse, error) { return nil, context.Canceled }
func (*fakeWorkStream) Header() (metadata.MD, error)          { return nil, nil }
func (*fakeWorkStream) Trailer() metadata.MD                  { return nil }
func (*fakeWorkStream) CloseSend() error                      { return nil }
func (*fakeWorkStream) Context() context.Context              { return context.Background() }
func (*fakeWorkStream) SendMsg(any) error                     { return nil }
func (*fakeWorkStream) RecvMsg(any) error                     { return context.Canceled }

func (executor *recordingExecutor) Execute(_ context.Context, jobID uuid.UUID, _ pipeline.PlanJob) error {
	executor.started <- jobID
	return nil
}
