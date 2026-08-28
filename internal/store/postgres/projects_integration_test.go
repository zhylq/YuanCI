package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/run/storetest"
)

func TestProjectsScopePaginationAndMinimalDTO(t *testing.T) {
	f := newAccessFixture(t)
	ctx, s := t.Context(), f.store
	q := project.Query{Limit: 1}
	page, err := s.ListProjects(ctx, f.memberSession.Token, q)
	if err != nil || len(page.Items) != 0 || page.Items == nil || page.NextCursor != "" {
		t.Fatal("ungranted list", err)
	}
	// An environment grant never exposes the containing repository.
	if _, err := s.pool.Exec(ctx, `UPDATE environments SET protected=false WHERE id=$1`, f.environment); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeMembership(ctx, f.adminSession.Token, f.member, authorization.Scope{Kind: authorization.Environment, ID: f.environment}, authorization.Approver, true); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListProjects(ctx, f.memberSession.Token, q)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("environment grant inherited upward", err)
	}
	grantProject(t, f, authorization.Viewer)
	grantProject(t, f, authorization.Developer)
	page, err = s.ListProjects(ctx, f.memberSession.Token, q)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != f.project || page.NextCursor != "" {
		t.Fatal("duplicate grants or cross-org leak", err)
	}
	if _, err := s.GetProject(ctx, f.memberSession.Token, f.otherProject); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("cross-org detail")
	}
	if _, err := s.GetProject(ctx, f.memberSession.Token, uuid.New()); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("missing detail")
	}
	if err := s.ChangeMembership(ctx, f.adminSession.Token, f.member, authorization.Scope{Kind: authorization.Organization, ID: f.organization}, authorization.Viewer, true); err != nil {
		t.Fatal(err)
	}
	// More than one internal SQL batch, plus a disabled repository, exercise
	// filtering before public keyset pagination without global totals.
	for i := 0; i < 105; i++ {
		id := uuid.New()
		if _, err := s.pool.Exec(ctx, `INSERT INTO repositories(id,organization_id,provider,provider_instance,external_id,owner,name,clone_url,default_branch)
            VALUES ($1,$2,'github','https://github.com',$3,'team','search%_fixture','https://secret:credential@example.test/repo','main')`, id, f.organization, id.String()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE repositories SET active=false WHERE id=$1`, f.project); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject(ctx, f.memberSession.Token, f.project); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("inactive detail")
	}
	seen := map[uuid.UUID]bool{}
	q = project.Query{Limit: 20, Search: "%_"}
	for {
		page, err = s.ListProjects(ctx, f.memberSession.Token, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 20 {
			t.Fatal("page bound")
		}
		for _, item := range page.Items {
			if seen[item.ID] || item.Organization.ID != f.organization || item.ConnectionStatus != "not_connected" {
				t.Fatal("duplicate/leaked/false connectivity")
			}
			seen[item.ID] = true
		}
		data, _ := json.Marshal(page)
		for _, forbidden := range []string{"credential", "clone_url", "external_id", "provider_instance", "total"} {
			if strings.Contains(string(data), forbidden) {
				t.Fatal("sensitive DTO", forbidden)
			}
		}
		if page.NextCursor == "" {
			break
		}
		q.After = page.NextCursor
	}
	if len(seen) != 105 {
		t.Fatalf("lost pagination records: %d", len(seen))
	}
	page, err = s.ListProjects(ctx, f.memberSession.Token, project.Query{Limit: 100, Search: "%_"})
	if err != nil || len(page.Items) != 100 || page.NextCursor == "" {
		t.Fatal("max page", err)
	}
	page, err = s.ListProjects(ctx, f.memberSession.Token, project.Query{Limit: 20, Search: "' OR true --"})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("search injection", err)
	}
}

func TestProjectRunPagesStayScoped(t *testing.T) {
	f := newAccessFixture(t)
	s, ctx := f.store, t.Context()
	grantProject(t, f, authorization.Viewer)
	stamp := time.Now().UTC().Truncate(time.Microsecond)
	wanted := map[uuid.UUID]bool{}
	for i := 0; i < 5; i++ {
		r := storetest.Record(t, 1, false)
		r.CreatedAt = stamp
		created, err := s.CreateAuthorizedRun(ctx, f.adminSession.Token, f.project, r)
		if err != nil {
			t.Fatal(err)
		}
		wanted[created.ID] = true
	}
	other, err := s.CreateAuthorizedRun(ctx, f.adminSession.Token, f.otherProject, storetest.Record(t, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, storetest.Record(t, 1, false)); err != nil {
		t.Fatal(err)
	}
	q := project.Query{Limit: 2}
	seen := map[uuid.UUID]bool{}
	for {
		page, err := s.ListProjectRuns(ctx, f.memberSession.Token, f.project, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if !wanted[item.ID] || seen[item.ID] {
				t.Fatal("unscoped or duplicate run")
			}
			seen[item.ID] = true
		}
		data, _ := json.Marshal(page)
		for _, bad := range []string{"\"plan\"", "commands", "created_by", "config_sha256", other.ID.String()} {
			if strings.Contains(string(data), bad) {
				t.Fatal("run DTO leak")
			}
		}
		if page.NextCursor == "" {
			break
		}
		q.After = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatal("equal-time cursor lost runs")
	}
	if _, err := s.ListProjectRuns(ctx, f.memberSession.Token, f.otherProject, q); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("cursor granted cross-project access")
	}
	q.After = project.EncodeRunCursor(other.CreatedAt, other.ID)
	page, err := s.ListProjectRuns(ctx, f.memberSession.Token, f.project, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if !wanted[item.ID] {
			t.Fatal("foreign cursor leaked run")
		}
	}
	if err := s.ChangeMembership(ctx, f.adminSession.Token, f.member, authorization.Scope{Kind: authorization.Project, ID: f.project}, authorization.Viewer, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListProjectRuns(ctx, f.memberSession.Token, f.project, q); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("revoked cursor replay")
	}
}

func TestProjectQueriesRejectRevokedAndExpiredSessions(t *testing.T) {
	for _, mode := range []string{"revoked", "suspended", "expired"} {
		t.Run(mode, func(t *testing.T) {
			f := newAccessFixture(t)
			ctx := t.Context()
			grantProject(t, f, authorization.Viewer)
			var err error
			switch mode {
			case "revoked":
				err = f.store.RevokeSession(ctx, f.memberSession.Token)
			case "suspended":
				_, err = f.store.pool.Exec(ctx, `UPDATE users SET status='suspended' WHERE id=$1`, f.member)
			case "expired":
				_, err = f.store.pool.Exec(ctx, `UPDATE browser_sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, f.memberSession.Session.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, a := f.store.ListProjects(ctx, f.memberSession.Token, project.Query{Limit: 20})
			_, b := f.store.GetProject(ctx, f.memberSession.Token, f.project)
			_, c := f.store.ListProjectRuns(ctx, f.memberSession.Token, f.project, project.Query{Limit: 20})
			for _, err := range []error{a, b, c} {
				if !errors.Is(err, identity.ErrUnauthenticated) {
					t.Fatal("stale session read", err)
				}
			}
		})
	}
}

