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
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/db/migrations"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/githubci"
	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
)

func TestDeploymentMigrationDowngradePreservesRegisteredIdentity(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	body, err := migrations.Files.ReadFile("000021_deployment_runs.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	// A never-used installation can still roll this additive migration back.
	tx, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), string(body)); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("empty migration downgrade: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	record := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 1)
	if _, err := s.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 1, true)
	job := deploymentClaim(t, s, runner, record.ID)
	deploymentStart(t, s, runner, job)
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, true)
	tx, err = s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	_, downgradeErr := tx.Exec(t.Context(), string(body))
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if downgradeErr == nil {
		t.Fatal("downgrade erased a released deployment registration")
	}
	var durable bool
	if err := s.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM deployment_runs AS d JOIN jobs AS j ON j.run_id=d.run_id WHERE d.run_id=$1 AND d.released_at IS NOT NULL AND octet_length(j.execution_token_hash)=32 AND j.execution_finished_at IS NOT NULL)`, record.ID).Scan(&durable); err != nil || !durable {
		t.Fatalf("downgrade damaged execution identity: %v", err)
	}
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, true)
}

func TestDeploymentRegistrationSerializesCommitVisibility(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	first := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 1)
	second := deploymentRecord(t, f.project, "production", strings.Repeat("b", 40), 1)
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 2, true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := insertRun(ctx, tx, first); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Create(ctx, second); done <- err }()
	// Observe the actual registration lock wait, rather than assuming that a
	// goroutine has started. The second Run cannot become visible before first.
	for {
		var waiting bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("second registration escaped commit gate: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	deploymentClaim(t, s, runner, uuid.Nil)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	deploymentClaim(t, s, runner, first.ID)
}

func TestDeploymentAlreadyRunningClaimsCannotOverlap(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	record := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 8)
	if _, err := s.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET status='running' WHERE id=$1`, record.ID); err != nil {
		t.Fatal(err)
	}
	runners := make([]uuid.UUID, 8)
	for i := range runners {
		runners[i] = deploymentRunner(t, s, f.project, "deployment", 3, 32, true)
	}
	for round := 0; round < 8; round++ {
		jobs := make(chan *runmodel.Assignment, 32)
		failures := make(chan error, 32)
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			runner := runners[i%len(runners)]
			wg.Go(func() {
				job, err := s.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner})
				jobs <- job
				failures <- err
			})
		}
		wg.Wait()
		close(jobs)
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		var assignment *runmodel.Assignment
		for job := range jobs {
			if job != nil {
				if assignment != nil {
					t.Fatalf("overlapping jobs on already-running Run, round %d", round)
				}
				assignment = job
			}
		}
		if assignment == nil || assignment.RunID != record.ID {
			t.Fatalf("missing assignment round %d", round)
		}
		var runner uuid.UUID
		if err := s.pool.QueryRow(t.Context(), `SELECT runner_id FROM jobs WHERE id=$1`, assignment.JobID).Scan(&runner); err != nil {
			t.Fatal(err)
		}
		deploymentStart(t, s, runner, assignment)
		deploymentComplete(t, s, runner, assignment, runmodel.JobSucceeded, true)
	}
}

const deploymentYAML = `version: v1
name: deployment
deployment:
  environment: production
stages:
  - name: deploy
    jobs:
      - name: commands
        image: alpine
        steps:
          - name: deploy
            commands: [echo deploy]
`

