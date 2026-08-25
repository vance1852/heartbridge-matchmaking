// Package clock isolates wall-clock access and pins the business timezone used
// by every deadline in the matchmaking domain. Services depend on Clock instead
// of time.Now so that deadlines and expiry rules stay testable.
package clock

import (
	"sync"
	"time"

	// tzdata is embedded so that the business timezone resolves inside minimal
	// container images that ship no system zoneinfo database.
	_ "time/tzdata"
)

// businessZone is the single timezone in which consent deadlines, meetup days
// and entitlement validity are interpreted.
const businessZone = "Asia/Shanghai"

var (
	locationOnce sync.Once
	location     *time.Location
)

// BusinessLocation returns the pinned business timezone. It falls back to UTC
// only if the embedded database is unexpectedly unavailable, which keeps the
// service running with an explicit, documented behaviour.
func BusinessLocation() *time.Location {
	locationOnce.Do(func() {
		loaded, err := time.LoadLocation(businessZone)
		if err != nil {
			location = time.UTC
			return
		}
		location = loaded
	})
	return location
}

// Clock abstracts the current time.
type Clock interface {
	Now() time.Time
}

// System is the production Clock. It always reports UTC instants; callers
// convert to BusinessLocation only when a calendar day matters.
type System struct{}

// Now returns the current UTC instant.
func (System) Now() time.Time { return time.Now().UTC() }

// Fixed is a deterministic Clock used by tests and by the deterministic replay
// of background jobs. It is safe for concurrent use.
type Fixed struct {
	mu      sync.Mutex
	current time.Time
}

// NewFixed builds a Fixed clock anchored at the given instant.
func NewFixed(at time.Time) *Fixed {
	return &Fixed{current: at.UTC()}
}

// Now returns the currently configured instant.
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

// Advance moves the clock forward by d and returns the new instant.
func (f *Fixed) Advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = f.current.Add(d).UTC()
	return f.current
}

// Set overrides the current instant.
func (f *Fixed) Set(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = at.UTC()
}

// BusinessDay returns the calendar day of instant in the business timezone. It
// is used to enforce "one meetup per member per business day" style rules.
func BusinessDay(instant time.Time) (year int, month time.Month, day int) {
	local := instant.In(BusinessLocation())
	return local.Year(), local.Month(), local.Day()
}

// SameBusinessDay reports whether two instants fall on the same business day.
func SameBusinessDay(a, b time.Time) bool {
	ay, am, ad := BusinessDay(a)
	by, bm, bd := BusinessDay(b)
	return ay == by && am == bm && ad == bd
}
