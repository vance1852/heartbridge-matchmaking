package entitlement

import (
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// reference is the instant used by the validity assertions.
var reference = time.Date(2026, time.March, 2, 2, 0, 0, 0, time.UTC)

// newAllowance builds an allowance fixture.
func newAllowance() Entitlement {
	return Entitlement{
		ID:         "ent_1",
		MemberID:   "mbr_a",
		PlanCode:   "starter",
		Total:      4,
		Used:       0,
		Reserved:   0,
		State:      StateActive,
		ValidFrom:  reference.Add(-time.Hour),
		ValidUntil: reference.Add(180 * 24 * time.Hour),
		Version:    1,
		CreatedAt:  reference,
		UpdatedAt:  reference,
	}
}

// TestAvailableNeverGoesNegative verifies the remaining allowance calculation.
func TestAvailableNeverGoesNegative(t *testing.T) {
	allowance := newAllowance()
	if allowance.Available() != 4 {
		t.Fatalf("expected 4 available, got %d", allowance.Available())
	}
	allowance.Used = 2
	allowance.Reserved = 1
	if allowance.Available() != 1 {
		t.Fatalf("expected 1 available, got %d", allowance.Available())
	}
	// A corrupted row must not report a negative remainder to callers.
	allowance.Used = 5
	if allowance.Available() != 0 {
		t.Fatalf("expected 0 available, got %d", allowance.Available())
	}
}

// TestCanReserveReportsThePreciseReason covers every refusal branch.
func TestCanReserveReportsThePreciseReason(t *testing.T) {
	allowance := newAllowance()
	if err := allowance.CanReserve(reference); err != nil {
		t.Fatalf("expected a fresh allowance to be usable: %v", err)
	}

	notStarted := newAllowance()
	notStarted.ValidFrom = reference.Add(time.Hour)
	err := notStarted.CanReserve(reference)
	if code := apperr.CodeOf(err); code != apperr.CodePreconditionFailed {
		t.Fatalf("expected code %s before the validity window, got %s (%v)",
			apperr.CodePreconditionFailed, code, err)
	}

	expired := newAllowance()
	expired.ValidUntil = reference.Add(-time.Minute)
	if code := apperr.CodeOf(expired.CanReserve(reference)); code != apperr.CodeQuotaExhausted {
		t.Fatalf("expected an expired allowance to report %s, got %s", apperr.CodeQuotaExhausted, code)
	}
	// The upper bound is exclusive: the exact instant is already outside.
	boundary := newAllowance()
	boundary.ValidUntil = reference
	if boundary.CanReserve(reference) == nil {
		t.Fatal("expected the validity end to be exclusive")
	}

	drained := newAllowance()
	drained.Used = 3
	drained.Reserved = 1
	if code := apperr.CodeOf(drained.CanReserve(reference)); code != apperr.CodeQuotaExhausted {
		t.Fatalf("expected a drained allowance to report %s, got %s", apperr.CodeQuotaExhausted, code)
	}

	for _, state := range []State{StateExhausted, StateExpired} {
		inactive := newAllowance()
		inactive.State = state
		if code := apperr.CodeOf(inactive.CanReserve(reference)); code != apperr.CodeQuotaExhausted {
			t.Fatalf("expected state %s to report %s", state, apperr.CodeQuotaExhausted)
		}
	}
}

// TestValidityWindowHelpers covers the boundary semantics of the window.
func TestValidityWindowHelpers(t *testing.T) {
	allowance := newAllowance()
	if !allowance.Started(reference) {
		t.Fatal("expected the window to have started")
	}
	if allowance.Started(allowance.ValidFrom.Add(-time.Nanosecond)) {
		t.Fatal("expected the window not to have started yet")
	}
	if allowance.Expired(reference) {
		t.Fatal("expected the window to still be open")
	}
	if !allowance.Expired(allowance.ValidUntil) {
		t.Fatal("expected the window end to be exclusive")
	}
}

// TestAllowanceValidationRejectsBrokenCounters covers the structural invariants.
func TestAllowanceValidationRejectsBrokenCounters(t *testing.T) {
	if err := newAllowance().Validate(); err != nil {
		t.Fatalf("expected a well formed allowance to validate: %v", err)
	}
	cases := map[string]func(Entitlement) Entitlement{
		"no member":     func(e Entitlement) Entitlement { e.MemberID = ""; return e },
		"zero total":    func(e Entitlement) Entitlement { e.Total = 0; return e },
		"negative used": func(e Entitlement) Entitlement { e.Used = -1; return e },
		"negative held": func(e Entitlement) Entitlement { e.Reserved = -1; return e },
		"overcommitted": func(e Entitlement) Entitlement { e.Used = 3; e.Reserved = 2; return e },
		"empty window":  func(e Entitlement) Entitlement { e.ValidUntil = e.ValidFrom; return e },
		"unknown state": func(e Entitlement) Entitlement { e.State = "paused"; return e },
	}
	for name, mutate := range cases {
		if err := mutate(newAllowance()).Validate(); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}
}

// TestPlanValidationRejectsIncompleteDefinitions covers the plan rules.
func TestPlanValidationRejectsIncompleteDefinitions(t *testing.T) {
	base := Plan{
		Code: "starter", Name: "Starter", IntroQuota: 4,
		ValidDays: 180, MaxActiveMatch: 2, ConsentHours: 48, Active: true,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("expected a complete plan to validate: %v", err)
	}
	cases := map[string]func(Plan) Plan{
		"blank code":    func(p Plan) Plan { p.Code = " "; return p },
		"blank name":    func(p Plan) Plan { p.Name = ""; return p },
		"zero quota":    func(p Plan) Plan { p.IntroQuota = 0; return p },
		"zero validity": func(p Plan) Plan { p.ValidDays = 0; return p },
		"zero live cap": func(p Plan) Plan { p.MaxActiveMatch = 0; return p },
		"zero window":   func(p Plan) Plan { p.ConsentHours = 0; return p },
	}
	for name, mutate := range cases {
		if err := mutate(base).Validate(); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}
	if clone := base.Clone(); clone != base {
		t.Fatal("expected the plan copy to be equal to its source")
	}
}

// TestLedgerValidationAndBalance covers the movement rules and the fold.
func TestLedgerValidationAndBalance(t *testing.T) {
	base := LedgerEntry{
		ID:            "led_1",
		EntitlementID: "ent_1",
		MatchID:       "mch_1",
		Reason:        ReasonReserve,
		DeltaReserved: 1,
		CreatedAt:     reference,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("expected a complete movement to validate: %v", err)
	}
	cases := map[string]func(LedgerEntry) LedgerEntry{
		"no allowance":   func(l LedgerEntry) LedgerEntry { l.EntitlementID = ""; return l },
		"no match":       func(l LedgerEntry) LedgerEntry { l.MatchID = ""; return l },
		"unknown reason": func(l LedgerEntry) LedgerEntry { l.Reason = "refund"; return l },
		"no movement":    func(l LedgerEntry) LedgerEntry { l.DeltaReserved = 0; l.DeltaUsed = 0; return l },
	}
	for name, mutate := range cases {
		if err := mutate(base).Validate(); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}

	reserved, used := Balance([]LedgerEntry{
		{Reason: ReasonReserve, DeltaReserved: 1},
		{Reason: ReasonReserve, DeltaReserved: 1},
		{Reason: ReasonRelease, DeltaReserved: -1},
		{Reason: ReasonConsume, DeltaReserved: -1, DeltaUsed: 1},
	})
	if reserved != 0 || used != 1 {
		t.Fatalf("expected reserved=0 used=1, got reserved=%d used=%d", reserved, used)
	}
	if empty, emptyUsed := Balance(nil); empty != 0 || emptyUsed != 0 {
		t.Fatalf("expected an empty ledger to balance at zero, got %d and %d", empty, emptyUsed)
	}
}

// TestReasonAndStateVocabulary guards the persisted enumerations.
func TestReasonAndStateVocabulary(t *testing.T) {
	for _, reason := range []Reason{ReasonReserve, ReasonRelease, ReasonConsume} {
		if err := reason.Validate(); err != nil {
			t.Fatalf("expected %s to be a valid reason: %v", reason, err)
		}
	}
	if err := Reason("cancel").Validate(); err == nil {
		t.Fatal("expected an unknown reason to be refused")
	}
	for _, state := range []State{StateActive, StateExhausted, StateExpired} {
		if err := state.Validate(); err != nil {
			t.Fatalf("expected %s to be a valid state: %v", state, err)
		}
	}
	if err := State("frozen").Validate(); err == nil {
		t.Fatal("expected an unknown state to be refused")
	}
}