func deploymentRecord(t *testing.T, repository uuid.UUID, environment, sha string, jobs int) runmodel.Record {
	t.Helper()
	plan, err := pipeline.Compile([]byte(strings.Replace(deploymentYAML, "environment: production", "environment: "+environment, 1)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < jobs; i++ {
		job := plan.Stages[0].Jobs[0]
		job.Name = uuid.NewString()
		plan.Stages[0].Jobs = append(plan.Stages[0].Jobs, job)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return runmodel.Record{ID: uuid.New(), ProjectID: &repository, PipelineName: plan.Name, Event: "manual", CommitSHA: sha, Status: runmodel.StatusQueued, Plan: data, ConfigSHA256: plan.ConfigSHA256, CreatedAt: time.Now().UTC()}
}

func deploymentRunner(t *testing.T, s *Store, repository uuid.UUID, pool string, protocol, capacity int, grant bool) uuid.UUID {
	t.Helper()
	var provider, externalID string
	if err := s.pool.QueryRow(t.Context(), `SELECT provider,external_id FROM repositories WHERE id=$1`, repository).Scan(&provider, &externalID); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	if grant {
		labels[pipeline.DeploymentRepositoryLabel(provider, externalID)] = "true"
	}
	data, _ := json.Marshal(labels)
	poolID, id := uuid.New(), uuid.New()
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO runner_pools(id,name,pool_type) VALUES($1,$2,$3)`, poolID, poolID.String(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO runners(id,pool_id,name,status,capacity,labels,certificate_serial,os,architecture,executor,isolation_level,available_disk_bytes,protocol_version,runner_version) VALUES($1,$2,$3,'online',$4,$5,$3,'linux','amd64','docker',$6,1000000000,$7,'test')`, id, poolID, id.String(), capacity, data, pool, protocol); err != nil {
		t.Fatal(err)
	}
	return id
}

func deploymentClaim(t *testing.T, s *Store, runner uuid.UUID, want uuid.UUID) *runmodel.Assignment {
	t.Helper()
	job, err := s.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner})
	if err != nil {
		t.Fatal(err)
	}
	if want == uuid.Nil {
		if job != nil {
			t.Fatalf("unexpected deployment assignment: %s", job.RunID)
		}
		return nil
	}
	if job == nil || job.RunID != want {
		t.Fatalf("claim: got %#v want %s", job, want)
	}
	return job
}

func deploymentStart(t *testing.T, s *Store, runner uuid.UUID, job *runmodel.Assignment) {
	t.Helper()
	request := runmodel.LeaseRequest{RunnerID: runner, JobID: job.JobID, LeaseToken: job.LeaseToken}
	if _, err := s.AcknowledgeRunnerJob(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRunnerJob(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func deploymentComplete(t *testing.T, s *Store, runner uuid.UUID, job *runmodel.Assignment, status runmodel.JobStatus, cleanup bool) {
	t.Helper()
	if err := s.CompleteRunnerJob(t.Context(), runmodel.RunnerCompletion{RunnerID: runner, JobID: job.JobID, LeaseToken: job.LeaseToken, Status: status, CleanupConfirmed: cleanup}); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentFIFOAndConcurrentClaims(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	first := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 2)
	second := deploymentRecord(t, f.project, "production", strings.Repeat("b", 40), 1)
	// Deliberately invert timestamps: ledger registration, not supplied time or SHA,
	// determines FIFO and never skips an older commit.
	second.CreatedAt = first.CreatedAt.Add(-time.Hour)
	for _, r := range []runmodel.Record{first, second} {
		if _, err := s.Create(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []uuid.UUID{deploymentRunner(t, s, f.project, "standard", 3, 10, true), deploymentRunner(t, s, f.project, "deployment", 2, 10, true), deploymentRunner(t, s, f.project, "deployment", 3, 10, false)} {
		deploymentClaim(t, s, r, uuid.Nil)
	}
	if job, err := s.ClaimJob(t.Context(), runmodel.ClaimRequest{}); err != nil || job != nil {
		t.Fatalf("legacy claimed deployment: %v %v", job, err)
	}
	runners := []uuid.UUID{deploymentRunner(t, s, f.project, "deployment", 3, 10, true), deploymentRunner(t, s, f.project, "deployment", 3, 10, true)}
	jobs := make(chan *runmodel.Assignment, 16)
	failures := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		runner := runners[i%2]
		wg.Go(func() {
			job, err := s.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner})
			jobs <- job
			failures <- err
		})
	}
	wg.Wait()
	close(jobs)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var assignment *runmodel.Assignment
	for job := range jobs {
		if job != nil {
			if assignment != nil {
				t.Fatal("overlapping deployment claims")
			}
			assignment = job
		}
	}
	if assignment == nil || assignment.RunID != first.ID {
		t.Fatal("FIFO first deployment was skipped")
	}
	var runner uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `SELECT runner_id FROM jobs WHERE id=$1`, assignment.JobID).Scan(&runner); err != nil {
		t.Fatal(err)
	}
	deploymentStart(t, s, runner, assignment)
	for _, r := range runners {
		deploymentClaim(t, s, r, uuid.Nil)
	}
	deploymentComplete(t, s, runner, assignment, runmodel.JobSucceeded, true)
	next := deploymentClaim(t, s, runner, first.ID)
	deploymentStart(t, s, runner, next)
	deploymentComplete(t, s, runner, next, runmodel.JobSucceeded, true)
	deploymentClaim(t, s, runner, second.ID)
}

func TestDeploymentCancellationCleanupAndIdempotentAcknowledgement(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	grantProject(t, f, authorization.Developer)
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 1, true)
	first := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 2)
	second := deploymentRecord(t, f.project, "production", strings.Repeat("b", 40), 1)
	for _, r := range []runmodel.Record{first, second} {
		if _, err := s.Create(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	job := deploymentClaim(t, s, runner, first.ID)
	deploymentStart(t, s, runner, job)
	if _, err := s.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.project, first.ID); err != nil {
		t.Fatal(err)
	}
	deploymentClaim(t, s, runner, uuid.Nil)
	detail, err := s.GetAuthorizedRun(t.Context(), f.memberSession.Token, f.project, first.ID)
	if err != nil || detail.Run.Status != runmodel.StatusCanceled {
		t.Fatalf("detail: %v %v", detail, err)
	}
	pending := 0
	for _, j := range detail.Jobs {
		if j.CleanupPending {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("cleanup pending count %d", pending)
	}
	// Wrong token and identity cannot release the gate, even with cleanup=true.
	for _, bad := range []runmodel.RunnerCompletion{{RunnerID: runner, JobID: job.JobID, LeaseToken: "wrong", Status: runmodel.JobSucceeded, CleanupConfirmed: true}, {RunnerID: uuid.New(), JobID: job.JobID, LeaseToken: job.LeaseToken, Status: runmodel.JobSucceeded, CleanupConfirmed: true}} {
		if err := s.CompleteRunnerJob(t.Context(), bad); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("bad cleanup auth: %v", err)
		}
	}
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, false)
	deploymentClaim(t, s, runner, uuid.Nil)
	// Cancellation already invalidated the live lease; the original execution
	// token remains valid for cleanup-only completion and response-loss retries.
	// Reopen a control-plane connection to prove the cleanup gate is durable.
	reopened, err := Open(t.Context(), s.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	deploymentClaim(t, reopened, runner, uuid.Nil)
	finishedAt := *detail.Run.FinishedAt
	s = reopened
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, true)
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, true)
	detail, err = s.GetAuthorizedRun(t.Context(), f.memberSession.Token, f.project, first.ID)
	if err != nil || detail.Run.Status != runmodel.StatusCanceled || !detail.Run.FinishedAt.Equal(finishedAt) {
		t.Fatalf("cleanup rewrote canceled result: %v %v", detail, err)
	}
	for _, j := range detail.Jobs {
		if j.CleanupPending {
			t.Fatal("cleanup still pending")
		}
	}
	deploymentClaim(t, s, runner, second.ID)
}

