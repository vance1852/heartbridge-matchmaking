package apptest

import (
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/member"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/adminsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// TestBatchProposalIsolatesFailedItems verifies that one rejected pair neither
// aborts the batch nor leaves partial state behind.
func TestBatchProposalIsolatesFailedItems(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("batchmm@heartbridge.test", "matchmaker")
	goodLeft := h.enrollMember(memberSpec{Email: "bgA@heartbridge.test", Gender: "female"})
	goodRight := h.enrollMember(memberSpec{Email: "bgB@heartbridge.test", Gender: "male"})
	secondLeft := h.enrollMember(memberSpec{Email: "bsA@heartbridge.test", Gender: "female"})
	secondRight := h.enrollMember(memberSpec{Email: "bsB@heartbridge.test", Gender: "male"})
	// This member lives in a city the counterparts do not accept.
	unreachable := h.enrollMember(memberSpec{Email: "bfar@heartbridge.test", Gender: "male", City: "Chengdu"})
	// And this one has no service plan at all.
	unfunded := h.enrollMember(memberSpec{Email: "bpoor@heartbridge.test", Gender: "male", Plan: "none"})

	result, err := h.app.Matches.ProposeBatch(h.ctx(), matchmaker, []matchsvc.ProposeInput{
		{FirstMemberID: goodLeft.MemberID, SecondMemberID: goodRight.MemberID},
		{FirstMemberID: secondLeft.MemberID, SecondMemberID: unreachable.MemberID},
		{FirstMemberID: secondLeft.MemberID, SecondMemberID: unfunded.MemberID},
		{FirstMemberID: secondLeft.MemberID, SecondMemberID: secondRight.MemberID},
	})
	if err != nil {
		t.Fatalf("batch proposal: %v", err)
	}
	if result.Requested != 4 {
		t.Fatalf("expected four requested pairs, got %d", result.Requested)
	}
	if result.Accepted != 2 {
		t.Fatalf("expected two accepted pairs, got %d", result.Accepted)
	}
	if result.Rejected != 2 {
		t.Fatalf("expected two rejected pairs, got %d", result.Rejected)
	}
	if len(result.Items) != 4 {
		t.Fatalf("expected a verdict per pair, got %d", len(result.Items))
	}
	if !result.Items[0].Accepted || result.Items[0].MatchID == "" {
		t.Fatalf("expected the first pair to be accepted: %+v", result.Items[0])
	}
	if result.Items[1].Accepted || result.Items[1].ErrorCode != apperr.CodePreconditionFailed {
		t.Fatalf("expected the unreachable pair to fail a precondition: %+v", result.Items[1])
	}
	if result.Items[2].Accepted || result.Items[2].ErrorCode != apperr.CodeQuotaExhausted {
		t.Fatalf("expected the unfunded pair to fail on quota: %+v", result.Items[2])
	}
	if !result.Items[3].Accepted {
		t.Fatalf("expected the last pair to be accepted: %+v", result.Items[3])
	}

	// A rejected item must not have consumed anything.
	if granted := h.allowance(unreachable.MemberID); granted.Reserved != 0 {
		t.Fatalf("a rejected pair reserved allowance: %d", granted.Reserved)
	}
	if granted := h.allowance(secondLeft.MemberID); granted.Reserved != 1 {
		t.Fatalf("expected exactly one reservation for the shared member, got %d", granted.Reserved)
	}
	page, err := h.app.Matches.List(h.ctx(), matchmaker, repository.MatchFilter{})
	if err != nil {
		t.Fatalf("list introductions: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("expected two stored introductions, got %d", page.Total)
	}
}

// TestBatchHonoursConcurrentIntroductionCap reproduces the regression in which a
// matchmaker used the batch entry point to seat one member with more simultaneous
// introductions than her plan allows. The starter plan caps live introductions at
// two, so the third and fourth pairs of a batch that all share the same member must
// be refused exactly the way a sequence of single proposals would refuse the third.
func TestBatchHonoursConcurrentIntroductionCap(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("capmm@heartbridge.test", "matchmaker")
	// The starter plan grants MaxActiveMatch = 2.
	focus := h.enrollMember(memberSpec{Email: "capA@heartbridge.test", Gender: "female"})
	partners := []memberFixture{
		h.enrollMember(memberSpec{Email: "capB@heartbridge.test", Gender: "male"}),
		h.enrollMember(memberSpec{Email: "capC@heartbridge.test", Gender: "male"}),
		h.enrollMember(memberSpec{Email: "capD@heartbridge.test", Gender: "male"}),
		h.enrollMember(memberSpec{Email: "capE@heartbridge.test", Gender: "male"}),
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
		t.Fatalf("batch proposal: %v", err)
	}
	if result.Accepted != 2 || result.Rejected != 2 {
		t.Fatalf("expected two accepted and two rejected pairs, got accepted=%d rejected=%d",
			result.Accepted, result.Rejected)
	}
	for index := 0; index < 2; index++ {
		if !result.Items[index].Accepted || result.Items[index].MatchID == "" {
			t.Fatalf("expected pair %d to be accepted: %+v", index, result.Items[index])
		}
	}
	for index := 2; index < 4; index++ {
		if result.Items[index].Accepted {
			t.Fatalf("expected pair %d to be rejected for exceeding the cap: %+v", index, result.Items[index])
		}
		if result.Items[index].ErrorCode != apperr.CodePreconditionFailed {
			t.Fatalf("expected pair %d to fail a precondition, got %s: %+v",
				index, result.Items[index].ErrorCode, result.Items[index])
		}
	}

	// A batch that exceeds the cap must reserve exactly the cap, leaving room to
	// swap partners later instead of locking all four introductions at once.
	if granted := h.allowance(focus.MemberID); granted.Reserved != 2 {
		t.Fatalf("expected exactly two reservations for the shared member, got %d", granted.Reserved)
	}
	for index := 0; index < 2; index++ {
		if granted := h.allowance(partners[index].MemberID); granted.Reserved != 1 {
			t.Fatalf("expected one reservation for accepted partner %d, got %d", index, granted.Reserved)
		}
	}
	for index := 2; index < 4; index++ {
		if granted := h.allowance(partners[index].MemberID); granted.Reserved != 0 {
			t.Fatalf("expected no reservation for rejected partner %d, got %d", index, granted.Reserved)
		}
	}
	page, err := h.app.Matches.List(h.ctx(), matchmaker, repository.MatchFilter{})
	if err != nil {
		t.Fatalf("list introductions: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("expected two stored introductions, got %d", page.Total)
	}

	// The same cap must also bite when the shared member is the second argument
	// of every pair, which is the order the batch snapshot walks them in.
	swap := h.enrollMember(memberSpec{Email: "capF@heartbridge.test", Gender: "female"})
	swapPartners := []memberFixture{
		h.enrollMember(memberSpec{Email: "capG@heartbridge.test", Gender: "male"}),
		h.enrollMember(memberSpec{Email: "capH@heartbridge.test", Gender: "male"}),
		h.enrollMember(memberSpec{Email: "capI@heartbridge.test", Gender: "male"}),
	}
	swapInputs := make([]matchsvc.ProposeInput, 0, len(swapPartners))
	for _, partner := range swapPartners {
		swapInputs = append(swapInputs, matchsvc.ProposeInput{
			FirstMemberID:  partner.MemberID,
			SecondMemberID: swap.MemberID,
		})
	}
	swapResult, err := h.app.Matches.ProposeBatch(h.ctx(), matchmaker, swapInputs)
	if err != nil {
		t.Fatalf("swapped batch proposal: %v", err)
	}
	if swapResult.Accepted != 2 || swapResult.Rejected != 1 {
		t.Fatalf("expected two accepted and one rejected in the swapped batch, got accepted=%d rejected=%d",
			swapResult.Accepted, swapResult.Rejected)
	}
	if granted := h.allowance(swap.MemberID); granted.Reserved != 2 {
		t.Fatalf("expected exactly two reservations for the swapped shared member, got %d", granted.Reserved)
	}
}

// TestBatchRejectsMalformedRequests verifies the guards applied before any pair is
// processed.
func TestBatchRejectsMalformedRequests(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("badbatch@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "bbA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "bbB@heartbridge.test", Gender: "male"})

	if _, err := h.app.Matches.ProposeBatch(h.ctx(), matchmaker, nil); err == nil {
		t.Fatal("expected an empty batch to be refused")
	}
	// The same pair twice would produce a self-inflicted uniqueness conflict.
	_, err := h.app.Matches.ProposeBatch(h.ctx(), matchmaker, []matchsvc.ProposeInput{
		{FirstMemberID: left.MemberID, SecondMemberID: right.MemberID},
		{FirstMemberID: right.MemberID, SecondMemberID: left.MemberID},
	})
	if err == nil {
		t.Fatal("expected a duplicated pair to be refused")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeInvalidArgument {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeInvalidArgument, code, err)
	}
	// Nothing may have been written by the refused batch.
	page, err := h.app.Matches.List(h.ctx(), matchmaker, repository.MatchFilter{})
	if err != nil {
		t.Fatalf("list introductions: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("a refused batch created %d introductions", page.Total)
	}

	oversized := make([]matchsvc.ProposeInput, matchsvc.MaxBatchSize+1)
	for index := range oversized {
		oversized[index] = matchsvc.ProposeInput{
			FirstMemberID:  left.MemberID,
			SecondMemberID: right.MemberID,
		}
	}
	if _, err := h.app.Matches.ProposeBatch(h.ctx(), matchmaker, oversized); err == nil {
		t.Fatal("expected an oversized batch to be refused")
	}
}

// TestBatchOverHTTPReportsPerItemVerdicts verifies the transport contract of the
// batch endpoint.
func TestBatchOverHTTPReportsPerItemVerdicts(t *testing.T) {
	h := newHarness(t)
	_, matchmakerToken := h.staff("httpbatch@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "hbA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "hbB@heartbridge.test", Gender: "male"})
	far := h.enrollMember(memberSpec{Email: "hbC@heartbridge.test", Gender: "male", City: "Chengdu"})

	response := h.do(http.MethodPost, "/api/v1/matches/batch", matchmakerToken, map[string]any{
		"pairs": []map[string]any{
			{"first_member_id": left.MemberID, "second_member_id": right.MemberID},
			{"first_member_id": left.MemberID, "second_member_id": far.MemberID},
		},
	}, map[string]string{"Idempotency-Key": "batch-001"})
	if response.Status != http.StatusOK {
		t.Fatalf("expected 200 for a partially rejected batch, got %d (%s)", response.Status, response.Raw)
	}
	if int(response.number("accepted")) != 1 || int(response.number("rejected")) != 1 {
		t.Fatalf("unexpected batch summary: %s", response.Raw)
	}
	items := response.Body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected two verdicts, got %d", len(items))
	}
	rejected := items[1].(map[string]any)
	if rejected["accepted"] != false {
		t.Fatalf("expected the second pair to be rejected: %v", rejected)
	}
	if rejected["error_code"] != string(apperr.CodePreconditionFailed) {
		t.Fatalf("expected a precondition failure, got %v", rejected["error_code"])
	}

	replay := h.do(http.MethodPost, "/api/v1/matches/batch", matchmakerToken, map[string]any{
		"pairs": []map[string]any{
			{"first_member_id": left.MemberID, "second_member_id": right.MemberID},
			{"first_member_id": left.MemberID, "second_member_id": far.MemberID},
		},
	}, map[string]string{"Idempotency-Key": "batch-001"})
	if replay.Headers.Get("Idempotent-Replay") != "true" {
		t.Fatal("expected the batch to be replayed rather than executed again")
	}
	if int(replay.number("accepted")) != 1 {
		t.Fatalf("expected the stored summary on replay: %s", replay.Raw)
	}
}

// TestVenueSlotPublishingRules verifies the operations guards around bookable
// windows.
func TestVenueSlotPublishingRules(t *testing.T) {
	h := newHarness(t)
	admin := h.adminActor()
	matchmaker, _ := h.staff("slotguard@heartbridge.test", "matchmaker")
	start := h.clk.Now().Add(48 * time.Hour)

	if _, err := h.app.Admin.PublishSlot(h.ctx(), admin, adminsvc.SlotInput{
		VenueCode: "GUARD-1", VenueName: "Garden", City: "Hangzhou",
		StartAt: start, EndAt: start.Add(90 * time.Minute), Capacity: 2,
	}); err != nil {
		t.Fatalf("publish slot: %v", err)
	}
	// A matchmaker may read but not publish.
	if _, err := h.app.Admin.PublishSlot(h.ctx(), matchmaker, adminsvc.SlotInput{
		VenueCode: "GUARD-2", VenueName: "Garden", City: "Hangzhou",
		StartAt: start, EndAt: start.Add(time.Hour), Capacity: 2,
	}); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("expected a matchmaker to be refused, got %v", err)
	}
	// The same venue cannot publish two windows starting at the same instant.
	if _, err := h.app.Admin.PublishSlot(h.ctx(), admin, adminsvc.SlotInput{
		VenueCode: "GUARD-1", VenueName: "Garden", City: "Hangzhou",
		StartAt: start, EndAt: start.Add(time.Hour), Capacity: 4,
	}); apperr.CodeOf(err) != apperr.CodeConflict {
		t.Fatalf("expected a duplicate window to conflict, got %v", err)
	}
	// A window in the past is refused.
	if _, err := h.app.Admin.PublishSlot(h.ctx(), admin, adminsvc.SlotInput{
		VenueCode: "GUARD-3", VenueName: "Garden", City: "Hangzhou",
		StartAt: h.clk.Now().Add(-time.Hour), EndAt: h.clk.Now(), Capacity: 2,
	}); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("expected a past window to be refused, got %v", err)
	}
	// An empty window is refused.
	if _, err := h.app.Admin.PublishSlot(h.ctx(), admin, adminsvc.SlotInput{
		VenueCode: "GUARD-4", VenueName: "Garden", City: "Hangzhou",
		StartAt: start, EndAt: start, Capacity: 2,
	}); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("expected an empty window to be refused, got %v", err)
	}

	slots, err := h.app.Admin.ListSlots(h.ctx(), matchmaker,
		h.clk.Now(), h.clk.Now().Add(72*time.Hour), "Hangzhou", repository.Page{Limit: 10})
	if err != nil {
		t.Fatalf("list slots: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("expected exactly one published window, got %d", len(slots))
	}
	if slots[0].BusinessDate() == "" {
		t.Fatal("expected the business day to be rendered for operators")
	}
	// A city filter that matches nothing returns an empty page, not an error.
	empty, err := h.app.Admin.ListSlots(h.ctx(), matchmaker,
		h.clk.Now(), h.clk.Now().Add(72*time.Hour), "Shanghai", repository.Page{Limit: 10})
	if err != nil {
		t.Fatalf("list slots for another city: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no window in another city, got %d", len(empty))
	}
	// An excessive range is refused rather than scanning the whole table.
	if _, err := h.app.Admin.ListSlots(h.ctx(), matchmaker,
		h.clk.Now(), h.clk.Now().Add(200*24*time.Hour), "", repository.Page{}); err == nil {
		t.Fatal("expected an excessive range to be refused")
	}
}

