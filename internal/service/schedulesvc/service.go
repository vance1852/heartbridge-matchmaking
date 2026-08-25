// Package schedulesvc implements the offline path: booking a venue slot for an
// accepted introduction, checking in, settling the allowance on completion and
// collecting the feedback that closes the introduction.
package schedulesvc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/auditlog"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/notify"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/security"
)

// Service owns the meetup lifecycle.
type Service struct {
	tx            repository.TxManager
	matches       repository.MatchRepository
	meetups       repository.MeetupRepository
	slots         repository.SlotRepository
	entitlements  repository.EntitlementRepository
	notifications repository.NotificationRepository
	audit         *auditlog.Recorder
	clock         clock.Clock
}

// Dependencies bundles the collaborators of the service.
type Dependencies struct {
	Tx            repository.TxManager
	Matches       repository.MatchRepository
	Meetups       repository.MeetupRepository
	Slots         repository.SlotRepository
	Entitlements  repository.EntitlementRepository
	Notifications repository.NotificationRepository
	Audit         *auditlog.Recorder
	Clock         clock.Clock
}

// New builds the service.
func New(deps Dependencies) *Service {
	return &Service{
		tx:            deps.Tx,
		matches:       deps.Matches,
		meetups:       deps.Meetups,
		slots:         deps.Slots,
		entitlements:  deps.Entitlements,
		notifications: deps.Notifications,
		audit:         deps.Audit,
		clock:         deps.Clock,
	}
}

// Detail is the read model of one meetup.
type Detail struct {
	Meetup   meetup.Meetup
	Slot     meetup.VenueSlot
	Match    matching.Match
	Feedback []meetup.Feedback
}

// Book reserves a venue slot for an accepted introduction.
//
// Three scarce resources move together here: the match state, the venue seat and
// the meetup row. The seat is taken with a conditional update so that two
// matchmakers racing for the last seat cannot both win, and the whole set of
// writes shares one transaction so a rejected match transition never leaves a
// phantom seat behind.
func (s *Service) Book(ctx context.Context, actor identity.Actor, matchID, slotID string) (Detail, error) {
	if err := actor.RequireRole(identity.RoleMatchmaker, identity.RoleAdmin); err != nil {
		return Detail{}, err
	}
	current, err := s.matches.GetByID(ctx, matchID)
	if err != nil {
		return Detail{}, err
	}
	if err := current.CanTransition(matching.StateScheduled); err != nil {
		return Detail{}, err
	}
	consents, err := s.matches.ListConsents(ctx, matchID)
	if err != nil {
		return Detail{}, err
	}
	outcome := matching.SummarizeConsents(consents)
	if outcome.Accepted != outcome.Total || outcome.Total == 0 {
		return Detail{}, apperr.New(apperr.CodePreconditionFailed,
			"a meetup may only be arranged once both members accepted")
	}
	slot, err := s.slots.GetByID(ctx, slotID)
	if err != nil {
		return Detail{}, err
	}
	now := s.clock.Now()
	if err := slot.Bookable(now); err != nil {
		return Detail{}, err
	}
	if err := s.rejectOverlaps(ctx, current, slot); err != nil {
		return Detail{}, err
	}

	meetupID, err := security.NewID("mtp")
	if err != nil {
		return Detail{}, err
	}
	appointment := meetup.Meetup{
		ID:        meetupID,
		MatchID:   matchID,
		SlotID:    slotID,
		State:     meetup.StateBooked,
		Version:   1,
		BookedAt:  now,
		CreatedAt: now,
		UpdatedAt: now,
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.slots.TryBook(ctx, slotID, now); err != nil {
			return err
		}
		if err := s.meetups.Create(ctx, appointment); err != nil {
			return err
		}
		if err := s.matches.UpdateState(ctx, matchID, current.Version,
			matching.StateScheduled, "meetup arranged", now); err != nil {
			return err
		}
		for _, participant := range current.Participants() {
			if err := s.enqueue(ctx, matchID, participant, notify.KindMeetupBooked,
				fmt.Sprintf("your meetup is booked at %s on %s",
					slot.VenueName, slot.BusinessDate()), now); err != nil {
				return err
			}
		}
		return s.audit.Success(ctx, actor, audit.ActionMeetupBooked, audit.ObjectMeetup, meetupID,
			map[string]any{"match_id": matchID, "slot_id": slotID, "venue": slot.VenueCode})
	})
	if err != nil {
		s.audit.Rejected(ctx, actor, audit.ActionMeetupBooked, audit.ObjectMeetup, meetupID, err)
		return Detail{}, err
	}
	logging.FromContext(ctx).Info("meetup booked", "meetup_id", meetupID, "match_id", matchID, "slot_id", slotID)
	return s.Get(ctx, actor, meetupID)
}

