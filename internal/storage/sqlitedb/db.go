// Package sqlitedb owns the real SQLite connection, the transaction manager and
// the versioned migration runner. It is the only package that imports a database
// driver, which keeps the domain and service layers storage agnostic.
package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	// Pure Go SQLite driver: no cgo, so the same source cross-compiles to every
	// target architecture of the release images.
	_ "modernc.org/sqlite"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// driverName is the registered pure Go SQLite driver.
const driverName = "sqlite"

// TimeLayout is the fixed-width UTC layout used for every stored timestamp.
// Fixed width matters: it makes lexicographic comparison in SQL identical to
// chronological comparison, which the deadline and expiry queries rely on.
const TimeLayout = "2006-01-02T15:04:05.000000000Z"

// Options configures a database handle.
type Options struct {
	// Path is the database file. The special value ":memory:" is rejected because
	// production persistence must survive a restart.
	Path string
	// BusyTimeout is how long a writer waits for a competing writer.
	BusyTimeout time.Duration
	// MaxOpenConns bounds the pool. It stays above one so that concurrency tests
	// exercise real contention instead of a serialized single connection.
	MaxOpenConns int
}

// withDefaults fills unset options.
func (o Options) withDefaults() Options {
	filled := o
	if filled.BusyTimeout <= 0 {
		filled.BusyTimeout = 5 * time.Second
	}
	if filled.MaxOpenConns <= 0 {
		filled.MaxOpenConns = 8
	}
	return filled
}

// DB wraps the connection pool and exposes the transaction-aware executor used
// by every repository.
type DB struct {
	pool *sql.DB
	path string
}

// txKey is the context key carrying the ambient transaction.
type txKey struct{}

// Querier is the subset of database/sql shared by *sql.DB and *sql.Tx.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Open builds the pool, applies the connection pragmas and verifies that they
// took effect. Verification is deliberate: a silently ignored foreign_keys
// pragma would turn referential integrity into a no-op.
func Open(ctx context.Context, options Options) (*DB, error) {
	filled := options.withDefaults()
	if strings.TrimSpace(filled.Path) == "" {
		return nil, apperr.New(apperr.CodeInvalidArgument, "database path is required")
	}
	if strings.Contains(filled.Path, ":memory:") {
		return nil, apperr.New(apperr.CodeInvalidArgument, "in-memory databases are not a supported production target")
	}
	absolute, err := filepath.Abs(filled.Path)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "resolve database path", err)
	}

	pool, err := sql.Open(driverName, buildDSN(absolute, filled.BusyTimeout))
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeUnavailable, "open database", err)
	}
	pool.SetMaxOpenConns(filled.MaxOpenConns)
	pool.SetMaxIdleConns(filled.MaxOpenConns)
	pool.SetConnMaxLifetime(time.Hour)

	db := &DB{pool: pool, path: absolute}
	if err := db.verifyPragmas(ctx); err != nil {
		_ = pool.Close()
		return nil, err
	}
	return db, nil
}

// buildDSN renders the connection string. Pragmas are part of the DSN so that
// every pooled connection receives them, not only the first one.
func buildDSN(absolutePath string, busyTimeout time.Duration) string {
	values := url.Values{}
	values.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	values.Add("_pragma", "journal_mode(WAL)")
	values.Add("_pragma", "foreign_keys(ON)")
	values.Add("_pragma", "synchronous(NORMAL)")
	// Every transaction takes the write lock immediately. This removes the
	// deferred-to-exclusive upgrade deadlock that otherwise turns two concurrent
	// read-then-write transactions into an unrecoverable SQLITE_BUSY.
	values.Set("_txlock", "immediate")
	return "file:" + filepath.ToSlash(absolutePath) + "?" + values.Encode()
}

// verifyPragmas asserts that the connection level settings the service depends on
// are actually active.
func (d *DB) verifyPragmas(ctx context.Context) error {
	var journalMode string
	if err := d.pool.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return apperr.Wrap(apperr.CodeUnavailable, "read journal_mode", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return apperr.Newf(apperr.CodeUnavailable, "expected WAL journal mode, got %q", journalMode)
	}
	var foreignKeys int
	if err := d.pool.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return apperr.Wrap(apperr.CodeUnavailable, "read foreign_keys", err)
	}
	if foreignKeys != 1 {
		return apperr.New(apperr.CodeUnavailable, "foreign key enforcement is disabled")
	}
	return nil
}

