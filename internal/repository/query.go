// Package repository declares the persistence contracts of the service. It owns
// the query and filter value types but no SQL: concrete implementations live in
// internal/repository/sqliterepo, so the service layer never sees a driver type.
package repository

import (
	"context"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/member"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/notify"
)

// MaxPageSize caps how many rows one list request may return.
const MaxPageSize = 100

// DefaultPageSize is applied when a request omits the page size.
const DefaultPageSize = 20

// Page is an offset based page request.
type Page struct {
	Limit  int
	Offset int
}

// Normalize clamps the page into the supported range.
func (p Page) Normalize() Page {
	normalized := p
	if normalized.Limit <= 0 {
		normalized.Limit = DefaultPageSize
	}
	if normalized.Limit > MaxPageSize {
		normalized.Limit = MaxPageSize
	}
	if normalized.Offset < 0 {
		normalized.Offset = 0
	}
	return normalized
}

// Validate rejects page requests that cannot be served.
func (p Page) Validate() error {
	if p.Limit < 0 {
		return apperr.New(apperr.CodeInvalidArgument, "page size must not be negative")
	}
	if p.Limit > MaxPageSize {
		return apperr.Newf(apperr.CodeInvalidArgument, "page size must not exceed %d", MaxPageSize)
	}
	if p.Offset < 0 {
		return apperr.New(apperr.CodeInvalidArgument, "page offset must not be negative")
	}
	return nil
}

// SortDirection is an explicit ordering direction.
type SortDirection string

const (
	// SortAscending orders from the smallest value upwards.
	SortAscending SortDirection = "asc"
	// SortDescending orders from the largest value downwards.
	SortDescending SortDirection = "desc"
)

// Validate rejects unknown sort directions.
func (d SortDirection) Validate() error {
	switch d {
	case SortAscending, SortDescending:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown sort direction %q", string(d))
	}
}

// MatchSortField enumerates the orderable columns of the match list.
type MatchSortField string

const (
	// MatchSortCreatedAt orders by proposal time.
	MatchSortCreatedAt MatchSortField = "created_at"
	// MatchSortUpdatedAt orders by last change time.
	MatchSortUpdatedAt MatchSortField = "updated_at"
	// MatchSortConsentDeadline orders by the consent deadline.
	MatchSortConsentDeadline MatchSortField = "consent_deadline"
)

// Validate rejects unknown sort fields so that no caller can inject SQL through
// the ordering clause.
func (f MatchSortField) Validate() error {
	switch f {
	case MatchSortCreatedAt, MatchSortUpdatedAt, MatchSortConsentDeadline:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown match sort field %q", string(f))
	}
}

// MatchFilter selects and orders matches. The same filter is used for the page
// query and for the total count so that the two can never diverge.
type MatchFilter struct {
	States       []matching.State
	MemberID     string
	MatchmakerID string
	CreatedFrom  *time.Time
	CreatedTo    *time.Time
	SortField    MatchSortField
	SortDir      SortDirection
	Page         Page
}

// Normalize fills defaults and clamps the page.
func (f MatchFilter) Normalize() MatchFilter {
	normalized := f
	if normalized.SortField == "" {
		normalized.SortField = MatchSortCreatedAt
	}
	if normalized.SortDir == "" {
		normalized.SortDir = SortDescending
	}
	normalized.Page = normalized.Page.Normalize()
	if normalized.States != nil {
		states := make([]matching.State, len(normalized.States))
		copy(states, normalized.States)
		normalized.States = states
	}
	return normalized
}

// Validate rejects filters that cannot be served.
func (f MatchFilter) Validate() error {
	for _, state := range f.States {
		if err := state.Validate(); err != nil {
			return err
		}
	}
	if f.SortField != "" {
		if err := f.SortField.Validate(); err != nil {
			return err
		}
	}
	if f.SortDir != "" {
		if err := f.SortDir.Validate(); err != nil {
			return err
		}
	}
	if f.CreatedFrom != nil && f.CreatedTo != nil && f.CreatedTo.Before(*f.CreatedFrom) {
		return apperr.New(apperr.CodeInvalidArgument, "created_to must not be earlier than created_from")
	}
	return f.Page.Validate()
}

// AuditFilter selects audit rows for the operations console.
type AuditFilter struct {
	ObjectType audit.ObjectType
	ObjectID   string
	ActorID    string
	RequestID  string
	Page       Page
}

// Normalize clamps the page of an audit filter.
func (f AuditFilter) Normalize() AuditFilter {
	normalized := f
	normalized.Page = normalized.Page.Normalize()
	return normalized
}

// MatchPage is a page of matches together with the total under the same filter.
type MatchPage struct {
	Items  []matching.Match
	Total  int
	Limit  int
	Offset int
}

// AuditPage is a page of audit rows together with the total under the same filter.
type AuditPage struct {
	Items  []audit.Event
	Total  int
	Limit  int
	Offset int
}

