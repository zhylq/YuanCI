package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/db/migrations"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
)

func grantProject(t *testing.T, f accessFixture, role authorization.Role) {
	t.Helper()
	if err := f.store.ChangeMembership(t.Context(), f.adminSession.Token, f.member, authorization.Scope{Kind: authorization.Project, ID: f.project}, role, true); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizedRunsScopeAndRevocation(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	grantProject(t, f, authorization.Viewer)
	if _, err := s.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.project, storetest.Record(t, 1, false)); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("viewer created run")
	}
	grantProject(t, f, authorization.Developer)
	input := storetest.Record(t, 1, false)
	input.ProjectID = &f.otherProject
	input.CreatedBy = &f.admin
	created, err := s.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if *created.ProjectID != f.project || *created.CreatedBy != f.member {
		t.Fatal("caller forged run ownership")
	}
	if _, err := s.Create(t.Context(), storetest.Record(t, 1, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAuthorizedRun(t.Context(), f.adminSession.Token, f.otherProject, storetest.Record(t, 1, false)); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListAuthorizedRuns(t.Context(), f.memberSession.Token, f.project, 200)
	if err != nil || len(records) != 1 || records[0].ID != created.ID {
		t.Fatalf("scoped list leaked or lost records: count=%d err=%v", len(records), err)
	}
	if _, err := s.ListAuthorizedRuns(t.Context(), f.memberSession.Token, f.otherProject, 200); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("cross-project read")
	}
	if _, err := s.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.otherProject, storetest.Record(t, 1, false)); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("cross-project write")
	}
	if err := s.ChangeMembership(t.Context(), f.adminSession.Token, f.member, authorization.Scope{Kind: authorization.Project, ID: f.project}, authorization.Developer, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.project, storetest.Record(t, 1, false)); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("stale grant used after revocation")
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE repositories SET active=false WHERE id=$1`, f.project); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListAuthorizedRuns(t.Context(), f.memberSession.Token, f.project, 20); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("inactive repository accessible")
	}
}

func TestAuthorizedRunAuditFailureIsAtomic(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Developer)
	rejectAudit(t, f.store)
	if _, err := f.store.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.project, storetest.Record(t, 1, false)); err == nil {
		t.Fatal("run ignored audit failure")
	}
	for _, table := range []string{"runs", "jobs"} {
		var count int
		if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial %s write: count=%d err=%v", table, count, err)
		}
	}
}

func TestRevocationWinsBeforeWaitingRunAuthorization(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Developer)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := f.store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE user_id=$1`, f.member); err != nil {
		t.Fatal(err)
	}
	record := storetest.Record(t, 1, false)
	result := make(chan error, 1)
	go func() {
		_, err := f.store.CreateAuthorizedRun(ctx, f.memberSession.Token, f.project, record)
		result <- err
	}()
	waitForLock(t, ctx, f.store)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("authorization used revoked grant: %v", err)
	}
}

