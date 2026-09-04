package postgres

import (
	"errors"
	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestCancelRunAuthorizationAndCompletionRace(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Developer)
	r, err := f.store.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.project, storetest.Record(t, 2, false))
	if err != nil {
		t.Fatal(err)
	}
	grantProject(t, f, authorization.Viewer)
	if err := f.store.ChangeMembership(t.Context(), f.adminSession.Token, f.member, authorization.Scope{Kind: authorization.Project, ID: f.project}, authorization.Developer, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.project, r.ID); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	grantProject(t, f, authorization.Developer)
	if _, err := f.store.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.otherProject, r.ID); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("cross-project: %v", err)
	}
	job, err := f.store.ClaimJob(t.Context(), runmodel.ClaimRequest{RunnerName: "cancel-test"})
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	runnerID := uuid.New()
	if _, err := f.store.pool.Exec(t.Context(), `INSERT INTO runners(id,pool_id,name,status,capacity) SELECT $1,id,'cancel-runner','online',1 FROM runner_pools WHERE name='standard'`, runnerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(t.Context(), `UPDATE jobs SET runner_id=$2 WHERE id=$1`, job.JobID, runnerID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	wg.Go(func() {
		_, err := f.store.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.project, r.ID)
		failures <- err
	})
	wg.Go(func() {
		err := f.store.CompleteRunnerJob(t.Context(), runmodel.RunnerCompletion{RunnerID: runnerID, JobID: job.JobID, LeaseToken: job.LeaseToken, Status: runmodel.JobSucceeded})
		if errors.Is(err, runmodel.ErrLeaseInvalid) {
			err = nil
		}
		failures <- err
	})
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	status, err := f.store.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.project, r.ID)
	if err != nil || status != runmodel.StatusCanceled {
		t.Fatalf("convergence: %s %v", status, err)
	}
	heartbeat, err := f.store.RenewRunnerLeases(t.Context(), runmodel.HeartbeatRequest{Runner: runmodel.RunnerDescriptor{ID: runnerID, PoolType: "standard", OS: "linux", Architecture: "amd64", Executor: "docker", Capacity: 1, ProtocolVersion: 2, Labels: map[string]string{}}, ActiveJobs: []runmodel.ActiveLease{{JobID: job.JobID, LeaseToken: job.LeaseToken, State: "running"}}})
	if err != nil || len(heartbeat.Jobs) != 1 || heartbeat.Jobs[0].Renewed || heartbeat.Jobs[0].CancelReason == "" {
		t.Fatalf("cancellation signal: %+v %v", heartbeat, err)
	}
	var active, audits int
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE run_id=$1 AND (status IN ('queued','assigned','running','blocked') OR lease_token_hash IS NOT NULL)`, r.ID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("active: %d %v", active, err)
	}
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='run.canceled' AND resource_id=$1`, r.ID.String()).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits: %d %v", audits, err)
	}
}

func TestCancelRunAuditFailureRollsBack(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Developer)
	r, err := f.store.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.project, storetest.Record(t, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	rejectAudit(t, f.store)
	if _, err := f.store.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.project, r.ID); err == nil {
		t.Fatal("ignored audit failure")
	}
	var state string
	if err := f.store.pool.QueryRow(t.Context(), `SELECT status FROM runs WHERE id=$1`, r.ID).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("rollback: %s %v", state, err)
	}
}

func TestCancelRunHTTPAuthenticationCSRFAndRBAC(t *testing.T) {
	f := newAccessFixture(t)
	record, err := f.store.CreateAuthorizedRun(t.Context(), f.adminSession.Token, f.project, storetest.Record(t, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewAuthenticated(slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, f.store, 1<<20, "https://ci.example.test")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + f.project.String() + "/runs/" + record.ID.String() + "/cancel"
	call := func(token, origin, csrf string) int {
		r := httptest.NewRequest("POST", path, nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
		}
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := call("", "", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	grantProject(t, f, authorization.Viewer)
	token := f.memberSession.Token
	if code := call(token, "https://ci.example.test", identity.CSRFToken(token)); code != 403 {
		t.Fatalf("viewer: %d", code)
	}
	grantProject(t, f, authorization.Developer)
	if code := call(token, "https://attacker.test", identity.CSRFToken(token)); code != 403 {
		t.Fatalf("origin: %d", code)
	}
	if code := call(token, "https://ci.example.test", ""); code != 403 {
		t.Fatalf("csrf: %d", code)
	}
	if code := call(token, "https://ci.example.test", identity.CSRFToken(token)); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
}
