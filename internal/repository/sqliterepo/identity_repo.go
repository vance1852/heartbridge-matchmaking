package sqliterepo

import (
	"context"
	"database/sql"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/storage/sqlitedb"
)

// UserRepository persists accounts in SQLite.
type UserRepository struct {
	db *sqlitedb.DB
}

// NewUserRepository builds a UserRepository.
func NewUserRepository(db *sqlitedb.DB) *UserRepository { return &UserRepository{db: db} }

const userColumns = `id, email, password_hash, role, status, created_at, updated_at`

// Create inserts an account, mapping a duplicate email onto a conflict error.
func (r *UserRepository) Create(ctx context.Context, user identity.User) error {
	const statement = `
INSERT INTO users (id, email, password_hash, role, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		user.ID,
		identity.NormalizeEmail(user.Email),
		user.PasswordHash,
		string(user.Role),
		string(user.Status),
		sqlitedb.FormatTime(user.CreatedAt),
		sqlitedb.FormatTime(user.UpdatedAt),
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict, "email is already registered", err)
		}
		return sqlitedb.TranslateError("insert user", err)
	}
	return nil
}

// GetByID loads an account by identifier.
func (r *UserRepository) GetByID(ctx context.Context, id string) (identity.User, error) {
	const query = `SELECT ` + userColumns + ` FROM users WHERE id = ?`
	return r.scanOne(ctx, query, "user", id, id)
}

// GetByEmail loads an account by its normalized email.
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (identity.User, error) {
	const query = `SELECT ` + userColumns + ` FROM users WHERE email = ?`
	normalized := identity.NormalizeEmail(email)
	return r.scanOne(ctx, query, "user", normalized, normalized)
}

// scanOne shares the single-row decoding of both lookups.
func (r *UserRepository) scanOne(ctx context.Context, query, entity, label string, arg any) (identity.User, error) {
	var (
		user      identity.User
		role      string
		status    string
		createdAt string
		updatedAt string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, arg).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &role, &status, &createdAt, &updatedAt,
	)
	if sqlitedb.IsNoRows(err) {
		return identity.User{}, notFound(entity, label)
	}
	if err != nil {
		return identity.User{}, sqlitedb.TranslateError("select user", err)
	}
	user.Role = identity.Role(role)
	user.Status = identity.AccountStatus(status)
	if user.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
		return identity.User{}, err
	}
	if user.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
		return identity.User{}, err
	}
	return user.Clone(), nil
}

// SessionRepository persists revocable sessions in SQLite.
type SessionRepository struct {
	db *sqlitedb.DB
}

// NewSessionRepository builds a SessionRepository.
func NewSessionRepository(db *sqlitedb.DB) *SessionRepository { return &SessionRepository{db: db} }

// Create inserts a session row.
func (r *SessionRepository) Create(ctx context.Context, session identity.Session) error {
	const statement = `
INSERT INTO sessions (id, user_id, token_hash, issued_at, expires_at, revoked_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		session.ID,
		session.UserID,
		session.TokenHash,
		sqlitedb.FormatTime(session.IssuedAt),
		sqlitedb.FormatTime(session.ExpiresAt),
		sqlitedb.FormatNullableTime(session.RevokedAt),
		sqlitedb.FormatTime(session.LastSeenAt),
	)
	if err != nil {
		return sqlitedb.TranslateError("insert session", err)
	}
	return nil
}

// GetByTokenHash loads a session by the digest of its bearer token.
func (r *SessionRepository) GetByTokenHash(ctx context.Context, tokenHash string) (identity.Session, error) {
	const query = `
SELECT id, user_id, token_hash, issued_at, expires_at, revoked_at, last_seen_at
FROM sessions WHERE token_hash = ?`
	var (
		session    identity.Session
		issuedAt   string
		expiresAt  string
		revokedAt  sql.NullString
		lastSeenAt string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, tokenHash).Scan(
		&session.ID, &session.UserID, &session.TokenHash,
		&issuedAt, &expiresAt, &revokedAt, &lastSeenAt,
	)
	if sqlitedb.IsNoRows(err) {
		return identity.Session{}, apperr.New(apperr.CodeUnauthenticated, "session token is not recognised")
	}
	if err != nil {
		return identity.Session{}, sqlitedb.TranslateError("select session", err)
	}
	if session.IssuedAt, err = sqlitedb.ParseTime(issuedAt); err != nil {
		return identity.Session{}, err
	}
	if session.ExpiresAt, err = sqlitedb.ParseTime(expiresAt); err != nil {
		return identity.Session{}, err
	}
	if session.LastSeenAt, err = sqlitedb.ParseTime(lastSeenAt); err != nil {
		return identity.Session{}, err
	}
	if session.RevokedAt, err = sqlitedb.ParseNullableTime(revokedAt); err != nil {
		return identity.Session{}, err
	}
	return session.Clone(), nil
}

// Revoke stamps a session as revoked. Revoking an already revoked session keeps
// the first timestamp so that the audit trail stays truthful.
func (r *SessionRepository) Revoke(ctx context.Context, sessionID string, at time.Time) error {
	const statement = `UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement, sqlitedb.FormatTime(at), sessionID)
	if err != nil {
		return sqlitedb.TranslateError("revoke session", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return sqlitedb.TranslateError("revoke session rows", err)
	}
	if affected == 0 {
		// Either the session does not exist or it was already revoked. Both are
		// reported as an authentication failure so that logout stays idempotent
		// from the caller's point of view without leaking which case occurred.
		return apperr.New(apperr.CodeUnauthenticated, "session is no longer active")
	}
	return nil
}

// TouchLastSeen records the last successful authentication of a session.
func (r *SessionRepository) TouchLastSeen(ctx context.Context, sessionID string, at time.Time) error {
	const statement = `UPDATE sessions SET last_seen_at = ? WHERE id = ?`
	if _, err := r.db.Conn(ctx).ExecContext(ctx, statement, sqlitedb.FormatTime(at), sessionID); err != nil {
		return sqlitedb.TranslateError("touch session", err)
	}
	return nil
}

// DeleteExpiredBefore removes sessions that expired before the cutoff.
func (r *SessionRepository) DeleteExpiredBefore(ctx context.Context, cutoff time.Time) (int, error) {
	const statement = `DELETE FROM sessions WHERE expires_at < ?`
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement, sqlitedb.FormatTime(cutoff))
	if err != nil {
		return 0, sqlitedb.TranslateError("delete expired sessions", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, sqlitedb.TranslateError("delete expired sessions rows", err)
	}
	return int(affected), nil
}
