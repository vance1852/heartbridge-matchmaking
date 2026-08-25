package apptest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/notify"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// TestOutboxIsWrittenInsideBusinessTransaction verifies that a committed business
// change always leaves its notifications behind, and that a rejected change leaves
// none.
func TestOutboxIsWrittenInsideBusinessTransaction(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("obmm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "obA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "obB@heartbridge.test", Gender: "male"})

	if pending := h.pendingNotifications(); pending != 0 {
		t.Fatalf("expected an empty outbox, got %d rows", pending)
	}
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	if pending := h.pendingNotifications(); pending != 2 {
		t.Fatalf("expected one notification per member, got %d", pending)
	}

	// A rejected duplicate proposal must not enqueue anything.
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err == nil {
		t.Fatal("expected the duplicate proposal to be rejected")
	}
	if pending := h.pendingNotifications(); pending != 2 {
		t.Fatalf("a rejected change enqueued notifications: %d rows", pending)
	}

	delivered := h.drainNotifications()
	if delivered != 2 {
		t.Fatalf("expected two deliveries, got %d", delivered)
	}
	if h.sender.deliveredCount() != 2 {
		t.Fatalf("expected the gateway to receive two notifications, got %d", h.sender.deliveredCount())
	}
	if pending := h.pendingNotifications(); pending != 0 {
		t.Fatalf("expected a drained outbox, got %d rows", pending)
	}
}

// TestDispatcherRetriesWithBackoffThenGivesUp exercises the whole retry policy of
// the outbox without sleeping: the frozen clock is advanced by the expected delay.
func TestDispatcherRetriesWithBackoffThenGivesUp(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("retrymm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "retryA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "retryB@heartbridge.test", Gender: "male"})
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}

	gatewayDown := errors.New("gateway refused the message")
	// Fail every attempt of both rows for the entire attempt budget.
	h.sender.failNext(2*notify.DefaultMaxAttempts, gatewayDown)

	for attempt := 1; attempt <= notify.DefaultMaxAttempts; attempt++ {
		claimed, err := h.app.Dispatcher.Tick(h.ctx())
		if err != nil {
			t.Fatalf("tick %d: %v", attempt, err)
		}
		if claimed != 2 {
			t.Fatalf("tick %d claimed %d rows, expected 2", attempt, claimed)
		}
		if attempt == notify.DefaultMaxAttempts {
			break
		}
		// Before the backoff elapsed nothing is due.
		idle, err := h.app.Dispatcher.Tick(h.ctx())
		if err != nil {
			t.Fatalf("idle tick after attempt %d: %v", attempt, err)
		}
		if idle != 0 {
			t.Fatalf("expected the backoff to hold the rows back, %d were claimed", idle)
		}
		h.clk.Advance(notify.Backoff(attempt))
	}

	stats := h.app.Dispatcher.Stats()
	if stats.Retried != 2*(notify.DefaultMaxAttempts-1) {
		t.Fatalf("expected %d retries, got %d", 2*(notify.DefaultMaxAttempts-1), stats.Retried)
	}
	if stats.PermanentlyDead != 2 {
		t.Fatalf("expected both rows to be retired, got %d", stats.PermanentlyDead)
	}
	if stats.Delivered != 0 {
		t.Fatalf("expected no delivery, got %d", stats.Delivered)
	}
	if pending := h.pendingNotifications(); pending != 0 {
		t.Fatalf("expected no pending rows left, got %d", pending)
	}
	failedCount, err := h.app.Repositories.Notifications.CountByState(h.ctx(), notify.StateFailedPermanent)
	if err != nil {
		t.Fatalf("count failed rows: %v", err)
	}
	if failedCount != 2 {
		t.Fatalf("expected two permanently failed rows, got %d", failedCount)
	}
	if h.sender.failureCount() != 2*notify.DefaultMaxAttempts {
		t.Fatalf("expected %d attempts, got %d", 2*notify.DefaultMaxAttempts, h.sender.failureCount())
	}
}

