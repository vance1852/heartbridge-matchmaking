package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/member"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/authsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/membersvc"
)

// encodePayload marshals a response payload once so that it can be both stored
// for replay and written to the client.
func encodePayload(payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "encode response", err)
	}
	return body, nil
}

// handleLiveness answers whether the process is running.
func (s *Server) handleLiveness(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, request, http.StatusOK, map[string]any{
		"status":  "alive",
		"version": s.version,
		"time":    s.clock.Now().Format(time.RFC3339),
	})
}

// handleReadiness answers whether the required dependencies are usable. It
// exercises the real database, so a broken or un-migrated store fails the probe.
func (s *Server) handleReadiness(writer http.ResponseWriter, request *http.Request) {
	if s.readiness != nil {
		if err := s.readiness(request.Context()); err != nil {
			writeError(writer, request, err)
			return
		}
	}
	writeJSON(writer, request, http.StatusOK, map[string]any{
		"status":  "ready",
		"version": s.version,
	})
}

// registerMemberRequest is the self-service enrollment payload.
type registerMemberRequest struct {
	Email         string `json:"email"`
	Password      string `json:"password"`
	DisplayName   string `json:"display_name"`
	Gender        string `json:"gender"`
	BirthDate     string `json:"birth_date"`
	City          string `json:"city"`
	MaritalStatus string `json:"marital_status"`
	Education     string `json:"education"`
}

// handleRegisterMember enrolls a new member.
func (s *Server) handleRegisterMember(writer http.ResponseWriter, request *http.Request) {
	var payload registerMemberRequest
	if _, err := decodeBody(request, &payload); err != nil {
		writeError(writer, request, err)
		return
	}
	birthDate, err := parseTimestamp("birth_date", payload.BirthDate)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	account, profile, err := s.auth.RegisterMember(request.Context(), authsvc.RegisterMemberInput{
		Email:         payload.Email,
		Password:      payload.Password,
		DisplayName:   payload.DisplayName,
		Gender:        member.Gender(payload.Gender),
		BirthDate:     birthDate,
		City:          payload.City,
		MaritalStatus: member.MaritalStatus(payload.MaritalStatus),
		Education:     member.Education(payload.Education),
	})
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusCreated, map[string]any{
		"user_id": account.ID,
		"member":  newMemberView(profile, s.clock.Now()),
	})
}

// registerStaffRequest is the operations payload creating a staff account.
type registerStaffRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// handleRegisterStaff creates a matchmaker or operations account.
func (s *Server) handleRegisterStaff(writer http.ResponseWriter, request *http.Request) {
	if _, err := actor(request); err != nil {
		writeError(writer, request, err)
		return
	}
	var payload registerStaffRequest
	if _, err := decodeBody(request, &payload); err != nil {
		writeError(writer, request, err)
		return
	}
	account, err := s.auth.RegisterStaff(request.Context(), authsvc.RegisterStaffInput{
		Email:    payload.Email,
		Password: payload.Password,
		Role:     identity.Role(payload.Role),
	})
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusCreated, map[string]any{
		"user_id": account.ID,
		"role":    string(account.Role),
	})
}

// loginRequest is the credential payload.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin opens a session.
func (s *Server) handleLogin(writer http.ResponseWriter, request *http.Request) {
	var payload loginRequest
	if _, err := decodeBody(request, &payload); err != nil {
		writeError(writer, request, err)
		return
	}
	credential, err := s.auth.Login(request.Context(), payload.Email, payload.Password)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, map[string]any{
		"token":      credential.Token,
		"expires_at": credential.ExpiresAt.UTC().Format(time.RFC3339),
		"actor":      newActorView(credential.Actor),
	})
}

// handleLogout revokes the current session.
func (s *Server) handleLogout(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	if err := s.auth.Logout(request.Context(), current); err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, map[string]any{"status": "revoked"})
}

// handleWhoAmI reports the authenticated principal.
func (s *Server) handleWhoAmI(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, map[string]any{"actor": newActorView(current)})
}

// handleGetMember returns a member profile.
func (s *Server) handleGetMember(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	profile, err := s.members.GetProfile(request.Context(), current, request.PathValue("memberID"))
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newProfileView(profile, s.clock.Now()))
}

// preferenceRequest is the partner criteria payload.
type preferenceRequest struct {
	SeekingGender   string   `json:"seeking_gender"`
	MinAge          int      `json:"min_age"`
	MaxAge          int      `json:"max_age"`
	Cities          []string `json:"cities"`
	MaritalStatuses []string `json:"marital_statuses"`
	MinEducation    string   `json:"min_education"`
}

// handleSavePreference stores the partner criteria of the acting member.
func (s *Server) handleSavePreference(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	if current.MemberID != request.PathValue("memberID") {
		writeError(writer, request,
			apperr.New(apperr.CodeForbidden, "a member may only change their own partner criteria"))
		return
	}
	var payload preferenceRequest
	if _, err := decodeBody(request, &payload); err != nil {
		writeError(writer, request, err)
		return
	}
	statuses := make([]member.MaritalStatus, 0, len(payload.MaritalStatuses))
	for _, status := range payload.MaritalStatuses {
		statuses = append(statuses, member.MaritalStatus(status))
	}
	preference, err := s.members.SavePreference(request.Context(), current, membersvc.PreferenceInput{
		SeekingGender:   member.Gender(payload.SeekingGender),
		MinAge:          payload.MinAge,
		MaxAge:          payload.MaxAge,
		Cities:          payload.Cities,
		MaritalStatuses: statuses,
		MinEducation:    member.Education(payload.MinEducation),
	})
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newPreferenceView(preference))
}

// statusRequest is the member status payload.
type statusRequest struct {
	Status string `json:"status"`
}

// handleSetMemberStatus changes the enrollment status of a member.
func (s *Server) handleSetMemberStatus(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	var payload statusRequest
	if _, err := decodeBody(request, &payload); err != nil {
		writeError(writer, request, err)
		return
	}
	memberID := request.PathValue("memberID")
	if err := s.members.SetStatus(request.Context(), current, memberID, member.Status(payload.Status)); err != nil {
		writeError(writer, request, err)
		return
	}
	profile, err := s.members.GetProfile(request.Context(), current, memberID)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, newProfileView(profile, s.clock.Now()))
}

// handleAllowanceMovements lists the allowance history of a member.
func (s *Server) handleAllowanceMovements(writer http.ResponseWriter, request *http.Request) {
	current, err := actor(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	entries, err := s.members.LedgerForMember(request.Context(), current, request.PathValue("memberID"))
	if err != nil {
		writeError(writer, request, err)
		return
	}
	items := make([]ledgerView, 0, len(entries))
	for _, entry := range entries {
		items = append(items, ledgerView{
			EntitlementID: entry.EntitlementID,
			Reason:        string(entry.Reason),
			DeltaReserved: entry.DeltaReserved,
			DeltaUsed:     entry.DeltaUsed,
			CreatedAt:     entry.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(writer, request, http.StatusOK, map[string]any{"items": items})
}
