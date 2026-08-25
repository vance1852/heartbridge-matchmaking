package apptest

import (
	"context"
	"testing"

	"github.com/vance1852/heartbridge-matchmaking/internal/domain/notify"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/worker"
)

// TestShutdownDuringDeliveryLeavesOutboxUntouched checks that a notification
// round interrupted by a shutdown records no delivery outcome, so every queued
// message is still picked up by the next healthy round.
func TestShutdownDuringDeliveryLeavesOutboxUntouched(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("shutdownmm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "sda@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "sdb@heartbridge.test", Gender: "male"})
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err != nil {
		t.Fatalf("propose introduction: %v", err)
	}
	queued := h.pendingNotifications()
	if queued != 2 {
		t.Fatalf("expected one queued message per member, got %d", queued)
	}

	interrupted, stop := context.WithCancel(h.ctx())
	attempts := 0
	interruptedSender := worker.SenderFunc(func(context.Context, notify.Job) error {
		attempts++
		// The process is asked to stop while the gateway call is in flight.
		stop()
		return nil
	})
	interruptedDispatcher := worker.NewDispatcher(
		h.app.Repositories.Notifications, interruptedSender, h.clk, 10)

	if _, err := interruptedDispatcher.Tick(interrupted); err == nil {
		t.Fatal("expected the interrupted round to report the shutdown instead of finishing quietly")
	}
	if attempts == 0 {
		t.Fatal("expected the interrupted round to have started one delivery")
	}
	if still := h.pendingNotifications(); still != queued {
		t.Fatalf("the interrupted round recorded an outcome: %d of %d messages are still queued",
			still, queued)
	}
	delivered, err := h.app.Repositories.Notifications.CountByState(h.ctx(), notify.StateSucceeded)
	if err != nil {
		t.Fatalf("count delivered messages: %v", err)
	}
	if delivered != 0 {
		t.Fatalf("expected no message to be marked delivered by the interrupted round, got %d", delivered)
	}

	// A healthy round afterwards must still deliver everything exactly once.
	healthyAttempts := 0
	healthySender := worker.SenderFunc(func(context.Context, notify.Job) error {
		healthyAttempts++
		return nil
	})
	healthyDispatcher := worker.NewDispatcher(
		h.app.Repositories.Notifications, healthySender, h.clk, 10)
	if _, err := healthyDispatcher.Tick(h.ctx()); err != nil {
		t.Fatalf("healthy round: %v", err)
	}
	if healthyAttempts != queued {
		t.Fatalf("expected the healthy round to attempt %d messages, got %d", queued, healthyAttempts)
	}
	if remaining := h.pendingNotifications(); remaining != 0 {
		t.Fatalf("expected the outbox to be drained, %d messages are still queued", remaining)
	}
}
