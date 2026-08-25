package meetup

import (
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
)

// reference is the instant used by the booking assertions.
var reference = time.Date(2026, time.March, 2, 2, 0, 0, 0, time.UTC)

// newSlot builds a venue slot fixture.
func newSlot() VenueSlot {
	start := reference.Add(24 * time.Hour)
	return VenueSlot{
		ID:          "slt_1",
		VenueCode:   "TH-1",
		VenueName:   "Tea house",
		City:        "Hangzhou",
		StartAt:     start,
		EndAt:       start.Add(2 * time.Hour),
		Capacity:    2,
		BookedCount: 0,
		Version:     1,
		CreatedAt:   reference,
		UpdatedAt:   reference,
	}
}

// TestSlotValidationRejectsBrokenWindows covers the structural rules.
func TestSlotValidationRejectsBrokenWindows(t *testing.T) {
	if err := newSlot().Validate(); err != nil {
		t.Fatalf("expected a well formed slot to validate: %v", err)
	}
	cases := map[string]func(VenueSlot) VenueSlot{
		"blank code":     func(s VenueSlot) VenueSlot { s.VenueCode = " "; return s },
		"blank name":     func(s VenueSlot) VenueSlot { s.VenueName = ""; return s },
		"blank city":     func(s VenueSlot) VenueSlot { s.City = ""; return s },
		"missing window": func(s VenueSlot) VenueSlot { s.StartAt = time.Time{}; return s },
		"empty window":   func(s VenueSlot) VenueSlot { s.EndAt = s.StartAt; return s },
		"zero capacity":  func(s VenueSlot) VenueSlot { s.Capacity = 0; return s },
		"overbooked":     func(s VenueSlot) VenueSlot { s.BookedCount = s.Capacity + 1; return s },
		"negative count": func(s VenueSlot) VenueSlot { s.BookedCount = -1; return s },
	}
	for name, mutate := range cases {
		if err := mutate(newSlot()).Validate(); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}
}

// TestSlotBookabilityRules covers capacity and lead time.
func TestSlotBookabilityRules(t *testing.T) {
	slot := newSlot()
	if err := slot.Bookable(reference); err != nil {
		t.Fatalf("expected a future slot with room to be bookable: %v", err)
	}
	if !slot.HasRoom() {
		t.Fatal("expected an empty slot to have room")
	}

	full := newSlot()
	full.BookedCount = full.Capacity
	err := full.Bookable(reference)
	if code := apperr.CodeOf(err); code != apperr.CodeCapacityExhausted {
		t.Fatalf("expected code %s for a full slot, got %s (%v)", apperr.CodeCapacityExhausted, code, err)
	}
	if full.HasRoom() {
		t.Fatal("expected a full slot to report no room")
	}

	started := newSlot()
	if code := apperr.CodeOf(started.Bookable(started.StartAt)); code != apperr.CodePreconditionFailed {
		t.Fatalf("expected a started slot to report %s", apperr.CodePreconditionFailed)
	}
	if code := apperr.CodeOf(started.Bookable(started.StartAt.Add(time.Hour))); code != apperr.CodePreconditionFailed {
		t.Fatalf("expected a past slot to report %s", apperr.CodePreconditionFailed)
	}
}

// TestSlotOverlapExcludesTouchingWindows verifies the interval semantics used by
// the conflict detection.
func TestSlotOverlapExcludesTouchingWindows(t *testing.T) {
	slot := newSlot()
	if !slot.Overlaps(slot.StartAt.Add(time.Hour), slot.EndAt.Add(time.Hour)) {
		t.Fatal("expected a partially covering window to overlap")
	}
	if !slot.Overlaps(slot.StartAt.Add(-time.Hour), slot.EndAt.Add(time.Hour)) {
		t.Fatal("expected an enclosing window to overlap")
	}
	if slot.Overlaps(slot.EndAt, slot.EndAt.Add(time.Hour)) {
		t.Fatal("expected a window starting exactly at the end not to overlap")
	}
	if slot.Overlaps(slot.StartAt.Add(-time.Hour), slot.StartAt) {
		t.Fatal("expected a window ending exactly at the start not to overlap")
	}
}

