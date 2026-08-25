package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/idempotency"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/authsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// SystemActorID is the actor recorded for changes made by a background loop. It
// keeps the audit trail complete for automatic transitions.
const SystemActorID = "system:expiry-sweeper"

// SweeperStats is the cumulative outcome of the sweeper.
type SweeperStats struct {
	ExpiredMatches   int
	PurgedSessions   int
	PurgedIdempotent int
	Rounds           int
}

// Sweeper expires unanswered introductions and prunes stale bookkeeping rows.
type Sweeper struct {
	matches *matchsvc.Service
	auth    *authsvc.Service
	guard   *idempotency.Guard
	clock   clock.Clock
	batch   int
	mu      sync.Mutex
	stats   SweeperStats
}

// NewSweeper builds a Sweeper.
func NewSweeper(
	matches *matchsvc.Service, auth *authsvc.Service, guard *idempotency.Guard,
	source clock.Clock, batch int,
) *Sweeper {
	if batch <= 0 {
		batch = 50
	}
	return &Sweeper{matches: matches, auth: auth, guard: guard, clock: source, batch: batch}
}

// Stats returns a snapshot of the cumulative outcome.
func (s *Sweeper) Stats() SweeperStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// systemActor is the principal used for automatic transitions.
func systemActor() identity.Actor {
	return identity.Actor{UserID: SystemActorID, Role: identity.RoleAdmin}
}

// Tick performs one sweep. Each concern is independent: a failure to prune
// sessions must not stop introductions from expiring, so the first error is
// remembered and returned after the remaining work ran.
func (s *Sweeper) Tick(ctx context.Context) (SweeperStats, error) {
	var firstErr error
	round := SweeperStats{Rounds: 1}

	expired, err := s.matches.ExpireDue(ctx, systemActor(), s.batch)
	round.ExpiredMatches = expired
	if err != nil {
		firstErr = err
	}

	if ctx.Err() == nil {
		purgedSessions, err := s.auth.PurgeExpiredSessions(ctx, s.clock.Now())
		round.PurgedSessions = purgedSessions
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if ctx.Err() == nil && s.guard != nil {
		purgedKeys, err := s.guard.Sweep(ctx)
		round.PurgedIdempotent = purgedKeys
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	s.mu.Lock()
	s.stats.ExpiredMatches += round.ExpiredMatches
	s.stats.PurgedSessions += round.PurgedSessions
	s.stats.PurgedIdempotent += round.PurgedIdempotent
	s.stats.Rounds++
	s.mu.Unlock()

	if firstErr != nil {
		return round, firstErr
	}
	return round, nil
}

// Run drives Tick on a fixed interval until the context is cancelled.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	logger := logging.FromContext(ctx)
	logger.Info("expiry sweeper started", "interval", interval.String(), "batch", s.batch)

	for {
		select {
		case <-ctx.Done():
			logger.Info("expiry sweeper stopped", "reason", ctx.Err().Error())
			return nil
		case <-ticker.C:
			round, err := s.Tick(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					logger.Info("expiry sweeper stopped mid-tick")
					return nil
				}
				logger.Error("expiry sweeper tick failed", "error", err.Error())
				continue
			}
			if round.ExpiredMatches > 0 || round.PurgedSessions > 0 || round.PurgedIdempotent > 0 {
				logger.Info("expiry sweep completed",
					"expired_matches", round.ExpiredMatches,
					"purged_sessions", round.PurgedSessions,
					"purged_idempotency_keys", round.PurgedIdempotent)
			}
		}
	}
}
