// Package sqliterepo implements the repository contracts on top of a real SQLite
// database. Every method runs actual SQL and joins the caller's transaction when
// one is present in the context.
package sqliterepo

import (
	"strings"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/storage/sqlitedb"
)

// Set bundles every repository implementation so that wiring code passes one
// value around instead of ten constructor arguments.
type Set struct {
	Users         repository.UserRepository
	Sessions      repository.SessionRepository
	Members       repository.MemberRepository
	Entitlements  repository.EntitlementRepository
	Matches       repository.MatchRepository
	Slots         repository.SlotRepository
	Meetups       repository.MeetupRepository
	Notifications repository.NotificationRepository
	Audit         repository.AuditRepository
	Idempotency   repository.IdempotencyRepository
}

// New builds every repository against one database handle.
func New(db *sqlitedb.DB) Set {
	return Set{
		Users:         NewUserRepository(db),
		Sessions:      NewSessionRepository(db),
		Members:       NewMemberRepository(db),
		Entitlements:  NewEntitlementRepository(db),
		Matches:       NewMatchRepository(db),
		Slots:         NewSlotRepository(db),
		Meetups:       NewMeetupRepository(db),
		Notifications: NewNotificationRepository(db),
		Audit:         NewAuditRepository(db),
		Idempotency:   NewIdempotencyRepository(db),
	}
}

// notFound builds the canonical missing-resource error for a repository lookup.
func notFound(entity, id string) error {
	return apperr.Newf(apperr.CodeNotFound, "%s %q was not found", entity, id)
}

// encodeList joins a multi-valued attribute into the stored representation.
func encodeList(values []string) string {
	return strings.Join(values, ",")
}

// decodeList splits a stored multi-valued attribute, dropping empty segments so
// that an empty column decodes to an empty slice rather than one blank entry.
func decodeList(encoded string) []string {
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	parts := strings.Split(encoded, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		values = append(values, trimmed)
	}
	return values
}
