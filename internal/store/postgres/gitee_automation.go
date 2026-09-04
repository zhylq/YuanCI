package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/githubci"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/scm"
	"github.com/yuanci/yuanci/internal/secrets"
)

const giteeBindingQuery = `SELECT r.id,r.external_id,r.owner,r.name,r.default_branch,a.account_id,r.gitee_authorization_id,COALESCE(h.revision,0),h.encrypted_secret
 FROM repositories r JOIN gitee_accounts a ON a.organization_id=r.organization_id
 JOIN gitee_authorizations g ON g.id=r.gitee_authorization_id
 LEFT JOIN gitee_webhook_configs h ON h.repository_id=r.id
 WHERE r.provider='gitee' AND r.provider_instance='https://gitee.com' AND r.active AND g.status<>'revoked' AND ` + liveGiteeGrant

func giteeBindingRow(row pgx.Row) (gitee.Binding, error) {
	var b gitee.Binding
	var encrypted []byte
	err := row.Scan(&b.ProjectID, &b.ID, &b.Owner, &b.Name, &b.DefaultBranch, &b.AccountID, &b.GrantID, &b.HookRevision, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, gitee.ErrStale
	}
	if err != nil {
		return b, err
	}
	if len(encrypted) > 0 && json.Unmarshal(encrypted, &b.HookSecret) != nil {
		return b, gitee.ErrStale
	}
	return b, nil
}
func (s *Store) ResolveGiteeRepository(ctx context.Context, external string) (gitee.Binding, error) {
	if !identity.ValidGitHubSubject(external) {
		return gitee.Binding{}, gitee.ErrStale
	}
	return giteeBindingRow(s.pool.QueryRow(ctx, giteeBindingQuery+` AND r.external_id=$1`, external))
}
func (s *Store) giteeProjectTx(ctx context.Context, token string, id uuid.UUID, fn func(pgx.Tx, identity.Session, gitee.Binding) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return err
	}
	if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Project, ID: id}, authorization.RepositoryManage); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,714928))`, id); err != nil {
		return err
	}
	binding, err := giteeBindingRow(tx.QueryRow(ctx, giteeBindingQuery+` AND r.id=$1 FOR UPDATE OF r,g`, id))
	if err != nil {
		return err
	}
	if err := fn(tx, session, binding); err != nil {
		return err
	}
	if err := sessionLive(ctx, tx, session); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) GiteeProject(ctx context.Context, token string, id uuid.UUID) (gitee.Binding, error) {
	var result gitee.Binding
	err := s.giteeProjectTx(ctx, token, id, func(_ pgx.Tx, _ identity.Session, b gitee.Binding) error { result = b; return nil })
	return result, err
}
func (s *Store) SaveGiteeWebhook(ctx context.Context, token string, expected gitee.Binding, encrypted secrets.Envelope) error {
	return s.giteeProjectTx(ctx, token, expected.ProjectID, func(tx pgx.Tx, session identity.Session, current gitee.Binding) error {
		if current.HookRevision != expected.HookRevision || current.ID != expected.ID {
			return project.ErrAutomationConflict
		}
		encoded, err := json.Marshal(encrypted)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO gitee_webhook_configs(repository_id,revision,encrypted_secret) VALUES($1,1,$2) ON CONFLICT(repository_id) DO UPDATE SET revision=gitee_webhook_configs.revision+1,encrypted_secret=EXCLUDED.encrypted_secret`, current.ProjectID, encoded)
		if err != nil {
			return err
		}
		return appendAudit(ctx, tx, session.UserID, "gitee.webhook_configured", "repository", current.ProjectID)
	})
}
func giteeAutomation(ctx context.Context, tx pgx.Tx, id uuid.UUID) (project.AutomationSettings, error) {
	return scanAutomation(tx.QueryRow(ctx, `SELECT COALESCE(s.enabled,false),COALESCE(s.pipeline_path,'.yuanci.yml'),COALESCE(s.trigger_push,true),COALESCE(s.trigger_tag,true),COALESCE(s.trigger_pull_request,true),COALESCE(s.cancel_older_commits,true),COALESCE(s.revision,0) FROM repositories r LEFT JOIN repository_automation_settings s ON s.repository_id=r.id WHERE r.id=$1`, id))
}
func (s *Store) GiteeValidationTarget(ctx context.Context, token string, id uuid.UUID, revision int64) (gitee.Binding, project.AutomationSettings, error) {
	var binding gitee.Binding
	var settings project.AutomationSettings
	err := s.giteeProjectTx(ctx, token, id, func(tx pgx.Tx, _ identity.Session, b gitee.Binding) error {
		var err error
		settings, err = giteeAutomation(ctx, tx, id)
		if err != nil {
			return err
		}
		if settings.Revision != revision {
			return project.ErrAutomationConflict
		}
		if b.HookRevision == 0 {
			return project.ErrAutomationNotReady
		}
		binding = b
		return nil
	})
	return binding, settings, err
}
func (s *Store) RecordGiteeValidation(ctx context.Context, token string, expected gitee.Binding, settings project.AutomationSettings, proof githubapp.ValidationProof) error {
	if proof.RepositoryID != expected.ProjectID || proof.AppRevision == uuid.Nil || len(proof.CommitSHA) != 40 || len(proof.ConfigSHA256) != 64 {
		return project.ErrAutomationInvalid
	}
	return s.giteeProjectTx(ctx, token, expected.ProjectID, func(tx pgx.Tx, session identity.Session, binding gitee.Binding) error {
		current, err := giteeAutomation(ctx, tx, binding.ProjectID)
		if err != nil {
			return err
		}
		if binding.HookRevision != expected.HookRevision || binding.GrantID != expected.GrantID || current.Revision != settings.Revision || current.PipelinePath != settings.PipelinePath {
			return project.ErrAutomationConflict
		}
		var revision uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT revision FROM gitee_authorizations WHERE id=$1 AND status='active' AND expires_at>clock_timestamp()`, binding.GrantID).Scan(&revision); err != nil {
			return gitee.ErrStale
		}
		if revision != proof.AppRevision {
			return project.ErrAutomationConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO gitee_automation_validations(repository_id,grant_revision,webhook_revision,settings_revision,pipeline_path,commit_sha,config_sha256) VALUES($1,$2,$3,$4,$5,$6,$7)
  ON CONFLICT(repository_id) DO UPDATE SET grant_revision=EXCLUDED.grant_revision,webhook_revision=EXCLUDED.webhook_revision,settings_revision=EXCLUDED.settings_revision,pipeline_path=EXCLUDED.pipeline_path,commit_sha=EXCLUDED.commit_sha,config_sha256=EXCLUDED.config_sha256`, binding.ProjectID, revision, binding.HookRevision, current.Revision, current.PipelinePath, proof.CommitSHA, proof.ConfigSHA256)
		if err != nil {
			return err
		}
		return appendAudit(ctx, tx, session.UserID, "gitee.automation_validated", "repository", binding.ProjectID)
	})
}

