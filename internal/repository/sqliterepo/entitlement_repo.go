package sqliterepo

import (
	"context"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/storage/sqlitedb"
)

// EntitlementRepository persists plans, allowances and their movement ledger.
type EntitlementRepository struct {
	db *sqlitedb.DB
}

// NewEntitlementRepository builds an EntitlementRepository.
func NewEntitlementRepository(db *sqlitedb.DB) *EntitlementRepository {
	return &EntitlementRepository{db: db}
}

const planColumns = `code, name, intro_quota, valid_days, max_active_match, consent_hours, active, created_at, updated_at`

const entitlementColumns = `id, member_id, plan_code, total, used, reserved, state, valid_from, valid_until, version, created_at, updated_at`

// UpsertPlan inserts or updates a service plan definition.
func (r *EntitlementRepository) UpsertPlan(ctx context.Context, plan entitlement.Plan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	active := 0
	if plan.Active {
		active = 1
	}
	const statement = `
INSERT INTO service_plans (code, name, intro_quota, valid_days, max_active_match, consent_hours, active, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (code) DO UPDATE SET
    name = excluded.name,
    intro_quota = excluded.intro_quota,
    valid_days = excluded.valid_days,
    max_active_match = excluded.max_active_match,
    consent_hours = excluded.consent_hours,
    active = excluded.active,
    updated_at = excluded.updated_at`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		plan.Code, plan.Name, plan.IntroQuota, plan.ValidDays, plan.MaxActiveMatch,
		plan.ConsentHours, active,
		sqlitedb.FormatTime(plan.CreatedAt), sqlitedb.FormatTime(plan.UpdatedAt),
	)
	if err != nil {
		return sqlitedb.TranslateError("upsert service plan", err)
	}
	return nil
}

// GetPlan loads one service plan.
func (r *EntitlementRepository) GetPlan(ctx context.Context, code string) (entitlement.Plan, error) {
	const query = `SELECT ` + planColumns + ` FROM service_plans WHERE code = ?`
	var (
		plan      entitlement.Plan
		active    int
		createdAt string
		updatedAt string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, code).Scan(
		&plan.Code, &plan.Name, &plan.IntroQuota, &plan.ValidDays,
		&plan.MaxActiveMatch, &plan.ConsentHours, &active, &createdAt, &updatedAt,
	)
	if sqlitedb.IsNoRows(err) {
		return entitlement.Plan{}, notFound("service plan", code)
	}
	if err != nil {
		return entitlement.Plan{}, sqlitedb.TranslateError("select service plan", err)
	}
	plan.Active = active == 1
	if plan.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
		return entitlement.Plan{}, err
	}
	if plan.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
		return entitlement.Plan{}, err
	}
	return plan.Clone(), nil
}

// ListPlans returns every plan ordered by code.
func (r *EntitlementRepository) ListPlans(ctx context.Context) ([]entitlement.Plan, error) {
	const query = `SELECT ` + planColumns + ` FROM service_plans ORDER BY code`
	rows, err := r.db.Conn(ctx).QueryContext(ctx, query)
	if err != nil {
		return nil, sqlitedb.TranslateError("list service plans", err)
	}
	defer rows.Close()

	plans := make([]entitlement.Plan, 0, 4)
	for rows.Next() {
		var (
			plan      entitlement.Plan
			active    int
			createdAt string
			updatedAt string
		)
		if err := rows.Scan(&plan.Code, &plan.Name, &plan.IntroQuota, &plan.ValidDays,
			&plan.MaxActiveMatch, &plan.ConsentHours, &active, &createdAt, &updatedAt); err != nil {
			return nil, sqlitedb.TranslateError("scan service plan", err)
		}
		plan.Active = active == 1
		if plan.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
			return nil, err
		}
		if plan.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
			return nil, err
		}
		plans = append(plans, plan.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, sqlitedb.TranslateError("iterate service plans", err)
	}
	return plans, nil
}