// TestServicePlanAdministration verifies plan maintenance and the guards on it.
func TestServicePlanAdministration(t *testing.T) {
	h := newHarness(t)
	admin := h.adminActor()
	matchmaker, _ := h.staff("planmm@heartbridge.test", "matchmaker")

	plans, err := h.app.Admin.ListPlans(h.ctx(), matchmaker)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(plans) < 2 {
		t.Fatalf("expected the seeded plans to be present, got %d", len(plans))
	}

	created, err := h.app.Admin.UpsertPlan(h.ctx(), admin, adminsvc.PlanInput{
		Code: "trial", Name: "Trial introductions", IntroQuota: 1,
		ValidDays: 30, MaxActiveMatch: 1, ConsentHours: 24, Active: true,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if created.IntroQuota != 1 || created.ConsentHours != 24 {
		t.Fatalf("unexpected plan definition: %+v", created)
	}
	updated, err := h.app.Admin.UpsertPlan(h.ctx(), admin, adminsvc.PlanInput{
		Code: "trial", Name: "Trial introductions", IntroQuota: 2,
		ValidDays: 30, MaxActiveMatch: 1, ConsentHours: 12, Active: false,
	})
	if err != nil {
		t.Fatalf("update plan: %v", err)
	}
	if updated.IntroQuota != 2 || updated.Active {
		t.Fatalf("expected the plan to be updated, got %+v", updated)
	}
	// An invalid definition is refused.
	if _, err := h.app.Admin.UpsertPlan(h.ctx(), admin, adminsvc.PlanInput{
		Code: "broken", Name: "Broken", IntroQuota: 0,
		ValidDays: 30, MaxActiveMatch: 1, ConsentHours: 24, Active: true,
	}); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("expected an invalid plan to be refused, got %v", err)
	}
	// A matchmaker may not maintain plans.
	if _, err := h.app.Admin.UpsertPlan(h.ctx(), matchmaker, adminsvc.PlanInput{
		Code: "sneaky", Name: "Sneaky", IntroQuota: 5,
		ValidDays: 30, MaxActiveMatch: 1, ConsentHours: 24, Active: true,
	}); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("expected a matchmaker to be refused, got %v", err)
	}

	// A retired plan cannot be granted to a member any more.
	fixture := h.enrollMember(memberSpec{Email: "planmember@heartbridge.test", Gender: "female", Plan: "none"})
	if _, err := h.app.Members.GrantEntitlement(h.ctx(), admin, fixture.MemberID, "trial"); apperr.CodeOf(err) != apperr.CodePreconditionFailed {
		t.Fatalf("expected an inactive plan to be refused, got %v", err)
	}
	if _, err := h.app.Members.GrantEntitlement(h.ctx(), admin, fixture.MemberID, "starter"); err != nil {
		t.Fatalf("grant an active plan: %v", err)
	}
	// One active allowance per member is a storage invariant.
	if _, err := h.app.Members.GrantEntitlement(h.ctx(), admin, fixture.MemberID, "premium"); apperr.CodeOf(err) != apperr.CodeConflict {
		t.Fatalf("expected a second active allowance to conflict, got %v", err)
	}
}

