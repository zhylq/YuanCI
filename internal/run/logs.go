package run

import (
	"context"
	"errors"
	"time"
)

const (
	MaxLogChunkBytes = 32 << 10
	MaxJobLogBytes   = 16 << 20
	MaxJobLogChunks  = 8192
	LogRetention     = 7 * 24 * time.Hour
)

var (
	ErrInvalidLogChunk = errors.New("invalid log chunk")
	ErrLogSequence     = errors.New("log sequence conflict")
	ErrLogLimit        = errors.New("job log limit reached")
	ErrLogExpired      = errors.New("job log retention expired")
)

// LogChunk is ordered across stdout/stderr and all steps in one Job. Data must
// already be redacted by the Runner. Lease material is never stored with logs.
// An empty truncation marker closes the stream and consumes one sequence.
type LogChunk struct {
	Lease     LeaseRequest `json:"-"`
	Sequence  int64        `json:"sequence"`
	Step      int          `json:"step"`
	Stream    string       `json:"stream"`
	Data      []byte       `json:"data"`
	Truncated bool         `json:"truncated"`
}

type LogStore interface {
	AppendLogChunk(context.Context, LogChunk) error
}

func ValidateLogChunk(c LogChunk) error {
	if c.Lease.RunnerID == [16]byte{} || c.Lease.JobID == [16]byte{} || len(c.Lease.LeaseToken) == 0 || len(c.Lease.LeaseToken) > 512 ||
		c.Sequence < 1 || c.Sequence > MaxJobLogChunks || c.Step < 0 || c.Step > 1023 || (c.Stream != "stdout" && c.Stream != "stderr") ||
		len(c.Data) > MaxLogChunkBytes || (!c.Truncated && len(c.Data) == 0) || (c.Truncated && len(c.Data) != 0) {
		return ErrInvalidLogChunk
	}
	return nil
}
