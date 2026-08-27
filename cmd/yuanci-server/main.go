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
	runmodel "github.com/yuanci/yuanci/internal/run"
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

	server := &http.Server{
		Addr: cfg.Address, Handler: httpapi.New(logger, store, cfg.RequestBodyLimit, cfg.RunnerSharedToken),
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
