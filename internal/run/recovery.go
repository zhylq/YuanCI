package run

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	DefaultRecoveryInterval = 5 * time.Second
	MaximumRecoveryBatch    = 100
)

var ErrInvalidRecoveryLimit = errors.New("invalid lease recovery limit")

type RecoveryResult struct {
	Requeued int
	Failed   int
}

type LeaseRecoveryStore interface {
	RecoverExpiredRunnerLeases(context.Context, int) (RecoveryResult, error)
}

type LeaseReconciler struct {
	store    LeaseRecoveryStore
	logger   *slog.Logger
	interval time.Duration
}

func NewLeaseReconciler(store LeaseRecoveryStore, logger *slog.Logger) (*LeaseReconciler, error) {
	if store == nil || logger == nil {
		return nil, errors.New("invalid lease reconciler configuration")
	}
	return &LeaseReconciler{store: store, logger: logger, interval: DefaultRecoveryInterval}, nil
}

func (reconciler *LeaseReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(reconciler.interval)
	defer ticker.Stop()
	for {
		reconciler.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (reconciler *LeaseReconciler) reconcile(ctx context.Context) {
	check, cancel := context.WithTimeout(ctx, reconciler.interval)
	defer cancel()
	result, err := reconciler.store.RecoverExpiredRunnerLeases(check, MaximumRecoveryBatch)
	if err != nil {
		if ctx.Err() == nil {
			reconciler.logger.Warn("Runner lease reconciliation failed", "reason", "store_error")
		}
		return
	}
	if result.Requeued > 0 || result.Failed > 0 {
		reconciler.logger.Info("Runner leases reconciled", "requeued", result.Requeued, "failed", result.Failed)
	}
}
