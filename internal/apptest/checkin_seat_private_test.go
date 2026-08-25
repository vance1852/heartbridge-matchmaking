package apptest

import (
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
)

// TestCheckInKeepsTheVenueSeatUntilTheMeetupEnds checks that confirming the
// arrival of a pair does not hand their table back to the pool, so a full venue
// slot stays full until a meetup is actually called off.
func TestCheckInKeepsTheVenueSeatUntilTheMeetupEnds(t *testing.T) {
	h := newHarness(t)
	staffActor, _ := h.staff("checkinmm@heartbridge.test", "matchmaker")
	slotID := h.publishSlot("CHECKIN-1", 26*time.Hour, 2*time.Hour, 2)

	firstMatch := h.consentedMatch("ci1@heartbridge.test", "ci1a@heartbridge.test", "ci1b@heartbridge.test")
	secondMatch := h.consentedMatch("ci2@heartbridge.test", "ci2a@heartbridge.test", "ci2b@heartbridge.test")
	thirdMatch := h.consentedMatch("ci3@heartbridge.test", "ci3a@heartbridge.test", "ci3b@heartbridge.test")

	firstMeetup, err := h.app.Schedule.Book(h.ctx(), staffActor, firstMatch, slotID)
	if err != nil {
		t.Fatalf("book the first meetup: %v", err)
	}
	secondMeetup, err := h.app.Schedule.Book(h.ctx(), staffActor, secondMatch, slotID)
	if err != nil {
		t.Fatalf("book the second meetup: %v", err)
	}

	h.clk.Advance(27 * time.Hour)
	checkedIn, err := h.app.Schedule.CheckIn(h.ctx(), staffActor, firstMeetup.Meetup.ID)
	if err != nil {
		t.Fatalf("confirm the arrival: %v", err)
	}
	if checkedIn.Meetup.State != meetup.StateCheckedIn {
		t.Fatalf("expected the meetup to be checked in, got %s", checkedIn.Meetup.State)
	}

	afterCheckIn, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("read the venue slot: %v", err)
	}
	if afterCheckIn.BookedCount != 2 {
		t.Fatalf("confirming an arrival changed the occupied tables to %d, both meetups still need theirs",
			afterCheckIn.BookedCount)
	}

	// The venue is full, so a third pair must not be squeezed in.
	_, err = h.app.Schedule.Book(h.ctx(), staffActor, thirdMatch, slotID)
	if err == nil {
		t.Fatal("expected the full venue slot to refuse a third meetup")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeCapacityExhausted {
		t.Fatalf("expected the venue to report exhausted capacity, got %s (%v)", code, err)
	}

	// Calling off the other meetup frees exactly one table for the waiting pair.
	if _, err := h.app.Schedule.Cancel(h.ctx(), staffActor, secondMeetup.Meetup.ID, "男方临时加班"); err != nil {
		t.Fatalf("call off the second meetup: %v", err)
	}
	freed, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("re-read the venue slot: %v", err)
	}
	if freed.BookedCount != 1 {
		t.Fatalf("expected exactly one occupied table after the withdrawal, got %d", freed.BookedCount)
	}
	if _, err := h.app.Schedule.Book(h.ctx(), staffActor, thirdMatch, slotID); err != nil {
		t.Fatalf("expected the freed table to be bookable: %v", err)
	}
	final, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("read the venue slot after rebooking: %v", err)
	}
	if final.BookedCount > final.Capacity {
		t.Fatalf("the venue is oversold: booked_count=%d capacity=%d",
			final.BookedCount, final.Capacity)
	}
}
