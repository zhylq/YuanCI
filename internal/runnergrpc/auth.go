package runnergrpc

import (
	"context"
	"crypto/x509"

	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/runnerauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type identityContextKey struct{}

func IdentityFromContext(ctx context.Context) (runnerauth.Identity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(runnerauth.Identity)
	return identity, ok
}

func unaryAuthenticator(auth *runnerauth.Service) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == runnerv1.RunnerService_Register_FullMethodName {
			return handler(ctx, request)
		}
		identity, err := authenticatePeer(ctx, auth)
		if err != nil {
			return nil, err
		}
		return handler(context.WithValue(ctx, identityContextKey{}, identity), request)
	}
}

func streamAuthenticator(auth *runnerauth.Service) grpc.StreamServerInterceptor {
	return func(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		identity, err := authenticatePeer(stream.Context(), auth)
		if err != nil {
			return err
		}
		return handler(service, &identityStream{ServerStream: stream, ctx: context.WithValue(stream.Context(), identityContextKey{}, identity)})
	}
}

type identityStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (stream *identityStream) Context() context.Context { return stream.ctx }

func authenticatePeer(ctx context.Context, auth *runnerauth.Service) (runnerauth.Identity, error) {
	remote, ok := peer.FromContext(ctx)
	if !ok {
		return runnerauth.Identity{}, authenticationError()
	}
	tlsInfo, ok := remote.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) < 1 || len(tlsInfo.State.VerifiedChains[0]) < 2 || len(tlsInfo.State.PeerCertificates) == 0 {
		return runnerauth.Identity{}, authenticationError()
	}
	leaf := tlsInfo.State.PeerCertificates[0]
	if leaf == nil || !sameCertificate(leaf, tlsInfo.State.VerifiedChains[0][0]) {
		return runnerauth.Identity{}, authenticationError()
	}
	identity, err := auth.Authenticate(ctx, leaf)
	if err != nil {
		return runnerauth.Identity{}, authenticationError()
	}
	return identity, nil
}

func sameCertificate(left, right *x509.Certificate) bool {
	return left != nil && right != nil && string(left.Raw) == string(right.Raw)
}

func authenticationError() error {
	return status.Error(codes.Unauthenticated, "Runner authentication failed")
}
