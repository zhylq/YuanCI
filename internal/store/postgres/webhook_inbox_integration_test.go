package postgres

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubhook"
)

func TestWebhookInboxConcurrentIdempotencyAndConflictAudit(t *testing.T) {
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	delivery := githubhook.Delivery{
		Provider: "github", ProviderInstance: "https://github.com", DeliveryID: uuid.NewString(),
		EventType: "push", PayloadSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		NormalizedEvent: []byte(`{"provider":"github","delivery_id":"test","type":"push"}`),
		ReceivedAt:      time.Now().UTC().Truncate(time.Microsecond),
	}
	const workers = 16
	var wg sync.WaitGroup
	results := make(chan githubhook.Receipt, workers)
	errorsFound := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			receipt, err := store.ReceiveWebhook(t.Context(), delivery)
			results <- receipt
			errorsFound <- err
		})
	}
	wg.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	unique := map[uuid.UUID]bool{}
	created := 0
	for receipt := range results {
		unique[receipt.ID] = true
		if !receipt.Duplicate {
			created++
		}
	}
	if created != 1 || len(unique) != 1 {
		t.Fatalf("created=%d unique_ids=%d", created, len(unique))
	}
	conflict := delivery
	conflict.PayloadSHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := store.ReceiveWebhook(t.Context(), conflict); !errors.Is(err, githubhook.ErrConflict) {
		t.Fatalf("expected delivery conflict, got %v", err)
	}
	var deliveries, audits int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM webhook_deliveries`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='webhook.delivery_conflict'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 || audits != 1 {
		t.Fatalf("deliveries=%d audits=%d", deliveries, audits)
	}
}

func TestWebhookInboxLeaseRetryRecoveryAndTerminalState(t *testing.T) {
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	delivery := githubhook.Delivery{
		Provider: "github", ProviderInstance: "https://github.com", DeliveryID: uuid.NewString(), EventType: "push",
		PayloadSHA256:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		NormalizedEvent: []byte(`{"provider":"github","delivery_id":"delivery","type":"push","repository":{"external_id":"42","owner":"acme","name":"widget"},"ref":"refs/heads/main","after_sha":"0123456789abcdef0123456789abcdef01234567","sender":"octocat","received_at":"2026-09-02T00:00:00Z"}`),
		ReceivedAt:      time.Now().UTC(),
	}
	if _, err := store.ReceiveWebhook(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimWebhook(t.Context(), time.Minute)
	if err != nil || first.Attempt != 1 || first.Event.Repository.ExternalID != "42" {
		t.Fatalf("first claim: %#v %v", first, err)
	}
	if _, err := store.ClaimWebhook(t.Context(), time.Minute); !errors.Is(err, githubhook.ErrNoDelivery) {
		t.Fatalf("leased delivery was claimed twice: %v", err)
	}
	if err := store.FinalizeWebhook(t.Context(), githubhook.Finalize{ID: first.ID, LeaseID: uuid.New(), State: githubhook.FinalProcessed}); !errors.Is(err, githubhook.ErrLeaseInvalid) {
		t.Fatalf("wrong lease finalized delivery: %v", err)
	}
	next := time.Now().UTC().Add(20 * time.Millisecond)
	if err := store.FinalizeWebhook(t.Context(), githubhook.Finalize{ID: first.ID, LeaseID: first.LeaseID, State: githubhook.FinalRetry, NextAttempt: next, ErrorCode: "github_timeout", ErrorSummary: "temporary GitHub timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimWebhook(t.Context(), time.Minute); !errors.Is(err, githubhook.ErrNoDelivery) {
		t.Fatalf("retry delay ignored: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE webhook_deliveries SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimWebhook(t.Context(), time.Minute)
	if err != nil || second.Attempt != 2 {
		t.Fatalf("second claim: %#v %v", second, err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE webhook_deliveries SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := store.RecoverWebhookLeases(t.Context(), 100); err != nil || count != 1 {
		t.Fatalf("recover count=%d err=%v", count, err)
	}
	third, err := store.ClaimWebhook(t.Context(), time.Minute)
	if err != nil || third.Attempt != 3 {
		t.Fatalf("third claim: %#v %v", third, err)
	}
	if err := store.FinalizeWebhook(t.Context(), githubhook.Finalize{ID: third.ID, LeaseID: third.LeaseID, State: githubhook.FinalProcessed}); err != nil {
		t.Fatal(err)
	}
	var status string
	var processed *time.Time
	if err := store.pool.QueryRow(t.Context(), `SELECT status,processed_at FROM webhook_deliveries WHERE id=$1`, third.ID).Scan(&status, &processed); err != nil {
		t.Fatal(err)
	}
	if status != "processed" || processed == nil {
		t.Fatalf("status=%s processed=%v", status, processed)
	}
}
