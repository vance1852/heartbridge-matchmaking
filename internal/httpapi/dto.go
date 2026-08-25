package httpapi

import (
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/domain/entitlement"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/member"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/membersvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/schedulesvc"
)

// memberView is the wire representation of a member profile.
type memberView struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	Gender        string `json:"gender"`
	Age           int    `json:"age"`
	City          string `json:"city"`
	MaritalStatus string `json:"marital_status"`
	Education     string `json:"education"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
}

// newMemberView renders a member profile at a given instant.
func newMemberView(profile member.Member, at time.Time) memberView {
	return memberView{
		ID:            profile.ID,
		DisplayName:   profile.DisplayName,
		Gender:        string(profile.Gender),
		Age:           profile.AgeAt(at),
		City:          profile.City,
		MaritalStatus: string(profile.MaritalStatus),
		Education:     string(profile.Education),
		Status:        string(profile.Status),
		CreatedAt:     profile.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// preferenceView is the wire representation of partner criteria.
type preferenceView struct {
	SeekingGender   string   `json:"seeking_gender"`
	MinAge          int      `json:"min_age"`
	MaxAge          int      `json:"max_age"`
	Cities          []string `json:"cities"`
	MaritalStatuses []string `json:"marital_statuses"`
	MinEducation    string   `json:"min_education"`
	UpdatedAt       string   `json:"updated_at"`
}

// newPreferenceView renders partner criteria.
func newPreferenceView(preference member.Preference) preferenceView {
	statuses := make([]string, 0, len(preference.MaritalStatuses))
	for _, status := range preference.MaritalStatuses {
		statuses = append(statuses, string(status))
	}
	cities := make([]string, len(preference.Cities))
	copy(cities, preference.Cities)
	return preferenceView{
		SeekingGender:   string(preference.SeekingGender),
		MinAge:          preference.MinAge,
		MaxAge:          preference.MaxAge,
		Cities:          cities,
		MaritalStatuses: statuses,
		MinEducation:    string(preference.MinEducation),
		UpdatedAt:       preference.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// entitlementView is the wire representation of an introduction allowance.
type entitlementView struct {
	ID         string `json:"id"`
	PlanCode   string `json:"plan_code"`
	Total      int    `json:"total"`
	Used       int    `json:"used"`
	Reserved   int    `json:"reserved"`
	Available  int    `json:"available"`
	State      string `json:"state"`
	ValidFrom  string `json:"valid_from"`
	ValidUntil string `json:"valid_until"`
	Version    int64  `json:"version"`
}

// newEntitlementView renders an allowance.
func newEntitlementView(granted entitlement.Entitlement) entitlementView {
	return entitlementView{
		ID:         granted.ID,
		PlanCode:   granted.PlanCode,
		Total:      granted.Total,
		Used:       granted.Used,
		Reserved:   granted.Reserved,
		Available:  granted.Available(),
		State:      string(granted.State),
		ValidFrom:  granted.ValidFrom.UTC().Format(time.RFC3339),
		ValidUntil: granted.ValidUntil.UTC().Format(time.RFC3339),
		Version:    granted.Version,
	}
}

// planView is the wire representation of a service plan.
type planView struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	IntroQuota     int    `json:"intro_quota"`
	ValidDays      int    `json:"valid_days"`
	MaxActiveMatch int    `json:"max_active_match"`
	ConsentHours   int    `json:"consent_hours"`
	Active         bool   `json:"active"`
}

// newPlanView renders a service plan.
func newPlanView(plan entitlement.Plan) planView {
	return planView{
		Code:           plan.Code,
		Name:           plan.Name,
		IntroQuota:     plan.IntroQuota,
		ValidDays:      plan.ValidDays,
		MaxActiveMatch: plan.MaxActiveMatch,
		ConsentHours:   plan.ConsentHours,
		Active:         plan.Active,
	}
}

// profileView bundles the member self-service payload.
type profileView struct {
	Member      memberView       `json:"member"`
	Preference  *preferenceView  `json:"preference,omitempty"`
	Entitlement *entitlementView `json:"entitlement,omitempty"`
}

// newProfileView renders the member self-service payload.
func newProfileView(profile membersvc.Profile, at time.Time) profileView {
	view := profileView{Member: newMemberView(profile.Member, at)}
	if profile.Preference != nil {
		rendered := newPreferenceView(*profile.Preference)
		view.Preference = &rendered
	}
	if profile.Entitlement != nil {
		rendered := newEntitlementView(*profile.Entitlement)
		view.Entitlement = &rendered
	}
	return view
}

// consentView is the wire representation of one member answer.
type consentView struct {
	MemberID  string `json:"member_id"`
	Decision  string `json:"decision"`
	DecidedAt string `json:"decided_at,omitempty"`
}

// ledgerView is the wire representation of one allowance movement.
type ledgerView struct {
	EntitlementID string `json:"entitlement_id"`
	Reason        string `json:"reason"`
	DeltaReserved int    `json:"delta_reserved"`
	DeltaUsed     int    `json:"delta_used"`
	CreatedAt     string `json:"created_at"`
}

// matchView is the wire representation of one introduction.
type matchView struct {
	ID              string        `json:"id"`
	MatchmakerID    string        `json:"matchmaker_id"`
	MemberAID       string        `json:"member_a_id"`
	MemberBID       string        `json:"member_b_id"`
	State           string        `json:"state"`
	Version         int64         `json:"version"`
	ConsentDeadline string        `json:"consent_deadline"`
	ClosingNote     string        `json:"closing_note,omitempty"`
	CreatedAt       string        `json:"created_at"`
	UpdatedAt       string        `json:"updated_at"`
	ClosedAt        string        `json:"closed_at,omitempty"`
	Consents        []consentView `json:"consents,omitempty"`
	Ledger          []ledgerView  `json:"allowance_movements,omitempty"`
}

// newMatchView renders one introduction without its child collections.
func newMatchView(match matching.Match) matchView {
	view := matchView{
		ID:              match.ID,
		MatchmakerID:    match.MatchmakerID,
		MemberAID:       match.MemberAID,
		MemberBID:       match.MemberBID,
		State:           string(match.State),
		Version:         match.Version,
		ConsentDeadline: match.ConsentDeadline.UTC().Format(time.RFC3339),
		ClosingNote:     match.ClosingNote,
		CreatedAt:       match.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       match.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if match.ClosedAt != nil {
		view.ClosedAt = match.ClosedAt.UTC().Format(time.RFC3339)
	}
	return view
}

// newMatchDetailView renders one introduction with consents and movements.
func newMatchDetailView(detail matchsvc.Detail) matchView {
	view := newMatchView(detail.Match)
	view.Consents = make([]consentView, 0, len(detail.Consents))
	for _, consent := range detail.Consents {
		rendered := consentView{MemberID: consent.MemberID, Decision: string(consent.Decision)}
		if consent.DecidedAt != nil {
			rendered.DecidedAt = consent.DecidedAt.UTC().Format(time.RFC3339)
		}
		view.Consents = append(view.Consents, rendered)
	}
	view.Ledger = make([]ledgerView, 0, len(detail.Ledger))
	for _, entry := range detail.Ledger {
		view.Ledger = append(view.Ledger, ledgerView{
			EntitlementID: entry.EntitlementID,
			Reason:        string(entry.Reason),
			DeltaReserved: entry.DeltaReserved,
			DeltaUsed:     entry.DeltaUsed,
			CreatedAt:     entry.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return view
}

// slotView is the wire representation of a bookable venue window.
type slotView struct {
	ID          string `json:"id"`
	VenueCode   string `json:"venue_code"`
	VenueName   string `json:"venue_name"`
	City        string `json:"city"`
	StartAt     string `json:"start_at"`
	EndAt       string `json:"end_at"`
	BusinessDay string `json:"business_day"`
	Capacity    int    `json:"capacity"`
	BookedCount int    `json:"booked_count"`
	Remaining   int    `json:"remaining"`
}

// newSlotView renders a venue slot.
func newSlotView(slot meetup.VenueSlot) slotView {
	return slotView{
		ID:          slot.ID,
		VenueCode:   slot.VenueCode,
		VenueName:   slot.VenueName,
		City:        slot.City,
		StartAt:     slot.StartAt.UTC().Format(time.RFC3339),
		EndAt:       slot.EndAt.UTC().Format(time.RFC3339),
		BusinessDay: slot.BusinessDate(),
		Capacity:    slot.Capacity,
		BookedCount: slot.BookedCount,
		Remaining:   slot.Capacity - slot.BookedCount,
	}
}

// feedbackView is the wire representation of one participant report.
type feedbackView struct {
	AuthorMemberID string `json:"author_member_id"`
	Intent         string `json:"intent"`
	Rating         int    `json:"rating"`
	Comment        string `json:"comment,omitempty"`
	CreatedAt      string `json:"created_at"`
}

// meetupView is the wire representation of one meetup.
type meetupView struct {
	ID          string         `json:"id"`
	MatchID     string         `json:"match_id"`
	State       string         `json:"state"`
	Version     int64          `json:"version"`
	BookedAt    string         `json:"booked_at"`
	CheckedInAt string         `json:"checked_in_at,omitempty"`
	CompletedAt string         `json:"completed_at,omitempty"`
	Slot        slotView       `json:"slot"`
	MatchState  string         `json:"match_state"`
	Feedback    []feedbackView `json:"feedback"`
}

// newMeetupView renders a meetup with its slot and visible reports.
func newMeetupView(detail schedulesvc.Detail) meetupView {
	view := meetupView{
		ID:         detail.Meetup.ID,
		MatchID:    detail.Meetup.MatchID,
		State:      string(detail.Meetup.State),
		Version:    detail.Meetup.Version,
		BookedAt:   detail.Meetup.BookedAt.UTC().Format(time.RFC3339),
		Slot:       newSlotView(detail.Slot),
		MatchState: string(detail.Match.State),
		Feedback:   make([]feedbackView, 0, len(detail.Feedback)),
	}
	if detail.Meetup.CheckedInAt != nil {
		view.CheckedInAt = detail.Meetup.CheckedInAt.UTC().Format(time.RFC3339)
	}
	if detail.Meetup.CompletedAt != nil {
		view.CompletedAt = detail.Meetup.CompletedAt.UTC().Format(time.RFC3339)
	}
	for _, report := range detail.Feedback {
		view.Feedback = append(view.Feedback, feedbackView{
			AuthorMemberID: report.AuthorMemberID,
			Intent:         string(report.Intent),
			Rating:         report.Rating,
			Comment:        report.Comment,
			CreatedAt:      report.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return view
}

// pageMeta describes the pagination of a list response.
type pageMeta struct {
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// matchListResponse is the payload of the match list endpoint.
type matchListResponse struct {
	Items []matchView `json:"items"`
	Page  pageMeta    `json:"page"`
}

// newMatchListResponse renders a page of introductions.
func newMatchListResponse(page repository.MatchPage) matchListResponse {
	items := make([]matchView, 0, len(page.Items))
	for _, match := range page.Items {
		items = append(items, newMatchView(match))
	}
	return matchListResponse{
		Items: items,
		Page:  pageMeta{Total: page.Total, Limit: page.Limit, Offset: page.Offset},
	}
}

// auditView is the wire representation of one audit row.
type auditView struct {
	ID         string `json:"id"`
	ActorID    string `json:"actor_id"`
	ActorRole  string `json:"actor_role"`
	Action     string `json:"action"`
	ObjectType string `json:"object_type"`
	ObjectID   string `json:"object_id"`
	Result     string `json:"result"`
	Detail     string `json:"detail"`
	RequestID  string `json:"request_id"`
	CreatedAt  string `json:"created_at"`
}

// auditListResponse is the payload of the audit list endpoint.
type auditListResponse struct {
	Items []auditView `json:"items"`
	Page  pageMeta    `json:"page"`
}

// newAuditListResponse renders a page of audit rows.
func newAuditListResponse(page repository.AuditPage) auditListResponse {
	items := make([]auditView, 0, len(page.Items))
	for _, event := range page.Items {
		items = append(items, auditView{
			ID:         event.ID,
			ActorID:    event.ActorID,
			ActorRole:  event.ActorRole,
			Action:     string(event.Action),
			ObjectType: string(event.ObjectType),
			ObjectID:   event.ObjectID,
			Result:     string(event.Result),
			Detail:     event.Detail,
			RequestID:  event.RequestID,
			CreatedAt:  event.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return auditListResponse{
		Items: items,
		Page:  pageMeta{Total: page.Total, Limit: page.Limit, Offset: page.Offset},
	}
}

// actorView describes the authenticated principal.
type actorView struct {
	UserID   string `json:"user_id"`
	Role     string `json:"role"`
	MemberID string `json:"member_id,omitempty"`
}

// newActorView renders the authenticated principal.
func newActorView(actor identity.Actor) actorView {
	return actorView{UserID: actor.UserID, Role: string(actor.Role), MemberID: actor.MemberID}
}
