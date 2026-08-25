package apptest

import (
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// TestMatchWindowTotalAgreesWithReturnedPage checks that narrowing the
// introduction list to a proposal time window reports a total consistent with
// the rows it actually returns, while an unfiltered listing still reports every
// introduction.
func TestMatchWindowTotalAgreesWithReturnedPage(t *testing.T) {
	h := newHarness(t)
	matchmaker, staffToken := h.staff("windowmm@heartbridge.test", "matchmaker")
	focus := h.enrollMember(memberSpec{Email: "windowhub@heartbridge.test", Gender: "female", Plan: "premium"})
	partners := []memberFixture{
		h.enrollMember(memberSpec{Email: "window1@heartbridge.test", Gender: "male", Plan: "premium"}),
		h.enrollMember(memberSpec{Email: "window2@heartbridge.test", Gender: "male", Plan: "premium"}),
		h.enrollMember(memberSpec{Email: "window3@heartbridge.test", Gender: "male", Plan: "premium"}),
	}

	proposedAt := make([]time.Time, 0, len(partners))
	created := make([]string, 0, len(partners))
	for index, partner := range partners {
		if index > 0 {
			h.clk.Advance(20 * time.Minute)
		}
		moment := h.clk.Now()
		detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
			FirstMemberID:  focus.MemberID,
			SecondMemberID: partner.MemberID,
		})
		if err != nil {
			t.Fatalf("propose introduction %d: %v", index, err)
		}
		proposedAt = append(proposedAt, moment)
		created = append(created, detail.Match.ID)
	}

	unfiltered := h.do(http.MethodGet, "/api/v1/matches?limit=50", staffToken, nil, nil)
	if unfiltered.Status != http.StatusOK {
		t.Fatalf("expected 200 for the unfiltered listing, got %d (%s)", unfiltered.Status, unfiltered.Raw)
	}
	unfilteredTotal := int(unfiltered.Body["page"].(map[string]any)["total"].(float64))
	if unfilteredTotal != len(created) {
		t.Fatalf("expected the unfiltered total to be %d, got %d", len(created), unfilteredTotal)
	}

	from := proposedAt[1].UTC().Format(time.RFC3339)
	to := proposedAt[2].UTC().Format(time.RFC3339)
	windowed := h.do(http.MethodGet,
		"/api/v1/matches?limit=50&created_from="+from+"&created_to="+to, staffToken, nil, nil)
	if windowed.Status != http.StatusOK {
		t.Fatalf("expected 200 for the windowed listing, got %d (%s)", windowed.Status, windowed.Raw)
	}
	items := windowed.Body["items"].([]any)
	total := int(windowed.Body["page"].(map[string]any)["total"].(float64))
	if len(items) != 2 {
		t.Fatalf("expected the window to return two introductions, got %d (%s)", len(items), windowed.Raw)
	}
	if total != len(items) {
		t.Fatalf("the window reported %d introductions but returned %d rows (%s)",
			total, len(items), windowed.Raw)
	}

	returned := map[string]bool{}
	for _, entry := range items {
		returned[entry.(map[string]any)["id"].(string)] = true
	}
	if returned[created[0]] {
		t.Fatalf("the earliest introduction %s must stay outside the window", created[0])
	}
	if !returned[created[1]] || !returned[created[2]] {
		t.Fatalf("expected the window to contain %s and %s, got %v", created[1], created[2], returned)
	}

	narrow := h.do(http.MethodGet,
		"/api/v1/matches?limit=50&created_from="+to+"&created_to="+to, staffToken, nil, nil)
	narrowItems := narrow.Body["items"].([]any)
	narrowTotal := int(narrow.Body["page"].(map[string]any)["total"].(float64))
	if len(narrowItems) != 1 || narrowTotal != 1 {
		t.Fatalf("a single-instant window must report exactly one introduction, got total=%d rows=%d (%s)",
			narrowTotal, len(narrowItems), narrow.Raw)
	}
}
