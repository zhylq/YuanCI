package githubci

import (
	"context"
	"errors"
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
	repositoryID uuid.UUID
	settings     project.AutomationSettings
	policyCalls  int
	finalized    []githubhook.Finalize
	commit       RunCommit
	commitResult RunResult
}

func (s *orchestratorStore) RuntimeAutomationForGitHub(_ context.Context, externalID string) (uuid.UUID, project.AutomationSettings, error) {
	s.policyCalls++
	if externalID != "70" {
		return uuid.Nil, project.AutomationSettings{}, errors.New("unexpected repository")
	}
	return s.repositoryID, s.settings, nil
}

func (s *orchestratorStore) CommitWebhookRun(_ context.Context, request RunCommit) (RunResult, error) {
	s.commit = request
	return s.commitResult, nil
}

func (s *orchestratorStore) FinalizeWebhook(_ context.Context, request githubhook.Finalize) error {
	s.finalized = append(s.finalized, request)
	return nil
}

type pipelineFetcher struct {
	repository githubapp.Repository
	source     []byte
	calls      int
	event      scm.Event
	path       string
}

func (f *pipelineFetcher) FetchPipeline(_ context.Context, event scm.Event, path string) (githubapp.Repository, []byte, error) {
	f.calls++
	f.event = event
	f.path = path
	return f.repository, append([]byte(nil), f.source...), nil
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
	if _, err := orchestrator.Process(t.Context(), workItem(webhookEvent(scm.EventPush))); !errors.Is(err, ErrRepositoryMismatch) {
		t.Fatalf("expected repository mismatch, got %v", err)
	}
	if len(store.finalized) != 0 || store.commit.RepositoryID != uuid.Nil {
		t.Fatal("mismatched repository changed delivery or committed a run")
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
	return githubhook.WorkItem{ID: uuid.New(), LeaseID: uuid.New(), Event: event, LeaseExpires: time.Now().Add(time.Minute)}
}