var _ gitee.AutomationStore = (*Store)(nil)

func lockGiteeDelivery(ctx context.Context, tx pgx.Tx, delivery githubhook.WorkItem, id uuid.UUID, path string) error {
	if delivery.Event.Type == scm.EventPullRequest && delivery.Event.Metadata["fork"] != "false" {
		return githubci.ErrInvalidCommit
	}
	normalized, err := json.Marshal(delivery.Event)
	if err != nil {
		return githubci.ErrInvalidCommit
	}
	var found uuid.UUID
	err = tx.QueryRow(ctx, `SELECT r.id FROM repositories r
	JOIN gitee_webhook_configs h ON h.repository_id=r.id
	JOIN gitee_authorizations g ON g.id=r.gitee_authorization_id AND g.status='active' AND g.expires_at>clock_timestamp()
	JOIN repository_automation_settings s ON s.repository_id=r.id AND s.enabled
	JOIN webhook_deliveries d ON d.id=$2 AND d.provider='gitee' AND d.normalized_event=$3
	WHERE r.id=$1 AND r.active AND r.provider='gitee' AND r.provider_instance='https://gitee.com'
	AND r.external_id=d.normalized_event->'repository'->>'external_id'
	AND h.revision::text=d.normalized_event->'metadata'->>'webhook_revision' AND s.pipeline_path=$4
	AND ((d.event_type='push' AND s.trigger_push) OR (d.event_type='tag' AND s.trigger_tag) OR (d.event_type='pull_request' AND s.trigger_pull_request))
	AND `+liveGiteeGrant+` FOR UPDATE OF r,h,g,s`, id, delivery.ID, normalized, path).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return githubci.ErrInvalidCommit
	}
	return err
}
