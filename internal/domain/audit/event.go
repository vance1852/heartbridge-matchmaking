// Package audit models the persisted operational trail. Audit rows are business
// records, not log lines: they are written inside the same transaction as the
// change they describe and can be queried by operations.
package audit

import (
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// Action is the operation an audit row describes.
type Action string

const (
	// ActionLogin records a successful authentication.
	ActionLogin Action = "auth.login"
	// ActionLogout records a session revocation.
	ActionLogout Action = "auth.logout"
	// ActionMatchProposed records a created introduction.
	ActionMatchProposed Action = "match.proposed"
	// ActionMatchConsent records a member decision.
	ActionMatchConsent Action = "match.consent"
	// ActionMatchCancelled records a withdrawn introduction.
	ActionMatchCancelled Action = "match.cancelled"
	// ActionMatchExpired records an introduction that timed out.
	ActionMatchExpired Action = "match.expired"
	// ActionMatchClosed records the final outcome of an introduction.
	ActionMatchClosed Action = "match.closed"
	// ActionMeetupBooked records a venue slot booking.
	ActionMeetupBooked Action = "meetup.booked"
	// ActionMeetupCheckedIn records the arrival confirmation.
	ActionMeetupCheckedIn Action = "meetup.checked_in"
	// ActionMeetupCompleted records a finished meetup and its settlement.
	ActionMeetupCompleted Action = "meetup.completed"
	// ActionMeetupCancelled records a withdrawn booking.
	ActionMeetupCancelled Action = "meetup.cancelled"
	// ActionFeedbackSubmitted records a participant report.
	ActionFeedbackSubmitted Action = "meetup.feedback"
	// ActionSlotPublished records a newly published venue slot.
	ActionSlotPublished Action = "venue.slot_published"
	// ActionEntitlementGranted records a granted service plan.
	ActionEntitlementGranted Action = "entitlement.granted"
)

// Result is the outcome recorded for an action.
type Result string

const (
	// ResultSuccess marks a completed operation.
	ResultSuccess Result = "success"
	// ResultRejected marks an operation refused by a business rule.
	ResultRejected Result = "rejected"
)

// Validate rejects unknown results.
func (r Result) Validate() error {
	switch r {
	case ResultSuccess, ResultRejected:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown audit result %q", string(r))
	}
}

// ObjectType names the entity an audit row points at.
type ObjectType string

const (
	// ObjectSession is an authentication session.
	ObjectSession ObjectType = "session"
	// ObjectMatch is a match.
	ObjectMatch ObjectType = "match"
	// ObjectMeetup is a meetup.
	ObjectMeetup ObjectType = "meetup"
	// ObjectVenueSlot is a venue slot.
	ObjectVenueSlot ObjectType = "venue_slot"
	// ObjectEntitlement is a member entitlement.
	ObjectEntitlement ObjectType = "entitlement"
)

// Event is one audit row.
type Event struct {
	ID         string
	ActorID    string
	ActorRole  string
	Action     Action
	ObjectType ObjectType
	ObjectID   string
	Result     Result
	Detail     string
	RequestID  string
	CreatedAt  time.Time
}

// Validate applies the structural rules of an audit row so that an incomplete
// trail can never be persisted.
func (e Event) Validate() error {
	if e.ActorID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "audit event must reference an actor")
	}
	if e.Action == "" {
		return apperr.New(apperr.CodeInvalidArgument, "audit event must reference an action")
	}
	if e.ObjectType == "" || e.ObjectID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "audit event must reference an object")
	}
	return e.Result.Validate()
}

// Clone returns an independent copy of the audit row.
func (e Event) Clone() Event { return e }
