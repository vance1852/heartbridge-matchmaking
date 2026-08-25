package sqlitedb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// Migration is one versioned schema step loaded from the embedded file set.
type Migration struct {
	Version  int
	Name     string
	Checksum string
	Body     string
}

// AppliedMigration is a row of the schema_migrations bookkeeping table.
type AppliedMigration struct {
	Version   int
	Name      string
	Checksum  string
	AppliedAt time.Time
}

// LoadMigrations reads and orders every "<version>_<name>.sql" file in fsys.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "list migrations", err)
	}
	if len(entries) == 0 {
		return nil, apperr.New(apperr.CodeInternal, "no migrations were embedded")
	}
	loaded := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, name := range entries {
		version, label, err := parseMigrationName(name)
		if err != nil {
			return nil, err
		}
		if previous, duplicate := seen[version]; duplicate {
			return nil, apperr.Newf(apperr.CodeInternal,
				"migration version %d is declared twice by %s and %s", version, previous, name)
		}
		seen[version] = name
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, apperr.Wrap(apperr.CodeInternal, "read migration "+name, err)
		}
		sum := sha256.Sum256(body)
		loaded = append(loaded, Migration{
			Version:  version,
			Name:     label,
			Checksum: hex.EncodeToString(sum[:]),
			Body:     string(body),
		})
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Version < loaded[j].Version })
	return loaded, nil
}

// parseMigrationName splits "0002_entitlements_and_matches.sql" into 2 and the
// label.
func parseMigrationName(fileName string) (int, string, error) {
	trimmed := strings.TrimSuffix(fileName, ".sql")
	separator := strings.IndexByte(trimmed, '_')
	if separator <= 0 || separator == len(trimmed)-1 {
		return 0, "", apperr.Newf(apperr.CodeInternal,
			"migration %q must be named <version>_<name>.sql", fileName)
	}
	version, err := strconv.Atoi(trimmed[:separator])
	if err != nil || version <= 0 {
		return 0, "", apperr.Newf(apperr.CodeInternal,
			"migration %q must start with a positive version", fileName)
	}
	return version, trimmed[separator+1:], nil
}

// Migrate brings the database to the latest embedded version.
//
// Re-running it on an up to date database is a no-op. Two situations block the
// service instead of guessing: a stored checksum that no longer matches the
// embedded file, and a database that already carries a version this binary does
// not know. Neither is repaired silently, because both mean the running binary
// and the data on disk disagree about the schema.
func (d *DB) Migrate(ctx context.Context, fsys fs.FS) ([]Migration, error) {
	available, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	if err := d.ensureMigrationTable(ctx); err != nil {
		return nil, err
	}
	applied, err := d.AppliedMigrations(ctx)
	if err != nil {
		return nil, err
	}
	appliedByVersion := make(map[int]AppliedMigration, len(applied))
	for _, record := range applied {
		appliedByVersion[record.Version] = record
	}
	known := make(map[int]struct{}, len(available))
	for _, migration := range available {
		known[migration.Version] = struct{}{}
	}
	for _, record := range applied {
		if _, ok := known[record.Version]; !ok {
			return nil, apperr.Newf(apperr.CodeUnavailable,
				"database is at schema version %d (%s) which this build does not contain; refusing to downgrade",
				record.Version, record.Name)
		}
	}

	executed := make([]Migration, 0, len(available))
	for _, migration := range available {
		if record, ok := appliedByVersion[migration.Version]; ok {
			if record.Checksum != migration.Checksum {
				return nil, apperr.Newf(apperr.CodeUnavailable,
					"migration %d (%s) was applied with checksum %s but the embedded file has checksum %s; refusing to rewrite history",
					migration.Version, migration.Name, record.Checksum, migration.Checksum)
			}
			continue
		}
		if err := d.applyMigration(ctx, migration); err != nil {
			return nil, err
		}
		executed = append(executed, migration)
	}
	return executed, nil
}

// ensureMigrationTable creates the bookkeeping table when it is missing.
func (d *DB) ensureMigrationTable(ctx context.Context) error {
	const statement = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`
	if _, err := d.pool.ExecContext(ctx, statement); err != nil {
		return TranslateError("create schema_migrations", err)
	}
	return nil
}

// applyMigration runs one migration and records it in the same transaction, so a
// half applied version can never be marked as done.
func (d *DB) applyMigration(ctx context.Context, migration Migration) error {
	return d.WithinTx(ctx, func(ctx context.Context) error {
		conn := d.Conn(ctx)
		if _, err := conn.ExecContext(ctx, migration.Body); err != nil {
			return apperr.Wrap(apperr.CodeUnavailable,
				fmt.Sprintf("apply migration %d (%s)", migration.Version, migration.Name), err)
		}
		const insert = `
INSERT INTO schema_migrations (version, name, checksum, applied_at)
VALUES (?, ?, ?, ?)`
		if _, err := conn.ExecContext(ctx, insert,
			migration.Version, migration.Name, migration.Checksum, FormatTime(time.Now())); err != nil {
			return TranslateError("record migration", err)
		}
		return nil
	})
}

// AppliedMigrations returns the recorded migrations in ascending version order.
func (d *DB) AppliedMigrations(ctx context.Context) ([]AppliedMigration, error) {
	const query = `SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`
	rows, err := d.Conn(ctx).QueryContext(ctx, query)
	if err != nil {
		return nil, TranslateError("list applied migrations", err)
	}
	defer rows.Close()

	records := make([]AppliedMigration, 0, 8)
	for rows.Next() {
		var record AppliedMigration
		var appliedAt string
		if err := rows.Scan(&record.Version, &record.Name, &record.Checksum, &appliedAt); err != nil {
			return nil, TranslateError("scan applied migration", err)
		}
		parsed, err := ParseTime(appliedAt)
		if err != nil {
			return nil, err
		}
		record.AppliedAt = parsed
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, TranslateError("iterate applied migrations", err)
	}
	return records, nil
}

// SchemaVersion returns the highest applied version, or zero for an empty
// database.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	if err := d.ensureMigrationTable(ctx); err != nil {
		return 0, err
	}
	var version int
	const query = `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`
	if err := d.Conn(ctx).QueryRowContext(ctx, query).Scan(&version); err != nil {
		return 0, TranslateError("read schema version", err)
	}
	return version, nil
}
