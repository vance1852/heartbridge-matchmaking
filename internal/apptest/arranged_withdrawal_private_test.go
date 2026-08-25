package apptest

import (
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// TestWithdrawingArrangedIntroductionReturnsSeatAndAllowance checks that calling
// off an introduction after its meetup was arranged returns both the venue seat
// and the introduction each member had on hold, so the pair can be introduced
// again.
func TestWithdrawingArrangedIntroductionReturnsSeatAndAllowance(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("arrangedmm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "ara@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "arb@heartbridge.test", Gender: "male"})
	slotID := h.publishSlot("ARRANGED-1", 28*time.Hour, 2*time.Hour, 2)

	matchID := h.consentedMatchBetween(matchmaker, left, right)
	booked, err := h.app.Schedule.Book(h.ctx(), matchmaker, matchID, slotID)
	if err != nil {
		t.Fatalf("arrange the meetup: %v", err)
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		if granted := h.allowance(memberID); granted.Reserved != 1 {
			t.Fatalf("member %s should hold one introduction before the withdrawal, got %d",
				memberID, granted.Reserved)
		}
	}

	withdrawn, err := h.app.Matches.Cancel(h.ctx(), matchmaker, matchID, "女方家里有事，本轮先停")
	if err != nil {
		t.Fatalf("withdraw the arranged introduction: %v", err)
	}
	if withdrawn.Match.State != matching.StateCancelled {
		t.Fatalf("expected the introduction to be cancelled, got %s", withdrawn.Match.State)
	}

	releases := 0
	for _, entry := range withdrawn.Ledger {
		if entry.Reason == entitlement.ReasonRelease {
			releases++
		}
	}
	if releases != 2 {
		t.Fatalf("expected one returned introduction per member, got %d ledger entries", releases)
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 0 {
			t.Fatalf("member %s still holds %d introductions after the withdrawal",
				memberID, granted.Reserved)
		}
		if granted.Used != 0 {
			t.Fatalf("member %s was charged for a meetup that never happened: used=%d",
				memberID, granted.Used)
		}
		if granted.Available() != granted.Total {
			t.Fatalf("member %s did not get the full allowance back: available=%d total=%d",
				memberID, granted.Available(), granted.Total)
		}
	}

	appointment, err := h.app.Repositories.Meetups.GetByID(h.ctx(), booked.Meetup.ID)
	if err != nil {
		t.Fatalf("read the meetup: %v", err)
	}
	if appointment.State != meetup.StateCancelled {
		t.Fatalf("expected the arranged meetup to be called off, got %s", appointment.State)
	}
	slot, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("read the venue slot: %v", err)
	}
	if slot.BookedCount != 0 {
		t.Fatalf("expected the venue seat to be returned, booked_count=%d", slot.BookedCount)
	}

	// The pair can be introduced again with the allowance they got back.
	reintroduced := h.consentedMatchBetween(matchmaker, left, right)
	if reintroduced == matchID {
		t.Fatalf("expected a new introduction, got the cancelled one %s", matchID)
	}
	if _, err := h.app.Schedule.Book(h.ctx(), matchmaker, reintroduced, slotID); err != nil {
		t.Fatalf("expected the returned seat to be bookable again: %v", err)
	}
}

// consentedMatchBetween proposes an introduction for two already enrolled members
// and records both acceptances.
func (h *harness) consentedMatchBetween(
	matchmaker identity.Actor, left memberFixture, right memberFixture,
) string {
	h.t.Helper()
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		h.t.Fatalf("propose introduction: %v", err)
	}
	for _, fixture := range []memberFixture{left, right} {
		if _, err := h.app.Matches.Decide(h.ctx(), fixture.Actor, detail.Match.ID,
			matching.DecisionAccepted); err != nil {
			h.t.Fatalf("accept as %s: %v", fixture.MemberID, err)
		}
	}
	return detail.Match.ID
}
