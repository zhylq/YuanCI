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
