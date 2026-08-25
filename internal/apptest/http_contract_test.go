package apptest

import (
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/middleware"
)

// TestHealthAndReadinessEndpoints checks both probes of the container contract.
func TestHealthAndReadinessEndpoints(t *testing.T) {
	h := newHarness(t)

	live := h.do(http.MethodGet, "/healthz", "", nil, nil)
	if live.Status != http.StatusOK {
		t.Fatalf("expected 200 from /healthz, got %d (%s)", live.Status, live.Raw)
	}
	if live.str("status") != "alive" {
		t.Fatalf("unexpected liveness payload: %s", live.Raw)
	}

	ready := h.do(http.MethodGet, "/readyz", "", nil, nil)
	if ready.Status != http.StatusOK {
		t.Fatalf("expected 200 from /readyz, got %d (%s)", ready.Status, ready.Raw)
	}
	if ready.str("status") != "ready" {
		t.Fatalf("unexpected readiness payload: %s", ready.Raw)
	}
	if ready.Headers.Get(middleware.RequestIDHeader) == "" {
		t.Fatal("expected a correlation id header on every response")
	}
}

// TestRegistrationAndLoginOverHTTP walks the public identity endpoints.
func TestRegistrationAndLoginOverHTTP(t *testing.T) {
	h := newHarness(t)

	created := h.do(http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"email":          "http.member@heartbridge.test",
		"password":       "httpMember2026",
		"display_name":   "Http Member",
		"gender":         "female",
		"birth_date":     "1995-04-11T00:00:00Z",
		"city":           "Hangzhou",
		"marital_status": "single",
		"education":      "bachelor",
	}, nil)
	if created.Status != http.StatusCreated {
		t.Fatalf("expected 201 on registration, got %d (%s)", created.Status, created.Raw)
	}

	weak := h.do(http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"email":          "weak@heartbridge.test",
		"password":       "short",
		"display_name":   "Weak",
		"gender":         "female",
		"birth_date":     "1995-04-11T00:00:00Z",
		"city":           "Hangzhou",
		"marital_status": "single",
		"education":      "bachelor",
	}, nil)
	if weak.Status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a weak password, got %d (%s)", weak.Status, weak.Raw)
	}
	if weak.errorCode() != "invalid_argument" {
		t.Fatalf("expected invalid_argument, got %q", weak.errorCode())
	}

	unknownField := h.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email":    "http.member@heartbridge.test",
		"password": "httpMember2026",
		"surprise": true,
	}, nil)
	if unknownField.Status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown field, got %d (%s)", unknownField.Status, unknownField.Raw)
	}

	wrong := h.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email":    "http.member@heartbridge.test",
		"password": "notThePassword2026",
	}, nil)
	if wrong.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a wrong password, got %d (%s)", wrong.Status, wrong.Raw)
	}
	missing := h.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email":    "nobody@heartbridge.test",
		"password": "notThePassword2026",
	}, nil)
	if missing.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an unknown account, got %d (%s)", missing.Status, missing.Raw)
	}
	// The two failures must be indistinguishable so the endpoint cannot be used to
	// discover which addresses are registered.
	if wrong.errorCode() != missing.errorCode() {
		t.Fatalf("credential failures leak account existence: %q vs %q",
			wrong.errorCode(), missing.errorCode())
	}

	ok := h.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email":    "http.member@heartbridge.test",
		"password": "httpMember2026",
	}, nil)
	if ok.Status != http.StatusOK {
		t.Fatalf("expected 200 on login, got %d (%s)", ok.Status, ok.Raw)
	}
	token := ok.str("token")
	if token == "" {
		t.Fatalf("expected a bearer token in the login payload: %s", ok.Raw)
	}

	whoami := h.do(http.MethodGet, "/api/v1/me", token, nil, nil)
	if whoami.Status != http.StatusOK {
		t.Fatalf("expected 200 from /api/v1/me, got %d (%s)", whoami.Status, whoami.Raw)
	}
}

