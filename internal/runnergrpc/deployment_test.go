package runnergrpc

import (
	"context"
	"io"
	"testing"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/runnerauth"
)

type completionStream struct {
	captureWorkStream
	requests []*runnerv1.WorkRequest
}

func (stream *completionStream) Recv() (*runnerv1.WorkRequest, error) {
	if len(stream.requests) == 0 {
		return nil, io.EOF
	}
	request := stream.requests[0]
	stream.requests = stream.requests[1:]
	return request, nil
}

func TestCompletionProtocolCompatibilityAndCleanupMapping(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		jobs := &assignmentJobStore{}
		server := &Server{jobs: jobs, sessions: make(map[uuid.UUID]struct{})}
		identity := runnerauth.Identity{RunnerID: uuid.New(), Capabilities: runnerauth.Capabilities{ProtocolVersion: version}}
		message := &runnerv1.JobCompleted{JobId: uuid.NewString(), LeaseToken: "original", Conclusion: runnerv1.JobConclusion_JOB_CONCLUSION_CANCELED, CleanupConfirmed: true}
		stream := &completionStream{captureWorkStream: captureWorkStream{ctx: context.WithValue(t.Context(), identityContextKey{}, identity)}, requests: []*runnerv1.WorkRequest{{Body: &runnerv1.WorkRequest_JobCompleted{JobCompleted: message}}}}
		if err := server.Work(stream); err != nil {
			t.Fatal(err)
		}
		if len(jobs.completed) != 1 || !jobs.completed[0].CleanupConfirmed || jobs.completed[0].RunnerID != identity.RunnerID {
			t.Fatal("completion lost authenticated identity or cleanup flag")
		}
		if version < 3 && len(stream.responses) != 0 {
			t.Fatal("old binary received unknown response")
		}
		if version == 3 && (len(stream.responses) != 1 || stream.responses[0].GetJobCompletionAcknowledged().GetJobId() != message.JobId) {
			t.Fatal("v3 completion not acknowledged")
		}
	}
}

func TestStaleCompletionRejectsOnlyTheJob(t *testing.T) {
	jobs := &assignmentJobStore{completionErr: runmodel.ErrLeaseInvalid}
	server := &Server{jobs: jobs, sessions: make(map[uuid.UUID]struct{})}
	identity := runnerauth.Identity{RunnerID: uuid.New(), Capabilities: runnerauth.Capabilities{ProtocolVersion: 3}}
	message := &runnerv1.JobCompleted{JobId: uuid.NewString(), LeaseToken: "original", Conclusion: runnerv1.JobConclusion_JOB_CONCLUSION_CANCELED, CleanupConfirmed: true}
	stream := &completionStream{captureWorkStream: captureWorkStream{ctx: context.WithValue(t.Context(), identityContextKey{}, identity)}, requests: []*runnerv1.WorkRequest{{Body: &runnerv1.WorkRequest_JobCompleted{JobCompleted: message}}, {Body: &runnerv1.WorkRequest_JobCompleted{JobCompleted: message}}}}
	if err := server.Work(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.responses) != 2 || stream.responses[0].GetJobRejected().GetJobId() != message.JobId {
		t.Fatal("stale completion broke the channel")
	}
}
