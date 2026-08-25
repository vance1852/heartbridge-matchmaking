package sqliterepo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/storage/sqlitedb"
)

// MatchRepository persists matches and their consent rows.
type MatchRepository struct {
	db *sqlitedb.DB
}

// NewMatchRepository builds a MatchRepository.
func NewMatchRepository(db *sqlitedb.DB) *MatchRepository { return &MatchRepository{db: db} }

const matchColumns = `id, matchmaker_id, member_a_id, member_b_id, state, version, consent_deadline, closing_note, created_at, updated_at, closed_at`

// Create inserts a match together with the consent row of every participant.
// Both writes belong to one unit of work: a match without its consent rows would
// be unanswerable, so the caller's transaction must cover them together.
func (r *MatchRepository) Create(ctx context.Context, match matching.Match, consents []matching.Consent) error {
	if err := match.Validate(); err != nil {
		return err
	}
	if len(consents) != 2 {
		return apperr.New(apperr.CodeInvalidArgument, "a match requires exactly two consent rows")
	}
	conn := r.db.Conn(ctx)
	const statement = `
INSERT INTO matches (id, matchmaker_id, member_a_id, member_b_id, pair_key, state, version, consent_deadline, closing_note, created_at, updated_at, closed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := conn.ExecContext(ctx, statement,
		match.ID, match.MatchmakerID, match.MemberAID, match.MemberBID,
		matching.PairKey(match.MemberAID, match.MemberBID),
		string(match.State), match.Version,
		sqlitedb.FormatTime(match.ConsentDeadline), match.ClosingNote,
		sqlitedb.FormatTime(match.CreatedAt), sqlitedb.FormatTime(match.UpdatedAt),
		sqlitedb.FormatNullableTime(match.ClosedAt),
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict,
				"these two members already have a live introduction", err)
		}
		return sqlitedb.TranslateError("insert match", err)
	}

	const consentStatement = `
INSERT INTO match_consents (match_id, member_id, decision, decided_at, updated_at)
VALUES (?, ?, ?, ?, ?)`
	for _, consent := range consents {
		if err := consent.Decision.Validate(); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, consentStatement,
			consent.MatchID, consent.MemberID, string(consent.Decision),
			sqlitedb.FormatNullableTime(consent.DecidedAt), sqlitedb.FormatTime(consent.UpdatedAt),
		); err != nil {
			return sqlitedb.TranslateError("insert match consent", err)
		}
	}
	return nil
}

// GetByID loads a match by identifier.
func (r *MatchRepository) GetByID(ctx context.Context, id string) (matching.Match, error) {
	const query = `SELECT ` + matchColumns + ` FROM matches WHERE id = ?`
	var (
		match           matching.Match
		state           string
		consentDeadline string
		createdAt       string
		updatedAt       string
		closedAt        sql.NullString
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, id).Scan(
		&match.ID, &match.MatchmakerID, &match.MemberAID, &match.MemberBID,
		&state, &match.Version, &consentDeadline, &match.ClosingNote,
		&createdAt, &updatedAt, &closedAt,
	)
	if sqlitedb.IsNoRows(err) {
		return matching.Match{}, notFound("match", id)
	}
	if err != nil {
		return matching.Match{}, sqlitedb.TranslateError("select match", err)
	}
	match.State = matching.State(state)
	if match.ConsentDeadline, err = sqlitedb.ParseTime(consentDeadline); err != nil {
		return matching.Match{}, err
	}
	if match.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
		return matching.Match{}, err
	}
	if match.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
		return matching.Match{}, err
	}
	if match.ClosedAt, err = sqlitedb.ParseNullableTime(closedAt); err != nil {
		return matching.Match{}, err
	}
	return match.Clone(), nil
}

// UpdateState performs the optimistic state transition. The expected version is
// part of the WHERE clause, so a lost race updates zero rows and is reported as a
// version conflict instead of silently overwriting a concurrent decision.
func (r *MatchRepository) UpdateState(
	ctx context.Context, id string, expectedVersion int64, target matching.State, note string, at time.Time,
) error {
	if err := target.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE matches
SET state = ?,
    version = version + 1,
    closing_note = ?,
    updated_at = ?,
    closed_at = CASE WHEN ? THEN ? ELSE closed_at END
WHERE id = ? AND version = ?`
	stamp := sqlitedb.FormatTime(at)
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		string(target), note, stamp, target.Terminal(), stamp, id, expectedVersion,
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict,
				"another live introduction already exists for this pair", err)
		}
		return sqlitedb.TranslateError("update match state", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return sqlitedb.TranslateError("update match state rows", err)
	}
	if affected == 0 {
		if _, lookupErr := r.GetByID(ctx, id); lookupErr != nil {
			return lookupErr
		}
		return apperr.Newf(apperr.CodeVersionConflict,
			"match %s was modified concurrently", id)
	}
	return nil
}