// TestLogoutRevokesTokenImmediately verifies that a revoked session cannot be
// reused on the very next request.
func TestLogoutRevokesTokenImmediately(t *testing.T) {
	h := newHarness(t)
	fixture := h.enrollMember(memberSpec{Email: "revoke@heartbridge.test", Gender: "female"})

	before := h.do(http.MethodGet, "/api/v1/me", fixture.Token, nil, nil)
	if before.Status != http.StatusOK {
		t.Fatalf("expected the session to work before logout, got %d", before.Status)
	}
	logout := h.do(http.MethodPost, "/api/v1/auth/logout", fixture.Token, nil, nil)
	if logout.Status != http.StatusOK {
		t.Fatalf("expected 200 on logout, got %d (%s)", logout.Status, logout.Raw)
	}
	after := h.do(http.MethodGet, "/api/v1/me", fixture.Token, nil, nil)
	if after.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401 after logout, got %d (%s)", after.Status, after.Raw)
	}
	if after.errorCode() != "unauthenticated" {
		t.Fatalf("expected unauthenticated, got %q", after.errorCode())
	}
	// A second logout is refused because the session is already gone.
	repeat := h.do(http.MethodPost, "/api/v1/auth/logout", fixture.Token, nil, nil)
	if repeat.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401 on a repeated logout, got %d", repeat.Status)
	}
}

// TestExpiredSessionIsRejected verifies the session lifetime is enforced.
func TestExpiredSessionIsRejected(t *testing.T) {
	h := newHarness(t)
	fixture := h.enrollMember(memberSpec{Email: "expire@heartbridge.test", Gender: "female"})

	h.clk.Advance(h.cfg.SessionTTL + time.Minute)
	response := h.do(http.MethodGet, "/api/v1/me", fixture.Token, nil, nil)
	if response.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an expired session, got %d (%s)", response.Status, response.Raw)
	}

	purged, err := h.app.Auth.PurgeExpiredSessions(h.ctx(), h.clk.Now())
	if err != nil {
		t.Fatalf("purge expired sessions: %v", err)
	}
	if purged < 1 {
		t.Fatalf("expected at least one expired session to be pruned, got %d", purged)
	}
}

