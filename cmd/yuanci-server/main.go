package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yuanci/yuanci/internal/config"
	"github.com/yuanci/yuanci/internal/httpapi"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/secrets"
	"github.com/yuanci/yuanci/internal/store/postgres"
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
	var handler http.Handler
	if cfg.AuthenticatedPreview {
		database := store.(*postgres.Store) // Config forbids memory storage in preview.
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
			login = httpapi.GitHubLogin{Store: database, Managed: provisioning.New(database, cipher, cfg.PublicOrigin)}
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
		handler = httpapi.NewEvaluation(logger, store, cfg.RequestBodyLimit, cfg.RunnerSharedToken)
	}

	server := &http.Server{
		Addr: cfg.Address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
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
	<-stop
	shutdown, done := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer done()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
