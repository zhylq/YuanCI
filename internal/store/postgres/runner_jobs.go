package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

func (s *Store) ClaimRunnerJob(ctx context.Context, request runmodel.RunnerClaim) (*runmodel.Assignment, error) {
	if request.RunnerID == uuid.Nil {
		return nil, runmodel.ErrInvalidRunnerRequest
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin Runner claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var poolType, runnerOS, architecture, executor string
	var labels []byte
	var capacity, protocolVersion int
	var disk int64
	err = tx.QueryRow(ctx, `SELECT pool.pool_type,COALESCE(runner.os,''),COALESCE(runner.architecture,''),
		COALESCE(runner.executor,''),runner.labels,runner.capacity,COALESCE(runner.available_disk_bytes,0),runner.protocol_version
        FROM runners AS runner JOIN runner_pools AS pool ON pool.id=runner.pool_id
        WHERE runner.id=$1 AND runner.status <> 'disabled' FOR UPDATE OF runner`, request.RunnerID).
		Scan(&poolType, &runnerOS, &architecture, &executor, &labels, &capacity, &disk, &protocolVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, runmodel.ErrInvalidRunnerRequest
	}
	if err != nil {
		return nil, fmt.Errorf("lock Runner: %w", err)
	}
	if runnerOS == "" || architecture == "" || executor == "" {
		return nil, runmodel.ErrInvalidRunnerRequest
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE runner_id=$1
        AND status IN ('assigned','running') AND lease_expires_at > clock_timestamp()`, request.RunnerID).Scan(&active); err != nil {
		return nil, fmt.Errorf("count Runner jobs: %w", err)
	}
	if active >= capacity {
		return nil, nil
	}

	var assignment runmodel.Assignment
	var spec []byte
	err = tx.QueryRow(ctx, `SELECT run.id FROM runs AS run
		WHERE run.status IN ('queued','running') AND EXISTS (
            SELECT 1 FROM jobs AS job WHERE job.run_id=run.id AND job.status='queued'
              AND job.required_pool_type=$1 AND job.required_os=$2
              AND (job.required_architecture IS NULL OR job.required_architecture=$3)
              AND job.required_executor=$4 AND job.required_labels <@ $5::jsonb
			  AND job.required_disk_bytes <= $6
			  AND ($7 >= 2 OR run.repository_id IS NULL OR run.commit_sha IS NULL))
        ORDER BY run.created_at,run.id FOR UPDATE OF run SKIP LOCKED LIMIT 1`,
		poolType, runnerOS, architecture, executor, labels, disk, protocolVersion).Scan(&assignment.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock matching run: %w", err)
	}
	var sourceProvider, sourceRepositoryID, sourceCloneURL, sourceCommitSHA sql.NullString
	err = tx.QueryRow(ctx, `SELECT job.id,job.run_id,job.stage_name,job.job_name,job.attempt,job.spec,
		  repository.provider,repository.external_id,repository.clone_url,source_run.commit_sha
		FROM jobs AS job JOIN runs AS source_run ON source_run.id=job.run_id
		LEFT JOIN repositories AS repository ON repository.id=source_run.repository_id
		WHERE job.run_id=$1 AND job.status='queued' AND job.required_pool_type=$2 AND job.required_os=$3
		  AND (job.required_architecture IS NULL OR job.required_architecture=$4) AND job.required_executor=$5
		  AND job.required_labels <@ $6::jsonb AND job.required_disk_bytes <= $7
		  AND ($8 >= 2 OR source_run.repository_id IS NULL OR source_run.commit_sha IS NULL)
		ORDER BY job.created_at,job.id FOR UPDATE OF job SKIP LOCKED LIMIT 1`, assignment.RunID, poolType,
		runnerOS, architecture, executor, labels, disk, protocolVersion).Scan(&assignment.JobID, &assignment.RunID,
		&assignment.StageName, &assignment.JobName, &assignment.Attempt, &spec, &sourceProvider,
		&sourceRepositoryID, &sourceCloneURL, &sourceCommitSHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select matching job: %w", err)
	}
	if err := json.Unmarshal(spec, &assignment.Spec); err != nil {
		return nil, fmt.Errorf("decode job spec: %w", err)
	}
	if sourceProvider.Valid || sourceRepositoryID.Valid || sourceCloneURL.Valid || sourceCommitSHA.Valid {
		if !sourceProvider.Valid || !sourceRepositoryID.Valid || !sourceCloneURL.Valid || !sourceCommitSHA.Valid {
			return nil, errors.New("source-backed job has incomplete repository metadata")
		}
		assignment.Source = &runmodel.SourceCheckout{Provider: sourceProvider.String,
			RepositoryID: sourceRepositoryID.String, CloneURL: sourceCloneURL.String, CommitSHA: sourceCommitSHA.String}
	}
	assignment.LeaseToken = secureToken()
	digest := sha256.Sum256([]byte(assignment.LeaseToken))
	err = tx.QueryRow(ctx, `UPDATE jobs SET status='assigned',runner_id=$2,lease_token_hash=$3,
        accepted_at=NULL,lease_renewed_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '30 seconds'
        WHERE id=$1 AND status='queued' RETURNING lease_expires_at`, assignment.JobID, request.RunnerID, digest[:]).
		Scan(&assignment.LeaseExpires)
	if err != nil {
		return nil, fmt.Errorf("bind Runner lease: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET status='running',started_at=COALESCE(started_at,clock_timestamp())
        WHERE id=$1 AND status='queued'`, assignment.RunID); err != nil {
		return nil, fmt.Errorf("start run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit Runner claim: %w", err)
	}
	return &assignment, nil
}

func (s *Store) AcknowledgeRunnerJob(ctx context.Context, request runmodel.LeaseRequest) (runmodel.LeaseState, error) {
	if err := validateRunnerLeaseRequest(request); err != nil {
		return runmodel.LeaseState{}, err
	}
	digest := sha256.Sum256([]byte(request.LeaseToken))
	state := runmodel.LeaseState{JobID: request.JobID}
	err := s.pool.QueryRow(ctx, `UPDATE jobs SET accepted_at=COALESCE(accepted_at,clock_timestamp())
        WHERE id=$1 AND runner_id=$2 AND status IN ('assigned','running') AND lease_token_hash=$3
          AND lease_expires_at > clock_timestamp() RETURNING lease_expires_at`,
		request.JobID, request.RunnerID, digest[:]).Scan(&state.LeaseExpires)
	if errors.Is(err, pgx.ErrNoRows) {
		return runmodel.LeaseState{}, runmodel.ErrLeaseInvalid
	}
	if err != nil {
		return runmodel.LeaseState{}, fmt.Errorf("acknowledge Runner job: %w", err)
	}
	return state, nil
}

func (s *Store) StartRunnerJob(ctx context.Context, request runmodel.LeaseRequest) (runmodel.LeaseState, error) {
	if err := validateRunnerLeaseRequest(request); err != nil {
		return runmodel.LeaseState{}, err
	}
	digest := sha256.Sum256([]byte(request.LeaseToken))
	state := runmodel.LeaseState{JobID: request.JobID}
	err := s.pool.QueryRow(ctx, `UPDATE jobs SET status='running',started_at=COALESCE(started_at,clock_timestamp())
        WHERE id=$1 AND runner_id=$2 AND status IN ('assigned','running') AND accepted_at IS NOT NULL
          AND lease_token_hash=$3 AND lease_expires_at > clock_timestamp() RETURNING lease_expires_at`,
		request.JobID, request.RunnerID, digest[:]).Scan(&state.LeaseExpires)
	if errors.Is(err, pgx.ErrNoRows) {
		return runmodel.LeaseState{}, runmodel.ErrLeaseInvalid
	}
	if err != nil {
		return runmodel.LeaseState{}, fmt.Errorf("start Runner job: %w", err)
	}
	return state, nil
}

func (s *Store) RenewRunnerLeases(ctx context.Context, request runmodel.HeartbeatRequest) (runmodel.HeartbeatResult, error) {
	if err := runmodel.ValidateHeartbeatRequest(request); err != nil {
		return runmodel.HeartbeatResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return runmodel.HeartbeatResult{}, fmt.Errorf("begin Runner heartbeat: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var poolType string
	err = tx.QueryRow(ctx, `SELECT pool.pool_type FROM runners AS runner
        JOIN runner_pools AS pool ON pool.id=runner.pool_id
        WHERE runner.id=$1 AND runner.status <> 'disabled' FOR UPDATE OF runner`, request.Runner.ID).Scan(&poolType)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && poolType != request.Runner.PoolType) {
		return runmodel.HeartbeatResult{}, runmodel.ErrInvalidRunnerRequest
	}
	if err != nil {
		return runmodel.HeartbeatResult{}, fmt.Errorf("lock heartbeat Runner: %w", err)
	}
	labels, err := json.Marshal(request.Runner.Labels)
	if err != nil {
		return runmodel.HeartbeatResult{}, runmodel.ErrInvalidRunnerRequest
	}
	if _, err := tx.Exec(ctx, `UPDATE runners SET status='online',last_seen_at=clock_timestamp(),os=$2,
        architecture=$3,executor=$4,isolation_level=$5,labels=$6,capacity=$7,available_disk_bytes=$8
        WHERE id=$1`, request.Runner.ID, request.Runner.OS, request.Runner.Architecture,
		request.Runner.Executor, request.Runner.PoolType, labels, request.Runner.Capacity,
		request.Runner.AvailableDiskBytes); err != nil {
		return runmodel.HeartbeatResult{}, fmt.Errorf("update Runner heartbeat: %w", err)
	}
	var now, expires time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp(),clock_timestamp()+interval '30 seconds'`).Scan(&now, &expires); err != nil {
		return runmodel.HeartbeatResult{}, fmt.Errorf("read heartbeat deadline: %w", err)
	}
	result := runmodel.HeartbeatResult{LeaseExpires: expires, Jobs: make([]runmodel.LeaseResult, 0, len(request.ActiveJobs))}
	for _, active := range request.ActiveJobs {
		lease := runmodel.LeaseResult{JobID: active.JobID}
		digest := sha256.Sum256([]byte(active.LeaseToken))
		command, updateErr := tx.Exec(ctx, `UPDATE jobs SET lease_renewed_at=$4,lease_expires_at=$5
            WHERE id=$1 AND runner_id=$2 AND lease_token_hash=$3 AND status IN ('assigned','running')
              AND lease_expires_at > $4`, active.JobID, request.Runner.ID, digest[:], now, expires)
		if updateErr != nil {
			return runmodel.HeartbeatResult{}, fmt.Errorf("renew Runner lease: %w", updateErr)
		}
		if command.RowsAffected() == 1 {
			lease.Renewed = true
			lease.LeaseExpires = expires
		} else {
			lease.CancelReason = "lease_invalid"
		}
		result.Jobs = append(result.Jobs, lease)
	}
	if err := tx.Commit(ctx); err != nil {
		return runmodel.HeartbeatResult{}, fmt.Errorf("commit Runner heartbeat: %w", err)
	}
	return result, nil
}

func (s *Store) CompleteRunnerJob(ctx context.Context, request runmodel.RunnerCompletion) error {
	if request.RunnerID == uuid.Nil || request.JobID == uuid.Nil || request.LeaseToken == "" {
		return runmodel.ErrInvalidRunnerRequest
	}
	if request.Status != runmodel.JobSucceeded && request.Status != runmodel.JobFailed && request.Status != runmodel.JobCanceled {
		return errors.New("invalid terminal job status")
	}
	digest := sha256.Sum256([]byte(request.LeaseToken))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin Runner completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var runID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT run_id FROM jobs WHERE id=$1`, request.JobID).Scan(&runID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return runmodel.ErrLeaseInvalid
		}
		return fmt.Errorf("find Runner job: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM runs WHERE id=$1 FOR UPDATE`, runID); err != nil {
		return fmt.Errorf("lock completing run: %w", err)
	}
	err = tx.QueryRow(ctx, `UPDATE jobs SET status=$4,finished_at=clock_timestamp(),lease_token_hash=NULL,
        lease_expires_at=NULL WHERE id=$1 AND runner_id=$2 AND lease_token_hash=$3
          AND status IN ('assigned','running') AND lease_expires_at > clock_timestamp() RETURNING run_id`,
		request.JobID, request.RunnerID, digest[:], request.Status).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return runmodel.ErrLeaseInvalid
	}
	if err != nil {
		return fmt.Errorf("complete Runner job: %w", err)
	}
	if err := finalizeRunJobs(ctx, tx, runID, request.Status); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Runner completion: %w", err)
	}
	return nil
}

