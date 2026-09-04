package postgres

import (
	"context"
	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"time"
)

func (s *Store) RerunAuthorizedRun(ctx context.Context, token string, projectID, runID uuid.UUID, mode string, requestID uuid.UUID) (runmodel.Record, error) {
	if (mode != "full" && mode != "failed") || requestID == uuid.Nil {
		return runmodel.Record{}, runmodel.ErrRunConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return runmodel.Record{}, err
	}
	defer tx.Rollback(ctx)
	session, original, err := lockAuthorizedRun(ctx, tx, token, projectID, runID, authorization.RunCreate)
	if err != nil {
		return runmodel.Record{}, err
	}
	if !original.Status.Terminal() || (mode == "failed" && original.Status != runmodel.StatusFailed) {
		return runmodel.Record{}, runmodel.ErrRunConflict
	}
	key := "rerun:" + runID.String() + ":" + session.UserID.String() + ":" + mode + ":" + requestID.String()
	rows, err := tx.Query(ctx, `SELECT `+runColumns+` FROM runs WHERE idempotency_key=$1`, key)
	if err != nil {
		return runmodel.Record{}, err
	}
	previous, err := scanRuns(rows, 1)
	if err != nil {
		return runmodel.Record{}, err
	}
	if len(previous) > 0 {
		return previous[0], tx.Commit(ctx)
	}
	var executable int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE run_id=$1`, runID).Scan(&executable); err != nil {
		return runmodel.Record{}, err
	}
	if executable == 0 {
		return runmodel.Record{}, runmodel.ErrRunConflict
	}
	next := original
	next.ID = uuid.New()
	next.Status = runmodel.StatusQueued
	next.CreatedAt = time.Now().UTC()
	next.StartedAt = nil
	next.FinishedAt = nil
	next.CreatedBy = &session.UserID
	next.IdempotencyKey = key
	if err := tx.QueryRow(ctx, `SELECT pipeline_version_id FROM runs WHERE id=$1`, runID).Scan(&next.PipelineVersionID); err != nil {
		return runmodel.Record{}, err
	}
	if err := insertRun(ctx, tx, next); err != nil {
		return runmodel.Record{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET rerun_of=$2,rerun_mode=$3 WHERE id=$1`, next.ID, runID, mode); err != nil {
		return runmodel.Record{}, err
	}
	if mode == "failed" {
		// Preserve the complete immutable plan; successful predecessors are explicit
		// result references. The remaining DAG is scheduled against those successes.
		if _, err := tx.Exec(ctx, `UPDATE jobs AS new SET status='succeeded',finished_at=clock_timestamp(),reused_from_job_id=old.id
 FROM jobs AS old WHERE new.run_id=$1 AND old.run_id=$2 AND new.job_key=old.job_key AND old.status='succeeded'`, next.ID, runID); err != nil {
			return runmodel.Record{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs AS candidate SET status='queued' WHERE run_id=$1 AND status='blocked' AND NOT EXISTS(
 SELECT 1 FROM unnest(candidate.dependencies) AS dependency WHERE NOT EXISTS(
 SELECT 1 FROM jobs AS completed WHERE completed.run_id=$1 AND completed.job_key=dependency AND completed.status='succeeded'))`, next.ID); err != nil {
			return runmodel.Record{}, err
		}
	}
	if err := appendAudit(ctx, tx, session.UserID, "run.rerun", "run", next.ID); err != nil {
		return runmodel.Record{}, err
	}
	return next, tx.Commit(ctx)
}
