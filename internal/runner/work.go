package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/pipeline"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	workHeartbeatInterval = 5 * time.Second
	workReconnectMinimum  = time.Second
	workReconnectMaximum  = 30 * time.Second
	workMessageLimit      = 2 << 20
	runnerProtocolVersion = 2
	maximumCheckoutToken  = 4 << 10
)

// Executor runs one immutable job plan. Lease cancellation is delivered through
// the context; the Docker implementation performs the actual isolation.
type Executor interface {
	Execute(context.Context, uuid.UUID, pipeline.PlanJob) error
}

type WorkConfig struct {
	Address      string
	ServerName   string
	StateDir     string
	Credentials  Credentials
	Capabilities *runnerv1.RunnerCapabilities
	Logger       *slog.Logger
}

type WorkClient struct {
	config WorkConfig
}

var errCertificateRotationDue = errors.New("Runner certificate rotation is due")

type localJobPhase uint8

const (
	jobAwaitingAcceptance localJobPhase = iota + 1
	jobAwaitingStart
	jobRunning
	jobCompleted
)

type localJob struct {
	id            uuid.UUID
	leaseToken    string
	leaseExpires  time.Time
	plan          pipeline.PlanJob
	phase         localJobPhase
	ctx           context.Context
	cancel        context.CancelFunc
	result        *executionResult
	leaseTimer    *time.Timer
	authorityLost atomic.Bool
	source        *localSource
}

type localSource struct {
	provider     string
	repositoryID string
	cloneURL     string
	commitSHA    string
	credential   []byte
	expiresAt    time.Time
}

type executionResult struct {
	jobID      uuid.UUID
	conclusion runnerv1.JobConclusion
	detail     string
	duration   time.Duration
}

func NewWorkClient(config WorkConfig) (*WorkClient, error) {
	if config.Address == "" || config.ServerName == "" || config.Capabilities == nil ||
		config.Capabilities.Capacity < 1 || config.Capabilities.Capacity > 256 {
		return nil, errors.New("invalid Runner work configuration")
	}
	if _, err := config.Credentials.TLSConfig(config.ServerName); err != nil {
		return nil, err
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &WorkClient{config: config}, nil
}

// Run maintains the authenticated Work stream until ctx is canceled. Active
// jobs survive transport reconnects in memory and are reconciled by lease token.
func (client *WorkClient) Run(ctx context.Context, executor Executor) error {
	if executor == nil {
		return errors.New("Runner executor is required")
	}
	active := make(map[uuid.UUID]*localJob)
	results := make(chan executionResult, client.config.Capabilities.Capacity)
	leaseLosses := make(chan uuid.UUID, client.config.Capabilities.Capacity)
	var executors sync.WaitGroup
	backoff := workReconnectMinimum
	for {
		err := client.runSession(ctx, executor, active, results, leaseLosses, &executors)
		if ctx.Err() != nil {
			cancelJobs(active)
			waitExecutors(&executors, 15*time.Second)
			return nil
		}
		if errors.Is(err, errCertificateRotationDue) {
			rotationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			rotated, rotationErr := RotateCredentials(rotationCtx, RotationConfig{Address: client.config.Address,
				ServerName: client.config.ServerName, StateDir: client.config.StateDir, Current: client.config.Credentials})
			cancel()
			if rotationErr == nil {
				client.config.Credentials = rotated
				backoff = workReconnectMinimum
				client.config.Logger.Info("Runner certificate rotated", "certificate_expires_at", rotated.NotAfter)
				continue
			}
			err = rotationErr
		}
		client.config.Logger.Warn("Runner work stream disconnected", "error", safeWorkError(err), "retry_in", backoff)
		delay := jitter(backoff)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancelJobs(active)
			waitExecutors(&executors, 15*time.Second)
			return nil
		case <-timer.C:
		}
		backoff = min(backoff*2, workReconnectMaximum)
	}
}

