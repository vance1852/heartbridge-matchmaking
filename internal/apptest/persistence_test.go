package apptest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/storage/sqlitedb"
	"github.com/vance1852/heartbridge-matchmaking/migrations"
)

// shutdown closes the HTTP server and the database of a harness so that a test
// can reopen the very same database file.
func (h *harness) shutdown() {
	h.t.Helper()
	h.server.Close()
	if err := h.app.Close(); err != nil {
		h.t.Fatalf("close application: %v", err)
	}
}

// openScratchDatabase opens an empty database in a temporary directory.
func openScratchDatabase(t *testing.T) *sqlitedb.DB {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Options{
		Path:         filepath.Join(t.TempDir(), "scratch.sqlite"),
		BusyTimeout:  2 * time.Second,
		MaxOpenConns: 4,
	})
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close scratch database: %v", err)
		}
	})
	return db
}

// TestMigrationsApplyOnceAndAreIdempotent verifies the versioned migration runner
// against the real embedded schema.
func TestMigrationsApplyOnceAndAreIdempotent(t *testing.T) {
	db := openScratchDatabase(t)
	available, err := sqlitedb.LoadMigrations(migrations.FS)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(available) < 3 {
		t.Fatalf("expected at least three migrations, got %d", len(available))
	}
	for index := 1; index < len(available); index++ {
		if available[index-1].Version >= available[index].Version {
			t.Fatalf("migrations are not ordered: %d then %d",
				available[index-1].Version, available[index].Version)
		}
	}

	applied, err := db.Migrate(context.Background(), migrations.FS)
	if err != nil {
		t.Fatalf("first migration run: %v", err)
	}
	if len(applied) != len(available) {
		t.Fatalf("expected %d migrations to be applied, got %d", len(available), len(applied))
	}
	version, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != available[len(available)-1].Version {
		t.Fatalf("expected schema version %d, got %d", available[len(available)-1].Version, version)
	}

	again, err := db.Migrate(context.Background(), migrations.FS)
	if err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected a repeated run to be a no-op, got %d applied", len(again))
	}
	records, err := db.AppliedMigrations(context.Background())
	if err != nil {
		t.Fatalf("list applied migrations: %v", err)
	}
	if len(records) != len(available) {
		t.Fatalf("expected %d bookkeeping rows, got %d", len(available), len(records))
	}
	for _, record := range records {
		if record.Checksum == "" || record.AppliedAt.IsZero() {
			t.Fatalf("incomplete bookkeeping row for version %d", record.Version)
		}
	}
}

// TestMigrationChecksumConflictBlocksStartup verifies that a rewritten migration
// is reported instead of being silently reapplied or ignored.
func TestMigrationChecksumConflictBlocksStartup(t *testing.T) {
	db := openScratchDatabase(t)
	original := fstest.MapFS{
		"0001_initial.sql": &fstest.MapFile{Data: []byte("CREATE TABLE demo (id TEXT PRIMARY KEY);")},
	}
	if _, err := db.Migrate(context.Background(), original); err != nil {
		t.Fatalf("apply original migration: %v", err)
	}
	rewritten := fstest.MapFS{
		"0001_initial.sql": &fstest.MapFile{Data: []byte("CREATE TABLE demo (id TEXT PRIMARY KEY, extra TEXT);")},
	}
	_, err := db.Migrate(context.Background(), rewritten)
	if err == nil {
		t.Fatal("expected a rewritten migration to be refused")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeUnavailable {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeUnavailable, code, err)
	}
	if !strings.Contains(err.Error(), "refusing to rewrite history") {
		t.Fatalf("expected the failure to explain the conflict, got %v", err)
	}
}

// TestMigrationDowngradeBlocksStartup verifies that an older binary refuses to run
// against a newer database instead of corrupting it.
func TestMigrationDowngradeBlocksStartup(t *testing.T) {
	db := openScratchDatabase(t)
	newer := fstest.MapFS{
		"0001_initial.sql": &fstest.MapFile{Data: []byte("CREATE TABLE demo (id TEXT PRIMARY KEY);")},
		"0002_extend.sql":  &fstest.MapFile{Data: []byte("ALTER TABLE demo ADD COLUMN extra TEXT;")},
	}
	if _, err := db.Migrate(context.Background(), newer); err != nil {
		t.Fatalf("apply newer schema: %v", err)
	}
	older := fstest.MapFS{
		"0001_initial.sql": &fstest.MapFile{Data: []byte("CREATE TABLE demo (id TEXT PRIMARY KEY);")},
	}
	_, err := db.Migrate(context.Background(), older)
	if err == nil {
		t.Fatal("expected a downgrade to be refused")
	}
	if !strings.Contains(err.Error(), "refusing to downgrade") {
		t.Fatalf("expected the failure to explain the downgrade, got %v", err)
	}
}

