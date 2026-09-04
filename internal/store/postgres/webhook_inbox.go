package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/identity"
)

func (s *Store) ReceiveWebhook(ctx context.Context, delivery githubhook.Delivery) (githubhook.Receipt, error) {
	if !identity.ValidProviderInstance(delivery.Provider, delivery.ProviderInstance) || delivery.DeliveryID == "" ||
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

func (s *Store) ClaimWebhook(ctx context.Context, leaseDuration time.Duration) (*githubhook.WorkItem, error) {
	if leaseDuration < time.Second || leaseDuration > 10*time.Minute {
		return nil, githubhook.ErrLeaseInvalid
	}
	leaseID := uuid.New()
	expires := time.Now().UTC().Add(leaseDuration)
	var item githubhook.WorkItem
	var normalized []byte
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
	 SELECT id FROM webhook_deliveries
	 WHERE status='received' AND normalized_event IS NOT NULL AND next_attempt_at<=clock_timestamp()
	 ORDER BY next_attempt_at,received_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE webhook_deliveries d SET status='processing',attempt_count=attempt_count+1,
	 lease_owner=$1,lease_expires_at=$2
	FROM candidate WHERE d.id=candidate.id
	RETURNING d.id,d.normalized_event,d.attempt_count,d.lease_expires_at`, leaseID, expires).Scan(
		&item.ID, &normalized, &item.Attempt, &item.LeaseExpires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, githubhook.ErrNoDelivery
	}
	if err != nil {
		return nil, fmt.Errorf("claim webhook: %w", err)
	}
	if err := json.Unmarshal(normalized, &item.Event); err != nil {
		return nil, fmt.Errorf("decode claimed webhook: %w", err)
	}
	item.LeaseID = leaseID
	return &item, nil
}

func (s *Store) FinalizeWebhook(ctx context.Context, request githubhook.Finalize) error {
	if request.ID == uuid.Nil || request.LeaseID == uuid.Nil || len(request.ErrorCode) > 64 || len(request.ErrorSummary) > 1024 {
		return githubhook.ErrLeaseInvalid
	}
	status := string(request.State)
	processed := request.State != githubhook.FinalRetry
	if request.State == githubhook.FinalRetry {
		status = "received"
		if !request.NextAttempt.After(time.Now().UTC()) {
			return githubhook.ErrLeaseInvalid
		}
	} else if request.State != githubhook.FinalProcessed && request.State != githubhook.FinalIgnored && request.State != githubhook.FinalDead {
		return githubhook.ErrLeaseInvalid
	}
	result, err := s.pool.Exec(ctx, `UPDATE webhook_deliveries SET status=$3,lease_owner=NULL,lease_expires_at=NULL,
	 next_attempt_at=CASE WHEN $4 THEN next_attempt_at ELSE $5 END,
	 processed_at=CASE WHEN $4 THEN clock_timestamp() ELSE NULL END,
	 error_code=NULLIF($6,''),error_summary=NULLIF($7,'')
	WHERE id=$1 AND status='processing' AND lease_owner=$2 AND lease_expires_at>clock_timestamp()`,
		request.ID, request.LeaseID, status, processed, request.NextAttempt, request.ErrorCode, request.ErrorSummary)
	if err != nil {
		return fmt.Errorf("finalize webhook: %w", err)
	}
	if result.RowsAffected() != 1 {
		return githubhook.ErrLeaseInvalid
	}
	return nil
}

func (s *Store) RecoverWebhookLeases(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, githubhook.ErrLeaseInvalid
	}
	rows, err := s.pool.Query(ctx, `WITH expired AS (
	 SELECT id FROM webhook_deliveries WHERE status='processing' AND lease_expires_at<=clock_timestamp()
	 ORDER BY lease_expires_at,id FOR UPDATE SKIP LOCKED LIMIT $1
	)
	UPDATE webhook_deliveries d SET
	 status=CASE WHEN attempt_count >= $2 THEN 'dead_letter' ELSE 'received' END,
	 lease_owner=NULL,lease_expires_at=NULL,next_attempt_at=clock_timestamp(),
	 processed_at=CASE WHEN attempt_count >= $2 THEN clock_timestamp() ELSE NULL END,
	 error_code=CASE WHEN attempt_count >= $2 THEN 'lease_recovery_exhausted' ELSE error_code END,
	 error_summary=CASE WHEN attempt_count >= $2 THEN 'Webhook processing repeatedly lost its worker lease' ELSE error_summary END
	FROM expired WHERE d.id=expired.id RETURNING d.id`, limit, githubhook.MaxAttempts)
	if err != nil {
		return 0, fmt.Errorf("recover webhook leases: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("scan recovered webhook: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("recover webhook leases: %w", err)
	}
	return count, nil
}

var _ githubhook.WorkStore = (*Store)(nil)
