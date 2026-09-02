package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/config"
	"github.com/yuanci/yuanci/internal/runner"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadRunner()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
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
	if err := runGRPC(ctx, logger, cfg, executor); err != nil {
		logger.Error("Runner work channel stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("runner stopping")
}

func runGRPC(ctx context.Context, logger *slog.Logger, cfg config.Runner, executor *runner.DockerExecutor) error {
	isolation := runnerv1.IsolationLevel_ISOLATION_LEVEL_STANDARD
	switch cfg.IsolationLevel {
	case "privileged":
		isolation = runnerv1.IsolationLevel_ISOLATION_LEVEL_PRIVILEGED
	case "deployment":
		isolation = runnerv1.IsolationLevel_ISOLATION_LEVEL_DEPLOYMENT
	}
	capabilities := &runnerv1.RunnerCapabilities{Os: cfg.OS, Architecture: cfg.Architecture, Labels: cfg.Labels,
		Capacity: int32(cfg.Capacity), AvailableDiskBytes: cfg.AvailableDiskBytes, Executor: cfg.Executor, IsolationLevel: isolation}
	enrollmentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	credentials, err := runner.LoadOrEnroll(enrollmentCtx, runner.EnrollmentConfig{Address: cfg.GRPCAddress,
		ServerName: cfg.GRPCServerName, RootCAFile: cfg.RootCAFile, StateDir: cfg.StateDir,
		Token: cfg.RegistrationToken, TokenFile: cfg.RegistrationTokenFile, Name: cfg.Name, Capabilities: capabilities})
	cancel()
	if err != nil {
		return err
	}
	client, err := runner.NewWorkClient(runner.WorkConfig{Address: cfg.GRPCAddress, ServerName: cfg.GRPCServerName,
		StateDir: cfg.StateDir, Credentials: credentials, Capabilities: capabilities, Logger: logger})
	if err != nil {
		return err
	}
	logger.Info("Runner mTLS identity ready", "runner_id", credentials.RunnerID, "certificate_expires_at", credentials.NotAfter)
	return client.Run(ctx, executor)
}
