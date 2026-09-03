package runnergrpc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/runnerauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxMessageBytes     = 2 << 20
	registrationTimeout = 15 * time.Second
	heartbeatInterval   = 10 * time.Second
	leaseDuration       = 30 * time.Second
	minimumProtocol     = uint32(1)
	currentProtocol     = uint32(2)
)

func supportedProtocol(version uint32) bool {
	return version >= minimumProtocol && version <= currentProtocol
}

type Server struct {
	runnerv1.UnimplementedRunnerServiceServer
	auth              *runnerauth.Service
	jobs              runmodel.RunnerJobStore
	rootPEM           []byte
	registrationSlots chan struct{}
	sessionsMu        sync.Mutex
	sessions          map[uuid.UUID]struct{}
}

func NewServer(auth *runnerauth.Service, jobs runmodel.RunnerJobStore, rootPEM []byte, tlsConfig *tls.Config) (*grpc.Server, error) {
	if auth == nil || jobs == nil || len(rootPEM) == 0 || tlsConfig == nil || tlsConfig.MinVersion < tls.VersionTLS13 ||
		tlsConfig.ClientAuth != tls.VerifyClientCertIfGiven || len(tlsConfig.Certificates) == 0 || tlsConfig.ClientCAs == nil {
		return nil, errors.New("invalid Runner gRPC security configuration")
	}
	implementation := &Server{auth: auth, jobs: jobs, rootPEM: append([]byte(nil), rootPEM...),
		registrationSlots: make(chan struct{}, 16), sessions: make(map[uuid.UUID]struct{})}
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsConfig.Clone())),
		grpc.MaxRecvMsgSize(maxMessageBytes), grpc.MaxSendMsgSize(maxMessageBytes),
		grpc.MaxConcurrentStreams(256),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: false}),
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: 5 * time.Minute, MaxConnectionAge: 2 * time.Hour,
			MaxConnectionAgeGrace: 30 * time.Second, Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.UnaryInterceptor(unaryAuthenticator(auth)), grpc.StreamInterceptor(streamAuthenticator(auth)),
	)
	runnerv1.RegisterRunnerServiceServer(server, implementation)
	return server, nil
}

func (server *Server) Register(ctx context.Context, request *runnerv1.RegisterRequest) (*runnerv1.RegisterResponse, error) {
	select {
	case server.registrationSlots <- struct{}{}:
		defer func() { <-server.registrationSlots }()
	default:
		return nil, status.Error(codes.ResourceExhausted, "Runner registration capacity exceeded")
	}
	ctx, cancel := boundedContext(ctx, registrationTimeout)
	defer cancel()
	if request == nil || !supportedProtocol(request.ProtocolVersion) || request.Capabilities == nil ||
		len(request.OneTimeToken) > 512 || len(request.Name) > 128 || len(request.CsrPem) > runnerauth.MaxCSRBytes {
		return nil, status.Error(codes.InvalidArgument, "invalid Runner registration request")
	}
	capability, err := capabilities(request.Capabilities, request.ProtocolVersion)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid Runner registration request")
	}
	identity, certificate, err := server.auth.Enroll(ctx, request.OneTimeToken, request.Name, capability, request.CsrPem)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "Runner registration denied")
	}
	return &runnerv1.RegisterResponse{RunnerId: identity.RunnerID.String(), CertificateChainPem: certificate.ChainPEM,
		CaCertificatePem: append([]byte(nil), server.rootPEM...), ExpiresAt: timestamppb.New(certificate.NotAfter),
		HeartbeatInterval: durationpb.New(heartbeatInterval), LeaseDuration: durationpb.New(leaseDuration)}, nil
}

func (server *Server) RotateCertificate(ctx context.Context, request *runnerv1.RotateCertificateRequest) (*runnerv1.RotateCertificateResponse, error) {
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, authenticationError()
	}
	if request == nil || !supportedProtocol(request.ProtocolVersion) || len(request.CsrPem) == 0 || len(request.CsrPem) > runnerauth.MaxCSRBytes {
		return nil, status.Error(codes.InvalidArgument, "invalid certificate rotation request")
	}
	certificate, err := server.auth.Rotate(ctx, identity, request.CsrPem)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "certificate rotation denied")
	}
	return &runnerv1.RotateCertificateResponse{CertificateChainPem: certificate.ChainPEM,
		ExpiresAt: timestamppb.New(certificate.NotAfter), PreviousCertificateValidUntil: timestamppb.New(certificate.PreviousValidUntil)}, nil
}

