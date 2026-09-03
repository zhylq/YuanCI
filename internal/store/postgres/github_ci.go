package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/githubci"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/project"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/scm"
)

func (s *Store) RuntimeAutomation(ctx context.Context, repositoryID uuid.UUID) (project.AutomationSettings, error) {
	if repositoryID == uuid.Nil {
		return project.AutomationSettings{}, githubci.ErrInvalidCommit
	}
	settings, err := scanAutomation(s.pool.QueryRow(ctx, `SELECT COALESCE(s.enabled,false),
		COALESCE(s.pipeline_path,'.yuanci.yml'),COALESCE(s.trigger_push,true),COALESCE(s.trigger_tag,true),
		COALESCE(s.trigger_pull_request,true),COALESCE(s.cancel_older_commits,true),COALESCE(s.revision,0)
		FROM repositories r LEFT JOIN repository_automation_settings s ON s.repository_id=r.id
		WHERE r.id=$1 AND r.active`, repositoryID))
	if errors.Is(err, pgx.ErrNoRows) {
		return project.AutomationSettings{}, githubci.ErrRepositoryUnavailable
	}
	return settings, err
}

func (s *Store) RuntimeAutomationForGitHub(ctx context.Context, externalID string) (uuid.UUID, project.AutomationSettings, error) {
	if !identity.ValidGitHubSubject(externalID) {
		return uuid.Nil, project.AutomationSettings{}, githubci.ErrRepositoryUnavailable
	}
	var repositoryID uuid.UUID
	var settings project.AutomationSettings
	err := s.pool.QueryRow(ctx, `SELECT r.id,COALESCE(s.enabled,false),
		COALESCE(s.pipeline_path,'.yuanci.yml'),COALESCE(s.trigger_push,true),COALESCE(s.trigger_tag,true),
		COALESCE(s.trigger_pull_request,true),COALESCE(s.cancel_older_commits,true),COALESCE(s.revision,0)
		FROM repositories r LEFT JOIN repository_automation_settings s ON s.repository_id=r.id
		WHERE r.provider='github' AND r.provider_instance=$1 AND r.external_id=$2 AND r.active`,
		identity.GitHubInstance, externalID).Scan(&repositoryID, &settings.Enabled, &settings.PipelinePath,
		&settings.TriggerPush, &settings.TriggerTag, &settings.TriggerPullRequest,
		&settings.CancelOlderCommits, &settings.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, project.AutomationSettings{}, githubci.ErrRepositoryUnavailable
	}
	return repositoryID, settings, err
}

