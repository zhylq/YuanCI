package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/commitstatus"
)

func (s *Store) ClaimCommitStatus(ctx context.Context, leaseDuration time.Duration) (*commitstatus.Item, error) {
	if leaseDuration <= 0 || leaseDuration > 15*time.Minute {
		return nil, commitstatus.ErrInvalid
	}
	var item commitstatus.Item
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM commit_status_outbox
		WHERE delivery_state='queued' AND available_at<=clock_timestamp() AND expires_at>clock_timestamp()
		ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE commit_status_outbox o SET delivery_state='processing',lease_owner=gen_random_uuid(),
		lease_expires_at=clock_timestamp()+$1::interval,attempt_count=attempt_count+1,updated_at=clock_timestamp()
	FROM candidate WHERE o.id=candidate.id
	RETURNING o.id,o.repository_id,o.run_id,o.provider,o.commit_sha,o.context,o.commit_state,o.description,
		COALESCE(o.target_url,''),o.deterministic_key,o.delivery_state,o.attempt_count,o.available_at,o.expires_at,
		o.lease_owner,o.lease_expires_at`, leaseDuration.String()).Scan(&item.ID, &item.RepositoryID, &item.RunID,
		&item.Provider, &item.CommitSHA, &item.Context, &item.State, &item.Description, &item.TargetURL,
		&item.DeterministicKey, &item.DeliveryState, &item.AttemptCount, &item.AvailableAt, &item.ExpiresAt,
		&item.LeaseOwner, &item.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim commit status: %w", err)
	}
	return &item, nil
}

func (s *Store) RecoverCommitStatusLeases(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, commitstatus.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `WITH expired AS (
		SELECT id FROM commit_status_outbox WHERE delivery_state='processing'
		AND lease_expires_at<=clock_timestamp() ORDER BY lease_expires_at,id
		FOR UPDATE SKIP LOCKED LIMIT $1
	)
	UPDATE commit_status_outbox o SET
		delivery_state=CASE WHEN o.expires_at<=clock_timestamp() THEN 'dead' ELSE 'queued' END,
		lease_owner=NULL,lease_expires_at=NULL,updated_at=clock_timestamp()
	FROM expired WHERE o.id=expired.id`, limit)
	if err != nil {
		return 0, fmt.Errorf("recover commit status leases: %w", err)
	}
	return int(command.RowsAffected()), nil
}

var _ commitstatus.RecoveryRepository = (*Store)(nil)
