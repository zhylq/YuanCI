package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuanci/yuanci/db/migrations"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	store := &Store{pool: pool}
	if err := store.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *Store) Close()                         { s.pool.Close() }

func (s *Store) Create(ctx context.Context, record runmodel.Record) (runmodel.Record, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return runmodel.Record{}, fmt.Errorf("begin create run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const query = `INSERT INTO runs
        (id, pipeline_name, event, ref, commit_sha, status, config_sha256, compiled_plan, created_at)
        VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9)`
	_, err = tx.Exec(ctx, query, record.ID, record.PipelineName, record.Event, record.Ref,
		record.CommitSHA, record.Status, record.ConfigSHA256, record.Plan, record.CreatedAt)
	if err != nil {
		return runmodel.Record{}, fmt.Errorf("create run: %w", err)
	}
	var plan pipeline.Plan
	if err := json.Unmarshal(record.Plan, &plan); err != nil {
		return runmodel.Record{}, fmt.Errorf("decode compiled plan: %w", err)
	}
	stageJobs := make(map[string][]string, len(plan.Stages))
	for _, stage := range plan.Stages {
		for _, job := range stage.Jobs {
			stageJobs[stage.Name] = append(stageJobs[stage.Name], stage.Name+"/"+job.Name)
		}
	}
	for _, stage := range plan.Stages {
		for _, job := range stage.Jobs {
			dependencies := make([]string, 0)
			for _, dependency := range stage.DependsOn {
				dependencies = append(dependencies, stageJobs[dependency]...)
			}
			for _, dependency := range job.DependsOn {
				dependencies = append(dependencies, stage.Name+"/"+dependency)
			}
			status := runmodel.JobBlocked
			if len(dependencies) == 0 {
				status = runmodel.JobQueued
			}
			spec, err := json.Marshal(job)
			if err != nil {
				return runmodel.Record{}, fmt.Errorf("encode job spec: %w", err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO jobs
                    (id, run_id, stage_name, job_name, job_key, dependencies, spec, status, attempt)
                    VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1)`,
				uuid.New(), record.ID, stage.Name, job.Name, stage.Name+"/"+job.Name, dependencies, spec, status)
			if err != nil {
				return runmodel.Record{}, fmt.Errorf("create job %s/%s: %w", stage.Name, job.Name, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return runmodel.Record{}, fmt.Errorf("commit run: %w", err)
	}
	return record, nil
}

