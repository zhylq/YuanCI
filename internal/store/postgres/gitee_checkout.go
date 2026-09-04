package postgres

import (
	"context"
	"crypto/sha256"

	"github.com/google/uuid"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

// CheckGiteeCheckoutLease is read-only: checkout cannot renew or resurrect work.
func (s *Store) CheckGiteeCheckoutLease(ctx context.Context, lease runmodel.LeaseRequest, repository uuid.UUID, sha string) error {
	if lease.RunnerID == uuid.Nil || lease.JobID == uuid.Nil || repository == uuid.Nil || len(lease.LeaseToken) == 0 || len(lease.LeaseToken) > 512 {
		return runmodel.ErrLeaseInvalid
	}
	hash := sha256.Sum256([]byte(lease.LeaseToken))
	var allowed bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j
 JOIN runs run ON run.id=j.run_id JOIN repositories r ON r.id=run.repository_id
 JOIN runners runner ON runner.id=j.runner_id JOIN gitee_authorizations g ON g.id=r.gitee_authorization_id
 WHERE j.id=$1 AND j.runner_id=$2 AND j.lease_token_hash=$3 AND j.status IN ('assigned','running')
 AND j.lease_expires_at>clock_timestamp() AND runner.status<>'disabled'
 AND run.status IN ('queued','running') AND r.id=$4 AND run.commit_sha=$5
 AND r.provider='gitee' AND r.provider_instance='https://gitee.com' AND r.active
 AND g.status<>'revoked' AND `+liveGiteeGrant+`)`, lease.JobID, lease.RunnerID, hash[:], repository, sha).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return runmodel.ErrLeaseInvalid
	}
	return nil
}
