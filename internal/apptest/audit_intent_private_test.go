package apptest

import (
	"errors"
	"testing"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// TestAuditTrailOnlyRecordsIntroductionsThatExist checks that a refused proposal
// leaves no successful entry in the operational trail, while the accepted
// proposal is still recorded and points at a real introduction.
func TestAuditTrailOnlyRecordsIntroductionsThatExist(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("trailmm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "tra@heartbridge.test", Gender: "female", Plan: "premium"})
	right := h.enrollMember(memberSpec{Email: "trb@heartbridge.test", Gender: "male", Plan: "premium"})

	accepted, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose the introduction: %v", err)
	}
	// The same pair may not have a second live introduction.
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  right.MemberID,
		SecondMemberID: left.MemberID,
	}); err == nil {
		t.Fatal("expected the duplicate pair to be refused")
	}

	page, err := h.app.Admin.ListAudit(h.ctx(), h.adminActor(), repository.AuditFilter{
		ObjectType: audit.ObjectMatch,
		Page:       repository.Page{Limit: 100},
	})
	if err != nil {
		t.Fatalf("read the operational trail: %v", err)
	}

	recorded := 0
	for _, event := range page.Items {
		if event.Action != audit.ActionMatchProposed || event.Result != audit.ResultSuccess {
			continue
		}
		recorded++
		if _, err := h.app.Repositories.Matches.GetByID(h.ctx(), event.ObjectID); err != nil {
			if errors.Is(err, apperr.ErrNotFound) {
				t.Fatalf("the trail reports a proposed introduction %s that does not exist", event.ObjectID)
			}
			t.Fatalf("resolve the introduction referenced by the trail: %v", err)
		}
	}
	if recorded != 1 {
		t.Fatalf("expected exactly one recorded proposal, got %d", recorded)
	}

	found := false
	for _, event := range page.Items {
		if event.ObjectID == accepted.Match.ID && event.Action == audit.ActionMatchProposed {
			found = true
			if event.Result != audit.ResultSuccess {
				t.Fatalf("expected the accepted proposal to be recorded as a success, got %s", event.Result)
			}
		}
	}
	if !found {
		t.Fatalf("the accepted introduction %s is missing from the trail", accepted.Match.ID)
	}
}
