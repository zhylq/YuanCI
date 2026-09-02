package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/project"
)

func scanAutomation(row pgx.Row) (project.AutomationSettings, error) {
	var settings project.AutomationSettings
	err := row.Scan(&settings.Enabled, &settings.PipelinePath, &settings.TriggerPush, &settings.TriggerTag,
		&settings.TriggerPullRequest, &settings.CancelOlderCommits, &settings.Revision)
	return settings, err
}

func (s *Store) GetProjectAutomation(ctx context.Context, token string, id uuid.UUID) (project.AutomationSettings, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return project.AutomationSettings{}, err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return project.AutomationSettings{}, err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: id}, authorization.ResourceRead); err != nil {
		return project.AutomationSettings{}, err
	}
	settings, err := scanAutomation(tx.QueryRow(ctx, `SELECT enabled,pipeline_path,trigger_push,trigger_tag,
        trigger_pull_request,cancel_older_commits,revision FROM repository_automation_settings WHERE repository_id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		settings, err = project.DefaultAutomationSettings(), nil
	}
	if err != nil {
		return project.AutomationSettings{}, err
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return project.AutomationSettings{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return project.AutomationSettings{}, err
	}
	return settings, nil
}

func (s *Store) UpdateProjectAutomation(ctx context.Context, token string, id uuid.UUID, update project.AutomationUpdate) (project.AutomationSettings, error) {
	if err := update.Validate(); err != nil {
		return project.AutomationSettings{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return project.AutomationSettings{}, err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return project.AutomationSettings{}, err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: id}, authorization.RepositoryManage); err != nil {
		return project.AutomationSettings{}, err
	}
	// Enabling is intentionally closed until the immutable config-validation
	// flow records a successful validation in the next orchestration increment.
	if update.Enabled {
		return project.AutomationSettings{}, project.ErrAutomationNotReady
	}
	// A repository-scoped transaction lock closes the create-on-first-write
	// race while avoiding a heavyweight row update on the repository itself.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,714928))`, id); err != nil {
		return project.AutomationSettings{}, err
	}
	var current int64
	err = tx.QueryRow(ctx, `SELECT revision FROM repository_automation_settings WHERE repository_id=$1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		current, err = 0, nil
	}
	if err != nil {
		return project.AutomationSettings{}, err
	}
	if current != update.ExpectedRevision {
		return project.AutomationSettings{}, project.ErrAutomationConflict
	}
	settings, err := scanAutomation(tx.QueryRow(ctx, `INSERT INTO repository_automation_settings
        (repository_id,enabled,pipeline_path,trigger_push,trigger_tag,trigger_pull_request,cancel_older_commits,revision)
        VALUES($1,$2,$3,$4,$5,$6,$7,1)
        ON CONFLICT(repository_id) DO UPDATE SET enabled=EXCLUDED.enabled,pipeline_path=EXCLUDED.pipeline_path,
          trigger_push=EXCLUDED.trigger_push,trigger_tag=EXCLUDED.trigger_tag,
          trigger_pull_request=EXCLUDED.trigger_pull_request,cancel_older_commits=EXCLUDED.cancel_older_commits,
          revision=repository_automation_settings.revision+1,updated_at=clock_timestamp()
        RETURNING enabled,pipeline_path,trigger_push,trigger_tag,trigger_pull_request,cancel_older_commits,revision`,
		id, update.Enabled, update.PipelinePath, update.TriggerPush, update.TriggerTag,
		update.TriggerPullRequest, update.CancelOlderCommits))
	if err != nil {
		return project.AutomationSettings{}, err
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return project.AutomationSettings{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_user_id,action,resource_type,resource_id,metadata)
        VALUES($1,'repository.automation_updated','repository',$2,
          jsonb_build_object('enabled',$3::boolean,'pipeline_path',$4::text,'trigger_push',$5::boolean,
            'trigger_tag',$6::boolean,'trigger_pull_request',$7::boolean,'cancel_older_commits',$8::boolean,'revision',$9::bigint))`,
		session.UserID, id.String(), settings.Enabled, settings.PipelinePath, settings.TriggerPush, settings.TriggerTag,
		settings.TriggerPullRequest, settings.CancelOlderCommits, settings.Revision)
	if err != nil {
		return project.AutomationSettings{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return project.AutomationSettings{}, err
	}
	return settings, nil
}