// TestMemberStatusRulesAndMatchability verifies the enrollment lifecycle and its
// effect on matchmaking.
func TestMemberStatusRulesAndMatchability(t *testing.T) {
	h := newHarness(t)
	admin := h.adminActor()
	matchmaker, _ := h.staff("statusmm@heartbridge.test", "matchmaker")
	left := h.enrollMember(memberSpec{Email: "statusA@heartbridge.test", Gender: "female"})
	right := h.enrollMember(memberSpec{Email: "statusB@heartbridge.test", Gender: "male"})

	// Registering criteria is what activates an onboarding profile.
	profile, err := h.app.Members.GetProfile(h.ctx(), left.Actor, left.MemberID)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if profile.Member.Status != member.StatusActive {
		t.Fatalf("expected an active profile after criteria were stored, got %s", profile.Member.Status)
	}
	if profile.Preference == nil || profile.Entitlement == nil {
		t.Fatalf("expected criteria and allowance in the profile payload: %+v", profile)
	}

	// A paused member receives no introductions.
	if err := h.app.Members.SetStatus(h.ctx(), left.Actor, left.MemberID, member.StatusPaused); err != nil {
		t.Fatalf("pause profile: %v", err)
	}
	_, err = h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if apperr.CodeOf(err) != apperr.CodePreconditionFailed {
		t.Fatalf("expected a paused member to block the proposal, got %v", err)
	}

	// A member may not retire their own profile, and may not touch another one.
	if err := h.app.Members.SetStatus(h.ctx(), left.Actor, left.MemberID, member.StatusRetired); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("expected self-retirement to be refused, got %v", err)
	}
	if err := h.app.Members.SetStatus(h.ctx(), left.Actor, right.MemberID, member.StatusActive); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("expected changing another profile to be refused, got %v", err)
	}

	if err := h.app.Members.SetStatus(h.ctx(), left.Actor, left.MemberID, member.StatusActive); err != nil {
		t.Fatalf("resume profile: %v", err)
	}
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose after resuming: %v", err)
	}
	if detail.Match.State != matching.StatePendingConsent {
		t.Fatalf("unexpected state %s", detail.Match.State)
	}

	// Operations may retire a profile, and a retired profile stays retired.
	if err := h.app.Members.SetStatus(h.ctx(), admin, right.MemberID, member.StatusRetired); err != nil {
		t.Fatalf("retire profile: %v", err)
	}
	if err := h.app.Members.SetStatus(h.ctx(), admin, right.MemberID, member.StatusActive); apperr.CodeOf(err) != apperr.CodePreconditionFailed {
		t.Fatalf("expected reactivation to be refused, got %v", err)
	}
}

