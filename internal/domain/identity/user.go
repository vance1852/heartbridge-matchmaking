// Package identity models the accounts and server-side sessions of the
// HeartBridge platform. It is transport and storage agnostic.
package identity

import (
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// Role is a business role. Roles are not a permission-management product; they
// only distinguish the three kinds of actor the matchmaking business has.
type Role string

const (
	// RoleMember is a paying single who receives introductions.
	RoleMember Role = "member"
	// RoleMatchmaker is the consultant who proposes matches and arranges meetups.
	RoleMatchmaker Role = "matchmaker"
	// RoleAdmin runs venue slots, service plans and reviews the audit trail.
	RoleAdmin Role = "admin"
)

// Validate rejects unknown roles.
func (r Role) Validate() error {
	switch r {
	case RoleMember, RoleMatchmaker, RoleAdmin:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown role %q", string(r))
	}
}

// String implements fmt.Stringer.
func (r Role) String() string { return string(r) }

// AccountStatus is the lifecycle state of an account.
type AccountStatus string

const (
	// StatusActive accounts may authenticate.
	StatusActive AccountStatus = "active"
	// StatusSuspended accounts keep their data but cannot authenticate.
	StatusSuspended AccountStatus = "suspended"
)

// Validate rejects unknown account statuses.
func (s AccountStatus) Validate() error {
	switch s {
	case StatusActive, StatusSuspended:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown account status %q", string(s))
	}
}

// User is an account. The password digest never leaves the service.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	Role         Role
	Status       AccountStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NormalizeEmail lowercases and trims an email so that uniqueness is stable.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail applies the minimal structural rules the platform relies on.
func ValidateEmail(email string) error {
	normalized := NormalizeEmail(email)
	if normalized == "" {
		return apperr.New(apperr.CodeInvalidArgument, "email is required")
	}
	at := strings.IndexByte(normalized, '@')
	if at <= 0 || at == len(normalized)-1 {
		return apperr.New(apperr.CodeInvalidArgument, "email must contain a local part and a domain")
	}
	if strings.Contains(normalized, " ") {
		return apperr.New(apperr.CodeInvalidArgument, "email must not contain spaces")
	}
	if !strings.Contains(normalized[at+1:], ".") {
		return apperr.New(apperr.CodeInvalidArgument, "email domain must be fully qualified")
	}
	return nil
}

// ValidatePassword enforces the platform password policy.
func ValidatePassword(password string) error {
	if len(password) < 10 {
		return apperr.New(apperr.CodeInvalidArgument, "password must be at least 10 characters long")
	}
	var hasLetter, hasDigit bool
	for _, r := range password {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		}
	}
	if !hasLetter || !hasDigit {
		return apperr.New(apperr.CodeInvalidArgument, "password must mix letters and digits")
	}
	return nil
}

// CanAuthenticate reports whether the account may open a new session.
func (u User) CanAuthenticate() error {
	if u.Status != StatusActive {
		return apperr.New(apperr.CodeForbidden, "account is suspended")
	}
	return nil
}

// Clone returns a copy of the user so that repositories never hand out a
// pointer into their own cache.
func (u User) Clone() User { return u }
