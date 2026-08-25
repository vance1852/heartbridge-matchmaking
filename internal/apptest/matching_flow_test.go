package apptest

import (
	"errors"
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/schedulesvc"
)

// allowance reads the active allowance of a member.
func (h *harness) allowance(memberID string) entitlement.Entitlement {
	h.t.Helper()
	granted, err := h.app.Repositories.Entitlements.GetActiveByMember(h.ctx(), memberID)
	if err != nil {
		h.t.Fatalf("read allowance of %s: %v", memberID, err)
	}
	return granted
}

// TestProposeReservesAllowanceForBothMembers checks the happy path of the
// matchmaking entry point: one introduction reserves exactly one introduction on
// each side and records both movements.
func TestProposeReservesAllowanceForBothMembers(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm1@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "lin@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "wei@heartbridge.test", Gender: "male"})

	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose introduction: %v", err)
	}
	if detail.Match.State != matching.StatePendingConsent {
		t.Fatalf("expected state %s, got %s", matching.StatePendingConsent, detail.Match.State)
	}
	if len(detail.Consents) != 2 {
		t.Fatalf("expected two consent rows, got %d", len(detail.Consents))
	}
	for _, consent := range detail.Consents {
		if consent.Decision != matching.DecisionPending {
			t.Fatalf("expected pending consent for %s, got %s", consent.MemberID, consent.Decision)
		}
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 1 || granted.Used != 0 {
			t.Fatalf("member %s: expected reserved=1 used=0, got reserved=%d used=%d",
				memberID, granted.Reserved, granted.Used)
		}
	}
	if len(detail.Ledger) != 2 {
		t.Fatalf("expected two allowance movements, got %d", len(detail.Ledger))
	}
	reserved, used := entitlement.Balance(detail.Ledger)
	if reserved != 2 || used != 0 {
		t.Fatalf("expected ledger balance reserved=2 used=0, got reserved=%d used=%d", reserved, used)
	}
	// The consent deadline comes from the starter plan window of 48 hours.
	expectedDeadline := anchor.Add(48 * time.Hour)
	if !detail.Match.ConsentDeadline.Equal(expectedDeadline) {
		t.Fatalf("expected deadline %s, got %s", expectedDeadline, detail.Match.ConsentDeadline)
	}
}

// TestProposeRejectsIncompatiblePreferences verifies the cross-entity eligibility
// rule and that a rejected proposal leaves no allowance behind.
func TestProposeRejectsIncompatiblePreferences(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm2@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "yan@heartbridge.test", Gender: "female"})
	// A member living in a city the counterpart does not accept.
	right := h.enrollMember(memberSpec{Email: "chen@heartbridge.test", Gender: "male", City: "Chengdu"})

	_, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err == nil {
		t.Fatal("expected the proposal to be rejected")
	}
	if code := apperr.CodeOf(err); code != apperr.CodePreconditionFailed {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodePreconditionFailed, code, err)
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 0 {
			t.Fatalf("member %s kept a reservation after a rejected proposal: %d",
				memberID, granted.Reserved)
		}
	}
	page, err := h.app.Matches.List(h.ctx(), matchmaker, repository.MatchFilter{})
	if err != nil {
		t.Fatalf("list introductions: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("expected no stored introduction, got %d", page.Total)
	}
}

// TestProposeRejectsDuplicateLivePair verifies the pair uniqueness invariant.
func TestProposeRejectsDuplicateLivePair(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm3@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "mei@heartbridge.test", Gender: "female", Plan: "premium"})
	right := h.enrollMember(memberSpec{Email: "hao@heartbridge.test", Gender: "male", Plan: "premium"})

	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err != nil {
		t.Fatalf("first proposal: %v", err)
	}
	// Swapping the arguments must hit the same uniqueness rule.
	_, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  right.MemberID,
		SecondMemberID: left.MemberID,
	})
	if err == nil {
		t.Fatal("expected the duplicate pair to be rejected")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeConflict {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeConflict, code, err)
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 1 {
			t.Fatalf("member %s: expected exactly one reservation, got %d", memberID, granted.Reserved)
		}
	}
}

