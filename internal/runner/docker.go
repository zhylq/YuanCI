package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

type DockerExecutor struct {
	DeploymentPolicy    *DeploymentPolicy
	Binary              string
	Stdout              io.Writer
	Stderr              io.Writer
	CleanupTimeout      time.Duration
	ServiceReadyTimeout time.Duration
	ServicePollInterval time.Duration
	command             func(context.Context, string, ...string) *exec.Cmd
}

func NewDockerExecutor(stdout, stderr io.Writer) *DockerExecutor {
	return &DockerExecutor{Binary: "docker", Stdout: stdout, Stderr: stderr, CleanupTimeout: 15 * time.Second,
		command: exec.CommandContext}
}

func (e *DockerExecutor) Check(ctx context.Context) error {
	command := e.commandFor(ctx, e.Binary, "version", "--format", "{{.Server.Version}}")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("docker daemon is unavailable: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (e *DockerExecutor) Execute(ctx context.Context, jobID uuid.UUID, spec pipeline.PlanJob, source *localSource) (result error) {
	if spec.Deployment != "" {
		if err := e.DeploymentPolicy.authorize(source); err != nil {
			return err
		}
	}
	values := checkoutRedactionValues(source)
	defer func() {
		for _, value := range values {
			clear(value)
		}
	}()
	stdout, err := newRedactingWriter(e.Stdout, values)
	if err != nil {
		return err
	}
	stderr, err := newRedactingWriter(e.Stderr, values)
	if err != nil {
		stdout.destroy()
		return err
	}
	defer func() { result = errors.Join(result, stdout.Close(), stderr.Close()) }()
	local := *e
	e = &local
	e.Stdout, e.Stderr = stdout, stderr
	if source != nil {
		defer clear(source.credential)
	}
	if err := pipeline.ValidateServices(spec.Services); err != nil {
		return fmt.Errorf("invalid service configuration: %w", err)
	}
	jobCtx := ctx
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		jobCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	if err := jobCtx.Err(); err != nil {
		return err
	}
	volume, network := dockerResourceNames(jobID)
	if spec.Deployment != "" {
		defer func() {
			result = errors.Join(result, e.cleanupDeployment(jobID, len(spec.Steps), volume, network, len(spec.Services)))
		}()
	}
	if err := e.run(jobCtx, "volume", "create", volume); err != nil {
		return err
	}
	if spec.Deployment == "" {
		defer e.cleanup(jobID, len(spec.Steps), volume, network, len(spec.Services))
	}
	if err := e.run(jobCtx, "network", "create", "--driver", "bridge", network); err != nil {
		return err
	}
	if source != nil {
		if err := e.checkout(jobCtx, jobID, volume, network, source); err != nil {
			_, failureLog := jobLogWriters(jobCtx, 0, e.Stdout, e.Stderr)
			_, _ = io.WriteString(failureLog, "[checkout] Source checkout failed before user steps.\n")
			return fmt.Errorf("source checkout failed: %w", err)
		}
	}
	if err := e.startServices(jobCtx, jobID, network, spec); err != nil {
		_, failureLog := jobLogWriters(jobCtx, 0, e.Stdout, e.Stderr)
		_, _ = fmt.Fprintf(failureLog, "[services] Startup failed: %v\n", err)
		return err
	}
	for index, step := range spec.Steps {
		if err := jobCtx.Err(); err != nil {
			return err
		}
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
		var mountArgs []string
		if spec.Deployment != "" {
			mountArgs = e.DeploymentPolicy.dockerMountArgs()
		}
		args := buildDockerArgs(volume, network, jobID, index, image, spec, step, mountArgs...)
		command := e.commandFor(stepCtx, e.Binary, args...)
		out, errOut := jobLogWriters(stepCtx, index, e.Stdout, e.Stderr)
		stepOut, _ := newRedactingWriter(out, values)
		stepErr, _ := newRedactingWriter(errOut, values)
		command.Stdout, command.Stderr = stepOut, stepErr
		err := command.Run()
		err = errors.Join(err, stepOut.Close(), stepErr.Close())
		cancel()
		if err != nil {
			return fmt.Errorf("step %q failed: %w", step.Name, err)
		}
	}
	return nil
}

func (e *DockerExecutor) checkout(ctx context.Context, jobID uuid.UUID, volume, network string, source *localSource) error {
	commandSpec, input, err := buildCheckoutCommand(volume, network, dockerCheckoutContainerName(jobID), source)
	if err != nil {
		return err
	}
	defer clear(input)
	command := e.commandFor(ctx, e.Binary, commandSpec.args...)
	command.Stdin = bytes.NewReader(input)
	out, errOut := jobLogWriters(ctx, 0, e.Stdout, e.Stderr)
	if _, ok := ctx.Value(logContextKey{}).(*logBuffer); ok {
		out = io.MultiWriter(out, e.Stdout)
		errOut = io.MultiWriter(errOut, e.Stderr)
	}
	values := checkoutRedactionValues(source)
	defer func() {
		for _, value := range values {
			clear(value)
		}
	}()
	stdout, err := newRedactingWriter(out, values)
	if err != nil {
		return err
	}
	stderr, err := newRedactingWriter(errOut, values)
	if err != nil {
		stdout.destroy()
		return err
	}
	command.Stdout, command.Stderr = stdout, stderr
	_, _ = io.WriteString(stdout, "[checkout] Fetching source at assigned commit.\n")
	return errors.Join(command.Run(), stdout.Close(), stderr.Close())
}

func buildDockerArgs(volume, network string, jobID uuid.UUID, index int, image string, job pipeline.PlanJob, step pipeline.Step, administratorMountArgs ...string) []string {
	name := dockerContainerName(jobID, index)
	args := []string{"run", "--rm", "--name", name, "--network", network, "--log-driver", "none", "--cap-drop", "ALL",
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
		args = append(args, "--memory", dockerMemory(job.Resources.Memory))
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
	args = append(args, administratorMountArgs...)
	args = append(args, "--entrypoint", "sh", image, "-euc", strings.Join(step.Commands, "\n"))
	return args
}

func (e *DockerExecutor) run(ctx context.Context, args ...string) error {
	command := e.commandFor(ctx, e.Binary, args...)
	command.Stdout = e.Stdout
	command.Stderr = e.Stderr
	if err := command.Run(); err != nil {
		return err
	}
	return nil
}

func (e *DockerExecutor) cleanup(jobID uuid.UUID, steps int, volume, network string, services int) {
	timeout := e.CleanupTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	containerArgs := []string{"container", "rm", "-f", dockerCheckoutContainerName(jobID)}
	for index := 0; index < steps; index++ {
		containerArgs = append(containerArgs, dockerContainerName(jobID, index))
	}
	e.runQuiet(ctx, containerArgs...)
	if services > 0 {
		serviceArgs := []string{"container", "rm", "-f", "-v"}
		for index := 0; index < services; index++ {
			serviceArgs = append(serviceArgs, dockerServiceContainerName(jobID, index))
		}
		e.runQuiet(ctx, serviceArgs...)
	}
	var cleanup sync.WaitGroup
	cleanup.Add(2)
	go func() {
		defer cleanup.Done()
		e.runQuiet(ctx, "network", "rm", network)
	}()
	go func() {
		defer cleanup.Done()
		e.runQuiet(ctx, "volume", "rm", "-f", volume)
	}()
	cleanup.Wait()
}

func (e *DockerExecutor) runQuiet(ctx context.Context, args ...string) {
	command := e.commandFor(ctx, e.Binary, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	_ = command.Run()
}

// ErrDeploymentCleanupUnconfirmed keeps the deployment queue closed when a
// daemon failure prevents verifying that all job resources are absent.
var ErrDeploymentCleanupUnconfirmed = errors.New("deployment cleanup could not be confirmed")

func (e *DockerExecutor) cleanupDeployment(jobID uuid.UUID, steps int, volume, network string, services int) error {
	timeout := e.CleanupTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	names := []string{dockerCheckoutContainerName(jobID)}
	for i := 0; i < steps; i++ {
		names = append(names, dockerContainerName(jobID, i))
	}
	for i := 0; i < services; i++ {
		names = append(names, dockerServiceContainerName(jobID, i))
	}
	// Give user shell traps a bounded opportunity before forcing removal.
	stopCtx, stopCancel := context.WithTimeout(ctx, 5*time.Second)
	e.runQuiet(stopCtx, append([]string{"container", "stop", "--time", "3"}, names...)...)
	stopCancel()
	e.runQuiet(ctx, append([]string{"container", "rm", "-f", "-v"}, names...)...)
	e.runQuiet(ctx, "network", "rm", network)
	e.runQuiet(ctx, "volume", "rm", "-f", volume)
	for _, name := range names {
		if !e.resourceAbsent(ctx, []string{"container", "ls", "--all", "--filter", "name=^/" + name + "$", "--format", "{{.Names}}"}, name) {
			return ErrDeploymentCleanupUnconfirmed
		}
	}
	if !e.resourceAbsent(ctx, []string{"network", "ls", "--filter", "name=^" + network + "$", "--format", "{{.Name}}"}, network) ||
		!e.resourceAbsent(ctx, []string{"volume", "ls", "--filter", "name=^" + volume + "$", "--format", "{{.Name}}"}, volume) {
		return ErrDeploymentCleanupUnconfirmed
	}
	return nil
}

func (e *DockerExecutor) resourceAbsent(ctx context.Context, args []string, name string) bool {
	output, err := e.commandFor(ctx, e.Binary, args...).Output()
	if err != nil || ctx.Err() != nil {
		return false
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == name {
			return false
		}
	}
	return true
}

func (e *DockerExecutor) commandFor(ctx context.Context, name string, args ...string) *exec.Cmd {
	if e.command != nil {
		return e.command(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

func dockerResourceNames(jobID uuid.UUID) (string, string) {
	suffix := strings.ReplaceAll(jobID.String(), "-", "")
	return "yuanci-workspace-" + suffix, "yuanci-network-" + suffix
}

func dockerContainerName(jobID uuid.UUID, index int) string {
	return fmt.Sprintf("yuanci-%s-%d", strings.ReplaceAll(jobID.String(), "-", ""), index)
}

func dockerCheckoutContainerName(jobID uuid.UUID) string {
	return fmt.Sprintf("yuanci-%s-checkout", strings.ReplaceAll(jobID.String(), "-", ""))
}

var resourcePattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:[kmgtKMGT]i?[bB]?)?$`)

func safeResource(value string) bool { return value != "" && resourcePattern.MatchString(value) }

// Docker's memory units are binary but do not accept the IEC "i" marker.
func dockerMemory(value string) string { return strings.ReplaceAll(value, "i", "") }
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