func TestProjectListWaitsForMembershipRevocation(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Viewer)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := f.store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(982716421)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE user_id=$1`, f.member); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		page, err := f.store.ListProjects(ctx, f.memberSession.Token, project.Query{Limit: 20})
		if err == nil && len(page.Items) != 0 {
			err = errors.New("revoked grant read")
		}
		result <- err
	}()
	waitForLock(t, ctx, f.store)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestProjectBrowserHTTP(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Viewer)
	h, err := httpapi.NewAuthenticated(slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, f.store, 1<<20, "https://ci.example.test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	base := "/api/v1/projects/" + f.project.String()
	for _, path := range []string{"/api/v1/projects", base, base + "/runs"} {
		if w := call(path, ""); w.Code != 401 {
			t.Fatal("anonymous project API")
		}
		if w := call(path, f.memberSession.Token); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("project API %s %d %s", path, w.Code, w.Body.String())
		}
	}
	a := call("/api/v1/projects/"+f.otherProject.String(), f.memberSession.Token)
	b := call("/api/v1/projects/"+uuid.NewString(), f.memberSession.Token)
	if a.Code != 404 || b.Code != 404 || a.Body.String() != b.Body.String() {
		t.Fatal("existence oracle")
	}
	for _, path := range []string{"/api/v1/projects?limit=101", "/api/v1/projects?limit=0", "/api/v1/projects?limit=x", "/api/v1/projects?limit=1&limit=2", "/api/v1/projects?after=bad", "/api/v1/projects?q=%00", "/api/v1/projects?q=%zz", "/api/v1/projects?offset=1", base + "/runs?after=bad", base + "/runs?q=x"} {
		if w := call(path, f.memberSession.Token); w.Code != 400 {
			t.Fatalf("bad query %s status %d", path, w.Code)
		}
	}
	if w := call("/api/v1/projects/not-an-id", f.memberSession.Token); w.Code != 404 {
		t.Fatal("malformed project")
	}
}

func TestProjectReadsExpireWhileWaitingForResource(t *testing.T) {
	for _, operation := range []string{"list", "detail", "runs"} {
		t.Run(operation, func(t *testing.T) {
			f := newAccessFixture(t)
			grantProject(t, f, authorization.Viewer)
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
			if _, err := tx.Exec(ctx, `SELECT id FROM repositories WHERE id=$1 FOR UPDATE`, f.project); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "list":
					_, err = f.store.ListProjects(ctx, f.memberSession.Token, project.Query{Limit: 20})
				case "detail":
					_, err = f.store.GetProject(ctx, f.memberSession.Token, f.project)
				case "runs":
					_, err = f.store.ListProjectRuns(ctx, f.memberSession.Token, f.project, project.Query{Limit: 20})
				}
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
				t.Fatalf("expired read returned data: %v", err)
			}
		})
	}
}
