// Package authsvc implements registration, login, session validation and logout.
// It is the only package that compares credentials.
package authsvc

import (
	"context"
	"errors"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/auditlog"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/member"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/security"
)

// Service owns the account and session lifecycle.
type Service struct {
	tx         repository.TxManager
	users      repository.UserRepository
	sessions   repository.SessionRepository
	members    repository.MemberRepository
	audit      *auditlog.Recorder
	clock      clock.Clock
	sessionTTL time.Duration
}

// Dependencies bundles the collaborators of the service.
type Dependencies struct {
	Tx         repository.TxManager
	Users      repository.UserRepository
	Sessions   repository.SessionRepository
	Members    repository.MemberRepository
	Audit      *auditlog.Recorder
	Clock      clock.Clock
	SessionTTL time.Duration
}

// New builds the service.
func New(deps Dependencies) *Service {
	ttl := deps.SessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Service{
		tx:         deps.Tx,
		users:      deps.Users,
		sessions:   deps.Sessions,
		members:    deps.Members,
		audit:      deps.Audit,
		clock:      deps.Clock,
		sessionTTL: ttl,
	}
}

// RegisterMemberInput describes a self-service member enrollment.
type RegisterMemberInput struct {
	Email         string
	Password      string
	DisplayName   string
	Gender        member.Gender
	BirthDate     time.Time
	City          string
	MaritalStatus member.MaritalStatus
	Education     member.Education
}

// RegisterStaffInput describes the creation of a matchmaker or admin account.
type RegisterStaffInput struct {
	Email    string
	Password string
	Role     identity.Role
}

// RegisterMember creates the account and the member profile in one transaction.
// A profile without its account, or the reverse, would leave the platform with an
// unusable half-registered member, so both writes share the unit of work.
func (s *Service) RegisterMember(ctx context.Context, input RegisterMemberInput) (identity.User, member.Member, error) {
	if err := identity.ValidateEmail(input.Email); err != nil {
		return identity.User{}, member.Member{}, err
	}
	if err := identity.ValidatePassword(input.Password); err != nil {
		return identity.User{}, member.Member{}, err
	}
	digest, err := security.HashPassword(input.Password)
	if err != nil {
		return identity.User{}, member.Member{}, apperr.Wrap(apperr.CodeInternal, "hash password", err)
	}
	now := s.clock.Now()
	userID, err := security.NewID("usr")
	if err != nil {
		return identity.User{}, member.Member{}, err
	}
	memberID, err := security.NewID("mbr")
	if err != nil {
		return identity.User{}, member.Member{}, err
	}
	account := identity.User{
		ID:           userID,
		Email:        identity.NormalizeEmail(input.Email),
		PasswordHash: digest,
		Role:         identity.RoleMember,
		Status:       identity.StatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	profile := member.Member{
		ID:            memberID,
		UserID:        userID,
		DisplayName:   input.DisplayName,
		Gender:        input.Gender,
		BirthDate:     input.BirthDate.UTC(),
		City:          input.City,
		MaritalStatus: input.MaritalStatus,
		Education:     input.Education,
		Status:        member.StatusOnboarding,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := profile.Validate(); err != nil {
		return identity.User{}, member.Member{}, err
	}
	if profile.AgeAt(now) < 18 {
		return identity.User{}, member.Member{}, apperr.New(apperr.CodeInvalidArgument,
			"members must be at least 18 years old")
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.users.Create(ctx, account); err != nil {
			return err
		}
		return s.members.Create(ctx, profile)
	})
	if err != nil {
		return identity.User{}, member.Member{}, err
	}
	return account, profile, nil
}

// RegisterStaff creates a matchmaker or admin account.
func (s *Service) RegisterStaff(ctx context.Context, input RegisterStaffInput) (identity.User, error) {
	if err := input.Role.Validate(); err != nil {
		return identity.User{}, err
	}
	if input.Role == identity.RoleMember {
		return identity.User{}, apperr.New(apperr.CodeInvalidArgument,
			"member accounts must be created through member registration")
	}
	if err := identity.ValidateEmail(input.Email); err != nil {
		return identity.User{}, err
	}
	if err := identity.ValidatePassword(input.Password); err != nil {
		return identity.User{}, err
	}
	digest, err := security.HashPassword(input.Password)
	if err != nil {
		return identity.User{}, apperr.Wrap(apperr.CodeInternal, "hash password", err)
	}
	id, err := security.NewID("usr")
	if err != nil {
		return identity.User{}, err
	}
	now := s.clock.Now()
	account := identity.User{
		ID:           id,
		Email:        identity.NormalizeEmail(input.Email),
		PasswordHash: digest,
		Role:         input.Role,
		Status:       identity.StatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.users.Create(ctx, account); err != nil {
		return identity.User{}, err
	}
	return account, nil
}

// Credential is the result of a successful login. The plaintext token is
// returned exactly once and never stored.
type Credential struct {
	Token     string
	ExpiresAt time.Time
	Actor     identity.Actor
}

// Login verifies the credentials and opens a revocable session.
//
// A wrong password and an unknown email produce the identical error so that the
// endpoint cannot be used to enumerate registered addresses.
func (s *Service) Login(ctx context.Context, email, password string) (Credential, error) {
	invalid := apperr.New(apperr.CodeUnauthenticated, "email or password is incorrect")
	account, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return Credential{}, invalid
		}
		return Credential{}, err
	}
	matches, err := security.VerifyPassword(account.PasswordHash, password)
	if err != nil {
		return Credential{}, apperr.Wrap(apperr.CodeInternal, "verify password", err)
	}
	if !matches {
		return Credential{}, invalid
	}
	if err := account.CanAuthenticate(); err != nil {
		return Credential{}, err
	}

	plaintext, digest, err := security.NewSessionToken()
	if err != nil {
		return Credential{}, apperr.Wrap(apperr.CodeInternal, "issue session token", err)
	}
	sessionID, err := security.NewID("ses")
	if err != nil {
		return Credential{}, err
	}
	now := s.clock.Now()
	session := identity.Session{
		ID:         sessionID,
		UserID:     account.ID,
		TokenHash:  digest,
		IssuedAt:   now,
		ExpiresAt:  now.Add(s.sessionTTL),
		LastSeenAt: now,
	}
	actor, err := s.resolveActor(ctx, account, session.ID)
	if err != nil {
		return Credential{}, err
	}

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.sessions.Create(ctx, session); err != nil {
			return err
		}
		return s.audit.Success(ctx, actor, audit.ActionLogin, audit.ObjectSession, session.ID,
			map[string]any{"role": string(account.Role)})
	})
	if err != nil {
		return Credential{}, err
	}
	logging.FromContext(ctx).Info("session opened",
		"user_id", account.ID, "role", string(account.Role),
		"token_fingerprint", security.Fingerprint(plaintext))
	return Credential{Token: plaintext, ExpiresAt: session.ExpiresAt, Actor: actor}, nil
}