func waitForLock(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	for {
		var blocked bool
		err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND pid<>pg_backend_pid())`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("operation never waited for the lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestAuthenticatedBrowserAPI(t *testing.T) {
	f := newAccessFixture(t)
	handler, err := httpapi.NewAuthenticated(slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, f.store, 1<<20, "https://ci.example.test")
	if err != nil {
		t.Fatal(err)
	}
	pipeline := "version: v1\nname: fixture\nstages:\n  - name: test\n    jobs:\n      - name: unit\n        image: alpine\n        steps: [{name: test, commands: ['true']}]\n"
	body, _ := json.Marshal(map[string]string{"project_id": f.project.String(), "pipeline": pipeline})
	call := func(method, path, token, origin, csrf string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/session", "/api/v1/system/info", "/api/v1/runs?project_id=" + f.project.String()} {
		if w := call("GET", path, "", "", "", nil); w.Code != 401 {
			t.Fatal("anonymous browser access")
		}
	}
	token := f.memberSession.Token
	csrf := identity.CSRFToken(token)
	for _, pair := range [][2]string{{"", csrf}, {"null", csrf}, {"https://attacker.test", csrf}, {"https://ci.example.test", ""}, {"https://ci.example.test", identity.CSRFToken(identity.NewToken())}} {
		if w := call("POST", "/api/v1/runs", token, pair[0], pair[1], body); w.Code != 403 {
			t.Fatal("CSRF/origin check bypass")
		}
	}
	grantProject(t, f, authorization.Viewer)
	if w := call("POST", "/api/v1/runs", token, "https://ci.example.test", csrf, body); w.Code != 403 {
		t.Fatal("viewer HTTP write")
	}
	grantProject(t, f, authorization.Developer)
	if w := call("POST", "/api/v1/runs", token, "https://ci.example.test", csrf, body); w.Code != 201 {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	if w := call("GET", "/api/v1/runs?project_id="+f.otherProject.String(), token, "", "", nil); w.Code != 403 {
		t.Fatal("HTTP cross-project read")
	}
	if w := call("GET", "/api/v1/runs", token, "", "", nil); w.Code != 400 {
		t.Fatal("unscoped HTTP list")
	}
	w := call("GET", "/api/v1/session", token, "", "", nil)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte(token)) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session disclosure/cache policy")
	}
	var info struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.CSRF != csrf {
		t.Fatal("CSRF bootstrap")
	}
	if w := call("POST", "/api/v1/runner/jobs/claim", token, "https://ci.example.test", csrf, []byte(`{"runner_name":"x"}`)); w.Code != 404 {
		t.Fatal("browser reached legacy runner protocol")
	}
	for _, credentialMode := range []string{"duplicate", "bearer"} {
		r := httptest.NewRequest("GET", "/api/v1/session", nil)
		r.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
		if credentialMode == "duplicate" {
			r.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
		} else {
			r.Header.Set("Authorization", "Bearer runner-token")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("ambiguous credentials accepted")
		}
	}
	w = call("DELETE", "/api/v1/session", token, "https://ci.example.test", csrf, nil)
	if w.Code != 204 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout failed to expire cookie")
	}
	if w := call("GET", "/api/v1/session", token, "", "", nil); w.Code != 401 {
		t.Fatal("logged-out session replayed")
	}
	if _, err := f.store.CreateAuthorizedRun(t.Context(), token, f.project, storetest.Record(t, 1, false)); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("service bypassed revoked session")
	}
}

var _ runmodel.AuthorizedStore = (*Store)(nil)
var _ identity.Sessions = (*Store)(nil)

func TestSessionExpiryWhileWaitingForPermission(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Developer)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := f.store.pool.Exec(ctx, `UPDATE browser_sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1`, f.memberSession.Session.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM memberships WHERE user_id=$1 FOR UPDATE`, f.member); err != nil {
		t.Fatal(err)
	}
	record := storetest.Record(t, 1, false)
	result := make(chan error, 1)
	go func() {
		_, err := f.store.CreateAuthorizedRun(ctx, f.memberSession.Token, f.project, record)
		result <- err
	}()
	waitForLock(t, ctx, f.store)
	for {
		var expired bool
		if err := tx.QueryRow(ctx, `SELECT expires_at <= clock_timestamp() FROM browser_sessions WHERE id=$1`, f.memberSession.Session.ID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("accepted session that expired during permission wait: %v", err)
	}
}

func TestIdentityMigrationPreservesEvaluationRuns(t *testing.T) {
	databaseURL := newTestDatabase(t)
	connection, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	baseline, err := migrations.Files.ReadFile("000001_initial.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(t.Context(), string(baseline)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(t.Context(), `INSERT INTO schema_migrations(version) VALUES ('000001_initial.up.sql')`); err != nil {
		t.Fatal(err)
	}
	record := storetest.Record(t, 1, false)
	if _, err := connection.Exec(t.Context(), `INSERT INTO runs(id,pipeline_name,event,status,config_sha256,compiled_plan,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		record.ID, record.PipelineName, record.Event, record.Status, record.ConfigSHA256, record.Plan, record.CreatedAt); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	items, err := s.List(t.Context(), 20)
	if err != nil || len(items) != 1 || items[0].ID != record.ID || items[0].ProjectID != nil {
		t.Fatal("additive upgrade lost or reassigned evaluation run")
	}
	var count int
	if err := connection.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != 6 {
		t.Fatal("upgrade did not apply exactly the additive migration")
	}
}
