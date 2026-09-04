package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"

	"github.com/jackc/pgx/v5"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

func (s *Store) AppendLogChunk(ctx context.Context, c runmodel.LogChunk) error {
	if err := runmodel.ValidateLogChunk(c); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	digest := sha256.Sum256([]byte(c.Lease.LeaseToken))
	// Serialize with completion, recovery and other appenders on the Job row.
	var valid bool
	err = tx.QueryRow(ctx, `SELECT true FROM jobs WHERE id=$1 AND runner_id=$2
 AND lease_token_hash=$3 AND lease_expires_at>clock_timestamp() AND status='running' FOR UPDATE`,
		c.Lease.JobID, c.Lease.RunnerID, digest[:]).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return runmodel.ErrLeaseInvalid
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO job_log_streams(job_id,expires_at)
 SELECT id,created_at+interval '7 days' FROM jobs WHERE id=$1 ON CONFLICT DO NOTHING`, c.Lease.JobID)
	if err != nil {
		return err
	}
	var next, total int64
	var truncated, expired bool
	err = tx.QueryRow(ctx, `SELECT next_sequence,total_bytes,truncated,expires_at<=clock_timestamp()
 FROM job_log_streams WHERE job_id=$1 FOR UPDATE`, c.Lease.JobID).Scan(&next, &total, &truncated, &expired)
	if err != nil {
		return err
	}
	if expired {
		return runmodel.ErrLogExpired
	}
	if c.Sequence < next {
		var data []byte
		var step int
		var stream string
		var marker bool
		err = tx.QueryRow(ctx, `SELECT data,step_index,stream,truncated FROM job_log_chunks WHERE job_id=$1 AND sequence=$2`, c.Lease.JobID, c.Sequence).Scan(&data, &step, &stream, &marker)
		if errors.Is(err, pgx.ErrNoRows) {
			return runmodel.ErrLogSequence
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(data, c.Data) || step != c.Step || stream != c.Stream || marker != c.Truncated {
			return runmodel.ErrLogSequence
		}
		return tx.Commit(ctx)
	}
	if c.Sequence != next {
		return runmodel.ErrLogSequence
	}
	if truncated || total+int64(len(c.Data)) > runmodel.MaxJobLogBytes || (c.Sequence == runmodel.MaxJobLogChunks && !c.Truncated) {
		return runmodel.ErrLogLimit
	}
	data := c.Data
	if data == nil {
		data = []byte{}
	}
	_, err = tx.Exec(ctx, `INSERT INTO job_log_chunks(job_id,sequence,step_index,stream,data,truncated) VALUES($1,$2,$3,$4,$5,$6)`, c.Lease.JobID, c.Sequence, c.Step, c.Stream, data, c.Truncated)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE job_log_streams SET next_sequence=next_sequence+1,total_bytes=total_bytes+$2,truncated=$3 WHERE job_id=$1`, c.Lease.JobID, len(data), c.Truncated)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ runmodel.LogStore = (*Store)(nil)