func (client *WorkClient) runSession(ctx context.Context, executor Executor, active map[uuid.UUID]*localJob,
	results chan executionResult, leaseLosses chan uuid.UUID, executors *sync.WaitGroup) error {
	tlsConfig, err := client.config.Credentials.TLSConfig(client.config.ServerName)
	if err != nil {
		return err
	}
	connection, err := grpc.NewClient(client.config.Address,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(workMessageLimit), grpc.MaxCallSendMsgSize(workMessageLimit)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: false}))
	if err != nil {
		return errors.New("cannot create Runner work connection")
	}
	defer connection.Close()
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	stream, err := runnerv1.NewRunnerServiceClient(connection).Work(sessionCtx)
	if err != nil {
		return errors.New("cannot open Runner work stream")
	}
	responses := make(chan *runnerv1.WorkResponse, 1)
	receiveErrors := make(chan error, 1)
	go receiveWork(sessionCtx, stream, responses, receiveErrors)

	// Re-send only the transition that may have lost its response. Heartbeats do
	// not stand in for acceptance/start acknowledgements.
	for _, job := range active {
		if err := resendTransition(stream, job); err != nil {
			return err
		}
	}
	if err := sendHeartbeat(stream, client.config.Capabilities, active); err != nil {
		return err
	}
	ticker := time.NewTicker(workHeartbeatInterval)
	defer ticker.Stop()
	rotation := rotationTimer(client.config)
	if rotation != nil {
		defer rotation.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-receiveErrors:
			return err
		case response := <-responses:
			if err := client.handleResponse(ctx, stream, executor, active, results, leaseLosses, executors, response); err != nil {
				return err
			}
		case result := <-results:
			job := active[result.jobID]
			if job == nil || job.phase != jobRunning || job.authorityLost.Load() || !time.Now().Before(job.leaseExpires) {
				if job != nil && (!time.Now().Before(job.leaseExpires) || job.authorityLost.Load()) {
					forgetJob(active, job)
				}
				continue
			}
			job.phase = jobCompleted
			job.result = &result
			if err := stream.Send(completionRequest(job, result)); err != nil {
				return err
			}
		case <-ticker.C:
			if err := sendHeartbeat(stream, client.config.Capabilities, active); err != nil {
				return err
			}
		case <-timerChannel(rotation):
			return errCertificateRotationDue
		case jobID := <-leaseLosses:
			if job := active[jobID]; job != nil && job.authorityLost.Load() {
				forgetJob(active, job)
			}
		}
	}
}

func (client *WorkClient) handleResponse(ctx context.Context,
	stream grpc.BidiStreamingClient[runnerv1.WorkRequest, runnerv1.WorkResponse], executor Executor,
	active map[uuid.UUID]*localJob, results chan<- executionResult, leaseLosses chan<- uuid.UUID,
	executors *sync.WaitGroup, response *runnerv1.WorkResponse) error {
	if response == nil || response.Body == nil {
		return errors.New("invalid Runner work response")
	}
	switch body := response.Body.(type) {
	case *runnerv1.WorkResponse_Assignment:
		job, err := decodeAssignment(body.Assignment)
		if err != nil {
			return err
		}
		if existing := active[job.id]; existing != nil {
			clearJobSource(job)
			if existing.leaseToken != job.leaseToken {
				return errors.New("conflicting Runner assignment")
			}
			return resendTransition(stream, existing)
		}
		if len(active) >= int(client.config.Capabilities.Capacity) {
			clearJobSource(job)
			return errors.New("Runner local capacity exceeded")
		}
		jobCtx, cancel := context.WithCancel(ctx)
		job.ctx = jobCtx
		job.cancel = cancel
		if !armLeaseDeadline(job, leaseLosses) {
			clearJobSource(job)
			return errors.New("Runner assignment lease expired before acceptance")
		}
		active[job.id] = job
		return stream.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_JobAccepted{JobAccepted: &runnerv1.JobAccepted{
			JobId: job.id.String(), LeaseToken: job.leaseToken}}})
	case *runnerv1.WorkResponse_LeaseRenewed:
		if body.LeaseRenewed == nil || body.LeaseRenewed.ExpiresAt == nil || !body.LeaseRenewed.ExpiresAt.IsValid() {
			return errors.New("invalid Runner lease renewal")
		}
		jobID, err := uuid.Parse(body.LeaseRenewed.JobId)
		job := active[jobID]
		if err != nil || job == nil {
			return errors.New("unknown Runner lease renewal")
		}
		if job.authorityLost.Load() {
			forgetJob(active, job)
			return nil
		}
		job.leaseExpires = body.LeaseRenewed.ExpiresAt.AsTime()
		if !resetLeaseDeadline(job, leaseLosses) {
			forgetJob(active, job)
			return nil
		}
		switch job.phase {
		case jobAwaitingAcceptance:
			job.phase = jobAwaitingStart
			return stream.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_JobStarted{JobStarted: &runnerv1.JobStarted{
				JobId: job.id.String(), LeaseToken: job.leaseToken}}})
		case jobAwaitingStart:
			job.phase = jobRunning
			executors.Add(1)
			go func() {
				defer executors.Done()
				executeJob(job.ctx, executor, job, results)
			}()
		case jobCompleted:
			if job.result == nil {
				return errors.New("Runner completion state is invalid")
			}
			return stream.Send(completionRequest(job, *job.result))
		}
		return nil
	case *runnerv1.WorkResponse_Cancel:
		if body.Cancel == nil {
			return errors.New("invalid Runner cancellation")
		}
		jobID, err := uuid.Parse(body.Cancel.JobId)
		if err != nil {
			return errors.New("invalid Runner cancellation")
		}
		if job := active[jobID]; job != nil {
			forgetJob(active, job)
		}
		return nil
	case *runnerv1.WorkResponse_JobRejected:
		if body.JobRejected == nil {
			return errors.New("invalid Runner rejection")
		}
		jobID, err := uuid.Parse(body.JobRejected.JobId)
		if err == nil {
			if job := active[jobID]; job != nil {
				forgetJob(active, job)
			}
		}
		return nil
	default:
		return errors.New("unsupported Runner work response")
	}
}