// Path returns the absolute database file path.
func (d *DB) Path() string { return d.path }

// Pool exposes the underlying pool for health checks.
func (d *DB) Pool() *sql.DB { return d.pool }

// Close releases the pool.
func (d *DB) Close() error {
	if d == nil || d.pool == nil {
		return nil
	}
	return d.pool.Close()
}

// Ping verifies the database answers, propagating the caller's context.
func (d *DB) Ping(ctx context.Context) error {
	if err := d.pool.PingContext(ctx); err != nil {
		return apperr.Wrap(apperr.CodeUnavailable, "ping database", err)
	}
	return nil
}

// Conn returns the ambient transaction when the context carries one and the pool
// otherwise. Repositories always go through this method, which is what makes a
// repository call automatically join the caller's transaction.
func (d *DB) Conn(ctx context.Context) Querier {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok && tx != nil {
		return tx
	}
	return d.pool
}

// InTx reports whether the context already carries a transaction.
func InTx(ctx context.Context) bool {
	tx, ok := ctx.Value(txKey{}).(*sql.Tx)
	return ok && tx != nil
}

// WithinTx runs fn inside one transaction. Nested calls join the outer
// transaction so that a service composing two repository helpers still commits
// atomically. A panic inside fn rolls the transaction back and is re-raised.
func (d *DB) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}
	tx, err := d.pool.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(apperr.CodeUnavailable, "begin transaction", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
		_ = tx.Rollback()
	}()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(apperr.CodeUnavailable, "commit transaction", err)
	}
	committed = true
	return nil
}

// FormatTime renders an instant in the fixed-width UTC storage layout.
func FormatTime(instant time.Time) string {
	return instant.UTC().Format(TimeLayout)
}

// FormatNullableTime renders an optional instant, returning nil for absent values
// so that the column stays SQL NULL.
func FormatNullableTime(instant *time.Time) any {
	if instant == nil {
		return nil
	}
	return FormatTime(*instant)
}

// ParseTime parses a stored timestamp.
func ParseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(TimeLayout, value)
	if err != nil {
		// Tolerate RFC3339 values written by older releases so that an upgrade
		// never fails on a legitimately stored timestamp.
		fallback, fallbackErr := time.Parse(time.RFC3339Nano, value)
		if fallbackErr != nil {
			return time.Time{}, apperr.Wrap(apperr.CodeInternal, "parse stored timestamp", err)
		}
		return fallback.UTC(), nil
	}
	return parsed.UTC(), nil
}

// ParseNullableTime parses an optional stored timestamp.
func ParseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := ParseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// IsNoRows reports whether err is the driver's "no rows" signal.
func IsNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// IsUniqueViolation reports whether err is a uniqueness or primary key conflict.
// The driver reports these as text, so the check is textual by necessity and is
// kept in exactly one place.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(err.Error())
	return strings.Contains(message, "UNIQUE CONSTRAINT FAILED") ||
		strings.Contains(message, "SQLITE_CONSTRAINT_UNIQUE") ||
		strings.Contains(message, "SQLITE_CONSTRAINT_PRIMARYKEY")
}

// IsCheckViolation reports whether err is a CHECK constraint failure.
func IsCheckViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(err.Error())
	return strings.Contains(message, "CHECK CONSTRAINT FAILED") ||
		strings.Contains(message, "SQLITE_CONSTRAINT_CHECK")
}

// TranslateError converts a driver error into the stable business vocabulary
// while preserving the original cause in the chain.
func TranslateError(operation string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return apperr.Wrap(apperr.CodeCanceled, operation, err)
	case errors.Is(err, context.DeadlineExceeded):
		return apperr.Wrap(apperr.CodeDeadlineExceeded, operation, err)
	case IsNoRows(err):
		return apperr.Wrap(apperr.CodeNotFound, operation, err)
	case IsUniqueViolation(err):
		return apperr.Wrap(apperr.CodeConflict, operation, err)
	case IsCheckViolation(err):
		return apperr.Wrap(apperr.CodePreconditionFailed, operation, err)
	default:
		return apperr.Wrap(apperr.CodeInternal, operation, err)
	}
}
