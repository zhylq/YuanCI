package postgres

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/githubci"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/scm"
)

func (p *giteeProviderFixture) Commit(context.Context, string, gitee.Repository, string) (string, error) {
	return strings.Repeat("a", 40), nil
}
func (p *giteeProviderFixture) File(_ context.Context, _ string, _ gitee.Repository, _ string, sha string) ([]byte, error) {
	if sha != strings.Repeat("a", 40) {
		return nil, scm.ErrNotFound
	}
	return []byte(githubCIPipeline), nil
}
func (p *giteeProviderFixture) VerifyEvent(_ context.Context, _ string, repo gitee.Repository, event scm.Event) error {
	if repo.ID != event.Repository.ExternalID || event.AfterSHA != strings.Repeat("a", 40) {
		return scm.ErrInvalidHook
	}
	return nil
}
func giteeProjectFixture(t *testing.T) (*Store, *gitee.Service, identity.Credentials, uuid.UUID) {
	t.Helper()
	s, service, session, _ := giteeFixture(t)
	authorizeGitee(t, s, service, session.Token)
	imported, err := service.Import(t.Context(), session.Token, []gitee.Selection{{ID: "42", Owner: "fixture", Name: "repo"}})
	if err != nil {
		t.Fatal(err)
	}
	return s, service, session, imported[0].ID
}
func TestGiteeWebhookDurabilityValidationAndSecretReplacement(t *testing.T) {
	s, service, session, id := giteeProjectFixture(t)
	secret := []byte(strings.Repeat("s", 32))
	if err := service.SaveWebhook(t.Context(), session.Token, id, 0, secret); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveWebhook(t.Context(), session.Token, id, 0, secret); err == nil {
		t.Fatal("stale webhook revision accepted")
	}
	proof, err := service.ValidateProject(t.Context(), session.Token, id, 0)
	if err != nil || proof.CommitSHA != strings.Repeat("a", 40) {
		t.Fatalf("validation: %v", err)
	}
	headers := http.Header{"X-Gitee-Token": {string(secret)}, "X-Gitee-Timestamp": {fmt.Sprint(time.Now().UnixMilli())}, "X-Gitee-Event": {"Push Hook"}}
	body := []byte(`{"ref":"refs/heads/main","after":"` + strings.Repeat("a", 40) + `","repository":{"id":42},"password":"must-not-persist"}`)
	receipt, err := service.ReceiveWebhook(t.Context(), "42", headers, body)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.ReceiveWebhook(t.Context(), "42", headers, body)
	if err != nil || !duplicate.Duplicate || duplicate.ID != receipt.ID {
		t.Fatalf("duplicate: %v", err)
	}
	var normalized string
	if err := s.pool.QueryRow(t.Context(), `SELECT normalized_event::text FROM webhook_deliveries WHERE id=$1`, receipt.ID).Scan(&normalized); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(normalized, "must-not-persist") || strings.Contains(normalized, string(secret)) {
		t.Fatal("webhook secret persisted in event")
	}
	item, err := s.ClaimWebhook(t.Context(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	repository, source, err := service.FetchPipeline(t.Context(), item.Event, ".yuanci.yml")
	if err != nil || repository.ID != id || len(source) == 0 {
		t.Fatalf("fetch: %v", err)
	}
	if err := service.SaveWebhook(t.Context(), session.Token, id, 1, []byte(strings.Repeat("n", 32))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.FetchPipeline(t.Context(), item.Event, ".yuanci.yml"); err == nil {
		t.Fatal("old webhook config event trusted")
	}
	if _, err := service.ReceiveWebhook(t.Context(), "42", headers, body); err == nil {
		t.Fatal("old webhook secret accepted")
	}
}

func TestGiteeWebhookCreatesSharedRun(t *testing.T) {
	s, service, session, id := giteeProjectFixture(t)
	secret := []byte(strings.Repeat("s", 32))
	if err := service.SaveWebhook(t.Context(), session.Token, id, 0, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateProject(t.Context(), session.Token, id, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProjectAutomation(t.Context(), session.Token, id, project.AutomationUpdate{Enabled: true, PipelinePath: ".yuanci.yml", TriggerPush: true, TriggerPullRequest: true, ExpectedRevision: 0}); err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"X-Gitee-Token": {string(secret)}, "X-Gitee-Timestamp": {fmt.Sprint(time.Now().UnixMilli())}, "X-Gitee-Event": {"Push Hook"}}
	body := []byte(`{"ref":"refs/heads/main","after":"` + strings.Repeat("a", 40) + `","repository":{"id":42}}`)
	if _, err := service.ReceiveWebhook(t.Context(), "42", headers, body); err != nil {
		t.Fatal(err)
	}
	delivery, err := s.ClaimWebhook(t.Context(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator, err := githubci.NewOrchestrator(s, service)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := orchestrator.Process(t.Context(), *delivery)
	if err != nil || outcome != githubci.OutcomeRunCreated {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	var count int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE repository_id=$1 AND commit_sha=$2`, id, strings.Repeat("a", 40)).Scan(&count); err != nil || count != 1 {
		t.Fatal("run not created")
	}
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM commit_status_outbox WHERE repository_id=$1 AND provider='gitee' AND commit_state='pending'`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal("pending outbox missing")
	}
}

type brokenGiteePipeline struct {
	*giteeProviderFixture
	missing bool
}

func (p brokenGiteePipeline) File(context.Context, string, gitee.Repository, string, string) ([]byte, error) {
	if p.missing {
		return nil, scm.ErrNotFound
	}
	return []byte("invalid pipeline"), nil
}
func TestGiteeSharedRunPolicyAndConfigurationFailure(t *testing.T) {
	for _, mode := range []string{"fork", "disabled", "missing", "invalid", "substitution"} {
		t.Run(mode, func(t *testing.T) {
			s, service, session, id := giteeProjectFixture(t)
			secret := []byte(strings.Repeat("s", 32))
			if err := service.SaveWebhook(t.Context(), session.Token, id, 0, secret); err != nil {
				t.Fatal(err)
			}
			if _, err := service.ValidateProject(t.Context(), session.Token, id, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateProjectAutomation(t.Context(), session.Token, id, project.AutomationUpdate{Enabled: mode != "disabled", PipelinePath: ".yuanci.yml", TriggerPush: true, TriggerPullRequest: true}); err != nil {
				t.Fatal(err)
			}
			if mode == "missing" || mode == "invalid" {
				service.Provider = brokenGiteePipeline{giteeProviderFixture: service.Provider.(*giteeProviderFixture), missing: mode == "missing"}
			}
			headers := http.Header{"X-Gitee-Token": {string(secret)}, "X-Gitee-Timestamp": {fmt.Sprint(time.Now().UnixMilli())}, "X-Gitee-Event": {"Push Hook"}}
			body := []byte(`{"ref":"refs/heads/main","after":"` + strings.Repeat("a", 40) + `","repository":{"id":42}}`)
			if mode == "fork" {
				headers.Set("X-Gitee-Event", "Merge Request Hook")
				body = []byte(`{"pull_request":{"number":1,"state":"open","head":{"sha":"` + strings.Repeat("a", 40) + `","ref":"feature","repo":{"id":99}},"base":{"ref":"main","repo":{"id":42}}}}`)
			}
			if _, err := service.ReceiveWebhook(t.Context(), "42", headers, body); err != nil {
				t.Fatal(err)
			}
			delivery, err := s.ClaimWebhook(t.Context(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "substitution" {
				delivery.Event.Ref = "refs/heads/other"
			}
			orchestrator, _ := githubci.NewOrchestrator(s, service)
			outcome, err := orchestrator.Process(t.Context(), *delivery)
			want := githubci.OutcomeFailedRunCreated
			switch mode {
			case "fork":
				want = githubci.OutcomeIgnoredFork
			case "disabled":
				want = githubci.OutcomeIgnoredDisabled
			case "substitution":
				want = githubci.OutcomeDeadLettered
			}
			if err != nil || outcome != want {
				t.Fatalf("outcome=%s want=%s err=%v", outcome, want, err)
			}
			var jobs int
			if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs`).Scan(&jobs); err != nil || jobs != 0 {
				t.Fatal("unsafe executable jobs created")
			}
		})
	}
}
