package postgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/authorization"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
)

// Snapshot immutable execution and registration identity, including terminal
// timestamps; cleanup confirmation may only add cleanup/release timestamps.
func recoveryIdentity(t *testing.T, s *Store, job uuid.UUID) string {
	t.Helper()
	var data string
	err := s.pool.QueryRow(t.Context(), `SELECT jsonb_build_object('job_status',j.status,'job_finished',j.finished_at,'run_status',r.status,'run_finished',r.finished_at,'token',encode(j.execution_token_hash,'hex'),'attempt',j.attempt,'run',d.run_id,'repository',d.repository_id,'environment',d.environment,'commit',d.commit_sha,'queue_order',d.queue_order)::text FROM jobs j JOIN runs r ON r.id=j.run_id JOIN deployment_runs d ON d.run_id=r.id WHERE j.id=$1`, job).Scan(&data)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func recoveryState(t *testing.T, s *Store, job uuid.UUID) (string, int) {
	t.Helper()
	var state string
	var audits int
	err := s.pool.QueryRow(t.Context(), `SELECT jsonb_build_object('cleanup',j.execution_finished_at,'release',d.released_at)::text,(SELECT count(*) FROM audit_events WHERE action='deployment.cleanup_confirmed' AND resource_id=$1::text) FROM jobs j JOIN deployment_runs d ON d.run_id=j.run_id WHERE j.id=$1::uuid`, job.String()).Scan(&state, &audits)
	if err != nil {
		t.Fatal(err)
	}
	return state, audits
}

func TestDeploymentRecoveryCanceledReleaseAndIdempotency(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	grantProject(t, f, authorization.Developer)
	first := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 1)
	next := deploymentRecord(t, f.project, "production", strings.Repeat("b", 40), 1)
	for _, r := range []runmodel.Record{first, next} {
		if _, err := s.Create(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 2, true)
	job := deploymentClaim(t, s, runner, first.ID)
	deploymentStart(t, s, runner, job)
	if _, err := s.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.project, first.ID); err != nil {
		t.Fatal(err)
	}
	deploymentClaim(t, s, runner, uuid.Nil)
	before := recoveryIdentity(t, s, job.JobID)
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "Runner stopped; exact job resources absent; remote command terminated"); err != nil {
		t.Fatal(err)
	}
	state, count := recoveryState(t, s, job.JobID)
	if count != 1 || strings.Contains(state, "null") {
		t.Fatalf("not released/audited: %s %d", state, count)
	}
	var metadata []byte
	if err := s.pool.QueryRow(t.Context(), `SELECT metadata FROM audit_events WHERE action='deployment.cleanup_confirmed' AND resource_id=$1`, job.JobID.String()).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var audit map[string]string
	if err := json.Unmarshal(metadata, &audit); err != nil {
		t.Fatal(err)
	}
	if audit["job_id"] != job.JobID.String() || audit["run_id"] != first.ID.String() || audit["runner_id"] != runner.String() || audit["operator"] == "" || audit["authority"] != "database_admin" || audit["reason"] == "" {
		t.Fatalf("audit incomplete: %s", metadata)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "repeat inspection"); err != nil {
		t.Fatal(err)
	}
	after, count := recoveryState(t, s, job.JobID)
	if state != after || count != 1 || before != recoveryIdentity(t, s, job.JobID) {
		t.Fatal("repeat changed result/identity/audit")
	}
	deploymentClaim(t, s, runner, next.ID)
}

func TestDeploymentRecoveryAuditFailureRollbackAndOtherPendingJob(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	r := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 2)
	if _, err := s.Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 2, true)
	job := deploymentClaim(t, s, runner, r.ID)
	deploymentStart(t, s, runner, job)
	deploymentComplete(t, s, runner, job, runmodel.JobFailed, false)
	// Historical pending cleanup from a second execution must keep the gate held.
	var other uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `UPDATE jobs SET started_at=clock_timestamp(),runner_id=$2,execution_token_hash=decode(repeat('ab',32),'hex') WHERE run_id=$1 AND id<>$3 RETURNING id`, r.ID, runner, job.JobID).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `CREATE FUNCTION reject_cleanup_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='deployment.cleanup_confirmed' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_cleanup_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_cleanup_audit()`); err != nil {
		t.Fatal(err)
	}
	before, count := recoveryState(t, s, job.JobID)
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "inspected"); err == nil {
		t.Fatal("audit failure accepted")
	}
	after, newCount := recoveryState(t, s, job.JobID)
	if before != after || count != newCount {
		t.Fatal("audit failure did not roll back")
	}
	if _, err := s.pool.Exec(t.Context(), `DROP TRIGGER reject_cleanup_audit ON audit_events; DROP FUNCTION reject_cleanup_audit()`); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "inspected"); err != nil {
		t.Fatal(err)
	}
	var held bool
	if err := s.pool.QueryRow(t.Context(), `SELECT released_at IS NULL FROM deployment_runs WHERE run_id=$1`, r.ID).Scan(&held); err != nil || !held {
		t.Fatalf("pending execution released: %v", err)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), other, "second execution inspected"); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(t.Context(), `SELECT released_at IS NULL FROM deployment_runs WHERE run_id=$1`, r.ID).Scan(&held); err != nil || held {
		t.Fatalf("all confirmed still held: %v", err)
	}
}

