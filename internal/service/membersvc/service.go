// Package membersvc owns the member profile lifecycle: partner criteria, the
// transition into the matchable state and the entitlement granted by a plan.
package membersvc

import (
	"context"
	"errors"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/auditlog"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/member"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/security"
)

// Service owns member profiles and their entitlements.
type Service struct {
	tx           repository.TxManager
	members      repository.MemberRepository
	entitlements repository.EntitlementRepository
	audit        *auditlog.Recorder
	clock        clock.Clock
}

// Dependencies bundles the collaborators of the service.
type Dependencies struct {
	Tx           repository.TxManager
	Members      repository.MemberRepository
	Entitlements repository.EntitlementRepository
	Audit        *auditlog.Recorder
	Clock        clock.Clock
}

// New builds the service.
func New(deps Dependencies) *Service {
	return &Service{
		tx:           deps.Tx,
		members:      deps.Members,
		entitlements: deps.Entitlements,
		audit:        deps.Audit,
		clock:        deps.Clock,
	}
}

// Profile is the member view returned by the API.
type Profile struct {
	Member      member.Member
	Preference  *member.Preference
	Entitlement *entitlement.Entitlement
}

// PreferenceInput describes the partner criteria a member registers.
type PreferenceInput struct {
	SeekingGender   member.Gender
	MinAge          int
	MaxAge          int
	Cities          []string
	MaritalStatuses []member.MaritalStatus
	MinEducation    member.Education
}

// SavePreference stores the partner criteria of the acting member. Registering
// criteria is what moves an onboarding profile into the matchable state, so both
// writes happen together.
func (s *Service) SavePreference(ctx context.Context, actor identity.Actor, input PreferenceInput) (member.Preference, error) {
	memberID, err := actor.RequireMember()
	if err != nil {
		return member.Preference{}, err
	}
	profile, err := s.members.GetByID(ctx, memberID)
	if err != nil {
		return member.Preference{}, err
	}
	if profile.Status == member.StatusRetired {
		return member.Preference{}, apperr.New(apperr.CodePreconditionFailed,
			"a retired profile cannot register new partner criteria")
	}
	now := s.clock.Now()
	preference := member.Preference{
		MemberID:        memberID,
		SeekingGender:   input.SeekingGender,
		MinAge:          input.MinAge,
		MaxAge:          input.MaxAge,
		Cities:          append([]string(nil), input.Cities...),
		MaritalStatuses: append([]member.MaritalStatus(nil), input.MaritalStatuses...),
		MinEducation:    input.MinEducation,
		UpdatedAt:       now,
	}
	preference.Normalize()
	if err := preference.Validate(); err != nil {
		return member.Preference{}, err
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.members.SavePreference(ctx, preference); err != nil {
			return err
		}
		if profile.Status == member.StatusOnboarding {
			return s.members.UpdateStatus(ctx, memberID, member.StatusActive, now)
		}
		return nil
	})
	if err != nil {
		return member.Preference{}, err
	}
	return preference, nil
}

// SetStatus changes the enrollment status of a member. Members may pause and
// resume themselves; only operations may retire a profile.
func (s *Service) SetStatus(ctx context.Context, actor identity.Actor, memberID string, status member.Status) error {
	if err := status.Validate(); err != nil {
		return err
	}
	if actor.Role == identity.RoleMember {
		own, err := actor.RequireMember()
		if err != nil {
			return err
		}
		if own != memberID {
			return apperr.New(apperr.CodeForbidden, "a member may only change their own profile")
		}
		if status == member.StatusRetired {
			return apperr.New(apperr.CodeForbidden, "retiring a profile is an operations action")
		}
	} else if err := actor.RequireRole(identity.RoleAdmin, identity.RoleMatchmaker); err != nil {
		return err
	}
	profile, err := s.members.GetByID(ctx, memberID)
	if err != nil {
		return err
	}
	if profile.Status == status {
		return nil
	}
	if profile.Status == member.StatusRetired {
		return apperr.New(apperr.CodePreconditionFailed, "a retired profile cannot be reactivated")
	}
	if status == member.StatusActive {
		if _, err := s.members.GetPreference(ctx, memberID); err != nil {
			return err
		}
	}
	return s.members.UpdateStatus(ctx, memberID, status, s.clock.Now())
}

