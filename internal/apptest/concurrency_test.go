package apptest

import (
	"sync"
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// consentedMatch creates an accepted introduction between two fresh members and
// returns its identifier.
func (h *harness) consentedMatch(matchmakerEmail, leftEmail, rightEmail string) string {
	h.t.Helper()
	matchmaker, _ := h.staff(matchmakerEmail, "matchmaker")
	left := h.enrollMember(memberSpec{Email: leftEmail, Gender: "female"})
	right := h.enrollMember(memberSpec{Email: rightEmail, Gender: "male"})
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		h.t.Fatalf("propose: %v", err)
	}
	for _, fixture := range []memberFixture{left, right} {
		if _, err := h.app.Matches.Decide(h.ctx(), fixture.Actor, detail.Match.ID, matching.DecisionAccepted); err != nil {
			h.t.Fatalf("accept: %v", err)
		}
	}
	return detail.Match.ID
}

// startGate returns a barrier that releases every goroutine at once, which makes
// the contention real without relying on sleeps.
func startGate() (wait func(), release func()) {
	gate := make(chan struct{})
	return func() { <-gate }, func() { close(gate) }
}

// TestConcurrentBookingsDoNotOversellVenueSlot verifies that the last seat of a
// venue slot can only be taken once, even when both bookings race.
func TestConcurrentBookingsDoNotOversellVenueSlot(t *testing.T) {
	h := newHarness(t)
	staffActor, _ := h.staff("slotstaff@heartbridge.test", "matchmaker")
	slotID := h.publishSlot("RACE-1", 30*time.Hour, time.Hour, 1)

	firstMatch := h.consentedMatch("mmr1@heartbridge.test", "r1a@heartbridge.test", "r1b@heartbridge.test")
	secondMatch := h.consentedMatch("mmr2@heartbridge.test", "r2a@heartbridge.test", "r2b@heartbridge.test")

	wait, release := startGate()
	results := make([]error, 2)
	var group sync.WaitGroup
	for index, matchID := range []string{firstMatch, secondMatch} {
		group.Add(1)
		go func(slot int, match string) {
			defer group.Done()
			wait()
			_, err := h.app.Schedule.Book(h.ctx(), staffActor, match, slotID)
			results[slot] = err
		}(index, matchID)
	}
	release()
	group.Wait()

	successes := 0
	for index, err := range results {
		if err == nil {
			successes++
			continue
		}
		if code := apperr.CodeOf(err); code != apperr.CodeCapacityExhausted {
			t.Fatalf("booking %d failed with %s instead of %s: %v",
				index, code, apperr.CodeCapacityExhausted, err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one successful booking, got %d", successes)
	}
	slot, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("read slot: %v", err)
	}
	if slot.BookedCount != 1 {
		t.Fatalf("expected booked_count=1, got %d", slot.BookedCount)
	}
	if slot.BookedCount > slot.Capacity {
		t.Fatalf("capacity invariant broken: %d booked of %d", slot.BookedCount, slot.Capacity)
	}
}

// TestConcurrentCancellationsProduceOneWinner verifies the optimistic version
// guard on the match state machine.
func TestConcurrentCancellationsProduceOneWinner(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mmc@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "cancelA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "cancelB@heartbridge.test", Gender: "male"})
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	const attempts = 4
	wait, release := startGate()
	errs := make([]error, attempts)
	var group sync.WaitGroup
	for index := 0; index < attempts; index++ {
		group.Add(1)
		go func(slot int) {
			defer group.Done()
			wait()
			_, err := h.app.Matches.Cancel(h.ctx(), matchmaker, detail.Match.ID, "concurrent withdrawal")
			errs[slot] = err
		}(index)
	}
	release()
	group.Wait()

	successes := 0
	for index, err := range errs {
		if err == nil {
			successes++
			continue
		}
		switch code := apperr.CodeOf(err); code {
		case apperr.CodeVersionConflict, apperr.CodeIllegalTransition, apperr.CodeConflict:
		default:
			t.Fatalf("cancellation %d failed with unexpected code %s: %v", index, code, err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one successful cancellation, got %d", successes)
	}

	final, err := h.app.Matches.Get(h.ctx(), matchmaker, detail.Match.ID)
	if err != nil {
		t.Fatalf("read introduction: %v", err)
	}
	if final.Match.State != matching.StateCancelled {
		t.Fatalf("expected state %s, got %s", matching.StateCancelled, final.Match.State)
	}
	releases := 0
	for _, entry := range final.Ledger {
		if entry.Reason == entitlement.ReasonRelease {
			releases++
		}
	}
	if releases != 2 {
		t.Fatalf("expected exactly one release per member even under contention, got %d", releases)
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 0 {
			t.Fatalf("member %s still holds %d reservations", memberID, granted.Reserved)
		}
	}
}

// TestConcurrentProposalsRespectAllowanceInvariant races several proposals that
// all involve the same member and verifies the allowance never overcommits.
func TestConcurrentProposalsRespectAllowanceInvariant(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mmp@heartbridge.test", "matchmaker")
	focus := h.enrollMember(memberSpec{Email: "hub@heartbridge.test", Gender: "female", Plan: "premium"})
	partners := make([]memberFixture, 0, 5)
	for index := 0; index < 5; index++ {
		partners = append(partners, h.enrollMember(memberSpec{
			Email:  string(rune('a'+index)) + "hub@heartbridge.test",
			Gender: "male",
			Plan:   "premium",
		}))
	}

	wait, release := startGate()
	errs := make([]error, len(partners))
	var group sync.WaitGroup
	for index, partner := range partners {
		group.Add(1)
		go func(slot int, partnerID string) {
			defer group.Done()
			wait()
			_, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
				FirstMemberID:  focus.MemberID,
				SecondMemberID: partnerID,
			})
			errs[slot] = err
		}(index, partner.MemberID)
	}
	release()
	group.Wait()

	successes := 0
	for index, err := range errs {
		if err == nil {
			successes++
			continue
		}
		switch code := apperr.CodeOf(err); code {
		case apperr.CodeVersionConflict, apperr.CodePreconditionFailed, apperr.CodeQuotaExhausted, apperr.CodeConflict:
		default:
			t.Fatalf("proposal %d failed with unexpected code %s: %v", index, code, err)
		}
	}
	if successes == 0 {
		t.Fatal("expected at least one proposal to succeed")
	}
	// The premium plan allows three concurrent introductions.
	if successes > 3 {
		t.Fatalf("the concurrent match limit was exceeded: %d live introductions", successes)
	}

	granted := h.allowance(focus.MemberID)
	if granted.Reserved != successes {
		t.Fatalf("expected reserved=%d to equal the number of successes, got %d",
			successes, granted.Reserved)
	}
	if granted.Used+granted.Reserved > granted.Total {
		t.Fatalf("allowance invariant broken: used=%d reserved=%d total=%d",
			granted.Used, granted.Reserved, granted.Total)
	}
	movements, err := h.app.Repositories.Entitlements.ListLedgerByEntitlement(h.ctx(), granted.ID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	reservedInLedger, usedInLedger := entitlement.Balance(movements)
	if reservedInLedger != granted.Reserved || usedInLedger != granted.Used {
		t.Fatalf("ledger disagrees with the allowance row: ledger reserved=%d used=%d, row reserved=%d used=%d",
			reservedInLedger, usedInLedger, granted.Reserved, granted.Used)
	}
}

// TestConcurrentAcceptancesReachConsentedExactlyOnce races the two answers of one
// introduction and verifies the outcome is derived exactly once.
func TestConcurrentAcceptancesReachConsentedExactlyOnce(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mma@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "raceAcceptA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "raceAcceptB@heartbridge.test", Gender: "male"})
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	wait, release := startGate()
	errs := make([]error, 2)
	var group sync.WaitGroup
	for index, fixture := range []memberFixture{left, right} {
		group.Add(1)
		go func(slot int, participant memberFixture) {
			defer group.Done()
			wait()
			_, err := h.app.Matches.Decide(h.ctx(), participant.Actor,
				detail.Match.ID, matching.DecisionAccepted)
			errs[slot] = err
		}(index, fixture)
	}
	release()
	group.Wait()

	for index, err := range errs {
		if err != nil {
			t.Fatalf("answer %d failed: %v", index, err)
		}
	}
	final, err := h.app.Matches.Get(h.ctx(), matchmaker, detail.Match.ID)
	if err != nil {
		t.Fatalf("read introduction: %v", err)
	}
	if final.Match.State != matching.StateConsented {
		t.Fatalf("expected state %s, got %s", matching.StateConsented, final.Match.State)
	}
	if final.Match.Version != 2 {
		t.Fatalf("expected exactly one state transition, version is %d", final.Match.Version)
	}
	outcome := matching.SummarizeConsents(final.Consents)
	if outcome.Accepted != 2 || outcome.Pending != 0 {
		t.Fatalf("expected both answers recorded, got %+v", outcome)
	}
}

// TestOverlappingMeetupsForSameMemberAreRejected verifies the time conflict rule
// across two different introductions of the same member.
func TestOverlappingMeetupsForSameMemberAreRejected(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mmo@heartbridge.test", "matchmaker")
	focus := h.enrollMember(memberSpec{Email: "busy@heartbridge.test", Gender: "female", Plan: "premium"})
	firstPartner := h.enrollMember(memberSpec{Email: "busy1@heartbridge.test", Gender: "male", Plan: "premium"})
	secondPartner := h.enrollMember(memberSpec{Email: "busy2@heartbridge.test", Gender: "male", Plan: "premium"})

	matchIDs := make([]string, 0, 2)
	for _, partner := range []memberFixture{firstPartner, secondPartner} {
		detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
			FirstMemberID:  focus.MemberID,
			SecondMemberID: partner.MemberID,
		})
		if err != nil {
			t.Fatalf("propose: %v", err)
		}
		for _, fixture := range []memberFixture{focus, partner} {
			if _, err := h.app.Matches.Decide(h.ctx(), fixture.Actor, detail.Match.ID,
				matching.DecisionAccepted); err != nil {
				t.Fatalf("accept: %v", err)
			}
		}
		matchIDs = append(matchIDs, detail.Match.ID)
	}

	firstSlot := h.publishSlot("OVL-1", 40*time.Hour, 2*time.Hour, 4)
	// The second window starts one hour into the first one.
	secondSlot := h.publishSlot("OVL-2", 41*time.Hour, 2*time.Hour, 4)
	// And this one begins exactly when the first ends, which is not an overlap.
	adjacentSlot := h.publishSlot("OVL-3", 42*time.Hour, time.Hour, 4)

	if _, err := h.app.Schedule.Book(h.ctx(), matchmaker, matchIDs[0], firstSlot); err != nil {
		t.Fatalf("book first meetup: %v", err)
	}
	_, err := h.app.Schedule.Book(h.ctx(), matchmaker, matchIDs[1], secondSlot)
	if err == nil {
		t.Fatal("expected the overlapping booking to be rejected")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeConflict {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeConflict, code, err)
	}
	if _, err := h.app.Schedule.Book(h.ctx(), matchmaker, matchIDs[1], adjacentSlot); err != nil {
		t.Fatalf("expected the adjacent window to be bookable: %v", err)
	}
}
