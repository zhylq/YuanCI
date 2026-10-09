package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

func TestDeploymentDockerDeniesBeforeAnyMutationAndOnlyMountsApprovedSteps(t *testing.T) {
	file := filepath.Join(t.TempDir(), "calls")
	executor := NewDockerExecutor(io.Discard, io.Discard)
	executor.command = dockerHelperCommand(t, file)
	source := &localSource{provider: "gitee", repositoryID: "70", cloneURL: "https://gitee.com/team/repository.git", commitSHA: strings.Repeat("a", 40), credential: []byte("token")}
	plan := pipeline.PlanJob{Name: "deploy", Deployment: "production", Image: "alpine", Steps: []pipeline.Step{{Name: "commands", Commands: []string{"echo chosen-command"}}}}
	if err := executor.Execute(t.Context(), uuid.New(), plan, source); err == nil {
		t.Fatal("missing policy accepted")
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Docker called before policy authorization")
	}
	executor.DeploymentPolicy = &DeploymentPolicy{repositories: map[string]bool{pipeline.DeploymentRepositoryLabel("gitee", "70"): true}, mounts: []DeploymentMount{{Source: t.TempDir(), Target: "/release", ReadOnly: true}}}
	if err := executor.Execute(t.Context(), uuid.New(), plan, source); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(file)
	if strings.Count(string(calls), "type=bind,") != 1 || !strings.Contains(string(calls), "dst=/release,readonly") || !strings.Contains(string(calls), "--entrypoint sh") || !strings.Contains(string(calls), "alpine -euc echo chosen-command") {
		t.Fatalf("deployment step mount or command mismatch: %s", calls)
	}
	plan.Deployment = ""
	before := len(calls)
	if err := executor.Execute(t.Context(), uuid.New(), plan, nil); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(file)
	if strings.Contains(string(calls[before:]), "type=bind,") {
		t.Fatal("standard job received deployment mounts")
	}
}

func TestDeploymentCleanupConfirmsAbsenceAndFailsClosedOnDaemonFailure(t *testing.T) {
	for _, failure := range []string{"", "DOCKER_HELPER_FAIL_QUERY=1", "DOCKER_HELPER_RESOURCE_REMAINS=1"} {
		t.Run(failure, func(t *testing.T) {
			executor := NewDockerExecutor(io.Discard, io.Discard)
			executor.command = dockerHelperCommandWith(t, filepath.Join(t.TempDir(), "calls"), "DOCKER_HELPER_NO_SLEEP=1", failure)
			id := uuid.New()
			volume, network := dockerResourceNames(id)
			err := executor.cleanupDeployment(id, 1, volume, network, 0)
			if errors.Is(err, ErrDeploymentCleanupUnconfirmed) != (failure != "") {
				t.Fatalf("cleanup: %v", err)
			}
		})
	}
}

func TestDeploymentCleanupFailurePropagatesThroughExecutor(t *testing.T) {
	executor := NewDockerExecutor(io.Discard, io.Discard)
	executor.command = dockerHelperCommandWith(t, filepath.Join(t.TempDir(), "calls"), "DOCKER_HELPER_NO_SLEEP=1", "DOCKER_HELPER_FAIL_QUERY=1")
	executor.DeploymentPolicy = &DeploymentPolicy{repositories: map[string]bool{pipeline.DeploymentRepositoryLabel("gitee", "70"): true}}
	source := &localSource{provider: "gitee", repositoryID: "70", cloneURL: "https://gitee.com/team/repository.git", commitSHA: strings.Repeat("a", 40), credential: []byte("token")}
	err := executor.Execute(t.Context(), uuid.New(), pipeline.PlanJob{Name: "deploy", Deployment: "production", Image: "alpine", Steps: []pipeline.Step{{Commands: []string{"true"}}}}, source)
	if !errors.Is(err, ErrDeploymentCleanupUnconfirmed) {
		t.Fatalf("cleanup error not propagated: %v", err)
	}
}

func TestDeploymentCommandEntrypointIntegration(t *testing.T) {
	if os.Getenv("YUANCI_TEST_DOCKER") != "1" {
		t.Skip("set YUANCI_TEST_DOCKER=1 with a dedicated Docker test daemon")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	tag := "yuanci-entrypoint-test:" + uuid.NewString()
	build := exec.CommandContext(ctx, "docker", "build", "--pull=false", "-t", tag, "-")
	build.Stdin = strings.NewReader("FROM alpine:3.21\nENTRYPOINT [\"/bin/false\"]\n")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, out)
	}
	defer exec.Command("docker", "image", "rm", tag).Run()
	var output bytes.Buffer
	executor := NewDockerExecutor(&output, &output)
	executor.DeploymentPolicy = &DeploymentPolicy{repositories: map[string]bool{pipeline.DeploymentRepositoryLabel("gitee", "70"): true}}
	// Source identity is still required; omit checkout by calling the step args
	// directly, then exercise verified resource cleanup on these same resources.
	id := uuid.New()
	volume, network := dockerResourceNames(id)
	defer func() {
		if err := executor.cleanupDeployment(id, 1, volume, network, 0); err != nil {
			t.Error(err)
		}
	}()
	if err := executor.run(ctx, "volume", "create", volume); err != nil {
		t.Fatal(err)
	}
	if err := executor.run(ctx, "network", "create", network); err != nil {
		t.Fatal(err)
	}
	args := buildDockerArgs(volume, network, id, 0, tag, pipeline.PlanJob{Deployment: "production"}, pipeline.Step{Commands: []string{"printf 'only-user-command-ran\\n'"}})
	if err := executor.run(ctx, args...); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "only-user-command-ran") {
		t.Fatal("ENTRYPOINT prevented user commands")
	}
}
