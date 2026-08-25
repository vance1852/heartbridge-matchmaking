package apptest

import (
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// TestAcceptedIntroductionSurvivesTheAnswerDeadlineSweep checks that the
// background sweep only retires introductions that are still waiting for an
// answer, and leaves an introduction both members already accepted untouched
// together with the allowance it holds.
func TestAcceptedIntroductionSurvivesTheAnswerDeadlineSweep(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("sweepguard@heartbridge.test", "matchmaker")
	acceptedLeft := h.enrollMember(memberSpec{Email: "sga@heartbridge.test", Gender: "female"})
	acceptedRight := h.enrollMember(memberSpec{Email: "sgb@heartbridge.test", Gender: "male"})
	silentLeft := h.enrollMember(memberSpec{Email: "sgc@heartbridge.test", Gender: "female"})
	silentRight := h.enrollMember(memberSpec{Email: "sgd@heartbridge.test", Gender: "male"})

	accepted, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  acceptedLeft.MemberID,
		SecondMemberID: acceptedRight.MemberID,
	})
	if err != nil {
		t.Fatalf("propose the answered introduction: %v", err)
	}
	for _, fixture := range []memberFixture{acceptedLeft, acceptedRight} {
		if _, err := h.app.Matches.Decide(h.ctx(), fixture.Actor, accepted.Match.ID,
			matching.DecisionAccepted); err != nil {
			t.Fatalf("accept as %s: %v", fixture.MemberID, err)
		}
	}
	silent, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  silentLeft.MemberID,
		SecondMemberID: silentRight.MemberID,
	})
	if err != nil {
		t.Fatalf("propose the unanswered introduction: %v", err)
	}

	// Both consent deadlines have elapsed by now.
	h.clk.Advance(49 * time.Hour)
	swept, err := h.app.Matches.ExpireDue(h.ctx(), h.adminActor(), 20)
	if err != nil {
		t.Fatalf("run the deadline sweep: %v", err)
	}
	if swept != 1 {
		t.Fatalf("expected only the unanswered introduction to be retired, got %d", swept)
	}

	survivor, err := h.app.Matches.Get(h.ctx(), matchmaker, accepted.Match.ID)
	if err != nil {
		t.Fatalf("read the answered introduction: %v", err)
	}
	if survivor.Match.State != matching.StateConsented {
		t.Fatalf("the answered introduction was retired: state=%s", survivor.Match.State)
	}
	for _, entry := range survivor.Ledger {
		if entry.Reason == entitlement.ReasonRelease {
			t.Fatalf("the answered introduction had its allowance returned: %+v", entry)
		}
	}
	for _, memberID := range []string{acceptedLeft.MemberID, acceptedRight.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 1 {
			t.Fatalf("member %s lost the introduction it still holds: reserved=%d",
				memberID, granted.Reserved)
		}
	}

	retired, err := h.app.Matches.Get(h.ctx(), matchmaker, silent.Match.ID)
	if err != nil {
		t.Fatalf("read the unanswered introduction: %v", err)
	}
	if retired.Match.State != matching.StateExpired {
		t.Fatalf("expected the unanswered introduction to be expired, got %s", retired.Match.State)
	}
	for _, memberID := range []string{silentLeft.MemberID, silentRight.MemberID} {
		granted := h.allowance(memberID)
		if granted.Reserved != 0 {
			t.Fatalf("member %s kept an introduction after expiry: reserved=%d",
				memberID, granted.Reserved)
		}
	}

	// A second sweep must stay idempotent for both introductions.
	again, err := h.app.Matches.ExpireDue(h.ctx(), h.adminActor(), 20)
	if err != nil {
		t.Fatalf("run the second sweep: %v", err)
	}
	if again != 0 {
		t.Fatalf("expected the second sweep to retire nothing, got %d", again)
	}
}
