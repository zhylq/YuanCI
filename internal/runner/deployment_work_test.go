package runner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/pipeline"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDeploymentCancellationWaitsForExecutorCleanupAndCompletionAck(t *testing.T) {
	for _, loss := range []string{"cancel", "expired", "rejected"} {
		t.Run(loss, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			job := &localJob{id: uuid.New(), leaseToken: "original-assignment", leaseExpires: time.Now().Add(time.Minute), phase: jobRunning, plan: pipeline.PlanJob{Deployment: "production"}, ctx: ctx, cancel: cancel, source: &localSource{credential: []byte("private")}}
			active := map[uuid.UUID]*localJob{job.id: job}
			started, release := make(chan struct{}), make(chan struct{})
			results := make(chan executionResult, 1)
			var executions atomic.Int32
			executor := executorFunc(func(ctx context.Context, _ uuid.UUID, _ pipeline.PlanJob) error {
				executions.Add(1)
				close(started)
				<-ctx.Done()
				<-release
				return ctx.Err()
			})
			go executeJob(ctx, executor, job, results)
			<-started
			stream := &fakeWorkStream{}
			client := &WorkClient{}
			var response *runnerv1.WorkResponse
			switch loss {
			case "cancel":
				response = &runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_Cancel{Cancel: &runnerv1.CancelJob{JobId: job.id.String()}}}
			case "expired":
				job.leaseExpires = time.Now().Add(-time.Minute)
				job.authorityLost.Store(true)
				response = &runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_LeaseRenewed{LeaseRenewed: &runnerv1.LeaseRenewed{JobId: job.id.String(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}}}
			case "rejected":
				response = &runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_JobRejected{JobRejected: &runnerv1.JobRejected{JobId: job.id.String()}}}
			}
			if err := client.handleResponse(t.Context(), stream, executor, active, results, make(chan uuid.UUID, 1), &sync.WaitGroup{}, response); err != nil {
				t.Fatal(err)
			}
			if active[job.id] != job || allZero(job.source.credential) || len(stream.sent) != 0 {
				t.Fatal("cleanup in progress was forgotten or acknowledged early")
			}
			close(release)
			var result executionResult
			select {
			case result = <-results:
			case <-time.After(time.Second):
				t.Fatal("canceled job result was lost")
			}
			if !result.cleanupConfirmed || result.conclusion != runnerv1.JobConclusion_JOB_CONCLUSION_CANCELED {
				t.Fatalf("result: %+v", result)
			}
			if err := recordExecutionResult(stream, active, result); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := resendTransition(stream, job); err != nil {
					t.Fatal(err)
				}
			}
			for _, request := range stream.sent {
				if message := request.GetJobCompleted(); message == nil || message.LeaseToken != "original-assignment" || !message.CleanupConfirmed {
					t.Fatal("reconnect replayed commands or lost cleanup completion")
				}
			}
			if executions.Load() != 1 || active[job.id] == nil {
				t.Fatal("completion dispatched again or cleared before ack")
			}
			ack := &runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_JobCompletionAcknowledged{JobCompletionAcknowledged: &runnerv1.JobCompletionAcknowledged{JobId: job.id.String()}}}
			if err := client.handleResponse(t.Context(), stream, executor, active, results, nil, &sync.WaitGroup{}, ack); err != nil {
				t.Fatal(err)
			}
			if active[job.id] != nil || !allZero(job.source.credential) {
				t.Fatal("ack did not release local job")
			}
		})
	}
}

func TestDeploymentStoppedBeforeStartReportsCleanupWithoutExecuting(t *testing.T) {
	for _, phase := range []localJobPhase{jobAwaitingAcceptance, jobAwaitingStart} {
		ctx, cancel := context.WithCancel(t.Context())
		job := &localJob{id: uuid.New(), leaseToken: "lease", phase: phase, plan: pipeline.PlanJob{Deployment: "production"}, ctx: ctx, cancel: cancel}
		active := map[uuid.UUID]*localJob{job.id: job}
		stream := &fakeWorkStream{}
		if err := stopLocalJob(stream, active, job); err != nil {
			t.Fatal(err)
		}
		var executions atomic.Int32
		executor := executorFunc(func(context.Context, uuid.UUID, pipeline.PlanJob) error { executions.Add(1); return nil })
		response := &runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_LeaseRenewed{LeaseRenewed: &runnerv1.LeaseRenewed{JobId: job.id.String(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}}}
		if err := (&WorkClient{}).handleResponse(t.Context(), stream, executor, active, make(chan executionResult, 1), nil, &sync.WaitGroup{}, response); err != nil {
			t.Fatal(err)
		}
		if executions.Load() != 0 || !stream.sent[len(stream.sent)-1].GetJobCompleted().CleanupConfirmed || active[job.id] == nil {
			t.Fatal("pending cancellation executed or lost completion")
		}
	}
}

func TestDeploymentCleanupFailureReportsFalseAndUsesProtocolThree(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	job := &localJob{id: uuid.New(), plan: pipeline.PlanJob{Deployment: "production"}, ctx: ctx, cancel: cancel}
	results := make(chan executionResult, 1)
	executeJob(ctx, executorFunc(func(context.Context, uuid.UUID, pipeline.PlanJob) error {
		return errors.Join(context.Canceled, ErrDeploymentCleanupUnconfirmed)
	}), job, results)
	if result := <-results; completionRequest(job, result).GetJobCompleted().CleanupConfirmed {
		t.Fatal("unconfirmed cleanup reported true")
	}
	caps := credentialCapabilities()
	caps.IsolationLevel = runnerv1.IsolationLevel_ISOLATION_LEVEL_DEPLOYMENT
	stream := &fakeWorkStream{}
	if err := sendHeartbeat(stream, caps, nil); err != nil {
		t.Fatal(err)
	}
	if stream.sent[0].GetHeartbeat().ProtocolVersion != 3 {
		t.Fatal("deployment did not use v3")
	}
}