func (s *Store) List(ctx context.Context, limit int) ([]runmodel.Record, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, pipeline_name, event, COALESCE(ref,''),
		COALESCE(commit_sha,''), status, config_sha256, compiled_plan, created_at,
		started_at, finished_at
		FROM runs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
	result := make([]runmodel.Record, 0, limit)
	for rows.Next() {
		var record runmodel.Record
		if err := rows.Scan(&record.ID, &record.PipelineName, &record.Event, &record.Ref,
			&record.CommitSHA, &record.Status, &record.ConfigSHA256, &record.Plan, &record.CreatedAt,
			&record.StartedAt, &record.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) ClaimJob(ctx context.Context, request runmodel.ClaimRequest) (*runmodel.Assignment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim job: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var assignment runmodel.Assignment
	var spec []byte
	// Always lock the parent before children, as completion also updates the
	// Run and may unblock/skip sibling jobs. Other Runs remain claimable.
	err = tx.QueryRow(ctx, `SELECT r.id FROM runs r
        WHERE r.status IN ('queued','running')
        AND EXISTS (SELECT 1 FROM jobs j WHERE j.run_id=r.id AND j.status='queued')
        ORDER BY r.created_at, r.id FOR UPDATE OF r SKIP LOCKED LIMIT 1`).Scan(&assignment.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock queued run: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT id, run_id, stage_name, job_name, attempt, spec
        FROM jobs WHERE run_id=$1 AND status='queued' ORDER BY created_at, id
        FOR UPDATE SKIP LOCKED LIMIT 1`, assignment.RunID).Scan(&assignment.JobID, &assignment.RunID,
		&assignment.StageName, &assignment.JobName, &assignment.Attempt, &spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select queued job: %w", err)
	}
	if err := json.Unmarshal(spec, &assignment.Spec); err != nil {
		return nil, fmt.Errorf("decode job spec: %w", err)
	}
	assignment.LeaseToken = secureToken()
	leaseDuration := assignment.Spec.Timeout + 5*time.Minute
	if leaseDuration < 10*time.Minute {
		leaseDuration = 10 * time.Minute
	}
	assignment.LeaseExpires = time.Now().UTC().Add(leaseDuration)
	digest := sha256.Sum256([]byte(assignment.LeaseToken))
	result, err := tx.Exec(ctx, `UPDATE jobs SET status='assigned', lease_token_hash=$2,
        lease_expires_at=$3 WHERE id=$1 AND status='queued'`, assignment.JobID, digest[:], assignment.LeaseExpires)
	if err != nil {
		return nil, fmt.Errorf("lease job: %w", err)
	}
	if result.RowsAffected() != 1 {
		return nil, runmodel.ErrLeaseInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET status='running', started_at=COALESCE(started_at, now())
        WHERE id=$1 AND status='queued'`, assignment.RunID); err != nil {
		return nil, fmt.Errorf("start run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit job claim: %w", err)
	}
	_ = request // Runner identity is bound to certificates in the gRPC implementation.
	return &assignment, nil
}

func (s *Store) StartJob(ctx context.Context, id uuid.UUID, token string) error {
	digest := sha256.Sum256([]byte(token))
	result, err := s.pool.Exec(ctx, `UPDATE jobs SET status='running', started_at=COALESCE(started_at, now())
        WHERE id=$1 AND status='assigned' AND lease_token_hash=$2 AND lease_expires_at > clock_timestamp()`, id, digest[:])
	if err != nil {
		return fmt.Errorf("start job: %w", err)
	}
	if result.RowsAffected() != 1 {
		return runmodel.ErrLeaseInvalid
	}
	return nil
}

func (s *Store) CompleteJob(ctx context.Context, id uuid.UUID, token string, status runmodel.JobStatus) error {
	if status != runmodel.JobSucceeded && status != runmodel.JobFailed && status != runmodel.JobCanceled {
		return errors.New("invalid terminal job status")
	}
	digest := sha256.Sum256([]byte(token))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin complete job: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var runID uuid.UUID
	// Serialize graph updates for one Run. Without this lock, two successful
	// siblings can both observe an unfinished peer and leave their join blocked.
	err = tx.QueryRow(ctx, `SELECT run_id FROM jobs WHERE id=$1`, id).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return runmodel.ErrLeaseInvalid
	}
	if err != nil {
		return fmt.Errorf("find job run: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM runs WHERE id=$1 FOR UPDATE`, runID); err != nil {
		return fmt.Errorf("lock completing run: %w", err)
	}
	err = tx.QueryRow(ctx, `UPDATE jobs SET status=$3, finished_at=now(), lease_token_hash=NULL,
        lease_expires_at=NULL WHERE id=$1 AND status IN ('assigned','running')
        AND lease_token_hash=$2 AND lease_expires_at > clock_timestamp() RETURNING run_id`, id, digest[:], status).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return runmodel.ErrLeaseInvalid
	}
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	if status == runmodel.JobSucceeded {
		_, err = tx.Exec(ctx, `UPDATE jobs AS candidate SET status='queued'
            WHERE candidate.run_id=$1 AND candidate.status='blocked'
            AND NOT EXISTS (
                SELECT 1 FROM unnest(candidate.dependencies) AS dependency
                WHERE NOT EXISTS (
                    SELECT 1 FROM jobs AS completed
                    WHERE completed.run_id=candidate.run_id
                    AND completed.job_key=dependency AND completed.status='succeeded'
                )
            )`, runID)
		if err != nil {
			return fmt.Errorf("unblock dependent jobs: %w", err)
		}
	}
	if status == runmodel.JobFailed || status == runmodel.JobCanceled {
		if _, err := tx.Exec(ctx, `UPDATE jobs SET status='skipped', finished_at=now()
            WHERE run_id=$1 AND status IN ('blocked','queued')`, runID); err != nil {
			return fmt.Errorf("skip downstream jobs: %w", err)
		}
	}
	var remaining int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE run_id=$1
        AND status IN ('blocked','queued','assigned','running')`, runID).Scan(&remaining); err != nil {
		return fmt.Errorf("count remaining jobs: %w", err)
	}
	if remaining == 0 {
		finalStatus := runmodel.StatusSucceeded
		var failed, canceled int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='failed'),
            count(*) FILTER (WHERE status='canceled') FROM jobs WHERE run_id=$1`, runID).Scan(&failed, &canceled); err != nil {
			return err
		}
		if failed > 0 {
			finalStatus = runmodel.StatusFailed
		} else if canceled > 0 {
			finalStatus = runmodel.StatusCanceled
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET status=$2, finished_at=now() WHERE id=$1`, runID, finalStatus); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit job completion: %w", err)
	}
	return nil
}

func secureToken() string {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}

func (s *Store) migrate(ctx context.Context) error {
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Release()

	// Advisory locks are session-scoped, so all migration operations must use
	// this dedicated connection. The key is stable and unique to YuanCI.
	const migrationLockKey int64 = 0x5975616e4349
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockContext, `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()

	if _, err := connection.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var exists bool
		if err := connection.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists {
			continue
		}
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := connection.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, name)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