// CountActiveByMember counts the live introductions a member takes part in.
func (r *MatchRepository) CountActiveByMember(ctx context.Context, memberID string) (int, error) {
	placeholders, args := stateArgs(matching.ActiveStates())
	query := fmt.Sprintf(`
SELECT COUNT(*) FROM matches
WHERE (member_a_id = ? OR member_b_id = ?) AND state IN (%s)`, placeholders)
	full := append([]any{memberID, memberID}, args...)
	var total int
	if err := r.db.Conn(ctx).QueryRowContext(ctx, query, full...).Scan(&total); err != nil {
		return 0, sqlitedb.TranslateError("count active matches", err)
	}
	return total, nil
}

// ActivePairExists reports whether the two members already have a live
// introduction, independently of the argument order.
func (r *MatchRepository) ActivePairExists(ctx context.Context, leftMemberID, rightMemberID string) (bool, error) {
	placeholders, args := stateArgs(matching.ActiveStates())
	query := fmt.Sprintf(`
SELECT COUNT(*) FROM matches WHERE pair_key = ? AND state IN (%s)`, placeholders)
	full := append([]any{matching.PairKey(leftMemberID, rightMemberID)}, args...)
	var total int
	if err := r.db.Conn(ctx).QueryRowContext(ctx, query, full...).Scan(&total); err != nil {
		return false, sqlitedb.TranslateError("check active pair", err)
	}
	return total > 0, nil
}

// List returns one page of matches together with the total under the very same
// filter. Both statements are built from a single predicate builder so the page
// and the count can never drift apart.
func (r *MatchRepository) List(ctx context.Context, filter repository.MatchFilter) (repository.MatchPage, error) {
	if err := filter.Validate(); err != nil {
		return repository.MatchPage{}, err
	}
	normalized := filter.Normalize()
	predicate, args := buildMatchPredicate(normalized)

	countPredicate, countArgs := buildMatchPredicate(normalized.CountScope())
	countQuery := `SELECT COUNT(*) FROM matches` + countPredicate
	var total int
	if err := r.db.Conn(ctx).QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return repository.MatchPage{}, sqlitedb.TranslateError("count matches", err)
	}

	direction := "ASC"
	if normalized.SortDir == repository.SortDescending {
		direction = "DESC"
	}
	pageQuery := fmt.Sprintf(`SELECT %s FROM matches%s ORDER BY %s %s, id %s LIMIT ? OFFSET ?`,
		matchColumns, predicate, string(normalized.SortField), direction, direction)
	pageArgs := append(append([]any{}, args...), normalized.Page.Limit, normalized.Page.Offset)

	rows, err := r.db.Conn(ctx).QueryContext(ctx, pageQuery, pageArgs...)
	if err != nil {
		return repository.MatchPage{}, sqlitedb.TranslateError("list matches", err)
	}
	defer rows.Close()

	items := make([]matching.Match, 0, normalized.Page.Limit)
	for rows.Next() {
		var (
			match           matching.Match
			state           string
			consentDeadline string
			createdAt       string
			updatedAt       string
			closedAt        sql.NullString
		)
		if err := rows.Scan(
			&match.ID, &match.MatchmakerID, &match.MemberAID, &match.MemberBID,
			&state, &match.Version, &consentDeadline, &match.ClosingNote,
			&createdAt, &updatedAt, &closedAt,
		); err != nil {
			return repository.MatchPage{}, sqlitedb.TranslateError("scan match", err)
		}
		match.State = matching.State(state)
		if match.ConsentDeadline, err = sqlitedb.ParseTime(consentDeadline); err != nil {
			return repository.MatchPage{}, err
		}
		if match.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
			return repository.MatchPage{}, err
		}
		if match.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
			return repository.MatchPage{}, err
		}
		if match.ClosedAt, err = sqlitedb.ParseNullableTime(closedAt); err != nil {
			return repository.MatchPage{}, err
		}
		items = append(items, match.Clone())
	}
	if err := rows.Err(); err != nil {
		return repository.MatchPage{}, sqlitedb.TranslateError("iterate matches", err)
	}
	return repository.MatchPage{
		Items:  items,
		Total:  total,
		Limit:  normalized.Page.Limit,
		Offset: normalized.Page.Offset,
	}, nil
}

// buildMatchPredicate renders the shared WHERE clause of the list and the count.
func buildMatchPredicate(filter repository.MatchFilter) (string, []any) {
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 8)
	if len(filter.States) > 0 {
		placeholders, stateValues := stateArgs(filter.States)
		clauses = append(clauses, "state IN ("+placeholders+")")
		args = append(args, stateValues...)
	}
	if filter.MemberID != "" {
		clauses = append(clauses, "(member_a_id = ? OR member_b_id = ?)")
		args = append(args, filter.MemberID, filter.MemberID)
	}
	if filter.MatchmakerID != "" {
		clauses = append(clauses, "matchmaker_id = ?")
		args = append(args, filter.MatchmakerID)
	}
	if filter.CreatedFrom != nil {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, sqlitedb.FormatTime(*filter.CreatedFrom))
	}
	if filter.CreatedTo != nil {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, sqlitedb.FormatTime(*filter.CreatedTo))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// stateArgs renders a placeholder list for an IN clause over match states.