func decodeAssignment(assignment *runnerv1.JobAssignment) (*localJob, error) {
	if assignment != nil && assignment.Credential != nil {
		defer clear(assignment.Credential.Token)
	}
	if assignment == nil || assignment.LeaseToken == "" || len(assignment.LeaseToken) > 512 ||
		len(assignment.ExecutionPlanJson) == 0 || len(assignment.ExecutionPlanJson) > 1<<20 ||
		assignment.LeaseExpiresAt == nil || !assignment.LeaseExpiresAt.IsValid() {
		return nil, errors.New("invalid Runner assignment")
	}
	jobID, jobErr := uuid.Parse(assignment.JobId)
	_, runErr := uuid.Parse(assignment.RunId)
	if jobErr != nil || runErr != nil || !time.Now().Before(assignment.LeaseExpiresAt.AsTime()) {
		return nil, errors.New("invalid Runner assignment")
	}
	var plan pipeline.PlanJob
	decoder := json.NewDecoder(bytes.NewReader(assignment.ExecutionPlanJson))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF || plan.Name == "" {
		return nil, errors.New("invalid Runner execution plan")
	}
	job := &localJob{id: jobID, leaseToken: assignment.LeaseToken, leaseExpires: assignment.LeaseExpiresAt.AsTime(),
		plan: plan, phase: jobAwaitingAcceptance}
	if (assignment.Source == nil) != (assignment.Credential == nil) {
		return nil, errors.New("invalid Runner source credential")
	}
	if assignment.Source != nil {
		credential := assignment.Credential
		if validateSourceDescriptor(assignment.Source) != nil || len(credential.Token) == 0 ||
			len(credential.Token) > maximumCheckoutToken || credential.ExpiresAt == nil ||
			!credential.ExpiresAt.IsValid() || !time.Now().Before(credential.ExpiresAt.AsTime()) {
			return nil, errors.New("invalid Runner source credential")
		}
		job.source = &localSource{provider: assignment.Source.Provider, repositoryID: assignment.Source.RepositoryId,
			cloneURL: assignment.Source.CloneUrl, commitSHA: assignment.Source.CommitSha,
			credential: append([]byte(nil), credential.Token...), expiresAt: credential.ExpiresAt.AsTime()}
	}
	return job, nil
}

func resendTransition(stream grpc.BidiStreamingClient[runnerv1.WorkRequest, runnerv1.WorkResponse], job *localJob) error {
	switch job.phase {
	case jobAwaitingAcceptance:
		return stream.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_JobAccepted{JobAccepted: &runnerv1.JobAccepted{
			JobId: job.id.String(), LeaseToken: job.leaseToken}}})
	case jobAwaitingStart:
		return stream.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_JobStarted{JobStarted: &runnerv1.JobStarted{
			JobId: job.id.String(), LeaseToken: job.leaseToken}}})
	}
	return nil
}

func sendHeartbeat(stream grpc.BidiStreamingClient[runnerv1.WorkRequest, runnerv1.WorkResponse],
	capabilities *runnerv1.RunnerCapabilities, active map[uuid.UUID]*localJob) error {
	leases := make([]*runnerv1.ActiveLease, 0, len(active))
	for _, job := range active {
		if job.authorityLost.Load() || (job.phase != jobRunning && job.phase != jobCompleted) {
			continue
		}
		leases = append(leases, &runnerv1.ActiveLease{JobId: job.id.String(), LeaseToken: job.leaseToken,
			State: runnerv1.LocalJobState_LOCAL_JOB_STATE_RUNNING})
	}
	return stream.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_Heartbeat{Heartbeat: &runnerv1.Heartbeat{
		Capabilities: capabilities, ActiveLeases: leases, ProtocolVersion: runnerProtocolVersion}}})
}