// TestProposeRejectsExhaustedAllowance verifies the quota invariant across
// several sequential introductions.
func TestProposeRejectsExhaustedAllowance(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm4@heartbridge.test", "matchmaker")
	// The premium plan allows three live introductions, the starter plan two.
	focus := h.enrollMember(memberSpec{Email: "focus@heartbridge.test", Gender: "female", Plan: "premium"})
	partners := []memberFixture{
		h.enrollMember(memberSpec{Email: "p1@heartbridge.test", Gender: "male", Plan: "premium"}),
		h.enrollMember(memberSpec{Email: "p2@heartbridge.test", Gender: "male", Plan: "premium"}),
		h.enrollMember(memberSpec{Email: "p3@heartbridge.test", Gender: "male", Plan: "premium"}),
		h.enrollMember(memberSpec{Email: "p4@heartbridge.test", Gender: "male", Plan: "premium"}),
	}
	for index := 0; index < 3; index++ {
		if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
			FirstMemberID:  focus.MemberID,
			SecondMemberID: partners[index].MemberID,
		}); err != nil {
			t.Fatalf("proposal %d: %v", index, err)
		}
	}
	_, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  focus.MemberID,
		SecondMemberID: partners[3].MemberID,
	})
	if err == nil {
		t.Fatal("expected the fourth live introduction to be refused")
	}
	if code := apperr.CodeOf(err); code != apperr.CodePreconditionFailed {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodePreconditionFailed, code, err)
	}
	granted := h.allowance(focus.MemberID)
	if granted.Reserved != 3 {
		t.Fatalf("expected three reservations, got %d", granted.Reserved)
	}
}

// TestDeclineClosesIntroductionAndReturnsAllowance verifies the decline branch of
// the consent state machine and its allowance refund.
func TestDeclineClosesIntroductionAndReturnsAllowance(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm5@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "declineA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "declineB@heartbridge.test", Gender: "male"})

	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	after, err := h.app.Matches.Decide(h.ctx(), right.Actor, detail.Match.ID, matching.DecisionDeclined)
	if err != nil {
		t.Fatalf("decline: %v", err)
	}
	if after.Match.State != matching.StateClosedFailed {
		t.Fatalf("expected state %s, got %s", matching.StateClosedFailed, after.Match.State)
	}
	if after.Match.ClosedAt == nil {
		t.Fatal("expected a closing timestamp on a terminal introduction")
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 0 || granted.Used != 0 {
			t.Fatalf("member %s: expected the reservation to be returned, got reserved=%d used=%d",
				memberID, granted.Reserved, granted.Used)
		}
	}
	reserved, used := entitlement.Balance(after.Ledger)
	if reserved != 0 || used != 0 {
		t.Fatalf("expected a balanced ledger, got reserved=%d used=%d", reserved, used)
	}
	if len(after.Ledger) != 4 {
		t.Fatalf("expected two reserve and two release movements, got %d", len(after.Ledger))
	}
}

// TestSecondAnswerFromSameMemberIsRejected verifies that a member cannot revise
// their answer, which would otherwise corrupt the consent outcome.
func TestSecondAnswerFromSameMemberIsRejected(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm6@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "twiceA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "twiceB@heartbridge.test", Gender: "male"})
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := h.app.Matches.Decide(h.ctx(), left.Actor, detail.Match.ID, matching.DecisionAccepted); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	_, err = h.app.Matches.Decide(h.ctx(), left.Actor, detail.Match.ID, matching.DecisionDeclined)
	if err == nil {
		t.Fatal("expected the second answer to be rejected")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeConflict {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeConflict, code, err)
	}
}

// TestOutsiderCannotAnswerIntroduction verifies the participant ownership rule.
func TestOutsiderCannotAnswerIntroduction(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm7@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "ownA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "ownB@heartbridge.test", Gender: "male"})
	outsider := h.enrollMember(memberSpec{Email: "ownC@heartbridge.test", Gender: "female"})
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	_, err = h.app.Matches.Decide(h.ctx(), outsider.Actor, detail.Match.ID, matching.DecisionAccepted)
	if err == nil {
		t.Fatal("expected an outsider to be refused")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeForbidden {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeForbidden, code, err)
	}
	if _, err := h.app.Matches.Get(h.ctx(), outsider.Actor, detail.Match.ID); err == nil {
		t.Fatal("expected an outsider to be unable to read the introduction")
	}
}

