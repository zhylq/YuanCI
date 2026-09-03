package githubci

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/scm"
)

var ErrInvalidDelivery = errors.New("invalid GitHub CI delivery")

type Outcome string

const (
	OutcomeIgnoredDisabled Outcome = "ignored_disabled"
	OutcomeIgnoredFork     Outcome = "ignored_fork"
	OutcomeIgnoredTrigger  Outcome = "ignored_trigger"
	OutcomeRunCreated      Outcome = "run_created"
	OutcomeRunReused       Outcome = "run_reused"
)

type PipelineFetcher interface {
	FetchPipeline(context.Context, scm.Event, string) (githubapp.Repository, []byte, error)
}

// Orchestrator processes one already-claimed delivery. Retry and dead-letter
// decisions intentionally remain outside this single-delivery boundary.
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
	if delivery.ID == uuid.Nil || delivery.LeaseID == uuid.Nil || delivery.Event.Provider != scm.GitHub ||
		delivery.Event.Repository.ExternalID == "" {
		return "", ErrInvalidDelivery
	}
	repositoryID, settings, err := o.store.RuntimeAutomationForGitHub(ctx, delivery.Event.Repository.ExternalID)
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
		return "", err
	}
	if repository.ID != repositoryID {
		return "", ErrRepositoryMismatch
	}
	now := o.now().UTC()
	plan, err := pipeline.Compile(source, now)
	if err != nil {
		return "", err
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