func TestDeploymentRunnerLossHoldsWithoutReplay(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	first := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 2)
	second := deploymentRecord(t, f.project, "production", strings.Repeat("b", 40), 1)
	for _, r := range []runmodel.Record{first, second} {
		if _, err := s.Create(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 1, true)
	job := deploymentClaim(t, s, runner, first.ID)
	if _, err := s.pool.Exec(t.Context(), `UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, job.JobID); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.RecoverExpiredRunnerLeases(t.Context(), 10)
	if err != nil || recovered.Requeued != 1 {
		t.Fatalf("unstarted recovery: %v %v", recovered, err)
	}
	job = deploymentClaim(t, s, runner, first.ID)
	deploymentStart(t, s, runner, job)
	if _, err := s.pool.Exec(t.Context(), `UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, job.JobID); err != nil {
		t.Fatal(err)
	}
	recovered, err = s.RecoverExpiredRunnerLeases(t.Context(), 10)
	if err != nil || recovered.Failed != 1 || recovered.Requeued != 0 {
		t.Fatalf("started recovery: %v %v", recovered, err)
	}
	staging := deploymentRecord(t, f.project, "staging", strings.Repeat("c", 40), 1)
	if _, err := s.Create(t.Context(), staging); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		deploymentClaim(t, s, runner, uuid.Nil)
		if _, err := s.RecoverExpiredRunnerLeases(t.Context(), 10); err != nil {
			t.Fatal(err)
		}
	}
	otherRunner := deploymentRunner(t, s, f.project, "deployment", 3, 1, true)
	deploymentClaim(t, s, otherRunner, staging.ID)
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, true)
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, true)
	var status string
	var attempt int
	if err := s.pool.QueryRow(t.Context(), `SELECT status,attempt FROM jobs WHERE id=$1`, job.JobID).Scan(&status, &attempt); err != nil || status != "failed" || attempt != 1 {
		t.Fatalf("runner loss replay/result: %s %d %v", status, attempt, err)
	}
	deploymentClaim(t, s, runner, second.ID)
}

