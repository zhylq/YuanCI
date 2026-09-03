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
	if update.Enabled {
		var ready bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM repository_automation_validations v
			JOIN repositories r ON r.id=v.repository_id AND r.active AND r.provider='github'
			JOIN github_installations i ON i.id=r.github_installation_id
			JOIN github_app_configs a ON a.app_id=i.app_id AND a.id=v.app_revision
			JOIN login_configs l ON l.id=a.login_config_id AND l.status='active'
			WHERE v.repository_id=$1 AND v.settings_revision=$2 AND v.pipeline_path=$3)`,
			id, current, update.PipelinePath).Scan(&ready); err != nil {
			return project.AutomationSettings{}, err
		}
		if !ready {
			return project.AutomationSettings{}, project.ErrAutomationNotReady
		}
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

func (s *Store) GetProjectAutomationValidationTarget(ctx context.Context, token string, id uuid.UUID, expectedRevision int64) (project.AutomationValidationTarget, error) {
	if id == uuid.Nil || expectedRevision < 0 {
		return project.AutomationValidationTarget{}, project.ErrAutomationInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return project.AutomationValidationTarget{}, err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return project.AutomationValidationTarget{}, err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: id}, authorization.RepositoryManage); err != nil {
		return project.AutomationValidationTarget{}, err
	}
	var target project.AutomationValidationTarget
	err = tx.QueryRow(ctx, `SELECT r.external_id,COALESCE(s.pipeline_path,$2),COALESCE(s.revision,0)
		FROM repositories r LEFT JOIN repository_automation_settings s ON s.repository_id=r.id
		WHERE r.id=$1 AND r.active AND r.provider='github'`, id, project.DefaultPipelinePath).
		Scan(&target.RepositoryExternalID, &target.PipelinePath, &target.SettingsRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.AutomationValidationTarget{}, authorization.ErrForbidden
	}
	if err != nil {
		return project.AutomationValidationTarget{}, err
	}
	if target.SettingsRevision != expectedRevision {
		return project.AutomationValidationTarget{}, project.ErrAutomationConflict
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return project.AutomationValidationTarget{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return project.AutomationValidationTarget{}, err
	}
	return target, nil
}

func (s *Store) RecordProjectAutomationValidation(ctx context.Context, token string, validation project.AutomationValidation) error {
	if err := validation.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: validation.RepositoryID}, authorization.RepositoryManage); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,714928))`, validation.RepositoryID); err != nil {
		return err
	}
	var pipelinePath string
	var revision int64
	var appRevision uuid.UUID
	err = tx.QueryRow(ctx, `SELECT COALESCE(s.pipeline_path,$2),COALESCE(s.revision,0),a.id
		FROM repositories r
		JOIN github_installations i ON i.id=r.github_installation_id
		JOIN github_app_configs a ON a.app_id=i.app_id
		JOIN login_configs l ON l.id=a.login_config_id AND l.status='active'
		LEFT JOIN repository_automation_settings s ON s.repository_id=r.id
		WHERE r.id=$1 AND r.active AND r.provider='github'`, validation.RepositoryID, project.DefaultPipelinePath).
		Scan(&pipelinePath, &revision, &appRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.ErrAutomationNotReady
	}
	if err != nil {
		return err
	}
	if revision != validation.SettingsRevision || pipelinePath != validation.PipelinePath || appRevision != validation.AppRevision {
		return project.ErrAutomationConflict
	}
	if _, err := tx.Exec(ctx, `INSERT INTO repository_automation_validations
		(repository_id,settings_revision,pipeline_path,app_revision,commit_sha,config_sha256,pipeline_name,validated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(repository_id) DO UPDATE SET settings_revision=EXCLUDED.settings_revision,
		pipeline_path=EXCLUDED.pipeline_path,app_revision=EXCLUDED.app_revision,commit_sha=EXCLUDED.commit_sha,
		config_sha256=EXCLUDED.config_sha256,pipeline_name=EXCLUDED.pipeline_name,validated_at=EXCLUDED.validated_at`,
		validation.RepositoryID, validation.SettingsRevision, validation.PipelinePath, validation.AppRevision,
		validation.CommitSHA, validation.ConfigSHA256, validation.PipelineName, validation.ValidatedAt.UTC()); err != nil {
		return err
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_user_id,action,resource_type,resource_id,metadata)
		VALUES($1,'repository.automation_validated','repository',$2,
		jsonb_build_object('settings_revision',$3::bigint,'pipeline_path',$4::text,'app_revision',$5::text,
		'commit_sha',$6::text,'config_sha256',$7::text,'pipeline_name',$8::text))`,
		session.UserID, validation.RepositoryID.String(), validation.SettingsRevision, validation.PipelinePath,
		validation.AppRevision.String(), validation.CommitSHA, validation.ConfigSHA256, validation.PipelineName)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ project.AutomationStore = (*Store)(nil)
