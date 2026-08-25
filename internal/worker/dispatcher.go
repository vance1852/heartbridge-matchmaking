// Package worker runs the background loops of the service: the notification
// outbox dispatcher and the expiry sweeper. Both honour context cancellation and
// stop gracefully.
package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/notify"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
)

// Sender delivers one notification. A real deployment plugs an email or SMS
// gateway in here; the interface keeps the retry policy testable without any
// network access.
type Sender interface {
	Send(ctx context.Context, job notify.Job) error
}

// SenderFunc adapts a function to the Sender interface.
type SenderFunc func(ctx context.Context, job notify.Job) error

// Send implements Sender.
func (f SenderFunc) Send(ctx context.Context, job notify.Job) error { return f(ctx, job) }

// DiscardSender accepts every notification. It is the default for deployments
// without a configured gateway, and it never hides a failure because it cannot
// fail.
type DiscardSender struct{}

// Send implements Sender.
func (DiscardSender) Send(context.Context, notify.Job) error { return nil }

// DispatcherStats is the cumulative outcome of the dispatcher.
type DispatcherStats struct {
	Delivered        int
	Retried          int
	PermanentlyDead  int
	ClaimedLastRound int
}

// Dispatcher drains the notification outbox.
type Dispatcher struct {
	repo   repository.NotificationRepository
	sender Sender
	clock  clock.Clock
	batch  int
	mu     sync.Mutex
	stats  DispatcherStats
}

// NewDispatcher builds a Dispatcher.
func NewDispatcher(repo repository.NotificationRepository, sender Sender, source clock.Clock, batch int) *Dispatcher {
	if sender == nil {
		sender = DiscardSender{}
	}
	if batch <= 0 {
		batch = repository.DefaultPageSize
	}
	return &Dispatcher{repo: repo, sender: sender, clock: source, batch: batch}
}

// Stats returns a snapshot of the cumulative outcome.
func (d *Dispatcher) Stats() DispatcherStats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stats
}

// Tick processes one batch of due notifications and returns how many rows were
// claimed. A delivery failure is retried with exponential backoff until the
// attempt budget is exhausted, at which point the row is retired as permanently
// failed instead of being retried forever.
func (d *Dispatcher) Tick(ctx context.Context) (int, error) {
	due, err := d.repo.ListDue(ctx, d.clock.Now(), d.batch)
	if err != nil {
		return 0, err
	}
	claimed := 0
	for _, job := range due {
		if err := ctx.Err(); err != nil {
			return claimed, err
		}
		claimed++
		if err := d.deliver(ctx, job); err != nil {
			return claimed, err
		}
	}
	d.mu.Lock()
	d.stats.ClaimedLastRound = claimed
	d.mu.Unlock()
	return claimed, nil
}

// deliver performs one attempt and records its outcome.
func (d *Dispatcher) deliver(ctx context.Context, job notify.Job) error {
	attempts := job.Attempts + 1
	sendErr := d.sender.Send(ctx, job)
	now := d.clock.Now()

	record := detachedContext(ctx)
	if sendErr == nil {
		if err := d.repo.MarkSucceeded(record, job.ID, attempts, now); err != nil {
			return d.tolerateRaces(ctx, job.ID, err)
		}
		d.mu.Lock()
		d.stats.Delivered++
		d.mu.Unlock()
		return nil
	}
	// A cancelled context is not a delivery failure: the attempt never really
	// happened, so the row stays untouched and is picked up again later.
	if errors.Is(sendErr, context.Canceled) || errors.Is(sendErr, context.DeadlineExceeded) {
		return sendErr
	}

	if attempts >= job.MaxAttempts {
		if err := d.repo.MarkPermanentFailure(record, job.ID, attempts, sendErr.Error(), now); err != nil {
			return d.tolerateRaces(ctx, job.ID, err)
		}
		d.mu.Lock()
		d.stats.PermanentlyDead++
		d.mu.Unlock()
		logging.FromContext(ctx).Warn("notification permanently failed",
			"job_id", job.ID, "kind", string(job.Kind), "attempts", attempts, "error", sendErr.Error())
		return nil
	}
	next := now.Add(notify.Backoff(attempts))
	if err := d.repo.ScheduleRetry(record, job.ID, attempts, next, sendErr.Error(), now); err != nil {
		return d.tolerateRaces(ctx, job.ID, err)
	}
	d.mu.Lock()
	d.stats.Retried++
	d.mu.Unlock()
	logging.FromContext(ctx).Debug("notification retry scheduled",
		"job_id", job.ID, "attempts", attempts, "next_attempt_at", next)
	return nil
}

// tolerateRaces swallows the conflict raised when another dispatcher instance
// already finished the same row, and propagates every other failure.
func (d *Dispatcher) tolerateRaces(ctx context.Context, jobID string, err error) error {
	if apperr.CodeOf(err) == apperr.CodeConflict {
		logging.FromContext(ctx).Debug("notification already finalised elsewhere", "job_id", jobID)
		return nil
	}
	return err
}

// Run drives Tick on a fixed interval until the context is cancelled. It returns
// nil on a clean shutdown so that the caller can distinguish a stop from a crash.
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	logger := logging.FromContext(ctx)
	logger.Info("notification dispatcher started", "interval", interval.String(), "batch", d.batch)

	for {
		select {
		case <-ctx.Done():
			logger.Info("notification dispatcher stopped", "reason", ctx.Err().Error())
			return nil
		case <-ticker.C:
			if _, err := d.Tick(ctx); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					logger.Info("notification dispatcher stopped mid-tick")
					return nil
				}
				logger.Error("notification dispatcher tick failed", "error", err.Error())
			}
		}
	}
}
