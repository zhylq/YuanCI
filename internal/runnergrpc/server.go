package runnergrpc

import (
	"context"
	"crypto/tls"
	"errors"
	"time"

	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
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
)

type Server struct {
	runnerv1.UnimplementedRunnerServiceServer
	auth              *runnerauth.Service
	rootPEM           []byte
	registrationSlots chan struct{}
}

func NewServer(auth *runnerauth.Service, rootPEM []byte, tlsConfig *tls.Config) (*grpc.Server, error) {
	if auth == nil || len(rootPEM) == 0 || tlsConfig == nil || tlsConfig.MinVersion < tls.VersionTLS13 ||
		tlsConfig.ClientAuth != tls.VerifyClientCertIfGiven || len(tlsConfig.Certificates) == 0 || tlsConfig.ClientCAs == nil {
		return nil, errors.New("invalid Runner gRPC security configuration")
	}
	implementation := &Server{auth: auth, rootPEM: append([]byte(nil), rootPEM...), registrationSlots: make(chan struct{}, 16)}
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
	if request == nil || request.ProtocolVersion != 1 || request.Capabilities == nil ||
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
	if request == nil || request.ProtocolVersion != 1 || len(request.CsrPem) == 0 || len(request.CsrPem) > runnerauth.MaxCSRBytes {
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
	if _, ok := IdentityFromContext(stream.Context()); !ok {
		return authenticationError()
	}
	return status.Error(codes.Unimplemented, "Runner work channel is enabled in the next protocol batch")
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
		ProtocolVersion: int(protocol), RunnerVersion: "protocol-v1"}, nil
}

func boundedContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= maximum {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, maximum)
}
