package githubci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/scm"
)

var ErrInvalidDelivery = errors.New("invalid GitHub CI delivery")
var ErrInvalidPipeline = errors.New("invalid GitHub pipeline configuration")

const (
	retryBaseDelay = 5 * time.Second
	retryMaxDelay  = 15 * time.Minute
)

type Outcome string

const (
	OutcomeIgnoredDisabled  Outcome = "ignored_disabled"
	OutcomeIgnoredFork      Outcome = "ignored_fork"
	OutcomeIgnoredTrigger   Outcome = "ignored_trigger"
	OutcomeRunCreated       Outcome = "run_created"
	OutcomeRunReused        Outcome = "run_reused"
	OutcomeFailedRunCreated Outcome = "failed_run_created"
	OutcomeFailedRunReused  Outcome = "failed_run_reused"
	OutcomeRetryScheduled   Outcome = "retry_scheduled"
	OutcomeDeadLettered     Outcome = "dead_lettered"
)

type PipelineFetcher interface {
	FetchPipeline(context.Context, scm.Event, string) (githubapp.Repository, []byte, error)
}

// Orchestrator processes and finalizes one already-claimed delivery. Claiming,
// lease recovery and the worker loop remain outside this boundary.
type Orchestrator struct {
	store   Store
	fetcher PipelineFetcher
	now     func() time.Time
}

func NewOrchestrator(store Store, fetcher PipelineFetcher) (*Orchestrator, error) {
	if store == nil || fetcher == nil {
		return nil, errors.New("GitHub CI orchestrator requires store and pipeline fetcher")
	}
	return &Orchestrator{store: store, fetcher: fetcher, now: time.Now}, nil
}

func (o *Orchestrator) Process(ctx context.Context, delivery githubhook.WorkItem) (Outcome, error) {
	outcome, err := o.process(ctx, delivery)
	if err == nil {
		return outcome, nil
	}
	if delivery.ID == uuid.Nil || delivery.LeaseID == uuid.Nil {
		return "", ErrInvalidDelivery
	}
	return o.finalizeFailure(ctx, delivery, err)
}

func (o *Orchestrator) process(ctx context.Context, delivery githubhook.WorkItem) (Outcome, error) {
	if delivery.ID == uuid.Nil || delivery.LeaseID == uuid.Nil || (delivery.Event.Provider != scm.GitHub && delivery.Event.Provider != scm.Gitee) ||
		delivery.Event.Repository.ExternalID == "" || delivery.Attempt < 1 {
		return "", ErrInvalidDelivery
	}
	var repositoryID uuid.UUID
	var settings project.AutomationSettings
	var err error
	if delivery.Event.Provider == scm.Gitee {
		store, ok := o.store.(interface {
			RuntimeAutomationForProvider(context.Context, scm.Provider, string) (uuid.UUID, project.AutomationSettings, error)
		})
		if !ok {
			return "", ErrInvalidDelivery
		}
		repositoryID, settings, err = store.RuntimeAutomationForProvider(ctx, scm.Gitee, delivery.Event.Repository.ExternalID)
	} else {
		repositoryID, settings, err = o.store.RuntimeAutomationForGitHub(ctx, delivery.Event.Repository.ExternalID)
	}
	if err != nil {
		return "", err
	}
	if !settings.Enabled {
		return o.ignore(ctx, delivery, OutcomeIgnoredDisabled, "automation_disabled", "Project automation is disabled")
	}
	if delivery.Event.Type == scm.EventPullRequest && delivery.Event.Metadata["fork"] != "false" {
		return o.ignore(ctx, delivery, OutcomeIgnoredFork, "external_fork", "External fork pull request is not trusted")
	}
	if !triggerEnabled(settings, delivery.Event.Type) {
		return o.ignore(ctx, delivery, OutcomeIgnoredTrigger, "trigger_disabled", "Event type is not enabled for this project")
	}
	repository, source, err := o.fetcher.FetchPipeline(ctx, delivery.Event, settings.PipelinePath)
	if err != nil {
		if errors.Is(err, scm.ErrNotFound) {
			return o.commitConfigurationFailure(ctx, delivery, repositoryID, settings.PipelinePath, nil, classifyFailure(err))
		}
		return "", err
	}
	if repository.ID != repositoryID {
		return "", ErrRepositoryMismatch
	}
	now := o.now().UTC()
	plan, err := pipeline.Compile(source, now)
	if err != nil {
		return o.commitConfigurationFailure(ctx, delivery, repositoryID, settings.PipelinePath, source,
			classifyFailure(fmt.Errorf("%w: %v", ErrInvalidPipeline, err)))
	}
	result, err := o.store.CommitWebhookRun(ctx, RunCommit{
		Delivery: delivery, RepositoryID: repositoryID, PipelinePath: settings.PipelinePath,
		PipelineSource: source, Plan: plan, CreatedAt: now,
	})
	if err != nil {
		return "", err
	}
	if result.Created {
		return OutcomeRunCreated, nil
	}
	return OutcomeRunReused, nil
}