// TestDispatcherRecoversAfterTransientFailure verifies a row that fails once is
// delivered on its next attempt and keeps its attempt history.
func TestDispatcherRecoversAfterTransientFailure(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("flakymm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "flakyA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "flakyB@heartbridge.test", Gender: "male"})
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}

	h.sender.failNext(1, errors.New("temporary network glitch"))
	if _, err := h.app.Dispatcher.Tick(h.ctx()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if h.pendingNotifications() != 1 {
		t.Fatalf("expected one row waiting for its retry, got %d", h.pendingNotifications())
	}
	h.clk.Advance(notify.Backoff(1))
	if _, err := h.app.Dispatcher.Tick(h.ctx()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if h.pendingNotifications() != 0 {
		t.Fatalf("expected the retry to succeed, %d rows are still pending", h.pendingNotifications())
	}
	stats := h.app.Dispatcher.Stats()
	if stats.Delivered != 2 || stats.Retried != 1 || stats.PermanentlyDead != 0 {
		t.Fatalf("unexpected dispatcher stats: %+v", stats)
	}
}

// TestDispatcherStopsOnCancelledContext verifies the worker honours cancellation
// and leaves the outbox untouched.
func TestDispatcherStopsOnCancelledContext(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("cancelmm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "cancelWA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "cancelWB@heartbridge.test", Gender: "male"})
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}

	cancelled, cancel := context.WithCancel(h.ctx())
	cancel()
	if _, err := h.app.Dispatcher.Tick(cancelled); err == nil {
		t.Fatal("expected a cancelled tick to report the cancellation")
	}
	if pending := h.pendingNotifications(); pending != 2 {
		t.Fatalf("a cancelled tick changed the outbox: %d rows pending", pending)
	}

	// Run exits cleanly rather than reporting the cancellation as a crash.
	stopped := make(chan error, 1)
	runCtx, stopRun := context.WithCancel(h.ctx())
	go func() { stopped <- h.app.Dispatcher.Run(runCtx, 10*time.Millisecond) }()
	stopRun()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("expected a clean shutdown, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the dispatcher did not stop within the shutdown window")
	}
}

// TestSweeperExpiresAndPrunes verifies the sweeper handles all three of its
// concerns in one round.
func TestSweeperExpiresAndPrunes(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("sweepmm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "sweepA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "sweepB@heartbridge.test", Gender: "male"})
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	idle, err := h.app.Sweeper.Tick(h.ctx())
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if idle.ExpiredMatches != 0 {
		t.Fatalf("expected nothing to expire yet, got %d", idle.ExpiredMatches)
	}

	// Move past both the consent deadline and the session lifetime.
	h.clk.Advance(50 * time.Hour)
	round, err := h.app.Sweeper.Tick(h.ctx())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if round.ExpiredMatches != 1 {
		t.Fatalf("expected one expired introduction, got %d", round.ExpiredMatches)
	}
	if round.PurgedSessions == 0 {
		t.Fatalf("expected expired sessions to be pruned, got %d", round.PurgedSessions)
	}

	after, err := h.app.Matches.Get(h.ctx(), matchmaker, detail.Match.ID)
	if err != nil {
		t.Fatalf("read introduction: %v", err)
	}
	if after.Match.State != matching.StateExpired {
		t.Fatalf("expected state %s, got %s", matching.StateExpired, after.Match.State)
	}
	stats := h.app.Sweeper.Stats()
	if stats.ExpiredMatches != 1 {
		t.Fatalf("expected the cumulative counter to be 1, got %d", stats.ExpiredMatches)
	}

	// A cancelled sweep reports the cancellation instead of pretending it worked.
	cancelled, cancel := context.WithCancel(h.ctx())
	cancel()
	if _, err := h.app.Sweeper.Tick(cancelled); err == nil {
		t.Fatal("expected a cancelled sweep to report the cancellation")
	}
}

// TestBackoffGrowsAndIsCapped documents the retry schedule the dispatcher relies on.
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	if notify.Backoff(1) != notify.BaseBackoff {
		t.Fatalf("expected the first retry to wait %s, got %s", notify.BaseBackoff, notify.Backoff(1))
	}
	if notify.Backoff(2) != 2*notify.BaseBackoff {
		t.Fatalf("expected exponential growth, got %s", notify.Backoff(2))
	}
	if notify.Backoff(0) != notify.BaseBackoff {
		t.Fatalf("expected a non-positive attempt to be clamped, got %s", notify.Backoff(0))
	}
	if notify.Backoff(40) != notify.MaxBackoff {
		t.Fatalf("expected the delay to be capped at %s, got %s", notify.MaxBackoff, notify.Backoff(40))
	}
}

// TestContextDeadlinePropagatesToPersistence verifies that a request deadline
// reaches the database layer instead of being silently ignored.
func TestContextDeadlinePropagatesToPersistence(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("ctxmm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "ctxA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "ctxB@heartbridge.test", Gender: "male"})

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := h.app.Matches.Propose(expired, matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err == nil {
		t.Fatal("expected an expired deadline to abort the proposal")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeDeadlineExceeded {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeDeadlineExceeded, code, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the cause chain to keep the deadline error, got %v", err)
	}
	// Nothing was written, so a later proposal with a healthy context still works.
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err != nil {
		t.Fatalf("proposal after the aborted attempt: %v", err)
	}
}
