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
	"github.com/yuanci/yuanci/internal/identity"
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
