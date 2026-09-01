package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

type expiredJob struct {
	id       uuid.UUID
	status   runmodel.JobStatus
	runnerID *uuid.UUID
}

func (s *Store) RecoverExpiredRunnerLeases(ctx context.Context, limit int) (runmodel.RecoveryResult, error) {
	if limit < 1 || limit > runmodel.MaximumRecoveryBatch {
		return runmodel.RecoveryResult{}, runmodel.ErrInvalidRecoveryLimit
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return runmodel.RecoveryResult{}, fmt.Errorf("begin lease recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `SELECT run_id FROM jobs
        WHERE status IN ('assigned','running') AND lease_expires_at <= clock_timestamp()
        GROUP BY run_id ORDER BY min(lease_expires_at),run_id LIMIT $1`, limit)
	if err != nil {
		return runmodel.RecoveryResult{}, fmt.Errorf("find expired runs: %w", err)
	}
	runIDs := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var runID uuid.UUID
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return runmodel.RecoveryResult{}, fmt.Errorf("scan expired run: %w", err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return runmodel.RecoveryResult{}, fmt.Errorf("read expired runs: %w", err)
	}
	rows.Close()

	result := runmodel.RecoveryResult{}
	processed := 0
	for _, runID := range runIDs {
		if processed >= limit {
			break
		}
		var lockedRun uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM runs WHERE id=$1 FOR UPDATE SKIP LOCKED`, runID).Scan(&lockedRun)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return runmodel.RecoveryResult{}, fmt.Errorf("lock expired run: %w", err)
		}
		jobRows, err := tx.Query(ctx, `SELECT id,status,runner_id FROM jobs
            WHERE run_id=$1 AND status IN ('assigned','running') AND lease_expires_at <= clock_timestamp()
            ORDER BY lease_expires_at,id FOR UPDATE SKIP LOCKED LIMIT $2`, runID, limit-processed)
		if err != nil {
			return runmodel.RecoveryResult{}, fmt.Errorf("lock expired jobs: %w", err)
		}
		jobs := make([]expiredJob, 0, limit-processed)
		for jobRows.Next() {
			var job expiredJob
			if err := jobRows.Scan(&job.id, &job.status, &job.runnerID); err != nil {
				jobRows.Close()
				return runmodel.RecoveryResult{}, fmt.Errorf("scan expired job: %w", err)
			}
			jobs = append(jobs, job)
		}
		if err := jobRows.Err(); err != nil {
			jobRows.Close()
			return runmodel.RecoveryResult{}, fmt.Errorf("read expired jobs: %w", err)
		}
		jobRows.Close()
		failedRun := false
		for _, job := range jobs {
			outcome := "assigned_requeued"
			if job.status == runmodel.JobAssigned {
				command, err := tx.Exec(ctx, `UPDATE jobs SET status='queued',runner_id=NULL,lease_token_hash=NULL,
                    lease_expires_at=NULL,accepted_at=NULL,lease_renewed_at=NULL
                    WHERE id=$1 AND status='assigned' AND lease_expires_at <= clock_timestamp()`, job.id)
				if err != nil {
					return runmodel.RecoveryResult{}, fmt.Errorf("requeue expired job: %w", err)
				}
				if command.RowsAffected() != 1 {
					continue
				}
				result.Requeued++
			} else {
				outcome = "running_failed"
				command, err := tx.Exec(ctx, `UPDATE jobs SET status='failed',failure_reason='runner_lost',
                    finished_at=clock_timestamp(),lease_token_hash=NULL,lease_expires_at=NULL,lease_renewed_at=NULL
                    WHERE id=$1 AND status='running' AND lease_expires_at <= clock_timestamp()`, job.id)
				if err != nil {
					return runmodel.RecoveryResult{}, fmt.Errorf("fail expired job: %w", err)
				}
				if command.RowsAffected() != 1 {
					continue
				}
				if _, err := tx.Exec(ctx, `UPDATE jobs SET status='skipped',finished_at=COALESCE(finished_at,clock_timestamp())
                    WHERE run_id=$1 AND status IN ('blocked','queued')`, runID); err != nil {
					return runmodel.RecoveryResult{}, fmt.Errorf("skip jobs after Runner loss: %w", err)
				}
				failedRun = true
				result.Failed++
			}
			metadata := map[string]any{"outcome": outcome}
			if job.runnerID != nil {
				metadata["runner_id"] = *job.runnerID
			}
			if err := appendRunnerAudit(ctx, tx, nil, "runner_lease.recovered", "job", job.id, metadata); err != nil {
				return runmodel.RecoveryResult{}, fmt.Errorf("audit lease recovery: %w", err)
			}
			processed++
		}
		if failedRun {
			if _, err := tx.Exec(ctx, `UPDATE runs SET status='failed',finished_at=COALESCE(finished_at,clock_timestamp())
                WHERE id=$1`, runID); err != nil {
				return runmodel.RecoveryResult{}, fmt.Errorf("fail run after Runner loss: %w", err)
			}
		} else if len(jobs) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE runs SET status='queued',started_at=NULL,finished_at=NULL
                WHERE id=$1 AND NOT EXISTS (SELECT 1 FROM jobs WHERE run_id=$1
                  AND status IN ('running','succeeded','failed','canceled'))`, runID); err != nil {
				return runmodel.RecoveryResult{}, fmt.Errorf("requeue run after lease expiry: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return runmodel.RecoveryResult{}, fmt.Errorf("commit lease recovery: %w", err)
	}
	return result, nil
}

var _ runmodel.LeaseRecoveryStore = (*Store)(nil)