// IdempotencyRecord is a stored response fingerprint for a replayed request.
type IdempotencyRecord struct {
	ActorID     string
	Method      string
	Path        string
	Key         string
	RequestHash string
	Status      int
	ResponseB64 string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// TxManager runs a function inside a single database transaction. The returned
// context carries the transaction, so repositories participate automatically.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// UserRepository persists accounts.
type UserRepository interface {
	Create(ctx context.Context, user identity.User) error
	GetByID(ctx context.Context, id string) (identity.User, error)
	GetByEmail(ctx context.Context, email string) (identity.User, error)
}

// SessionRepository persists revocable server-side sessions.
type SessionRepository interface {
	Create(ctx context.Context, session identity.Session) error
	GetByTokenHash(ctx context.Context, tokenHash string) (identity.Session, error)
	Revoke(ctx context.Context, sessionID string, at time.Time) error
	TouchLastSeen(ctx context.Context, sessionID string, at time.Time) error
	DeleteExpiredBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// MemberRepository persists member profiles and their partner criteria.
type MemberRepository interface {
	Create(ctx context.Context, profile member.Member) error
	GetByID(ctx context.Context, id string) (member.Member, error)
	GetByUserID(ctx context.Context, userID string) (member.Member, error)
	UpdateStatus(ctx context.Context, id string, status member.Status, at time.Time) error
	SavePreference(ctx context.Context, preference member.Preference) error
	GetPreference(ctx context.Context, memberID string) (member.Preference, error)
}

// EntitlementRepository persists service plans, allowances and the movement
// ledger. Quota movements are conditional updates so that concurrent callers
// cannot both consume the last introduction.
type EntitlementRepository interface {
	UpsertPlan(ctx context.Context, plan entitlement.Plan) error
	GetPlan(ctx context.Context, code string) (entitlement.Plan, error)
	ListPlans(ctx context.Context) ([]entitlement.Plan, error)
	Create(ctx context.Context, granted entitlement.Entitlement) error
	GetByID(ctx context.Context, id string) (entitlement.Entitlement, error)
	GetActiveByMember(ctx context.Context, memberID string) (entitlement.Entitlement, error)
	Reserve(ctx context.Context, id string, version int64, now time.Time) error
	Release(ctx context.Context, id string, version int64, now time.Time) error
	Consume(ctx context.Context, id string, version int64, now time.Time) error
	AppendLedger(ctx context.Context, entry entitlement.LedgerEntry) error
	ListLedgerByMatch(ctx context.Context, matchID string) ([]entitlement.LedgerEntry, error)
	ListLedgerByEntitlement(ctx context.Context, entitlementID string) ([]entitlement.LedgerEntry, error)
}

// MatchRepository persists matches and their consent rows.
type MatchRepository interface {
	Create(ctx context.Context, match matching.Match, consents []matching.Consent) error
	GetByID(ctx context.Context, id string) (matching.Match, error)
	UpdateState(ctx context.Context, id string, expectedVersion int64, target matching.State, note string, at time.Time) error
	CountActiveByMember(ctx context.Context, memberID string) (int, error)
	ActivePairExists(ctx context.Context, leftMemberID, rightMemberID string) (bool, error)
	List(ctx context.Context, filter MatchFilter) (MatchPage, error)
	ListConsents(ctx context.Context, matchID string) ([]matching.Consent, error)
	RecordDecision(ctx context.Context, matchID, memberID string, decision matching.Decision, at time.Time) error
	ListExpiredPendingConsent(ctx context.Context, cutoff time.Time, limit int) ([]matching.Match, error)
}

// SlotRepository persists venue slots and their scarce capacity.
type SlotRepository interface {
	Create(ctx context.Context, slot meetup.VenueSlot) error
	GetByID(ctx context.Context, id string) (meetup.VenueSlot, error)
	ListBetween(ctx context.Context, from, to time.Time, city string, page Page) ([]meetup.VenueSlot, error)
	TryBook(ctx context.Context, id string, at time.Time) error
	ReleaseBooking(ctx context.Context, id string, at time.Time) error
}

// MeetupRepository persists meetups and their feedback rows.
type MeetupRepository interface {
	Create(ctx context.Context, appointment meetup.Meetup) error
	GetByID(ctx context.Context, id string) (meetup.Meetup, error)
	GetActiveByMatch(ctx context.Context, matchID string) (meetup.Meetup, error)
	UpdateState(ctx context.Context, id string, expectedVersion int64, target meetup.State, at time.Time) error
	ListBookingsForMember(ctx context.Context, memberID string, from, to time.Time) ([]meetup.Booking, error)
	AddFeedback(ctx context.Context, feedback meetup.Feedback) error
	ListFeedback(ctx context.Context, meetupID string) ([]meetup.Feedback, error)
}

// NotificationRepository persists the transactional outbox.
type NotificationRepository interface {
	Enqueue(ctx context.Context, job notify.Job) error
	GetByID(ctx context.Context, id string) (notify.Job, error)
	ListDue(ctx context.Context, now time.Time, limit int) ([]notify.Job, error)
	MarkSucceeded(ctx context.Context, id string, attempts int, at time.Time) error
	ScheduleRetry(ctx context.Context, id string, attempts int, nextAttemptAt time.Time, lastError string, at time.Time) error
	MarkPermanentFailure(ctx context.Context, id string, attempts int, lastError string, at time.Time) error
	CountByState(ctx context.Context, state notify.State) (int, error)
}

// AuditRepository persists and queries the operational trail.
type AuditRepository interface {
	Append(ctx context.Context, event audit.Event) error
	List(ctx context.Context, filter AuditFilter) (AuditPage, error)
}

// IdempotencyRepository persists replay records for mutating requests.
type IdempotencyRepository interface {
	Get(ctx context.Context, actorID, method, path, key string) (IdempotencyRecord, error)
	Put(ctx context.Context, record IdempotencyRecord) error
	DeleteExpiredBefore(ctx context.Context, cutoff time.Time) (int, error)
}