// TestSlotBusinessDateUsesBusinessTimezone verifies the operator facing day label.
func TestSlotBusinessDateUsesBusinessTimezone(t *testing.T) {
	location := clock.BusinessLocation()
	slot := newSlot()
	// 22:00 UTC is already the next calendar day in the business timezone.
	slot.StartAt = time.Date(2026, time.March, 2, 22, 0, 0, 0, time.UTC)
	slot.EndAt = slot.StartAt.Add(time.Hour)
	expected := slot.StartAt.In(location).Format("2006-01-02")
	if slot.BusinessDate() != expected {
		t.Fatalf("expected %s, got %s", expected, slot.BusinessDate())
	}
	if slot.BusinessDate() == slot.StartAt.UTC().Format("2006-01-02") {
		t.Fatal("expected the business day to differ from the UTC day for a late window")
	}
}

// TestMeetupStateMachine walks the transition table of a meetup.
func TestMeetupStateMachine(t *testing.T) {
	allowed := map[State][]State{
		StateBooked:    {StateCheckedIn, StateCancelled, StateNoShow},
		StateCheckedIn: {StateCompleted, StateNoShow},
	}
	every := []State{StateBooked, StateCheckedIn, StateCompleted, StateCancelled, StateNoShow}
	for _, from := range every {
		appointment := Meetup{ID: "mtp_1", MatchID: "mch_1", SlotID: "slt_1", State: from, Version: 1}
		permitted := map[State]bool{}
		for _, target := range allowed[from] {
			permitted[target] = true
		}
		for _, to := range every {
			err := appointment.CanTransition(to)
			if permitted[to] {
				if err != nil {
					t.Fatalf("expected %s -> %s to be allowed: %v", from, to, err)
				}
				continue
			}
			if err == nil {
				t.Fatalf("expected %s -> %s to be refused", from, to)
			}
			if code := apperr.CodeOf(err); code != apperr.CodeIllegalTransition {
				t.Fatalf("expected code %s, got %s", apperr.CodeIllegalTransition, code)
			}
		}
	}
	if err := (Meetup{State: StateBooked}).CanTransition(State("halfway")); err == nil {
		t.Fatal("expected an unknown target state to be refused")
	}
}

// TestMeetupSlotOwnershipPerState documents which states hold a venue seat.
func TestMeetupSlotOwnershipPerState(t *testing.T) {
	holding := map[State]bool{StateBooked: true, StateCheckedIn: true, StateCompleted: true}
	for _, state := range []State{StateBooked, StateCheckedIn, StateCompleted, StateCancelled, StateNoShow} {
		if state.OccupiesSlot() != holding[state] {
			t.Fatalf("unexpected seat ownership for %s: %v", state, state.OccupiesSlot())
		}
	}
	if len(ActiveStates()) != 3 {
		t.Fatalf("expected three live states, got %d", len(ActiveStates()))
	}
	for _, state := range []State{StateCompleted, StateCancelled, StateNoShow} {
		if !state.Terminal() {
			t.Fatalf("expected %s to be terminal", state)
		}
	}
}

// TestMeetupValidationAndClone covers the structural rules and the deep copy.
func TestMeetupValidationAndClone(t *testing.T) {
	checkedIn := reference.Add(time.Hour)
	completed := reference.Add(2 * time.Hour)
	closed := reference.Add(3 * time.Hour)
	appointment := Meetup{
		ID: "mtp_1", MatchID: "mch_1", SlotID: "slt_1", State: StateCompleted, Version: 3,
		BookedAt: reference, CheckedInAt: &checkedIn, CompletedAt: &completed, ClosedAt: &closed,
	}
	if err := appointment.Validate(); err != nil {
		t.Fatalf("expected a complete meetup to validate: %v", err)
	}
	if err := (Meetup{SlotID: "slt_1", State: StateBooked}).Validate(); err == nil {
		t.Fatal("expected a meetup without a match to be refused")
	}
	if err := (Meetup{MatchID: "mch_1", State: StateBooked}).Validate(); err == nil {
		t.Fatal("expected a meetup without a slot to be refused")
	}
	if err := (Meetup{MatchID: "mch_1", SlotID: "slt_1", State: "arrived"}).Validate(); err == nil {
		t.Fatal("expected an unknown state to be refused")
	}

	copied := appointment.Clone()
	*copied.CheckedInAt = copied.CheckedInAt.Add(time.Hour)
	*copied.CompletedAt = copied.CompletedAt.Add(time.Hour)
	*copied.ClosedAt = copied.ClosedAt.Add(time.Hour)
	if appointment.CheckedInAt.Equal(*copied.CheckedInAt) ||
		appointment.CompletedAt.Equal(*copied.CompletedAt) ||
		appointment.ClosedAt.Equal(*copied.ClosedAt) {
		t.Fatal("expected the clone to own every optional timestamp")
	}
}

