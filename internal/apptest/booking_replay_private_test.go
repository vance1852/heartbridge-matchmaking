package apptest

import (
	"net/http"
	"testing"
	"time"
)

// TestMeetupBookingReplayStaysWithinItsIntroduction checks that a retry safety
// key reused by the same consultant for a different introduction still books
// that introduction, and that repeating the very same call replays its own
// result.
func TestMeetupBookingReplayStaysWithinItsIntroduction(t *testing.T) {
	h := newHarness(t)
	_, staffToken := h.staff("replaystaff@heartbridge.test", "matchmaker")
	firstMatch := h.consentedMatch("rp1@heartbridge.test", "rp1a@heartbridge.test", "rp1b@heartbridge.test")
	secondMatch := h.consentedMatch("rp2@heartbridge.test", "rp2a@heartbridge.test", "rp2b@heartbridge.test")
	slotID := h.publishSlot("REPLAY-1", 36*time.Hour, 2*time.Hour, 4)

	payload := map[string]any{"slot_id": slotID}
	retryKey := map[string]string{"Idempotency-Key": "evening-tea-retry"}

	first := h.do(http.MethodPost, "/api/v1/matches/"+firstMatch+"/meetup", staffToken, payload, retryKey)
	if first.Status != http.StatusCreated {
		t.Fatalf("expected 201 for the first booking, got %d (%s)", first.Status, first.Raw)
	}
	firstMeetupID := first.str("id")
	if firstMeetupID == "" || first.str("match_id") != firstMatch {
		t.Fatalf("first booking did not describe its own introduction: %s", first.Raw)
	}

	second := h.do(http.MethodPost, "/api/v1/matches/"+secondMatch+"/meetup", staffToken, payload, retryKey)
	if second.Status != http.StatusCreated {
		t.Fatalf("expected 201 for the second introduction, got %d (%s)", second.Status, second.Raw)
	}
	if second.Headers.Get("Idempotent-Replay") == "true" {
		t.Fatalf("the second introduction was answered with a replay of an unrelated booking: %s", second.Raw)
	}
	if second.str("match_id") != secondMatch {
		t.Fatalf("expected the booking to belong to %s, got %s (%s)",
			secondMatch, second.str("match_id"), second.Raw)
	}
	if second.str("id") == firstMeetupID {
		t.Fatalf("both introductions were given the same meetup %s", firstMeetupID)
	}

	secondState := h.do(http.MethodGet, "/api/v1/matches/"+secondMatch, staffToken, nil, nil)
	if secondState.str("state") != "scheduled" {
		t.Fatalf("expected the second introduction to be scheduled, got %q (%s)",
			secondState.str("state"), secondState.Raw)
	}

	slot, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("read venue slot: %v", err)
	}
	if slot.BookedCount != 2 {
		t.Fatalf("expected both introductions to hold a seat, booked_count=%d", slot.BookedCount)
	}

	replay := h.do(http.MethodPost, "/api/v1/matches/"+secondMatch+"/meetup", staffToken, payload, retryKey)
	if replay.Status != http.StatusCreated || replay.str("id") != second.str("id") {
		t.Fatalf("repeating the same booking must replay its own result, got %d (%s)",
			replay.Status, replay.Raw)
	}
	if slotAfter, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID); err != nil {
		t.Fatalf("re-read venue slot: %v", err)
	} else if slotAfter.BookedCount != 2 {
		t.Fatalf("a replayed booking consumed another seat, booked_count=%d", slotAfter.BookedCount)
	}
}
