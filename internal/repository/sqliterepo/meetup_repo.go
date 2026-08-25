package sqliterepo

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/storage/sqlitedb"
)

// SlotRepository persists venue slots and their scarce capacity.
type SlotRepository struct {
	db *sqlitedb.DB
}

// NewSlotRepository builds a SlotRepository.
func NewSlotRepository(db *sqlitedb.DB) *SlotRepository { return &SlotRepository{db: db} }

const slotColumns = `id, venue_code, venue_name, city, start_at, end_at, capacity, booked_count, version, created_at, updated_at`

// Create publishes a bookable venue slot.
func (r *SlotRepository) Create(ctx context.Context, slot meetup.VenueSlot) error {
	if err := slot.Validate(); err != nil {
		return err
	}
	const statement = `
INSERT INTO venue_slots (id, venue_code, venue_name, city, start_at, end_at, capacity, booked_count, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		slot.ID, slot.VenueCode, slot.VenueName, slot.City,
		sqlitedb.FormatTime(slot.StartAt), sqlitedb.FormatTime(slot.EndAt),
		slot.Capacity, slot.BookedCount, slot.Version,
		sqlitedb.FormatTime(slot.CreatedAt), sqlitedb.FormatTime(slot.UpdatedAt),
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict,
				"this venue already has a slot starting at that time", err)
		}
		return sqlitedb.TranslateError("insert venue slot", err)
	}
	return nil
}

// GetByID loads one venue slot.
func (r *SlotRepository) GetByID(ctx context.Context, id string) (meetup.VenueSlot, error) {
	const query = `SELECT ` + slotColumns + ` FROM venue_slots WHERE id = ?`
	var (
		slot      meetup.VenueSlot
		startAt   string
		endAt     string
		createdAt string
		updatedAt string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, id).Scan(
		&slot.ID, &slot.VenueCode, &slot.VenueName, &slot.City,
		&startAt, &endAt, &slot.Capacity, &slot.BookedCount, &slot.Version,
		&createdAt, &updatedAt,
	)
	if sqlitedb.IsNoRows(err) {
		return meetup.VenueSlot{}, notFound("venue slot", id)
	}
	if err != nil {
		return meetup.VenueSlot{}, sqlitedb.TranslateError("select venue slot", err)
	}
	if slot.StartAt, err = sqlitedb.ParseTime(startAt); err != nil {
		return meetup.VenueSlot{}, err
	}
	if slot.EndAt, err = sqlitedb.ParseTime(endAt); err != nil {
		return meetup.VenueSlot{}, err
	}
	if slot.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
		return meetup.VenueSlot{}, err
	}
	if slot.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
		return meetup.VenueSlot{}, err
	}
	return slot.Clone(), nil
}

// ListBetween returns the slots starting inside the window, optionally filtered
// by city.
func (r *SlotRepository) ListBetween(
	ctx context.Context, from, to time.Time, city string, page repository.Page,
) ([]meetup.VenueSlot, error) {
	if err := page.Validate(); err != nil {
		return nil, err
	}
	normalized := page.Normalize()
	clauses := []string{"start_at >= ?", "start_at < ?"}
	args := []any{sqlitedb.FormatTime(from), sqlitedb.FormatTime(to)}
	if strings.TrimSpace(city) != "" {
		clauses = append(clauses, "city = ?")
		args = append(args, city)
	}
	query := `SELECT ` + slotColumns + ` FROM venue_slots WHERE ` +
		strings.Join(clauses, " AND ") + ` ORDER BY start_at, id LIMIT ? OFFSET ?`
	args = append(args, normalized.Limit, normalized.Offset)

	rows, err := r.db.Conn(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlitedb.TranslateError("list venue slots", err)
	}
	defer rows.Close()

	slots := make([]meetup.VenueSlot, 0, normalized.Limit)
	for rows.Next() {
		var (
			slot      meetup.VenueSlot
			startAt   string
			endAt     string
			createdAt string
			updatedAt string
		)
		if err := rows.Scan(&slot.ID, &slot.VenueCode, &slot.VenueName, &slot.City,
			&startAt, &endAt, &slot.Capacity, &slot.BookedCount, &slot.Version,
			&createdAt, &updatedAt); err != nil {
			return nil, sqlitedb.TranslateError("scan venue slot", err)
		}
		if slot.StartAt, err = sqlitedb.ParseTime(startAt); err != nil {
			return nil, err
		}
		if slot.EndAt, err = sqlitedb.ParseTime(endAt); err != nil {
			return nil, err
		}
		if slot.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
			return nil, err
		}
		if slot.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
			return nil, err
		}
		slots = append(slots, slot.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, sqlitedb.TranslateError("iterate venue slots", err)
	}
	return slots, nil
}

// TryBook takes one seat of a slot. The remaining capacity is checked by the
// database inside the same statement that increments the counter, which is what
// makes concurrent bookings safe without a service level lock.
func (r *SlotRepository) TryBook(ctx context.Context, id string, at time.Time) error {
	const statement = `
UPDATE venue_slots
SET booked_count = booked_count + 1,
    version = version + 1,
    updated_at = ?
WHERE id = ? AND booked_count < capacity`
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement, sqlitedb.FormatTime(at), id)
	if err != nil {
		return sqlitedb.TranslateError("book venue slot", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return sqlitedb.TranslateError("book venue slot rows", err)
	}
	if affected == 0 {
		if _, lookupErr := r.GetByID(ctx, id); lookupErr != nil {
			return lookupErr
		}
		return apperr.Newf(apperr.CodeCapacityExhausted, "venue slot %s is fully booked", id)
	}
	return nil
}

// ReleaseBooking returns one seat to a slot.
func (r *SlotRepository) ReleaseBooking(ctx context.Context, id string, at time.Time) error {
	const statement = `
UPDATE venue_slots
SET booked_count = booked_count - 1,
    version = version + 1,
    updated_at = ?
WHERE id = ? AND booked_count > 0`
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement, sqlitedb.FormatTime(at), id)
	if err != nil {
		return sqlitedb.TranslateError("release venue slot", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return sqlitedb.TranslateError("release venue slot rows", err)
	}
	if affected == 0 {
		if _, lookupErr := r.GetByID(ctx, id); lookupErr != nil {
			return lookupErr
		}
		return apperr.Newf(apperr.CodeConflict, "venue slot %s has no booking to release", id)
	}
	return nil
}

// MeetupRepository persists meetups and their feedback rows.
type MeetupRepository struct {
	db *sqlitedb.DB
}

// NewMeetupRepository builds a MeetupRepository.
func NewMeetupRepository(db *sqlitedb.DB) *MeetupRepository { return &MeetupRepository{db: db} }

const meetupColumns = `id, match_id, slot_id, state, version, booked_at, checked_in_at, completed_at, closed_at, created_at, updated_at`

// Create inserts a meetup for a match.
func (r *MeetupRepository) Create(ctx context.Context, appointment meetup.Meetup) error {
	if err := appointment.Validate(); err != nil {
		return err
	}
	const statement = `
INSERT INTO meetups (id, match_id, slot_id, state, version, booked_at, checked_in_at, completed_at, closed_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		appointment.ID, appointment.MatchID, appointment.SlotID,
		string(appointment.State), appointment.Version,
		sqlitedb.FormatTime(appointment.BookedAt),
		sqlitedb.FormatNullableTime(appointment.CheckedInAt),
		sqlitedb.FormatNullableTime(appointment.CompletedAt),
		sqlitedb.FormatNullableTime(appointment.ClosedAt),
		sqlitedb.FormatTime(appointment.CreatedAt),
		sqlitedb.FormatTime(appointment.UpdatedAt),
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict,
				"this introduction already has a live meetup", err)
		}
		return sqlitedb.TranslateError("insert meetup", err)
	}
	return nil
}

