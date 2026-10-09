package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/pipeline"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

func completeDeploymentJob(ctx context.Context, tx pgx.Tx, runID uuid.UUID, request runmodel.RunnerCompletion, digest []byte) error {
	var status runmodel.JobStatus
	var started, finished, live bool
	err := tx.QueryRow(ctx, `SELECT status,started_at IS NOT NULL,execution_finished_at IS NOT NULL,
	 COALESCE(lease_expires_at>clock_timestamp(),false) FROM jobs
	 WHERE id=$1 AND runner_id=$2 AND (execution_token_hash=$3 OR
	 (started_at IS NULL AND lease_token_hash=$3 AND lease_expires_at>clock_timestamp())) FOR UPDATE`, request.JobID, request.RunnerID, digest).Scan(&status, &started, &finished, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return runmodel.ErrLeaseInvalid
	}
	if err != nil {
		return err
	}
	if finished {
		return nil
	} // Durable reconnect acknowledgement using the original token.
	if !live && !status.Terminal() { // No normal result may win after lease expiry.
		status = runmodel.JobFailed
		if _, err := tx.Exec(ctx, `UPDATE jobs SET status='failed',failure_reason='runner_lost',finished_at=COALESCE(finished_at,clock_timestamp()),lease_token_hash=NULL,lease_expires_at=NULL WHERE id=$1`, request.JobID); err != nil {
			return err
		}
	}
	if !status.Terminal() {
		status = request.Status
		if started && !request.CleanupConfirmed {
			status = runmodel.JobFailed
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET status=$2,finished_at=clock_timestamp(),lease_token_hash=NULL,lease_expires_at=NULL,
		 failure_reason=CASE WHEN $3 THEN 'cleanup_unconfirmed' ELSE failure_reason END WHERE id=$1`, request.JobID, status, started && !request.CleanupConfirmed); err != nil {
			return err
		}
	}
	if request.CleanupConfirmed && started {
		if _, err := tx.Exec(ctx, `UPDATE jobs SET execution_finished_at=COALESCE(execution_finished_at,clock_timestamp()) WHERE id=$1`, request.JobID); err != nil {
			return err
		}
	}
	return finalizeRunJobs(ctx, tx, runID, status)
}

var deploymentSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var zeroSHAPattern = regexp.MustCompile(`^0+$`)

func decodeDeployment(record runmodel.Record) (pipeline.Plan, error) {
	var plan pipeline.Plan
	if err := json.Unmarshal(record.Plan, &plan); err != nil {
		return plan, err
	}
	if pipeline.ValidatePlanDeployment(plan) != nil {
		return plan, runmodel.ErrInvalidDeployment
	}
	if plan.Deployment != nil && (record.ProjectID == nil || *record.ProjectID == uuid.Nil || !deploymentSHAPattern.MatchString(record.CommitSHA) || zeroSHAPattern.MatchString(record.CommitSHA)) {
		return plan, runmodel.ErrInvalidDeployment
	}
	return plan, nil
}

// Registration locks before assigning queue_order, making committed queue order
// stable even when delivery transactions race. Hash collisions only serialize
// unrelated environments and cannot weaken identity or FIFO constraints.
func lockDeploymentEnvironment(ctx context.Context, tx pgx.Tx, repositoryID uuid.UUID, environment string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "yuanci.deploy:"+repositoryID.String()+":"+environment)
	return err
}

func runnerDeploymentEligible(ctx context.Context, tx pgx.Tx, runID uuid.UUID, poolType string, protocol int, labels []byte) (bool, error) {
	var eligible bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM deployment_runs WHERE run_id=$1) OR
	 ($2='deployment' AND $3>=3 AND EXISTS (
	 SELECT 1 FROM deployment_runs AS deployment JOIN repositories AS repository ON repository.id=deployment.repository_id
	 WHERE deployment.run_id=$1 AND deployment.released_at IS NULL
	 AND ($4::jsonb ->> ('yuanci.deploy.' || repository.provider || '.' || repository.external_id))='true'
	 AND NOT EXISTS(SELECT 1 FROM deployment_runs AS prior WHERE prior.repository_id=deployment.repository_id
	 AND prior.environment=deployment.environment AND prior.released_at IS NULL AND prior.queue_order<deployment.queue_order)
	 AND NOT EXISTS(SELECT 1 FROM jobs WHERE run_id=$1 AND
	 (status IN ('assigned','running') OR (started_at IS NOT NULL AND execution_finished_at IS NULL)))))`, runID, poolType, protocol, labels).Scan(&eligible)
	return eligible, err
}

// The caller holds the Run lock. Terminal result and execution termination are
// separate: a canceled/failed process continues to own its environment gate.
func releaseDeploymentRun(ctx context.Context, tx pgx.Tx, runID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE deployment_runs SET released_at=COALESCE(released_at,clock_timestamp())
 WHERE run_id=$1 AND NOT EXISTS (SELECT 1 FROM jobs WHERE run_id=$1 AND
 (status IN ('blocked','queued','assigned','running') OR (started_at IS NOT NULL AND execution_finished_at IS NULL)))`, runID)
	return err
}
