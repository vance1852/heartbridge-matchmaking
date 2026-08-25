// Package matching models the core artefact of the platform: a match between two
// members proposed by a matchmaker, its consent collection and its state machine.
package matching

import (
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// State is the lifecycle state of a match.
type State string

const (
	// StatePendingConsent waits for both members to answer the introduction.
	StatePendingConsent State = "pending_consent"
	// StateConsented means both members accepted and a meetup can be arranged.
	StateConsented State = "consented"
	// StateScheduled means a venue slot has been booked for the meetup.
	StateScheduled State = "scheduled"
	// StateMet means the meetup actually happened.
	StateMet State = "met"
	// StateClosedSuccess means both sides want to continue after the meetup.
	StateClosedSuccess State = "closed_success"
	// StateClosedFailed means the introduction did not lead anywhere.
	StateClosedFailed State = "closed_failed"
	// StateCancelled means a matchmaker or member withdrew the introduction.
	StateCancelled State = "cancelled"
	// StateExpired means the consent window elapsed without both answers.
	StateExpired State = "expired"
)

// allowedTransitions is the single source of truth for the match state machine.
var allowedTransitions = map[State][]State{
	StatePendingConsent: {StateConsented, StateCancelled, StateExpired, StateClosedFailed},
	StateConsented:      {StateScheduled, StateCancelled, StateExpired},
	StateScheduled:      {StateMet, StateCancelled},
	StateMet:            {StateClosedSuccess, StateClosedFailed},
	StateClosedSuccess:  nil,
	StateClosedFailed:   nil,
	StateCancelled:      nil,
	StateExpired:        nil,
}

// Validate rejects unknown match states.
func (s State) Validate() error {
	if _, ok := allowedTransitions[s]; !ok {
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown match state %q", string(s))
	}
	return nil
}

// Terminal reports whether the state admits no further transition.
func (s State) Terminal() bool { return len(allowedTransitions[s]) == 0 }

// HoldsQuota reports whether a match in this state still holds a reserved
// introduction. Releasing quota is only correct for states that held it.
func (s State) HoldsQuota() bool {
	switch s {
	case StatePendingConsent, StateConsented, StateScheduled:
		return true
	default:
		return false
	}
}

// OccupiesSlot reports whether a match in this state still occupies a venue slot.
func (s State) OccupiesSlot() bool {
	return s == StateScheduled
}

// ActiveStates lists the states considered live for uniqueness and quota rules.
func ActiveStates() []State {
	return []State{StatePendingConsent, StateConsented, StateScheduled, StateMet}
}

// Match is one introduction between two members proposed by a matchmaker.
//
// MemberAID and MemberBID are stored in a canonical order so that the pair
// uniqueness index cannot be bypassed by swapping the arguments.
type Match struct {
	ID              string
	MatchmakerID    string
	MemberAID       string
	MemberBID       string
	State           State
	Version         int64
	ConsentDeadline time.Time
	ClosingNote     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ClosedAt        *time.Time
}

// CanonicalPair orders two member ids so that (a,b) and (b,a) map to one key.
func CanonicalPair(left, right string) (string, string) {
	if left <= right {
		return left, right
	}
	return right, left
}

// PairKey returns the stable uniqueness key of a member pair.
func PairKey(left, right string) string {
	first, second := CanonicalPair(left, right)
	return first + "|" + second
}

// Participants returns both member ids of the match.
func (m Match) Participants() []string { return []string{m.MemberAID, m.MemberBID} }

// HasParticipant reports whether the given member takes part in the match.
func (m Match) HasParticipant(memberID string) bool {
	return memberID != "" && (m.MemberAID == memberID || m.MemberBID == memberID)
}

// Counterpart returns the other member of the pair.
func (m Match) Counterpart(memberID string) (string, error) {
	switch memberID {
	case m.MemberAID:
		return m.MemberBID, nil
	case m.MemberBID:
		return m.MemberAID, nil
	default:
		return "", apperr.Newf(apperr.CodeForbidden, "member %s does not take part in match %s", memberID, m.ID)
	}
}

// CanTransition validates a state machine edge and returns a stable error when
// the edge does not exist.
func (m Match) CanTransition(target State) error {
	if err := target.Validate(); err != nil {
		return err
	}
	for _, candidate := range allowedTransitions[m.State] {
		if candidate == target {
			return nil
		}
	}
	return apperr.Newf(apperr.CodeIllegalTransition,
		"match %s cannot move from %s to %s", m.ID, string(m.State), string(target))
}

// ConsentExpired reports whether the consent window elapsed.
func (m Match) ConsentExpired(now time.Time) bool {
	return m.State == StatePendingConsent && !now.Before(m.ConsentDeadline)
}

// Validate applies the structural rules of a match record.
func (m Match) Validate() error {
	if m.MatchmakerID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "match must reference a matchmaker")
	}
	if m.MemberAID == "" || m.MemberBID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "match must reference two members")
	}
	if m.MemberAID == m.MemberBID {
		return apperr.New(apperr.CodeInvalidArgument, "match must reference two distinct members")
	}
	if m.MemberAID > m.MemberBID {
		return apperr.New(apperr.CodeInvalidArgument, "match member ids must be stored in canonical order")
	}
	if m.ConsentDeadline.IsZero() {
		return apperr.New(apperr.CodeInvalidArgument, "match consent deadline is required")
	}
	return m.State.Validate()
}

// Clone returns an independent copy including the closing timestamp.
func (m Match) Clone() Match {
	copied := m
	if m.ClosedAt != nil {
		closed := *m.ClosedAt
		copied.ClosedAt = &closed
	}
	return copied
}