// Create inserts a granted entitlement.
func (r *EntitlementRepository) Create(ctx context.Context, granted entitlement.Entitlement) error {
	if err := granted.Validate(); err != nil {
		return err
	}
	const statement = `
INSERT INTO entitlements (id, member_id, plan_code, total, used, reserved, state, valid_from, valid_until, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		granted.ID, granted.MemberID, granted.PlanCode, granted.Total, granted.Used, granted.Reserved,
		string(granted.State), sqlitedb.FormatTime(granted.ValidFrom), sqlitedb.FormatTime(granted.ValidUntil),
		granted.Version, sqlitedb.FormatTime(granted.CreatedAt), sqlitedb.FormatTime(granted.UpdatedAt),
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict, "member already holds an active entitlement", err)
		}
		return sqlitedb.TranslateError("insert entitlement", err)
	}
	return nil
}

// GetByID loads an entitlement by identifier.
func (r *EntitlementRepository) GetByID(ctx context.Context, id string) (entitlement.Entitlement, error) {
	const query = `SELECT ` + entitlementColumns + ` FROM entitlements WHERE id = ?`
	return r.scanOne(ctx, query, id, id)
}

// GetActiveByMember loads the single active entitlement of a member.
func (r *EntitlementRepository) GetActiveByMember(ctx context.Context, memberID string) (entitlement.Entitlement, error) {
	const query = `SELECT ` + entitlementColumns + ` FROM entitlements WHERE member_id = ? AND state = 'active'`
	granted, err := r.scanOne(ctx, query, memberID, memberID)
	if err != nil && apperr.CodeOf(err) == apperr.CodeNotFound {
		return entitlement.Entitlement{}, apperr.Newf(apperr.CodeQuotaExhausted,
			"member %s has no active service plan", memberID)
	}
	return granted, err
}

// scanOne shares the row decoding of both entitlement lookups.
func (r *EntitlementRepository) scanOne(ctx context.Context, query, label string, arg any) (entitlement.Entitlement, error) {
	var (
		granted    entitlement.Entitlement
		state      string
		validFrom  string
		validUntil string
		createdAt  string
		updatedAt  string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, arg).Scan(
		&granted.ID, &granted.MemberID, &granted.PlanCode, &granted.Total, &granted.Used,
		&granted.Reserved, &state, &validFrom, &validUntil, &granted.Version, &createdAt, &updatedAt,
	)
	if sqlitedb.IsNoRows(err) {
		return entitlement.Entitlement{}, notFound("entitlement", label)
	}
	if err != nil {
		return entitlement.Entitlement{}, sqlitedb.TranslateError("select entitlement", err)
	}
	granted.State = entitlement.State(state)
	if granted.ValidFrom, err = sqlitedb.ParseTime(validFrom); err != nil {
		return entitlement.Entitlement{}, err
	}
	if granted.ValidUntil, err = sqlitedb.ParseTime(validUntil); err != nil {
		return entitlement.Entitlement{}, err
	}
	if granted.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
		return entitlement.Entitlement{}, err
	}
	if granted.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
		return entitlement.Entitlement{}, err
	}
	return granted.Clone(), nil
}

// Reserve holds one introduction. The whole rule lives in the WHERE clause: the
// expected version, the remaining allowance and the validity window are checked
// by the database, so two concurrent callers cannot both take the last one.
func (r *EntitlementRepository) Reserve(ctx context.Context, id string, version int64, now time.Time) error {
	const statement = `
UPDATE entitlements
SET reserved = reserved + 1,
    version = version + 1,
    updated_at = ?
WHERE id = ?
  AND version = ?
  AND state = 'active'
  AND valid_from <= ?
  AND valid_until > ?
  AND used + reserved < total`
	stamp := sqlitedb.FormatTime(now)
	return r.conditionalUpdate(ctx, "reserve introduction", statement,
		[]any{stamp, id, version, stamp, stamp}, id, now)
}

// Release returns a previously held introduction.
func (r *EntitlementRepository) Release(ctx context.Context, id string, version int64, now time.Time) error {
	const statement = `
UPDATE entitlements
SET reserved = reserved - 1,
    version = version + 1,
    updated_at = ?
WHERE id = ?
  AND version = ?
  AND reserved > 0`
	return r.conditionalUpdate(ctx, "release introduction", statement,
		[]any{sqlitedb.FormatTime(now), id, version}, id, now)
}

// Consume converts a held introduction into a consumed one and exhausts the
// entitlement once nothing is left.
func (r *EntitlementRepository) Consume(ctx context.Context, id string, version int64, now time.Time) error {
	const statement = `
UPDATE entitlements
SET reserved = reserved - 1,
    used = used + 1,
    state = CASE WHEN used + 1 >= total THEN 'exhausted' ELSE state END,
    version = version + 1,
    updated_at = ?
WHERE id = ?
  AND version = ?
  AND reserved > 0
  AND used < total`
	return r.conditionalUpdate(ctx, "consume introduction", statement,
		[]any{sqlitedb.FormatTime(now), id, version}, id, now)
}

// conditionalUpdate runs a guarded update and turns "no row matched" into the
// precise business reason by re-reading the current row.
func (r *EntitlementRepository) conditionalUpdate(
	ctx context.Context, operation, statement string, args []any, id string, now time.Time,
) error {
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement, args...)
	if err != nil {
		return sqlitedb.TranslateError(operation, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return sqlitedb.TranslateError(operation+" rows", err)
	}
	if affected == 1 {
		return nil
	}
	current, lookupErr := r.GetByID(ctx, id)
	if lookupErr != nil {
		return lookupErr
	}
	if reason := current.CanReserve(now); reason != nil && operation == "reserve introduction" {
		return reason
	}
	return apperr.Newf(apperr.CodeVersionConflict,
		"entitlement %s changed concurrently while trying to %s", id, operation)
}

// AppendLedger records one movement. The unique index over (entitlement, match,
// reason) is what makes a duplicated release impossible, so the conflict is
// translated instead of hidden.
func (r *EntitlementRepository) AppendLedger(ctx context.Context, entry entitlement.LedgerEntry) error {
	if err := entry.Validate(); err != nil {
		return err
	}
	const statement = `
INSERT INTO entitlement_ledger (id, entitlement_id, match_id, reason, delta_reserved, delta_used, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		entry.ID, entry.EntitlementID, entry.MatchID, string(entry.Reason),
		entry.DeltaReserved, entry.DeltaUsed, sqlitedb.FormatTime(entry.CreatedAt),
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrapf(apperr.CodeConflict, err,
				"movement %s for match %s was already recorded", string(entry.Reason), entry.MatchID)
		}
		return sqlitedb.TranslateError("append entitlement ledger", err)
	}
	return nil
}

