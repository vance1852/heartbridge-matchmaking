// Package entitlement models the paid introduction allowance of a member: how
// many introductions the service plan grants, how many are currently reserved by
// live matches and how many were finally consumed by completed meetups.
package entitlement

import (
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// State is the lifecycle of an entitlement.
type State string

const (
	// StateActive entitlements can still reserve introductions.
	StateActive State = "active"
	// StateExhausted entitlements have no remaining introductions.
	StateExhausted State = "exhausted"
	// StateExpired entitlements passed their validity window.
	StateExpired State = "expired"
)

// Validate rejects unknown entitlement states.
func (s State) Validate() error {
	switch s {
	case StateActive, StateExhausted, StateExpired:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown entitlement state %q", string(s))
	}
}

// Plan is a purchasable service plan. It is reference data owned by operations.
type Plan struct {
	Code           string
	Name           string
	IntroQuota     int
	ValidDays      int
	MaxActiveMatch int
	ConsentHours   int
	Active         bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Validate applies the structural rules of a plan definition.
func (p Plan) Validate() error {
	if strings.TrimSpace(p.Code) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "plan code is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "plan name is required")
	}
	if p.IntroQuota <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, "plan introduction quota must be positive")
	}
	if p.ValidDays <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, "plan validity in days must be positive")
	}
	if p.MaxActiveMatch <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, "plan concurrent match limit must be positive")
	}
	if p.ConsentHours <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, "plan consent window in hours must be positive")
	}
	return nil
}

// Clone returns an independent copy of the plan.
func (p Plan) Clone() Plan { return p }

// Entitlement is the per-member allowance derived from a purchased plan.
//
// Reserved counts introductions held by live matches; Used counts introductions
// that reached a completed meetup. The invariant Used+Reserved <= Total is
// enforced by conditional SQL updates, not by read-then-write in the service.
type Entitlement struct {
	ID         string
	MemberID   string
	PlanCode   string
	Total      int
	Used       int
	Reserved   int
	State      State
	ValidFrom  time.Time
	ValidUntil time.Time
	Version    int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Available returns the number of introductions that can still be reserved.
func (e Entitlement) Available() int {
	remaining := e.Total - e.Used - e.Reserved
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Expired reports whether the validity window has elapsed.
func (e Entitlement) Expired(now time.Time) bool { return !now.Before(e.ValidUntil) }

// Started reports whether the validity window has begun.
func (e Entitlement) Started(now time.Time) bool { return !now.Before(e.ValidFrom) }

// CanReserve returns the stable business failure preventing a new reservation.
func (e Entitlement) CanReserve(now time.Time) error {
	if e.State != StateActive {
		return apperr.Newf(apperr.CodeQuotaExhausted,
			"entitlement of member %s is %s", e.MemberID, string(e.State))
	}
	if !e.Started(now) {
		return apperr.Newf(apperr.CodePreconditionFailed,
			"entitlement of member %s is not valid yet", e.MemberID)
	}
	if e.Expired(now) {
		return apperr.Newf(apperr.CodeQuotaExhausted,
			"entitlement of member %s expired at %s", e.MemberID, e.ValidUntil.UTC().Format(time.RFC3339))
	}
	if e.Available() <= 0 {
		return apperr.Newf(apperr.CodeQuotaExhausted,
			"member %s has no remaining introductions", e.MemberID)
	}
	return nil
}

// Validate applies the structural invariants of a stored entitlement.
func (e Entitlement) Validate() error {
	if e.MemberID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "entitlement must reference a member")
	}
	if e.Total <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, "entitlement total must be positive")
	}
	if e.Used < 0 || e.Reserved < 0 {
		return apperr.New(apperr.CodeInvalidArgument, "entitlement counters must not be negative")
	}
	if e.Used+e.Reserved > e.Total {
		return apperr.New(apperr.CodeInvalidArgument, "entitlement counters exceed the granted total")
	}
	if !e.ValidUntil.After(e.ValidFrom) {
		return apperr.New(apperr.CodeInvalidArgument, "entitlement validity window is empty")
	}
	return e.State.Validate()
}

// Clone returns an independent copy of the entitlement.
func (e Entitlement) Clone() Entitlement { return e }

// Reason explains why a ledger row was written.
type Reason string

const (
	// ReasonReserve holds one introduction for a newly created match.
	ReasonReserve Reason = "reserve"
	// ReasonRelease returns a held introduction after a cancelled or expired match.
	ReasonRelease Reason = "release"
	// ReasonConsume converts a held introduction into a consumed one.
	ReasonConsume Reason = "consume"
)

// Validate rejects unknown ledger reasons.
func (r Reason) Validate() error {
	switch r {
	case ReasonReserve, ReasonRelease, ReasonConsume:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown ledger reason %q", string(r))
	}
}

// LedgerEntry is an append-only record of an entitlement movement. The storage
// layer enforces uniqueness over (entitlement, match, reason) so that a retried
// or duplicated release can never double-count.
type LedgerEntry struct {
	ID            string
	EntitlementID string
	MatchID       string
	Reason        Reason
	DeltaReserved int
	DeltaUsed     int
	CreatedAt     time.Time
}

// Validate applies the structural rules of a ledger row.
func (l LedgerEntry) Validate() error {
	if l.EntitlementID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "ledger entry must reference an entitlement")
	}
	if l.MatchID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "ledger entry must reference a match")
	}
	if err := l.Reason.Validate(); err != nil {
		return err
	}
	if l.DeltaReserved == 0 && l.DeltaUsed == 0 {
		return apperr.New(apperr.CodeInvalidArgument, "ledger entry must move at least one counter")
	}
	return nil
}

// Clone returns an independent copy of the ledger entry.
func (l LedgerEntry) Clone() LedgerEntry { return l }

// Balance folds a ledger into the counters it represents. It is used by the
// consistency checks that compare the ledger with the entitlement row.
func Balance(entries []LedgerEntry) (reserved int, used int) {
	for _, entry := range entries {
		reserved += entry.DeltaReserved
		used += entry.DeltaUsed
	}
	return reserved, used
}
