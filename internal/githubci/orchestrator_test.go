package githubci

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/scm"
)

const orchestratorPipeline = `version: v1
name: webhook
stages:
  - name: test
    jobs:
      - name: unit
        image: alpine:3.20
        steps:
          - name: test
            commands: [echo ok]
`

type orchestratorStore struct {
	repositoryID       uuid.UUID
	settings           project.AutomationSettings
	policyErr          error
	policyCalls        int
	finalized          []githubhook.Finalize
	finalizeErr        error
	commit             RunCommit
	commitResult       RunResult
	commitErr          error
	failedCommit       FailedRunCommit
	failedCommitResult RunResult
	failedCommitErr    error
}

func (s *orchestratorStore) RuntimeAutomationForGitHub(_ context.Context, externalID string) (uuid.UUID, project.AutomationSettings, error) {
	s.policyCalls++
	if externalID != "70" {
		return uuid.Nil, project.AutomationSettings{}, errors.New("unexpected repository")
	}
	if s.policyErr != nil {
		return uuid.Nil, project.AutomationSettings{}, s.policyErr
	}
	return s.repositoryID, s.settings, nil
}

func (s *orchestratorStore) CommitWebhookRun(_ context.Context, request RunCommit) (RunResult, error) {
	s.commit = request
	return s.commitResult, s.commitErr
}

func (s *orchestratorStore) CommitWebhookFailedRun(_ context.Context, request FailedRunCommit) (RunResult, error) {
	s.failedCommit = request
	return s.failedCommitResult, s.failedCommitErr
}

func (s *orchestratorStore) FinalizeWebhook(_ context.Context, request githubhook.Finalize) error {
	s.finalized = append(s.finalized, request)
	return s.finalizeErr
}

type pipelineFetcher struct {
	repository githubapp.Repository
	source     []byte
	calls      int
	event      scm.Event
	path       string
	err        error
}

func (f *pipelineFetcher) FetchPipeline(_ context.Context, event scm.Event, path string) (githubapp.Repository, []byte, error) {
	f.calls++
	f.event = event
	f.path = path
	return f.repository, append([]byte(nil), f.source...), f.err
}

func TestOrchestratorClassifiesIgnoredDeliveriesBeforeFetching(t *testing.T) {
	repositoryID := uuid.New()
	base := project.DefaultAutomationSettings()
	base.Enabled = true
	tests := []struct {
		name     string
		settings project.AutomationSettings
		event    scm.Event
		outcome  Outcome
		code     string
	}{
		{"disabled", project.DefaultAutomationSettings(), webhookEvent(scm.EventPush), OutcomeIgnoredDisabled, "automation_disabled"},
		{"external fork", base, forkEvent(), OutcomeIgnoredFork, "external_fork"},
		{"push disabled", withoutTrigger(base, scm.EventPush), webhookEvent(scm.EventPush), OutcomeIgnoredTrigger, "trigger_disabled"},
		{"tag disabled", withoutTrigger(base, scm.EventTag), webhookEvent(scm.EventTag), OutcomeIgnoredTrigger, "trigger_disabled"},
		{"pull request disabled", withoutTrigger(base, scm.EventPullRequest), webhookEvent(scm.EventPullRequest), OutcomeIgnoredTrigger, "trigger_disabled"},
		{"unsupported event", base, webhookEvent(scm.EventType("deployment")), OutcomeIgnoredTrigger, "trigger_disabled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &orchestratorStore{repositoryID: repositoryID, settings: test.settings}
			fetcher := &pipelineFetcher{}
			orchestrator, err := NewOrchestrator(store, fetcher)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := orchestrator.Process(t.Context(), workItem(test.event))
			if err != nil || outcome != test.outcome {
				t.Fatalf("process: outcome=%q err=%v", outcome, err)
			}
			if store.policyCalls != 1 || fetcher.calls != 0 || len(store.finalized) != 1 {
				t.Fatalf("unexpected calls: policy=%d fetch=%d finalize=%d", store.policyCalls, fetcher.calls, len(store.finalized))
			}
			if store.finalized[0].State != githubhook.FinalIgnored || store.finalized[0].ErrorCode != test.code {
				t.Fatalf("unexpected finalization: %#v", store.finalized[0])
			}
		})
	}
}

