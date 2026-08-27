package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

type DockerExecutor struct {
	Binary string
	Stdout io.Writer
	Stderr io.Writer
}

func NewDockerExecutor(stdout, stderr io.Writer) *DockerExecutor {
	return &DockerExecutor{Binary: "docker", Stdout: stdout, Stderr: stderr}
}

func (e *DockerExecutor) Check(ctx context.Context) error {
	command := exec.CommandContext(ctx, e.Binary, "version", "--format", "{{.Server.Version}}")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("docker daemon is unavailable: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (e *DockerExecutor) Execute(ctx context.Context, jobID uuid.UUID, spec pipeline.PlanJob) error {
	if len(spec.Services) > 0 {
		return errors.New("service containers are declared but not implemented by the milestone-0 executor")
	}
	volume := "yuanci-workspace-" + strings.ReplaceAll(jobID.String(), "-", "")
	if err := e.run(ctx, "volume", "create", volume); err != nil {
		return err
	}
	defer func() { _ = e.run(context.Background(), "volume", "rm", "-f", volume) }()

	jobCtx := ctx
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		jobCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	for index, step := range spec.Steps {
		image := step.Image
		if image == "" {
			image = spec.Image
		}
		if image == "" {
			return fmt.Errorf("step %q has no image", step.Name)
		}
		stepCtx := jobCtx
		var cancel context.CancelFunc = func() {}
		if step.Timeout != "" {
			duration, err := time.ParseDuration(step.Timeout)
			if err != nil {
				return fmt.Errorf("step %q timeout: %w", step.Name, err)
			}
			stepCtx, cancel = context.WithTimeout(jobCtx, duration)
		}
		args := buildDockerArgs(volume, jobID, index, image, spec, step)
		err := e.run(stepCtx, args...)
		cancel()
		if err != nil {
			return fmt.Errorf("step %q failed: %w", step.Name, err)
		}
	}
	return nil
}

func buildDockerArgs(volume string, jobID uuid.UUID, index int, image string, job pipeline.PlanJob, step pipeline.Step) []string {
	name := fmt.Sprintf("yuanci-%s-%d", strings.ReplaceAll(jobID.String(), "-", ""), index)
	args := []string{"run", "--rm", "--name", name, "--network", "bridge", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--pids-limit", "256", "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,size=536870912", "--volume", volume + ":/workspace",
		"--workdir", defaultString(step.WorkingDir, "/workspace"), "--env", "HOME=/tmp"}
	if job.Resources.PIDs > 0 {
		replaceArg(args, "--pids-limit", fmt.Sprint(job.Resources.PIDs))
	}
	if safeResource(job.Resources.CPU) {
		args = append(args, "--cpus", job.Resources.CPU)
	}
	if safeResource(job.Resources.Memory) {
		args = append(args, "--memory", job.Resources.Memory)
	}
	environment := make(map[string]string, len(job.Environment)+len(step.Environment))
	for key, value := range job.Environment {
		environment[key] = value
	}
	for key, value := range step.Environment {
		environment[key] = value
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--env", key+"="+environment[key])
	}
	args = append(args, image, "sh", "-euc", strings.Join(step.Commands, "\n"))
	return args
}

func (e *DockerExecutor) run(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, e.Binary, args...)
	command.Stdout = e.Stdout
	command.Stderr = e.Stderr
	if err := command.Run(); err != nil {
		return err
	}
	return nil
}

var resourcePattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:[kmgtKMGT]i?[bB]?)?$`)

func safeResource(value string) bool { return value != "" && resourcePattern.MatchString(value) }
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func replaceArg(args []string, key, value string) {
	for i := range args {
		if args[i] == key && i+1 < len(args) {
			args[i+1] = value
			return
		}
	}
}
