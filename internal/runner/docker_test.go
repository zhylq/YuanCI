package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

func TestDockerArgsApplySecurityDefaults(t *testing.T) {
	args := buildDockerArgs("workspace", "network", uuid.MustParse("a120348a-b47b-4e92-91a4-5d2e266dc680"), 0, "alpine:3.21",
		pipeline.PlanJob{Resources: pipeline.Resources{CPU: "2", Memory: "1Gi"}},
		pipeline.Step{Name: "test", Commands: []string{"echo ok"}})
	joined := strings.Join(args, " ")
	for _, expected := range []string{"--network network", "--cap-drop ALL", "no-new-privileges", "--read-only", "--pids-limit 256", "--cpus 2", "--memory 1Gi"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("expected %q in %q", expected, joined)
		}
	}
}

func TestDockerCancellationRunsBoundedDeterministicCleanup(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "docker-calls.log")
	executor := NewDockerExecutor(os.Stdout, os.Stderr)
	executor.CleanupTimeout = 5 * time.Second
	executor.command = dockerHelperCommandWith(t, logFile)
	jobID := uuid.MustParse("a120348a-b47b-4e92-91a4-5d2e266dc680")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- executor.Execute(ctx, jobID, pipeline.PlanJob{Name: "cancel", Image: "alpine:3.21",
			Steps: []pipeline.Step{{Name: "wait", Commands: []string{"sleep 30"}}}}, nil)
	}()
	waitForDockerCall(t, logFile, "run --rm --name", 5*time.Second)
	cancel()
	var executionErr error
	select {
	case executionErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled Docker execution did not return")
	}
	if executionErr == nil || ctx.Err() != context.Canceled {
		t.Fatalf("canceled Docker command returned %v", executionErr)
	}
	body, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(body)
	for _, expected := range []string{
		"volume create yuanci-workspace-a120348ab47b4e9291a45d2e266dc680",
		"network create --driver bridge yuanci-network-a120348ab47b4e9291a45d2e266dc680",
		"run --rm --name yuanci-a120348ab47b4e9291a45d2e266dc680-0",
		"container rm -f yuanci-a120348ab47b4e9291a45d2e266dc680-checkout yuanci-a120348ab47b4e9291a45d2e266dc680-0",
		"network rm yuanci-network-a120348ab47b4e9291a45d2e266dc680",
		"volume rm -f yuanci-workspace-a120348ab47b4e9291a45d2e266dc680",
	} {
		if !strings.Contains(calls, expected) {
			t.Errorf("cleanup call %q missing from:\n%s", expected, calls)
		}
	}
}

func TestDockerCheckoutCompletesBeforeUserStepsAndClearsCredential(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "docker-calls.log")
	executor := NewDockerExecutor(os.Stdout, os.Stderr)
	executor.command = dockerHelperCommand(t, logFile)
	jobID := uuid.MustParse("a120348a-b47b-4e92-91a4-5d2e266dc680")
	source := &localSource{cloneURL: "https://github.com/example/repository.git",
		commitSHA: strings.Repeat("a", 40), credential: []byte("checkout-secret")}
	if err := executor.Execute(t.Context(), jobID, pipeline.PlanJob{Name: "build", Image: "alpine:3.21",
		Steps: []pipeline.Step{{Name: "test", Commands: []string{"true"}}}}, source); err != nil {
		t.Fatal(err)
	}
	if !allZero(source.credential) {
		t.Fatal("checkout credential was retained after helper exit")
	}
	body, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(body)
	checkout := strings.Index(calls, "-checkout")
	step := strings.Index(calls, "-0 --network")
	if checkout < 0 || step < 0 || checkout >= step {
		t.Fatalf("checkout did not precede the user step:\n%s", calls)
	}
	if !strings.Contains(calls, "rev-parse HEAD") {
		t.Fatal("checkout helper did not verify HEAD")
	}
}

func TestDockerCheckoutMismatchStopsStepsAndCleansResources(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "docker-calls.log")
	executor := NewDockerExecutor(os.Stdout, os.Stderr)
	executor.command = dockerHelperCommandWith(t, logFile, "DOCKER_HELPER_FAIL_CHECKOUT=1")
	jobID := uuid.MustParse("a120348a-b47b-4e92-91a4-5d2e266dc680")
	source := &localSource{cloneURL: "https://github.com/example/repository.git",
		commitSHA: strings.Repeat("a", 40), credential: []byte("checkout-secret")}
	err := executor.Execute(t.Context(), jobID, pipeline.PlanJob{Name: "build", Image: "alpine:3.21",
		Steps: []pipeline.Step{{Name: "must-not-run", Commands: []string{"false"}}}}, source)
	if err == nil || !strings.Contains(err.Error(), "source checkout failed") {
		t.Fatalf("checkout mismatch returned %v", err)
	}
	body, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	calls := string(body)
	if strings.Contains(calls, "-0 --network") {
		t.Fatalf("user step ran after checkout mismatch:\n%s", calls)
	}
	for _, expected := range []string{"network rm", "volume rm -f"} {
		if !strings.Contains(calls, expected) {
			t.Fatalf("cleanup %q missing after mismatch:\n%s", expected, calls)
		}
	}
}

func waitForDockerCall(t *testing.T, logFile, expected string, maximum time.Duration) {
	t.Helper()
	deadline := time.Now().Add(maximum)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(logFile)
		if strings.Contains(string(body), expected) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Docker helper did not record %q", expected)
}

func TestDockerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DOCKER_HELPER") != "1" {
		return
	}
	separator := 0
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index + 1
			break
		}
	}
	arguments := os.Args[separator:]
	file, err := os.OpenFile(os.Getenv("DOCKER_HELPER_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = file.WriteString(strings.Join(arguments[1:], " ") + "\n")
	_ = file.Close()
	joined := strings.Join(arguments[1:], " ")
	if strings.Contains(joined, "-checkout") && os.Getenv("DOCKER_HELPER_FAIL_CHECKOUT") == "1" {
		os.Exit(42)
	}
	if len(arguments) > 1 && arguments[1] == "run" && !strings.Contains(joined, "-checkout") &&
		os.Getenv("DOCKER_HELPER_NO_SLEEP") != "1" {
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func dockerHelperCommand(t *testing.T, logFile string) func(context.Context, string, ...string) *exec.Cmd {
	return dockerHelperCommandWith(t, logFile, "DOCKER_HELPER_NO_SLEEP=1")
}

func dockerHelperCommandWith(t *testing.T, logFile string, extra ...string) func(context.Context, string, ...string) *exec.Cmd {
	t.Helper()
	return func(ctx context.Context, name string, arguments ...string) *exec.Cmd {
		args := append([]string{"-test.run=TestDockerHelperProcess", "--", name}, arguments...)
		command := exec.CommandContext(ctx, os.Args[0], args...)
		command.Env = append(os.Environ(), append([]string{"GO_WANT_DOCKER_HELPER=1", "DOCKER_HELPER_LOG=" + logFile}, extra...)...)
		return command
	}
}
