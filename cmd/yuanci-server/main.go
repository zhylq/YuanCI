package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yuanci/yuanci/internal/commitstatus"
	"github.com/yuanci/yuanci/internal/config"
	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/githubci"
	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/integration"
	"github.com/yuanci/yuanci/internal/provisioning"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/runnerauth"
	"github.com/yuanci/yuanci/internal/runnergrpc"
	"github.com/yuanci/yuanci/internal/secrets"
	"github.com/yuanci/yuanci/internal/store/postgres"
	"google.golang.org/grpc"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.LoadServer()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var store runmodel.Store
	if cfg.DevInMemory {
		logger.Warn("using in-memory development store; data will be lost on restart")
		store = runmodel.NewMemoryStore()
	} else {
		store, err = postgres.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			logger.Error("open database", "error", err)
			os.Exit(1)
		}
	}
	defer store.Close()
	recoveryStore, ok := store.(runmodel.LeaseRecoveryStore)
	if !ok {
		logger.Error("Run store does not support Runner lease recovery")
		os.Exit(1)
	}
	stopRecovery := startLeaseRecovery(logger, recoveryStore)
	defer stopRecovery()
	var handler http.Handler
	var database *postgres.Store
	var githubPipeline *githubapp.Service
	var giteePipeline *gitee.Service
	if cfg.AuthenticatedPreview {
		database = store.(*postgres.Store) // Config forbids memory storage in preview.
		var login httpapi.GitHubLogin
		if cfg.ManagedSetup {
			if err := database.BindManagedMasterKey(ctx, cfg.MasterKey); err != nil {
				logger.Error("master key does not match persisted configuration")
				os.Exit(2)
			}
			cipher, cipherErr := secrets.NewCipher(cfg.MasterKey)
			clear(cfg.MasterKey)
			if cipherErr != nil {
				logger.Error("invalid master key")
				os.Exit(2)
			}
			githubProvider := integration.NewGitHub()
			integrations := integration.New(database, cipher, cfg.PublicOrigin)
			integrations.Provider = githubProvider
			githubPipeline, err = githubapp.New(database, cipher, githubProvider)
			if err != nil {
				logger.Error("GitHub pipeline service initialization failed")
				os.Exit(2)
			}
			giteePipeline = gitee.New(database, cipher, cfg.PublicOrigin)
			login = httpapi.GitHubLogin{Store: database, Managed: provisioning.New(database, cipher, cfg.PublicOrigin), Integrations: integrations, Pipeline: githubPipeline, Gitee: giteePipeline}
			stopCleanup := startIntegrationCleanup(logger, database)
			defer stopCleanup()
		} else {
			if err := database.ConfigureGitHubBootstrap(ctx, cfg.BootstrapGitHubUserID); err != nil {
				logger.Error("administrator bootstrap initialization failed; check persisted configuration")
				os.Exit(2)
			}
			provider, err := identity.NewGitHub(cfg.GitHubClientID, cfg.GitHubClientSecret, cfg.PublicOrigin+"/api/v1/auth/github/callback")
			if err != nil {
				logger.Error("invalid GitHub login configuration")
				os.Exit(2)
			}
			login = httpapi.GitHubLogin{Store: database, Provider: provider}
		}
		handler, err = httpapi.NewAuthenticated(logger, store, database, cfg.RequestBodyLimit, cfg.PublicOrigin, login)
		if err != nil {
			logger.Error("authenticated API initialization failed")
			os.Exit(2)
		}
		logger.Warn("authenticated preview enabled; legacy Runner API disabled; not production ready")
	} else {
		handler = httpapi.NewEvaluation(logger, store, cfg.RequestBodyLimit)
	}
	if githubPipeline != nil {
		orchestrator, workerErr := githubci.NewOrchestrator(database, pipelineRouter{github: githubPipeline, gitee: giteePipeline})
		if workerErr != nil {
			logger.Error("GitHub delivery orchestrator initialization failed")
			os.Exit(2)
		}
		worker, workerErr := githubci.NewWorker(database, orchestrator, logger)
		if workerErr != nil {
			logger.Error("GitHub delivery worker initialization failed")
			os.Exit(2)
		}
		stopGitHubWorker := startGitHubWorker(worker)
		defer stopGitHubWorker()
		statusProvider, workerErr := commitstatus.NewGitHubProvider(githubPipeline)
		if workerErr != nil {
			logger.Error("GitHub status provider initialization failed")
			os.Exit(2)
		}
		statusWorker, workerErr := commitstatus.NewWorker(database, statusRouter{github: statusProvider, gitee: giteePipeline}, logger)
		if workerErr != nil {
			logger.Error("commit status worker initialization failed")
			os.Exit(2)
		}
		stopStatusWorker := startStatusWorker(statusWorker)
		defer stopStatusWorker()
	}

	server := &http.Server{
		Addr: cfg.Address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
	}
	var grpcServer *grpc.Server
	var grpcListener net.Listener
	if cfg.RunnerGRPCAddress != "" {
		pki, err := runnergrpc.LoadPKI(runnergrpc.PKIFiles{
			ServerCertificate: cfg.RunnerServerCertFile, ServerKey: cfg.RunnerServerKeyFile,
			ClientCA: cfg.RunnerClientCAFile, IssuerCertificate: cfg.RunnerIssuerCertFile,
			IssuerKey: cfg.RunnerIssuerKeyFile,
		})
		if err != nil {
			logger.Error("Runner PKI initialization failed")
			os.Exit(2)
		}
		auth, err := runnerauth.New(store.(*postgres.Store), pki.Issuer, pki.IssuerKey)
		if err != nil {
			logger.Error("Runner identity service initialization failed")
			os.Exit(2)
		}
		var credentialIssuers []runnergrpc.CredentialIssuer
		if githubPipeline != nil {
			credentialIssuers = append(credentialIssuers, githubPipeline)
		}
		grpcServer, err = runnergrpc.NewServer(auth, store.(runmodel.RunnerJobStore), pki.RootPEM, pki.TLSConfig, credentialIssuers...)
		if err != nil {
			logger.Error("Runner gRPC initialization failed")
			os.Exit(2)
		}
		grpcListener, err = net.Listen("tcp", cfg.RunnerGRPCAddress)
		if err != nil {
			logger.Error("Runner gRPC listener failed")
			os.Exit(1)
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		logger.Info("server listening", "address", cfg.Address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			stop <- syscall.SIGTERM
		}
	}()
	if grpcServer != nil {
		go func() {
			logger.Info("Runner gRPC listening", "address", cfg.RunnerGRPCAddress)
			if err := grpcServer.Serve(grpcListener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				logger.Error("Runner gRPC server failed")
				select {
				case stop <- syscall.SIGTERM:
				default:
				}
			}
		}()
	}
	<-stop
	shutdown, done := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer done()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
	if grpcServer != nil {
		gracefulStopGRPC(grpcServer, cfg.ShutdownTimeout)
	}
}

func gracefulStopGRPC(server *grpc.Server, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		server.Stop()
		<-done
	}
}

func startIntegrationCleanup(logger *slog.Logger, database *postgres.Store) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			check, finish := context.WithTimeout(ctx, 10*time.Second)
			err := database.PruneIntegrationCredentials(check)
			finish()
			if err != nil && ctx.Err() == nil {
				logger.Warn("expired integration credential cleanup failed; authorization deadlines remain enforced")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func startLeaseRecovery(logger *slog.Logger, store runmodel.LeaseRecoveryStore) func() {
	reconciler, err := runmodel.NewLeaseReconciler(store, logger)
	if err != nil {
		panic("invalid Runner lease reconciler configuration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		reconciler.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func startGitHubWorker(worker *githubci.Worker) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func startStatusWorker(worker *commitstatus.Worker) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}
