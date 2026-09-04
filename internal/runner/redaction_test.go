package runner

import (
	"bytes"
	"context"
	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactionSplitBoundariesAndFlush(t *testing.T) {
	for split := 0; split <= len("before super-secret after"); split++ {
		var out bytes.Buffer
		w, err := newRedactingWriter(&out, [][]byte{[]byte("super-secret"), []byte("super")})
		if err != nil {
			t.Fatal(err)
		}
		input := []byte("before super-secret after")
		if _, err = w.Write(input[:split]); err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(input[split:]); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != "before [REDACTED] after" {
			t.Fatalf("split %d: %q", split, got)
		}
	}
	var out bytes.Buffer
	w, _ := newRedactingWriter(&out, [][]byte{[]byte("super-secret")})
	_, _ = w.Write([]byte("prefix super-sec"))
	_ = w.Close()
	if out.String() != "prefix [REDACTED]" {
		t.Fatal("partial credential was flushed")
	}
}

func TestDockerRedactsBeforeTransportAndLocalOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	executor := NewDockerExecutor(&stdout, &stderr)
	path := filepath.Join(t.TempDir(), "calls")
	executor.command = dockerHelperCommandWith(t, path, "DOCKER_HELPER_NO_SLEEP=1", "DOCKER_HELPER_REDACTION=1")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	logs := newLogBuffer(ctx)
	var transmitted bytes.Buffer
	done := make(chan error, 1)
	source := &localSource{cloneURL: "https://github.com/example/repository.git", commitSHA: strings.Repeat("a", 40), credential: []byte("synthetic-log-secret")}
	go func() {
		done <- executor.Execute(withJobLogs(ctx, logs), uuid.New(), pipeline.PlanJob{Name: "logs", Image: "alpine", Steps: []pipeline.Step{{Name: "test", Commands: []string{"echo test"}}}}, source)
	}()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			for _, output := range []string{stdout.String(), stderr.String(), transmitted.String()} {
				if strings.Contains(output, "synthetic-log-secret") {
					t.Fatal("credential leaked")
				}
			}
			if !strings.Contains(transmitted.String(), "[REDACTED]") || !strings.Contains(stdout.String(), "[REDACTED]") {
				t.Fatal("missing protected outputs")
			}
			calls, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(calls), "--log-driver none") != 2 {
				t.Fatal("Docker raw logging enabled")
			}
			return
		case <-ctx.Done():
			t.Fatal("log pipeline stalled")
		case <-logs.wake:
			if c := logs.pending(); c != nil {
				transmitted.Write(c.Data)
				logs.ack(c.Sequence)
			}
		}
	}
}

func FuzzRedactionChunkBoundaries(f *testing.F) {
	f.Add([]byte("before token-1234 after token-1234"), uint8(3))
	f.Add([]byte("token-1234token-1234"), uint8(1))
	f.Fuzz(func(t *testing.T, input []byte, width uint8) {
		if len(input) > 65536 {
			t.Skip()
		}
		var whole, split bytes.Buffer
		a, _ := newRedactingWriter(&whole, [][]byte{[]byte("token-1234")})
		b, _ := newRedactingWriter(&split, [][]byte{[]byte("token-1234")})
		_, _ = a.Write(input)
		_ = a.Close()
		size := int(width) + 1
		for len(input) > 0 {
			n := min(size, len(input))
			_, _ = b.Write(input[:n])
			input = input[n:]
			if len(b.pending) > 4096 {
				t.Fatal("unbounded tail")
			}
		}
		_ = b.Close()
		if !bytes.Equal(whole.Bytes(), split.Bytes()) || bytes.Contains(split.Bytes(), []byte("token-1234")) {
			t.Fatal("split mismatch or leaked secret")
		}
	})
}
