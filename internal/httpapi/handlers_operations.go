package httpapi

import (
	"net/http"
	"strings"

	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/meetup"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/adminsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/schedulesvc"
)

// handleGetMeetup returns one meetup.
func (s *Server) handleGetMeetup(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	detail, err := s.schedule.Get(request.Context(), current, request.PathValue("meetupID"))
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newMeetupView(detail))
}

// handleCheckIn confirms both participants arrived.
func (s *Server) handleCheckIn(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	detail, err := s.schedule.CheckIn(request.Context(), current, request.PathValue("meetupID"))
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newMeetupView(detail))
}

// handleCompleteMeetup finishes a meetup and settles both allowances.
func (s *Server) handleCompleteMeetup(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	detail, err := s.schedule.Complete(request.Context(), current, request.PathValue("meetupID"))
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newMeetupView(detail))
}

// handleCancelMeetup withdraws a booking and frees the venue seat.
func (s *Server) handleCancelMeetup(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	payload := reasonRequest{}
	if request.ContentLength > 0 {
		if _, err := decodeBody(request, &payload); err != nil {
			writeError(writer, request, err)
			return
		}
	}
	detail, err := s.schedule.Cancel(request.Context(), current, request.PathValue("meetupID"), payload.Reason)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newMeetupView(detail))
}

// feedbackRequest is one participant report.
type feedbackRequest struct {
	Intent  string `json:"intent"`
	Rating  int    `json:"rating"`
	Comment string `json:"comment"`
}

// handleSubmitFeedback records a participant report.
func (s *Server) handleSubmitFeedback(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	var payload feedbackRequest
	if _, err := decodeBody(request, &payload); err != nil {
		writeError(writer, request, err)
		return
	}
	detail, err := s.schedule.SubmitFeedback(request.Context(), current,
		request.PathValue("meetupID"), schedulesvc.FeedbackInput{
			Intent:  meetup.Intent(payload.Intent),
			Rating:  payload.Rating,
			Comment: payload.Comment,
		})
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newMeetupView(detail))
}

// slotRequest is the venue slot payload.
type slotRequest struct {
	VenueCode string `json:"venue_code"`
	VenueName string `json:"venue_name"`
	City      string `json:"city"`
	StartAt   string `json:"start_at"`
	EndAt     string `json:"end_at"`
	Capacity  int    `json:"capacity"`
}

// handlePublishSlot publishes a bookable venue window.
func (s *Server) handlePublishSlot(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	var payload slotRequest
	body, err := decodeBody(request, &payload)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	startAt, err := parseTimestamp("start_at", payload.StartAt)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	endAt, err := parseTimestamp("end_at", payload.EndAt)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	s.idempotent(writer, request, body, current, func() (int, any, error) {
		slot, err := s.admin.PublishSlot(request.Context(), current, adminsvc.SlotInput{
			VenueCode: payload.VenueCode,
			VenueName: payload.VenueName,
			City:      payload.City,
			StartAt:   startAt,
			EndAt:     endAt,
			Capacity:  payload.Capacity,
		})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, newSlotView(slot), nil
	})
}

// handleListSlots lists the bookable windows of a range.
func (s *Server) handleListSlots(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	from, err := requiredInstant(request, "from")
	if err != nil {
		writeError(writer, request, err)
		return
	}
	to, err := requiredInstant(request, "to")
	if err != nil {
		writeError(writer, request, err)
		return
	}
	page, err := parsePage(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	slots, err := s.admin.ListSlots(request.Context(), current, from, to,
		strings.TrimSpace(request.URL.Query().Get("city")), page)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	items := make([]slotView, 0, len(slots))
	for _, slot := range slots {
		items = append(items, newSlotView(slot))
	}
	writeJSON(writer, request, http.StatusOK, map[string]any{"items": items})
}

// planRequest is the service plan payload.
type planRequest struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	IntroQuota     int    `json:"intro_quota"`
	ValidDays      int    `json:"valid_days"`
	MaxActiveMatch int    `json:"max_active_match"`
	ConsentHours   int    `json:"consent_hours"`
	Active         bool   `json:"active"`
}

// handleUpsertPlan creates or updates a service plan.
func (s *Server) handleUpsertPlan(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	var payload planRequest
	if _, err := decodeBody(request, &payload); err != nil {
		writeError(writer, request, err)
		return
	}
	plan, err := s.admin.UpsertPlan(request.Context(), current, adminsvc.PlanInput{
		Code:           payload.Code,
		Name:           payload.Name,
		IntroQuota:     payload.IntroQuota,
		ValidDays:      payload.ValidDays,
		MaxActiveMatch: payload.MaxActiveMatch,
		ConsentHours:   payload.ConsentHours,
		Active:         payload.Active,
	})
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newPlanView(plan))
}

// handleListPlans lists the service plans.
func (s *Server) handleListPlans(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	plans, err := s.admin.ListPlans(request.Context(), current)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	items := make([]planView, 0, len(plans))
	for _, plan := range plans {
		items = append(items, newPlanView(plan))
	}
	writeJSON(writer, request, http.StatusOK, map[string]any{"items": items})
}

// grantRequest identifies the plan to activate for a member.
type grantRequest struct {
	PlanCode string `json:"plan_code"`
}

// handleGrantEntitlement activates a service plan for a member.
func (s *Server) handleGrantEntitlement(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	var payload grantRequest
	body, err := decodeBody(request, &payload)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	memberID := request.PathValue("memberID")
	s.idempotent(writer, request, body, current, func() (int, any, error) {
		granted, err := s.members.GrantEntitlement(request.Context(), current, memberID, payload.PlanCode)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, newEntitlementView(granted), nil
	})
}

// handleListAudit returns a page of the audit trail.
func (s *Server) handleListAudit(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	page, err := parsePage(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	query := request.URL.Query()
	result, err := s.admin.ListAudit(request.Context(), current, repository.AuditFilter{
		ObjectType: audit.ObjectType(strings.TrimSpace(query.Get("object_type"))),
		ObjectID:   strings.TrimSpace(query.Get("object_id")),
		ActorID:    strings.TrimSpace(query.Get("actor_id")),
		RequestID:  strings.TrimSpace(query.Get("request_id")),
		Page:       page,
	})
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newAuditListResponse(result))
}
