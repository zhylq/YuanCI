package postgres

import (
	"context"
	"encoding/json"
	"errors"
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

func (s *Store) GetAuthorizedRun(ctx context.Context, token string, projectID, runID uuid.UUID) (runmodel.Detail, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return runmodel.Detail{}, err
	}
	defer tx.Rollback(ctx)
	_, record, err := lockAuthorizedRun(ctx, tx, token, projectID, runID, authorization.RunRead)
	if err != nil {
		return runmodel.Detail{}, err
	}
	// Job details carry the execution specs; avoid duplicating the full plan.
	record.Plan = nil
	detail := runmodel.Detail{Run: record, Jobs: []runmodel.JobDetail{}}
	rows, err := tx.Query(ctx, `SELECT id,stage_name,job_name,status,spec,started_at,finished_at,reused_from_job_id FROM jobs WHERE run_id=$1 ORDER BY created_at,id LIMIT 1025`, runID)
	if err != nil {
		return detail, err
	}
	for rows.Next() {
		var job runmodel.JobDetail
		var spec []byte
		if err := rows.Scan(&job.ID, &job.StageName, &job.JobName, &job.Status, &spec, &job.StartedAt, &job.FinishedAt, &job.ReusedFrom); err != nil {
			rows.Close()
			return detail, err
		}
		if err := json.Unmarshal(spec, &job.Spec); err != nil {
			rows.Close()
			return detail, err
		}
		detail.Jobs = append(detail.Jobs, job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return detail, err
	}
	if len(detail.Jobs) > 1024 {
		return runmodel.Detail{}, errors.New("Run detail limit exceeded")
	}
	return detail, tx.Commit(ctx)
}

func (s *Store) ReadAuthorizedLogs(ctx context.Context, token string, projectID, runID, jobID uuid.UUID, after int64) (runmodel.LogPage, error) {
	page := runmodel.LogPage{Items: []runmodel.LogChunk{}, NextSequence: after}
	if after < 0 || after > runmodel.MaxJobLogChunks {
		return page, runmodel.ErrInvalidLogChunk
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if _, _, err := lockAuthorizedRun(ctx, tx, token, projectID, runID, authorization.RunRead); err != nil {
		return page, err
	}
	err = tx.QueryRow(ctx, `SELECT COALESCE(log.expires_at,job.created_at+interval '7 days')<=clock_timestamp() FROM jobs job LEFT JOIN job_log_streams log ON log.job_id=job.id WHERE job.id=$1 AND job.run_id=$2`, jobID, runID).Scan(&page.Expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, authorization.ErrForbidden
	}
	if err != nil {
		return page, err
	}
	if page.Expired {
		return page, tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `SELECT sequence,step_index,stream,data,truncated FROM job_log_chunks WHERE job_id=$1 AND sequence>$2 ORDER BY sequence LIMIT 16`, jobID, after)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var c runmodel.LogChunk
		if err := rows.Scan(&c.Sequence, &c.Step, &c.Stream, &c.Data, &c.Truncated); err != nil {
			rows.Close()
			return page, err
		}
		page.Items = append(page.Items, c)
		page.NextSequence = c.Sequence
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	return page, tx.Commit(ctx)
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