func TestOrchestratorFetchesCompilesAndCommitsRun(t *testing.T) {
	repositoryID := uuid.New()
	settings := project.DefaultAutomationSettings()
	settings.Enabled = true
	store := &orchestratorStore{repositoryID: repositoryID, settings: settings, commitResult: RunResult{ID: uuid.New(), Created: true}}
	fetcher := &pipelineFetcher{repository: githubapp.Repository{ID: repositoryID}, source: []byte(orchestratorPipeline)}
	orchestrator, err := NewOrchestrator(store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 4, 5, 6, 0, time.UTC)
	orchestrator.now = func() time.Time { return now }
	item := workItem(webhookEvent(scm.EventPush))
	outcome, err := orchestrator.Process(t.Context(), item)
	if err != nil || outcome != OutcomeRunCreated {
		t.Fatalf("process: outcome=%q err=%v", outcome, err)
	}
	if fetcher.calls != 1 || len(store.finalized) != 0 {
		t.Fatalf("unexpected calls: fetch=%d finalize=%d", fetcher.calls, len(store.finalized))
	}
	if fetcher.path != project.DefaultPipelinePath || fetcher.event.AfterSHA != item.Event.AfterSHA {
		t.Fatalf("pipeline was not fetched from the configured immutable event: path=%q sha=%q", fetcher.path, fetcher.event.AfterSHA)
	}
	if store.commit.RepositoryID != repositoryID || store.commit.PipelinePath != project.DefaultPipelinePath ||
		string(store.commit.PipelineSource) != orchestratorPipeline || store.commit.Plan.Name != "webhook" ||
		!store.commit.Plan.CompiledAt.Equal(now) || store.commit.Delivery.ID != item.ID || !store.commit.CreatedAt.Equal(now) {
		t.Fatalf("unexpected commit: %#v", store.commit)
	}
}

func TestOrchestratorClassifiesIdempotentRun(t *testing.T) {
	repositoryID := uuid.New()
	settings := project.DefaultAutomationSettings()
	settings.Enabled = true
	store := &orchestratorStore{repositoryID: repositoryID, settings: settings, commitResult: RunResult{ID: uuid.New(), Created: false}}
	fetcher := &pipelineFetcher{repository: githubapp.Repository{ID: repositoryID}, source: []byte(orchestratorPipeline)}
	orchestrator, _ := NewOrchestrator(store, fetcher)
	outcome, err := orchestrator.Process(t.Context(), workItem(webhookEvent(scm.EventTag)))
	if err != nil || outcome != OutcomeRunReused {
		t.Fatalf("process: outcome=%q err=%v", outcome, err)
	}
}

func TestOrchestratorRejectsRepositoryIdentityMismatch(t *testing.T) {
	settings := project.DefaultAutomationSettings()
	settings.Enabled = true
	store := &orchestratorStore{repositoryID: uuid.New(), settings: settings}
	fetcher := &pipelineFetcher{repository: githubapp.Repository{ID: uuid.New()}, source: []byte(orchestratorPipeline)}
	orchestrator, _ := NewOrchestrator(store, fetcher)
	outcome, err := orchestrator.Process(t.Context(), workItem(webhookEvent(scm.EventPush)))
	if err != nil || outcome != OutcomeDeadLettered {
		t.Fatalf("process mismatch: outcome=%q err=%v", outcome, err)
	}
	if len(store.finalized) != 1 || store.finalized[0].ErrorCode != "repository_mismatch" ||
		store.finalized[0].State != githubhook.FinalDead || store.commit.RepositoryID != uuid.Nil {
		t.Fatalf("mismatched repository was not safely dead-lettered: %#v", store.finalized)
	}
}

