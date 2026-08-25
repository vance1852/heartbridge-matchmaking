package sqliterepo

import (
	"context"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/member"
	"github.com/vance1852/heartbridge-matchmaking/internal/storage/sqlitedb"
)

// MemberRepository persists member profiles and preferences in SQLite.
type MemberRepository struct {
	db *sqlitedb.DB
}

// NewMemberRepository builds a MemberRepository.
func NewMemberRepository(db *sqlitedb.DB) *MemberRepository { return &MemberRepository{db: db} }

const memberColumns = `id, user_id, display_name, gender, birth_date, city, marital_status, education, status, created_at, updated_at`

// Create inserts a member profile.
func (r *MemberRepository) Create(ctx context.Context, profile member.Member) error {
	const statement = `
INSERT INTO members (id, user_id, display_name, gender, birth_date, city, marital_status, education, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		profile.ID,
		profile.UserID,
		profile.DisplayName,
		string(profile.Gender),
		sqlitedb.FormatTime(profile.BirthDate),
		profile.City,
		string(profile.MaritalStatus),
		string(profile.Education),
		string(profile.Status),
		sqlitedb.FormatTime(profile.CreatedAt),
		sqlitedb.FormatTime(profile.UpdatedAt),
	)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return apperr.Wrap(apperr.CodeConflict, "account already has a member profile", err)
		}
		return sqlitedb.TranslateError("insert member", err)
	}
	return nil
}

// GetByID loads a member profile by identifier.
func (r *MemberRepository) GetByID(ctx context.Context, id string) (member.Member, error) {
	const query = `SELECT ` + memberColumns + ` FROM members WHERE id = ?`
	return r.scanOne(ctx, query, id, id)
}

// GetByUserID loads the member profile owned by an account.
func (r *MemberRepository) GetByUserID(ctx context.Context, userID string) (member.Member, error) {
	const query = `SELECT ` + memberColumns + ` FROM members WHERE user_id = ?`
	return r.scanOne(ctx, query, userID, userID)
}

// scanOne shares the row decoding of both member lookups.
func (r *MemberRepository) scanOne(ctx context.Context, query, label string, arg any) (member.Member, error) {
	var (
		profile       member.Member
		gender        string
		birthDate     string
		maritalStatus string
		education     string
		status        string
		createdAt     string
		updatedAt     string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, arg).Scan(
		&profile.ID, &profile.UserID, &profile.DisplayName, &gender, &birthDate,
		&profile.City, &maritalStatus, &education, &status, &createdAt, &updatedAt,
	)
	if sqlitedb.IsNoRows(err) {
		return member.Member{}, notFound("member", label)
	}
	if err != nil {
		return member.Member{}, sqlitedb.TranslateError("select member", err)
	}
	profile.Gender = member.Gender(gender)
	profile.MaritalStatus = member.MaritalStatus(maritalStatus)
	profile.Education = member.Education(education)
	profile.Status = member.Status(status)
	if profile.BirthDate, err = sqlitedb.ParseTime(birthDate); err != nil {
		return member.Member{}, err
	}
	if profile.CreatedAt, err = sqlitedb.ParseTime(createdAt); err != nil {
		return member.Member{}, err
	}
	if profile.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
		return member.Member{}, err
	}
	return profile.Clone(), nil
}

// UpdateStatus moves a member profile to a new enrollment status.
func (r *MemberRepository) UpdateStatus(ctx context.Context, id string, status member.Status, at time.Time) error {
	if err := status.Validate(); err != nil {
		return err
	}
	const statement = `UPDATE members SET status = ?, updated_at = ? WHERE id = ?`
	result, err := r.db.Conn(ctx).ExecContext(ctx, statement, string(status), sqlitedb.FormatTime(at), id)
	if err != nil {
		return sqlitedb.TranslateError("update member status", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return sqlitedb.TranslateError("update member status rows", err)
	}
	if affected == 0 {
		return notFound("member", id)
	}
	return nil
}

// SavePreference inserts or replaces the partner criteria of a member.
func (r *MemberRepository) SavePreference(ctx context.Context, preference member.Preference) error {
	normalized := preference.Clone()
	normalized.Normalize()
	if err := normalized.Validate(); err != nil {
		return err
	}
	statuses := make([]string, 0, len(normalized.MaritalStatuses))
	for _, status := range normalized.MaritalStatuses {
		statuses = append(statuses, string(status))
	}
	const statement = `
INSERT INTO member_preferences (member_id, seeking_gender, min_age, max_age, cities, marital_statuses, min_education, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (member_id) DO UPDATE SET
    seeking_gender = excluded.seeking_gender,
    min_age = excluded.min_age,
    max_age = excluded.max_age,
    cities = excluded.cities,
    marital_statuses = excluded.marital_statuses,
    min_education = excluded.min_education,
    updated_at = excluded.updated_at`
	_, err := r.db.Conn(ctx).ExecContext(ctx, statement,
		normalized.MemberID,
		string(normalized.SeekingGender),
		normalized.MinAge,
		normalized.MaxAge,
		encodeList(normalized.Cities),
		encodeList(statuses),
		string(normalized.MinEducation),
		sqlitedb.FormatTime(normalized.UpdatedAt),
	)
	if err != nil {
		return sqlitedb.TranslateError("save member preference", err)
	}
	return nil
}

// GetPreference loads the partner criteria of a member. The returned value owns
// its slices, so a caller appending to Cities cannot corrupt anything shared.
func (r *MemberRepository) GetPreference(ctx context.Context, memberID string) (member.Preference, error) {
	const query = `
SELECT member_id, seeking_gender, min_age, max_age, cities, marital_statuses, min_education, updated_at
FROM member_preferences WHERE member_id = ?`
	var (
		preference    member.Preference
		seekingGender string
		cities        string
		statuses      string
		minEducation  string
		updatedAt     string
	)
	err := r.db.Conn(ctx).QueryRowContext(ctx, query, memberID).Scan(
		&preference.MemberID, &seekingGender, &preference.MinAge, &preference.MaxAge,
		&cities, &statuses, &minEducation, &updatedAt,
	)
	if sqlitedb.IsNoRows(err) {
		return member.Preference{}, apperr.Newf(apperr.CodePreconditionFailed,
			"member %s has not registered partner criteria yet", memberID)
	}
	if err != nil {
		return member.Preference{}, sqlitedb.TranslateError("select member preference", err)
	}
	preference.SeekingGender = member.Gender(seekingGender)
	preference.MinEducation = member.Education(minEducation)
	preference.Cities = decodeList(cities)
	for _, status := range decodeList(statuses) {
		preference.MaritalStatuses = append(preference.MaritalStatuses, member.MaritalStatus(status))
	}
	if preference.UpdatedAt, err = sqlitedb.ParseTime(updatedAt); err != nil {
		return member.Preference{}, err
	}
	return preference.Clone(), nil
}
