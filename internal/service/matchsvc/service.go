// Package matchsvc implements the matchmaking path: proposing an introduction,
// collecting both consents, cancelling and expiring. Every allowance movement is
// written in the same transaction as the state change that caused it.
package matchsvc

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
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/member"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/notify"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/security"
)

// Service owns the match lifecycle.
type Service struct {
	tx            repository.TxManager
	matches       repository.MatchRepository
	members       repository.MemberRepository
	entitlements  repository.EntitlementRepository
	meetups       repository.MeetupRepository
	slots         repository.SlotRepository
	notifications repository.NotificationRepository
	audit         *auditlog.Recorder
	clock         clock.Clock
}

// Dependencies bundles the collaborators of the service.
type Dependencies struct {
	Tx            repository.TxManager
	Matches       repository.MatchRepository
	Members       repository.MemberRepository
	Entitlements  repository.EntitlementRepository
	Meetups       repository.MeetupRepository
	Slots         repository.SlotRepository
	Notifications repository.NotificationRepository
	Audit         *auditlog.Recorder
	Clock         clock.Clock
}

// New builds the service.
func New(deps Dependencies) *Service {
	return &Service{
		tx:            deps.Tx,
		matches:       deps.Matches,
		members:       deps.Members,
		entitlements:  deps.Entitlements,
		meetups:       deps.Meetups,
		slots:         deps.Slots,
		notifications: deps.Notifications,
		audit:         deps.Audit,
		clock:         deps.Clock,
	}
}

// DefaultConsentWindow is used when the plans of the two members disagree.
const DefaultConsentWindow = 48 * time.Hour

// Detail is the full read model of one introduction.
type Detail struct {
	Match    matching.Match
	Consents []matching.Consent
	Ledger   []entitlement.LedgerEntry
}

// ProposeInput identifies the pair a matchmaker wants to introduce.
type ProposeInput struct {
	FirstMemberID  string
	SecondMemberID string
}

// Propose creates one introduction.
//
// The whole operation is a single unit of work: the match row, both consent rows,
// one reserved introduction on each member's allowance, the two ledger movements,
// the audit row and the outbox notifications either all land or none do. A
// partially committed proposal would either strand allowance or announce an
// introduction that does not exist.
func (s *Service) Propose(ctx context.Context, actor identity.Actor, input ProposeInput) (Detail, error) {
	if err := actor.RequireRole(identity.RoleMatchmaker, identity.RoleAdmin); err != nil {
		return Detail{}, err
	}
	firstID, secondID := matching.CanonicalPair(input.FirstMemberID, input.SecondMemberID)
	if firstID == "" || secondID == "" {
		return Detail{}, apperr.New(apperr.CodeInvalidArgument, "two member identifiers are required")
	}
	if firstID == secondID {
		return Detail{}, apperr.New(apperr.CodeInvalidArgument, "a member cannot be introduced to themselves")
	}

	prepared, err := s.prepare(ctx, actor, firstID, secondID)
	if err != nil {
		return Detail{}, err
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		return s.persistProposal(ctx, actor, prepared)
	})
	if err != nil {
		s.audit.Rejected(ctx, actor, audit.ActionMatchProposed, audit.ObjectMatch, prepared.match.ID, err)
		return Detail{}, err
	}
	logging.FromContext(ctx).Info("introduction proposed",
		"match_id", prepared.match.ID, "matchmaker_id", actor.UserID)
	return s.Get(ctx, actor, prepared.match.ID)
}

// proposal holds everything validated before the transaction opens.
type proposal struct {
	match        matching.Match
	consents     []matching.Consent
	entitlements [2]entitlement.Entitlement
	profiles     [2]member.Member
}