func TestOrchestratorSchedulesBoundedDeterministicRetries(t *testing.T) {
	now := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	settings := project.DefaultAutomationSettings()
	settings.Enabled = true
	tests := []struct {
		attempt int
		delay   time.Duration
	}{
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{9, 15 * time.Minute},
		{11, 15 * time.Minute},
	}
	for _, test := range tests {
		t.Run(test.delay.String()+"-attempt", func(t *testing.T) {
			store := &orchestratorStore{repositoryID: uuid.New(), settings: settings}
			fetcher := &pipelineFetcher{err: errors.New("dial tcp token=super-secret")}
			orchestrator, _ := NewOrchestrator(store, fetcher)
			orchestrator.now = func() time.Time { return now }
			item := workItem(webhookEvent(scm.EventPush))
			item.Attempt = test.attempt
			outcome, err := orchestrator.Process(t.Context(), item)
			if err != nil || outcome != OutcomeRetryScheduled {
				t.Fatalf("process: outcome=%q err=%v", outcome, err)
			}
			final := store.finalized[0]
			if final.State != githubhook.FinalRetry || !final.NextAttempt.Equal(now.Add(test.delay)) ||
				final.ErrorCode != "processing_unavailable" || final.ErrorSummary != "GitHub delivery processing is temporarily unavailable" {
				t.Fatalf("unexpected retry finalization: %#v", final)
			}
		})
	}
}

func TestOrchestratorDeadLettersPermanentAndExhaustedErrorsWithRedactedSummaries(t *testing.T) {
	now := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	settings := project.DefaultAutomationSettings()
	settings.Enabled = true
	tests := []struct {
		name    string
		attempt int
		fetcher *pipelineFetcher
		code    string
		summary string
	}{
		{"credential unavailable", 1, &pipelineFetcher{err: errors.Join(scm.ErrUnauthorized, errors.New("Authorization: Bearer secret-token"))}, "credential_unavailable", "GitHub repository credential is unavailable"},
		{"retry exhausted", githubhook.MaxAttempts, &pipelineFetcher{err: errors.New("postgres password=database-secret")}, "retry_exhausted", "GitHub delivery processing failed after the retry limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repositoryID := uuid.New()
			store := &orchestratorStore{repositoryID: repositoryID, settings: settings}
			orchestrator, _ := NewOrchestrator(store, test.fetcher)
			orchestrator.now = func() time.Time { return now }
			item := workItem(webhookEvent(scm.EventPush))
			item.Attempt = test.attempt
			outcome, err := orchestrator.Process(t.Context(), item)
			if err != nil || outcome != OutcomeDeadLettered {
				t.Fatalf("process: outcome=%q err=%v", outcome, err)
			}
			final := store.finalized[0]
			if final.State != githubhook.FinalDead || !final.NextAttempt.IsZero() ||
				final.ErrorCode != test.code || final.ErrorSummary != test.summary {
				t.Fatalf("unexpected dead-letter finalization: %#v", final)
			}
		})
	}
}

func TestOrchestratorCommitsVisibleFailedRunForConfigurationErrors(t *testing.T) {
	settings := project.DefaultAutomationSettings()
	settings.Enabled = true
	now := time.Date(2026, 9, 3, 9, 10, 11, 0, time.UTC)
	tests := []struct {
		name       string
		fetcher    *pipelineFetcher
		code       string
		summary    string
		configHash string
	}{
		{"missing pipeline", &pipelineFetcher{err: scm.ErrNotFound}, "pipeline_not_found",
			"Pipeline configuration was not found at the event commit",
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"invalid pipeline", &pipelineFetcher{source: []byte("password: hunter2")}, "pipeline_invalid",
			"Pipeline configuration is invalid",
			"f8a178e0213a2d96850911cb2b43af702e04548f1dc8c0a210d79003f470d551"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repositoryID := uuid.New()
			test.fetcher.repository.ID = repositoryID
			store := &orchestratorStore{repositoryID: repositoryID, settings: settings,
				failedCommitResult: RunResult{ID: uuid.New(), Created: true}}
			orchestrator, _ := NewOrchestrator(store, test.fetcher)
			orchestrator.now = func() time.Time { return now }
			item := workItem(webhookEvent(scm.EventPush))
			outcome, err := orchestrator.Process(t.Context(), item)
			if err != nil || outcome != OutcomeFailedRunCreated {
				t.Fatalf("process: outcome=%q err=%v", outcome, err)
			}
			if len(store.finalized) != 0 || store.commit.RepositoryID != uuid.Nil {
				t.Fatalf("configuration failure used ordinary finalization: final=%#v commit=%#v", store.finalized, store.commit)
			}
			failed := store.failedCommit
			if failed.Delivery.ID != item.ID || failed.RepositoryID != repositoryID ||
				failed.PipelinePath != project.DefaultPipelinePath || failed.ConfigSHA256 != test.configHash ||
				failed.ErrorCode != test.code || failed.ErrorSummary != test.summary || !failed.CreatedAt.Equal(now) {
				t.Fatalf("unexpected failed Run commit: %#v", failed)
			}
		})
	}
}