// GetByID loads one meetup.
func (r *MeetupRepository) GetByID(ctx context.Context, id string) (meetup.Meetup, error) {
	const query = `SELECT ` + meetupColumns + ` FROM meetups WHERE id = ?`
	return r.scanOne(ctx, query, "meetup", id, id)
}

// GetActiveByMatch loads the live meetup of a match.
func (r *MeetupRepository) GetActiveByMatch(ctx context.Context, matchID string) (meetup.Meetup, error) {
	const query = `SELECT ` + meetupColumns + `
FROM meetups WHERE match_id = ? AND state IN ('booked', 'checked_in', 'completed')`
	return r.scanOne(ctx, query, "meetup for match", matchID, matchID)
}

// scanOne shares the row decoding of both meetup lookups.
func (r *MeetupRepository) scanOne(ctx context.Context, query, entity, label string, arg any) (meetup.Meetup, error) {
	var (
		appointment meetup.Meetup
		state       string
		bookedAt    string
		checkedInAt sql.NullString
		completedAt sql.NullString
		closedAt    sql.NullString
		createdAt   string
		updatedAt   string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, arg).Scan(
		&appointment.ID, &appointment.MatchID, &appointment.SlotID, &state, &appointment.Version,
		&bookedAt, &checkedInAt, &completedAt, &closedAt, &createdAt, &updatedAt,
	)
	if sqlitedb.IsNoRows(err) {
		return meetup.Meetup{}, notFound(entity, label)
	}
	if err != nil {
		return meetup.Meetup{}, sqlitedb.TranslateError("select meetup", err)
	}
	appointment.State = meetup.State(state)
	if appointment.BookedAt, err = sqlitedb.ParseTime(bookedAt); err != nil {
		return meetup.Meetup{}, err
	}
	if appointment.CheckedInAt, err = sqlitedb.ParseNullableTime(checkedInAt); err != nil {
		return meetup.Meetup{}, err
	}
	if appointment.CompletedAt, err = sqlitedb.ParseNullableTime(completedAt); err != nil {
		return meetup.Meetup{}, err
	}
	if appointment.ClosedAt, err = sqlitedb.ParseNullableTime(closedAt); err != nil {
		return meetup.Meetup{}, err
	}
	if appointment.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
		return meetup.Meetup{}, err
	}
	if appointment.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
		return meetup.Meetup{}, err
	}
	return appointment.Clone(), nil
}