func TestDeploymentUnconfirmedCleanupFailsAndBlocks(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	record := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 2)
	// Make the second job depend on the first to verify cleanup=false cannot
	// unblock a successful-looking completion's downstream deployment command.
	var plan pipeline.Plan
	_ = json.Unmarshal(record.Plan, &plan)
	plan.Stages[0].Jobs[1].DependsOn = []string{plan.Stages[0].Jobs[0].Name}
	record.Plan, _ = json.Marshal(plan)
	if _, err := s.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	next := deploymentRecord(t, f.project, "production", strings.Repeat("b", 40), 1)
	if _, err := s.Create(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 2, true)
	job := deploymentClaim(t, s, runner, record.ID)
	deploymentStart(t, s, runner, job)
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, false)
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, false)
	deploymentClaim(t, s, runner, uuid.Nil)
	var status, reason string
	if err := s.pool.QueryRow(t.Context(), `SELECT status,failure_reason FROM jobs WHERE id=$1`, job.JobID).Scan(&status, &reason); err != nil || status != "failed" || reason != "cleanup_unconfirmed" {
		t.Fatalf("false cleanup state: %s %s %v", status, reason, err)
	}
	deploymentComplete(t, s, runner, job, runmodel.JobSucceeded, true)
	deploymentClaim(t, s, runner, next.ID)
}

func TestDeploymentQueuedAndUnstartedCancellationReleases(t *testing.T) {
	for _, assigned := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "assigned"}[assigned], func(t *testing.T) {
			f := newAccessFixture(t)
			s := f.store
			grantProject(t, f, authorization.Developer)
			first := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 1)
			second := deploymentRecord(t, f.project, "production", strings.Repeat("b", 40), 1)
			for _, r := range []runmodel.Record{first, second} {
				if _, err := s.Create(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			}
			runner := deploymentRunner(t, s, f.project, "deployment", 3, 2, true)
			if assigned {
				deploymentClaim(t, s, runner, first.ID)
			}
			if _, err := s.CancelAuthorizedRun(t.Context(), f.memberSession.Token, f.project, first.ID); err != nil {
				t.Fatal(err)
			}
			deploymentClaim(t, s, runner, second.ID)
		})
	}
}

func TestDeploymentIndependentEnvironmentsRepositoriesAndCI(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	a := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 1)
	b := deploymentRecord(t, f.project, "staging", strings.Repeat("b", 40), 1)
	c := deploymentRecord(t, f.otherProject, "production", strings.Repeat("c", 40), 1)
	for _, r := range []runmodel.Record{a, b, c} {
		if _, err := s.Create(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	runner := deploymentRunner(t, s, f.project, "deployment", 3, 3, true)
	claimed := map[uuid.UUID]bool{}
	for i := 0; i < 2; i++ {
		job, err := s.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner})
		if err != nil || job == nil || (job.RunID != a.ID && job.RunID != b.ID) || claimed[job.RunID] {
			t.Fatalf("independent environments: %#v %v", job, err)
		}
		claimed[job.RunID] = true
	}
	other := deploymentRunner(t, s, f.otherProject, "deployment", 3, 3, true)
	deploymentClaim(t, s, other, c.ID)
	ordinary := storetest.Record(t, 1, false)
	if _, err := s.Create(t.Context(), ordinary); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob(t.Context(), runmodel.ClaimRequest{})
	if err != nil || job == nil || job.RunID != ordinary.ID {
		t.Fatalf("ordinary CI blocked by deployment: %v %v", job, err)
	}
}

