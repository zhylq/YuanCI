package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/commitstatus"
	"github.com/yuanci/yuanci/internal/githubci"
	"github.com/yuanci/yuanci/internal/githubhook"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestGracefulStopGRPCReturnsAndClosesListener(t *testing.T) {
	server := grpc.NewServer()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(done)
	}()
	gracefulStopGRPC(server, time.Second)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gRPC Serve goroutine did not stop")
	}
}

func TestGitHubWorkerLifecycleStopsWithServer(t *testing.T) {
	store := &serverWebhookStore{recovered: make(chan struct{}, 1)}
	worker, err := githubci.NewWorker(store, serverProcessor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	stop := startGitHubWorker(worker)
	select {
	case <-store.recovered:
	case <-time.After(time.Second):
		t.Fatal("server did not start GitHub worker recovery")
	}
	stop()
}

func TestStatusWorkerLifecycleStopsWithServer(t *testing.T) {
	store := &serverStatusStore{recovered: make(chan struct{}, 1)}
	worker, err := commitstatus.NewWorker(store, serverStatusProvider{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	stop := startStatusWorker(worker)
	select {
	case <-store.recovered:
	case <-time.After(time.Second):
		t.Fatal("server did not start commit status recovery")
	}
	stop()
}

type serverStatusProvider struct{}

func (serverStatusProvider) Deliver(context.Context, commitstatus.Item) error { return nil }

type serverStatusStore struct{ recovered chan struct{} }

func (*serverStatusStore) ClaimCommitStatus(context.Context, time.Duration) (*commitstatus.Item, error) {
	return nil, nil
}
func (store *serverStatusStore) RecoverCommitStatusLeases(context.Context, int) (int, error) {
	select {
	case store.recovered <- struct{}{}:
	default:
	}
	return 0, nil
}
func (*serverStatusStore) FinishCommitStatus(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (*serverStatusStore) RescheduleCommitStatus(context.Context, uuid.UUID, uuid.UUID, time.Time, string, bool) error {
	return nil
}
func (*serverStatusStore) ReplayCommitStatus(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type serverProcessor struct{}

func (serverProcessor) Process(context.Context, githubhook.WorkItem) (githubci.Outcome, error) {
	return githubci.OutcomeRunCreated, nil
}

type serverWebhookStore struct{ recovered chan struct{} }

func (s *serverWebhookStore) ClaimWebhook(context.Context, time.Duration) (*githubhook.WorkItem, error) {
	return nil, githubhook.ErrNoDelivery
}
func (s *serverWebhookStore) FinalizeWebhook(context.Context, githubhook.Finalize) error { return nil }
func (s *serverWebhookStore) RecoverWebhookLeases(context.Context, int) (int, error) {
	select {
	case s.recovered <- struct{}{}:
	default:
	}
	return 0, nil
}

func TestGracefulStopGRPCForcesLongLivedStreamsClosed(t *testing.T) {
	server := grpc.NewServer()
	healthpb.RegisterHealthServer(server, health.NewServer())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(serveDone)
	}()
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	stream, err := healthpb.NewHealthClient(connection).Watch(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	gracefulStopGRPC(server, 20*time.Millisecond)
	if _, err := stream.Recv(); err == nil {
		t.Fatal("long-lived stream survived forced shutdown")
	}
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("Serve goroutine leaked after forced shutdown")
	}
}