// GetProfile returns the profile, criteria and entitlement of a member. Members
// may only read their own profile.
func (s *Service) GetProfile(ctx context.Context, actor identity.Actor, memberID string) (Profile, error) {
	if actor.Role == identity.RoleMember && actor.MemberID != memberID {
		return Profile{}, apperr.New(apperr.CodeForbidden, "a member may only read their own profile")
	}
	profile, err := s.members.GetByID(ctx, memberID)
	if err != nil {
		return Profile{}, err
	}
	view := Profile{Member: profile}
	preference, err := s.members.GetPreference(ctx, memberID)
	switch {
	case err == nil:
		view.Preference = &preference
	case apperr.CodeOf(err) == apperr.CodePreconditionFailed:
		// Criteria are optional while onboarding.
	default:
		return Profile{}, err
	}
	granted, err := s.entitlements.GetActiveByMember(ctx, memberID)
	switch {
	case err == nil:
		view.Entitlement = &granted
	case apperr.CodeOf(err) == apperr.CodeQuotaExhausted, errors.Is(err, apperr.ErrNotFound):
		// A member without an active plan is a valid state.
	default:
		return Profile{}, err
	}
	return view, nil
}

// GrantEntitlement activates a service plan for a member. It is an operations
// action: the resulting allowance is what every later introduction spends.
func (s *Service) GrantEntitlement(
	ctx context.Context, actor identity.Actor, memberID, planCode string,
) (entitlement.Entitlement, error) {
	if err := actor.RequireRole(identity.RoleAdmin); err != nil {
		return entitlement.Entitlement{}, err
	}
	profile, err := s.members.GetByID(ctx, memberID)
	if err != nil {
		return entitlement.Entitlement{}, err
	}
	if profile.Status == member.StatusRetired {
		return entitlement.Entitlement{}, apperr.New(apperr.CodePreconditionFailed,
			"a retired profile cannot receive a new service plan")
	}
	plan, err := s.entitlements.GetPlan(ctx, planCode)
	if err != nil {
		return entitlement.Entitlement{}, err
	}
	if !plan.Active {
		return entitlement.Entitlement{}, apperr.Newf(apperr.CodePreconditionFailed,
			"service plan %s is no longer offered", planCode)
	}
	id, err := security.NewID("ent")
	if err != nil {
		return entitlement.Entitlement{}, err
	}
	now := s.clock.Now()
	granted := entitlement.Entitlement{
		ID:         id,
		MemberID:   memberID,
		PlanCode:   plan.Code,
		Total:      plan.IntroQuota,
		State:      entitlement.StateActive,
		ValidFrom:  now,
		ValidUntil: now.Add(time.Duration(plan.ValidDays) * 24 * time.Hour),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := granted.Validate(); err != nil {
		return entitlement.Entitlement{}, err
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.entitlements.Create(ctx, granted); err != nil {
			return err
		}
		return s.audit.Success(ctx, actor, audit.ActionEntitlementGranted,
			audit.ObjectEntitlement, granted.ID, map[string]any{
				"member_id": memberID,
				"plan":      plan.Code,
				"quota":     plan.IntroQuota,
			})
	})
	if err != nil {
		return entitlement.Entitlement{}, err
	}
	return granted, nil
}

// LedgerForMember returns every allowance movement of the member's active plan.
func (s *Service) LedgerForMember(
	ctx context.Context, actor identity.Actor, memberID string,
) ([]entitlement.LedgerEntry, error) {
	if actor.Role == identity.RoleMember && actor.MemberID != memberID {
		return nil, apperr.New(apperr.CodeForbidden, "a member may only read their own allowance history")
	}
	granted, err := s.entitlements.GetActiveByMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	return s.entitlements.ListLedgerByEntitlement(ctx, granted.ID)
}