func TestDeploymentIdentityValidationRerunAndHTTPConflict(t *testing.T) {
	f := newAccessFixture(t)
	s := f.store
	grantProject(t, f, authorization.Developer)
	record := deploymentRecord(t, f.project, "production", strings.Repeat("a", 40), 1)
	for _, sha := range []string{"", "main", strings.Repeat("0", 40), strings.Repeat("A", 40), strings.Repeat("a", 64)} {
		bad := record
		bad.CommitSHA = sha
		if _, err := s.Create(t.Context(), bad); !errors.Is(err, runmodel.ErrInvalidDeployment) {
			t.Fatalf("invalid SHA %q: %v", sha, err)
		}
	}
	bad := record
	bad.ProjectID = nil
	if _, err := s.Create(t.Context(), bad); !errors.Is(err, runmodel.ErrInvalidDeployment) {
		t.Fatalf("missing repository: %v", err)
	}
	var plan pipeline.Plan
	_ = json.Unmarshal(record.Plan, &plan)
	plan.Stages[0].Jobs[0].Deployment = "other"
	bad = record
	bad.Plan, _ = json.Marshal(plan)
	if _, err := s.Create(t.Context(), bad); !errors.Is(err, runmodel.ErrInvalidDeployment) {
		t.Fatalf("inconsistent plan: %v", err)
	}
	if _, err := s.CreateAuthorizedRun(t.Context(), f.memberSession.Token, f.project, record); err != nil {
		t.Fatal(err)
	}
	duplicate := record
	duplicate.ID = uuid.New()
	if _, err := s.Create(t.Context(), duplicate); !errors.Is(err, runmodel.ErrRunConflict) {
		t.Fatalf("manual duplicate: %v", err)
	}
	for _, status := range []string{"queued", "running", "failed", "canceled", "succeeded"} {
		if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET status=$2 WHERE id=$1`, record.ID, status); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"full", "failed"} {
			if _, err := s.RerunAuthorizedRun(t.Context(), f.memberSession.Token, f.project, record.ID, mode, uuid.New()); !errors.Is(err, runmodel.ErrRunConflict) {
				t.Fatalf("deployment rerun %s/%s: %v", status, mode, err)
			}
		}
	}
	handler, err := httpapi.NewAuthenticated(slog.New(slog.NewTextHandler(io.Discard, nil)), s, s, 1<<20, "https://ci.example.test")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"project_id": f.project.String(), "pipeline": deploymentYAML, "commit_sha": record.CommitSHA})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://ci.example.test")
	request.Header.Set("X-CSRF-Token", identity.CSRFToken(f.memberSession.Token))
	request.AddCookie(&http.Cookie{Name: identity.CookieName, Value: f.memberSession.Token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate HTTP status %d: %s", response.Code, response.Body.String())
	}
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM runs WHERE id=$1`, record.ID); err == nil {
		t.Fatal("history deletion erased registered identity")
	}
}

func TestDeploymentConcurrentWebhookDeliveriesReuseTerminalRun(t *testing.T) {
	s, service, session, _ := importFixture(t)
	authorizeImport(t, service, session.Token)
	imported, err := service.Import(t.Context(), session.Token, "34", []string{"70"})
	if err != nil {
		t.Fatal(err)
	}
	repository := imported[0].ID
	plan, err := pipeline.Compile([]byte(deploymentYAML), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]githubci.RunCommit, 8)
	for i := range requests {
		item := claimedGitHubDelivery(t, s, "70")
		requests[i] = githubci.RunCommit{Delivery: item, RepositoryID: repository, PipelinePath: ".yuanci.yml", PipelineSource: []byte(deploymentYAML), Plan: plan, CreatedAt: time.Now()}
	}
	var wg sync.WaitGroup
	results := make(chan githubci.RunResult, len(requests))
	failures := make(chan error, len(requests))
	for _, request := range requests {
		wg.Go(func() { result, err := s.CommitWebhookRun(t.Context(), request); results <- result; failures <- err })
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id uuid.UUID
	created := 0
	for result := range results {
		if id == uuid.Nil {
			id = result.ID
		}
		if result.ID != id {
			t.Fatal("same commit produced multiple Runs")
		}
		if result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created count %d", created)
	}
	var runs, jobs, linked int
	if err := s.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM jobs),(SELECT count(*) FROM webhook_deliveries WHERE run_id=$1)`, id).Scan(&runs, &jobs, &linked); err != nil || runs != 1 || jobs != 1 || linked != 8 {
		t.Fatalf("duplicate materialization %d/%d/%d %v", runs, jobs, linked, err)
	}
	for _, status := range []string{"failed", "canceled"} {
		if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET status=$2 WHERE id=$1`, id, status); err != nil {
			t.Fatal(err)
		}
		item := claimedGitHubDelivery(t, s, "70")
		request := requests[0]
		request.Delivery = item
		result, err := s.CommitWebhookRun(t.Context(), request)
		if err != nil || result.Created || result.ID != id {
			t.Fatalf("terminal commit replay %s: %v %v", status, result, err)
		}
	}
}
