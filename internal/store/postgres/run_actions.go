package postgres

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/identity"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

func lockAuthorizedRun(ctx context.Context, tx pgx.Tx, token string, projectID, runID uuid.UUID, action authorization.Action) (identity.Session, runmodel.Record, error) {
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return session, runmodel.Record{}, err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: projectID}, action); err != nil {
		return session, runmodel.Record{}, err
	}
	rows, err := tx.Query(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1 AND repository_id=$2 FOR UPDATE`, runID, projectID)
	if err != nil {
		return session, runmodel.Record{}, err
	}
	records, err := scanRuns(rows, 1)
	if err != nil {
		return session, runmodel.Record{}, err
	}
	if len(records) != 1 {
		return session, runmodel.Record{}, authorization.ErrForbidden
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return session, runmodel.Record{}, err
	}
	return session, records[0], nil
}

func (s *Store) CancelAuthorizedRun(ctx context.Context, token string, projectID, runID uuid.UUID) (runmodel.Status, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	session, record, err := lockAuthorizedRun(ctx, tx, token, projectID, runID, authorization.RunCancel)
	if err != nil {
		return "", err
	}
	if record.Status.Terminal() {
		return record.Status, tx.Commit(ctx)
	}
	// Run -> Job lock ordering matches assignment, completion and recovery.
	_, err = tx.Exec(ctx, `UPDATE jobs SET status='canceled',finished_at=clock_timestamp(),lease_token_hash=NULL,lease_expires_at=NULL
 WHERE run_id=$1 AND status IN ('blocked','queued','assigned','running')`, runID)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE runs SET status='canceled',finished_at=clock_timestamp() WHERE id=$1`, runID)
	if err != nil {
		return "", err
	}
	if err := enqueueCommitStatusForRun(ctx, tx, runID, runmodel.StatusCanceled); err != nil {
		return "", err
	}
	if err := appendAudit(ctx, tx, session.UserID, "run.canceled", "run", runID); err != nil {
		return "", err
	}
	return runmodel.StatusCanceled, tx.Commit(ctx)
}
