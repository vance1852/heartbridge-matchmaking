package apptest

import (
	"net/http"
	"testing"
	"time"
)

// TestExpiredPlanProposalReportsBusinessFailure checks that proposing an
// introduction for a member whose service period has elapsed is refused as a
// readable allowance failure rather than as an unexpected server fault, and that
// members enrolled inside a valid period can still be introduced.
func TestExpiredPlanProposalReportsBusinessFailure(t *testing.T) {
	h := newHarness(t)
	h.staff("expiredmm@heartbridge.test", "matchmaker")
	stale := h.enrollMember(memberSpec{Email: "stalea@heartbridge.test", Gender: "female"})
	stalePartner := h.enrollMember(memberSpec{Email: "staleb@heartbridge.test", Gender: "male"})

	// The starter plan is valid for 180 days; move past the end of the window.
	h.clk.Advance(181 * 24 * time.Hour)
	staffToken := h.login("expiredmm@heartbridge.test", "staffPassword2026")

	refused := h.do(http.MethodPost, "/api/v1/matches", staffToken, map[string]any{
		"first_member_id":  stale.MemberID,
		"second_member_id": stalePartner.MemberID,
	}, nil)
	if refused.Status == http.StatusCreated {
		t.Fatalf("expected an expired service period to block the introduction, got %d (%s)",
			refused.Status, refused.Raw)
	}
	if refused.Status >= http.StatusInternalServerError {
		t.Fatalf("an elapsed service period must not surface as a server fault, got %d (%s)",
			refused.Status, refused.Raw)
	}
	if refused.errorCode() != "quota_exhausted" {
		t.Fatalf("expected the stable code quota_exhausted, got %q (%s)",
			refused.errorCode(), refused.Raw)
	}
	if refused.Status != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for an exhausted allowance, got %d (%s)", refused.Status, refused.Raw)
	}

	// Nothing may have been reserved by the refused proposal.
	staleAllowance := h.allowance(stale.MemberID)
	if staleAllowance.Reserved != 0 {
		t.Fatalf("the refused proposal reserved an introduction: reserved=%d", staleAllowance.Reserved)
	}

	// Members enrolled inside a valid service period are still introducible.
	fresh := h.enrollMember(memberSpec{Email: "fresha@heartbridge.test", Gender: "female"})
	freshPartner := h.enrollMember(memberSpec{Email: "freshb@heartbridge.test", Gender: "male"})
	accepted := h.do(http.MethodPost, "/api/v1/matches", staffToken, map[string]any{
		"first_member_id":  fresh.MemberID,
		"second_member_id": freshPartner.MemberID,
	}, nil)
	if accepted.Status != http.StatusCreated {
		t.Fatalf("expected a valid service period to allow the introduction, got %d (%s)",
			accepted.Status, accepted.Raw)
	}
	if h.allowance(fresh.MemberID).Reserved != 1 {
		t.Fatalf("expected the accepted proposal to hold one introduction for the member")
	}
}
