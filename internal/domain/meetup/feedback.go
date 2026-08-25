package meetup

import (
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// Intent is the participant's answer about continuing after the meetup.
type Intent string

const (
	// IntentContinue means the participant wants to keep going.
	IntentContinue Intent = "continue"
	// IntentStop means the participant wants to end the introduction.
	IntentStop Intent = "stop"
)

// Validate rejects unknown intents.
func (i Intent) Validate() error {
	switch i {
	case IntentContinue, IntentStop:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown feedback intent %q", string(i))
	}
}

// Feedback is one participant's report about a completed meetup. Exactly one row
// per participant is allowed, enforced by a unique constraint in storage.
type Feedback struct {
	ID             string
	MeetupID       string
	AuthorMemberID string
	Intent         Intent
	Rating         int
	Comment        string
	CreatedAt      time.Time
}

// Validate applies the structural rules of a feedback row.
func (f Feedback) Validate() error {
	if f.MeetupID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "feedback must reference a meetup")
	}
	if f.AuthorMemberID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "feedback must reference its author")
	}
	if err := f.Intent.Validate(); err != nil {
		return err
	}
	if f.Rating < 1 || f.Rating > 5 {
		return apperr.New(apperr.CodeInvalidArgument, "feedback rating must be between 1 and 5")
	}
	if len(strings.TrimSpace(f.Comment)) > 500 {
		return apperr.New(apperr.CodeInvalidArgument, "feedback comment must not exceed 500 characters")
	}
	return nil
}

// Clone returns an independent copy of the feedback row.
func (f Feedback) Clone() Feedback { return f }

// FeedbackOutcome summarises the feedback rows of a meetup.
type FeedbackOutcome struct {
	Total    int
	Continue int
	Stop     int
}

// SummarizeFeedback folds feedback rows into an outcome.
func SummarizeFeedback(entries []Feedback) FeedbackOutcome {
	outcome := FeedbackOutcome{Total: len(entries)}
	for _, entry := range entries {
		if entry.Intent == IntentContinue {
			outcome.Continue++
		} else {
			outcome.Stop++
		}
	}
	return outcome
}

// Complete reports whether every participant of a two-person meetup answered.
func (o FeedbackOutcome) Complete() bool { return o.Total >= 2 }

// MutualContinue reports whether both participants want to keep going.
func (o FeedbackOutcome) MutualContinue() bool { return o.Complete() && o.Stop == 0 }

// AuthoredBy reports whether the member already submitted feedback.
func AuthoredBy(entries []Feedback, memberID string) bool {
	for _, entry := range entries {
		if entry.AuthorMemberID == memberID {
			return true
		}
	}
	return false
}