// TestExpiryReleasesAllowanceExactlyOnce drives the sweeper twice over the same
// expired introduction and verifies the allowance is returned once only.
func TestExpiryReleasesAllowanceExactlyOnce(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm8@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "expA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "expB@heartbridge.test", Gender: "male"})
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	// Nothing is due before the deadline.
	if expired, err := h.app.Matches.ExpireDue(h.ctx(), h.adminActor(), 10); err != nil || expired != 0 {
		t.Fatalf("expected nothing to expire yet, got %d (%v)", expired, err)
	}
	h.clk.Advance(49 * time.Hour)

	expired, err := h.app.Matches.ExpireDue(h.ctx(), h.adminActor(), 10)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if expired != 1 {
		t.Fatalf("expected one expired introduction, got %d", expired)
	}
	again, err := h.app.Matches.ExpireDue(h.ctx(), h.adminActor(), 10)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again != 0 {
		t.Fatalf("expected the second sweep to find nothing, got %d", again)
	}

	after, err := h.app.Matches.Get(h.ctx(), matchmaker, detail.Match.ID)
	if err != nil {
		t.Fatalf("read expired introduction: %v", err)
	}
	if after.Match.State != matching.StateExpired {
		t.Fatalf("expected state %s, got %s", matching.StateExpired, after.Match.State)
	}
	releases := 0
	for _, entry := range after.Ledger {
		if entry.Reason == entitlement.ReasonRelease {
			releases++
		}
	}
	if releases != 2 {
		t.Fatalf("expected exactly one release per member, got %d", releases)
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 0 {
			t.Fatalf("member %s still holds %d reservations", memberID, granted.Reserved)
		}
	}
}

// TestAnswerAfterDeadlineIsRejected verifies the business deadline is enforced
// even before the sweeper ran.
func TestAnswerAfterDeadlineIsRejected(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm9@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "lateA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "lateB@heartbridge.test", Gender: "male"})
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	h.clk.Advance(48*time.Hour + time.Minute)
	_, err = h.app.Matches.Decide(h.ctx(), left.Actor, detail.Match.ID, matching.DecisionAccepted)
	if err == nil {
		t.Fatal("expected a late answer to be rejected")
	}
	if code := apperr.CodeOf(err); code != apperr.CodePreconditionFailed {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodePreconditionFailed, code, err)
	}
}

// TestFullLifecycleSettlesAllowanceAndClosesIntroduction walks the whole business
// path from proposal to a mutually successful closure.
func TestFullLifecycleSettlesAllowanceAndClosesIntroduction(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm10@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "lifeA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "lifeB@heartbridge.test", Gender: "male"})
	slotID := h.publishSlot("TH-1", 24*time.Hour, 2*time.Hour, 2)

	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	matchID := detail.Match.ID
	for _, fixture := range []memberFixture{left, right} {
		if _, err := h.app.Matches.Decide(h.ctx(), fixture.Actor, matchID, matching.DecisionAccepted); err != nil {
			t.Fatalf("accept as %s: %v", fixture.MemberID, err)
		}
	}
	consented, err := h.app.Matches.Get(h.ctx(), matchmaker, matchID)
	if err != nil {
		t.Fatalf("read consented introduction: %v", err)
	}
	if consented.Match.State != matching.StateConsented {
		t.Fatalf("expected state %s, got %s", matching.StateConsented, consented.Match.State)
	}

	booked, err := h.app.Schedule.Book(h.ctx(), matchmaker, matchID, slotID)
	if err != nil {
		t.Fatalf("book meetup: %v", err)
	}
	if booked.Meetup.State != meetup.StateBooked {
		t.Fatalf("expected state %s, got %s", meetup.StateBooked, booked.Meetup.State)
	}
	if booked.Slot.BookedCount != 1 {
		t.Fatalf("expected the slot to hold one booking, got %d", booked.Slot.BookedCount)
	}

	h.clk.Advance(25 * time.Hour)
	if _, err := h.app.Schedule.CheckIn(h.ctx(), matchmaker, booked.Meetup.ID); err != nil {
		t.Fatalf("check in: %v", err)
	}
	completed, err := h.app.Schedule.Complete(h.ctx(), matchmaker, booked.Meetup.ID)
	if err != nil {
		t.Fatalf("complete meetup: %v", err)
	}
	if completed.Match.State != matching.StateMet {
		t.Fatalf("expected state %s, got %s", matching.StateMet, completed.Match.State)
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := h.allowance(memberID)
		if granted.Used != 1 || granted.Reserved != 0 {
			t.Fatalf("member %s: expected used=1 reserved=0, got used=%d reserved=%d",
				memberID, granted.Used, granted.Reserved)
		}
	}

	for _, fixture := range []memberFixture{left, right} {
		if _, err := h.app.Schedule.SubmitFeedback(h.ctx(), fixture.Actor, booked.Meetup.ID,
			schedulesvc.FeedbackInput{Intent: meetup.IntentContinue, Rating: 5, Comment: "warm conversation"}); err != nil {
			t.Fatalf("feedback from %s: %v", fixture.MemberID, err)
		}
	}
	closed, err := h.app.Matches.Get(h.ctx(), matchmaker, matchID)
	if err != nil {
		t.Fatalf("read closed introduction: %v", err)
	}
	if closed.Match.State != matching.StateClosedSuccess {
		t.Fatalf("expected state %s, got %s", matching.StateClosedSuccess, closed.Match.State)
	}
	reserved, used := entitlement.Balance(closed.Ledger)
	if reserved != 0 || used != 2 {
		t.Fatalf("expected reserved=0 used=2 in the ledger, got reserved=%d used=%d", reserved, used)
	}
}