func (s *Store) CommitWebhookRun(ctx context.Context, request githubci.RunCommit) (githubci.RunResult, error) {
	if err := validateWebhookRunCommit(request); err != nil {
		return githubci.RunResult{}, err
	}
	planJSON, err := json.Marshal(request.Plan)
	if err != nil {
		return githubci.RunResult{}, githubci.ErrInvalidCommit
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return githubci.RunResult{}, fmt.Errorf("begin GitHub run commit: %w", err)
	}
	defer tx.Rollback(ctx)
	var deliveryID string
	err = tx.QueryRow(ctx, `SELECT delivery_id FROM webhook_deliveries
		WHERE id=$1 AND status='processing' AND lease_owner=$2 AND lease_expires_at>clock_timestamp()
		FOR UPDATE`, request.Delivery.ID, request.Delivery.LeaseID).Scan(&deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return githubci.RunResult{}, githubhook.ErrLeaseInvalid
	}
	if err != nil {
		return githubci.RunResult{}, fmt.Errorf("lock GitHub delivery: %w", err)
	}
	event := request.Delivery.Event
	idempotencyKey := "github-webhook:" + request.Delivery.ID.String()
	result := githubci.RunResult{ID: uuid.New(), Created: true}
	var existingRepositoryID *uuid.UUID
	var existingCommitSHA, existingEvent, existingPipelineName, existingConfigSHA string
	err = tx.QueryRow(ctx, `SELECT id,repository_id,COALESCE(commit_sha,''),event,pipeline_name,config_sha256
		FROM runs WHERE idempotency_key=$1`, idempotencyKey).Scan(&result.ID, &existingRepositoryID,
		&existingCommitSHA, &existingEvent, &existingPipelineName, &existingConfigSHA)
	if err == nil {
		if existingRepositoryID == nil || *existingRepositoryID != request.RepositoryID ||
			existingCommitSHA != event.AfterSHA || existingEvent != string(event.Type) ||
			existingPipelineName != request.Plan.Name || existingConfigSHA != request.Plan.ConfigSHA256 {
			return githubci.RunResult{}, githubci.ErrInvalidCommit
		}
		result.Created = false
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return githubci.RunResult{}, fmt.Errorf("read idempotent GitHub run: %w", err)
	}
	if !result.Created {
		if err := finishWebhookRun(ctx, tx, request, result.ID); err != nil {
			return githubci.RunResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return githubci.RunResult{}, fmt.Errorf("commit idempotent GitHub run: %w", err)
		}
		return result, nil
	}
	var definitionID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO pipeline_definitions(repository_id,name,config_path)
		VALUES($1,$2,$3) ON CONFLICT(repository_id,name) DO UPDATE SET config_path=EXCLUDED.config_path
		RETURNING id`, request.RepositoryID, request.Plan.Name, request.PipelinePath).Scan(&definitionID)
	if err != nil {
		return githubci.RunResult{}, fmt.Errorf("persist pipeline definition: %w", err)
	}
	var versionID uuid.UUID
	err = tx.QueryRow(ctx, `WITH inserted AS (
		INSERT INTO pipeline_versions(pipeline_id,config_sha256,source_yaml,compiled_plan)
		VALUES($1,$2,$3,$4) ON CONFLICT(pipeline_id,config_sha256) DO NOTHING RETURNING id
	)
	SELECT id FROM inserted UNION ALL
	SELECT id FROM pipeline_versions WHERE pipeline_id=$1 AND config_sha256=$2 LIMIT 1`,
		definitionID, request.Plan.ConfigSHA256, string(request.PipelineSource), planJSON).Scan(&versionID)
	if err != nil {
		return githubci.RunResult{}, fmt.Errorf("persist pipeline version: %w", err)
	}
	projectID := request.RepositoryID
	record := runmodel.Record{
		ID: result.ID, ProjectID: &projectID, PipelineVersionID: &versionID, IdempotencyKey: idempotencyKey,
		PipelineName: request.Plan.Name, Event: string(event.Type), Ref: event.Ref,
		CommitSHA: event.AfterSHA, Status: runmodel.StatusQueued, ConfigSHA256: request.Plan.ConfigSHA256,
		Plan: planJSON, CreatedAt: request.CreatedAt.UTC(),
	}
	if err := insertRun(ctx, tx, record); err != nil {
		return githubci.RunResult{}, err
	}
	if err := finishWebhookRun(ctx, tx, request, result.ID); err != nil {
		return githubci.RunResult{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(action,resource_type,resource_id,metadata)
		VALUES('webhook.run_created','run',$1,jsonb_build_object('delivery_id',$2::text,
		'repository_id',$3::text,'commit_sha',$4::text))`, result.ID.String(), deliveryID,
		request.RepositoryID.String(), event.AfterSHA)
	if err != nil {
		return githubci.RunResult{}, fmt.Errorf("audit GitHub run creation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return githubci.RunResult{}, fmt.Errorf("commit GitHub run: %w", err)
	}
	return result, nil
}

func finishWebhookRun(ctx context.Context, tx pgx.Tx, request githubci.RunCommit, runID uuid.UUID) error {
	command, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET status='processed',processed_at=clock_timestamp(),
		repository_id=$3,run_id=$4,lease_owner=NULL,lease_expires_at=NULL,error_code=NULL,error_summary=NULL
		WHERE id=$1 AND status='processing' AND lease_owner=$2`, request.Delivery.ID, request.Delivery.LeaseID,
		request.RepositoryID, runID)
	if err != nil {
		return fmt.Errorf("complete GitHub delivery: %w", err)
	}
	if command.RowsAffected() != 1 {
		return githubhook.ErrLeaseInvalid
	}
	return nil
}

func validateWebhookRunCommit(request githubci.RunCommit) error {
	event := request.Delivery.Event
	if request.Delivery.ID == uuid.Nil || request.Delivery.LeaseID == uuid.Nil || request.RepositoryID == uuid.Nil ||
		event.Provider != scm.GitHub || event.DeliveryID == "" || event.AfterSHA == "" ||
		request.Plan.Version != pipeline.APIVersion || request.Plan.Name == "" || len(request.Plan.ConfigSHA256) != 64 ||
		len(request.PipelineSource) == 0 || len(request.PipelineSource) > 1<<20 || request.CreatedAt.IsZero() ||
		project.ValidatePipelinePath(request.PipelinePath) != nil {
		return githubci.ErrInvalidCommit
	}
	return nil
}

var _ githubci.Store = (*Store)(nil)
