package identity

import (
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// Session is a server-side, individually revocable credential. Only the digest
// of the bearer token is stored, so a leaked database row cannot be replayed.
type Session struct {
	ID         string
	UserID     string
	TokenHash  string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastSeenAt time.Time
}

// Revoked reports whether the session was explicitly revoked by a logout.
func (s Session) Revoked() bool { return s.RevokedAt != nil }

// Expired reports whether the session validity window has elapsed.
func (s Session) Expired(now time.Time) bool { return !now.Before(s.ExpiresAt) }

// Validate returns the stable authentication failure for an unusable session so
// that every caller reports the same code for revoked and expired credentials.
func (s Session) Validate(now time.Time) error {
	if s.Revoked() {
		return apperr.New(apperr.CodeUnauthenticated, "session has been revoked")
	}
	if s.Expired(now) {
		return apperr.New(apperr.CodeUnauthenticated, "session has expired")
	}
	return nil
}

// Clone returns an independent copy, including the revocation timestamp, so that
// a caller mutating the result cannot reach into repository-owned memory.
func (s Session) Clone() Session {
	copied := s
	if s.RevokedAt != nil {
		revoked := *s.RevokedAt
		copied.RevokedAt = &revoked
	}
	return copied
}

// Actor is the authenticated principal attached to a request context. It carries
// the member identity as well, because most business rules are expressed in
// terms of members rather than accounts.
type Actor struct {
	UserID    string
	SessionID string
	Role      Role
	MemberID  string
}

// RequireRole verifies that the actor holds one of the accepted roles.
func (a Actor) RequireRole(accepted ...Role) error {
	for _, role := range accepted {
		if a.Role == role {
			return nil
		}
	}
	return apperr.Newf(apperr.CodeForbidden, "role %q may not perform this operation", string(a.Role))
}

// RequireMember verifies the actor is a member and returns its member id.
func (a Actor) RequireMember() (string, error) {
	if a.Role != RoleMember || a.MemberID == "" {
		return "", apperr.New(apperr.CodeForbidden, "operation requires a member profile")
	}
	return a.MemberID, nil
}
