package apptest

import (
	"testing"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// TestBatchProposalRespectsConcurrentIntroductionLimit checks that submitting
// several pairs for one member in a single batch still stops at the concurrent
// introduction limit of that member's plan, and that the accepted pairs hold
// exactly one introduction each.
func TestBatchProposalRespectsConcurrentIntroductionLimit(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("batchcapmm@heartbridge.test", "matchmaker")
	// The starter plan allows two live introductions and four in total.
	focus := h.enrollMember(memberSpec{Email: "caphub@heartbridge.test", Gender: "female"})
	partners := []memberFixture{
		h.enrollMember(memberSpec{Email: "cap1@heartbridge.test", Gender: "male"}),
		h.enrollMember(memberSpec{Email: "cap2@heartbridge.test", Gender: "male"}),
		h.enrollMember(memberSpec{Email: "cap3@heartbridge.test", Gender: "male"}),
		h.enrollMember(memberSpec{Email: "cap4@heartbridge.test", Gender: "male"}),
	}

	inputs := make([]matchsvc.ProposeInput, 0, len(partners))
	for _, partner := range partners {
		inputs = append(inputs, matchsvc.ProposeInput{
			FirstMemberID:  focus.MemberID,
			SecondMemberID: partner.MemberID,
		})
	}
	result, err := h.app.Matches.ProposeBatch(h.ctx(), matchmaker, inputs)
	if err != nil {
		t.Fatalf("submit the batch: %v", err)
	}
	if result.Accepted != 2 {
		t.Fatalf("expected the plan limit to accept two pairs, got %d accepted (%+v)",
			result.Accepted, result.Items)
	}
	if result.Rejected != 2 {
		t.Fatalf("expected two pairs to be refused by the plan limit, got %d", result.Rejected)
	}
	for _, item := range result.Items {
		if item.Accepted {
			continue
		}
		if item.ErrorCode != apperr.CodePreconditionFailed {
			t.Fatalf("expected the refused pair %d to report a precondition failure, got %q",
				item.Index, item.ErrorCode)
		}
	}

	granted := h.allowance(focus.MemberID)
	if granted.Reserved != 2 {
		t.Fatalf("expected the member to hold exactly two introductions, got %d", granted.Reserved)
	}
	if granted.Used+granted.Reserved > granted.Total {
		t.Fatalf("the allowance was overcommitted: used=%d reserved=%d total=%d",
			granted.Used, granted.Reserved, granted.Total)
	}
	movements, err := h.app.Repositories.Entitlements.ListLedgerByEntitlement(h.ctx(), granted.ID)
	if err != nil {
		t.Fatalf("read the allowance history: %v", err)
	}
	reserves := 0
	for _, entry := range movements {
		if entry.Reason == entitlement.ReasonReserve {
			reserves++
		}
	}
	if reserves != 2 {
		t.Fatalf("expected two hold entries in the allowance history, got %d", reserves)
	}

	page, err := h.app.Matches.List(h.ctx(), matchmaker, repository.MatchFilter{MemberID: focus.MemberID})
	if err != nil {
		t.Fatalf("list the introductions of the member: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("expected two live introductions for the member, got %d", page.Total)
	}
}
