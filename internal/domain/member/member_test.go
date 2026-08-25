package member

import (
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
)

// reference is the instant used by the age and eligibility assertions.
var reference = time.Date(2026, time.March, 2, 2, 0, 0, 0, time.UTC)

// newProfile builds a member fixture.
func newProfile(id string, gender Gender, birth time.Time) Member {
	return Member{
		ID:            id,
		UserID:        "usr_" + id,
		DisplayName:   "Member " + id,
		Gender:        gender,
		BirthDate:     birth,
		City:          "Hangzhou",
		MaritalStatus: MaritalSingle,
		Education:     EducationBachelor,
		Status:        StatusActive,
		CreatedAt:     reference,
		UpdatedAt:     reference,
	}
}

// newPreference builds wide-open partner criteria.
func newPreference(memberID string, seeking Gender) Preference {
	return Preference{
		MemberID:        memberID,
		SeekingGender:   seeking,
		MinAge:          25,
		MaxAge:          40,
		Cities:          []string{"Hangzhou", "Shanghai"},
		MaritalStatuses: []MaritalStatus{MaritalSingle, MaritalDivorced},
		MinEducation:    EducationCollege,
		UpdatedAt:       reference,
	}
}

// TestAgeIsEvaluatedInBusinessTimezone verifies the birthday boundary is resolved
// in the business timezone rather than in UTC.
func TestAgeIsEvaluatedInBusinessTimezone(t *testing.T) {
	location := clock.BusinessLocation()
	// A birthday that falls on 2 March in the business timezone but is still
	// 1 March in UTC.
	birth := time.Date(1996, time.March, 2, 0, 30, 0, 0, location)
	profile := newProfile("tz", GenderFemale, birth)

	dayBefore := time.Date(2026, time.March, 1, 23, 0, 0, 0, location)
	if age := profile.AgeAt(dayBefore); age != 29 {
		t.Fatalf("expected 29 the day before the birthday, got %d", age)
	}
	onBirthday := time.Date(2026, time.March, 2, 0, 0, 0, 0, location)
	if age := profile.AgeAt(onBirthday); age != 30 {
		t.Fatalf("expected 30 on the birthday, got %d", age)
	}
	// The same instant expressed in UTC must give the same business age.
	if age := profile.AgeAt(onBirthday.UTC()); age != 30 {
		t.Fatalf("expected the UTC form of the same instant to give 30, got %d", age)
	}
	future := newProfile("future", GenderMale, reference.AddDate(1, 0, 0))
	if age := future.AgeAt(reference); age != 0 {
		t.Fatalf("expected a future birth date to clamp to 0, got %d", age)
	}
}

// TestProfileValidationRejectsIncompleteRecords covers the structural rules.
func TestProfileValidationRejectsIncompleteRecords(t *testing.T) {
	base := newProfile("a", GenderFemale, time.Date(1994, time.June, 15, 0, 0, 0, 0, time.UTC))
	if err := base.Validate(); err != nil {
		t.Fatalf("expected a complete profile to validate: %v", err)
	}
	cases := map[string]func(Member) Member{
		"blank name":      func(m Member) Member { m.DisplayName = "  "; return m },
		"missing birth":   func(m Member) Member { m.BirthDate = time.Time{}; return m },
		"blank city":      func(m Member) Member { m.City = ""; return m },
		"unknown gender":  func(m Member) Member { m.Gender = "other"; return m },
		"unknown marital": func(m Member) Member { m.MaritalStatus = "complicated"; return m },
		"unknown edu":     func(m Member) Member { m.Education = "kindergarten"; return m },
		"unknown status":  func(m Member) Member { m.Status = "vanished"; return m },
	}
	for name, mutate := range cases {
		if err := mutate(base).Validate(); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}
}

// TestMatchableOnlyForActiveProfiles verifies the enrollment gate.
func TestMatchableOnlyForActiveProfiles(t *testing.T) {
	base := newProfile("a", GenderFemale, time.Date(1994, time.June, 15, 0, 0, 0, 0, time.UTC))
	if err := base.Matchable(); err != nil {
		t.Fatalf("expected an active profile to be matchable: %v", err)
	}
	for _, status := range []Status{StatusOnboarding, StatusPaused, StatusRetired} {
		candidate := base
		candidate.Status = status
		err := candidate.Matchable()
		if err == nil {
			t.Fatalf("expected %s to block matchmaking", status)
		}
		if code := apperr.CodeOf(err); code != apperr.CodePreconditionFailed {
			t.Fatalf("expected code %s for %s, got %s", apperr.CodePreconditionFailed, status, code)
		}
	}
}

