package sqliterepo

import (
	"context"
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/notify"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/storage/sqlitedb"
)

// NotificationRepository persists the transactional outbox.
type NotificationRepository struct {
	db *sqlitedb.DB
}

// NewNotificationRepository builds a NotificationRepository.
func NewNotificationRepository(db *sqlitedb.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

const notificationColumns = `id, match_id, recipient_id, kind, payload, state, attempts, max_attempts, next_attempt_at, last_error, created_at, updated_at`

// Enqueue appends an outbox row. It is always called inside the transaction of
// the business change it announces.
func (r *NotificationRepository) Enqueue(ctx context.Context, job notify.Job) error {
	if err := job.Validate(); err != nil {
		return err
	}
	const statement = `
INSERT INTO notification_jobs (id, match_id, recipient_id, kind, payload, state, attempts, max_attempts, next_attempt_at, last_error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		job.ID, job.MatchID, job.RecipientID, string(job.Kind), job.Payload,
		string(job.State), job.Attempts, job.MaxAttempts,
		sqlitedb.FormatTime(job.NextAttemptAt), job.LastError,
		sqlitedb.FormatTime(job.CreatedAt), sqlitedb.FormatTime(job.UpdatedAt),
	)
	if err != nil {
		return sqlitedb.TranslateError("enqueue notification", err)
	}
	return nil
}

// GetByID loads one outbox row.
func (r *NotificationRepository) GetByID(ctx context.Context, id string) (notify.Job, error) {
	const query = `SELECT ` + notificationColumns + ` FROM notification_jobs WHERE id = ?`
	var (
		job           notify.Job
		kind          string
		state         string
		nextAttemptAt string
		createdAt     string
		updatedAt     string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, id).Scan(
		&job.ID, &job.MatchID, &job.RecipientID, &kind, &job.Payload, &state,
		&job.Attempts, &job.MaxAttempts, &nextAttemptAt, &job.LastError, &createdAt, &updatedAt,
	)
	if sqlitedb.IsNoRows(err) {
		return notify.Job{}, notFound("notification job", id)
	}
	if err != nil {
		return notify.Job{}, sqlitedb.TranslateError("select notification", err)
	}
	job.Kind = notify.Kind(kind)
	job.State = notify.State(state)
	if job.NextAttemptAt, err = sqlitedb.ParseTime(nextAttemptAt); err != nil {
		return notify.Job{}, err
	}
	if job.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
		return notify.Job{}, err
	}
	if job.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
		return notify.Job{}, err
	}
	return job.Clone(), nil
}

// ListDue returns pending rows whose next attempt is due.
func (r *NotificationRepository) ListDue(ctx context.Context, now time.Time, limit int) ([]notify.Job, error) {
	if limit <= 0 {
		limit = repository.DefaultPageSize
	}
	const query = `SELECT ` + notificationColumns + `
FROM notification_jobs
WHERE state = 'pending' AND next_attempt_at <= ?
ORDER BY next_attempt_at, id
LIMIT ?`
	rows, err := r.db.Conn(ctx).QueryContext(ctx, query, sqlitedb.FormatTime(now), limit)
	if err != nil {
		return nil, sqlitedb.TranslateError("list due notifications", err)
	}
	defer rows.Close()

	jobs := make([]notify.Job, 0, limit)
	for rows.Next() {
		var (
			job           notify.Job
			kind          string
			state         string
			nextAttemptAt string
			createdAt     string
			updatedAt     string
		)
		if err := rows.Scan(&job.ID, &job.MatchID, &job.RecipientID, &kind, &job.Payload, &state,
			&job.Attempts, &job.MaxAttempts, &nextAttemptAt, &job.LastError, &createdAt, &updatedAt); err != nil {
			return nil, sqlitedb.TranslateError("scan due notification", err)
		}
		job.Kind = notify.Kind(kind)
		job.State = notify.State(state)
		if job.NextAttemptAt, err = sqlitedb.ParseTime(nextAttemptAt); err != nil {
			return nil, err
		}
		if job.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
			return nil, err
		}
		if job.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, job.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, sqlitedb.TranslateError("iterate due notifications", err)
	}
	return jobs, nil
}

// MarkSucceeded records a delivered notification.
func (r *NotificationRepository) MarkSucceeded(ctx context.Context, id string, attempts int, at time.Time) error {
	const statement = `
UPDATE notification_jobs
SET state = 'succeeded', attempts = ?, last_error = '', updated_at = ?
WHERE id = ? AND state = 'pending'`
	return r.updateOne(ctx, "mark notification succeeded", statement,
		attempts, sqlitedb.FormatTime(at), id)
}

// ScheduleRetry parks a failed attempt until its backoff elapsed.
func (r *NotificationRepository) ScheduleRetry(
	ctx context.Context, id string, attempts int, nextAttemptAt time.Time, lastError string, at time.Time,
) error {
	const statement = `
UPDATE notification_jobs
SET attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ?
WHERE id = ? AND state = 'pending'`
	return r.updateOne(ctx, "schedule notification retry", statement,
		attempts, sqlitedb.FormatTime(nextAttemptAt), truncateError(lastError),
		sqlitedb.FormatTime(at), id)
}

// MarkPermanentFailure retires a job that exhausted its attempt budget.
func (r *NotificationRepository) MarkPermanentFailure(
	ctx context.Context, id string, attempts int, lastError string, at time.Time,
) error {
	const statement = `
UPDATE notification_jobs
SET state = 'failed_permanent', attempts = ?, last_error = ?, updated_at = ?
WHERE id = ? AND state = 'pending'`
	return r.updateOne(ctx, "mark notification permanently failed", statement,
		attempts, truncateError(lastError), sqlitedb.FormatTime(at), id)
}

// updateOne shares the affected-row handling of the three outbox transitions.
func (r *NotificationRepository) updateOne(ctx context.Context, operation, statement string, args ...any) error {
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement, args...)
	if err != nil {
		return sqlitedb.TranslateError(operation, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return sqlitedb.TranslateError(operation+" rows", err)
	}
	if affected == 0 {
		return apperr.Newf(apperr.CodeConflict, "notification job is no longer pending")
	}
	return nil
}

// CountByState counts outbox rows in a delivery state.
func (r *NotificationRepository) CountByState(ctx context.Context, state notify.State) (int, error) {
	if err := state.Validate(); err != nil {
		return 0, err
	}
	const query = `SELECT COUNT(*) FROM notification_jobs WHERE state = ?`
	var total int
	if err := r.db.Conn(ctx).QueryRowContext(ctx, query, string(state)).Scan(&total); err != nil {
		return 0, sqlitedb.TranslateError("count notifications", err)
	}
	return total, nil
}

// truncateError bounds a stored failure message.
func truncateError(message string) string {
	trimmed := strings.TrimSpace(message)
	const limit = 400
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit]
}

// AuditRepository persists and queries the operational trail.
type AuditRepository struct {
	db *sqlitedb.DB
}

// NewAuditRepository builds an AuditRepository.
func NewAuditRepository(db *sqlitedb.DB) *AuditRepository { return &AuditRepository{db: db} }

const auditColumns = `id, actor_id, actor_role, action, object_type, object_id, result, detail, request_id, created_at`

// Append writes one audit row inside the caller's transaction.
func (r *AuditRepository) Append(ctx context.Context, event audit.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	const statement = `
INSERT INTO audit_events (id, actor_id, actor_role, action, object_type, object_id, result, detail, request_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		event.ID, event.ActorID, event.ActorRole, string(event.Action),
		string(event.ObjectType), event.ObjectID, string(event.Result),
		event.Detail, event.RequestID, sqlitedb.FormatTime(event.CreatedAt),
	)
	if err != nil {
		return sqlitedb.TranslateError("append audit event", err)
	}
	return nil
}

// List returns one page of audit rows with the total under the same predicate.
func (r *AuditRepository) List(ctx context.Context, filter repository.AuditFilter) (repository.AuditPage, error) {
	if err := filter.Page.Validate(); err != nil {
		return repository.AuditPage{}, err
	}
	normalized := filter.Normalize()
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if normalized.ObjectType != "" {
		clauses = append(clauses, "object_type = ?")
		args = append(args, string(normalized.ObjectType))
	}
	if normalized.ObjectID != "" {
		clauses = append(clauses, "object_id = ?")
		args = append(args, normalized.ObjectID)
	}
	if normalized.ActorID != "" {
		clauses = append(clauses, "actor_id = ?")
		args = append(args, normalized.ActorID)
	}
	if normalized.RequestID != "" {
		clauses = append(clauses, "request_id = ?")
		args = append(args, normalized.RequestID)
	}
	predicate := ""
	if len(clauses) > 0 {
		predicate = " WHERE " + strings.Join(clauses, " AND ")
	}

	var total int
	if err := r.db.Conn(ctx).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events`+predicate, args...).Scan(&total); err != nil {
		return repository.AuditPage{}, sqlitedb.TranslateError("count audit events", err)
	}

	query := `SELECT ` + auditColumns + ` FROM audit_events` + predicate +
		` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), normalized.Page.Limit, normalized.Page.Offset)
	rows, err := r.db.Conn(ctx).QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return repository.AuditPage{}, sqlitedb.TranslateError("list audit events", err)
	}
	defer rows.Close()

	items := make([]audit.Event, 0, normalized.Page.Limit)
	for rows.Next() {
		var (
			event      audit.Event
			action     string
			objectType string
			result     string
			createdAt  string
		)
		if err := rows.Scan(&event.ID, &event.ActorID, &event.ActorRole, &action,
			&objectType, &event.ObjectID, &result, &event.Detail, &event.RequestID, &createdAt); err != nil {
			return repository.AuditPage{}, sqlitedb.TranslateError("scan audit event", err)
		}
		event.Action = audit.Action(action)
		event.ObjectType = audit.ObjectType(objectType)
		event.Result = audit.Result(result)
		if event.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
			return repository.AuditPage{}, err
		}
		items = append(items, event.Clone())
	}
	if err := rows.Err(); err != nil {
		return repository.AuditPage{}, sqlitedb.TranslateError("iterate audit events", err)
	}
	return repository.AuditPage{
		Items:  items,
		Total:  total,
		Limit:  normalized.Page.Limit,
		Offset: normalized.Page.Offset,
	}, nil
}

