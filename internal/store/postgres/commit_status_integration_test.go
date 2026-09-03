package postgres

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/commitstatus"
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