// rejectOverlaps refuses a slot that collides with an existing appointment of
// either participant.
func (s *Service) rejectOverlaps(ctx context.Context, current matching.Match, slot meetup.VenueSlot) error {
	for _, memberID := range current.Participants() {
		bookings, err := s.meetups.ListBookingsForMember(ctx, memberID, slot.StartAt, slot.EndAt)
		if err != nil {
			return err
		}
		for _, booking := range bookings {
			if booking.ConflictsWith(slot.StartAt, slot.EndAt) {
				return apperr.Newf(apperr.CodeConflict,
					"member %s already has a meetup overlapping this time window", memberID)
			}
		}
	}
	return nil
}

// CheckIn confirms that both participants arrived.
func (s *Service) CheckIn(ctx context.Context, actor identity.Actor, meetupID string) (Detail, error) {
	if err := actor.RequireRole(identity.RoleMatchmaker, identity.RoleAdmin); err != nil {
		return Detail{}, err
	}
	appointment, err := s.meetups.GetByID(ctx, meetupID)
	if err != nil {
		return Detail{}, err
	}
	if err := appointment.CanTransition(meetup.StateCheckedIn); err != nil {
		return Detail{}, err
	}
	now := s.clock.Now()
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.meetups.UpdateState(ctx, meetupID, appointment.Version, meetup.StateCheckedIn, now); err != nil {
			return err
		}
		return s.audit.Success(ctx, actor, audit.ActionMeetupCheckedIn, audit.ObjectMeetup, meetupID,
			map[string]any{"match_id": appointment.MatchID})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, actor, meetupID)
}

// Complete finishes a meetup and settles the allowance of both members.
//
// This is the point where a held introduction becomes a consumed one. The
// conversion is a conditional update guarded by the entitlement version, and the
// ledger movement is unique per match, so replaying the settlement can neither
// double-charge nor silently skip a member.
func (s *Service) Complete(ctx context.Context, actor identity.Actor, meetupID string) (Detail, error) {
	if err := actor.RequireRole(identity.RoleMatchmaker, identity.RoleAdmin); err != nil {
		return Detail{}, err
	}
	appointment, err := s.meetups.GetByID(ctx, meetupID)
	if err != nil {
		return Detail{}, err
	}
	if err := appointment.CanTransition(meetup.StateCompleted); err != nil {
		return Detail{}, err
	}
	current, err := s.matches.GetByID(ctx, appointment.MatchID)
	if err != nil {
		return Detail{}, err
	}
	if err := current.CanTransition(matching.StateMet); err != nil {
		return Detail{}, err
	}
	now := s.clock.Now()

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.meetups.UpdateState(ctx, meetupID, appointment.Version, meetup.StateCompleted, now); err != nil {
			return err
		}
		if err := s.matches.UpdateState(ctx, current.ID, current.Version,
			matching.StateMet, "meetup completed", now); err != nil {
			return err
		}
		if err := s.settle(ctx, current, now); err != nil {
			return err
		}
		for _, participant := range current.Participants() {
			if err := s.enqueue(ctx, current.ID, participant, notify.KindMeetupCompleted,
				"please share how the meetup went", now); err != nil {
				return err
			}
		}
		return s.audit.Success(ctx, actor, audit.ActionMeetupCompleted, audit.ObjectMeetup, meetupID,
			map[string]any{"match_id": current.ID})
	})
	if err != nil {
		s.audit.Rejected(ctx, actor, audit.ActionMeetupCompleted, audit.ObjectMeetup, meetupID, err)
		return Detail{}, err
	}
	return s.Get(ctx, actor, meetupID)
}