// prepare performs every read-only eligibility check of a proposal.
func (s *Service) prepare(ctx context.Context, actor identity.Actor, firstID, secondID string) (proposal, error) {
	now := s.clock.Now()
	firstProfile, err := s.members.GetByID(ctx, firstID)
	if err != nil {
		return proposal{}, err
	}
	secondProfile, err := s.members.GetByID(ctx, secondID)
	if err != nil {
		return proposal{}, err
	}
	firstPreference, err := s.members.GetPreference(ctx, firstID)
	if err != nil {
		return proposal{}, err
	}
	secondPreference, err := s.members.GetPreference(ctx, secondID)
	if err != nil {
		return proposal{}, err
	}
	if err := member.MutuallyEligible(firstProfile, firstPreference, secondProfile, secondPreference, now); err != nil {
		return proposal{}, err
	}

	exists, err := s.matches.ActivePairExists(ctx, firstID, secondID)
	if err != nil {
		return proposal{}, err
	}
	if exists {
		return proposal{}, apperr.New(apperr.CodeConflict,
			"these two members already have a live introduction")
	}

	firstEntitlement, firstWindow, err := s.checkAllowance(ctx, firstProfile, now)
	if err != nil {
		return proposal{}, err
	}
	secondEntitlement, secondWindow, err := s.checkAllowance(ctx, secondProfile, now)
	if err != nil {
		return proposal{}, err
	}
	window := firstWindow
	if secondWindow < window {
		window = secondWindow
	}

	matchID, err := security.NewID("mch")
	if err != nil {
		return proposal{}, err
	}
	built := matching.Match{
		ID:              matchID,
		MatchmakerID:    actor.UserID,
		MemberAID:       firstID,
		MemberBID:       secondID,
		State:           matching.StatePendingConsent,
		Version:         1,
		ConsentDeadline: now.Add(window),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := built.Validate(); err != nil {
		return proposal{}, err
	}
	consents := []matching.Consent{
		{MatchID: matchID, MemberID: firstID, Decision: matching.DecisionPending, UpdatedAt: now},
		{MatchID: matchID, MemberID: secondID, Decision: matching.DecisionPending, UpdatedAt: now},
	}
	return proposal{
		match:        built,
		consents:     consents,
		entitlements: [2]entitlement.Entitlement{firstEntitlement, secondEntitlement},
		profiles:     [2]member.Member{firstProfile, secondProfile},
	}, nil
}

// checkAllowance verifies that a member can still spend an introduction and
// returns the allowance plus the consent window configured by their plan.
func (s *Service) checkAllowance(
	ctx context.Context, profile member.Member, now time.Time,
) (entitlement.Entitlement, time.Duration, error) {
	granted, err := s.entitlements.GetActiveByMember(ctx, profile.ID)
	if err != nil {
		return entitlement.Entitlement{}, 0, err
	}
	if err := granted.CanReserve(now); err != nil {
		return entitlement.Entitlement{}, 0, err
	}
	plan, err := s.entitlements.GetPlan(ctx, granted.PlanCode)
	if err != nil {
		return entitlement.Entitlement{}, 0, err
	}
	active, err := s.matches.CountActiveByMember(ctx, profile.ID)
	if err != nil {
		return entitlement.Entitlement{}, 0, err
	}
	if active >= plan.MaxActiveMatch {
		return entitlement.Entitlement{}, 0, apperr.Newf(apperr.CodePreconditionFailed,
			"member %s already takes part in %d live introductions, which is the limit of plan %s",
			profile.ID, active, plan.Code)
	}
	window := time.Duration(plan.ConsentHours) * time.Hour
	if window <= 0 {
		window = DefaultConsentWindow
	}
	return granted, window, nil
}

// persistProposal writes the whole proposal inside the caller's transaction.
func (s *Service) persistProposal(ctx context.Context, actor identity.Actor, prepared proposal) error {
	if err := s.matches.Create(ctx, prepared.match, prepared.consents); err != nil {
		return err
	}
	now := prepared.match.CreatedAt
	for _, granted := range prepared.entitlements {
		if err := s.entitlements.Reserve(ctx, granted.ID, granted.Version, now); err != nil {
			return err
		}
		ledgerID, err := security.NewID("led")
		if err != nil {
			return err
		}
		if err := s.entitlements.AppendLedger(ctx, entitlement.LedgerEntry{
			ID:            ledgerID,
			EntitlementID: granted.ID,
			MatchID:       prepared.match.ID,
			Reason:        entitlement.ReasonReserve,
			DeltaReserved: 1,
			CreatedAt:     now,
		}); err != nil {
			return err
		}
	}
	for _, profile := range prepared.profiles {
		if err := s.enqueue(ctx, prepared.match.ID, profile.ID, notify.KindMatchProposed,
			fmt.Sprintf("introduction %s awaits your answer until %s",
				prepared.match.ID, prepared.match.ConsentDeadline.UTC().Format(time.RFC3339)), now); err != nil {
			return err
		}
	}
	return s.audit.Success(ctx, actor, audit.ActionMatchProposed, audit.ObjectMatch, prepared.match.ID,
		map[string]any{
			"member_a":         prepared.match.MemberAID,
			"member_b":         prepared.match.MemberBID,
			"consent_deadline": prepared.match.ConsentDeadline,
		})
}

// enqueue appends one outbox row inside the current transaction.
func (s *Service) enqueue(ctx context.Context, matchID, recipientID string, kind notify.Kind, payload string, now time.Time) error {
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

// Decide records the answer of one member and advances the match when the
// collection is complete. A decline releases both reservations immediately, so
// nobody keeps paying for an introduction that will not happen.
func (s *Service) Decide(
	ctx context.Context, actor identity.Actor, matchID string, decision matching.Decision,
) (Detail, error) {
	memberID, err := actor.RequireMember()
	if err != nil {
		return Detail{}, err
	}
	if err := decision.Answerable(); err != nil {
		return Detail{}, err
	}
	current, err := s.matches.GetByID(ctx, matchID)
	if err != nil {
		return Detail{}, err
	}
	if !current.HasParticipant(memberID) {
		return Detail{}, apperr.New(apperr.CodeForbidden, "this introduction does not involve you")
	}
	now := s.clock.Now()
	if current.State != matching.StatePendingConsent {
		return Detail{}, apperr.Newf(apperr.CodeIllegalTransition,
			"introduction %s is %s and no longer accepts answers", matchID, string(current.State))
	}
	if current.ConsentExpired(now) {
		return Detail{}, apperr.New(apperr.CodePreconditionFailed,
			"the answer window for this introduction has already closed")
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.matches.RecordDecision(ctx, matchID, memberID, decision, now); err != nil {
			return err
		}
		consents, err := s.matches.ListConsents(ctx, matchID)
		if err != nil {
			return err
		}
		outcome := matching.SummarizeConsents(consents)
		target := outcome.NextState(current.State)
		if target != current.State {
			note := "both members accepted"
			if target == matching.StateClosedFailed {
				note = "an invited member declined"
			}
			if err := s.matches.UpdateState(ctx, matchID, current.Version, target, note, now); err != nil {
				return err
			}
			if target == matching.StateClosedFailed {
				if err := s.releaseReservations(ctx, current, now); err != nil {
					return err
				}
			}
			for _, participant := range current.Participants() {
				kind := notify.KindMeetupBooked
				payload := "both sides accepted, a meetup can now be arranged"
				if target == matching.StateClosedFailed {
					kind = notify.KindMatchClosed
					payload = "the introduction was closed because one side declined"
				}
				if err := s.enqueue(ctx, matchID, participant, kind, payload, now); err != nil {
					return err
				}
			}
		}
		return s.audit.Success(ctx, actor, audit.ActionMatchConsent, audit.ObjectMatch, matchID,
			map[string]any{"member_id": memberID, "decision": string(decision), "match_state": string(target)})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, actor, matchID)
}

// Cancel withdraws a live introduction and returns the reserved allowance. A
// booked venue seat is released as part of the same transaction.
func (s *Service) Cancel(ctx context.Context, actor identity.Actor, matchID, reason string) (Detail, error) {
	current, err := s.matches.GetByID(ctx, matchID)
	if err != nil {
		return Detail{}, err
	}
	if err := s.authorizeCancel(actor, current); err != nil {
		return Detail{}, err
	}
	if err := current.CanTransition(matching.StateCancelled); err != nil {
		return Detail{}, err
	}
	now := s.clock.Now()
	note := reason
	if note == "" {
		note = "withdrawn by " + string(actor.Role)
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.matches.UpdateState(ctx, matchID, current.Version, matching.StateCancelled, note, now); err != nil {
			return err
		}
		if current.State.SeatBoundQuota() {
			// Returning the venue seat also settles the introduction this match
			// was holding.
			if err := s.releaseSlot(ctx, matchID, now); err != nil {
				return err
			}
		} else if err := s.releaseReservations(ctx, current, now); err != nil {
			return err
		}
		for _, participant := range current.Participants() {
			if err := s.enqueue(ctx, matchID, participant, notify.KindMatchClosed,
				"the introduction was withdrawn: "+note, now); err != nil {
				return err
			}
		}
		return s.audit.Success(ctx, actor, audit.ActionMatchCancelled, audit.ObjectMatch, matchID,
			map[string]any{"previous_state": string(current.State), "reason": note})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, actor, matchID)
}

// authorizeCancel allows staff and the two participants to withdraw.
func (s *Service) authorizeCancel(actor identity.Actor, current matching.Match) error {
	if actor.Role == identity.RoleMember {
		if !current.HasParticipant(actor.MemberID) {
			return apperr.New(apperr.CodeForbidden, "this introduction does not involve you")
		}
		return nil
	}
	return actor.RequireRole(identity.RoleMatchmaker, identity.RoleAdmin)
}

// releaseReservations returns the held introduction of both members. The ledger
// carries a uniqueness constraint over (entitlement, match, reason), so a second
// release attempt for the same match is rejected instead of double-refunding.
func (s *Service) releaseReservations(ctx context.Context, current matching.Match, now time.Time) error {
	if !current.State.HoldsQuota() {
		return nil
	}
	for _, memberID := range current.Participants() {
		granted, err := s.entitlements.GetActiveByMember(ctx, memberID)
		if err != nil {
			// An expired or replaced plan keeps no reservation to return; the
			// ledger of the original allowance already records the movement.
			if apperr.CodeOf(err) == apperr.CodeQuotaExhausted || errors.Is(err, apperr.ErrNotFound) {
				continue
			}
			return err
		}
		if err := s.entitlements.Release(ctx, granted.ID, granted.Version, now); err != nil {
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
			Reason:        entitlement.ReasonRelease,
			DeltaReserved: -1,
			CreatedAt:     now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// releaseSlot frees the venue seat held by the live meetup of a match.
func (s *Service) releaseSlot(ctx context.Context, matchID string, now time.Time) error {
	appointment, err := s.meetups.GetActiveByMatch(ctx, matchID)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return nil
		}
		return err
	}
	if !appointment.State.OccupiesSlot() {
		return nil
	}
	if err := s.meetups.UpdateState(ctx, appointment.ID, appointment.Version, meetup.StateCancelled, now); err != nil {
		return err
	}
	return s.slots.ReleaseBooking(ctx, appointment.SlotID, now)
}

// ExpireDue moves introductions whose answer window elapsed to the expired state
// and returns the reserved allowance. It is driven by the expiry sweeper and is
// safe to run repeatedly.
func (s *Service) ExpireDue(ctx context.Context, actor identity.Actor, limit int) (int, error) {
	now := s.clock.Now()
	due, err := s.matches.ListExpiredPendingConsent(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, current := range due {
		if err := ctx.Err(); err != nil {
			return expired, err
		}
		candidate := current
		err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
			if err := s.matches.UpdateState(ctx, candidate.ID, candidate.Version,
				matching.StateExpired, "answer window elapsed", now); err != nil {
				return err
			}
			if err := s.releaseReservations(ctx, candidate, now); err != nil {
				return err
			}
			for _, participant := range candidate.Participants() {
				if err := s.enqueue(ctx, candidate.ID, participant, notify.KindMatchClosed,
					"the introduction expired because it was not answered in time", now); err != nil {
					return err
				}
			}
			return s.audit.Success(ctx, actor, audit.ActionMatchExpired, audit.ObjectMatch, candidate.ID,
				map[string]any{"consent_deadline": candidate.ConsentDeadline})
		})
		if err != nil {
			// A concurrent answer or withdrawal wins the race. That is a normal
			// outcome for a sweeper, so the remaining candidates are still
			// processed.
			if apperr.CodeOf(err) == apperr.CodeVersionConflict {
				logging.FromContext(ctx).Debug("skipped concurrently modified introduction",
					"match_id", candidate.ID)
				continue
			}
			return expired, err
		}
		expired++
	}
	return expired, nil
}

// Get returns the full read model of one introduction.
func (s *Service) Get(ctx context.Context, actor identity.Actor, matchID string) (Detail, error) {
	current, err := s.matches.GetByID(ctx, matchID)
	if err != nil {
		return Detail{}, err
	}
	if actor.Role == identity.RoleMember && !current.HasParticipant(actor.MemberID) {
		return Detail{}, apperr.New(apperr.CodeForbidden, "this introduction does not involve you")
	}
	consents, err := s.matches.ListConsents(ctx, matchID)
	if err != nil {
		return Detail{}, err
	}
	ledger, err := s.entitlements.ListLedgerByMatch(ctx, matchID)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Match: current, Consents: consents, Ledger: ledger}, nil
}

// List returns a page of introductions. A member always sees their own
// introductions only, regardless of the requested filter.
func (s *Service) List(
	ctx context.Context, actor identity.Actor, filter repository.MatchFilter,
) (repository.MatchPage, error) {
	scoped := filter
	if actor.Role == identity.RoleMember {
		memberID, err := actor.RequireMember()
		if err != nil {
			return repository.MatchPage{}, err
		}
		scoped.MemberID = memberID
	}
	if err := scoped.Validate(); err != nil {
		return repository.MatchPage{}, err
	}
	return s.matches.List(ctx, scoped)
}
