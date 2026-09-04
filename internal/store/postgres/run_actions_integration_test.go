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

func TestRerunImmutableIdentityIdempotencyAndDAG(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Developer)
	input := storetest.Record(t, 2, true)
	input.CommitSHA = "0123456789abcdef0123456789abcdef01234567"
	original, err := f.store.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RerunAuthorizedRun(t.Context(), f.memberSession.Token, f.project, original.ID, "full", uuid.New()); !errors.Is(err, runmodel.ErrRunConflict) {
		t.Fatalf("active rerun: %v", err)
	}
	first, err := f.store.ClaimJob(t.Context(), runmodel.ClaimRequest{RunnerName: "rerun"})
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if err := f.store.CompleteJob(t.Context(), first.JobID, first.LeaseToken, runmodel.JobSucceeded); err != nil {
		t.Fatal(err)
	}
	second, err := f.store.ClaimJob(t.Context(), runmodel.ClaimRequest{RunnerName: "rerun"})
	if err != nil || second == nil {
		t.Fatal(err)
	}
	if err := f.store.CompleteJob(t.Context(), second.JobID, second.LeaseToken, runmodel.JobFailed); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	retry, err := f.store.RerunAuthorizedRun(t.Context(), f.memberSession.Token, f.project, original.ID, "failed", requestID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID == original.ID || retry.CommitSHA != original.CommitSHA || retry.ConfigSHA256 != original.ConfigSHA256 {
		t.Fatal("immutable identity changed")
	}
	var identical bool
	if err := f.store.pool.QueryRow(t.Context(), `SELECT r.compiled_plan=o.compiled_plan AND r.pipeline_version_id IS NOT DISTINCT FROM o.pipeline_version_id FROM runs r,runs o WHERE r.id=$1 AND o.id=$2`, retry.ID, original.ID).Scan(&identical); err != nil || !identical {
		t.Fatal("plan identity changed")
	}
	replay, err := f.store.RerunAuthorizedRun(t.Context(), f.memberSession.Token, f.project, original.ID, "failed", requestID)
	if err != nil || replay.ID != retry.ID {
		t.Fatalf("idempotency: %v", err)
	}
	var reused, queued int
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE reused_from_job_id IS NOT NULL AND status='succeeded'),count(*) FILTER (WHERE status='queued') FROM jobs WHERE run_id=$1`, retry.ID).Scan(&reused, &queued); err != nil || reused != 1 || queued != 1 {
		t.Fatalf("DAG reused=%d queued=%d err=%v", reused, queued, err)
	}
	next, err := f.store.ClaimJob(t.Context(), runmodel.ClaimRequest{RunnerName: "rerun"})
	if err != nil || next == nil || next.JobName != second.JobName {
		t.Fatalf("wrong failed job: %+v %v", next, err)
	}
	if err := f.store.CompleteJob(t.Context(), next.JobID, next.LeaseToken, runmodel.JobSucceeded); err != nil {
		t.Fatal(err)
	}
	join, err := f.store.ClaimJob(t.Context(), runmodel.ClaimRequest{RunnerName: "rerun"})
	if err != nil || join == nil || join.JobName != "join" {
		t.Fatalf("downstream DAG: %+v %v", join, err)
	}
	if err := f.store.CompleteJob(t.Context(), join.JobID, join.LeaseToken, runmodel.JobSucceeded); err != nil {
		t.Fatal(err)
	}
	full, err := f.store.RerunAuthorizedRun(t.Context(), f.memberSession.Token, f.project, original.ID, "full", uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE run_id=$1 AND reused_from_job_id IS NOT NULL`, full.ID).Scan(&reused); err != nil || reused != 0 {
		t.Fatal("full rerun reused results")
	}
	if _, err := f.store.RerunAuthorizedRun(t.Context(), f.memberSession.Token, f.otherProject, original.ID, "full", uuid.New()); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("cross-project rerun")
	}
}

func TestRerunConcurrentReplayAndAuditRollback(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Developer)
	original, err := f.store.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.project, storetest.Record(t, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.project, original.ID); err != nil {
		t.Fatal(err)
	}
	key := uuid.New()
	ids := make(chan uuid.UUID, 8)
	failures := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := f.store.RerunAuthorizedRun(t.Context(), f.memberSession.Token, f.project, original.ID, "full", key)
			ids <- r.ID
			failures <- err
		})
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	unique := map[uuid.UUID]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatal("duplicate reruns")
	}
	rejectAudit(t, f.store)
	if _, err := f.store.RerunAuthorizedRun(t.Context(), f.memberSession.Token, f.project, original.ID, "full", uuid.New()); err == nil {
		t.Fatal("ignored audit failure")
	}
	var count int
	if err := f.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE rerun_of=$1`, original.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback: %d %v", count, err)
	}
}

func TestRunDetailAndLogsAreScopedOrderedAndBounded(t *testing.T) {
	f := newAccessFixture(t)
	grantProject(t, f, authorization.Viewer)
	original, err := f.store.CreateAuthorizedRun(t.Context(), f.adminSession.Token, f.project, storetest.Record(t, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.store.GetAuthorizedRun(t.Context(), f.memberSession.Token, f.project, original.ID)
	if err != nil || len(detail.Jobs) != 1 {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	jobID := detail.Jobs[0].ID
	if _, err := f.store.pool.Exec(t.Context(), `INSERT INTO job_log_streams(job_id,expires_at) VALUES($1,clock_timestamp()+interval '1 day')`, jobID); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		if _, err := f.store.pool.Exec(t.Context(), `INSERT INTO job_log_chunks(job_id,sequence,step_index,stream,data) VALUES($1,$2,0,'stdout',$3)`, jobID, i, []byte("safe output")); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.store.ReadAuthorizedLogs(t.Context(), f.memberSession.Token, f.project, original.ID, jobID, 0)
	if err != nil || len(page.Items) != 16 || page.NextSequence != 16 {
		t.Fatalf("page: %+v %v", page, err)
	}
	page, err = f.store.ReadAuthorizedLogs(t.Context(), f.memberSession.Token, f.project, original.ID, jobID, page.NextSequence)
	if err != nil || len(page.Items) != 4 || page.NextSequence != 20 {
		t.Fatalf("cursor: %+v %v", page, err)
	}
	if _, err := f.store.ReadAuthorizedLogs(t.Context(), f.memberSession.Token, f.otherProject, original.ID, jobID, 0); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("cross-project logs")
	}
	if _, err := f.store.ReadAuthorizedLogs(t.Context(), f.memberSession.Token, f.project, original.ID, uuid.New(), 0); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal("foreign Job logs")
	}
	if _, err := f.store.pool.Exec(t.Context(), `UPDATE job_log_streams SET expires_at=clock_timestamp()-interval '1 second' WHERE job_id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	page, err = f.store.ReadAuthorizedLogs(t.Context(), f.memberSession.Token, f.project, original.ID, jobID, 0)
	if err != nil || !page.Expired || len(page.Items) != 0 {
		t.Fatal("expired logs disclosed")
	}
}
