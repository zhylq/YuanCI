package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/githubhook"
)

func (s *Store) ReceiveWebhook(ctx context.Context, delivery githubhook.Delivery) (githubhook.Receipt, error) {
	if delivery.Provider != "github" || delivery.ProviderInstance == "" || delivery.DeliveryID == "" ||
		delivery.EventType == "" || len(delivery.PayloadSHA256) != 64 || len(delivery.NormalizedEvent) == 0 || delivery.ReceivedAt.IsZero() {
		return githubhook.Receipt{}, githubhook.ErrInvalidRequest
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return githubhook.Receipt{}, fmt.Errorf("begin webhook receipt: %w", err)
	}
	defer tx.Rollback(ctx)
	receipt := githubhook.Receipt{ID: uuid.New()}
	err = tx.QueryRow(ctx, `INSERT INTO webhook_deliveries
 (id,provider,provider_instance,delivery_id,event_type,signature_valid,payload_sha256,normalized_event,received_at,next_attempt_at)
 VALUES($1,$2,$3,$4,$5,true,$6,$7,$8,$8)
 ON CONFLICT(provider,provider_instance,delivery_id) DO NOTHING RETURNING id`,
		receipt.ID, delivery.Provider, delivery.ProviderInstance, delivery.DeliveryID, delivery.EventType,
		delivery.PayloadSHA256, delivery.NormalizedEvent, delivery.ReceivedAt).Scan(&receipt.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		var eventType, digest string
		if err := tx.QueryRow(ctx, `SELECT id,event_type,payload_sha256 FROM webhook_deliveries
	WHERE provider=$1 AND provider_instance=$2 AND delivery_id=$3`, delivery.Provider, delivery.ProviderInstance, delivery.DeliveryID).Scan(&receipt.ID, &eventType, &digest); err != nil {
			return githubhook.Receipt{}, fmt.Errorf("read duplicate webhook: %w", err)
		}
		if eventType != delivery.EventType || digest != delivery.PayloadSHA256 {
			_, auditErr := tx.Exec(ctx, `INSERT INTO audit_events(action,resource_type,resource_id,metadata)
	 VALUES('webhook.delivery_conflict','webhook_delivery',$1,jsonb_build_object('provider',$2::text,'event_type',$3::text,'payload_sha256',$4::text))`,
				delivery.DeliveryID, delivery.Provider, delivery.EventType, delivery.PayloadSHA256)
			if auditErr != nil {
				return githubhook.Receipt{}, fmt.Errorf("audit webhook conflict: %w", auditErr)
			}
			if err := tx.Commit(ctx); err != nil {
				return githubhook.Receipt{}, fmt.Errorf("commit webhook conflict audit: %w", err)
			}
			return githubhook.Receipt{}, githubhook.ErrConflict
		}
		receipt.Duplicate = true
	} else if err != nil {
		return githubhook.Receipt{}, fmt.Errorf("insert webhook: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return githubhook.Receipt{}, fmt.Errorf("commit webhook receipt: %w", err)
	}
	return receipt, nil
}

var _ githubhook.Store = (*Store)(nil)
