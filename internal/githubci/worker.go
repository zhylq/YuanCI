package githubci

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/yuanci/yuanci/internal/githubhook"
)

const (
	WebhookLeaseDuration    = time.Minute
	WebhookRecoveryInterval = 5 * time.Second
	WebhookRecoveryBatch    = 100
	webhookIdleInterval     = 250 * time.Millisecond
)

// DeliveryProcessor handles a claimed delivery. Orchestrator is the production
// implementation; the small interface keeps the worker lifecycle testable.
type DeliveryProcessor interface {
	Process(context.Context, githubhook.WorkItem) (Outcome, error)
}

// Worker drains one-at-a-time from the durable inbox. Multiple process workers
// are safe because the inbox claim uses a database lease and SKIP LOCKED.
type Worker struct {
	store            githubhook.WorkStore
	processor        DeliveryProcessor
	logger           *slog.Logger
	leaseDuration    time.Duration
	recoveryInterval time.Duration
	idleInterval     time.Duration
}

func NewWorker(store githubhook.WorkStore, processor DeliveryProcessor, logger *slog.Logger) (*Worker, error) {
	if store == nil || processor == nil || logger == nil {
		return nil, errors.New("GitHub delivery worker requires store, processor and logger")
	}
	return &Worker{
		store: store, processor: processor, logger: logger, leaseDuration: WebhookLeaseDuration,
		recoveryInterval: WebhookRecoveryInterval, idleInterval: webhookIdleInterval,
	}, nil
}

// Run performs recovery before claiming work, repeats it on a bounded tick,
// and exits promptly when the owning server cancels ctx. A claimed but
// unfinished delivery is safely recovered by its lease after a crash.
func (w *Worker) Run(ctx context.Context) {
	w.recover(ctx)
	ticker := time.NewTicker(w.recoveryInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		item, err := w.store.ClaimWebhook(ctx, w.leaseDuration)
		switch {
		case err == nil:
			if item == nil {
				w.logger.Warn("GitHub delivery claim returned no item", "reason", "empty_claim")
			} else if _, processErr := w.processor.Process(ctx, *item); processErr != nil && ctx.Err() == nil {
				w.logger.Warn("GitHub delivery processing failed", "reason", "processing_error")
			}
		case errors.Is(err, githubhook.ErrNoDelivery):
		case ctx.Err() != nil:
			return
		default:
			w.logger.Warn("GitHub delivery claim failed", "reason", "store_error")
		}
		if !w.wait(ctx, ticker.C) {
			return
		}
	}
}

func (w *Worker) wait(ctx context.Context, tick <-chan time.Time) bool {
	timer := time.NewTimer(w.idleInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-tick:
		w.recover(ctx)
		return true
	case <-timer.C:
		return true
	}
}

func (w *Worker) recover(ctx context.Context) {
	check, cancel := context.WithTimeout(ctx, w.recoveryInterval)
	defer cancel()
	count, err := w.store.RecoverWebhookLeases(check, WebhookRecoveryBatch)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Warn("GitHub delivery lease recovery failed", "reason", "store_error")
		}
		return
	}
	if count > 0 {
		w.logger.Info("GitHub delivery leases recovered", "count", count)
	}
}