// TestAllowanceMovementsAreVisibleToOwnerOnly verifies the ownership rule of the
// allowance history endpoint.
func TestAllowanceMovementsAreVisibleToOwnerOnly(t *testing.T) {
	h := newHarness(t)
	matchmaker, _ := h.staff("ledgermm@heartbridge.test", "matchmaker")
	owner := h.enrollMember(memberSpec{Email: "ledgerA@heartbridge.test", Gender: "female"})
	partner := h.enrollMember(memberSpec{Email: "ledgerB@heartbridge.test", Gender: "male"})
	if _, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  owner.MemberID,
		SecondMemberID: partner.MemberID,
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}

	own, err := h.app.Members.LedgerForMember(h.ctx(), owner.Actor, owner.MemberID)
	if err != nil {
		t.Fatalf("read own allowance history: %v", err)
	}
	if len(own) != 1 {
		t.Fatalf("expected one movement, got %d", len(own))
	}
	if _, err := h.app.Members.LedgerForMember(h.ctx(), owner.Actor, partner.MemberID); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("expected reading another history to be refused, got %v", err)
	}
	if _, err := h.app.Members.LedgerForMember(h.ctx(), matchmaker, partner.MemberID); err != nil {
		t.Fatalf("expected staff to read the history: %v", err)
	}

	response := h.do(http.MethodGet, "/api/v1/members/"+owner.MemberID+"/allowance-movements",
		owner.Token, nil, nil)
	if response.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Status, response.Raw)
	}
	items := response.Body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one movement over HTTP, got %d", len(items))
	}
	entry := items[0].(map[string]any)
	if entry["reason"] != "reserve" {
		t.Fatalf("expected a reserve movement, got %v", entry["reason"])
	}
}
