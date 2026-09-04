package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
	"github.com/yuanci/yuanci/internal/scm"
	"github.com/yuanci/yuanci/internal/secrets"
)

type giteeProviderFixture struct {
	refreshes    atomic.Int32
	entered      chan struct{}
	release      chan struct{}
	refreshError error
}

func (p *giteeProviderFixture) Exchange(context.Context, gitee.OAuthConfig, string) (gitee.Token, error) {
	return gitee.Token{Access: "fixture-access-secret", Refresh: "fixture-refresh-secret", Scope: "user_info projects", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (p *giteeProviderFixture) User(context.Context, string) (identity.ExternalUser, error) {
	return identity.ExternalUser{Provider: "gitee", Instance: identity.GiteeInstance, Subject: "100", Login: "fixture"}, nil
}
func (p *giteeProviderFixture) Refresh(ctx context.Context, _ gitee.OAuthConfig, _ string) (gitee.Token, error) {
	p.refreshes.Add(1)
	if p.entered != nil {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return gitee.Token{}, ctx.Err()
		}
	}
	if p.refreshError != nil {
		return gitee.Token{}, p.refreshError
	}
	return gitee.Token{Access: "new-access-secret", Refresh: "new-refresh-secret", Scope: "user_info projects", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func giteeFixture(t *testing.T) (*Store, *gitee.Service, identity.Credentials, *giteeProviderFixture) {
	t.Helper()
	s, login, setup := managedFixture(t)
	input := candidateInput(nil)
	input.Provider = "gitee"
	access := provisioning.Access{SetupToken: setup}
	id, err := login.Save(t.Context(), access, input)
	if err != nil {
		t.Fatal(err)
	}
	user := identity.ExternalUser{Provider: "gitee", Instance: identity.GiteeInstance, Subject: "100", Login: "fixture"}
	session, err := s.FinishManagedOAuth(t.Context(), candidateTicket(t, s, login, id, access), user, "", setup)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	service := gitee.New(s, cipher, "https://ci.example.test")
	provider := &giteeProviderFixture{}
	service.Provider = provider
	return s, service, session, provider
}
func authorizeGitee(t *testing.T, s *Store, service *gitee.Service, session string) gitee.Grant {
	t.Helper()
	state, nonce := identity.NewToken(), identity.NewToken()
	if _, err := service.Start(t.Context(), session, state, nonce); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(t.Context(), session, state, nonce, "code"); err != nil {
		t.Fatal(err)
	}
	snap, err := s.GiteeContext(t.Context(), session, false)
	if err != nil || snap.Grant == nil {
		t.Fatalf("grant missing: %v", err)
	}
	return *snap.Grant
}
func TestGiteeAuthorizationEncryptionReplayAndRevocation(t *testing.T) {
	s, service, session, _ := giteeFixture(t)
	state, nonce := identity.NewToken(), identity.NewToken()
	if _, err := service.Start(t.Context(), session.Token, state, nonce); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(t.Context(), session.Token, state, identity.NewToken(), "code"); !errors.Is(err, identity.ErrOAuthFlow) {
		t.Fatal("wrong browser accepted")
	}
	if err := service.Finish(t.Context(), session.Token, state, nonce, "code"); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(t.Context(), session.Token, state, nonce, "code"); !errors.Is(err, identity.ErrOAuthFlow) {
		t.Fatal("flow replay accepted")
	}
	snap, err := s.GiteeContext(t.Context(), session.Token, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snap.Grant)
	var stored string
	if err := s.pool.QueryRow(t.Context(), `SELECT encrypted_token::text FROM gitee_authorizations WHERE id=$1`, snap.Grant.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored+string(encoded), "access-secret") || strings.Contains(string(encoded), "encrypted") {
		t.Fatal("token exposed")
	}
	cipher, _ := secrets.NewCipher([]byte(strings.Repeat("k", 32)))
	other := *snap.Grant
	other.UserID = uuid.New()
	if _, err := cipher.Open(snap.Grant.Encrypted, gitee.GrantAAD(other)); err == nil {
		t.Fatal("ciphertext moved across users")
	}
	token, err := service.Access(t.Context(), snap.Grant.ID)
	if err != nil || string(token) != "fixture-access-secret" {
		t.Fatalf("access: %v", err)
	}
	clear(token)
	if err := s.RevokeGiteeGrant(t.Context(), session.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Access(t.Context(), snap.Grant.ID); err == nil {
		t.Fatal("revoked token usable")
	}
	if err := s.pool.QueryRow(t.Context(), `SELECT COALESCE(encrypted_token::text,'') FROM gitee_authorizations WHERE id=$1`, snap.Grant.ID).Scan(&stored); err != nil || stored != "" {
		t.Fatal("revoked material retained")
	}
}
func TestGiteeRefreshClaimAndReplacementRace(t *testing.T) {
	s, service, session, provider := giteeFixture(t)
	grant := authorizeGitee(t, s, service, session.Token)
	if _, err := s.pool.Exec(t.Context(), `UPDATE gitee_authorizations SET expires_at=clock_timestamp() WHERE id=$1`, grant.ID); err != nil {
		t.Fatal(err)
	}
	provider.entered = make(chan struct{})
	provider.release = make(chan struct{})
	result := make(chan error, 1)
	go func() { token, err := service.Access(t.Context(), grant.ID); clear(token); result <- err }()
	select {
	case <-provider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("refresh not started")
	}
	if _, err := service.Access(t.Context(), grant.ID); !errors.Is(err, gitee.ErrBusy) {
		t.Fatalf("duplicate refresh: %v", err)
	}
	replacement := authorizeGitee(t, s, service, session.Token)
	close(provider.release)
	if err := <-result; !errors.Is(err, gitee.ErrStale) {
		t.Fatalf("old refresh overwrote replacement: %v", err)
	}
	current, _, err := s.GiteeGrant(t.Context(), grant.ID)
	if err != nil || current.Revision != replacement.Revision || provider.refreshes.Load() != 1 {
		t.Fatal("replacement lost")
	}
}
func TestGiteeRefreshRateAndCrashRecovery(t *testing.T) {
	for _, mode := range []string{"rate", "ambiguous", "crash", "success"} {
		t.Run(mode, func(t *testing.T) {
			s, service, session, provider := giteeFixture(t)
			grant := authorizeGitee(t, s, service, session.Token)
			if _, err := s.pool.Exec(t.Context(), `UPDATE gitee_authorizations SET expires_at=clock_timestamp() WHERE id=$1`, grant.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "crash" {
				claim, err := s.ClaimGiteeRefresh(t.Context(), grant)
				if err != nil || claim == uuid.Nil {
					t.Fatal(err)
				}
				if _, err := s.pool.Exec(t.Context(), `UPDATE gitee_authorizations SET refresh_until=clock_timestamp()-interval '1 second' WHERE id=$1`, grant.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "rate" {
				provider.refreshError = gitee.RateError{After: time.Minute}
			}
			if mode == "ambiguous" {
				provider.refreshError = gitee.ErrRemote
			}
			token, err := service.Access(t.Context(), grant.ID)
			defer clear(token)
			if mode == "success" {
				if err != nil || string(token) != "new-access-secret" {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("failure accepted")
			}
			if mode == "rate" {
				if !errors.Is(err, scm.ErrRateLimited) {
					t.Fatal(err)
				}
				if _, err := service.Access(t.Context(), grant.ID); !errors.Is(err, gitee.ErrBusy) {
					t.Fatal("rate delay ignored")
				}
			} else {
				if _, _, err := s.GiteeGrant(t.Context(), grant.ID); err == nil {
					t.Fatal("ambiguous token retained")
				}
			}
		})
	}
}
