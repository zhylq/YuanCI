package runner

import (
	"bytes"
	"context"
	"errors"
	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/run/storetest"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	runmodel "github.com/yuanci/yuanci/internal/run"
)

func TestLogBufferBackpressureReplayAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := newLogBuffer(ctx)
	done := make(chan error, 1)
	go func() { _, err := b.writer(2, false).Write([]byte("hello")); done <- err }()
	first := awaitLog(t, b)
	if first.Sequence != 1 || string(first.Data) != "hello" || first.StepIndex != 2 {
		t.Fatalf("bad chunk: %v", first)
	}
	select {
	case <-done:
		t.Fatal("writer did not wait for persistence")
	default:
	}
	if again := b.pending(); again != first {
		t.Fatal("reconnect changed pending chunk")
	}
	b.ack(1)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() { _, err := b.writer(2, true).Write([]byte("stderr")); done <- err }()
	second := awaitLog(t, b)
	if second.Sequence != 2 || !second.Stderr {
		t.Fatal("order/stream")
	}
	b.ack(1)
	select {
	case <-done:
		t.Fatal("old ack released next chunk")
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestLogBufferSplittingAndTruncation(t *testing.T) {
	b := newLogBuffer(t.Context())
	b.total = runmodel.MaxJobLogBytes - runmodel.MaxLogChunkBytes
	done := make(chan error, 1)
	go func() {
		_, err := b.writer(0, false).Write(bytes.Repeat([]byte("x"), runmodel.MaxLogChunkBytes+1))
		done <- err
	}()
	c := awaitLog(t, b)
	if len(c.Data) != runmodel.MaxLogChunkBytes {
		t.Fatal("chunk bound")
	}
	b.ack(c.Sequence)
	c = awaitLog(t, b)
	if !c.Truncated || len(c.Data) != 0 {
		t.Fatal("missing marker")
	}
	b.ack(c.Sequence)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n, err := b.writer(0, false).Write([]byte("ignored")); err != nil || n != 7 {
		t.Fatal("truncation must drain output")
	}
	if b.pending() != nil {
		t.Fatal("emitted beyond marker")
	}
}

func awaitLog(t *testing.T, b *logBuffer) *runnerv1.LogChunk {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		if c := b.pending(); c != nil {
			return c
		}
		select {
		case <-b.wake:
		case <-deadline:
			t.Fatal("missing chunk")
			return nil
		}
	}
}

type reconnectLogStore struct {
	*runmodel.MemoryStore
	mu     sync.Mutex
	chunks []runmodel.LogChunk
}

func (s *reconnectLogStore) AppendLogChunk(_ context.Context, c runmodel.LogChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Data = append([]byte(nil), c.Data...)
	s.chunks = append(s.chunks, c)
	if len(s.chunks) == 1 {
		return errors.New("injected lost acknowledgement")
	}
	return nil
}
func TestLogTransportReconnectCompletesAfterAcknowledgement(t *testing.T) {
	var logs *reconnectLogStore
	fixture := newEnrollmentFixture(t, func(m *runmodel.MemoryStore) runmodel.RunnerJobStore {
		logs = &reconnectLogStore{MemoryStore: m}
		return logs
	})
	caps := credentialCapabilities()
	credentials, err := LoadOrEnroll(t.Context(), EnrollmentConfig{Address: fixture.address, ServerName: "server", RootCAFile: fixture.rootFile, StateDir: filepath.Join(t.TempDir(), "runner"), Token: fixture.token, Name: "logs", Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	record := storetest.RunnerRecord(t, 1, pipeline.RunnerRequirements{OS: "linux", Architecture: "amd64", Executor: "docker", Labels: map[string]string{"region": "test"}}, "1MiB")
	if _, err := fixture.jobs.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	client, err := NewWorkClient(WorkConfig{Address: fixture.address, ServerName: "server", Credentials: credentials, Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx, executorFunc(func(ctx context.Context, _ uuid.UUID, _ pipeline.PlanJob) error {
			out, stderr := jobLogWriters(ctx, 0, io.Discard, io.Discard)
			if _, err := out.Write([]byte("stdout")); err != nil {
				return err
			}
			_, err := stderr.Write([]byte("stderr"))
			return err
		}))
	}()
	for {
		runs, err := fixture.jobs.List(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if runs[0].Status == runmodel.StatusSucceeded {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("completion did not converge")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if len(logs.chunks) != 3 || logs.chunks[0].Sequence != 1 || logs.chunks[1].Sequence != 1 || logs.chunks[2].Sequence != 2 || !bytes.Equal(logs.chunks[0].Data, logs.chunks[1].Data) || string(logs.chunks[2].Data) != "stderr" {
		t.Fatalf("bad replay: %+v", logs.chunks)
	}
}
