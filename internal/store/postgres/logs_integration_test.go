package postgres

import (
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

func TestLogChunksOrderingLeaseQuotaAndRetention(t *testing.T) {
	s, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	_, runID := insertCommitStatusParents(t, s)
	runnerID, jobID := uuid.New(), uuid.New()
	digest := sha256.Sum256([]byte("log-lease"))
	_, err = s.pool.Exec(t.Context(), `INSERT INTO runners(id,pool_id,name,status,capacity) SELECT $1,id,'log-runner','online',1 FROM runner_pools WHERE name='standard'`, runnerID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(t.Context(), `INSERT INTO jobs(id,run_id,runner_id,stage_name,job_name,job_key,spec,status,lease_token_hash,lease_expires_at) VALUES($1,$2,$3,'build','test','build/test','{}','running',$4,clock_timestamp()+interval '1 hour')`, jobID, runID, runnerID, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	chunk := runmodel.LogChunk{Lease: runmodel.LeaseRequest{RunnerID: runnerID, JobID: jobID, LeaseToken: "log-lease"}, Sequence: 1, Stream: "stdout", Step: 0, Data: []byte("hello")}
	gap := chunk
	gap.Sequence = 2
	if err := s.AppendLogChunk(t.Context(), gap); !errors.Is(err, runmodel.ErrLogSequence) {
		t.Fatalf("gap: %v", err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() { failures <- s.AppendLogChunk(t.Context(), chunk) })
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	conflict := chunk
	conflict.Data = []byte("changed")
	if err := s.AppendLogChunk(t.Context(), conflict); !errors.Is(err, runmodel.ErrLogSequence) {
		t.Fatalf("conflict: %v", err)
	}
	wrong := chunk
	wrong.Lease.RunnerID = uuid.New()
	if err := s.AppendLogChunk(t.Context(), wrong); !errors.Is(err, runmodel.ErrLeaseInvalid) {
		t.Fatalf("identity: %v", err)
	}
	var count int
	var total int64
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*),sum(octet_length(data)) FROM job_log_chunks WHERE job_id=$1`, jobID).Scan(&count, &total); err != nil || count != 1 || total != 5 {
		t.Fatalf("count=%d total=%d err=%v", count, total, err)
	}
	invalid := gap
	invalid.Data = make([]byte, runmodel.MaxLogChunkBytes+1)
	if err := s.AppendLogChunk(t.Context(), invalid); !errors.Is(err, runmodel.ErrInvalidLogChunk) {
		t.Fatalf("size: %v", err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE job_log_streams SET total_bytes=$2 WHERE job_id=$1`, jobID, runmodel.MaxJobLogBytes); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLogChunk(t.Context(), gap); !errors.Is(err, runmodel.ErrLogLimit) {
		t.Fatalf("quota: %v", err)
	}
	gap.Data = nil
	gap.Truncated = true
	if err := s.AppendLogChunk(t.Context(), gap); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLogChunk(t.Context(), gap); err != nil {
		t.Fatalf("truncation replay: %v", err)
	}
	end := chunk
	end.Sequence = 3
	if err := s.AppendLogChunk(t.Context(), end); !errors.Is(err, runmodel.ErrLogLimit) {
		t.Fatalf("closed: %v", err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE job_log_streams SET expires_at=clock_timestamp()-interval '1 second' WHERE job_id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLogChunk(t.Context(), chunk); !errors.Is(err, runmodel.ErrLogExpired) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM job_log_streams WHERE expires_at<clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM job_log_chunks WHERE job_id=$1`, jobID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retention cascade: %d %v", count, err)
	}
}