// TestMigrationNamingIsValidated verifies that a badly named file is reported.
func TestMigrationNamingIsValidated(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"missing version": {"initial.sql": &fstest.MapFile{Data: []byte("SELECT 1;")}},
		"zero version":    {"0000_initial.sql": &fstest.MapFile{Data: []byte("SELECT 1;")}},
		"duplicate version": {
			"0001_a.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			"0001_b.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		},
	}
	for name, fsys := range cases {
		if _, err := sqlitedb.LoadMigrations(fsys); err == nil {
			t.Fatalf("expected %s to be rejected", name)
		}
	}
	if _, err := sqlitedb.LoadMigrations(fstest.MapFS{}); err == nil {
		t.Fatal("expected an empty migration set to be rejected")
	}
}

// TestInMemoryDatabaseIsRefused verifies that production persistence cannot be
// pointed at a volatile store by accident.
func TestInMemoryDatabaseIsRefused(t *testing.T) {
	_, err := sqlitedb.Open(context.Background(), sqlitedb.Options{Path: ":memory:"})
	if err == nil {
		t.Fatal("expected an in-memory database to be refused")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeInvalidArgument {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeInvalidArgument, code, err)
	}
	if _, err := sqlitedb.Open(context.Background(), sqlitedb.Options{Path: "   "}); err == nil {
		t.Fatal("expected a blank database path to be refused")
	}
}

// TestStateSurvivesRestart verifies that the whole business state is recovered
// after the process closes and reopens the same database file.
func TestStateSurvivesRestart(t *testing.T) {
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "restart.sqlite")

	first := newHarnessAt(t, dbPath)
	matchmaker, _ := first.staff("restartmm@heartbridge.test", "matchmaker")
	left := first.enrollMember(memberSpec{Email: "restartA@heartbridge.test", Gender: "female"})
	right := first.enrollMember(memberSpec{Email: "restartB@heartbridge.test", Gender: "male"})
	slotID := first.publishSlot("RST-1", 26*time.Hour, 2*time.Hour, 3)

	detail, err := first.app.Matches.Propose(first.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  left.MemberID,
		SecondMemberID: right.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	for _, fixture := range []memberFixture{left, right} {
		if _, err := first.app.Matches.Decide(first.ctx(), fixture.Actor, detail.Match.ID,
			matching.DecisionAccepted); err != nil {
			t.Fatalf("accept: %v", err)
		}
	}
	booked, err := first.app.Schedule.Book(first.ctx(), matchmaker, detail.Match.ID, slotID)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	pendingBefore := first.pendingNotifications()
	if pendingBefore == 0 {
		t.Fatal("expected the outbox to hold pending notifications before the restart")
	}
	first.shutdown()

	second := newHarnessAt(t, dbPath)
	recovered, err := second.app.Matches.Get(second.ctx(), matchmaker, detail.Match.ID)
	if err != nil {
		t.Fatalf("read introduction after restart: %v", err)
	}
	if recovered.Match.State != matching.StateScheduled {
		t.Fatalf("expected state %s after restart, got %s", matching.StateScheduled, recovered.Match.State)
	}
	if recovered.Match.Version != detail.Match.Version+2 {
		t.Fatalf("expected the version to be preserved, got %d", recovered.Match.Version)
	}
	for _, memberID := range []string{left.MemberID, right.MemberID} {
		granted := second.allowance(memberID)
		if granted.Reserved != 1 || granted.Used != 0 {
			t.Fatalf("member %s lost its allowance state: reserved=%d used=%d",
				memberID, granted.Reserved, granted.Used)
		}
	}
	slot, err := second.app.Repositories.Slots.GetByID(second.ctx(), slotID)
	if err != nil {
		t.Fatalf("read slot after restart: %v", err)
	}
	if slot.BookedCount != 1 {
		t.Fatalf("expected the venue booking to survive, got booked_count=%d", slot.BookedCount)
	}
	appointment, err := second.app.Repositories.Meetups.GetByID(second.ctx(), booked.Meetup.ID)
	if err != nil {
		t.Fatalf("read meetup after restart: %v", err)
	}
	if appointment.State != meetup.StateBooked {
		t.Fatalf("expected the meetup to still be booked, got %s", appointment.State)
	}
	if pendingAfter := second.pendingNotifications(); pendingAfter != pendingBefore {
		t.Fatalf("expected %d pending notifications to survive, got %d", pendingBefore, pendingAfter)
	}
	// The restarted process can continue the business flow from the recovered state.
	second.clk.Advance(27 * time.Hour)
	if _, err := second.app.Schedule.CheckIn(second.ctx(), matchmaker, booked.Meetup.ID); err != nil {
		t.Fatalf("check in after restart: %v", err)
	}
}