// TestRoleGuardsRejectWrongRole verifies the role separation of the API surface.
func TestRoleGuardsRejectWrongRole(t *testing.T) {
	h := newHarness(t)
	left := h.enrollMember(memberSpec{Email: "roleA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "roleB@heartbridge.test", Gender: "male"})
	_, matchmakerToken := h.staff("rolemm@heartbridge.test", "matchmaker")

	// A member may not propose introductions.
	forbidden := h.do(http.MethodPost, "/api/v1/matches", left.Token, map[string]any{
		"first_member_id":  left.MemberID,
		"second_member_id": right.MemberID,
	}, nil)
	if forbidden.Status != http.StatusForbidden {
		t.Fatalf("expected 403 for a member proposing, got %d (%s)", forbidden.Status, forbidden.Raw)
	}
	if forbidden.errorCode() != "forbidden" {
		t.Fatalf("expected forbidden, got %q", forbidden.errorCode())
	}

	// A matchmaker may not read the audit trail.
	audit := h.do(http.MethodGet, "/api/v1/admin/audit-events", matchmakerToken, nil, nil)
	if audit.Status != http.StatusForbidden {
		t.Fatalf("expected 403 for a matchmaker reading the audit trail, got %d", audit.Status)
	}

	// A request without any credential is unauthenticated, not forbidden.
	anonymous := h.do(http.MethodGet, "/api/v1/matches", "", nil, nil)
	if anonymous.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d (%s)", anonymous.Status, anonymous.Raw)
	}

	// A member may not read another member's profile.
	crossRead := h.do(http.MethodGet, "/api/v1/members/"+right.MemberID, left.Token, nil, nil)
	if crossRead.Status != http.StatusForbidden {
		t.Fatalf("expected 403 when reading another profile, got %d", crossRead.Status)
	}
}

// TestIdempotentProposalReplaysStoredOutcome verifies the replay protection of the
// mutating endpoints.
func TestIdempotentProposalReplaysStoredOutcome(t *testing.T) {
	h := newHarness(t)
	_, matchmakerToken := h.staff("idem@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "idemA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "idemB@heartbridge.test", Gender: "male"})
	payload := map[string]any{
		"first_member_id":  left.MemberID,
		"second_member_id": right.MemberID,
	}
	headers := map[string]string{"Idempotency-Key": "proposal-2026-03-02-001"}

	first := h.do(http.MethodPost, "/api/v1/matches", matchmakerToken, payload, headers)
	if first.Status != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", first.Status, first.Raw)
	}
	matchID := first.str("id")
	if matchID == "" {
		t.Fatalf("expected a match id in the response: %s", first.Raw)
	}

	replay := h.do(http.MethodPost, "/api/v1/matches", matchmakerToken, payload, headers)
	if replay.Status != http.StatusCreated {
		t.Fatalf("expected the replay to return 201, got %d (%s)", replay.Status, replay.Raw)
	}
	if replay.Headers.Get("Idempotent-Replay") != "true" {
		t.Fatal("expected the replay to be marked as such")
	}
	if replay.str("id") != matchID {
		t.Fatalf("expected the same match id on replay, got %q and %q", matchID, replay.str("id"))
	}

	// Without the key the same request hits the pair uniqueness rule instead.
	duplicate := h.do(http.MethodPost, "/api/v1/matches", matchmakerToken, payload, nil)
	if duplicate.Status != http.StatusConflict {
		t.Fatalf("expected 409 without an idempotency key, got %d (%s)", duplicate.Status, duplicate.Raw)
	}

	// Reusing the key with a different payload must be refused.
	other := h.enrollMember(memberSpec{Email: "idemC@heartbridge.test", Gender: "male"})
	mismatch := h.do(http.MethodPost, "/api/v1/matches", matchmakerToken, map[string]any{
		"first_member_id":  left.MemberID,
		"second_member_id": other.MemberID,
	}, headers)
	if mismatch.Status != http.StatusConflict {
		t.Fatalf("expected 409 for a reused key, got %d (%s)", mismatch.Status, mismatch.Raw)
	}
	if mismatch.errorCode() != "idempotency_mismatch" {
		t.Fatalf("expected idempotency_mismatch, got %q", mismatch.errorCode())
	}

	// A malformed key is rejected before anything happens.
	bad := h.do(http.MethodPost, "/api/v1/matches", matchmakerToken, payload,
		map[string]string{"Idempotency-Key": "not valid!"})
	if bad.Status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid key, got %d (%s)", bad.Status, bad.Raw)
	}
}

// TestIdempotentBookingServesEachMatchSeparately reproduces the matchmaker
// scenario: the same retry key and the same tea-house slot are used to book two
// different accepted introductions. Each match must receive its own meetup and
// occupy its own seat, while retrying the same match still replays one booking.
func TestIdempotentBookingServesEachMatchSeparately(t *testing.T) {
	h := newHarness(t)
	matchmaker, matchmakerToken := h.staff("bookidem@heartbridge.test", "matchmaker")
	slotID := h.publishSlot("TEA-WED", 30*time.Hour, 2*time.Hour, 2)

	firstMatch := h.consentedMatch("bmm1@heartbridge.test", "b1a@heartbridge.test", "b1b@heartbridge.test")
	secondMatch := h.consentedMatch("bmm2@heartbridge.test", "b2a@heartbridge.test", "b2b@heartbridge.test")

	book := func(matchID string) apiResponse {
		return h.do(http.MethodPost, "/api/v1/matches/"+matchID+"/meetup", matchmakerToken,
			map[string]any{"slot_id": slotID}, map[string]string{"Idempotency-Key": "book-wednesday-tea"})
	}

	first := book(firstMatch)
	if first.Status != http.StatusCreated {
		t.Fatalf("expected the first booking to return 201, got %d (%s)", first.Status, first.Raw)
	}
	firstMeetup := first.str("id")
	firstMatchID := first.str("match_id")
	if firstMeetup == "" || firstMatchID != firstMatch {
		t.Fatalf("expected a meetup for match %s, got id=%q match_id=%q (%s)",
			firstMatch, firstMeetup, firstMatchID, first.Raw)
	}

	// Same key, same slot, different match: must NOT replay the first booking.
	second := book(secondMatch)
	if second.Status != http.StatusCreated {
		t.Fatalf("expected the second booking to return 201, got %d (%s)", second.Status, second.Raw)
	}
	if second.Headers.Get("Idempotent-Replay") == "true" {
		t.Fatal("the second match must be booked for real, not replayed")
	}
	secondMeetup := second.str("id")
	if secondMeetup == "" || secondMeetup == firstMeetup {
		t.Fatalf("expected a distinct meetup id for the second match, got %q", secondMeetup)
	}
	if second.str("match_id") != secondMatch {
		t.Fatalf("expected the meetup to belong to match %s, got match_id=%q",
			secondMatch, second.str("match_id"))
	}

	// Both seats are taken: the slot capacity is two and both landed.
	slot, err := h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("read slot: %v", err)
	}
	if slot.BookedCount != 2 {
		t.Fatalf("expected the slot to hold two bookings, got %d", slot.BookedCount)
	}

	// Retrying the first match with the same key and body replays the one booking,
	// so the slot is not overbooked and the same meetup is returned.
	replay := book(firstMatch)
	if replay.Status != http.StatusCreated {
		t.Fatalf("expected the replay to return 201, got %d (%s)", replay.Status, replay.Raw)
	}
	if replay.Headers.Get("Idempotent-Replay") != "true" {
		t.Fatal("expected the retry of the same match to be replayed")
	}
	if replay.str("id") != firstMeetup {
		t.Fatalf("expected the replay to return the same meetup id %q, got %q",
			firstMeetup, replay.str("id"))
	}
	slot, err = h.app.Repositories.Slots.GetByID(h.ctx(), slotID)
	if err != nil {
		t.Fatalf("read slot after replay: %v", err)
	}
	if slot.BookedCount != 2 {
		t.Fatalf("expected the slot to still hold two bookings after replay, got %d", slot.BookedCount)
	}
	_ = matchmaker // staff fixture keeps the matchmaker principal stable
}

