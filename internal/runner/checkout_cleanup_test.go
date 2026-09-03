package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

func TestLeaseLossCleansCheckoutResourcesAndCredential(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "docker-calls.log")
	var output bytes.Buffer
	executor := NewDockerExecutor(&output, &output)
	executor.CleanupTimeout = 5 * time.Second
	executor.command = dockerHelperCommandWith(t, logFile)
	jobID := uuid.MustParse("a120348a-b47b-4e92-91a4-5d2e266dc680")
	token := "lease-loss-checkout-secret"
	source := &localSource{cloneURL: "https://github.com/example/repository.git",
		commitSHA: strings.Repeat("a", 40), credential: []byte(token)}
	ctx, cancel := context.WithCancel(t.Context())
	job := &localJob{id: jobID, ctx: ctx, cancel: cancel, leaseExpires: time.Now().Add(time.Minute),
		plan: pipeline.PlanJob{Name: "lease-loss", Image: "alpine:3.21",
			Steps: []pipeline.Step{{Name: "wait", Commands: []string{"sleep 30"}}}}, source: source}
	losses := make(chan uuid.UUID, 1)
	if !armLeaseDeadline(job, losses) {
		t.Fatal("lease deadline was not armed")
	}
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, jobID, job.plan, source) }()
	waitForDockerCall(t, logFile, "-0 --network", 5*time.Second)
	loseLease(job, losses)
	select {
	case lost := <-losses:
		if lost != jobID {
			t.Fatalf("wrong lease was lost: %s", lost)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lease loss did not cancel checkout execution")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lease-lost execution succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled executor did not return")
	}
	assertSecureCheckoutCleanup(t, logFile, output.String(), token, source)
}

func TestStepProcessCrashCleansCheckoutResourcesAndCredential(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "docker-calls.log")
	var output bytes.Buffer
	executor := NewDockerExecutor(&output, &output)
	executor.command = dockerHelperCommandWith(t, logFile, "DOCKER_HELPER_FAIL_STEP=1")
	jobID := uuid.MustParse("a120348a-b47b-4e92-91a4-5d2e266dc680")
	token := "process-crash-checkout-secret"
	source := &localSource{cloneURL: "https://github.com/example/repository.git",
		commitSHA: strings.Repeat("a", 40), credential: []byte(token)}
	err := executor.Execute(t.Context(), jobID, pipeline.PlanJob{Name: "crash", Image: "alpine:3.21",
		Steps: []pipeline.Step{{Name: "crash", Commands: []string{"exit 1"}}}}, source)
	if err == nil || !strings.Contains(err.Error(), "step") {
		t.Fatalf("process crash returned %v", err)
	}
	assertSecureCheckoutCleanup(t, logFile, output.String(), token, source)
}

func assertSecureCheckoutCleanup(t *testing.T, logFile, output, token string, source *localSource) {
	t.Helper()
	body, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(body)
	for _, expected := range []string{
		"container rm -f yuanci-a120348ab47b4e9291a45d2e266dc680-checkout yuanci-a120348ab47b4e9291a45d2e266dc680-0",
		"network rm yuanci-network-a120348ab47b4e9291a45d2e266dc680",
		"volume rm -f yuanci-workspace-a120348ab47b4e9291a45d2e266dc680",
	} {
		if !strings.Contains(calls, expected) {
			t.Fatalf("cleanup %q missing:\n%s", expected, calls)
		}
	}
	if strings.Contains(calls, token) || strings.Contains(output, token) {
		t.Fatal("credential leaked into process metadata or output")
	}
	if !allZero(source.credential) {
		t.Fatal("credential buffer was not cleared")
	}
}
