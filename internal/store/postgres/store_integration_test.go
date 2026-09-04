package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/db/migrations"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/provisioning"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
	"github.com/yuanci/yuanci/internal/secrets"
)

// newTestDatabase never connects to or cleans the application's database.
// Only a database created by this invocation is eligible for cleanup.
func newTestDatabase(t *testing.T) string {
	t.Helper()
	rawURL := os.Getenv("YUANCI_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set YUANCI_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("integration configuration must be a PostgreSQL URL")
	}
	config, err := pgx.ParseConfig(rawURL)
	if err != nil {
		t.Fatal("invalid integration database configuration")
	}
	if config.Database != "yuanci_ci" {
		t.Fatal("integration URL must point to a dedicated yuanci_ci database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot connect to dedicated integration PostgreSQL")
	}
	name := "yuanci_test_" + uuid.New().String()
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create isolated database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, "DROP DATABASE "+identifier); err != nil {
			t.Errorf("drop owned test database: %v", err)
		}
	})
	parsed.Path = "/" + name
	parsed.RawPath = ""
	query := parsed.Query()
	query.Del("database")
	query.Del("dbname")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestPostgresStoreContract(t *testing.T) {
	storetest.Exercise(t, func(t *testing.T) runmodel.Store {
		url := newTestDatabase(t)
		store, err := Open(t.Context(), url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(store.Close)
		return store
	})
}

func TestPostgresRunnerStoreContract(t *testing.T) {
	storetest.ExerciseRunner(t, func(t *testing.T, runner runmodel.RunnerDescriptor) runmodel.RunnerJobStore {
		store, err := Open(t.Context(), newTestDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(store.Close)
		poolID := uuid.New()
		if _, err := store.pool.Exec(t.Context(), `INSERT INTO runner_pools(id,name,pool_type)
            VALUES ($1,$2,$3)`, poolID, "contract-"+runner.ID.String(), runner.PoolType); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(t.Context(), `INSERT INTO runners
            (id,pool_id,name,status,capacity,labels,certificate_serial,os,architecture,executor,
             isolation_level,available_disk_bytes,protocol_version,runner_version)
            VALUES ($1,$2,$3,'offline',$4,'{}'::jsonb,'contract-serial',$5,$6,$7,$8,$9,1,'contract')`,
			runner.ID, poolID, "contract-"+runner.ID.String(), runner.Capacity, runner.OS,
			runner.Architecture, runner.Executor, runner.PoolType, runner.AvailableDiskBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RenewRunnerLeases(t.Context(), runmodel.HeartbeatRequest{Runner: runner}); err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func TestConcurrentMigrationsAndReopen(t *testing.T) {
	url := newTestDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			store, err := Open(ctx, url)
			if err == nil {
				store.Close()
			}
			errors <- err
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	store, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	record := storetest.Record(t, 1, false)
	if _, err := store.Create(ctx, record); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	reopened, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err := reopened.List(ctx, 20)
	if err != nil || len(items) != 1 || items[0].ID != record.ID {
		t.Fatalf("reopen lost run: %v", err)
	}
}

func TestExpiredLeaseCannotChangeJob(t *testing.T) {
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(t.Context(), storetest.Record(t, 1, false)); err != nil {
		t.Fatal(err)
	}
	a, err := store.ClaimJob(t.Context(), runmodel.ClaimRequest{RunnerName: "contract"})
	if err != nil || a == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE jobs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, a.JobID); err != nil {
		t.Fatal(err)
	}
	if err := store.StartJob(t.Context(), a.JobID, a.LeaseToken); !errors.Is(err, runmodel.ErrLeaseInvalid) {
		t.Fatalf("expired start accepted: %v", err)
	}
	if err := store.CompleteJob(t.Context(), a.JobID, a.LeaseToken, runmodel.JobSucceeded); !errors.Is(err, runmodel.ErrLeaseInvalid) {
		t.Fatalf("expired completion accepted: %v", err)
	}
}

func TestExpiredRunnerLeaseCannotBeRenewedOrChanged(t *testing.T) {
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := runmodel.RunnerDescriptor{ID: uuid.New(), PoolType: "standard", OS: "linux", Architecture: "amd64",
		Executor: "docker", Labels: map[string]string{}, Capacity: 1, AvailableDiskBytes: 1 << 30, ProtocolVersion: 1}
	poolID := uuid.New()
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO runner_pools(id,name,pool_type) VALUES ($1,$2,'standard')`,
		poolID, "deadline-pool"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO runners
        (id,pool_id,name,status,capacity,labels,certificate_serial,os,architecture,executor,
         isolation_level,available_disk_bytes,protocol_version,runner_version)
        VALUES ($1,$2,'deadline-runner','offline',1,'{}'::jsonb,'deadline-serial','linux','amd64','docker',
          'standard',$3,1,'contract')`, runner.ID, poolID, runner.AvailableDiskBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenewRunnerLeases(t.Context(), runmodel.HeartbeatRequest{Runner: runner}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(t.Context(), storetest.RunnerRecord(t, 1, pipeline.RunnerRequirements{}, "")); err != nil {
		t.Fatal(err)
	}
	assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner.ID})
	if err != nil || assignment == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE jobs SET lease_expires_at=clock_timestamp() WHERE id=$1`, assignment.JobID); err != nil {
		t.Fatal(err)
	}
	lease := runmodel.LeaseRequest{RunnerID: runner.ID, JobID: assignment.JobID, LeaseToken: assignment.LeaseToken}
	if _, err := store.AcknowledgeRunnerJob(t.Context(), lease); !errors.Is(err, runmodel.ErrLeaseInvalid) {
		t.Fatalf("deadline receipt accepted: %v", err)
	}
	result, err := store.RenewRunnerLeases(t.Context(), runmodel.HeartbeatRequest{Runner: runner,
		ActiveJobs: []runmodel.ActiveLease{{JobID: assignment.JobID, LeaseToken: assignment.LeaseToken, State: "received"}}})
	if err != nil || len(result.Jobs) != 1 || result.Jobs[0].Renewed || result.Jobs[0].CancelReason != "lease_invalid" {
		t.Fatalf("deadline renewal: %#v %v", result, err)
	}
	if err := store.CompleteRunnerJob(t.Context(), runmodel.RunnerCompletion{RunnerID: runner.ID, JobID: assignment.JobID,
		LeaseToken: assignment.LeaseToken, Status: runmodel.JobSucceeded}); !errors.Is(err, runmodel.ErrLeaseInvalid) {
		t.Fatalf("deadline completion accepted: %v", err)
	}
}

func TestRunnerLeaseRecoveryOutcomesAndConvergence(t *testing.T) {
	t.Run("assigned is requeued once", func(t *testing.T) {
		store, runner := newRecoveryStore(t)
		if _, err := store.Create(t.Context(), storetest.Record(t, 1, false)); err != nil {
			t.Fatal(err)
		}
		assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner.ID})
		if err != nil || assignment == nil {
			t.Fatalf("claim: %v", err)
		}
		if _, err := store.pool.Exec(t.Context(), `UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, assignment.JobID); err != nil {
			t.Fatal(err)
		}
		results := make(chan runmodel.RecoveryResult, 2)
		errorsOut := make(chan error, 2)
		for range 2 {
			go func() {
				result, err := store.RecoverExpiredRunnerLeases(t.Context(), 100)
				results <- result
				errorsOut <- err
			}()
		}
		total := 0
		for range 2 {
			if err := <-errorsOut; err != nil {
				t.Fatal(err)
			}
			total += (<-results).Requeued
		}
		if total != 1 {
			t.Fatalf("two reconcilers requeued %d jobs", total)
		}
		var jobStatus, runStatus string
		var runnerID *uuid.UUID
		var leaseHash []byte
		var startedAt *time.Time
		if err := store.pool.QueryRow(t.Context(), `SELECT status,runner_id,lease_token_hash FROM jobs WHERE id=$1`, assignment.JobID).
			Scan(&jobStatus, &runnerID, &leaseHash); err != nil {
			t.Fatal(err)
		}
		if err := store.pool.QueryRow(t.Context(), `SELECT status,started_at FROM runs WHERE id=$1`, assignment.RunID).
			Scan(&runStatus, &startedAt); err != nil {
			t.Fatal(err)
		}
		if jobStatus != "queued" || runnerID != nil || leaseHash != nil || runStatus != "queued" || startedAt != nil {
			t.Fatalf("requeue state job=%s runner=%v lease=%x run=%s started=%v", jobStatus, runnerID, leaseHash, runStatus, startedAt)
		}
		if _, err := store.StartRunnerJob(t.Context(), runmodel.LeaseRequest{RunnerID: runner.ID, JobID: assignment.JobID,
			LeaseToken: assignment.LeaseToken}); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("late start accepted: %v", err)
		}
	})

	t.Run("running fails run and downstream", func(t *testing.T) {
		store, runner := newRecoveryStore(t)
		if _, err := store.Create(t.Context(), storetest.Record(t, 1, true)); err != nil {
			t.Fatal(err)
		}
		assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner.ID})
		if err != nil || assignment == nil {
			t.Fatalf("claim: %v", err)
		}
		lease := runmodel.LeaseRequest{RunnerID: runner.ID, JobID: assignment.JobID, LeaseToken: assignment.LeaseToken}
		if _, err := store.AcknowledgeRunnerJob(t.Context(), lease); err != nil {
			t.Fatal(err)
		}
		if _, err := store.StartRunnerJob(t.Context(), lease); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(t.Context(), `UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, assignment.JobID); err != nil {
			t.Fatal(err)
		}
		result, err := store.RecoverExpiredRunnerLeases(t.Context(), 100)
		if err != nil || result.Failed != 1 || result.Requeued != 0 {
			t.Fatalf("recover: %#v %v", result, err)
		}
		var failed, skipped, audits int
		if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE status='failed' AND failure_reason='runner_lost'),
            count(*) FILTER (WHERE status='skipped') FROM jobs WHERE run_id=$1`, assignment.RunID).Scan(&failed, &skipped); err != nil {
			t.Fatal(err)
		}
		if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events
            WHERE action='runner_lease.recovered' AND resource_id=$1`, assignment.JobID.String()).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		var runStatus string
		var finishedAt *time.Time
		if err := store.pool.QueryRow(t.Context(), `SELECT status,finished_at FROM runs WHERE id=$1`, assignment.RunID).
			Scan(&runStatus, &finishedAt); err != nil {
			t.Fatal(err)
		}
		if failed != 1 || skipped != 1 || audits != 1 || runStatus != "failed" || finishedAt == nil {
			t.Fatalf("loss state failed=%d skipped=%d audits=%d run=%s finished=%v", failed, skipped, audits, runStatus, finishedAt)
		}
		if err := store.CompleteRunnerJob(t.Context(), runmodel.RunnerCompletion{RunnerID: runner.ID, JobID: assignment.JobID,
			LeaseToken: assignment.LeaseToken, Status: runmodel.JobSucceeded}); !errors.Is(err, runmodel.ErrLeaseInvalid) {
			t.Fatalf("late completion accepted: %v", err)
		}
	})
}

func TestRunnerLeaseRecoveryAuditFailureRollsBackGraph(t *testing.T) {
	store, runner := newRecoveryStore(t)
	if _, err := store.Create(t.Context(), storetest.Record(t, 1, true)); err != nil {
		t.Fatal(err)
	}
	assignment, err := store.ClaimRunnerJob(t.Context(), runmodel.RunnerClaim{RunnerID: runner.ID})
	if err != nil || assignment == nil {
		t.Fatalf("claim: %v", err)
	}
	lease := runmodel.LeaseRequest{RunnerID: runner.ID, JobID: assignment.JobID, LeaseToken: assignment.LeaseToken}
	if _, err := store.AcknowledgeRunnerJob(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRunnerJob(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, assignment.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `CREATE FUNCTION reject_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN IF NEW.action='runner_lease.recovered' THEN RAISE EXCEPTION 'injected recovery audit failure'; END IF; RETURN NEW; END $$;
        CREATE TRIGGER reject_recovery_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_recovery_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecoverExpiredRunnerLeases(t.Context(), 100); err == nil {
		t.Fatal("audit failure did not fail recovery")
	}
	var running, blocked int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE status='running' AND lease_token_hash IS NOT NULL),
        count(*) FILTER (WHERE status='blocked') FROM jobs WHERE run_id=$1`, assignment.RunID).Scan(&running, &blocked); err != nil {
		t.Fatal(err)
	}
	var runStatus string
	if err := store.pool.QueryRow(t.Context(), `SELECT status FROM runs WHERE id=$1`, assignment.RunID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if running != 1 || blocked != 1 || runStatus != "running" {
		t.Fatalf("partial recovery persisted: running=%d blocked=%d run=%s", running, blocked, runStatus)
	}
}

func newRecoveryStore(t *testing.T) (*Store, runmodel.RunnerDescriptor) {
	t.Helper()
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	runner := runmodel.RunnerDescriptor{ID: uuid.New(), PoolType: "standard", OS: "linux", Architecture: "amd64",
		Executor: "docker", Labels: map[string]string{}, Capacity: 4, AvailableDiskBytes: 4 << 30, ProtocolVersion: 1}
	poolID := uuid.New()
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO runner_pools(id,name,pool_type) VALUES ($1,$2,'standard')`,
		poolID, "recovery-"+runner.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO runners
        (id,pool_id,name,status,capacity,labels,certificate_serial,os,architecture,executor,
         isolation_level,available_disk_bytes,protocol_version,runner_version)
        VALUES ($1,$2,$3,'online',$4,'{}'::jsonb,'recovery-serial','linux','amd64','docker',
          'standard',$5,1,'contract')`, runner.ID, poolID, "recovery-"+runner.ID.String(), runner.Capacity,
		runner.AvailableDiskBytes); err != nil {
		t.Fatal(err)
	}
	return store, runner
}

func TestRunnerIdentityMigrationRecoversLegacyJobs(t *testing.T) {
	databaseURL := newTestDatabase(t)
	connection, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(t.Context())

	for _, name := range []string{
		"000001_initial.up.sql",
		"000002_identity_access.up.sql",
		"000003_oauth_bootstrap.up.sql",
		"000004_managed_auth.up.sql",
		"000005_github_import.up.sql",
		"000006_github_proof_subject.up.sql",
	} {
		body, readErr := migrations.Files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := connection.Exec(t.Context(), string(body)); err != nil {
			t.Fatalf("apply fixture migration %s: %v", name, err)
		}
		if _, err := connection.Exec(t.Context(), `INSERT INTO schema_migrations(version) VALUES ($1)`, name); err != nil {
			t.Fatalf("record fixture migration %s: %v", name, err)
		}
	}

	poolID, runnerID := uuid.New(), uuid.New()
	if _, err := connection.Exec(t.Context(), `INSERT INTO runner_pools(id,name,pool_type) VALUES ($1,'legacy','standard')`, poolID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(t.Context(), `INSERT INTO runners
		(id,pool_id,name,status,capacity,certificate_serial,last_seen_at)
		VALUES ($1,$2,'legacy-runner','online',1,'legacy-serial',clock_timestamp())`, runnerID, poolID); err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	if _, err := connection.Exec(t.Context(), `INSERT INTO users(id,display_name) VALUES ($1,'preserved user')`, userID); err != nil {
		t.Fatal(err)
	}
	legacyLoginID := uuid.New()
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	legacySecret, err := cipher.Seal([]byte("legacy-github-secret"), []byte("yuanci:login:github:"+legacyLoginID.String()))
	if err != nil {
		t.Fatal(err)
	}
	encodedSecret, err := json.Marshal(legacySecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(t.Context(), `INSERT INTO login_configs
		(id,client_id,encrypted_secret,bootstrap_subject,setup_hash)
		VALUES ($1,'legacy-github-client',$2,'100',decode(repeat('aa',32),'hex'))`, legacyLoginID, encodedSecret); err != nil {
		t.Fatal(err)
	}

	assignedRun, runningRun, queuedRun, terminalRun := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	assignedJob, runningJob, downstreamJob, queuedJob, terminalJob := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, fixture := range []struct {
		id     uuid.UUID
		status string
	}{
		{assignedRun, "running"},
		{runningRun, "running"},
		{queuedRun, "queued"},
		{terminalRun, "succeeded"},
	} {
		_, err := connection.Exec(t.Context(), `INSERT INTO runs
			(id,pipeline_name,event,status,config_sha256,compiled_plan,started_at,finished_at)
			VALUES ($1,'migration fixture','manual',$2,repeat('a',64),'{}'::jsonb,
				CASE WHEN $2='running' THEN clock_timestamp() ELSE NULL END,
				CASE WHEN $2='succeeded' THEN clock_timestamp() ELSE NULL END)`, fixture.id, fixture.status)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertJob := func(id, runID uuid.UUID, key, status string, runner *uuid.UUID, withLease bool) {
		t.Helper()
		var leaseHash []byte
		var leaseExpiry *time.Time
		if withLease {
			leaseHash = make([]byte, 32)
			expires := time.Now().Add(time.Hour)
			leaseExpiry = &expires
		}
		_, err := connection.Exec(t.Context(), `INSERT INTO jobs
			(id,run_id,runner_id,stage_name,job_name,job_key,spec,status,lease_token_hash,lease_expires_at,started_at,finished_at)
			VALUES ($1,$2,$3,'test',$4,$4,'{}'::jsonb,$5,$6,$7,
				CASE WHEN $5='running' THEN clock_timestamp() ELSE NULL END,
				CASE WHEN $5='succeeded' THEN clock_timestamp() ELSE NULL END)`,
			id, runID, runner, key, status, leaseHash, leaseExpiry)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertJob(assignedJob, assignedRun, "assigned", "assigned", &runnerID, true)
	insertJob(runningJob, runningRun, "running", "running", &runnerID, true)
	insertJob(downstreamJob, runningRun, "downstream", "blocked", nil, false)
	insertJob(queuedJob, queuedRun, "queued", "queued", nil, false)
	insertJob(terminalJob, terminalRun, "terminal", "succeeded", &runnerID, true)

	store, err := Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	legacyConfig, err := configRow(reopened.pool.QueryRow(t.Context(), `SELECT `+configColumns+` FROM login_configs WHERE id=$1`, legacyLoginID))
	if err != nil {
		reopened.Close()
		t.Fatal(err)
	}
	plain, err := cipher.Open(legacyConfig.Encrypted, []byte("yuanci:login:github:"+legacyLoginID.String()))
	reopened.Close()
	if err != nil || string(plain) != "legacy-github-secret" || legacyConfig.Provider != "github" || legacyConfig.Instance != identity.GitHubInstance {
		t.Fatalf("legacy GitHub login config changed: provider=%q instance=%q secret=%q err=%v", legacyConfig.Provider, legacyConfig.Instance, plain, err)
	}

	assertJob := func(id uuid.UUID, wantStatus, wantReason string, wantRunner bool) {
		t.Helper()
		var status string
		var reason *string
		var storedRunner *uuid.UUID
		if err := connection.QueryRow(t.Context(), `SELECT status,failure_reason,runner_id FROM jobs WHERE id=$1`, id).
			Scan(&status, &reason, &storedRunner); err != nil {
			t.Fatal(err)
		}
		if status != wantStatus || valueOrEmpty(reason) != wantReason || (storedRunner != nil) != wantRunner {
			t.Fatalf("job %s: status=%s reason=%v runner=%v", id, status, reason, storedRunner)
		}
	}
	assertJob(assignedJob, "queued", "", false)
	assertJob(runningJob, "failed", "runner_lost", true)
	assertJob(downstreamJob, "skipped", "", false)
	assertJob(queuedJob, "queued", "", false)
	assertJob(terminalJob, "succeeded", "", true)

	var assignedStatus, runningStatus string
	var assignedStarted, runningFinished *time.Time
	if err := connection.QueryRow(t.Context(), `SELECT status,started_at FROM runs WHERE id=$1`, assignedRun).Scan(&assignedStatus, &assignedStarted); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(t.Context(), `SELECT status,finished_at FROM runs WHERE id=$1`, runningRun).Scan(&runningStatus, &runningFinished); err != nil {
		t.Fatal(err)
	}
	if assignedStatus != "queued" || assignedStarted != nil || runningStatus != "failed" || runningFinished == nil {
		t.Fatalf("run recovery mismatch: assigned=%s/%v running=%s/%v", assignedStatus, assignedStarted, runningStatus, runningFinished)
	}

	var leaseCount, migrationCount, userCount, recoveryAuditCount int
	if err := connection.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE lease_token_hash IS NOT NULL OR lease_expires_at IS NOT NULL`).Scan(&leaseCount); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE id=$1 AND display_name='preserved user'`, userID).Scan(&userCount); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='runner_protocol_upgrade_recovery'`).Scan(&recoveryAuditCount); err != nil {
		t.Fatal(err)
	}
	var runnerStatus string
	var legacySerial *string
	if err := connection.QueryRow(t.Context(), `SELECT status,certificate_serial FROM runners WHERE id=$1`, runnerID).Scan(&runnerStatus, &legacySerial); err != nil {
		t.Fatal(err)
	}
	if leaseCount != 0 || migrationCount != 20 || userCount != 1 || recoveryAuditCount != 2 || runnerStatus != "offline" || legacySerial != nil {
		t.Fatalf("upgrade preservation: leases=%d migrations=%d users=%d audits=%d runner=%s serial=%v", leaseCount, migrationCount, userCount, recoveryAuditCount, runnerStatus, legacySerial)
	}

	invalidStatements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runner_registration_tokens(pool_id,token_digest,expires_at,max_uses,used_count) VALUES ($1,decode(repeat('aa',32),'hex'),clock_timestamp()+interval '1 hour',1,2)`, []any{poolID}},
		{`INSERT INTO runner_certificates(runner_id,serial,csr_fingerprint,public_key_fingerprint,state,certificate_chain_pem,not_before,not_after) VALUES ($1,'0011223344556677',decode(repeat('aa',31),'hex'),decode(repeat('bb',32),'hex'),'active',convert_to('cert','UTF8'),clock_timestamp(),clock_timestamp()+interval '1 day')`, []any{runnerID}},
		{`INSERT INTO runner_certificates(runner_id,serial,csr_fingerprint,public_key_fingerprint,state,certificate_chain_pem,not_before,not_after) VALUES ($1,'1011223344556677',decode(repeat('aa',32),'hex'),decode(repeat('bb',32),'hex'),'unknown',convert_to('cert','UTF8'),clock_timestamp(),clock_timestamp()+interval '1 day')`, []any{runnerID}},
	}
	for _, statement := range invalidStatements {
		if _, err := connection.Exec(t.Context(), statement.query, statement.args...); err == nil {
			t.Fatal("Runner identity constraint accepted invalid row")
		}
	}

	var oldCertificateID uuid.UUID
	if err := connection.QueryRow(t.Context(), `INSERT INTO runner_certificates
		(runner_id,serial,csr_fingerprint,public_key_fingerprint,state,certificate_chain_pem,not_before,not_after)
		VALUES ($1,'2011223344556677',decode(repeat('cc',32),'hex'),decode(repeat('dd',32),'hex'),'active',
			convert_to('cert','UTF8'),clock_timestamp(),clock_timestamp()+interval '1 day') RETURNING id`, runnerID).Scan(&oldCertificateID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(t.Context(), `INSERT INTO runner_certificates
		(runner_id,serial,csr_fingerprint,public_key_fingerprint,state,certificate_chain_pem,not_before,not_after)
		VALUES ($1,'2111223344556677',decode(repeat('13',32),'hex'),decode(repeat('35',32),'hex'),'active',
			convert_to('cert','UTF8'),clock_timestamp(),clock_timestamp()+interval '1 day')`, runnerID); err == nil {
		t.Fatal("Runner accepted multiple active certificates")
	}
	if _, err := connection.Exec(t.Context(), `UPDATE runner_certificates SET state='retiring',retire_at=clock_timestamp()+interval '15 minutes' WHERE id=$1`, oldCertificateID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(t.Context(), `INSERT INTO runner_certificates
		(runner_id,serial,csr_fingerprint,public_key_fingerprint,state,certificate_chain_pem,not_before,not_after,replaces_certificate_id)
		VALUES ($1,'3011223344556677',decode(repeat('ee',32),'hex'),decode(repeat('ff',32),'hex'),'active',
			convert_to('cert','UTF8'),clock_timestamp(),clock_timestamp()+interval '1 day',$2)`, runnerID, oldCertificateID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(t.Context(), `INSERT INTO runner_certificates
		(runner_id,serial,csr_fingerprint,public_key_fingerprint,state,certificate_chain_pem,not_before,not_after,replaces_certificate_id)
		VALUES ($1,'4011223344556677',decode(repeat('12',32),'hex'),decode(repeat('34',32),'hex'),'expired',
			convert_to('cert','UTF8'),clock_timestamp(),clock_timestamp()+interval '1 day',$2)`, runnerID, oldCertificateID); err == nil {
		t.Fatal("one certificate accepted multiple pending replacements")
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestManagedGiteeBootstrapIsProviderInstanceBound(t *testing.T) {
	s, _, setup := managedFixture(t)
	access := provisioning.Access{SetupToken: setup}
	config := provisioning.Config{Info: provisioning.Info{
		ID:               uuid.New(),
		Provider:         "gitee",
		Instance:         identity.GiteeInstance,
		ClientID:         "gitee-fixture",
		BootstrapSubject: "100",
	}}
	if err := s.SaveLoginCandidate(t.Context(), access, config); err != nil {
		t.Fatal(err)
	}
	state, nonce := identity.NewToken(), identity.NewToken()
	if _, err := s.BeginManagedOAuth(t.Context(), state, nonce, "", config.ID, access); err != nil {
		t.Fatal(err)
	}
	ticket, err := s.ConsumeOAuth(t.Context(), state, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishManagedOAuth(t.Context(), ticket, githubUser("100"), "", setup); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("GitHub identity activated Gitee configuration: %v", err)
	}
	giteeUser := identity.ExternalUser{Provider: "gitee", Instance: identity.GiteeInstance, Subject: "100", Login: "fixture"}
	if _, err := s.FinishManagedOAuth(t.Context(), ticket, giteeUser, "", setup); err != nil {
		t.Fatal(err)
	}
	var provider, instance string
	if err := s.pool.QueryRow(t.Context(), `SELECT provider,provider_instance FROM oauth_bootstrap`).Scan(&provider, &instance); err != nil {
		t.Fatal(err)
	}
	if provider != "gitee" || instance != identity.GiteeInstance {
		t.Fatalf("bootstrap stored %q at %q", provider, instance)
	}
}
