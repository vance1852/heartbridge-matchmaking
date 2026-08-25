package meetup

import (
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// State is the lifecycle state of a booked meetup.
type State string

const (
	// StateBooked means a slot is held but the participants have not arrived.
	StateBooked State = "booked"
	// StateCheckedIn means the matchmaker confirmed both participants arrived.
	StateCheckedIn State = "checked_in"
	// StateCompleted means the meetup finished and feedback can be submitted.
	StateCompleted State = "completed"
	// StateCancelled means the booking was withdrawn before the meetup.
	StateCancelled State = "cancelled"
	// StateNoShow means at least one participant did not arrive.
	StateNoShow State = "no_show"
)

var allowedTransitions = map[State][]State{
	StateBooked:    {StateCheckedIn, StateCancelled, StateNoShow},
	StateCheckedIn: {StateCompleted, StateNoShow},
	StateCompleted: nil,
	StateCancelled: nil,
	StateNoShow:    nil,
}

// Validate rejects unknown meetup states.
func (s State) Validate() error {
	if _, ok := allowedTransitions[s]; !ok {
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown meetup state %q", string(s))
	}
	return nil
}

// Terminal reports whether the state admits no further transition.
func (s State) Terminal() bool { return len(allowedTransitions[s]) == 0 }

// OccupiesSlot reports whether a meetup in this state still holds a venue seat.
func (s State) OccupiesSlot() bool {
	switch s {
	case StateBooked, StateCheckedIn, StateCompleted:
		return true
	default:
		return false
	}
}

// ActiveStates lists the states that block a second meetup for the same match.
func ActiveStates() []State {
	return []State{StateBooked, StateCheckedIn, StateCompleted}
}

// Meetup is the offline appointment created once both members accepted.
type Meetup struct {
	ID          string
	MatchID     string
	SlotID      string
	State       State
	Version     int64
	BookedAt    time.Time
	CheckedInAt *time.Time
	CompletedAt *time.Time
	ClosedAt    *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CanTransition validates a state machine edge.
func (m Meetup) CanTransition(target State) error {
	if err := target.Validate(); err != nil {
		return err
	}
	for _, candidate := range allowedTransitions[m.State] {
		if candidate == target {
			return nil
		}
	}
	return apperr.Newf(apperr.CodeIllegalTransition,
		"meetup %s cannot move from %s to %s", m.ID, string(m.State), string(target))
}

// Validate applies the structural rules of a meetup record.
func (m Meetup) Validate() error {
	if m.MatchID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "meetup must reference a match")
	}
	if m.SlotID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "meetup must reference a venue slot")
	}
	return m.State.Validate()
}

// Clone returns an independent copy including all optional timestamps.
func (m Meetup) Clone() Meetup {
	copied := m
	if m.CheckedInAt != nil {
		value := *m.CheckedInAt
		copied.CheckedInAt = &value
	}
	if m.CompletedAt != nil {
		value := *m.CompletedAt
		copied.CompletedAt = &value
	}
	if m.ClosedAt != nil {
		value := *m.ClosedAt
		copied.ClosedAt = &value
	}
	return copied
}

// Booking pairs a meetup with the slot window it occupies. It is the read model
// used to detect overlapping appointments for the same member.
type Booking struct {
	Meetup  Meetup
	StartAt time.Time
	EndAt   time.Time
}

// Clone returns an independent copy of the booking.
func (b Booking) Clone() Booking {
	return Booking{Meetup: b.Meetup.Clone(), StartAt: b.StartAt, EndAt: b.EndAt}
}

// ConflictsWith reports whether the booking window intersects the given window.
func (b Booking) ConflictsWith(start, end time.Time) bool {
	return b.StartAt.Before(end) && start.Before(b.EndAt)
}
