package githubci

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubhook"
)

func TestWorkerStopsAfterContextCancellation(t *testing.T) {
	store := &workerStore{recovered: make(chan struct{}, 1)}
	worker, err := NewWorker(store, workerProcessorFunc(func(context.Context, githubhook.WorkItem) (Outcome, error) {
		return OutcomeRunCreated, nil
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	worker.idleInterval = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	select {
	case <-store.recovered:
	case <-time.After(time.Second):
		t.Fatal("worker did not perform initial lease recovery")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestDuplicateWorkersProcessClaimedDeliveryOnce(t *testing.T) {
	item := githubhook.WorkItem{ID: uuid.New(), LeaseID: uuid.New()}
	store := &workerStore{items: []githubhook.WorkItem{item}}
	processed := make(chan uuid.UUID, 2)
	processor := workerProcessorFunc(func(_ context.Context, got githubhook.WorkItem) (Outcome, error) {
		processed <- got.ID
		return OutcomeRunCreated, nil
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := NewWorker(store, processor, logger)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewWorker(store, processor, logger)
	if err != nil {
		t.Fatal(err)
	}
	first.idleInterval, second.idleInterval = time.Millisecond, time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{}, 2)
	go func() { first.Run(ctx); done <- struct{}{} }()
	go func() { second.Run(ctx); done <- struct{}{} }()
	select {
	case got := <-processed:
		if got != item.ID {
			t.Fatalf("processed delivery %s, want %s", got, item.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery was not processed")
	}
	time.Sleep(20 * time.Millisecond)
	if len(processed) != 0 {
		t.Fatal("duplicate workers processed one claimed delivery more than once")
	}
	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("worker did not stop")
		}
	}
}

func TestWorkerRecoversLeasesOnEachBoundedTick(t *testing.T) {
	store := &workerStore{}
	worker, err := NewWorker(store, workerProcessorFunc(func(context.Context, githubhook.WorkItem) (Outcome, error) {
		return OutcomeRunCreated, nil
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	worker.idleInterval, worker.recoveryInterval = time.Millisecond, 5*time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	deadline := time.After(time.Second)
	for {
		if store.recoveryCount() >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("lease recovery did not repeat")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	if limit := store.lastRecoveryLimit(); limit != WebhookRecoveryBatch {
		t.Fatalf("recovery batch = %d, want %d", limit, WebhookRecoveryBatch)
	}
}

type workerProcessorFunc func(context.Context, githubhook.WorkItem) (Outcome, error)

func (f workerProcessorFunc) Process(ctx context.Context, item githubhook.WorkItem) (Outcome, error) {
	return f(ctx, item)
}

type workerStore struct {
	mu        sync.Mutex
	items     []githubhook.WorkItem
	recovers  int
	limit     int
	recovered chan struct{}
}

func (s *workerStore) ClaimWebhook(context.Context, time.Duration) (*githubhook.WorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return nil, githubhook.ErrNoDelivery
	}
	item := s.items[0]
	s.items = s.items[1:]
	return &item, nil
}

func (s *workerStore) FinalizeWebhook(context.Context, githubhook.Finalize) error { return nil }

func (s *workerStore) RecoverWebhookLeases(_ context.Context, limit int) (int, error) {
	s.mu.Lock()
	s.recovers++
	s.limit = limit
	if s.recovered != nil {
		select {
		case s.recovered <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
	return 0, nil
}

func (s *workerStore) recoveryCount() int     { s.mu.Lock(); defer s.mu.Unlock(); return s.recovers }
func (s *workerStore) lastRecoveryLimit() int { s.mu.Lock(); defer s.mu.Unlock(); return s.limit }

var _ githubhook.WorkStore = (*workerStore)(nil)
