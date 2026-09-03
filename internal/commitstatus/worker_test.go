package commitstatus

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/scm"
)

type workerStore struct {
	item        *Item
	claimable   bool
	finishErr   error
	finished    int
	recovered   int
	rescheduled int
	replayActor uuid.UUID
	replayItem  uuid.UUID
	availableAt time.Time
	errorCode   string
	dead        bool
}

func (store *workerStore) ClaimCommitStatus(context.Context, time.Duration) (*Item, error) {
	if !store.claimable {
		return nil, nil
	}
	store.claimable = false
	copy := *store.item
	return &copy, nil
}
func (store *workerStore) RecoverCommitStatusLeases(context.Context, int) (int, error) {
	if store.recovered == 0 {
		store.recovered++
		store.claimable = true
		store.item.LeaseOwner = uuid.New()
		return 1, nil
	}
	return 0, nil
}
func (store *workerStore) FinishCommitStatus(context.Context, uuid.UUID, uuid.UUID) error {
	store.finished++
	err := store.finishErr
	store.finishErr = nil
	return err
}
func (store *workerStore) RescheduleCommitStatus(_ context.Context, _, _ uuid.UUID, availableAt time.Time, errorCode string, dead bool) error {
	store.rescheduled++
	store.availableAt, store.errorCode, store.dead = availableAt, errorCode, dead
	return nil
}
func (store *workerStore) ReplayCommitStatus(_ context.Context, itemID, actorID uuid.UUID) error {
	store.replayItem, store.replayActor = itemID, actorID
	return nil
}

type workerProvider struct {
	calls int
	err   error
}

func (provider *workerProvider) Deliver(context.Context, Item) error {
	provider.calls++
	return provider.err
}

func TestWorkerRecoversCrashAfterProviderSuccessBeforeAcknowledgement(t *testing.T) {
	item := &Item{ID: uuid.New(), LeaseOwner: uuid.New(), AttemptCount: 1, ExpiresAt: time.Now().Add(time.Hour)}
	store := &workerStore{item: item, claimable: true, finishErr: errors.New("database interrupted")}
	provider := &workerProvider{}
	worker, err := NewWorker(store, provider, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	worker.processOne(t.Context())
	if provider.calls != 1 || store.finished != 1 || store.claimable {
		t.Fatal("first delivery did not stop in the acknowledgement crash window")
	}
	worker.recover(t.Context())
	worker.processOne(t.Context())
	if provider.calls != 2 || store.finished != 2 {
		t.Fatal("expired crash-window lease was not safely redelivered")
	}
}

func TestWorkerSchedulesRateLimitsAndDeadLettersExpiry(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	item := &Item{ID: uuid.New(), LeaseOwner: uuid.New(), AttemptCount: 1, ExpiresAt: now.Add(time.Hour)}
	store := &workerStore{item: item, claimable: true}
	worker, _ := NewWorker(store, &workerProvider{err: scm.ErrRateLimited}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	worker.now = func() time.Time { return now }
	worker.processOne(t.Context())
	if store.rescheduled != 1 || !store.availableAt.Equal(now.Add(time.Minute)) || store.errorCode != "provider_rate_limited" || store.dead {
		t.Fatalf("persisted rate limit decision: %#v", store)
	}
	next, code, dead := worker.retryDecision(Item{AttemptCount: 1, ExpiresAt: now.Add(time.Hour)}, scm.ErrRateLimited)
	if !next.Equal(now.Add(time.Minute)) || code != "provider_rate_limited" || dead {
		t.Fatalf("rate limit decision: %s %s %v", next, code, dead)
	}
	_, _, dead = worker.retryDecision(Item{AttemptCount: 8, ExpiresAt: now.Add(time.Hour)}, errors.New("remote"))
	if !dead {
		t.Fatal("attempt limit did not dead-letter status")
	}
	_, _, dead = worker.retryDecision(Item{AttemptCount: 1, ExpiresAt: now.Add(500 * time.Millisecond)}, errors.New("remote"))
	if !dead {
		t.Fatal("expiry did not dead-letter status")
	}
}

func TestAdminReplayRequiresActorAndItem(t *testing.T) {
	store := &workerStore{}
	service, err := NewAdminService(store)
	if err != nil {
		t.Fatal(err)
	}
	actorID, itemID := uuid.New(), uuid.New()
	if err := service.Replay(t.Context(), actorID, itemID); err != nil {
		t.Fatal(err)
	}
	if store.replayActor != actorID || store.replayItem != itemID {
		t.Fatal("admin replay did not preserve actor and item identity")
	}
	if err := service.Replay(t.Context(), uuid.Nil, itemID); !errors.Is(err, ErrInvalid) {
		t.Fatal("anonymous replay was accepted")
	}
}