// settle turns the reserved introduction of both members into a consumed one.
func (s *Service) settle(ctx context.Context, current matching.Match, now time.Time) error {
	for _, memberID := range current.Participants() {
		granted, err := s.entitlements.GetActiveByMember(ctx, memberID)
		if err != nil {
			return err
		}
		if err := s.entitlements.Consume(ctx, granted.ID, granted.Version, now); err != nil {
			return err
		}
		ledgerID, err := security.NewID("led")
		if err != nil {
			return err
		}
		if err := s.entitlements.AppendLedger(ctx, entitlement.LedgerEntry{
			ID:            ledgerID,
			EntitlementID: granted.ID,
			MatchID:       current.ID,
			Reason:        entitlement.ReasonConsume,
			DeltaReserved: -1,
			DeltaUsed:     1,
			CreatedAt:     now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Cancel withdraws a booked meetup and returns the venue seat. The introduction
// falls back to the consented state so that another slot can be arranged.
func (s *Service) Cancel(ctx context.Context, actor identity.Actor, meetupID, reason string) (Detail, error) {
	if err := actor.RequireRole(identity.RoleMatchmaker, identity.RoleAdmin); err != nil {
		return Detail{}, err
	}
	appointment, err := s.meetups.GetByID(ctx, meetupID)
	if err != nil {
		return Detail{}, err
	}
	if err := appointment.CanTransition(meetup.StateCancelled); err != nil {
		return Detail{}, err
	}
	current, err := s.matches.GetByID(ctx, appointment.MatchID)
	if err != nil {
		return Detail{}, err
	}
	now := s.clock.Now()
	note := reason
	if note == "" {
		note = "meetup withdrawn"
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.meetups.UpdateState(ctx, meetupID, appointment.Version, meetup.StateCancelled, now); err != nil {
			return err
		}
		if err := s.slots.ReleaseBooking(ctx, appointment.SlotID, now); err != nil {
			return err
		}
		if current.State == matching.StateScheduled {
			// The introduction itself stays alive, so its reserved allowance is
			// intentionally left in place: the pair may book another slot.
			if err := s.matches.UpdateState(ctx, current.ID, current.Version,
				matching.StateConsented, note, now); err != nil {
				return err
			}
		}
		return s.audit.Success(ctx, actor, audit.ActionMeetupCancelled, audit.ObjectMeetup, meetupID,
			map[string]any{"match_id": current.ID, "reason": note})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, actor, meetupID)
}

// FeedbackInput is one participant report.
type FeedbackInput struct {
	Intent  meetup.Intent
	Rating  int
	Comment string
}

// SubmitFeedback records the report of one participant and closes the
// introduction once both answered. Only the two participants may report, which is
// an ownership rule rather than a role rule.
func (s *Service) SubmitFeedback(
	ctx context.Context, actor identity.Actor, meetupID string, input FeedbackInput,
) (Detail, error) {
	memberID, err := actor.RequireMember()
	if err != nil {
		return Detail{}, err
	}
	appointment, err := s.meetups.GetByID(ctx, meetupID)
	if err != nil {
		return Detail{}, err
	}
	if appointment.State != meetup.StateCompleted {
		return Detail{}, apperr.New(apperr.CodePreconditionFailed,
			"feedback can only be given once the meetup is completed")
	}
	current, err := s.matches.GetByID(ctx, appointment.MatchID)
	if err != nil {
		return Detail{}, err
	}
	if !current.HasParticipant(memberID) {
		return Detail{}, apperr.New(apperr.CodeForbidden, "only the two participants may report on this meetup")
	}
	feedbackID, err := security.NewID("fbk")
	if err != nil {
		return Detail{}, err
	}
	now := s.clock.Now()
	report := meetup.Feedback{
		ID:             feedbackID,
		MeetupID:       meetupID,
		AuthorMemberID: memberID,
		Intent:         input.Intent,
		Rating:         input.Rating,
		Comment:        input.Comment,
		CreatedAt:      now,
	}
	if err := report.Validate(); err != nil {
		return Detail{}, err
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.meetups.AddFeedback(ctx, report); err != nil {
			return err
		}
		reports, err := s.meetups.ListFeedback(ctx, meetupID)
		if err != nil {
			return err
		}
		summary := meetup.SummarizeFeedback(reports)
		if summary.Complete() && current.State == matching.StateMet {
			target := matching.StateClosedFailed
			note := "at least one side chose not to continue"
			if summary.MutualContinue() {
				target = matching.StateClosedSuccess
				note = "both sides want to continue"
			}
			if err := s.matches.UpdateState(ctx, current.ID, current.Version, target, note, now); err != nil {
				return err
			}
			for _, participant := range current.Participants() {
				if err := s.enqueue(ctx, current.ID, participant, notify.KindMatchClosed, note, now); err != nil {
					return err
				}
			}
			if err := s.audit.Success(ctx, actor, audit.ActionMatchClosed, audit.ObjectMatch, current.ID,
				map[string]any{"outcome": string(target)}); err != nil {
				return err
			}
		}
		return s.audit.Success(ctx, actor, audit.ActionFeedbackSubmitted, audit.ObjectMeetup, meetupID,
			map[string]any{"member_id": memberID, "intent": string(input.Intent), "rating": input.Rating})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, actor, meetupID)
}

// Get returns the read model of one meetup, enforcing participant scoping.
func (s *Service) Get(ctx context.Context, actor identity.Actor, meetupID string) (Detail, error) {
	appointment, err := s.meetups.GetByID(ctx, meetupID)
	if err != nil {
		return Detail{}, err
	}
	current, err := s.matches.GetByID(ctx, appointment.MatchID)
	if err != nil {
		return Detail{}, err
	}
	if actor.Role == identity.RoleMember && !current.HasParticipant(actor.MemberID) {
		return Detail{}, apperr.New(apperr.CodeForbidden, "this meetup does not involve you")
	}
	slot, err := s.slots.GetByID(ctx, appointment.SlotID)
	if err != nil {
		return Detail{}, err
	}
	reports, err := s.meetups.ListFeedback(ctx, meetupID)
	if err != nil {
		return Detail{}, err
	}
	if actor.Role == identity.RoleMember {
		// A member sees their own report only; the counterpart's rating stays
		// confidential until the introduction is closed.
		visible := make([]meetup.Feedback, 0, len(reports))
		for _, report := range reports {
			if report.AuthorMemberID == actor.MemberID || current.State.Terminal() {
				visible = append(visible, report)
			}
		}
		reports = visible
	}
	return Detail{Meetup: appointment, Slot: slot, Match: current, Feedback: reports}, nil
}

// enqueue appends one outbox row inside the current transaction.
func (s *Service) enqueue(
	ctx context.Context, matchID, recipientID string, kind notify.Kind, payload string, now time.Time,
) error {
	id, err := security.NewID("ntf")
	if err != nil {
		return err
	}
	return s.notifications.Enqueue(ctx, notify.Job{
		ID:            id,
		MatchID:       matchID,
		RecipientID:   recipientID,
		Kind:          kind,
		Payload:       payload,
		State:         notify.StatePending,
		MaxAttempts:   notify.DefaultMaxAttempts,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
}

// IsMissing reports whether err means the addressed meetup does not exist. It
// lets the HTTP layer distinguish an unknown id from an authorization failure.
func IsMissing(err error) bool { return errors.Is(err, apperr.ErrNotFound) }
