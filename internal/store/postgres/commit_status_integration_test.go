package postgres

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/commitstatus"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

func TestCommitStatusOutboxMigrationClaimAndRecovery(t *testing.T) {
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	repositoryID, runID := insertCommitStatusParents(t, store)
	itemID := uuid.New()
	_, err = store.pool.Exec(t.Context(), `INSERT INTO commit_status_outbox
		(id,repository_id,run_id,provider,commit_sha,context,commit_state,description,
		deterministic_key,expires_at) VALUES($1,$2,$3,'github',$4,'YuanCI','pending','Run queued',$5,$6)`,
		itemID, repositoryID, runID, "0123456789abcdef0123456789abcdef01234567", "run:pending", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	const claimers = 8
	results := make(chan *commitstatus.Item, claimers)
	errors := make(chan error, claimers)
	var wait sync.WaitGroup
	for range claimers {
		wait.Go(func() {
			item, claimErr := store.ClaimCommitStatus(t.Context(), time.Minute)
			results <- item
			errors <- claimErr
		})
	}
	wait.Wait()
	close(results)
	close(errors)
	for claimErr := range errors {
		if claimErr != nil {
			t.Fatal(claimErr)
		}
	}
	claimed := 0
	for item := range results {
		if item != nil {
			claimed++
			if item.ID != itemID || item.LeaseOwner == uuid.Nil || item.DeliveryState != commitstatus.DeliveryProcessing || item.AttemptCount != 1 {
				t.Fatalf("invalid claimed item: %#v", item)
			}
		}
	}
	if claimed != 1 {
		t.Fatalf("item was claimed %d times", claimed)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE commit_status_outbox
		SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, itemID); err != nil {
		t.Fatal(err)
	}
	count, err := store.RecoverCommitStatusLeases(t.Context(), 10)
	if err != nil || count != 1 {
		t.Fatalf("recover count=%d err=%v", count, err)
	}
	reclaimed, err := store.ClaimCommitStatus(t.Context(), time.Minute)
	if err != nil || reclaimed == nil || reclaimed.ID != itemID || reclaimed.AttemptCount != 2 {
		t.Fatalf("reclaim item=%#v err=%v", reclaimed, err)
	}
	if err := store.RescheduleCommitStatus(t.Context(), reclaimed.ID, reclaimed.LeaseOwner, time.Now(), "attempts_exhausted", true); err != nil {
		t.Fatal(err)
	}
	actorID := uuid.New()
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO users(id,display_name,is_instance_admin) VALUES($1,'Status Admin',true)`, actorID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplayCommitStatus(t.Context(), itemID, actorID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplayCommitStatus(t.Context(), itemID, actorID); err == nil {
		t.Fatal("non-dead status was replayed twice")
	}
	replayed, err := store.ClaimCommitStatus(t.Context(), time.Minute)
	if err != nil || replayed == nil || replayed.AttemptCount != 1 {
		t.Fatalf("replayed item=%#v err=%v", replayed, err)
	}
	if err := store.FinishCommitStatus(t.Context(), replayed.ID, replayed.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	var audits int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events
		WHERE actor_user_id=$1 AND action='commit_status.replayed' AND resource_id=$2`, actorID, itemID.String()).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("replay audits=%d err=%v", audits, err)
	}

	expiredID := uuid.New()
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO commit_status_outbox
		(id,repository_id,run_id,provider,commit_sha,context,commit_state,description,deterministic_key,
		created_at,available_at,expires_at) VALUES($1,$2,$3,'github',$4,'YuanCI','pending','Expired',$5,
		clock_timestamp()-interval '2 hours',clock_timestamp()-interval '2 hours',clock_timestamp()-interval '1 hour')`,
		expiredID, repositoryID, runID, "0123456789abcdef0123456789abcdef01234567", "run:expired"); err != nil {
		t.Fatal(err)
	}
	if count, err := store.RecoverCommitStatusLeases(t.Context(), 10); err != nil || count != 1 {
		t.Fatalf("expiry recovery count=%d err=%v", count, err)
	}
	var deliveryState commitstatus.DeliveryState
	if err := store.pool.QueryRow(t.Context(), `SELECT delivery_state FROM commit_status_outbox WHERE id=$1`, expiredID).Scan(&deliveryState); err != nil || deliveryState != commitstatus.DeliveryDead {
		t.Fatalf("expired delivery state=%q err=%v", deliveryState, err)
	}
}

func TestCommitStatusOutboxMigrationConstraints(t *testing.T) {
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	repositoryID, runID := insertCommitStatusParents(t, store)
	_, err = store.pool.Exec(t.Context(), `INSERT INTO commit_status_outbox
		(repository_id,run_id,provider,commit_sha,context,commit_state,description,
		deterministic_key,expires_at,delivery_state,lease_owner)
		VALUES($1,$2,'github',$3,'YuanCI','pending','Run queued','invalid-lease',$4,'queued',$5)`,
		repositoryID, runID, "0123456789abcdef0123456789abcdef01234567", time.Now().Add(time.Hour), uuid.New())
	if err == nil {
		t.Fatal("unpaired queued lease passed migration constraints")
	}
}

func TestRunStatusOutboxIsAtomicAndReplaySafe(t *testing.T) {
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	repositoryID, obsoleteRun := insertCommitStatusParents(t, store)
	if _, err := store.pool.Exec(t.Context(), `DELETE FROM runs WHERE id=$1`, obsoleteRun); err != nil {
		t.Fatal(err)
	}
	plan, err := pipeline.Compile([]byte(githubCIPipeline), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	record := runmodel.Record{ID: runID, ProjectID: &repositoryID, PipelineName: plan.Name, Event: "push",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", Status: runmodel.StatusQueued,
		ConfigSHA256: plan.ConfigSHA256, Plan: encoded, CreatedAt: time.Now().UTC()}

	installStatusRejectTrigger(t, store, "true")
	if _, err := store.Create(t.Context(), record); err == nil {
		t.Fatal("Run creation survived pending-status enqueue failure")
	}
	var runCount int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE id=$1`, runID).Scan(&runCount); err != nil || runCount != 0 {
		t.Fatalf("rolled-back Run count=%d err=%v", runCount, err)
	}
	dropStatusRejectTrigger(t, store)
	if _, err := store.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	assertRunStatusRows(t, store, runID, 1, commitstatus.StatePending)

	assignment, err := store.ClaimJob(t.Context(), runmodel.ClaimRequest{})
	if err != nil || assignment == nil || assignment.RunID != runID {
		t.Fatalf("claim=%#v err=%v", assignment, err)
	}
	if err := store.StartJob(t.Context(), assignment.JobID, assignment.LeaseToken); err != nil {
		t.Fatal(err)
	}
	installStatusRejectTrigger(t, store, "NEW.deterministic_key LIKE '%:final'")
	if err := store.CompleteJob(t.Context(), assignment.JobID, assignment.LeaseToken, runmodel.JobSucceeded); err == nil {
		t.Fatal("terminal transition survived final-status enqueue failure")
	}
	var runStatus runmodel.Status
	if err := store.pool.QueryRow(t.Context(), `SELECT status FROM runs WHERE id=$1`, runID).Scan(&runStatus); err != nil || runStatus != runmodel.StatusRunning {
		t.Fatalf("rolled-back terminal status=%q err=%v", runStatus, err)
	}
	dropStatusRejectTrigger(t, store)
	if err := store.CompleteJob(t.Context(), assignment.JobID, assignment.LeaseToken, runmodel.JobSucceeded); err != nil {
		t.Fatal(err)
	}
	assertRunStatusRows(t, store, runID, 2, commitstatus.StateSuccess)

	tx, err := store.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := enqueueCommitStatusForRun(t.Context(), tx, runID, runmodel.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := enqueueCommitStatusForRun(t.Context(), tx, runID, runmodel.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertRunStatusRows(t, store, runID, 2, commitstatus.StateSuccess)
}

func installStatusRejectTrigger(t *testing.T, store *Store, condition string) {
	t.Helper()
	statement := `CREATE OR REPLACE FUNCTION reject_test_status() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN IF ` + condition + ` THEN RAISE EXCEPTION 'status rejected'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER reject_test_status BEFORE INSERT ON commit_status_outbox
	FOR EACH ROW EXECUTE FUNCTION reject_test_status()`
	if _, err := store.pool.Exec(t.Context(), statement); err != nil {
		t.Fatal(err)
	}
}

func dropStatusRejectTrigger(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.pool.Exec(t.Context(), `DROP TRIGGER reject_test_status ON commit_status_outbox`); err != nil {
		t.Fatal(err)
	}
}

func assertRunStatusRows(t *testing.T, store *Store, runID uuid.UUID, want int, final commitstatus.State) {
	t.Helper()
	var count int
	var finalCount int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE commit_state=$2)
		FROM commit_status_outbox WHERE run_id=$1`, runID, final).Scan(&count, &finalCount); err != nil {
		t.Fatal(err)
	}
	if count != want || finalCount != 1 {
		t.Fatalf("status rows=%d final %q rows=%d", count, final, finalCount)
	}
}

func insertCommitStatusParents(t *testing.T, store *Store) (uuid.UUID, uuid.UUID) {
	t.Helper()
	organizationID, repositoryID, runID := uuid.New(), uuid.New(), uuid.New()
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO organizations(id,slug,display_name) VALUES($1,$2,'Status')`,
		organizationID, "status-"+organizationID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO repositories
		(id,organization_id,provider,provider_instance,external_id,owner,name,clone_url,default_branch)
		VALUES($1,$2,'github','https://github.com',$3,'owner','repo','https://github.com/owner/repo.git','main')`,
		repositoryID, organizationID, repositoryID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO runs
		(id,repository_id,pipeline_name,event,commit_sha,status,config_sha256,compiled_plan,created_at)
		VALUES($1,$2,'verify','push',$3,'queued',$4,'{}'::jsonb,clock_timestamp())`, runID, repositoryID,
		"0123456789abcdef0123456789abcdef01234567", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	return repositoryID, runID
}
