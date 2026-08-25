package worker

import (
	"context"
	"sync"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
)

// detachedContext returns a context for the bookkeeping write that records the
// outcome of an attempt already made, so that recording it does not depend on
// the lifetime of the loop that started the attempt.
func detachedContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.Background()
}

// Group supervises the background loops so that the process has exactly one
// shutdown path for all of them.
type Group struct {
	wait sync.WaitGroup
	mu   sync.Mutex
	errs []error
}

// Loop is a cancellable background loop.
type Loop func(ctx context.Context) error

// Start launches a named loop.
func (g *Group) Start(ctx context.Context, name string, loop Loop) {
	g.wait.Add(1)
	go func() {
		defer g.wait.Done()
		if err := loop(ctx); err != nil {
			g.mu.Lock()
			g.errs = append(g.errs, err)
			g.mu.Unlock()
			logging.FromContext(ctx).Error("background loop exited with an error",
				"loop", name, "error", err.Error())
		}
	}()
}

// StartDispatcher launches the notification dispatcher.
func (g *Group) StartDispatcher(ctx context.Context, dispatcher *Dispatcher, interval time.Duration) {
	g.Start(ctx, "notification-dispatcher", func(ctx context.Context) error {
		return dispatcher.Run(ctx, interval)
	})
}

// StartSweeper launches the expiry sweeper.
func (g *Group) StartSweeper(ctx context.Context, sweeper *Sweeper, interval time.Duration) {
	g.Start(ctx, "expiry-sweeper", func(ctx context.Context) error {
		return sweeper.Run(ctx, interval)
	})
}

// Wait blocks until every loop returned and reports the collected failures.
func (g *Group) Wait() []error {
	g.wait.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	collected := make([]error, len(g.errs))
	copy(collected, g.errs)
	return collected
}
