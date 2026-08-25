package member

import (
	"sort"
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
)

// Preference is the partner criteria a member registered. Cities and marital
// statuses are multi-valued, which makes value isolation on read a real concern.
type Preference struct {
	MemberID        string
	SeekingGender   Gender
	MinAge          int
	MaxAge          int
	Cities          []string
	MaritalStatuses []MaritalStatus
	MinEducation    Education
	UpdatedAt       time.Time
}

// Validate applies the structural rules of a preference record.
func (p Preference) Validate() error {
	if p.MemberID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "preference must reference a member")
	}
	if err := p.SeekingGender.Validate(); err != nil {
		return err
	}
	if p.MinAge < 18 {
		return apperr.New(apperr.CodeInvalidArgument, "minimum age must be at least 18")
	}
	if p.MaxAge < p.MinAge {
		return apperr.New(apperr.CodeInvalidArgument, "maximum age must not be lower than minimum age")
	}
	if p.MaxAge > 90 {
		return apperr.New(apperr.CodeInvalidArgument, "maximum age must not exceed 90")
	}
	if len(p.Cities) == 0 {
		return apperr.New(apperr.CodeInvalidArgument, "at least one acceptable city is required")
	}
	for _, city := range p.Cities {
		if strings.TrimSpace(city) == "" {
			return apperr.New(apperr.CodeInvalidArgument, "acceptable city must not be blank")
		}
	}
	if len(p.MaritalStatuses) == 0 {
		return apperr.New(apperr.CodeInvalidArgument, "at least one acceptable marital status is required")
	}
	for _, status := range p.MaritalStatuses {
		if err := status.Validate(); err != nil {
			return err
		}
	}
	return p.MinEducation.Validate()
}

// Normalize trims, lowercases and de-duplicates the multi-valued fields so that
// eligibility checks and persisted rows stay comparable.
func (p *Preference) Normalize() {
	seenCity := make(map[string]struct{}, len(p.Cities))
	cities := make([]string, 0, len(p.Cities))
	for _, city := range p.Cities {
		trimmed := strings.TrimSpace(city)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seenCity[key]; ok {
			continue
		}
		seenCity[key] = struct{}{}
		cities = append(cities, trimmed)
	}
	sort.Strings(cities)
	p.Cities = cities

	seenStatus := make(map[MaritalStatus]struct{}, len(p.MaritalStatuses))
	statuses := make([]MaritalStatus, 0, len(p.MaritalStatuses))
	for _, status := range p.MaritalStatuses {
		if _, ok := seenStatus[status]; ok {
			continue
		}
		seenStatus[status] = struct{}{}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i] < statuses[j] })
	p.MaritalStatuses = statuses
}

// AcceptsCity reports whether the preference accepts the given city.
func (p Preference) AcceptsCity(city string) bool {
	for _, candidate := range p.Cities {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(city)) {
			return true
		}
	}
	return false
}

// AcceptsMaritalStatus reports whether the preference accepts the given status.
func (p Preference) AcceptsMaritalStatus(status MaritalStatus) bool {
	for _, candidate := range p.MaritalStatuses {
		if candidate == status {
			return true
		}
	}
	return false
}

// Accepts evaluates the full criteria against a candidate profile at a given
// instant and returns the first unmet criterion as a business error.
func (p Preference) Accepts(candidate Member, at time.Time) error {
	if candidate.Gender != p.SeekingGender {
		return apperr.Newf(apperr.CodePreconditionFailed,
			"member %s seeks %s partners", p.MemberID, string(p.SeekingGender))
	}
	age := candidate.AgeAt(at)
	if age < p.MinAge || age > p.MaxAge {
		return apperr.Newf(apperr.CodePreconditionFailed,
			"candidate age %d is outside the accepted range %d-%d for member %s",
			age, p.MinAge, p.MaxAge, p.MemberID)
	}
	if !p.AcceptsCity(candidate.City) {
		return apperr.Newf(apperr.CodePreconditionFailed,
			"city %s is not accepted by member %s", candidate.City, p.MemberID)
	}
	if !p.AcceptsMaritalStatus(candidate.MaritalStatus) {
		return apperr.Newf(apperr.CodePreconditionFailed,
			"marital status %s is not accepted by member %s", string(candidate.MaritalStatus), p.MemberID)
	}
	if !candidate.Education.AtLeast(p.MinEducation) {
		return apperr.Newf(apperr.CodePreconditionFailed,
			"education %s is below the minimum %s required by member %s",
			string(candidate.Education), string(p.MinEducation), p.MemberID)
	}
	return nil
}

// Clone returns a deep copy. Repositories must return clones so that a caller
// appending to Cities cannot corrupt cached or shared state.
func (p Preference) Clone() Preference {
	copied := p
	if p.Cities != nil {
		copied.Cities = make([]string, len(p.Cities))
		copy(copied.Cities, p.Cities)
	}
	if p.MaritalStatuses != nil {
		copied.MaritalStatuses = make([]MaritalStatus, len(p.MaritalStatuses))
		copy(copied.MaritalStatuses, p.MaritalStatuses)
	}
	return copied
}

// MutuallyEligible checks both directions of a candidate pair. Matchmaking is
// only allowed when each side accepts the other.
func MutuallyEligible(left Member, leftPreference Preference, right Member, rightPreference Preference, at time.Time) error {
	if left.ID == right.ID {
		return apperr.New(apperr.CodeInvalidArgument, "a member cannot be matched with themselves")
	}
	if err := left.Matchable(); err != nil {
		return err
	}
	if err := right.Matchable(); err != nil {
		return err
	}
	if err := leftPreference.Accepts(right, at); err != nil {
		return err
	}
	return rightPreference.Accepts(left, at)
}