func stateArgs(states []matching.State) (string, []any) {
	placeholders := make([]string, 0, len(states))
	args := make([]any, 0, len(states))
	for _, state := range states {
		placeholders = append(placeholders, "?")
		args = append(args, string(state))
	}
	return strings.Join(placeholders, ", "), args
}

// ListConsents returns both consent rows of a match in a stable order.
func (r *MatchRepository) ListConsents(ctx context.Context, matchID string) ([]matching.Consent, error) {
	const query = `
SELECT match_id, member_id, decision, decided_at, updated_at
FROM match_consents WHERE match_id = ? ORDER BY member_id`
	rows, err := r.db.Conn(ctx).QueryContext(ctx, query, matchID)
	if err != nil {
		return nil, sqlitedb.TranslateError("list match consents", err)
	}
	defer rows.Close()

	consents := make([]matching.Consent, 0, 2)
	for rows.Next() {
		var (
			consent   matching.Consent
			decision  string
			decidedAt sql.NullString
			updatedAt string
		)
		if err := rows.Scan(&consent.MatchID, &consent.MemberID, &decision, &decidedAt, &updatedAt); err != nil {
			return nil, sqlitedb.TranslateError("scan match consent", err)
		}
		consent.Decision = matching.Decision(decision)
		if consent.DecidedAt, err = sqlitedb.ParseNullableTime(decidedAt); err != nil {
			return nil, err
		}
		if consent.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
			return nil, err
		}
		consents = append(consents, consent.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, sqlitedb.TranslateError("iterate match consents", err)
	}
	return consents, nil
}

// RecordDecision stores a member answer. Only a still pending row is updated, so
// a second answer from the same member cannot overwrite the first one.
func (r *MatchRepository) RecordDecision(
	ctx context.Context, matchID, memberID string, decision matching.Decision, at time.Time,
) error {
	if err := decision.Answerable(); err != nil {
		return err
	}
	const statement = `
UPDATE match_consents
SET decision = ?, decided_at = ?, updated_at = ?
WHERE match_id = ? AND member_id = ? AND decision = 'pending'`
	stamp := sqlitedb.FormatTime(at)
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		string(decision), stamp, stamp, matchID, memberID)
	if err != nil {
		return sqlitedb.TranslateError("record consent decision", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return sqlitedb.TranslateError("record consent decision rows", err)
	}
	if affected == 0 {
		existing, lookupErr := r.ListConsents(ctx, matchID)
		if lookupErr != nil {
			return lookupErr
		}
		current, findErr := matching.FindConsent(existing, memberID)
		if findErr != nil {
			return findErr
		}
		return apperr.Newf(apperr.CodeConflict,
			"member %s already answered this introduction with %s", memberID, string(current.Decision))
	}
	return nil
}

// ListExpiredPendingConsent returns matches whose consent window elapsed. It is
// the input of the expiry sweeper.
func (r *MatchRepository) ListExpiredPendingConsent(
	ctx context.Context, cutoff time.Time, limit int,
) ([]matching.Match, error) {
	if limit <= 0 {
		limit = repository.DefaultPageSize
	}
	query := `SELECT ` + matchColumns + ` FROM matches
WHERE state = 'pending_consent' AND consent_deadline <= ?
ORDER BY consent_deadline LIMIT ?`
	rows, err := r.db.Conn(ctx).QueryContext(ctx, query, sqlitedb.FormatTime(cutoff), limit)
	if err != nil {
		return nil, sqlitedb.TranslateError("list expired matches", err)
	}
	defer rows.Close()

	matches := make([]matching.Match, 0, limit)
	for rows.Next() {
		var (
			match           matching.Match
			state           string
			consentDeadline string
			createdAt       string
			updatedAt       string
			closedAt        sql.NullString
		)
		if err := rows.Scan(
			&match.ID, &match.MatchmakerID, &match.MemberAID, &match.MemberBID,
			&state, &match.Version, &consentDeadline, &match.ClosingNote,
			&createdAt, &updatedAt, &closedAt,
		); err != nil {
			return nil, sqlitedb.TranslateError("scan expired match", err)
		}
		match.State = matching.State(state)
		if match.ConsentDeadline, err = sqlitedb.ParseTime(consentDeadline); err != nil {
			return nil, err
		}
		if match.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
			return nil, err
		}
		if match.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
			return nil, err
		}
		if match.ClosedAt, err = sqlitedb.ParseNullableTime(closedAt); err != nil {
			return nil, err
		}
		matches = append(matches, match.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, sqlitedb.TranslateError("iterate expired matches", err)
	}
	return matches, nil
}
