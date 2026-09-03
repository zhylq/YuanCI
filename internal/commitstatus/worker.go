package commitstatus

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/yuanci/yuanci/internal/scm"
)

const (
	StatusLeaseDuration    = time.Minute
	StatusRecoveryInterval = 5 * time.Second
	StatusRecoveryBatch    = 100
	statusIdleInterval     = 250 * time.Millisecond
	statusMaximumAttempts  = 8
	statusRateLimitDelay   = time.Minute
)

type DeliveryProvider interface {
	Deliver(context.Context, Item) error
}

type Worker struct {
	store            RecoveryRepository
	provider         DeliveryProvider
	logger           *slog.Logger
	now              func() time.Time
	leaseDuration    time.Duration
	recoveryInterval time.Duration
	idleInterval     time.Duration
}

func NewWorker(store RecoveryRepository, provider DeliveryProvider, logger *slog.Logger) (*Worker, error) {
	if store == nil || provider == nil || logger == nil {
		return nil, ErrInvalid
	}
	return &Worker{store: store, provider: provider, logger: logger, now: time.Now,
		leaseDuration: StatusLeaseDuration, recoveryInterval: StatusRecoveryInterval, idleInterval: statusIdleInterval}, nil
}

func (worker *Worker) Run(ctx context.Context) {
	worker.recover(ctx)
	ticker := time.NewTicker(worker.recoveryInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		worker.processOne(ctx)
		timer := time.NewTimer(worker.idleInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-ticker.C:
			timer.Stop()
			worker.recover(ctx)
		case <-timer.C:
		}
	}
}

func (worker *Worker) processOne(ctx context.Context) {
	item, err := worker.store.ClaimCommitStatus(ctx, worker.leaseDuration)
	if err != nil {
		if ctx.Err() == nil {
			worker.logger.Warn("commit status claim failed", "reason", "store_error")
		}
		return
	}
	if item == nil {
		return
	}
	if err := worker.provider.Deliver(ctx, *item); err == nil {
		if finishErr := worker.store.FinishCommitStatus(ctx, item.ID, item.LeaseOwner); finishErr != nil && ctx.Err() == nil {
			worker.logger.Warn("commit status acknowledgement failed", "reason", "store_error")
		}
		return
	} else if ctx.Err() != nil {
		return
	} else {
		next, code, dead := worker.retryDecision(*item, err)
		if retryErr := worker.store.RescheduleCommitStatus(ctx, item.ID, item.LeaseOwner, next, code, dead); retryErr != nil {
			worker.logger.Warn("commit status retry persistence failed", "reason", "store_error")
		}
	}
}

func (worker *Worker) retryDecision(item Item, deliveryErr error) (time.Time, string, bool) {
	now := worker.now().UTC()
	delay, code := time.Second, "provider_transient"
	if errors.Is(deliveryErr, scm.ErrRateLimited) {
		delay, code = statusRateLimitDelay, "provider_rate_limited"
	} else {
		shift := min(max(item.AttemptCount-1, 0), 8)
		delay = time.Second * time.Duration(1<<shift)
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
	}
	next := now.Add(delay)
	dead := errors.Is(deliveryErr, ErrInvalid) || item.AttemptCount >= statusMaximumAttempts || !next.Before(item.ExpiresAt)
	if errors.Is(deliveryErr, ErrInvalid) {
		code = "provider_permanent"
	}
	return next, code, dead
}

func (worker *Worker) recover(ctx context.Context) {
	check, cancel := context.WithTimeout(ctx, worker.recoveryInterval)
	defer cancel()
	count, err := worker.store.RecoverCommitStatusLeases(check, StatusRecoveryBatch)
	if err != nil {
		if ctx.Err() == nil {
			worker.logger.Warn("commit status lease recovery failed", "reason", "store_error")
		}
		return
	}
	if count > 0 {
		worker.logger.Info("commit status leases recovered", "count", count)
	}
}
