// Package member models the singles enrolled with the matchmaking agency and the
// partner criteria they registered.
package member

import (
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
)

// Gender is the self-declared gender of a member.
type Gender string

const (
	// GenderMale is a male member.
	GenderMale Gender = "male"
	// GenderFemale is a female member.
	GenderFemale Gender = "female"
)

// Validate rejects unknown gender values.
func (g Gender) Validate() error {
	switch g {
	case GenderMale, GenderFemale:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown gender %q", string(g))
	}
}

// MaritalStatus is the marital history a member declares at enrollment.
type MaritalStatus string

const (
	// MaritalSingle never married.
	MaritalSingle MaritalStatus = "single"
	// MaritalDivorced previously married.
	MaritalDivorced MaritalStatus = "divorced"
	// MaritalWidowed lost a spouse.
	MaritalWidowed MaritalStatus = "widowed"
)

// Validate rejects unknown marital statuses.
func (m MaritalStatus) Validate() error {
	switch m {
	case MaritalSingle, MaritalDivorced, MaritalWidowed:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown marital status %q", string(m))
	}
}

// Education is an ordered education level. The ordering is business data: a
// preference expresses a minimum acceptable level.
type Education string

const (
	// EducationHighSchool is secondary education.
	EducationHighSchool Education = "high_school"
	// EducationCollege is a three-year college diploma.
	EducationCollege Education = "college"
	// EducationBachelor is a four-year degree.
	EducationBachelor Education = "bachelor"
	// EducationMaster is a master's degree.
	EducationMaster Education = "master"
	// EducationDoctor is a doctorate.
	EducationDoctor Education = "doctor"
)

var educationRank = map[Education]int{
	EducationHighSchool: 1,
	EducationCollege:    2,
	EducationBachelor:   3,
	EducationMaster:     4,
	EducationDoctor:     5,
}

// Validate rejects unknown education levels.
func (e Education) Validate() error {
	if _, ok := educationRank[e]; !ok {
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown education level %q", string(e))
	}
	return nil
}

// AtLeast reports whether the level reaches the given minimum.
func (e Education) AtLeast(minimum Education) bool {
	return educationRank[e] >= educationRank[minimum]
}

// Status is the enrollment lifecycle of a member profile.
type Status string

const (
	// StatusOnboarding profiles are incomplete and cannot be matched yet.
	StatusOnboarding Status = "onboarding"
	// StatusActive profiles participate in matching.
	StatusActive Status = "active"
	// StatusPaused profiles keep their data but receive no introductions.
	StatusPaused Status = "paused"
	// StatusRetired profiles left the service.
	StatusRetired Status = "retired"
)

// Validate rejects unknown member statuses.
func (s Status) Validate() error {
	switch s {
	case StatusOnboarding, StatusActive, StatusPaused, StatusRetired:
		return nil
	default:
		return apperr.Newf(apperr.CodeInvalidArgument, "unknown member status %q", string(s))
	}
}

// Member is an enrolled single.
type Member struct {
	ID            string
	UserID        string
	DisplayName   string
	Gender        Gender
	BirthDate     time.Time
	City          string
	MaritalStatus MaritalStatus
	Education     Education
	Status        Status
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AgeAt returns the member age in completed years, evaluated in the business
// timezone so that a birthday is not off by one for late-evening requests.
func (m Member) AgeAt(instant time.Time) int {
	location := clock.BusinessLocation()
	born := m.BirthDate.In(location)
	now := instant.In(location)
	age := now.Year() - born.Year()
	if now.Month() < born.Month() || (now.Month() == born.Month() && now.Day() < born.Day()) {
		age--
	}
	if age < 0 {
		return 0
	}
	return age
}

// Validate applies the structural rules of an enrolled profile.
func (m Member) Validate() error {
	if strings.TrimSpace(m.DisplayName) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "display name is required")
	}
	if m.BirthDate.IsZero() {
		return apperr.New(apperr.CodeInvalidArgument, "birth date is required")
	}
	if strings.TrimSpace(m.City) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "city is required")
	}
	if err := m.Gender.Validate(); err != nil {
		return err
	}
	if err := m.MaritalStatus.Validate(); err != nil {
		return err
	}
	if err := m.Education.Validate(); err != nil {
		return err
	}
	return m.Status.Validate()
}

// Matchable reports whether the profile may take part in a new match.
func (m Member) Matchable() error {
	if m.Status != StatusActive {
		return apperr.Newf(apperr.CodePreconditionFailed,
			"member %s is %s and cannot receive introductions", m.ID, string(m.Status))
	}
	return nil
}

// Clone returns an independent copy of the profile.
func (m Member) Clone() Member { return m }
