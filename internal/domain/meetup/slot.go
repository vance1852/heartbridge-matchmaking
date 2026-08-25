// Package meetup models the offline part of the business: bookable venue slots,
// the meetup itself and the feedback both participants leave afterwards.
package meetup

import (
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
)

// VenueSlot is a bookable time window at a partner venue. Capacity is a scarce
// shared resource: two concurrent bookings must not both succeed once the last
// seat is taken.
type VenueSlot struct {
	ID          string
	VenueCode   string
	VenueName   string
	City        string
	StartAt     time.Time
	EndAt       time.Time
	Capacity    int
	BookedCount int
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Validate applies the structural rules of a slot definition.
func (s VenueSlot) Validate() error {
	if strings.TrimSpace(s.VenueCode) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "venue code is required")
	}
	if strings.TrimSpace(s.VenueName) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "venue name is required")
	}
	if strings.TrimSpace(s.City) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "venue city is required")
	}
	if s.StartAt.IsZero() || s.EndAt.IsZero() {
		return apperr.New(apperr.CodeInvalidArgument, "venue slot window is required")
	}
	if !s.EndAt.After(s.StartAt) {
		return apperr.New(apperr.CodeInvalidArgument, "venue slot must end after it starts")
	}
	if s.Capacity <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, "venue slot capacity must be positive")
	}
	if s.BookedCount < 0 || s.BookedCount > s.Capacity {
		return apperr.New(apperr.CodeInvalidArgument, "venue slot booked count is out of range")
	}
	return nil
}

// HasRoom reports whether the slot still has a free seat.
func (s VenueSlot) HasRoom() bool { return s.BookedCount < s.Capacity }

// Bookable returns the stable business failure preventing a booking at instant.
func (s VenueSlot) Bookable(now time.Time) error {
	if !s.StartAt.After(now) {
		return apperr.Newf(apperr.CodePreconditionFailed,
			"venue slot %s starts at %s and can no longer be booked",
			s.ID, s.StartAt.UTC().Format(time.RFC3339))
	}
	if !s.HasRoom() {
		return apperr.Newf(apperr.CodeCapacityExhausted,
			"venue slot %s is fully booked", s.ID)
	}
	return nil
}

// Overlaps reports whether two windows intersect. Touching windows, where one
// ends exactly when the other starts, do not overlap.
func (s VenueSlot) Overlaps(otherStart, otherEnd time.Time) bool {
	return s.StartAt.Before(otherEnd) && otherStart.Before(s.EndAt)
}

// BusinessDate returns the slot day formatted in the business timezone. It is
// part of the API payload so that operators never have to convert UTC by hand.
func (s VenueSlot) BusinessDate() string {
	return s.StartAt.In(clock.BusinessLocation()).Format("2006-01-02")
}

// Clone returns an independent copy of the slot.
func (s VenueSlot) Clone() VenueSlot { return s }
