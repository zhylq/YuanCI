package postgres

import (
	"errors"
	"sync"
	"testing"

	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/identity"
)

func oauthStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.ConfigureGitHubBootstrap(t.Context(), "100"); err != nil {
		t.Fatal(err)
	}
	return s
}

func oauthTicket(t *testing.T, s *Store, linkToken string) string {
	t.Helper()
	state, nonce := identity.NewToken(), identity.NewToken()
	if err := s.BeginOAuth(t.Context(), state, nonce, linkToken); err != nil {
		t.Fatal(err)
	}
	ticket, err := s.ConsumeOAuth(t.Context(), state, nonce)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func githubUser(subject string) identity.ExternalUser {
	return identity.ExternalUser{Provider: "github", Instance: identity.GitHubInstance, Subject: subject, Login: "fixture", Name: "Fixture User"}
}

func oauthLogin(t *testing.T, s *Store, subject string) identity.Credentials {
	t.Helper()
	credentials, err := s.FinishOAuth(t.Context(), oauthTicket(t, s, ""), githubUser(subject), "")
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func TestOAuthStateBindingExpiryAndReplay(t *testing.T) {
	s := oauthStore(t)
	state, nonce := identity.NewToken(), identity.NewToken()
	if err := s.BeginOAuth(t.Context(), state, nonce, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOAuth(t.Context(), state, identity.NewToken()); !errors.Is(err, identity.ErrOAuthFlow) {
		t.Fatal("wrong browser consumed flow")
	}
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for range 10 {
		wg.Go(func() { _, err := s.ConsumeOAuth(t.Context(), state, nonce); results <- err })
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, identity.ErrOAuthFlow) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("flow consumed %d times", success)
	}
	state, nonce = identity.NewToken(), identity.NewToken()
	if err := s.BeginOAuth(t.Context(), state, nonce, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE oauth_flows SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOAuth(t.Context(), state, nonce); !errors.Is(err, identity.ErrOAuthFlow) {
		t.Fatal("expired flow accepted")
	}
}

func TestOAuthExplicitBootstrapAndNoPrivilegeRestoration(t *testing.T) {
	s := oauthStore(t)
	if _, err := s.FinishOAuth(t.Context(), oauthTicket(t, s, ""), githubUser("999"), ""); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("first visitor stole administrator")
	}
	admin := oauthLogin(t, s, "100")
	if err := s.ConfigureGitHubBootstrap(t.Context(), "100"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureGitHubBootstrap(t.Context(), "200"); !errors.Is(err, identity.ErrBootstrap) {
		t.Fatal("bootstrap subject changed")
	}
	ticket := oauthTicket(t, s, "")
	renamed := githubUser("100")
	renamed.Login = "renamed"
	rotated, err := s.FinishOAuth(t.Context(), ticket, renamed, admin.Token)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Session.UserID != admin.Session.UserID || rotated.Token == admin.Token {
		t.Fatal("identity changed with login name or session not rotated")
	}
	if _, err := s.AuthenticateSession(t.Context(), admin.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("old browser session survived rotation")
	}
	if _, err := s.FinishOAuth(t.Context(), ticket, renamed, ""); !errors.Is(err, identity.ErrOAuthFlow) {
		t.Fatal("completion ticket replayed")
	}
	member := oauthLogin(t, s, "200")
	var count int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM memberships WHERE user_id=$1`, member.Session.UserID).Scan(&count); err != nil || count != 0 {
		t.Fatal("new user received automatic roles")
	}
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM memberships WHERE user_id=$1`, admin.Session.UserID); err != nil {
		t.Fatal(err)
	}
	oauthLogin(t, s, "100")
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM memberships WHERE user_id=$1`, admin.Session.UserID).Scan(&count); err != nil || count != 0 {
		t.Fatal("bootstrap restored revoked privileges")
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE users SET status='suspended' WHERE id=$1`, admin.Session.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishOAuth(t.Context(), oauthTicket(t, s, ""), githubUser("100"), ""); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("suspended account logged in")
	}
}

func TestOAuthLinkRequiresSameRecentSessionAndCannotStealIdentity(t *testing.T) {
	s := oauthStore(t)
	admin := oauthLogin(t, s, "100")
	member := oauthLogin(t, s, "200")
	if _, err := s.FinishOAuth(t.Context(), oauthTicket(t, s, member.Token), githubUser("100"), member.Token); !errors.Is(err, identity.ErrIdentityConflict) {
		t.Fatal("identity stolen from another user")
	}
	if _, err := s.FinishOAuth(t.Context(), oauthTicket(t, s, member.Token), githubUser("300"), admin.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("link completed in another session")
	}
	linked, err := s.FinishOAuth(t.Context(), oauthTicket(t, s, member.Token), githubUser("300"), member.Token)
	if err != nil {
		t.Fatal(err)
	}
	if linked.Session.UserID != member.Session.UserID {
		t.Fatal("link created a second account")
	}
	if oauthLogin(t, s, "300").Session.UserID != member.Session.UserID {
		t.Fatal("linked account does not resolve to owner")
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE browser_sessions SET created_at=clock_timestamp()-interval '20 minutes' WHERE id=$1`, linked.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginOAuth(t.Context(), identity.NewToken(), identity.NewToken(), linked.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("old session started account link")
	}
}

func TestConcurrentOAuthBootstrapAndAuditAtomicity(t *testing.T) {
	t.Run("concurrent login", func(t *testing.T) {
		s := oauthStore(t)
		tickets := make([]string, 10)
		for i := range tickets {
			tickets[i] = oauthTicket(t, s, "")
		}
		var wg sync.WaitGroup
		results := make(chan error, 10)
		for _, ticket := range tickets {
			wg.Go(func() { _, err := s.FinishOAuth(t.Context(), ticket, githubUser("100"), ""); results <- err })
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, query := range []string{`SELECT count(*) FROM users`, `SELECT count(*) FROM external_identities`, `SELECT count(*) FROM memberships`, `SELECT count(*) FROM audit_events WHERE action='instance.bootstrapped'`} {
			var count int
			if err := s.pool.QueryRow(t.Context(), query).Scan(&count); err != nil || count != 1 {
				t.Fatalf("duplicate bootstrap: count=%d err=%v", count, err)
			}
		}
	})
	t.Run("audit failure", func(t *testing.T) {
		s := oauthStore(t)
		ticket := oauthTicket(t, s, "")
		rejectAudit(t, s)
		if _, err := s.FinishOAuth(t.Context(), ticket, githubUser("100"), ""); err == nil {
			t.Fatal("ignored audit failure")
		}
		for _, table := range []string{"users", "external_identities", "memberships", "browser_sessions"} {
			var count int
			if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial %s persisted", table)
			}
		}
		var consumed bool
		if err := s.pool.QueryRow(t.Context(), `SELECT consumed_at IS NOT NULL FROM oauth_bootstrap`).Scan(&consumed); err != nil || consumed {
			t.Fatal("failed bootstrap consumed administrator claim")
		}
	})
}