func finalizeRunJobs(ctx context.Context, tx pgx.Tx, runID uuid.UUID, status runmodel.JobStatus) error {
	if status == runmodel.JobSucceeded {
		if _, err := tx.Exec(ctx, `UPDATE jobs AS candidate SET status='queued'
            WHERE candidate.run_id=$1 AND candidate.status='blocked' AND NOT EXISTS (
              SELECT 1 FROM unnest(candidate.dependencies) AS dependency WHERE NOT EXISTS (
                SELECT 1 FROM jobs AS completed WHERE completed.run_id=candidate.run_id
                  AND completed.job_key=dependency AND completed.status='succeeded'))`, runID); err != nil {
			return fmt.Errorf("unblock dependent jobs: %w", err)
		}
	} else if status == runmodel.JobFailed || status == runmodel.JobCanceled {
		if _, err := tx.Exec(ctx, `UPDATE jobs SET status='skipped',finished_at=clock_timestamp()
            WHERE run_id=$1 AND status IN ('blocked','queued')`, runID); err != nil {
			return fmt.Errorf("skip downstream jobs: %w", err)
		}
	}
	var remaining, failed, canceled int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('blocked','queued','assigned','running')),
        count(*) FILTER (WHERE status='failed'),count(*) FILTER (WHERE status='canceled') FROM jobs WHERE run_id=$1`, runID).
		Scan(&remaining, &failed, &canceled); err != nil {
		return fmt.Errorf("summarize run jobs: %w", err)
	}
	if remaining != 0 {
		return nil
	}
	finalStatus := runmodel.StatusSucceeded
	if failed > 0 {
		finalStatus = runmodel.StatusFailed
	} else if canceled > 0 {
		finalStatus = runmodel.StatusCanceled
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1`, runID, finalStatus); err != nil {
		return fmt.Errorf("finalize run: %w", err)
	}
	return nil
}

func validateRunnerLeaseRequest(request runmodel.LeaseRequest) error {
	if request.RunnerID == uuid.Nil || request.JobID == uuid.Nil || request.LeaseToken == "" {
		return runmodel.ErrInvalidRunnerRequest
	}
	return nil
}

var _ runmodel.RunnerJobStore = (*Store)(nil)
