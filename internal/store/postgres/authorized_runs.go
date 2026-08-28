package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

func (s *Store) CreateAuthorizedRun(ctx context.Context, token string, projectID uuid.UUID, record runmodel.Record) (runmodel.Record, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return runmodel.Record{}, err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return runmodel.Record{}, err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: projectID}, authorization.RunCreate); err != nil {
		return runmodel.Record{}, err
	}
	record.ProjectID = &projectID
	record.CreatedBy = &session.UserID
	if err := sessionLive(ctx, tx, session); err != nil {
		return runmodel.Record{}, err
	}
	if err := insertRun(ctx, tx, record); err != nil {
		return runmodel.Record{}, err
	}
	if err := appendAudit(ctx, tx, session.UserID, "run.created", "run", record.ID); err != nil {
		return runmodel.Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return runmodel.Record{}, err
	}
	return record, nil
}

func (s *Store) ListAuthorizedRuns(ctx context.Context, token string, projectID uuid.UUID, limit int) ([]runmodel.Record, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: projectID}, authorization.RunRead); err != nil {
		return nil, err
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+runColumns+` FROM runs WHERE repository_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	records, err := scanRuns(rows, limit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return records, nil
}