func TestDeploymentRecoveryDeniesLiveStandardAndUnstarted(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	grantProject(t, f, authorization.Developer)
	r := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 2)
	if _, err := s.Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 2, true)
	job := deploymentClaim(t, s, runner, r.ID)
	deploymentStart(t, s, runner, job)
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "inspected"); !errors.Is(err, ErrDeploymentRecoveryDenied) {
		t.Fatalf("live accepted: %v", err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE jobs SET status='failed',finished_at=clock_timestamp() WHERE id=$1`, job.JobID); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "inspected"); !errors.Is(err, ErrDeploymentRecoveryDenied) {
		t.Fatalf("active Run with terminal job accepted: %v", err)
	}
	if _, err := s.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.project, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE jobs SET status='running' WHERE id=$1`, job.JobID); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "inspected"); !errors.Is(err, ErrDeploymentRecoveryDenied) {
		t.Fatalf("terminal Run with live job accepted: %v", err)
	}
	var unstarted uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `SELECT id FROM jobs WHERE run_id=$1 AND started_at IS NULL`, r.ID).Scan(&unstarted); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), unstarted, "inspected"); !errors.Is(err, ErrDeploymentRecoveryDenied) {
		t.Fatalf("unstarted accepted: %v", err)
	}
	standard := storetest.Record(t, 1, false)
	if _, err := s.Create(t.Context(), standard); err != nil {
		t.Fatal(err)
	}
	var standardJob uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `UPDATE jobs SET status='failed',started_at=clock_timestamp(),finished_at=clock_timestamp(),runner_id=$2 WHERE run_id=$1 RETURNING id`, standard.ID, runner).Scan(&standardJob); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET status='failed',finished_at=clock_timestamp() WHERE id=$1`, standard.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), standardJob, "inspected"); !errors.Is(err, ErrDeploymentRecoveryDenied) {
		t.Fatalf("standard accepted: %v", err)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), uuid.New(), "inspected"); !errors.Is(err, ErrDeploymentRecoveryDenied) {
		t.Fatalf("unknown accepted: %v", err)
	}
}

func TestDeploymentRecoveryAlreadyConfirmedRunnerCompletion(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	r := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 1)
	if _, err := s.Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 1, true)
	job := deploymentClaim(t, s, runner, r.ID)
	deploymentStart(t, s, runner, job)
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, true)
	before, count := recoveryState(t, s, job.JobID)
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "inspected"); err != nil {
		t.Fatal(err)
	}
	after, newCount := recoveryState(t, s, job.JobID)
	if before != after || count != 0 || newCount != 0 {
		t.Fatal("already confirmed execution changed")
	}
}

func TestDeploymentRecoveryRunnerLossAuditRollback(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	first := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 1)
	next := deploymentRecord(t, f.project, "production", strings.Repeat("b", 40), 1)
	for _, record := range []runmodel.Record{first, next} {
		if _, err := s.Create(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 2, true)
	job := deploymentClaim(t, s, runner, first.ID)
	deploymentStart(t, s, runner, job)
	if _, err := s.pool.Exec(t.Context(), `UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, job.JobID); err != nil {
		t.Fatal(err)
	}
	if recovered, err := s.RecoverExpiredRunnerLeases(t.Context(), 10); err != nil || recovered.Failed != 1 {
		t.Fatalf("loss recovery: %v %v", recovered, err)
	}
	deploymentClaim(t, s, runner, uuid.Nil)
	identity := recoveryIdentity(t, s, job.JobID)
	before, _ := recoveryState(t, s, job.JobID)
	if _, err := s.pool.Exec(t.Context(), `CREATE FUNCTION reject_loss_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER reject_loss_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_loss_audit()`); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "inspected"); err == nil {
		t.Fatal("audit failure accepted")
	}
	after, count := recoveryState(t, s, job.JobID)
	if before != after || count != 0 {
		t.Fatal("cleanup or release survived audit failure")
	}
	deploymentClaim(t, s, runner, uuid.Nil)
	if _, err := s.pool.Exec(t.Context(), `DROP TRIGGER reject_loss_audit ON audit_events; DROP FUNCTION reject_loss_audit()`); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmDeploymentStopped(t.Context(), job.JobID, "lost Runner stopped and all operations inspected"); err != nil {
		t.Fatal(err)
	}
	if identity != recoveryIdentity(t, s, job.JobID) {
		t.Fatal("lost execution result or identity changed")
	}
	deploymentClaim(t, s, runner, next.ID)
}
