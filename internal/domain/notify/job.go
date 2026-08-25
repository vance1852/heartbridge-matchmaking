// Package notify models the transactional outbox used to inform members and
// matchmakers about matching events. Jobs are written inside the same database
// transaction as the business change so that a committed change is never
// silently un-notified.
package notify

import (
	"math"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// Kind is the notification template a job refers to.
type Kind string

const (
	// KindMatchProposed informs both members about a new introduction.
	KindMatchProposed Kind = "match_proposed"
	// KindConsentReminder nudges a member whose consent window is closing.
	KindConsentReminder Kind = "consent_reminder"
	// KindMeetupBooked confirms the arranged venue slot.
	KindMeetupBooked Kind = "meetup_booked"
	// KindMeetupCompleted asks both participants for feedback.
	KindMeetupCompleted Kind = "meetup_completed"
	// KindMatchClosed reports the final outcome of an introduction.
	KindMatchClosed Kind = "match_closed"
)

// Validate rejects unknown notification kinds.
func (k Kind) Validate() error {
	switch k {
	case KindMatchProposed, KindConsentReminder, KindMeetupBooked, KindMeetupCompleted, KindMatchClosed:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown notification kind %q", string(k))
	}
}

// State is the delivery lifecycle of a job.
type State string

const (
	// StatePending is queued or waiting for its next attempt.
	StatePending State = "pending"
	// StateSucceeded was delivered.
	StateSucceeded State = "succeeded"
	// StateFailedPermanent exhausted its attempts and will not be retried.
	StateFailedPermanent State = "failed_permanent"
)

// Validate rejects unknown job states.
func (s State) Validate() error {
	switch s {
	case StatePending, StateSucceeded, StateFailedPermanent:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown notification state %q", string(s))
	}
}

// DefaultMaxAttempts is the attempt budget of a freshly enqueued job.
const DefaultMaxAttempts = 4

// BaseBackoff is the first retry delay; later delays grow exponentially.
const BaseBackoff = 2 * time.Second

// MaxBackoff caps the exponential growth so a job cannot be parked for hours.
const MaxBackoff = 5 * time.Minute

// Job is one outbox row.
type Job struct {
	ID            string
	MatchID       string
	RecipientID   string
	Kind          Kind
	Payload       string
	State         State
	Attempts      int
	MaxAttempts   int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Validate applies the structural rules of a job row.
func (j Job) Validate() error {
	if j.RecipientID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "notification must reference a recipient")
	}
	if err := j.Kind.Validate(); err != nil {
		return err
	}
	if j.MaxAttempts <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, "notification attempt budget must be positive")
	}
	if j.Attempts < 0 || j.Attempts > j.MaxAttempts {
		return apperr.New(apperr.CodeInvalidArgument, "notification attempt counter is out of range")
	}
	return j.State.Validate()
}

// Exhausted reports whether the attempt budget is used up.
func (j Job) Exhausted() bool { return j.Attempts >= j.MaxAttempts }

// Due reports whether the job may be attempted at the given instant.
func (j Job) Due(now time.Time) bool {
	return j.State == StatePending && !j.NextAttemptAt.After(now)
}

// Backoff returns the delay before the attempt following the given attempt
// count. The growth is deterministic so that retry tests need no sleeping.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	factor := math.Pow(2, float64(attempts-1))
	delay := time.Duration(float64(BaseBackoff) * factor)
	if delay > MaxBackoff || delay <= 0 {
		return MaxBackoff
	}
	return delay
}

// Clone returns an independent copy of the job.
func (j Job) Clone() Job { return j }
