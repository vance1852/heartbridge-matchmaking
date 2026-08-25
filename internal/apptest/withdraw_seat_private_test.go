package apptest

import (
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
)

// TestRepeatedMeetupWithdrawalKeepsVenueCountHonest checks that retrying a
// withdrawal frees exactly one venue seat, that the withdrawn introduction can be
// arranged again, and that the venue never accepts more meetups than its capacity.
func TestRepeatedMeetupWithdrawalKeepsVenueCountHonest(t *testing.T) {
	h := newHarness(t)
	staffActor, _ := h.staff("withdrawmm@heartbridge.test", "matchmaker")
	slotID := h.publishSlot("WITHDRAW-1", 30*time.Hour, 2*time.Hour, 2)

	first := h.consentedMatch("wd1@heartbridge.test", "wd1a@heartbridge.test", "wd1b@heartbridge.test")
	second := h.consentedMatch("wd2@heartbridge.test", "wd2a@heartbridge.test", "wd2b@heartbridge.test")
	third := h.consentedMatch("wd3@heartbridge.test", "wd3a@heartbridge.test", "wd3b@heartbridge.test")

	firstMeetup, err := h.app.Schedule.Book(h.ctx(), staffActor, first, slotID)
	if err != nil {
		t.Fatalf("book the first meetup: %v", err)
	}
	if _, err := h.app.Schedule.Book(h.ctx(), staffActor, second, slotID); err != nil {
		t.Fatalf("book the second meetup: %v", err)
	}
	if slot, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID); err != nil {
		t.Fatalf("read the venue slot: %v", err)
	} else if slot.BookedCount != 2 {
		t.Fatalf("expected the venue to be full, booked_count=%d", slot.BookedCount)
	}

	if _, err := h.app.Schedule.Cancel(h.ctx(), staffActor, firstMeetup.Meetup.ID, "会员临时出差"); err != nil {
		t.Fatalf("withdraw the first meetup: %v", err)
	}
	// The consultant retries the very same withdrawal after a timeout.
	if _, err := h.app.Schedule.Cancel(h.ctx(), staffActor, firstMeetup.Meetup.ID, "会员临时出差"); err == nil {
		t.Fatal("expected the repeated withdrawal of an already withdrawn meetup to be refused")
	}

	afterRetry, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("re-read the venue slot: %v", err)
	}
	if afterRetry.BookedCount != 1 {
		t.Fatalf("the retried withdrawal changed the venue count to %d, expected the remaining meetup to keep its seat",
			afterRetry.BookedCount)
	}

	// The withdrawn introduction returns to the arrangeable state.
	reopened, err := h.app.Matches.Get(h.ctx(), staffActor, first)
	if err != nil {
		t.Fatalf("read the withdrawn introduction: %v", err)
	}
	if reopened.Match.State != matching.StateConsented {
		t.Fatalf("expected the withdrawn introduction to be arrangeable again, got %s",
			reopened.Match.State)
	}

	// Only the one freed seat may be handed out again.
	if _, err := h.app.Schedule.Book(h.ctx(), staffActor, third, slotID); err != nil {
		t.Fatalf("expected the freed seat to be bookable: %v", err)
	}
	if _, err := h.app.Schedule.Book(h.ctx(), staffActor, first, slotID); err == nil {
		t.Fatal("expected the venue to refuse a meetup beyond its capacity")
	}
	final, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("read the venue slot after rebooking: %v", err)
	}
	if final.BookedCount > final.Capacity {
		t.Fatalf("the venue is oversold: booked_count=%d capacity=%d",
			final.BookedCount, final.Capacity)
	}
	if final.BookedCount != 2 {
		t.Fatalf("expected exactly two live meetups on the venue slot, got %d", final.BookedCount)
	}
}