// ListLedgerByMatch returns the movements recorded for one match.
func (r *EntitlementRepository) ListLedgerByMatch(ctx context.Context, matchID string) ([]entitlement.LedgerEntry, error) {
	const query = `
SELECT id, entitlement_id, match_id, reason, delta_reserved, delta_used, created_at
FROM entitlement_ledger WHERE match_id = ? ORDER BY created_at, id`
	return r.listLedger(ctx, query, matchID)
}

// ListLedgerByEntitlement returns every movement of one entitlement.
func (r *EntitlementRepository) ListLedgerByEntitlement(ctx context.Context, entitlementID string) ([]entitlement.LedgerEntry, error) {
	const query = `
SELECT id, entitlement_id, match_id, reason, delta_reserved, delta_used, created_at
FROM entitlement_ledger WHERE entitlement_id = ? ORDER BY created_at, id`
	return r.listLedger(ctx, query, entitlementID)
}

// listLedger shares the row decoding of both ledger queries.
func (r *EntitlementRepository) listLedger(ctx context.Context, query string, arg any) ([]entitlement.LedgerEntry, error) {
	rows, err := r.db.Conn(ctx).QueryContext(ctx, query, arg)
	if err != nil {
		return nil, sqlitedb.TranslateError("list entitlement ledger", err)
	}
	defer rows.Close()

	entries := make([]entitlement.LedgerEntry, 0, 4)
	for rows.Next() {
		var (
			entry     entitlement.LedgerEntry
			reason    string
			createdAt string
		)
		if err := rows.Scan(&entry.ID, &entry.EntitlementID, &entry.MatchID, &reason,
			&entry.DeltaReserved, &entry.DeltaUsed, &createdAt); err != nil {
			return nil, sqlitedb.TranslateError("scan entitlement ledger", err)
		}
		entry.Reason = entitlement.Reason(reason)
		if entry.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, sqlitedb.TranslateError("iterate entitlement ledger", err)
	}
	return entries, nil
}
