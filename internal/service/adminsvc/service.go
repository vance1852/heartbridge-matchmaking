// Package adminsvc holds the operations actions: publishing bookable venue slots,
// maintaining service plans and reading the audit trail.
package adminsvc

import (
	"context"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/auditlog"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/security"
)

// Service owns the operations surface of the platform.
type Service struct {
	tx           repository.TxManager
	slots        repository.SlotRepository
	entitlements repository.EntitlementRepository
	audit        *auditlog.Recorder
	clock        clock.Clock
}

// Dependencies bundles the collaborators of the service.
type Dependencies struct {
	Tx           repository.TxManager
	Slots        repository.SlotRepository
	Entitlements repository.EntitlementRepository
	Audit        *auditlog.Recorder
	Clock        clock.Clock
}

// New builds the service.
func New(deps Dependencies) *Service {
	return &Service{
		tx:           deps.Tx,
		slots:        deps.Slots,
		entitlements: deps.Entitlements,
		audit:        deps.Audit,
		clock:        deps.Clock,
	}
}

// SlotInput describes a bookable venue slot.
type SlotInput struct {
	VenueCode string
	VenueName string
	City      string
	StartAt   time.Time
	EndAt     time.Time
	Capacity  int
}

// PublishSlot makes a venue window bookable.
func (s *Service) PublishSlot(ctx context.Context, actor identity.Actor, input SlotInput) (meetup.VenueSlot, error) {
	if err := actor.RequireRole(identity.RoleAdmin); err != nil {
		return meetup.VenueSlot{}, err
	}
	now := s.clock.Now()
	if !input.StartAt.After(now) {
		return meetup.VenueSlot{}, apperr.New(apperr.CodeInvalidArgument,
			"a venue slot must start in the future")
	}
	id, err := security.NewID("slt")
	if err != nil {
		return meetup.VenueSlot{}, err
	}
	slot := meetup.VenueSlot{
		ID:        id,
		VenueCode: input.VenueCode,
		VenueName: input.VenueName,
		City:      input.City,
		StartAt:   input.StartAt.UTC(),
		EndAt:     input.EndAt.UTC(),
		Capacity:  input.Capacity,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := slot.Validate(); err != nil {
		return meetup.VenueSlot{}, err
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.slots.Create(ctx, slot); err != nil {
			return err
		}
		return s.audit.Success(ctx, actor, audit.ActionSlotPublished, audit.ObjectVenueSlot, slot.ID,
			map[string]any{
				"venue":    slot.VenueCode,
				"city":     slot.City,
				"start_at": slot.StartAt,
				"capacity": slot.Capacity,
			})
	})
	if err != nil {
		return meetup.VenueSlot{}, err
	}
	return slot, nil
}

// ListSlots returns the bookable windows inside a range. Matchmakers need this
// list to arrange meetups, so it is not restricted to operations.
func (s *Service) ListSlots(
	ctx context.Context, actor identity.Actor, from, to time.Time, city string, page repository.Page,
) ([]meetup.VenueSlot, error) {
	if err := actor.RequireRole(identity.RoleMatchmaker, identity.RoleAdmin); err != nil {
		return nil, err
	}
	if to.Before(from) {
		return nil, apperr.New(apperr.CodeInvalidArgument, "the range end must not precede its start")
	}
	if to.Sub(from) > 90*24*time.Hour {
		return nil, apperr.New(apperr.CodeInvalidArgument, "the range must not exceed 90 days")
	}
	return s.slots.ListBetween(ctx, from.UTC(), to.UTC(), city, page)
}

// PlanInput describes a service plan definition.
type PlanInput struct {
	Code           string
	Name           string
	IntroQuota     int
	ValidDays      int
	MaxActiveMatch int
	ConsentHours   int
	Active         bool
}

// UpsertPlan creates or updates a service plan.
func (s *Service) UpsertPlan(ctx context.Context, actor identity.Actor, input PlanInput) (entitlement.Plan, error) {
	if err := actor.RequireRole(identity.RoleAdmin); err != nil {
		return entitlement.Plan{}, err
	}
	now := s.clock.Now()
	plan := entitlement.Plan{
		Code:           input.Code,
		Name:           input.Name,
		IntroQuota:     input.IntroQuota,
		ValidDays:      input.ValidDays,
		MaxActiveMatch: input.MaxActiveMatch,
		ConsentHours:   input.ConsentHours,
		Active:         input.Active,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := plan.Validate(); err != nil {
		return entitlement.Plan{}, err
	}
	if err := s.entitlements.UpsertPlan(ctx, plan); err != nil {
		return entitlement.Plan{}, err
	}
	return s.entitlements.GetPlan(ctx, plan.Code)
}

// ListPlans returns every service plan.
func (s *Service) ListPlans(ctx context.Context, actor identity.Actor) ([]entitlement.Plan, error) {
	if err := actor.RequireRole(identity.RoleMatchmaker, identity.RoleAdmin); err != nil {
		return nil, err
	}
	return s.entitlements.ListPlans(ctx)
}

// ListAudit returns a page of the audit trail.
func (s *Service) ListAudit(
	ctx context.Context, actor identity.Actor, filter repository.AuditFilter,
) (repository.AuditPage, error) {
	if err := actor.RequireRole(identity.RoleAdmin); err != nil {
		return repository.AuditPage{}, err
	}
	return s.audit.List(ctx, filter)
}