// Authenticate resolves a bearer token into an actor. Revoked and expired
// sessions are rejected here, which is what makes a logout take effect
// immediately for every later request.
func (s *Service) Authenticate(ctx context.Context, plaintextToken string) (identity.Actor, error) {
	if plaintextToken == "" {
		return identity.Actor{}, apperr.New(apperr.CodeUnauthenticated, "a bearer token is required")
	}
	session, err := s.sessions.GetByTokenHash(ctx, security.HashToken(plaintextToken))
	if err != nil {
		return identity.Actor{}, err
	}
	now := s.clock.Now()
	if err := session.Validate(now); err != nil {
		return identity.Actor{}, err
	}
	account, err := s.users.GetByID(ctx, session.UserID)
	if err != nil {
		return identity.Actor{}, err
	}
	if err := account.CanAuthenticate(); err != nil {
		return identity.Actor{}, err
	}
	if err := s.sessions.TouchLastSeen(ctx, session.ID, now); err != nil {
		return identity.Actor{}, err
	}
	return s.resolveActor(ctx, account, session.ID)
}

// Logout revokes the session of the current actor.
func (s *Service) Logout(ctx context.Context, actor identity.Actor) error {
	if actor.SessionID == "" {
		return apperr.New(apperr.CodeUnauthenticated, "no active session to revoke")
	}
	now := s.clock.Now()
	return s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.sessions.Revoke(ctx, actor.SessionID, now); err != nil {
			return err
		}
		return s.audit.Success(ctx, actor, audit.ActionLogout, audit.ObjectSession, actor.SessionID, nil)
	})
}

// resolveActor attaches the member profile to an authenticated account. Staff
// accounts have no profile, which is not an error.
func (s *Service) resolveActor(ctx context.Context, account identity.User, sessionID string) (identity.Actor, error) {
	actor := identity.Actor{UserID: account.ID, SessionID: sessionID, Role: account.Role}
	if account.Role != identity.RoleMember {
		return actor, nil
	}
	profile, err := s.members.GetByUserID(ctx, account.ID)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return actor, nil
		}
		return identity.Actor{}, err
	}
	actor.MemberID = profile.ID
	return actor, nil
}

// PurgeExpiredSessions deletes sessions that expired before the cutoff. It is
// called by the expiry sweeper.
func (s *Service) PurgeExpiredSessions(ctx context.Context, cutoff time.Time) (int, error) {
	return s.sessions.DeleteExpiredBefore(ctx, cutoff)
}