// TestCorrelationIDIsPropagated verifies that a client supplied correlation id is
// echoed and reaches the error envelope and the audit trail.
func TestCorrelationIDIsPropagated(t *testing.T) {
	h := newHarness(t)
	fixture := h.enrollMember(memberSpec{Email: "trace@heartbridge.test", Gender: "female"})
	const correlation = "trace-abc-123"

	missing := h.do(http.MethodGet, "/api/v1/matches/mch_does_not_exist", fixture.Token, nil,
		map[string]string{middleware.RequestIDHeader: correlation})
	if missing.Status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", missing.Status, missing.Raw)
	}
	if got := missing.Headers.Get(middleware.RequestIDHeader); got != correlation {
		t.Fatalf("expected the correlation id to be echoed, got %q", got)
	}
	if got := missing.errorRequestID(); got != correlation {
		t.Fatalf("expected the correlation id in the error body, got %q", got)
	}
	if missing.errorCode() != "not_found" {
		t.Fatalf("expected not_found, got %q", missing.errorCode())
	}

	// An unsupported method on an existing path is answered by the router.
	wrongMethod := h.do(http.MethodDelete, "/api/v1/me", fixture.Token, nil, nil)
	if wrongMethod.Status != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for an unsupported method, got %d", wrongMethod.Status)
	}
}