// TestEducationOrderingIsRespected verifies the ranked comparison.
func TestEducationOrderingIsRespected(t *testing.T) {
	if !EducationDoctor.AtLeast(EducationHighSchool) {
		t.Fatal("expected a doctorate to satisfy a high school minimum")
	}
	if EducationHighSchool.AtLeast(EducationBachelor) {
		t.Fatal("expected high school not to satisfy a bachelor minimum")
	}
	if !EducationBachelor.AtLeast(EducationBachelor) {
		t.Fatal("expected the same level to satisfy the minimum")
	}
	if err := Education("phd").Validate(); err == nil {
		t.Fatal("expected an unknown education level to be refused")
	}
}

// TestPreferenceNormalizationDeduplicatesAndSorts verifies the stored form of the
// multi-valued criteria.
func TestPreferenceNormalizationDeduplicatesAndSorts(t *testing.T) {
	preference := Preference{
		MemberID:        "mbr_a",
		SeekingGender:   GenderMale,
		MinAge:          25,
		MaxAge:          40,
		Cities:          []string{" Shanghai ", "hangzhou", "Hangzhou", "", "Shanghai"},
		MaritalStatuses: []MaritalStatus{MaritalDivorced, MaritalSingle, MaritalDivorced},
		MinEducation:    EducationCollege,
		UpdatedAt:       reference,
	}
	preference.Normalize()
	if len(preference.Cities) != 2 {
		t.Fatalf("expected duplicates and blanks to be dropped, got %v", preference.Cities)
	}
	if preference.Cities[0] != "Shanghai" || preference.Cities[1] != "hangzhou" {
		t.Fatalf("expected a sorted city list, got %v", preference.Cities)
	}
	if len(preference.MaritalStatuses) != 2 {
		t.Fatalf("expected duplicate statuses to be dropped, got %v", preference.MaritalStatuses)
	}
	if preference.MaritalStatuses[0] != MaritalDivorced {
		t.Fatalf("expected a sorted status list, got %v", preference.MaritalStatuses)
	}
	if !preference.AcceptsCity("  hangzhou ") {
		t.Fatal("expected the city comparison to ignore case and padding")
	}
	if preference.AcceptsCity("Chengdu") {
		t.Fatal("expected an unlisted city to be refused")
	}
	if !preference.AcceptsMaritalStatus(MaritalSingle) || preference.AcceptsMaritalStatus(MaritalWidowed) {
		t.Fatal("expected the marital status filter to follow the stored list")
	}
}

// TestPreferenceValidationRejectsImpossibleCriteria covers the structural rules.
func TestPreferenceValidationRejectsImpossibleCriteria(t *testing.T) {
	base := newPreference("mbr_a", GenderMale)
	if err := base.Validate(); err != nil {
		t.Fatalf("expected complete criteria to validate: %v", err)
	}
	cases := map[string]func(Preference) Preference{
		"no member":       func(p Preference) Preference { p.MemberID = ""; return p },
		"unknown gender":  func(p Preference) Preference { p.SeekingGender = "any"; return p },
		"underage":        func(p Preference) Preference { p.MinAge = 17; return p },
		"inverted range":  func(p Preference) Preference { p.MaxAge = p.MinAge - 1; return p },
		"unbounded range": func(p Preference) Preference { p.MaxAge = 120; return p },
		"no city":         func(p Preference) Preference { p.Cities = nil; return p },
		"blank city":      func(p Preference) Preference { p.Cities = []string{" "}; return p },
		"no marital":      func(p Preference) Preference { p.MaritalStatuses = nil; return p },
		"unknown marital": func(p Preference) Preference { p.MaritalStatuses = []MaritalStatus{"single-ish"}; return p },
		"unknown min edu": func(p Preference) Preference { p.MinEducation = "school"; return p },
	}
	for name, mutate := range cases {
		if err := mutate(base).Validate(); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}
}