// TestFeedbackRequiresCompletedMeetupAndParticipation covers both guards of the
// feedback endpoint.
func TestFeedbackRequiresCompletedMeetupAndParticipation(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm11@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "fbA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "fbB@heartbridge.test", Gender: "male"})
	outsider := h.enrollMember(memberSpec{Email: "fbC@heartbridge.test", Gender: "female"})
	slotID := h.publishSlot("TH-2", 20*time.Hour, time.Hour, 2)

	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	for _, fixture := range []memberFixture{left, right} {
		if _, err := h.app.Matches.Decide(h.ctx(), fixture.Actor, detail.Match.ID, matching.DecisionAccepted); err != nil {
			t.Fatalf("accept: %v", err)
		}
	}
	booked, err := h.app.Schedule.Book(h.ctx(), matchmaker, detail.Match.ID, slotID)
	if err != nil {
		t.Fatalf("book: %v", err)
	}

	// Feedback before completion must be refused.
	_, err = h.app.Schedule.SubmitFeedback(h.ctx(), left.Actor, booked.Meetup.ID,
		schedulesvc.FeedbackInput{Intent: meetup.IntentContinue, Rating: 4})
	if code := apperr.CodeOf(err); code != apperr.CodePreconditionFailed {
		t.Fatalf("expected code %s before completion, got %s (%v)", apperr.CodePreconditionFailed, code, err)
	}

	h.clk.Advance(21 * time.Hour)
	if _, err := h.app.Schedule.CheckIn(h.ctx(), matchmaker, booked.Meetup.ID); err != nil {
		t.Fatalf("check in: %v", err)
	}
	if _, err := h.app.Schedule.Complete(h.ctx(), matchmaker, booked.Meetup.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// An outsider must be refused even after completion.
	_, err = h.app.Schedule.SubmitFeedback(h.ctx(), outsider.Actor, booked.Meetup.ID,
		schedulesvc.FeedbackInput{Intent: meetup.IntentContinue, Rating: 4})
	if code := apperr.CodeOf(err); code != apperr.CodeForbidden {
		t.Fatalf("expected code %s for an outsider, got %s (%v)", apperr.CodeForbidden, code, err)
	}

	if _, err := h.app.Schedule.SubmitFeedback(h.ctx(), left.Actor, booked.Meetup.ID,
		schedulesvc.FeedbackInput{Intent: meetup.IntentStop, Rating: 2}); err != nil {
		t.Fatalf("first feedback: %v", err)
	}
	// The same participant may not report twice.
	_, err = h.app.Schedule.SubmitFeedback(h.ctx(), left.Actor, booked.Meetup.ID,
		schedulesvc.FeedbackInput{Intent: meetup.IntentContinue, Rating: 5})
	if code := apperr.CodeOf(err); code != apperr.CodeConflict {
		t.Fatalf("expected code %s for a repeated report, got %s (%v)", apperr.CodeConflict, code, err)
	}

	final, err := h.app.Schedule.SubmitFeedback(h.ctx(), right.Actor, booked.Meetup.ID,
		schedulesvc.FeedbackInput{Intent: meetup.IntentContinue, Rating: 4})
	if err != nil {
		t.Fatalf("second feedback: %v", err)
	}
	if final.Match.State != matching.StateClosedFailed {
		t.Fatalf("expected a one-sided outcome to close as %s, got %s",
			matching.StateClosedFailed, final.Match.State)
	}
}

// TestIllegalTransitionsAreRefused checks that the state machine rejects the
// shortcuts a client could attempt.
func TestIllegalTransitionsAreRefused(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm12@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "stA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "stB@heartbridge.test", Gender: "male"})
	slotID := h.publishSlot("TH-3", 12*time.Hour, time.Hour, 1)
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	// Booking before both members accepted is not allowed.
	_, err = h.app.Schedule.Book(h.ctx(), matchmaker, detail.Match.ID, slotID)
	if err == nil {
		t.Fatal("expected booking to be refused while consent is pending")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeIllegalTransition {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeIllegalTransition, code, err)
	}

	if _, err := h.app.Matches.Cancel(h.ctx(), matchmaker, detail.Match.ID, "member unreachable"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// A terminal introduction cannot be cancelled again.
	_, err = h.app.Matches.Cancel(h.ctx(), matchmaker, detail.Match.ID, "second attempt")
	if code := apperr.CodeOf(err); code != apperr.CodeIllegalTransition {
		t.Fatalf("expected code %s on a terminal introduction, got %s (%v)",
			apperr.CodeIllegalTransition, code, err)
	}
	// And a member of a closed introduction cannot answer it any more.
	_, err = h.app.Matches.Decide(h.ctx(), left.Actor, detail.Match.ID, matching.DecisionAccepted)
	if code := apperr.CodeOf(err); code != apperr.CodeIllegalTransition {
		t.Fatalf("expected code %s when answering a cancelled introduction, got %s (%v)",
			apperr.CodeIllegalTransition, code, err)
	}
	if !errors.Is(err, apperr.New(apperr.CodeIllegalTransition, "")) {
		t.Fatal("expected the error to match the illegal transition sentinel by code")
	}
}

// TestCancelScheduledIntroductionReturnsSeatAndAllowance is the regression for a
// scheduled introduction that was withdrawn: the venue seat was freed but the
// reserved introduction of each member stayed held, leaving neither a refund in
// the ledger nor room for a new introduction. Cancelling a scheduled
// introduction must release both scarce resources together.
func TestCancelScheduledIntroductionReturnsSeatAndAllowance(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("mm13@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "schedA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "schedB@heartbridge.test", Gender: "male"})
	slotID := h.publishSlot("TH-4", 24*time.Hour, 2*time.Hour, 1)

	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	for _, fixture := range []memberFixture{left, right} {
		if _, err := h.app.Matches.Decide(h.ctx(), fixture.Actor, detail.Match.ID, matching.DecisionAccepted); err != nil {
			t.Fatalf("accept as %s: %v", fixture.MemberID, err)
		}
	}
	booked, err := h.app.Schedule.Book(h.ctx(), matchmaker, detail.Match.ID, slotID)
	if err != nil {
		t.Fatalf("book meetup: %v", err)
	}
	if booked.Match.State != matching.StateScheduled {
		t.Fatalf("expected state %s, got %s", matching.StateScheduled, booked.Match.State)
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		if granted := h.allowance(memberID); granted.Reserved != 1 {
			t.Fatalf("member %s: expected reserved=1 while scheduled, got %d", memberID, granted.Reserved)
		}
	}

	// A second introduction for the same pair is blocked while the first is live.
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err == nil {
		t.Fatal("expected the live pair to block a second proposal")
	}

	cancelled, err := h.app.Matches.Cancel(h.ctx(), matchmaker, detail.Match.ID, "venue incident")
	if err != nil {
		t.Fatalf("cancel scheduled introduction: %v", err)
	}
	if cancelled.Match.State != matching.StateCancelled {
		t.Fatalf("expected state %s, got %s", matching.StateCancelled, cancelled.Match.State)
	}

	// The venue seat is returned.
	slot, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("read slot: %v", err)
	}
	if slot.BookedCount != 0 {
		t.Fatalf("expected the slot to release its booking, got booked=%d", slot.BookedCount)
	}

	// Both members recover their reserved introduction and a ledger release is
	// recorded for each.
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 0 || granted.Used != 0 {
			t.Fatalf("member %s: expected reserved=0 used=0 after cancel, got reserved=%d used=%d",
				memberID, granted.Reserved, granted.Used)
		}
	}
	reserved, used := entitlement.Balance(cancelled.Ledger)
	if reserved != 0 || used != 0 {
		t.Fatalf("expected a balanced ledger after cancel, got reserved=%d used=%d", reserved, used)
	}
	var releases int
	for _, entry := range cancelled.Ledger {
		if entry.Reason == entitlement.ReasonRelease {
			releases++
		}
	}
	if releases != 2 {
		t.Fatalf("expected two release movements, got %d", releases)
	}

	// The freed allowance now allows a new introduction for the same pair.
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	}); err != nil {
		t.Fatalf("expected a new proposal to succeed after the scheduled introduction was cancelled: %v", err)
	}
}
