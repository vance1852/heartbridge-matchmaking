package matching

import (
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// Decision is one member's answer to an introduction.
type Decision string

const (
	// DecisionPending means the member has not answered yet.
	DecisionPending Decision = "pending"
	// DecisionAccepted means the member wants to meet.
	DecisionAccepted Decision = "accepted"
	// DecisionDeclined means the member refuses the introduction.
	DecisionDeclined Decision = "declined"
)

// Validate rejects unknown decisions.
func (d Decision) Validate() error {
	switch d {
	case DecisionPending, DecisionAccepted, DecisionDeclined:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown consent decision %q", string(d))
	}
}

// Answerable rejects decisions that a member may not submit.
func (d Decision) Answerable() error {
	switch d {
	case DecisionAccepted, DecisionDeclined:
		return nil
	default:
		return apperr.New(apperr.CodeInvalidArgument, "consent decision must be accepted or declined")
	}
}

// Consent is one member's answer row for a match. Exactly two rows exist per
// match and both are created together with the match itself.
type Consent struct {
	MatchID   string
	MemberID  string
	Decision  Decision
	DecidedAt *time.Time
	UpdatedAt time.Time
}

// Answered reports whether the member already replied.
func (c Consent) Answered() bool { return c.Decision != DecisionPending }

// Clone returns an independent copy including the decision timestamp.
func (c Consent) Clone() Consent {
	copied := c
	if c.DecidedAt != nil {
		decided := *c.DecidedAt
		copied.DecidedAt = &decided
	}
	return copied
}

// ConsentOutcome summarises the two consent rows of a match.
type ConsentOutcome struct {
	Total    int
	Accepted int
	Declined int
	Pending  int
}

// SummarizeConsents folds consent rows into an outcome.
func SummarizeConsents(consents []Consent) ConsentOutcome {
	outcome := ConsentOutcome{Total: len(consents)}
	for _, consent := range consents {
		switch consent.Decision {
		case DecisionAccepted:
			outcome.Accepted++
		case DecisionDeclined:
			outcome.Declined++
		default:
			outcome.Pending++
		}
	}
	return outcome
}

// NextState derives the match state implied by the consent rows. It returns the
// current state unchanged while the collection is still incomplete.
func (o ConsentOutcome) NextState(current State) State {
	if current != StatePendingConsent {
		return current
	}
	if o.Declined > 0 {
		return StateClosedFailed
	}
	if o.Total > 0 && o.Accepted == o.Total {
		return StateConsented
	}
	return StatePendingConsent
}

// FindConsent locates the consent row of a member.
func FindConsent(consents []Consent, memberID string) (Consent, error) {
	for _, consent := range consents {
		if consent.MemberID == memberID {
			return consent, nil
		}
	}
	return Consent{}, apperr.Newf(apperr.CodeNotFound, "no consent row for member %s", memberID)
}