// TestPreferenceAcceptsReportsFirstUnmetCriterion covers each eligibility rule.
func TestPreferenceAcceptsReportsFirstUnmetCriterion(t *testing.T) {
	preference := newPreference("mbr_a", GenderMale)
	candidate := newProfile("b", GenderMale, time.Date(1994, time.June, 15, 0, 0, 0, 0, time.UTC))
	if err := preference.Accepts(candidate, reference); err != nil {
		t.Fatalf("expected a compatible candidate to be accepted: %v", err)
	}

	wrongGender := candidate
	wrongGender.Gender = GenderFemale
	if err := preference.Accepts(wrongGender, reference); err == nil {
		t.Fatal("expected the sought gender to be enforced")
	}
	tooYoung := newProfile("c", GenderMale, time.Date(2005, time.June, 15, 0, 0, 0, 0, time.UTC))
	if err := preference.Accepts(tooYoung, reference); err == nil {
		t.Fatal("expected the age range to be enforced")
	}
	tooOld := newProfile("d", GenderMale, time.Date(1975, time.June, 15, 0, 0, 0, 0, time.UTC))
	if err := preference.Accepts(tooOld, reference); err == nil {
		t.Fatal("expected the upper age bound to be enforced")
	}
	wrongCity := candidate
	wrongCity.City = "Chengdu"
	if err := preference.Accepts(wrongCity, reference); err == nil {
		t.Fatal("expected the city list to be enforced")
	}
	wrongMarital := candidate
	wrongMarital.MaritalStatus = MaritalWidowed
	if err := preference.Accepts(wrongMarital, reference); err == nil {
		t.Fatal("expected the marital status list to be enforced")
	}
	lowEducation := candidate
	lowEducation.Education = EducationHighSchool
	err := preference.Accepts(lowEducation, reference)
	if err == nil {
		t.Fatal("expected the education minimum to be enforced")
	}
	if code := apperr.CodeOf(err); code != apperr.CodePreconditionFailed {
		t.Fatalf("expected code %s, got %s", apperr.CodePreconditionFailed, code)
	}
}

// TestMutualEligibilityChecksBothDirections verifies matchmaking needs consent
// from both sets of criteria.
func TestMutualEligibilityChecksBothDirections(t *testing.T) {
	left := newProfile("l", GenderFemale, time.Date(1993, time.June, 15, 0, 0, 0, 0, time.UTC))
	right := newProfile("r", GenderMale, time.Date(1992, time.June, 15, 0, 0, 0, 0, time.UTC))
	leftPreference := newPreference(left.ID, GenderMale)
	rightPreference := newPreference(right.ID, GenderFemale)

	if err := MutuallyEligible(left, leftPreference, right, rightPreference, reference); err != nil {
		t.Fatalf("expected a mutually compatible pair: %v", err)
	}
	if err := MutuallyEligible(left, leftPreference, left, leftPreference, reference); err == nil {
		t.Fatal("expected a self-match to be refused")
	}
	// Only the second direction fails: the right member wants someone younger.
	pickyRight := rightPreference
	pickyRight.MaxAge = 30
	if err := MutuallyEligible(left, leftPreference, right, pickyRight, reference); err == nil {
		t.Fatal("expected the second direction to be checked as well")
	}
	pausedRight := right
	pausedRight.Status = StatusPaused
	if err := MutuallyEligible(left, leftPreference, pausedRight, rightPreference, reference); err == nil {
		t.Fatal("expected a paused counterpart to block the pair")
	}
}

// TestPreferenceCloneIsDeep verifies the defensive copy of the criteria.
func TestPreferenceCloneIsDeep(t *testing.T) {
	original := newPreference("mbr_a", GenderMale)
	copied := original.Clone()
	copied.Cities[0] = "Mutated"
	copied.Cities = append(copied.Cities, "Extra")
	copied.MaritalStatuses[0] = MaritalWidowed

	if original.Cities[0] == "Mutated" {
		t.Fatal("expected the clone to own its city slice")
	}
	if len(original.Cities) != 2 {
		t.Fatalf("expected the original list to keep its length, got %d", len(original.Cities))
	}
	if original.MaritalStatuses[0] == MaritalWidowed {
		t.Fatal("expected the clone to own its status slice")
	}
	empty := Preference{MemberID: "mbr_b"}
	if clone := empty.Clone(); clone.Cities != nil || clone.MaritalStatuses != nil {
		t.Fatal("expected nil slices to stay nil after cloning")
	}
}

// TestSameBusinessDayUsesBusinessTimezone verifies the calendar helper.
func TestSameBusinessDayUsesBusinessTimezone(t *testing.T) {
	location := clock.BusinessLocation()
	morning := time.Date(2026, time.March, 2, 9, 0, 0, 0, location)
	evening := time.Date(2026, time.March, 2, 22, 0, 0, 0, location)
	nextDay := time.Date(2026, time.March, 3, 1, 0, 0, 0, location)

	if !clock.SameBusinessDay(morning, evening) {
		t.Fatal("expected two instants of the same business day to match")
	}
	if clock.SameBusinessDay(evening, nextDay) {
		t.Fatal("expected instants on different business days not to match")
	}
	year, month, day := clock.BusinessDay(morning.UTC())
	if year != 2026 || month != time.March || day != 2 {
		t.Fatalf("expected 2026-03-02 in the business timezone, got %d-%02d-%02d", year, month, day)
	}
}
