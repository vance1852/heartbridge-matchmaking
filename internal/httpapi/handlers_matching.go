package httpapi

import (
	"net/http"
	"strings"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
)

// proposeRequest identifies the pair to introduce.
type proposeRequest struct {
	FirstMemberID  string `json:"first_member_id"`
	SecondMemberID string `json:"second_member_id"`
}

// handleProposeMatch creates one introduction under replay protection.
func (s *Server) handleProposeMatch(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	var payload proposeRequest
	body, err := decodeBody(request, &payload)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	s.idempotent(writer, request, body, current, func() (int, any, error) {
		detail, err := s.matches.Propose(request.Context(), current, matchsvc.ProposeInput{
			FirstMemberID:  payload.FirstMemberID,
			SecondMemberID: payload.SecondMemberID,
		})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, newMatchDetailView(detail), nil
	})
}

// batchProposeRequest carries several pairs.
type batchProposeRequest struct {
	Pairs []proposeRequest `json:"pairs"`
}

// batchItemView is the wire representation of one batch verdict.
type batchItemView struct {
	Index          int    `json:"index"`
	FirstMemberID  string `json:"first_member_id"`
	SecondMemberID string `json:"second_member_id"`
	Accepted       bool   `json:"accepted"`
	MatchID        string `json:"match_id,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
}

// handleProposeBatch creates several introductions and reports each verdict.
func (s *Server) handleProposeBatch(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	var payload batchProposeRequest
	body, err := decodeBody(request, &payload)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	inputs := make([]matchsvc.ProposeInput, 0, len(payload.Pairs))
	for _, pair := range payload.Pairs {
		inputs = append(inputs, matchsvc.ProposeInput{
			FirstMemberID:  pair.FirstMemberID,
			SecondMemberID: pair.SecondMemberID,
		})
	}
	s.idempotent(writer, request, body, current, func() (int, any, error) {
		result, err := s.matches.ProposeBatch(request.Context(), current, inputs)
		if err != nil {
			return 0, nil, err
		}
		items := make([]batchItemView, 0, len(result.Items))
		for _, item := range result.Items {
			items = append(items, batchItemView{
				Index:          item.Index,
				FirstMemberID:  item.FirstMemberID,
				SecondMemberID: item.SecondMemberID,
				Accepted:       item.Accepted,
				MatchID:        item.MatchID,
				ErrorCode:      string(item.ErrorCode),
				ErrorMessage:   item.ErrorMessage,
			})
		}
		// A batch always answers 200: the per-item verdicts carry the outcome, so
		// a partially rejected batch is not a transport level failure.
		return http.StatusOK, map[string]any{
			"requested": result.Requested,
			"accepted":  result.Accepted,
			"rejected":  result.Rejected,
			"items":     items,
		}, nil
	})
}

// handleGetMatch returns one introduction.
func (s *Server) handleGetMatch(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	detail, err := s.matches.Get(request.Context(), current, request.PathValue("matchID"))
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newMatchDetailView(detail))
}

// handleListMatches returns a filtered, sorted page of introductions.
func (s *Server) handleListMatches(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	filter, err := parseMatchFilter(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	page, err := s.matches.List(request.Context(), current, filter)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newMatchListResponse(page))
}

// parseMatchFilter reads the list query parameters.
func parseMatchFilter(request *http.Request) (repository.MatchFilter, error) {
	page, err := parsePage(request)
	if err != nil {
		return repository.MatchFilter{}, err
	}
	query := request.URL.Query()
	filter := repository.MatchFilter{
		MemberID:     strings.TrimSpace(query.Get("member_id")),
		MatchmakerID: strings.TrimSpace(query.Get("matchmaker_id")),
		Page:         page,
	}
	if raw := strings.TrimSpace(query.Get("state")); raw != "" {
		for _, value := range strings.Split(raw, ",") {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			state := matching.State(trimmed)
			if err := state.Validate(); err != nil {
				return repository.MatchFilter{}, err
			}
			filter.States = append(filter.States, state)
		}
	}
	if filter.CreatedFrom, err = parseInstant(request, "created_from"); err != nil {
		return repository.MatchFilter{}, err
	}
	if filter.CreatedTo, err = parseInstant(request, "created_to"); err != nil {
		return repository.MatchFilter{}, err
	}
	if raw := strings.TrimSpace(query.Get("sort")); raw != "" {
		filter.SortField = repository.MatchSortField(raw)
		if err := filter.SortField.Validate(); err != nil {
			return repository.MatchFilter{}, err
		}
	}
	if raw := strings.TrimSpace(query.Get("order")); raw != "" {
		filter.SortDir = repository.SortDirection(strings.ToLower(raw))
		if err := filter.SortDir.Validate(); err != nil {
			return repository.MatchFilter{}, err
		}
	}
	return filter, nil
}

// consentRequest carries a member decision.
type consentRequest struct {
	Decision string `json:"decision"`
}

// handleConsent records the answer of one member.
func (s *Server) handleConsent(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	var payload consentRequest
	if _, err := decodeBody(request, &payload); err != nil {
		writeError(writer, request, err)
		return
	}
	detail, err := s.matches.Decide(request.Context(), current,
		request.PathValue("matchID"), matching.Decision(payload.Decision))
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newMatchDetailView(detail))
}

// reasonRequest carries an optional free-text reason.
type reasonRequest struct {
	Reason string `json:"reason"`
}

// handleCancelMatch withdraws one introduction.
func (s *Server) handleCancelMatch(writer http.ResponseWriter, request *http.Request) {
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
	detail, err := s.matches.Cancel(request.Context(), current, request.PathValue("matchID"), payload.Reason)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newMatchDetailView(detail))
}

// bookMeetupRequest identifies the venue slot to reserve.
type bookMeetupRequest struct {
	SlotID string `json:"slot_id"`
}

// handleBookMeetup arranges the offline meetup of an accepted introduction.
func (s *Server) handleBookMeetup(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	var payload bookMeetupRequest
	body, err := decodeBody(request, &payload)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	if strings.TrimSpace(payload.SlotID) == "" {
		writeError(writer, request, apperr.New(apperr.CodeInvalidArgument, "slot_id is required"))
		return
	}
	matchID := request.PathValue("matchID")
	s.idempotent(writer, request, body, current, func() (int, any, error) {
		detail, err := s.schedule.Book(request.Context(), current, matchID, payload.SlotID)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, newMeetupView(detail), nil
	})
}