// TestListMatchesPaginatesFiltersAndSorts verifies the list contract, including
// that the total is computed under the same predicate as the page.
func TestListMatchesPaginatesFiltersAndSorts(t *testing.T) {
	h := newHarness(t)
	matchmakerActor, matchmakerToken := h.staff("listmm@heartbridge.test", "matchmaker")
	focus := h.enrollMember(memberSpec{Email: "listhub@heartbridge.test", Gender: "female", Plan: "premium"})
	partners := []memberFixture{
		h.enrollMember(memberSpec{Email: "list1@heartbridge.test", Gender: "male", Plan: "premium"}),
		h.enrollMember(memberSpec{Email: "list2@heartbridge.test", Gender: "male", Plan: "premium"}),
		h.enrollMember(memberSpec{Email: "list3@heartbridge.test", Gender: "male", Plan: "premium"}),
	}
	created := make([]string, 0, len(partners))
	for _, partner := range partners {
		response := h.do(http.MethodPost, "/api/v1/matches", matchmakerToken, map[string]any{
			"first_member_id":  focus.MemberID,
			"second_member_id": partner.MemberID,
		}, nil)
		if response.Status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%s)", response.Status, response.Raw)
		}
		created = append(created, response.str("id"))
		h.clk.Advance(time.Minute)
	}

	page := h.do(http.MethodGet, "/api/v1/matches?limit=2&offset=0&sort=created_at&order=asc",
		matchmakerToken, nil, nil)
	if page.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", page.Status, page.Raw)
	}
	meta, ok := page.Body["page"].(map[string]any)
	if !ok {
		t.Fatalf("expected pagination metadata: %s", page.Raw)
	}
	if int(meta["total"].(float64)) != 3 {
		t.Fatalf("expected total 3, got %v", meta["total"])
	}
	items, ok := page.Body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected two items on the first page: %s", page.Raw)
	}
	firstItem := items[0].(map[string]any)
	if firstItem["id"] != created[0] {
		t.Fatalf("ascending order broken: expected %q first, got %v", created[0], firstItem["id"])
	}

	// Cancel one introduction and verify the state filter narrows both the page
	// and the total consistently.
	if _, err := h.app.Matches.Cancel(h.ctx(), matchmakerActor, created[0], "no longer interested"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	filtered := h.do(http.MethodGet, "/api/v1/matches?state=cancelled", matchmakerToken, nil, nil)
	if filtered.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", filtered.Status, filtered.Raw)
	}
	filteredMeta := filtered.Body["page"].(map[string]any)
	if int(filteredMeta["total"].(float64)) != 1 {
		t.Fatalf("expected the filtered total to be 1, got %v", filteredMeta["total"])
	}
	filteredItems := filtered.Body["items"].([]any)
	if len(filteredItems) != 1 {
		t.Fatalf("expected one filtered item, got %d", len(filteredItems))
	}

	// A member only ever sees their own introductions, whatever the filter says.
	scoped := h.do(http.MethodGet, "/api/v1/matches?member_id="+focus.MemberID,
		partners[0].Token, nil, nil)
	if scoped.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", scoped.Status, scoped.Raw)
	}
	scopedMeta := scoped.Body["page"].(map[string]any)
	if int(scopedMeta["total"].(float64)) != 1 {
		t.Fatalf("expected a member to see only their own introduction, got %v", scopedMeta["total"])
	}

	// An invalid state is rejected instead of being silently ignored.
	invalid := h.do(http.MethodGet, "/api/v1/matches?state=not_a_state", matchmakerToken, nil, nil)
	if invalid.Status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown state, got %d (%s)", invalid.Status, invalid.Raw)
	}
	tooLarge := h.do(http.MethodGet, "/api/v1/matches?limit=5000", matchmakerToken, nil, nil)
	if tooLarge.Status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an oversized page, got %d", tooLarge.Status)
	}
}

// TestAuditTrailRecordsBusinessActions verifies that operations can trace a change
// back to the request that caused it.
func TestAuditTrailRecordsBusinessActions(t *testing.T) {
	h := newHarness(t)
	_, matchmakerToken := h.staff("auditmm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "auditA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "auditB@heartbridge.test", Gender: "male"})
	const correlation = "audit-corr-001"

	created := h.do(http.MethodPost, "/api/v1/matches", matchmakerToken, map[string]any{
		"first_member_id":  left.MemberID,
		"second_member_id": right.MemberID,
	}, map[string]string{middleware.RequestIDHeader: correlation})
	if created.Status != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", created.Status, created.Raw)
	}

	trail := h.do(http.MethodGet, "/api/v1/admin/audit-events?request_id="+correlation,
		h.adminTok, nil, nil)
	if trail.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", trail.Status, trail.Raw)
	}
	items := trail.Body["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("expected the audit trail to contain the proposal: %s", trail.Raw)
	}
	entry := items[0].(map[string]any)
	if entry["action"] != "match.proposed" {
		t.Fatalf("expected a match.proposed row, got %v", entry["action"])
	}
	if entry["object_id"] != created.str("id") {
		t.Fatalf("expected the row to point at the new introduction, got %v", entry["object_id"])
	}
	if entry["result"] != "success" {
		t.Fatalf("expected a success result, got %v", entry["result"])
	}
}