func (server *Server) Work(stream grpc.BidiStreamingServer[runnerv1.WorkRequest, runnerv1.WorkResponse]) error {
	identity, ok := IdentityFromContext(stream.Context())
	if !ok {
		return authenticationError()
	}
	if !server.beginSession(identity.RunnerID) {
		return status.Error(codes.AlreadyExists, "Runner already has an active work session")
	}
	defer server.endSession(identity.RunnerID)
	for {
		request, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if request == nil || request.Body == nil {
			return status.Error(codes.InvalidArgument, "invalid Runner work message")
		}
		switch body := request.Body.(type) {
		case *runnerv1.WorkRequest_Heartbeat:
			if err := server.handleHeartbeat(stream, identity, body.Heartbeat); err != nil {
				return err
			}
		case *runnerv1.WorkRequest_JobAccepted:
			if err := server.handleReceipt(stream, identity.RunnerID, body.JobAccepted, false); err != nil {
				return err
			}
		case *runnerv1.WorkRequest_JobStarted:
			if err := server.handleReceipt(stream, identity.RunnerID, body.JobStarted, true); err != nil {
				return err
			}
		case *runnerv1.WorkRequest_JobCompleted:
			if err := server.handleCompletion(stream.Context(), identity.RunnerID, body.JobCompleted); err != nil {
				return err
			}
		case *runnerv1.WorkRequest_LogChunk:
			if body.LogChunk == nil || len(body.LogChunk.Data) > 64<<10 {
				return status.Error(codes.InvalidArgument, "invalid Runner log message")
			}
			if err := stream.Send(&runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_JobRejected{JobRejected: &runnerv1.JobRejected{
				JobId: body.LogChunk.JobId, Reason: runnerv1.JobRejectReason_JOB_REJECT_REASON_PROTOCOL_ERROR,
				Detail: "log transport is not enabled"}}}); err != nil {
				return err
			}
		default:
			return status.Error(codes.InvalidArgument, "invalid Runner work message")
		}
	}
}

type receiptMessage interface {
	GetJobId() string
	GetLeaseToken() string
}

func (server *Server) handleHeartbeat(stream grpc.BidiStreamingServer[runnerv1.WorkRequest, runnerv1.WorkResponse],
	identity runnerauth.Identity, heartbeat *runnerv1.Heartbeat) error {
	if heartbeat == nil || !supportedProtocol(heartbeat.ProtocolVersion) || heartbeat.Capabilities == nil ||
		len(heartbeat.ActiveLeases) > runmodel.MaximumHeartbeatJobCount {
		return status.Error(codes.InvalidArgument, "invalid Runner heartbeat")
	}
	capability, err := capabilities(heartbeat.Capabilities, heartbeat.ProtocolVersion)
	if err != nil || capability.IsolationLevel != identity.PoolType ||
		capability.ProtocolVersion != identity.Capabilities.ProtocolVersion {
		return status.Error(codes.InvalidArgument, "invalid Runner heartbeat")
	}
	descriptor := runmodel.RunnerDescriptor{ID: identity.RunnerID, PoolType: identity.PoolType, OS: capability.OS,
		Architecture: capability.Architecture, Executor: capability.Executor, Labels: capability.Labels,
		Capacity: capability.Capacity, AvailableDiskBytes: capability.AvailableDiskBytes,
		ProtocolVersion: capability.ProtocolVersion}
	request := runmodel.HeartbeatRequest{Runner: descriptor, ActiveJobs: make([]runmodel.ActiveLease, 0, len(heartbeat.ActiveLeases))}
	for _, active := range heartbeat.ActiveLeases {
		if active == nil || len(active.LeaseToken) > 512 {
			return status.Error(codes.InvalidArgument, "invalid Runner heartbeat")
		}
		jobID, parseErr := uuid.Parse(active.JobId)
		state := ""
		switch active.State {
		case runnerv1.LocalJobState_LOCAL_JOB_STATE_RECEIVED:
			state = "received"
		case runnerv1.LocalJobState_LOCAL_JOB_STATE_RUNNING:
			state = "running"
		}
		if parseErr != nil || state == "" {
			return status.Error(codes.InvalidArgument, "invalid Runner heartbeat")
		}
		request.ActiveJobs = append(request.ActiveJobs, runmodel.ActiveLease{JobID: jobID, LeaseToken: active.LeaseToken, State: state})
	}
	result, err := server.jobs.RenewRunnerLeases(stream.Context(), request)
	if err != nil {
		return workStoreError(err)
	}
	for _, lease := range result.Jobs {
		var response *runnerv1.WorkResponse
		if lease.Renewed {
			response = &runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_LeaseRenewed{LeaseRenewed: &runnerv1.LeaseRenewed{
				JobId: lease.JobID.String(), ExpiresAt: timestamppb.New(lease.LeaseExpires)}}}
		} else {
			response = &runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_Cancel{Cancel: &runnerv1.CancelJob{
				JobId: lease.JobID.String(), Reason: runnerv1.CancelReason_CANCEL_REASON_LEASE_REVOKED, Detail: "lease is no longer valid"}}}
		}
		if err := stream.Send(response); err != nil {
			return err
		}
	}
	for {
		assignment, err := server.jobs.ClaimRunnerJob(stream.Context(), runmodel.RunnerClaim{RunnerID: identity.RunnerID})
		if err != nil {
			return workStoreError(err)
		}
		if assignment == nil {
			return nil
		}
		plan, err := json.Marshal(assignment.Spec)
		if err != nil || len(plan) > 1<<20 {
			return status.Error(codes.Internal, "Runner assignment could not be encoded")
		}
		if err := stream.Send(&runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_Assignment{Assignment: &runnerv1.JobAssignment{
			JobId: assignment.JobID.String(), RunId: assignment.RunID.String(), LeaseToken: assignment.LeaseToken,
			LeaseExpiresAt: timestamppb.New(assignment.LeaseExpires), ExecutionPlanJson: plan}}}); err != nil {
			return err
		}
	}
}

