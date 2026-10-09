package postgres

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

const MaxDeploymentRecoveryReasonBytes = 1024

var ErrDeploymentRecoveryDenied = errors.New("cleanup confirmation requires a terminal deployment Run and a terminal started job")

// ConfirmDeploymentStopped records an administrator's physical verification.
// The database credential is the authority; this method does not stop execution.
func (s *Store) ConfirmDeploymentStopped(ctx context.Context, jobID uuid.UUID, reason string) error {
	reason = strings.TrimSpace(reason)
	if jobID == uuid.Nil || reason == "" || len(reason) > MaxDeploymentRecoveryReasonBytes || !utf8.ValidString(reason) {
		return errors.New("invalid deployment cleanup confirmation options")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var runID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT run_id FROM jobs WHERE id=$1`, jobID).Scan(&runID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeploymentRecoveryDenied
		}
		return err
	}
	// Run -> Job ordering matches cancellation and Runner completion.
	var runStatus runmodel.Status
	var deployment bool
	if err := tx.QueryRow(ctx, `SELECT status,EXISTS(SELECT 1 FROM deployment_runs WHERE run_id=runs.id) FROM runs WHERE id=$1 FOR UPDATE`, runID).Scan(&runStatus, &deployment); err != nil {
		return err
	}
	var jobStatus runmodel.JobStatus
	var started, finished bool
	var runnerID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status,started_at IS NOT NULL,execution_finished_at IS NOT NULL,runner_id FROM jobs WHERE id=$1 AND run_id=$2 FOR UPDATE`, jobID, runID).Scan(&jobStatus, &started, &finished, &runnerID); err != nil {
		return err
	}
	if !deployment || !runStatus.Terminal() || !jobStatus.Terminal() || !started || runnerID == nil {
		return ErrDeploymentRecoveryDenied
	}
	if finished {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET execution_finished_at=clock_timestamp() WHERE id=$1`, jobID); err != nil {
		return err
	}
	if err := releaseDeploymentRun(ctx, tx, runID); err != nil {
		return err
	}
	// Use the authenticated database identity, never an operator-supplied user.
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(action,resource_type,resource_id,metadata)
	 VALUES('deployment.cleanup_confirmed','job',$1,jsonb_build_object('job_id',$1::text,'run_id',$2::text,'runner_id',$3::text,'reason',$4::text,'operator',session_user,'authority','database_admin'))`, jobID.String(), runID.String(), runnerID.String(), reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
