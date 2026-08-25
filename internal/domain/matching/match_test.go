package matching

import (
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// newMatch builds a match fixture in the given state.
func newMatch(state State) Match {
	return Match{
		ID:              "mch_1",
		MatchmakerID:    "usr_mm",
		MemberAID:       "mbr_a",
		MemberBID:       "mbr_b",
		State:           state,
		Version:         1,
		ConsentDeadline: time.Date(2026, time.March, 4, 2, 0, 0, 0, time.UTC),
		CreatedAt:       time.Date(2026, time.March, 2, 2, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, time.March, 2, 2, 0, 0, 0, time.UTC),
	}
}

// TestStateMachineAcceptsOnlyDeclaredEdges walks the whole transition table.
func TestStateMachineAcceptsOnlyDeclaredEdges(t *testing.T) {
	allowed := map[State][]State{
		StatePendingConsent: {StateConsented, StateCancelled, StateExpired, StateClosedFailed},
		StateConsented:      {StateScheduled, StateCancelled, StateExpired},
		StateScheduled:      {StateMet, StateCancelled},
		StateMet:            {StateClosedSuccess, StateClosedFailed},
	}
	every := []State{
		StatePendingConsent, StateConsented, StateScheduled, StateMet,
		StateClosedSuccess, StateClosedFailed, StateCancelled, StateExpired,
	}
	for _, from := range every {
		match := newMatch(from)
		permitted := map[State]bool{}
		for _, target := range allowed[from] {
			permitted[target] = true
		}
		for _, to := range every {
			err := match.CanTransition(to)
			if permitted[to] {
				if err != nil {
					t.Fatalf("expected %s -> %s to be allowed: %v", from, to, err)
				}
				continue
			}
			if err == nil {
				t.Fatalf("expected %s -> %s to be refused", from, to)
			}
			if code := apperr.CodeOf(err); code != apperr.CodeIllegalTransition {
				t.Fatalf("expected code %s for %s -> %s, got %s",
					apperr.CodeIllegalTransition, from, to, code)
			}
		}
	}
}

// TestTerminalStatesAdmitNoTransition documents which states end an introduction.
func TestTerminalStatesAdmitNoTransition(t *testing.T) {
	terminal := []State{StateClosedSuccess, StateClosedFailed, StateCancelled, StateExpired}
	for _, state := range terminal {
		if !state.Terminal() {
			t.Fatalf("expected %s to be terminal", state)
		}
	}
	live := []State{StatePendingConsent, StateConsented, StateScheduled, StateMet}
	for _, state := range live {
		if state.Terminal() {
			t.Fatalf("expected %s to be non-terminal", state)
		}
	}
}

// TestQuotaAndSlotOwnershipPerState documents which states hold scarce resources.
func TestQuotaAndSlotOwnershipPerState(t *testing.T) {
	holding := map[State]bool{
		StatePendingConsent: true,
		StateConsented:      true,
		StateScheduled:      true,
	}
	for _, state := range []State{
		StatePendingConsent, StateConsented, StateScheduled, StateMet,
		StateClosedSuccess, StateClosedFailed, StateCancelled, StateExpired,
	} {
		if state.HoldsQuota() != holding[state] {
			t.Fatalf("unexpected quota ownership for %s: %v", state, state.HoldsQuota())
		}
	}
	if !StateScheduled.OccupiesSlot() {
		t.Fatal("expected a scheduled introduction to occupy a venue seat")
	}
	if StateMet.OccupiesSlot() {
		t.Fatal("expected a met introduction to no longer reserve a seat through the match")
	}
}

// TestUnknownStateIsRejected verifies that an unexpected stored value is reported.
func TestUnknownStateIsRejected(t *testing.T) {
	if err := State("halfway").Validate(); err == nil {
		t.Fatal("expected an unknown state to be refused")
	}
	if err := newMatch(StatePendingConsent).CanTransition(State("halfway")); err == nil {
		t.Fatal("expected a transition to an unknown state to be refused")
	}
}

// TestCanonicalPairIsOrderIndependent verifies the uniqueness key of a pair.
func TestCanonicalPairIsOrderIndependent(t *testing.T) {
	first, second := CanonicalPair("mbr_z", "mbr_a")
	if first != "mbr_a" || second != "mbr_z" {
		t.Fatalf("expected canonical ordering, got %q and %q", first, second)
	}
	if PairKey("mbr_z", "mbr_a") != PairKey("mbr_a", "mbr_z") {
		t.Fatal("expected the pair key to be independent of the argument order")
	}
	if PairKey("mbr_a", "mbr_b") == PairKey("mbr_a", "mbr_c") {
		t.Fatal("expected different pairs to produce different keys")
	}
}

// TestParticipantsAndCounterpart verifies the ownership helpers.
func TestParticipantsAndCounterpart(t *testing.T) {
	match := newMatch(StatePendingConsent)
	if !match.HasParticipant("mbr_a") || !match.HasParticipant("mbr_b") {
		t.Fatal("expected both members to be participants")
	}
	if match.HasParticipant("mbr_c") || match.HasParticipant("") {
		t.Fatal("expected outsiders and the empty id to be rejected")
	}
	counterpart, err := match.Counterpart("mbr_a")
	if err != nil || counterpart != "mbr_b" {
		t.Fatalf("expected mbr_b, got %q (%v)", counterpart, err)
	}
	if _, err := match.Counterpart("mbr_c"); err == nil {
		t.Fatal("expected an outsider to have no counterpart")
	} else if code := apperr.CodeOf(err); code != apperr.CodeForbidden {
		t.Fatalf("expected code %s, got %s", apperr.CodeForbidden, code)
	}
	if participants := match.Participants(); len(participants) != 2 {
		t.Fatalf("expected two participants, got %d", len(participants))
	}
}

// TestValidateRejectsMalformedMatches covers the structural invariants.
func TestValidateRejectsMalformedMatches(t *testing.T) {
	cases := map[string]func(Match) Match{
		"no matchmaker":  func(m Match) Match { m.MatchmakerID = ""; return m },
		"missing member": func(m Match) Match { m.MemberBID = ""; return m },
		"same member":    func(m Match) Match { m.MemberBID = m.MemberAID; return m },
		"reversed order": func(m Match) Match { m.MemberAID, m.MemberBID = m.MemberBID, m.MemberAID; return m },
		"no deadline":    func(m Match) Match { m.ConsentDeadline = time.Time{}; return m },
		"unknown state":  func(m Match) Match { m.State = "unclear"; return m },
	}
	for name, mutate := range cases {
		if err := mutate(newMatch(StatePendingConsent)).Validate(); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}
	if err := newMatch(StatePendingConsent).Validate(); err != nil {
		t.Fatalf("expected a well formed match to validate: %v", err)
	}
}

// TestConsentExpiryUsesDeadline verifies the deadline rule of the answer window.
func TestConsentExpiryUsesDeadline(t *testing.T) {
	match := newMatch(StatePendingConsent)
	if match.ConsentExpired(match.ConsentDeadline.Add(-time.Second)) {
		t.Fatal("expected the window to still be open one second early")
	}
	if !match.ConsentExpired(match.ConsentDeadline) {
		t.Fatal("expected the window to close exactly at the deadline")
	}
	consented := newMatch(StateConsented)
	if consented.ConsentExpired(consented.ConsentDeadline.Add(time.Hour)) {
		t.Fatal("expected an answered introduction to never expire on the consent deadline")
	}
}

// TestCloneIsolatesOptionalTimestamp verifies the defensive copy of a match.
func TestCloneIsolatesOptionalTimestamp(t *testing.T) {
	closedAt := time.Date(2026, time.March, 5, 0, 0, 0, 0, time.UTC)
	match := newMatch(StateCancelled)
	match.ClosedAt = &closedAt

	copied := match.Clone()
	*copied.ClosedAt = copied.ClosedAt.Add(48 * time.Hour)
	if match.ClosedAt.Equal(*copied.ClosedAt) {
		t.Fatal("expected the clone to own its timestamp")
	}
	if !match.ClosedAt.Equal(closedAt) {
		t.Fatal("expected the original timestamp to stay untouched")
	}
}

// TestConsentOutcomeDerivesNextState covers the consent aggregation rules.
func TestConsentOutcomeDerivesNextState(t *testing.T) {
	decided := time.Date(2026, time.March, 2, 5, 0, 0, 0, time.UTC)
	pending := []Consent{
		{MatchID: "mch_1", MemberID: "mbr_a", Decision: DecisionPending},
		{MatchID: "mch_1", MemberID: "mbr_b", Decision: DecisionPending},
	}
	if next := SummarizeConsents(pending).NextState(StatePendingConsent); next != StatePendingConsent {
		t.Fatalf("expected the state to stay pending, got %s", next)
	}
	halfway := []Consent{
		{MatchID: "mch_1", MemberID: "mbr_a", Decision: DecisionAccepted, DecidedAt: &decided},
		{MatchID: "mch_1", MemberID: "mbr_b", Decision: DecisionPending},
	}
	if next := SummarizeConsents(halfway).NextState(StatePendingConsent); next != StatePendingConsent {
		t.Fatalf("expected one answer not to be enough, got %s", next)
	}
	accepted := []Consent{
		{MatchID: "mch_1", MemberID: "mbr_a", Decision: DecisionAccepted, DecidedAt: &decided},
		{MatchID: "mch_1", MemberID: "mbr_b", Decision: DecisionAccepted, DecidedAt: &decided},
	}
	if next := SummarizeConsents(accepted).NextState(StatePendingConsent); next != StateConsented {
		t.Fatalf("expected both answers to consent the introduction, got %s", next)
	}
	declined := []Consent{
		{MatchID: "mch_1", MemberID: "mbr_a", Decision: DecisionAccepted, DecidedAt: &decided},
		{MatchID: "mch_1", MemberID: "mbr_b", Decision: DecisionDeclined, DecidedAt: &decided},
	}
	if next := SummarizeConsents(declined).NextState(StatePendingConsent); next != StateClosedFailed {
		t.Fatalf("expected a decline to close the introduction, got %s", next)
	}
	// An already answered introduction is never re-derived.
	if next := SummarizeConsents(accepted).NextState(StateScheduled); next != StateScheduled {
		t.Fatalf("expected a scheduled introduction to keep its state, got %s", next)
	}
	summary := SummarizeConsents(declined)
	if summary.Total != 2 || summary.Accepted != 1 || summary.Declined != 1 || summary.Pending != 0 {
		t.Fatalf("unexpected summary %+v", summary)
	}
}

// TestDecisionValidation covers the answer vocabulary.
func TestDecisionValidation(t *testing.T) {
	for _, decision := range []Decision{DecisionPending, DecisionAccepted, DecisionDeclined} {
		if err := decision.Validate(); err != nil {
			t.Fatalf("expected %s to be a valid decision: %v", decision, err)
		}
	}
	if err := Decision("maybe").Validate(); err == nil {
		t.Fatal("expected an unknown decision to be refused")
	}
	if err := DecisionPending.Answerable(); err == nil {
		t.Fatal("expected a member to be unable to submit the pending decision")
	}
	for _, decision := range []Decision{DecisionAccepted, DecisionDeclined} {
		if err := decision.Answerable(); err != nil {
			t.Fatalf("expected %s to be answerable: %v", decision, err)
		}
	}
}

// TestFindConsentAndCloneIsolation covers the consent lookup and copy.
func TestFindConsentAndCloneIsolation(t *testing.T) {
	decided := time.Date(2026, time.March, 2, 6, 0, 0, 0, time.UTC)
	consents := []Consent{
		{MatchID: "mch_1", MemberID: "mbr_a", Decision: DecisionAccepted, DecidedAt: &decided},
		{MatchID: "mch_1", MemberID: "mbr_b", Decision: DecisionPending},
	}
	found, err := FindConsent(consents, "mbr_a")
	if err != nil {
		t.Fatalf("expected the consent row to be found: %v", err)
	}
	if !found.Answered() {
		t.Fatal("expected the row to be marked as answered")
	}
	if _, err := FindConsent(consents, "mbr_c"); err == nil {
		t.Fatal("expected an unknown member to have no consent row")
	}
	copied := found.Clone()
	*copied.DecidedAt = copied.DecidedAt.Add(time.Hour)
	if found.DecidedAt.Equal(*copied.DecidedAt) {
		t.Fatal("expected the clone to own its timestamp")
	}
	if consents[1].Answered() {
		t.Fatal("expected a pending row to be unanswered")
	}
}

// TestActiveStatesCoverLiveIntroductions guards the list used by the uniqueness
// and quota queries.
func TestActiveStatesCoverLiveIntroductions(t *testing.T) {
	active := ActiveStates()
	if len(active) != 4 {
		t.Fatalf("expected four live states, got %d", len(active))
	}
	for _, state := range active {
		if state.Terminal() {
			t.Fatalf("%s is terminal and must not be treated as live", state)
		}
	}
}
