package postgres

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/commitstatus"
	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/githubci"
	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/provisioning"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/scm"
	"github.com/yuanci/yuanci/internal/secrets"
)

func (p *giteeProviderFixture) Commit(context.Context, string, gitee.Repository, string) (string, error) {
	return strings.Repeat("a", 40), nil
}
func (p *giteeProviderFixture) DeliverCheck(_ context.Context, token string, repo gitee.Repository, item commitstatus.Item, target string) error {
	if token == "" || repo.ID != "42" || item.RunID == uuid.Nil || item.CommitSHA != strings.Repeat("a", 40) || !strings.HasSuffix(target, "/runs/"+item.RunID.String()) {
		return commitstatus.ErrInvalid
	}
	return nil
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
	status, err := s.ClaimCommitStatus(t.Context(), time.Minute)
	if err != nil || status == nil {
		t.Fatal("claim status", err)
	}
	if err := service.Deliver(t.Context(), *status); err != nil {
		t.Fatal("pending delivery", err)
	}
	if err := s.FinishCommitStatus(t.Context(), status.ID, status.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	runnerID := uuid.New()
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO runners(id,pool_id,name,status,capacity,labels,certificate_serial,os,architecture,executor,isolation_level,available_disk_bytes,protocol_version,runner_version)
	SELECT $1,id,'gitee-runner','online',1,'{}','gitee-serial','linux','amd64','docker','standard',4294967296,2,'v2' FROM runner_pools WHERE name='standard'`, runnerID); err != nil {
		t.Fatal(err)
	}
	assignment, err := s.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runnerID})
	if err != nil || assignment == nil || assignment.Source == nil {
		t.Fatal("Gitee source claim", err)
	}
	lease := runmodel.LeaseRequest{RunnerID: runnerID, JobID: assignment.JobID, LeaseToken: assignment.LeaseToken}
	sha := assignment.Source.CommitSHA
	if err := s.CheckGiteeCheckoutLease(t.Context(), lease, id, sha); err != nil {
		t.Fatal("valid checkout lease", err)
	}
	wrong := lease
	wrong.LeaseToken = "other"
	if s.CheckGiteeCheckoutLease(t.Context(), wrong, id, sha) == nil || s.CheckGiteeCheckoutLease(t.Context(), lease, uuid.New(), sha) == nil || s.CheckGiteeCheckoutLease(t.Context(), lease, id, strings.Repeat("b", 40)) == nil {
		t.Fatal("checkout crossed lease/source scope")
	}
	for _, boundary := range []struct {
		deny, restore string
		target        uuid.UUID
	}{
		{`UPDATE runners SET status='disabled' WHERE id=$1`, `UPDATE runners SET status='online' WHERE id=$1`, runnerID},
		{`UPDATE repositories SET active=false WHERE id=$1`, `UPDATE repositories SET active=true WHERE id=$1`, id},
		{`UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, `UPDATE jobs SET lease_expires_at=clock_timestamp()+interval '30 seconds' WHERE id=$1`, assignment.JobID},
	} {
		if _, err := s.pool.Exec(t.Context(), boundary.deny, boundary.target); err != nil {
			t.Fatal(err)
		}
		if s.CheckGiteeCheckoutLease(t.Context(), lease, id, sha) == nil {
			t.Fatal("revoked checkout boundary accepted")
		}
		if _, err := s.pool.Exec(t.Context(), boundary.restore, boundary.target); err != nil {
			t.Fatal(err)
		}
	}
	broker := gitee.NewCheckoutBroker(service, s)
	credential, err := broker.IssueAssignmentCredential(t.Context(), runnerID, assignment)
	if err != nil || len(credential.Token) != 43 || credential.CloneURL == assignment.Source.CloneURL {
		t.Fatal("scoped issuance", err)
	}
	clear(credential.Token)
	if _, err := s.AcknowledgeRunnerJob(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRunnerJob(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRunnerJob(t.Context(), runmodel.RunnerCompletion{RunnerID: runnerID, JobID: assignment.JobID, LeaseToken: assignment.LeaseToken, Status: runmodel.JobSucceeded}); err != nil {
		t.Fatal(err)
	}
	if s.CheckGiteeCheckoutLease(t.Context(), lease, id, sha) == nil {
		t.Fatal("completed lease still authorizes checkout")
	}
	final, err := s.ClaimCommitStatus(t.Context(), time.Minute)
	if err != nil || final == nil || final.State != commitstatus.StateSuccess {
		t.Fatal("final outbox", err)
	}
	if err := service.Deliver(t.Context(), *final); err != nil {
		t.Fatal("final delivery", err)
	}
	if err := service.Store.RevokeGiteeGrant(t.Context(), session.Token); err != nil {
		t.Fatal(err)
	}
	if service.Deliver(t.Context(), *final) == nil {
		t.Fatal("revoked grant delivered status")
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

func TestGiteeAutomationHTTPWithoutGitHubService(t *testing.T) {
	s, service, session, id := giteeProjectFixture(t)
	secret := []byte(strings.Repeat("s", 32))
	if err := service.SaveWebhook(t.Context(), session.Token, id, 0, secret); err != nil {
		t.Fatal(err)
	}
	cipher, _ := secrets.NewCipher([]byte(strings.Repeat("k", 32)))
	handler, err := httpapi.NewAuthenticated(slog.New(slog.NewTextHandler(io.Discard, nil)), s, s, 1<<20, "https://ci.example.test", httpapi.GitHubLogin{Store: s, Managed: provisioning.New(s, cipher, "https://ci.example.test"), Gitee: service})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://ci.example.test/api/v1/projects/"+id.String()+"/pipeline/validate", strings.NewReader(`{"expected_revision":0}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://ci.example.test")
	r.Header.Set("X-CSRF-Token", identity.CSRFToken(session.Token))
	r.AddCookie(identity.SessionCookie(session))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("validate: %d %s", w.Code, w.Body.String())
	}
	hook := httptest.NewRequest("POST", "https://ci.example.test/api/v1/webhooks/gitee/42", strings.NewReader(`{"ref":"refs/heads/main","after":"`+strings.Repeat("a", 40)+`","repository":{"id":42}}`))
	hook.Header.Set("Content-Type", "application/json")
	hook.Header.Set("X-Gitee-Token", string(secret))
	hook.Header.Set("X-Gitee-Timestamp", fmt.Sprint(time.Now().UnixMilli()))
	hook.Header.Set("X-Gitee-Event", "Push Hook")
	accepted := httptest.NewRecorder()
	handler.ServeHTTP(accepted, hook)
	if accepted.Code != 202 {
		t.Fatalf("hook: %d %s", accepted.Code, accepted.Body.String())
	}
}
