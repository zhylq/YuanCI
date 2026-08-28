package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/integration"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/secrets"
)

type importProvider struct {
	subject   string
	exchanges atomic.Int32
	repos     []integration.Repository
	denied    bool
	afterRead func()
}

func (p *importProvider) VerifyApp(_ context.Context, client string, _ []byte) (integration.App, error) {
	return integration.App{AppID: "12", ClientID: client, Slug: "test-app"}, nil
}
func (p *importProvider) AuthorizationURL(_, _, _, _ string) string {
	return "https://github.com/login/oauth/authorize"
}
func (p *importProvider) Exchange(context.Context, string, string, string, string, string) (string, string, time.Time, error) {
	p.exchanges.Add(1)
	return p.subject, "fixture-user-token", time.Now().Add(10 * time.Minute), nil
}
func (p *importProvider) Installations(context.Context, string) ([]integration.Installation, error) {
	return []integration.Installation{{ID: "34", AccountID: "56", Account: "team"}}, nil
}
func (p *importProvider) VerifyInstallation(context.Context, integration.App, []byte, integration.Installation) error {
	if p.denied {
		return integration.ErrAccess
	}
	return nil
}
func (p *importProvider) Repositories(context.Context, string, string, int) (integration.RepoPage, error) {
	if p.afterRead != nil {
		p.afterRead()
	}
	return integration.RepoPage{Items: p.repos}, nil
}
func importFixture(t *testing.T) (*Store, *integration.Service, identity.Credentials, *importProvider) {
	t.Helper()
	s, managed, setup := managedFixture(t)
	session, _ := initializeManaged(t, s, managed, setup)
	cipher, _ := secrets.NewCipher([]byte(strings.Repeat("k", 32)))
	service := integration.New(s, cipher, "https://ci.example.test")
	provider := &importProvider{subject: "100", repos: []integration.Repository{{ID: "70", Owner: "team", Name: "safe", DefaultBranch: "main"}}}
	service.Provider = provider
	if err := service.Save(t.Context(), session.Token, integration.AppInput{AppID: "12", PrivateKey: "fixture-private-key"}); err != nil {
		t.Fatal(err)
	}
	return s, service, session, provider
}
func authorizeImport(t *testing.T, service *integration.Service, token string) (string, string) {
	t.Helper()
	state, nonce := identity.NewToken(), identity.NewToken()
	if _, err := service.Start(t.Context(), token, state, nonce); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(t.Context(), token, state, nonce, "fixture-code"); err != nil {
		t.Fatal(err)
	}
	return state, nonce
}
func TestGitHubImportEncryptionIdempotencyAndNoMembershipGrants(t *testing.T) {
	s, service, session, _ := importFixture(t)
	authorizeImport(t, service, session.Token)
	settings, err := service.Settings(t.Context(), session.Token)
	if err != nil || settings.AuthorizedUntil == nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(settings)
	var stored string
	if err := s.pool.QueryRow(t.Context(), `SELECT encrypted_key::text || (SELECT encrypted_token::text FROM github_import_proofs LIMIT 1) FROM github_app_configs`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(encoded), stored} {
		for _, secret := range []string{"fixture-private-key", "fixture-user-token"} {
			if strings.Contains(text, secret) {
				t.Fatal("plaintext leaked")
			}
		}
	}
	if strings.Contains(string(encoded), "encrypted") || strings.Contains(string(encoded), "ciphertext") {
		t.Fatal("encrypted data exposed")
	}
	var before int
	_ = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM memberships`).Scan(&before)
	var wg sync.WaitGroup
	results := make(chan []integration.Imported, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			items, err := service.Import(t.Context(), session.Token, "34", []string{"70"})
			results <- items
			failures <- err
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	created := 0
	var id uuid.UUID
	for items := range results {
		if len(items) != 1 {
			t.Fatal("missing result")
		}
		id = items[0].ID
		if items[0].Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d duplicate records", created)
	}
	var audit, after int
	_ = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='repository.imported'`).Scan(&audit)
	_ = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM memberships`).Scan(&after)
	if audit != 1 || before != after {
		t.Fatal("duplicate audit or implicit permission grant")
	}
	if _, err := s.GetProject(t.Context(), session.Token, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE repositories SET active=false WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Import(t.Context(), session.Token, "34", []string{"70"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject(t.Context(), session.Token, id); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("disabled repository reactivated")
	}
}
func TestGitHubImportFlowReplayWrongAccountAndSession(t *testing.T) {
	s, service, session, provider := importFixture(t)
	state, nonce := identity.NewToken(), identity.NewToken()
	if _, err := service.Start(t.Context(), session.Token, state, nonce); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(t.Context(), identity.NewToken(), state, nonce, "code"); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("wrong session accepted")
	}
	if err := service.Finish(t.Context(), session.Token, state, identity.NewToken(), "code"); !errors.Is(err, identity.ErrOAuthFlow) {
		t.Fatal("wrong nonce accepted")
	}
	provider.subject = "101"
	if err := service.Finish(t.Context(), session.Token, state, nonce, "code"); !errors.Is(err, integration.ErrAccess) {
		t.Fatal("wrong account accepted")
	}
	provider.subject = "100"
	if err := service.Finish(t.Context(), session.Token, state, nonce, "code"); !errors.Is(err, identity.ErrOAuthFlow) {
		t.Fatal("flow replay accepted")
	}
	if provider.exchanges.Load() != 1 {
		t.Fatal("replayed token exchange")
	}
	if _, err := service.Installations(t.Context(), session.Token); !errors.Is(err, integration.ErrStale) {
		t.Fatal("wrong account proof saved")
	}
	state, nonce = identity.NewToken(), identity.NewToken()
	_, _ = service.Start(t.Context(), session.Token, state, nonce)
	_, _ = s.pool.Exec(t.Context(), `UPDATE github_import_flows SET expires_at=clock_timestamp()-interval '1 second'`)
	if err := service.Finish(t.Context(), session.Token, state, nonce, "code"); !errors.Is(err, identity.ErrOAuthFlow) {
		t.Fatal("expired flow accepted")
	}
}
func TestGitHubImportSupersededFlowAndRevision(t *testing.T) {
	s, service, session, _ := importFixture(t)
	state, nonce := identity.NewToken(), identity.NewToken()
	_, _ = service.Start(t.Context(), session.Token, state, nonce)
	snap, err := s.ConsumeIntegrationFlow(t.Context(), session.Token, state, nonce)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = service.Start(t.Context(), session.Token, identity.NewToken(), identity.NewToken())
	if err := s.SaveIntegrationProof(t.Context(), session.Token, snap, integration.Proof{ID: uuid.New(), ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, integration.ErrStale) {
		t.Fatal("superseded authorization accepted")
	}
	authorizeImport(t, service, session.Token)
	old, _ := s.IntegrationContext(t.Context(), session.Token, false)
	if err := service.Save(t.Context(), session.Token, integration.AppInput{AppID: "12", PrivateKey: "replacement", ExpectedRevision: &old.App.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckIntegration(t.Context(), session.Token, old, true); !errors.Is(err, integration.ErrStale) {
		t.Fatal("stale App accepted")
	}
	if err := service.Save(t.Context(), session.Token, integration.AppInput{AppID: "12", PrivateKey: "replacement", ExpectedRevision: &old.App.ID}); !errors.Is(err, integration.ErrStale) {
		t.Fatal("lost update")
	}
	if _, err := service.Import(t.Context(), session.Token, "34", []string{"70"}); !errors.Is(err, integration.ErrStale) {
		t.Fatal("old proof used")
	}
}
func TestGitHubImportRemoteAndLocalRevocation(t *testing.T) {
	for _, which := range []string{"installation", "repository", "proof", "admin", "session"} {
		t.Run(which, func(t *testing.T) {
			s, service, session, p := importFixture(t)
			authorizeImport(t, service, session.Token)
			switch which {
			case "installation":
				p.denied = true
			case "repository":
				p.repos = nil
			case "proof":
				p.afterRead = func() {
					_, err := s.pool.Exec(t.Context(), `UPDATE github_import_proofs SET expires_at=clock_timestamp()-interval '1 second'`)
					if err != nil {
						t.Fatal(err)
					}
				}
			case "admin":
				p.afterRead = func() {
					_, err := s.pool.Exec(t.Context(), `DELETE FROM memberships WHERE user_id=$1`, session.Session.UserID)
					if err != nil {
						t.Fatal(err)
					}
				}
			case "session":
				p.afterRead = func() {
					if err := s.RevokeSession(t.Context(), session.Token); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := service.Import(t.Context(), session.Token, "34", []string{"70"}); err == nil {
				t.Fatal("revocation ignored")
			}
			var count int
			_ = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM repositories`).Scan(&count)
			if count != 0 {
				t.Fatal("partial import")
			}
		})
	}
}
func TestGitHubImportAuditRollbackAndConflicts(t *testing.T) {
	s, service, session, p := importFixture(t)
	authorizeImport(t, service, session.Token)
	if _, err := service.Import(t.Context(), session.Token, "999", []string{"70"}); !errors.Is(err, integration.ErrAccess) {
		t.Fatal("forged installation accepted")
	}
	if _, err := service.Import(t.Context(), session.Token, "34", []string{"70", "70"}); !errors.Is(err, integration.ErrConfig) {
		t.Fatal("duplicate selection accepted")
	}
	if _, err := service.Import(t.Context(), session.Token, "34", []string{"71"}); !errors.Is(err, integration.ErrAccess) {
		t.Fatal("unlisted repository accepted")
	}
	p.repos[0].Owner = "other"
	if _, err := service.Import(t.Context(), session.Token, "34", []string{"70"}); !errors.Is(err, integration.ErrAccess) {
		t.Fatal("wrong owner accepted")
	}
	p.repos[0].Owner = "team"
	rejectAudit(t, s)
	if _, err := service.Import(t.Context(), session.Token, "34", []string{"70"}); err == nil {
		t.Fatal("audit failure ignored")
	}
	for _, table := range []string{"repositories", "github_accounts", "github_installations", "organizations"} {
		var n int
		_ = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&n)
		if n != 0 {
			t.Fatal("partial write", table)
		}
	}
}
func TestGitHubImportDoesNotAdoptExistingOrganization(t *testing.T) {
	s, service, session, _ := importFixture(t)
	authorizeImport(t, service, session.Token)
	_, err := s.pool.Exec(t.Context(), `INSERT INTO organizations(slug,display_name) VALUES('github-56','Existing unrelated org')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Import(t.Context(), session.Token, "34", []string{"70"}); err == nil {
		t.Fatal("adopted organization by name")
	}
	page, err := s.ListProjects(t.Context(), session.Token, project.Query{Limit: 20})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("partial project import")
	}
}
