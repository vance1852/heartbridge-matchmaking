package app

import (
	"context"
	"errors"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/authsvc"
)

// defaultPlans are the service plans a fresh installation starts with. They are
// reference data rather than secrets, so seeding them is safe and repeatable.
func defaultPlans(now func() (int, int, int, int)) []entitlement.Plan {
	quotaStarter, daysStarter, activeStarter, consentStarter := now()
	return []entitlement.Plan{
		{
			Code:           "starter",
			Name:           "Starter introductions",
			IntroQuota:     quotaStarter,
			ValidDays:      daysStarter,
			MaxActiveMatch: activeStarter,
			ConsentHours:   consentStarter,
			Active:         true,
		},
		{
			Code:           "premium",
			Name:           "Premium introductions",
			IntroQuota:     12,
			ValidDays:      365,
			MaxActiveMatch: 3,
			ConsentHours:   72,
			Active:         true,
		},
	}
}

// starterPlanShape returns the parameters of the starter plan.
func starterPlanShape() (int, int, int, int) { return 4, 180, 2, 48 }

// bootstrap seeds the reference data and, when configured, the first operations
// account. It is idempotent: restarting the service does not duplicate anything.
func (a *Application) bootstrap(ctx context.Context) error {
	now := a.Clock.Now()
	for _, plan := range defaultPlans(starterPlanShape) {
		plan.CreatedAt = now
		plan.UpdatedAt = now
		existing, err := a.Repositories.Entitlements.GetPlan(ctx, plan.Code)
		switch {
		case err == nil:
			// Operations may have tuned the plan; seeding must not overwrite it.
			a.Logger.Debug("service plan already present", "code", existing.Code)
			continue
		case errors.Is(err, apperr.ErrNotFound):
			if err := a.Repositories.Entitlements.UpsertPlan(ctx, plan); err != nil {
				return err
			}
			a.Logger.Info("service plan seeded", "code", plan.Code, "quota", plan.IntroQuota)
		default:
			return err
		}
	}
	return a.bootstrapAdmin(ctx)
}

// bootstrapAdmin creates the configured operations account when it is missing.
func (a *Application) bootstrapAdmin(ctx context.Context) error {
	if !a.Config.BootstrapEnabled() {
		return nil
	}
	email := identity.NormalizeEmail(a.Config.BootstrapAdminEmail)
	_, err := a.Repositories.Users.GetByEmail(ctx, email)
	switch {
	case err == nil:
		a.Logger.Debug("bootstrap operations account already present")
		return nil
	case errors.Is(err, apperr.ErrNotFound):
	default:
		return err
	}
	account, err := a.Auth.RegisterStaff(ctx, authsvc.RegisterStaffInput{
		Email:    email,
		Password: a.Config.BootstrapAdminPassword,
		Role:     identity.RoleAdmin,
	})
	if err != nil {
		return err
	}
	// The password itself is never logged, only the fact that seeding happened.
	a.Logger.Info("bootstrap operations account created", "user_id", account.ID)
	return nil
}