// IdempotencyRepository persists replay records of mutating requests.
type IdempotencyRepository struct {
	db *sqlitedb.DB
}

// NewIdempotencyRepository builds an IdempotencyRepository.
func NewIdempotencyRepository(db *sqlitedb.DB) *IdempotencyRepository {
	return &IdempotencyRepository{db: db}
}

// Get loads a stored replay record.
func (r *IdempotencyRepository) Get(
	ctx context.Context, actorID, method, path, key string,
) (repository.IdempotencyRecord, error) {
	const query = `
SELECT actor_id, method, path, idem_key, request_hash, status, response_b64, created_at, expires_at
FROM idempotency_keys
WHERE actor_id = ? AND method = ? AND path = ? AND idem_key = ?`
	var (
		record    repository.IdempotencyRecord
		createdAt string
		expiresAt string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, actorID, method, path, key).Scan(
		&record.ActorID, &record.Method, &record.Path, &record.Key,
		&record.RequestHash, &record.Status, &record.ResponseB64, &createdAt, &expiresAt,
	)
	if sqlitedb.IsNoRows(err) {
		return repository.IdempotencyRecord{}, notFound("idempotency key", key)
	}
	if err != nil {
		return repository.IdempotencyRecord{}, sqlitedb.TranslateError("select idempotency key", err)
	}
	if record.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
		return repository.IdempotencyRecord{}, err
	}
	if record.ExpiresAt, err = sqlitedb.ParseTime(expiresAt); err != nil {
		return repository.IdempotencyRecord{}, err
	}
	return record, nil
}

// Put stores a replay record, mapping a concurrent insert onto a conflict.
func (r *IdempotencyRepository) Put(ctx context.Context, record repository.IdempotencyRecord) error {
	const statement = `
INSERT INTO idempotency_keys (actor_id, method, path, idem_key, request_hash, status, response_b64, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		record.ActorID, record.Method, record.Path, record.Key, record.RequestHash,
		record.Status, record.ResponseB64,
		sqlitedb.FormatTime(record.CreatedAt), sqlitedb.FormatTime(record.ExpiresAt),
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict, "idempotency key is already recorded", err)
		}
		return sqlitedb.TranslateError("insert idempotency key", err)
	}
	return nil
}

// DeleteExpiredBefore removes replay records past their retention window.
func (r *IdempotencyRepository) DeleteExpiredBefore(ctx context.Context, cutoff time.Time) (int, error) {
	const statement = `DELETE FROM idempotency_keys WHERE expires_at < ?`
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement, sqlitedb.FormatTime(cutoff))
	if err != nil {
		return 0, sqlitedb.TranslateError("delete expired idempotency keys", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, sqlitedb.TranslateError("delete expired idempotency keys rows", err)
	}
	return int(affected), nil
}