func (o *Orchestrator) commitConfigurationFailure(ctx context.Context, delivery githubhook.WorkItem,
	repositoryID uuid.UUID, pipelinePath string, source []byte, failure failureClass) (Outcome, error) {
	digest := sha256.Sum256(source)
	result, err := o.store.CommitWebhookFailedRun(ctx, FailedRunCommit{
		Delivery: delivery, RepositoryID: repositoryID, PipelinePath: pipelinePath,
		ConfigSHA256: hex.EncodeToString(digest[:]), ErrorCode: failure.code,
		ErrorSummary: failure.summary, CreatedAt: o.now().UTC(),
	})
	if err != nil {
		return "", err
	}
	if result.Created {
		return OutcomeFailedRunCreated, nil
	}
	return OutcomeFailedRunReused, nil
}

func (o *Orchestrator) finalizeFailure(ctx context.Context, delivery githubhook.WorkItem, processErr error) (Outcome, error) {
	failure := classifyFailure(processErr)
	final := githubhook.Finalize{
		ID: delivery.ID, LeaseID: delivery.LeaseID,
		ErrorCode: failure.code, ErrorSummary: failure.summary,
	}
	outcome := OutcomeDeadLettered
	if failure.transient && delivery.Attempt < githubhook.MaxAttempts {
		final.State = githubhook.FinalRetry
		final.NextAttempt = o.now().UTC().Add(retryDelay(delivery.Attempt))
		outcome = OutcomeRetryScheduled
	} else {
		final.State = githubhook.FinalDead
		if failure.transient {
			final.ErrorCode = "retry_exhausted"
			final.ErrorSummary = "GitHub delivery processing failed after the retry limit"
		}
	}
	if err := o.store.FinalizeWebhook(ctx, final); err != nil {
		return "", fmt.Errorf("finalize GitHub delivery failure: %w", err)
	}
	return outcome, nil
}

type failureClass struct {
	transient bool
	code      string
	summary   string
}

func classifyFailure(err error) failureClass {
	switch {
	case errors.Is(err, scm.ErrRateLimited):
		return failureClass{true, "github_rate_limited", "GitHub API rate limit delayed delivery processing"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return failureClass{true, "processing_interrupted", "GitHub delivery processing was interrupted"}
	case errors.Is(err, scm.ErrUnauthorized), errors.Is(err, githubapp.ErrCredentialUnavailable):
		return failureClass{false, "credential_unavailable", "GitHub repository credential is unavailable"}
	case errors.Is(err, scm.ErrNotFound):
		return failureClass{false, "pipeline_not_found", "Pipeline configuration was not found at the event commit"}
	case errors.Is(err, ErrInvalidPipeline):
		return failureClass{false, "pipeline_invalid", "Pipeline configuration is invalid"}
	case errors.Is(err, ErrRepositoryMismatch):
		return failureClass{false, "repository_mismatch", "GitHub repository identity changed during delivery processing"}
	case errors.Is(err, ErrRepositoryUnavailable), errors.Is(err, githubapp.ErrRepositoryUnavailable):
		return failureClass{false, "repository_unavailable", "GitHub repository is unavailable for automation"}
	case errors.Is(err, scm.ErrInvalidHook), errors.Is(err, ErrInvalidDelivery), errors.Is(err, ErrInvalidCommit),
		errors.Is(err, githubapp.ErrInvalidEvent), errors.Is(err, githubapp.ErrExternalFork),
		errors.Is(err, project.ErrAutomationInvalid), errors.Is(err, project.ErrAutomationNotReady):
		return failureClass{false, "delivery_invalid", "GitHub delivery cannot be processed safely"}
	default:
		return failureClass{true, "processing_unavailable", "GitHub delivery processing is temporarily unavailable"}
	}
}

func retryDelay(attempt int) time.Duration {
	delay := retryBaseDelay
	for current := 1; current < attempt && delay < retryMaxDelay; current++ {
		delay *= 2
		if delay > retryMaxDelay {
			delay = retryMaxDelay
		}
	}
	return delay
}

func (o *Orchestrator) ignore(ctx context.Context, delivery githubhook.WorkItem, outcome Outcome, code, summary string) (Outcome, error) {
	err := o.store.FinalizeWebhook(ctx, githubhook.Finalize{
		ID: delivery.ID, LeaseID: delivery.LeaseID, State: githubhook.FinalIgnored,
		ErrorCode: code, ErrorSummary: summary,
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

func triggerEnabled(settings project.AutomationSettings, event scm.EventType) bool {
	switch event {
	case scm.EventPush:
		return settings.TriggerPush
	case scm.EventTag:
		return settings.TriggerTag
	case scm.EventPullRequest:
		return settings.TriggerPullRequest
	default:
		return false
	}
}
