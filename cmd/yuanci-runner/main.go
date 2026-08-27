package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yuanci/yuanci/internal/config"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/runner"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadRunner()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	client, err := runner.NewClient(cfg.ServerURL, cfg.Token)
	if err != nil {
		logger.Error("create runner client", "error", err)
		os.Exit(2)
	}
	executor := runner.NewDockerExecutor(os.Stdout, os.Stderr)
	checkCtx, checkDone := context.WithTimeout(context.Background(), 10*time.Second)
	if err := executor.Check(checkCtx); err != nil {
		checkDone()
		logger.Error("executor check failed", "error", err)
		os.Exit(1)
	}
	checkDone()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Info("runner started", "name", cfg.Name, "capacity", cfg.Capacity)
	semaphore := make(chan struct{}, cfg.Capacity)
	ticker := time.NewTicker(cfg.PollPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("runner stopping")
			return
		case <-ticker.C:
			if len(semaphore) >= cap(semaphore) {
				continue
			}
			assignment, err := client.Claim(ctx, runmodel.ClaimRequest{RunnerName: cfg.Name, Labels: cfg.Labels})
			if err != nil {
				logger.Warn("claim failed", "error", err)
				continue
			}
			if assignment == nil {
				continue
			}
			semaphore <- struct{}{}
			go func() {
				defer func() { <-semaphore }()
				runJob(ctx, logger, client, executor, assignment)
			}()
		}
	}
}

func runJob(ctx context.Context, logger *slog.Logger, client *runner.Client, executor *runner.DockerExecutor, assignment *runmodel.Assignment) {
	jobLogger := logger.With("run_id", assignment.RunID, "job_id", assignment.JobID, "job", assignment.JobName)
	if err := client.Start(ctx, assignment); err != nil {
		jobLogger.Error("could not start leased job", "error", err)
		return
	}
	jobLogger.Info("job started")
	status := runmodel.JobSucceeded
	if err := executor.Execute(ctx, assignment.JobID, assignment.Spec); err != nil {
		status = runmodel.JobFailed
		jobLogger.Error("job execution failed", "error", err)
	}
	completeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Complete(completeCtx, assignment, status); err != nil {
		jobLogger.Error("could not report job completion", "error", err)
		return
	}
	jobLogger.Info("job completed", "status", status)
}