// UpdateState performs the optimistic meetup transition and stamps the matching
// lifecycle timestamp for the target state.
func (r *MeetupRepository) UpdateState(
	ctx context.Context, id string, expectedVersion int64, target meetup.State, at time.Time,
) error {
	if err := target.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE meetups
SET state = ?,
    version = version + 1,
    checked_in_at = CASE WHEN ? THEN ? ELSE checked_in_at END,
    completed_at = CASE WHEN ? THEN ? ELSE completed_at END,
    closed_at = CASE WHEN ? THEN ? ELSE closed_at END,
    updated_at = ?
WHERE id = ? AND version = ?`
	stamp := sqlitedb.FormatTime(at)
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		string(target),
		target == meetup.StateCheckedIn, stamp,
		target == meetup.StateCompleted, stamp,
		target.Terminal(), stamp,
		stamp, id, expectedVersion,
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict, "another live meetup already exists for this match", err)
		}
		return sqlitedb.TranslateError("update meetup state", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return sqlitedb.TranslateError("update meetup state rows", err)
	}
	if affected == 0 {
		if _, lookupErr := r.GetByID(ctx, id); lookupErr != nil {
			return lookupErr
		}
		return apperr.Newf(apperr.CodeVersionConflict, "meetup %s was modified concurrently", id)
	}
	return nil
}

// ListBookingsForMember returns the live bookings of a member inside a window,
// joined with the slot window so that overlap detection needs one query.
func (r *MeetupRepository) ListBookingsForMember(
	ctx context.Context, memberID string, from, to time.Time,
) ([]meetup.Booking, error) {
	const query = `
SELECT m.id, m.match_id, m.slot_id, m.state, m.version, m.booked_at,
       m.checked_in_at, m.completed_at, m.closed_at, m.created_at, m.updated_at,
       s.start_at, s.end_at
FROM meetups m
JOIN venue_slots s ON s.id = m.slot_id
JOIN matches mt ON mt.id = m.match_id
WHERE m.state IN ('booked', 'checked_in', 'completed')
  AND (mt.member_a_id = ? OR mt.member_b_id = ?)
  AND s.end_at > ?
  AND s.start_at < ?
ORDER BY s.start_at`
	rows, err := r.db.Conn(ctx).QueryContext(ctx, query,
		memberID, memberID, sqlitedb.FormatTime(from), sqlitedb.FormatTime(to))
	if err != nil {
		return nil, sqlitedb.TranslateError("list member bookings", err)
	}
	defer rows.Close()

	bookings := make([]meetup.Booking, 0, 4)
	for rows.Next() {
		var (
			booking     meetup.Booking
			state       string
			bookedAt    string
			checkedInAt sql.NullString
			completedAt sql.NullString
			closedAt    sql.NullString
			createdAt   string
			updatedAt   string
			startAt     string
			endAt       string
		)
		if err := rows.Scan(
			&booking.Meetup.ID, &booking.Meetup.MatchID, &booking.Meetup.SlotID, &state,
			&booking.Meetup.Version, &bookedAt, &checkedInAt, &completedAt, &closedAt,
			&createdAt, &updatedAt, &startAt, &endAt,
		); err != nil {
			return nil, sqlitedb.TranslateError("scan member booking", err)
		}
		booking.Meetup.State = meetup.State(state)
		if booking.Meetup.BookedAt, err = sqlitedb.ParseTime(bookedAt); err != nil {
			return nil, err
		}
		if booking.Meetup.CheckedInAt, err = sqlitedb.ParseNullableTime(checkedInAt); err != nil {
			return nil, err
		}
		if booking.Meetup.CompletedAt, err = sqlitedb.ParseNullableTime(completedAt); err != nil {
			return nil, err
		}
		if booking.Meetup.ClosedAt, err = sqlitedb.ParseNullableTime(closedAt); err != nil {
			return nil, err
		}
		if booking.Meetup.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
			return nil, err
		}
		if booking.Meetup.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
			return nil, err
		}
		if booking.StartAt, err = sqlitedb.ParseTime(startAt); err != nil {
			return nil, err
		}
		if booking.EndAt, err = sqlitedb.ParseTime(endAt); err != nil {
			return nil, err
		}
		bookings = append(bookings, booking.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, sqlitedb.TranslateError("iterate member bookings", err)
	}
	return bookings, nil
}

// AddFeedback stores one participant report, mapping the unique constraint onto a
// conflict so that a second submission cannot overwrite the first.
func (r *MeetupRepository) AddFeedback(ctx context.Context, feedback meetup.Feedback) error {
	if err := feedback.Validate(); err != nil {
		return err
	}
	const statement = `
INSERT INTO meetup_feedbacks (id, meetup_id, author_member_id, intent, rating, comment, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		feedback.ID, feedback.MeetupID, feedback.AuthorMemberID,
		string(feedback.Intent), feedback.Rating, feedback.Comment,
		sqlitedb.FormatTime(feedback.CreatedAt),
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict,
				"this member already reported on the meetup", err)
		}
		return sqlitedb.TranslateError("insert meetup feedback", err)
	}
	return nil
}

// ListFeedback returns the reports of a meetup in submission order.
func (r *MeetupRepository) ListFeedback(ctx context.Context, meetupID string) ([]meetup.Feedback, error) {
	const query = `
SELECT id, meetup_id, author_member_id, intent, rating, comment, created_at
FROM meetup_feedbacks WHERE meetup_id = ? ORDER BY created_at, id`
	rows, err := r.db.Conn(ctx).QueryContext(ctx, query, meetupID)
	if err != nil {
		return nil, sqlitedb.TranslateError("list meetup feedback", err)
	}
	defer rows.Close()

	entries := make([]meetup.Feedback, 0, 2)
	for rows.Next() {
		var (
			feedback  meetup.Feedback
			intent    string
			createdAt string
		)
		if err := rows.Scan(&feedback.ID, &feedback.MeetupID, &feedback.AuthorMemberID,
			&intent, &feedback.Rating, &feedback.Comment, &createdAt); err != nil {
			return nil, sqlitedb.TranslateError("scan meetup feedback", err)
		}
		feedback.Intent = meetup.Intent(intent)
		if feedback.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
			return nil, err
		}
		entries = append(entries, feedback.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, sqlitedb.TranslateError("iterate meetup feedback", err)
	}
	return entries, nil
}
