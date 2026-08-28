package postgres

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/identity"
)

type accessFixture struct {
	store                                                      *Store
	admin, member                                              uuid.UUID
	instance, organization, project, otherProject, environment uuid.UUID
	adminSession, memberSession                                identity.Credentials
}

func newAccessFixture(t *testing.T) accessFixture {
	t.Helper()
	s, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	f := accessFixture{store: s, admin: uuid.New(), member: uuid.New(), organization: uuid.New(), project: uuid.New(), otherProject: uuid.New(), environment: uuid.New()}
	for _, id := range []uuid.UUID{f.admin, f.member} {
		if _, err := s.pool.Exec(t.Context(), `INSERT INTO users(id,display_name) VALUES ($1,'fixture')`, id); err != nil {
			t.Fatal(err)
		}
	}
	otherOrg := uuid.New()
	for _, id := range []uuid.UUID{f.organization, otherOrg} {
		if _, err := s.pool.Exec(t.Context(), `INSERT INTO organizations(id,slug,display_name) VALUES ($1,$2,'fixture')`, id, id.String()); err != nil {
			t.Fatal(err)
		}
	}
	for project, org := range map[uuid.UUID]uuid.UUID{f.project: f.organization, f.otherProject: otherOrg} {
		_, err := s.pool.Exec(t.Context(), `INSERT INTO repositories(id,organization_id,provider,provider_instance,external_id,owner,name,clone_url,default_branch)
            VALUES ($1,$2,'github','https://github.com',$3,'test','fixture','https://example.test/repo.git','main')`, project, org, project.String())
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO environments(id,repository_id,name) VALUES ($1,$2,'production')`, f.environment, f.project); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(t.Context(), `SELECT id FROM instances`).Scan(&f.instance); err != nil {
		t.Fatal(err)
	}
	// Test-only bootstrap; no public endpoint or CLI performs this elevation.
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO memberships(user_id,role,instance_id) VALUES ($1,'admin',$2)`, f.admin, f.instance); err != nil {
		t.Fatal(err)
	}
	f.adminSession, err = s.IssueSession(t.Context(), f.admin, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.memberSession, err = s.IssueSession(t.Context(), f.member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSessionsPersistAndFailClosed(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	digest, _ := identity.TokenDigest(f.memberSession.Token)
	var stored []byte
	if err := s.pool.QueryRow(t.Context(), `SELECT token_hash FROM browser_sessions WHERE id=$1`, f.memberSession.Session.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, digest[:]) || bytes.Contains(stored, []byte(f.memberSession.Token)) {
		t.Fatal("session storage is not hash-only")
	}
	if _, err := s.AuthenticateSession(t.Context(), f.memberSession.Token); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "bad", identity.NewToken()} {
		if _, err := s.AuthenticateSession(t.Context(), bad); !errors.Is(err, identity.ErrUnauthenticated) {
			t.Fatal("invalid session accepted")
		}
	}
	if _, err := s.IssueSession(t.Context(), uuid.New(), time.Hour); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("missing user accepted")
	}
	if _, err := s.IssueSession(t.Context(), f.member, 25*time.Hour); err == nil {
		t.Fatal("unbounded session lifetime")
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE users SET status='suspended' WHERE id=$1`, f.member); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateSession(t.Context(), f.memberSession.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("suspended user session accepted")
	}
	if _, err := s.IssueSession(t.Context(), f.member, time.Hour); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("issued suspended user session")
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE users SET status='active' WHERE id=$1`, f.member); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(t.Context(), f.memberSession.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateSession(t.Context(), f.memberSession.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("revoked session accepted")
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE browser_sessions SET created_at=now()-interval '2 hours', expires_at=now()-interval '1 hour' WHERE id=$1`, f.adminSession.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateSession(t.Context(), f.adminSession.Token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("expired session accepted")
	}
	var count int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action LIKE 'session.%'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("session audit count=%d err=%v", count, err)
	}
	var leaked bool
	if err := s.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM audit_events WHERE metadata::text LIKE $1)`, "%"+f.memberSession.Token+"%").Scan(&leaked); err != nil || leaked {
		t.Fatal("session token leaked to audit")
	}
}

func TestMembershipConstraintsAndAuthorization(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	scope := authorization.Scope{Kind: authorization.Organization, ID: f.organization}
	if err := s.ChangeMembership(t.Context(), f.memberSession.Token, f.admin, scope, authorization.Admin, true); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("member escalated permissions")
	}
	if err := s.ChangeMembership(t.Context(), f.adminSession.Token, f.admin, scope, authorization.Admin, true); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("self grant accepted")
	}
	if err := s.ChangeMembership(t.Context(), f.adminSession.Token, f.member, scope, authorization.Developer, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeMembership(t.Context(), f.adminSession.Token, f.member, scope, authorization.Developer, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeMembership(t.Context(), f.adminSession.Token, f.member, scope, authorization.Role("wildcard"), true); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("unknown role accepted")
	}
	if err := s.ChangeMembership(t.Context(), f.adminSession.Token, f.member, scope, authorization.Approver, true); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("broad approver accepted")
	}
	env := authorization.Scope{Kind: authorization.Environment, ID: f.environment}
	if err := s.ChangeMembership(t.Context(), f.adminSession.Token, f.member, env, authorization.Approver, true); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("inherited admin modified protected membership")
	}
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO memberships(user_id,role,environment_id) VALUES ($1,'admin',$2)`, f.admin, f.environment); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeMembership(t.Context(), f.adminSession.Token, f.member, env, authorization.Approver, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeMembership(t.Context(), f.adminSession.Token, f.member, scope, authorization.Developer, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action LIKE 'membership.%'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("membership audit count=%d err=%v", count, err)
	}
	for _, query := range []string{
		`INSERT INTO memberships(user_id,role) VALUES ($1,'viewer')`,
		`INSERT INTO memberships(user_id,role,repository_id) VALUES ($1,'viewer',gen_random_uuid())`,
		`INSERT INTO memberships(user_id,role,instance_id,organization_id) SELECT $1,'viewer',id,$2 FROM instances`,
	} {
		args := []any{f.member}
		if strings.Contains(query, "$2") {
			args = append(args, f.organization)
		}
		if _, err := s.pool.Exec(t.Context(), query, args...); err == nil {
			t.Fatal("invalid scope persisted")
		}
	}
}

func rejectAudit(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.pool.Exec(t.Context(), `CREATE FUNCTION reject_test_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END; $$;
        CREATE TRIGGER reject_test_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_test_audit();`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestIdentityAuditFailureRollsBack(t *testing.T) {
	f := newAccessFixture(t)
	rejectAudit(t, f.store)
	if _, err := f.store.IssueSession(t.Context(), f.member, time.Hour); err == nil {
		t.Fatal("session creation ignored audit failure")
	}
	if err := f.store.RevokeSession(t.Context(), f.memberSession.Token); err == nil {
		t.Fatal("revoke ignored audit failure")
	}
	if _, err := f.store.AuthenticateSession(t.Context(), f.memberSession.Token); err != nil {
		t.Fatal("failed revoke changed session")
	}
	if err := f.store.ChangeMembership(t.Context(), f.adminSession.Token, f.member, authorization.Scope{Kind: authorization.Project, ID: f.project}, authorization.Developer, true); err == nil {
		t.Fatal("grant ignored audit failure")
	}
	var sessions, memberships int
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM browser_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM memberships WHERE user_id=$1`, f.member).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if sessions != 2 || memberships != 0 {
		t.Fatalf("partial mutation: sessions=%d memberships=%d", sessions, memberships)
	}
}