// TestBookingConflictDetection verifies the read model used for overlap checks.
func TestBookingConflictDetection(t *testing.T) {
	start := reference.Add(24 * time.Hour)
	booking := Booking{
		Meetup:  Meetup{ID: "mtp_1", MatchID: "mch_1", SlotID: "slt_1", State: StateBooked},
		StartAt: start,
		EndAt:   start.Add(2 * time.Hour),
	}
	if !booking.ConflictsWith(start.Add(time.Hour), start.Add(3*time.Hour)) {
		t.Fatal("expected an overlapping window to conflict")
	}
	if booking.ConflictsWith(booking.EndAt, booking.EndAt.Add(time.Hour)) {
		t.Fatal("expected an adjacent window not to conflict")
	}
	if booking.ConflictsWith(start.Add(-3*time.Hour), start.Add(-time.Hour)) {
		t.Fatal("expected an earlier window not to conflict")
	}
	copied := booking.Clone()
	copied.Meetup.State = StateCancelled
	if booking.Meetup.State == StateCancelled {
		t.Fatal("expected the booking clone to be independent")
	}
}

// TestFeedbackValidationRules covers the report rules.
func TestFeedbackValidationRules(t *testing.T) {
	base := Feedback{
		ID: "fbk_1", MeetupID: "mtp_1", AuthorMemberID: "mbr_a",
		Intent: IntentContinue, Rating: 4, Comment: "pleasant", CreatedAt: reference,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("expected a complete report to validate: %v", err)
	}
	cases := map[string]func(Feedback) Feedback{
		"no meetup":       func(f Feedback) Feedback { f.MeetupID = ""; return f },
		"no author":       func(f Feedback) Feedback { f.AuthorMemberID = ""; return f },
		"unknown intent":  func(f Feedback) Feedback { f.Intent = "unsure"; return f },
		"rating too low":  func(f Feedback) Feedback { f.Rating = 0; return f },
		"rating too high": func(f Feedback) Feedback { f.Rating = 6; return f },
	}
	for name, mutate := range cases {
		if err := mutate(base).Validate(); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}
	long := base
	long.Comment = string(make([]byte, 0, 501))
	for i := 0; i < 501; i++ {
		long.Comment += "a"
	}
	if err := long.Validate(); err == nil {
		t.Fatal("expected an oversized comment to be refused")
	}
}

// TestFeedbackAggregation covers the closure rules derived from the reports.
func TestFeedbackAggregation(t *testing.T) {
	single := []Feedback{{AuthorMemberID: "mbr_a", Intent: IntentContinue, Rating: 5}}
	summary := SummarizeFeedback(single)
	if summary.Complete() {
		t.Fatal("expected one report not to complete the meetup")
	}
	if summary.MutualContinue() {
		t.Fatal("expected one report not to imply a mutual outcome")
	}

	both := append(single, Feedback{AuthorMemberID: "mbr_b", Intent: IntentContinue, Rating: 4})
	summary = SummarizeFeedback(both)
	if !summary.Complete() || !summary.MutualContinue() {
		t.Fatalf("expected two positive reports to be a mutual success: %+v", summary)
	}
	mixed := []Feedback{
		{AuthorMemberID: "mbr_a", Intent: IntentContinue, Rating: 5},
		{AuthorMemberID: "mbr_b", Intent: IntentStop, Rating: 2},
	}
	summary = SummarizeFeedback(mixed)
	if !summary.Complete() || summary.MutualContinue() {
		t.Fatalf("expected a one-sided outcome: %+v", summary)
	}
	if summary.Continue != 1 || summary.Stop != 1 {
		t.Fatalf("unexpected counters: %+v", summary)
	}
	if !AuthoredBy(mixed, "mbr_b") || AuthoredBy(mixed, "mbr_c") {
		t.Fatal("expected the author lookup to follow the report list")
	}
	if err := Intent("maybe").Validate(); err == nil {
		t.Fatal("expected an unknown intent to be refused")
	}
}
