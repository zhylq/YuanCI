package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

func dockerServiceContainerName(jobID uuid.UUID, index int) string {
	return fmt.Sprintf("yuanci-%s-service-%d", strings.ReplaceAll(jobID.String(), "-", ""), index)
}

func buildServiceArgs(network string, jobID uuid.UUID, index int, service pipeline.Service, resources pipeline.Resources) []string {
	args := []string{"create", "--name", dockerServiceContainerName(jobID, index),
		"--network", network, "--network-alias", service.Name, "--log-driver", "none",
		"--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE",
		"--cap-add", "FOWNER", "--cap-add", "SETUID", "--cap-add", "SETGID",
		"--security-opt", "no-new-privileges", "--pids-limit", "256"}
	if resources.PIDs > 0 {
		replaceArg(args, "--pids-limit", fmt.Sprint(resources.PIDs))
	}
	if safeResource(resources.CPU) {
		args = append(args, "--cpus", resources.CPU)
	}
	if safeResource(resources.Memory) {
		args = append(args, "--memory", resources.Memory)
	}
	keys := make([]string, 0, len(service.Environment))
	for key := range service.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--env", key+"="+service.Environment[key])
	}
	return append(args, service.Image)
}

func (e *DockerExecutor) startServices(ctx context.Context, jobID uuid.UUID, network string, job pipeline.PlanJob) error {
	if len(job.Services) == 0 {
		return nil
	}
	// Create every deterministic name before waiting, so services may depend on
	// each other during startup. Deferred Job cleanup covers partial creation.
	for index, service := range job.Services {
		cmd := e.commandFor(ctx, e.Binary, buildServiceArgs(network, jobID, index, service, job.Resources)...)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("service %q creation failed: %w", service.Name, err)
		}
	}
	for index, service := range job.Services {
		cmd := e.commandFor(ctx, e.Binary, "container", "start", dockerServiceContainerName(jobID, index))
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("service %q startup failed: %w", service.Name, err)
		}
	}
	timeout := e.ServiceReadyTimeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for index, service := range job.Services {
		if err := e.waitForService(readyCtx, dockerServiceContainerName(jobID, index)); err != nil {
			return fmt.Errorf("service %q readiness failed: %w", service.Name, err)
		}
	}
	return nil
}

// Inspect only health status, never health output, configuration or environment.
const serviceStateFormat = `{"status":{{json .State.Status}},"running":{{json .State.Running}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}}}`

func (e *DockerExecutor) waitForService(ctx context.Context, name string) error {
	interval := e.ServicePollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var output boundedServiceOutput
		cmd := e.commandFor(ctx, e.Binary, "container", "inspect", "--format", serviceStateFormat, name)
		cmd.Stdout, cmd.Stderr = &output, io.Discard
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("cannot inspect container: %w", err)
		}
		var state struct {
			Status  string `json:"status"`
			Running bool   `json:"running"`
			Health  string `json:"health"`
		}
		if output.overflow || json.Unmarshal(output.Bytes(), &state) != nil {
			return errors.New("invalid container state")
		}
		if !state.Running || state.Status != "running" {
			return errors.New("container stopped before readiness")
		}
		switch state.Health {
		case "", "healthy":
			return nil
		case "starting":
		case "unhealthy":
			return errors.New("container is unhealthy")
		default:
			return errors.New("invalid health status")
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Drain excessive command output without retaining it or blocking the subprocess.
type boundedServiceOutput struct {
	data     bytes.Buffer
	overflow bool
}

func (b *boundedServiceOutput) Bytes() []byte { return b.data.Bytes() }

func (b *boundedServiceOutput) Write(p []byte) (int, error) {
	remaining := 4096 - b.data.Len()
	if len(p) > remaining {
		b.overflow = true
		_, _ = b.data.Write(p[:remaining])
	} else {
		_, _ = b.data.Write(p)
	}
	return len(p), nil
}