func (server *Server) handleReceipt(stream grpc.BidiStreamingServer[runnerv1.WorkRequest, runnerv1.WorkResponse],
	runnerID uuid.UUID, message receiptMessage, started bool) error {
	if message == nil || len(message.GetLeaseToken()) > 512 {
		return status.Error(codes.InvalidArgument, "invalid Runner receipt")
	}
	jobID, err := uuid.Parse(message.GetJobId())
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid Runner receipt")
	}
	request := runmodel.LeaseRequest{RunnerID: runnerID, JobID: jobID, LeaseToken: message.GetLeaseToken()}
	var lease runmodel.LeaseState
	if started {
		lease, err = server.jobs.StartRunnerJob(stream.Context(), request)
	} else {
		lease, err = server.jobs.AcknowledgeRunnerJob(stream.Context(), request)
	}
	if err != nil {
		return workStoreError(err)
	}
	return stream.Send(&runnerv1.WorkResponse{Body: &runnerv1.WorkResponse_LeaseRenewed{LeaseRenewed: &runnerv1.LeaseRenewed{
		JobId: lease.JobID.String(), ExpiresAt: timestamppb.New(lease.LeaseExpires)}}})
}

func (server *Server) handleCompletion(ctx context.Context, runnerID uuid.UUID, message *runnerv1.JobCompleted) error {
	if message == nil || len(message.LeaseToken) > 512 {
		return status.Error(codes.InvalidArgument, "invalid Runner completion")
	}
	jobID, err := uuid.Parse(message.JobId)
	statusValue := runmodel.JobStatus("")
	switch message.Conclusion {
	case runnerv1.JobConclusion_JOB_CONCLUSION_SUCCEEDED:
		statusValue = runmodel.JobSucceeded
	case runnerv1.JobConclusion_JOB_CONCLUSION_FAILED:
		statusValue = runmodel.JobFailed
	case runnerv1.JobConclusion_JOB_CONCLUSION_CANCELED:
		statusValue = runmodel.JobCanceled
	}
	if err != nil || statusValue == "" {
		return status.Error(codes.InvalidArgument, "invalid Runner completion")
	}
	if err := server.jobs.CompleteRunnerJob(ctx, runmodel.RunnerCompletion{RunnerID: runnerID,
		JobID: jobID, LeaseToken: message.LeaseToken, Status: statusValue}); err != nil {
		return workStoreError(err)
	}
	return nil
}

func workStoreError(err error) error {
	if errors.Is(err, runmodel.ErrInvalidRunnerRequest) {
		return status.Error(codes.InvalidArgument, "invalid Runner work request")
	}
	if errors.Is(err, runmodel.ErrLeaseInvalid) {
		return status.Error(codes.FailedPrecondition, "Runner lease is no longer valid")
	}
	return status.Error(codes.Internal, "Runner work operation failed")
}

func (server *Server) beginSession(runnerID uuid.UUID) bool {
	server.sessionsMu.Lock()
	defer server.sessionsMu.Unlock()
	if _, exists := server.sessions[runnerID]; exists {
		return false
	}
	server.sessions[runnerID] = struct{}{}
	return true
}

func (server *Server) endSession(runnerID uuid.UUID) {
	server.sessionsMu.Lock()
	delete(server.sessions, runnerID)
	server.sessionsMu.Unlock()
}

func capabilities(input *runnerv1.RunnerCapabilities, protocol uint32) (runnerauth.Capabilities, error) {
	isolation := ""
	switch input.IsolationLevel {
	case runnerv1.IsolationLevel_ISOLATION_LEVEL_STANDARD:
		isolation = "standard"
	case runnerv1.IsolationLevel_ISOLATION_LEVEL_PRIVILEGED:
		isolation = "privileged"
	case runnerv1.IsolationLevel_ISOLATION_LEVEL_DEPLOYMENT:
		isolation = "deployment"
	default:
		return runnerauth.Capabilities{}, errors.New("invalid isolation level")
	}
	if len(input.Labels) > 64 {
		return runnerauth.Capabilities{}, errors.New("too many labels")
	}
	labels := make(map[string]string, len(input.Labels))
	for key, value := range input.Labels {
		if len(key) > 128 || len(value) > 512 {
			return runnerauth.Capabilities{}, errors.New("invalid label")
		}
		labels[key] = value
	}
	return runnerauth.Capabilities{OS: input.Os, Architecture: input.Architecture, Executor: input.Executor,
		IsolationLevel: isolation, Labels: labels, Capacity: int(input.Capacity), AvailableDiskBytes: input.AvailableDiskBytes,
		ProtocolVersion: int(protocol), RunnerVersion: fmt.Sprintf("protocol-v%d", protocol)}, nil
}

func boundedContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= maximum {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, maximum)
}
