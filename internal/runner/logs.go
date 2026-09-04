package runner

import (
	"context"
	"io"
	"sync"

	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

// One unacknowledged chunk per Job bounds memory and backpressures both pipes.
// The Work loop is the sole transport writer; reconnect resends this same chunk.
type logBuffer struct {
	ctx          context.Context
	writeMu      sync.Mutex
	mu           sync.Mutex
	chunk        *runnerv1.LogChunk
	acknowledged chan struct{}
	wake         chan struct{}
	sequence     uint64
	total        int
	truncated    bool
}

func newLogBuffer(ctx context.Context) *logBuffer {
	return &logBuffer{ctx: ctx, wake: make(chan struct{}, 1)}
}
func (b *logBuffer) writer(step int, stderr bool) io.Writer { return logWriter{b, step, stderr} }

type logWriter struct {
	buffer *logBuffer
	step   int
	stderr bool
}

func (w logWriter) Write(p []byte) (int, error) {
	b := w.buffer
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	original := len(p)
	written := 0
	for len(p) > 0 {
		if err := b.ctx.Err(); err != nil {
			return written, err
		}
		if b.truncated {
			return original, nil
		}
		n := min(len(p), runmodel.MaxLogChunkBytes, runmodel.MaxJobLogBytes-b.total)
		marker := n == 0 || b.sequence >= runmodel.MaxJobLogChunks-1
		if marker {
			n = 0
			b.truncated = true
		}
		b.sequence++
		c := &runnerv1.LogChunk{Sequence: b.sequence, StepIndex: uint32(w.step), Stderr: w.stderr, Data: append([]byte(nil), p[:n]...), Truncated: marker}
		done := make(chan struct{})
		b.mu.Lock()
		b.chunk = c
		b.acknowledged = done
		b.mu.Unlock()
		select {
		case b.wake <- struct{}{}:
		default:
		}
		select {
		case <-done:
		case <-b.ctx.Done():
			return written, b.ctx.Err()
		}
		b.total += n
		written += n
		p = p[n:]
	}
	return original, nil
}
func (b *logBuffer) pending() *runnerv1.LogChunk { b.mu.Lock(); defer b.mu.Unlock(); return b.chunk }
func (b *logBuffer) ack(sequence uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.chunk != nil && b.chunk.Sequence == sequence {
		clear(b.chunk.Data)
		b.chunk = nil
		close(b.acknowledged)
	}
}

type logContextKey struct{}

func withJobLogs(ctx context.Context, b *logBuffer) context.Context {
	return context.WithValue(ctx, logContextKey{}, b)
}
func jobLogWriters(ctx context.Context, step int, stdout, stderr io.Writer) (io.Writer, io.Writer) {
	if b, ok := ctx.Value(logContextKey{}).(*logBuffer); ok {
		return b.writer(step, false), b.writer(step, true)
	}
	return stdout, stderr
}