func TestOrchestratorReturnsFinalizationFailure(t *testing.T) {
	finalizeErr := errors.New("database unavailable")
	settings := project.DefaultAutomationSettings()
	settings.Enabled = true
	store := &orchestratorStore{repositoryID: uuid.New(), settings: settings, finalizeErr: finalizeErr}
	orchestrator, _ := NewOrchestrator(store, &pipelineFetcher{err: scm.ErrRateLimited})
	if _, err := orchestrator.Process(t.Context(), workItem(webhookEvent(scm.EventPush))); !errors.Is(err, finalizeErr) {
		t.Fatalf("expected finalization error, got %v", err)
	}
}

func TestClassifyFailureUsesSafeStableCategories(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		transient bool
		code      string
	}{
		{"rate limit", fmt.Errorf("remote detail with token: %w", scm.ErrRateLimited), true, "github_rate_limited"},
		{"deadline", context.DeadlineExceeded, true, "processing_interrupted"},
		{"credential", githubapp.ErrCredentialUnavailable, false, "credential_unavailable"},
		{"not found", scm.ErrNotFound, false, "pipeline_not_found"},
		{"invalid pipeline", ErrInvalidPipeline, false, "pipeline_invalid"},
		{"repository mismatch", ErrRepositoryMismatch, false, "repository_mismatch"},
		{"repository unavailable", ErrRepositoryUnavailable, false, "repository_unavailable"},
		{"invalid delivery", ErrInvalidDelivery, false, "delivery_invalid"},
		{"unknown store error", errors.New("dsn=postgres://secret"), true, "processing_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyFailure(test.err)
			if got.transient != test.transient || got.code != test.code || got.summary == "" || len(got.summary) > 1024 {
				t.Fatalf("classifyFailure(%v)=%#v", test.err, got)
			}
			if got.summary == test.err.Error() {
				t.Fatal("persisted summary reused the raw error")
			}
		})
	}
}

func withoutTrigger(settings project.AutomationSettings, event scm.EventType) project.AutomationSettings {
	switch event {
	case scm.EventPush:
		settings.TriggerPush = false
	case scm.EventTag:
		settings.TriggerTag = false
	case scm.EventPullRequest:
		settings.TriggerPullRequest = false
	}
	return settings
}

func webhookEvent(eventType scm.EventType) scm.Event {
	return scm.Event{Provider: scm.GitHub, DeliveryID: "delivery-1", Type: eventType,
		Repository: scm.Repository{ExternalID: "70"}, Ref: "refs/heads/main",
		AfterSHA: "0123456789abcdef0123456789abcdef01234567", ReceivedAt: time.Now().UTC(),
		Metadata: map[string]string{"fork": "false"}}
}

func forkEvent() scm.Event {
	event := webhookEvent(scm.EventPullRequest)
	event.Metadata["fork"] = "true"
	return event
}

func workItem(event scm.Event) githubhook.WorkItem {
	return githubhook.WorkItem{ID: uuid.New(), LeaseID: uuid.New(), Event: event, Attempt: 1, LeaseExpires: time.Now().Add(time.Minute)}
}