// TestRepositoryReturnsIsolatedValues verifies that a caller mutating a returned
// value cannot corrupt the stored state.
func TestRepositoryReturnsIsolatedValues(t *testing.T) {
	h := newHarness(t)
	fixture := h.enrollMember(memberSpec{Email: "isolate@heartbridge.test", Gender: "female"})

	preference, err := h.app.Repositories.Members.GetPreference(h.ctx(), fixture.MemberID)
	if err != nil {
		t.Fatalf("read preference: %v", err)
	}
	originalCities := len(preference.Cities)
	if originalCities == 0 {
		t.Fatal("expected the fixture to register acceptable cities")
	}
	preference.Cities = append(preference.Cities, "Sanya")
	preference.Cities[0] = "Mutated"
	preference.MaritalStatuses = append(preference.MaritalStatuses, "widowed")

	reread, err := h.app.Repositories.Members.GetPreference(h.ctx(), fixture.MemberID)
	if err != nil {
		t.Fatalf("re-read preference: %v", err)
	}
	if len(reread.Cities) != originalCities {
		t.Fatalf("stored city list was corrupted: %v", reread.Cities)
	}
	if reread.Cities[0] == "Mutated" {
		t.Fatalf("the caller mutated repository owned memory: %v", reread.Cities)
	}
	for _, city := range reread.Cities {
		if city == "Sanya" {
			t.Fatal("an appended city leaked into storage")
		}
	}

	// The same guarantee holds for optional timestamps on a closed introduction.
	matchmaker, _ := h.staff("isolatemm@heartbridge.test", "matchmaker")
	partner := h.enrollMember(memberSpec{Email: "isolateB@heartbridge.test", Gender: "male"})
	detail, err := h.app.Matches.Propose(h.ctx(), matchmaker, matchsvc.ProposeInput{
		FirstMemberID:  fixture.MemberID,
		SecondMemberID: partner.MemberID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := h.app.Matches.Cancel(h.ctx(), matchmaker, detail.Match.ID, "isolation check"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	closed, err := h.app.Repositories.Matches.GetByID(h.ctx(), detail.Match.ID)
	if err != nil {
		t.Fatalf("read closed introduction: %v", err)
	}
	if closed.ClosedAt == nil {
		t.Fatal("expected a closing timestamp")
	}
	*closed.ClosedAt = closed.ClosedAt.Add(72 * time.Hour)
	reloaded, err := h.app.Repositories.Matches.GetByID(h.ctx(), detail.Match.ID)
	if err != nil {
		t.Fatalf("re-read closed introduction: %v", err)
	}
	if reloaded.ClosedAt.Equal(*closed.ClosedAt) {
		t.Fatal("mutating the returned timestamp changed the stored value")
	}
}

// TestConditionalAllowanceUpdatesRejectStaleVersions exercises the guarded updates
// of the allowance repository directly.
func TestConditionalAllowanceUpdatesRejectStaleVersions(t *testing.T) {
	h := newHarness(t)
	fixture := h.enrollMember(memberSpec{Email: "guard@heartbridge.test", Gender: "female"})
	repo := h.app.Repositories.Entitlements
	granted := h.allowance(fixture.MemberID)
	now := h.clk.Now()

	if err := repo.Reserve(h.ctx(), granted.ID, granted.Version, now); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// Replaying with the stale version must be refused.
	err := repo.Reserve(h.ctx(), granted.ID, granted.Version, now)
	if err == nil {
		t.Fatal("expected a stale version to be refused")
	}
	if code := apperr.CodeOf(err); code != apperr.CodeVersionConflict {
		t.Fatalf("expected code %s, got %s (%v)", apperr.CodeVersionConflict, code, err)
	}

	current := h.allowance(fixture.MemberID)
	if current.Reserved != 1 || current.Version != granted.Version+1 {
		t.Fatalf("unexpected allowance state: reserved=%d version=%d",
			current.Reserved, current.Version)
	}
	// Releasing more than is held is refused instead of driving the counter below
	// zero.
	if err := repo.Release(h.ctx(), current.ID, current.Version, now); err != nil {
		t.Fatalf("release: %v", err)
	}
	empty := h.allowance(fixture.MemberID)
	if empty.Reserved != 0 {
		t.Fatalf("expected reserved=0, got %d", empty.Reserved)
	}
	if err := repo.Release(h.ctx(), empty.ID, empty.Version, now); err == nil {
		t.Fatal("expected releasing an empty reservation to be refused")
	}
	// Consuming without a reservation is refused as well.
	if err := repo.Consume(h.ctx(), empty.ID, empty.Version, now); err == nil {
		t.Fatal("expected consuming without a reservation to be refused")
	}
	final := h.allowance(fixture.MemberID)
	if final.Used != 0 || final.Reserved != 0 {
		t.Fatalf("counters moved on a refused update: used=%d reserved=%d", final.Used, final.Reserved)
	}
}

// TestForeignKeyEnforcementIsActive verifies the pragma the schema relies on is
// really in effect.
func TestForeignKeyEnforcementIsActive(t *testing.T) {
	db := openScratchDatabase(t)
	if _, err := db.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, err := db.Conn(context.Background()).ExecContext(context.Background(),
		`INSERT INTO sessions (id, user_id, token_hash, issued_at, expires_at, revoked_at, last_seen_at)
         VALUES ('ses_orphan', 'usr_missing', 'hash', ?, ?, NULL, ?)`,
		sqlitedb.FormatTime(time.Now()), sqlitedb.FormatTime(time.Now()), sqlitedb.FormatTime(time.Now()))
	if err == nil {
		t.Fatal("expected the orphan session to be rejected by the foreign key")
	}
}