func executeJob(ctx context.Context, executor Executor, job *localJob, results chan<- executionResult) {
	started := time.Now()
	conclusion := runnerv1.JobConclusion_JOB_CONCLUSION_SUCCEEDED
	detail := ""
	if err := executor.Execute(ctx, job.id, job.plan); err != nil {
		conclusion = runnerv1.JobConclusion_JOB_CONCLUSION_FAILED
		detail = "job execution failed"
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			conclusion = runnerv1.JobConclusion_JOB_CONCLUSION_CANCELED
			detail = "job execution canceled"
		}
	}
	result := executionResult{jobID: job.id, conclusion: conclusion, detail: detail, duration: time.Since(started)}
	select {
	case results <- result:
	case <-ctx.Done():
	}
}

func completionRequest(job *localJob, result executionResult) *runnerv1.WorkRequest {
	return &runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_JobCompleted{JobCompleted: &runnerv1.JobCompleted{
		JobId: job.id.String(), LeaseToken: job.leaseToken, Conclusion: result.conclusion,
		Duration: durationpb.New(result.duration), Detail: result.detail}}}
}

func receiveWork(ctx context.Context, stream grpc.BidiStreamingClient[runnerv1.WorkRequest, runnerv1.WorkResponse],
	responses chan<- *runnerv1.WorkResponse, receiveErrors chan<- error) {
	for {
		response, err := stream.Recv()
		if err != nil {
			select {
			case receiveErrors <- err:
			case <-ctx.Done():
			}
			return
		}
		select {
		case responses <- response:
		case <-ctx.Done():
			return
		}
	}
}

func cancelJobs(active map[uuid.UUID]*localJob) {
	for _, job := range active {
		if job.leaseTimer != nil {
			job.leaseTimer.Stop()
		}
		job.authorityLost.Store(true)
		job.cancel()
		clearJobSource(job)
	}
}

func armLeaseDeadline(job *localJob, losses chan<- uuid.UUID) bool {
	if job.authorityLost.Load() || !time.Now().Before(job.leaseExpires) {
		loseLease(job, losses)
		return false
	}
	job.leaseTimer = time.AfterFunc(time.Until(job.leaseExpires), func() { loseLease(job, losses) })
	return true
}

func resetLeaseDeadline(job *localJob, losses chan<- uuid.UUID) bool {
	if job.authorityLost.Load() {
		return false
	}
	if job.leaseTimer != nil && !job.leaseTimer.Stop() {
		loseLease(job, losses)
		return false
	}
	return armLeaseDeadline(job, losses)
}

func loseLease(job *localJob, losses chan<- uuid.UUID) {
	if !job.authorityLost.CompareAndSwap(false, true) {
		return
	}
	job.cancel()
	select {
	case losses <- job.id:
	default:
	}
}

func forgetJob(active map[uuid.UUID]*localJob, job *localJob) {
	if job.leaseTimer != nil {
		job.leaseTimer.Stop()
	}
	job.authorityLost.Store(true)
	job.cancel()
	clearJobSource(job)
	delete(active, job.id)
}

func clearJobSource(job *localJob) {
	if job != nil && job.source != nil {
		clear(job.source.credential)
	}
}

func waitExecutors(executors *sync.WaitGroup, maximum time.Duration) {
	done := make(chan struct{})
	go func() {
		executors.Wait()
		close(done)
	}()
	timer := time.NewTimer(maximum)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func jitter(value time.Duration) time.Duration {
	if value <= 0 {
		return 0
	}
	return value/2 + time.Duration(rand.Int64N(int64(value/2)+1))
}

func safeWorkError(err error) string {
	if err == nil {
		return "stream closed"
	}
	return fmt.Sprintf("%T", err)
}

func rotationTimer(config WorkConfig) *time.Timer {
	if config.StateDir == "" {
		return nil
	}
	delay := time.Until(config.Credentials.NotAfter.Add(-certificateRotateAhead))
	if delay < 0 {
		delay = 0
	}
	return time.NewTimer(delay)
}

func timerChannel(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}
