package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/idempotency"
	"github.com/vance1852/heartbridge-matchmaking/internal/middleware"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/adminsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/authsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/matchsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/membersvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/schedulesvc"
)

// ReadinessProbe reports whether the required dependencies are usable.
type ReadinessProbe func(ctx context.Context) error

// Server holds the collaborators of the HTTP layer.
type Server struct {
	auth      *authsvc.Service
	members   *membersvc.Service
	matches   *matchsvc.Service
	schedule  *schedulesvc.Service
	admin     *adminsvc.Service
	guard     *idempotency.Guard
	clock     clock.Clock
	readiness ReadinessProbe
	version   string
}

// Dependencies bundles the collaborators of the HTTP layer.
type Dependencies struct {
	Auth      *authsvc.Service
	Members   *membersvc.Service
	Matches   *matchsvc.Service
	Schedule  *schedulesvc.Service
	Admin     *adminsvc.Service
	Guard     *idempotency.Guard
	Clock     clock.Clock
	Readiness ReadinessProbe
	Version   string
}

// NewServer builds the HTTP layer.
func NewServer(deps Dependencies) *Server {
	version := deps.Version
	if version == "" {
		version = "dev"
	}
	return &Server{
		auth:      deps.Auth,
		members:   deps.Members,
		matches:   deps.Matches,
		schedule:  deps.Schedule,
		admin:     deps.Admin,
		guard:     deps.Guard,
		clock:     deps.Clock,
		readiness: deps.Readiness,
		version:   version,
	}
}

// Handler builds the fully decorated router.
func (s *Server) Handler(requestTimeout time.Duration) http.Handler {
	mux := http.NewServeMux()
	s.registerPublicRoutes(mux)
	s.registerMemberRoutes(mux)
	s.registerMatchmakerRoutes(mux)
	s.registerAdminRoutes(mux)

	return middleware.Chain(mux,
		middleware.RequestID(),
		middleware.AccessLog(),
		middleware.Recover(writeError),
		middleware.Timeout(requestTimeout),
		normalizeRouterErrors,
		middleware.Authenticate(s.auth, writeError),
	)
}

// registerPublicRoutes wires the endpoints reachable without a session.
func (s *Server) registerPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleLiveness)
	mux.HandleFunc("GET /readyz", s.handleReadiness)
	mux.HandleFunc("POST /api/v1/auth/register", s.handleRegisterMember)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
}

// guarded wraps a handler so that only authenticated actors reach it.
func (s *Server) guarded(handler http.HandlerFunc) http.Handler {
	return middleware.RequireActor(writeError)(handler)
}

// roleGuarded wraps a handler so that only the accepted roles reach it.
func (s *Server) roleGuarded(handler http.HandlerFunc, accepted ...identity.Role) http.Handler {
	return middleware.RequireRole(writeError, accepted...)(handler)
}

// registerMemberRoutes wires the endpoints any authenticated actor may reach.
func (s *Server) registerMemberRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/auth/logout", s.guarded(s.handleLogout))
	mux.Handle("GET /api/v1/me", s.guarded(s.handleWhoAmI))
	mux.Handle("GET /api/v1/members/{memberID}", s.guarded(s.handleGetMember))
	mux.Handle("PUT /api/v1/members/{memberID}/preference",
		s.roleGuarded(s.handleSavePreference, identity.RoleMember))
	mux.Handle("POST /api/v1/members/{memberID}/status", s.guarded(s.handleSetMemberStatus))
	mux.Handle("GET /api/v1/members/{memberID}/allowance-movements",
		s.guarded(s.handleAllowanceMovements))
	mux.Handle("GET /api/v1/matches", s.guarded(s.handleListMatches))
	mux.Handle("GET /api/v1/matches/{matchID}", s.guarded(s.handleGetMatch))
	mux.Handle("POST /api/v1/matches/{matchID}/consent",
		s.roleGuarded(s.handleConsent, identity.RoleMember))
	mux.Handle("POST /api/v1/matches/{matchID}/cancel", s.guarded(s.handleCancelMatch))
	mux.Handle("GET /api/v1/meetups/{meetupID}", s.guarded(s.handleGetMeetup))
	mux.Handle("POST /api/v1/meetups/{meetupID}/feedback",
		s.roleGuarded(s.handleSubmitFeedback, identity.RoleMember))
}

// registerMatchmakerRoutes wires the consultant surface.
func (s *Server) registerMatchmakerRoutes(mux *http.ServeMux) {
	staff := []identity.Role{identity.RoleMatchmaker, identity.RoleAdmin}
	mux.Handle("POST /api/v1/matches", s.roleGuarded(s.handleProposeMatch, staff...))
	mux.Handle("POST /api/v1/matches/batch", s.roleGuarded(s.handleProposeBatch, staff...))
	mux.Handle("POST /api/v1/matches/{matchID}/meetup", s.roleGuarded(s.handleBookMeetup, staff...))
	mux.Handle("POST /api/v1/meetups/{meetupID}/check-in", s.roleGuarded(s.handleCheckIn, staff...))
	mux.Handle("POST /api/v1/meetups/{meetupID}/complete", s.roleGuarded(s.handleCompleteMeetup, staff...))
	mux.Handle("POST /api/v1/meetups/{meetupID}/cancel", s.roleGuarded(s.handleCancelMeetup, staff...))
	mux.Handle("GET /api/v1/venue-slots", s.roleGuarded(s.handleListSlots, staff...))
	mux.Handle("GET /api/v1/service-plans", s.roleGuarded(s.handleListPlans, staff...))
}

// registerAdminRoutes wires the operations surface.
func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	admin := identity.RoleAdmin
	mux.Handle("POST /api/v1/admin/staff", s.roleGuarded(s.handleRegisterStaff, admin))
	mux.Handle("POST /api/v1/admin/service-plans", s.roleGuarded(s.handleUpsertPlan, admin))
	mux.Handle("POST /api/v1/admin/venue-slots", s.roleGuarded(s.handlePublishSlot, admin))
	mux.Handle("POST /api/v1/admin/members/{memberID}/entitlements",
		s.roleGuarded(s.handleGrantEntitlement, admin))
	mux.Handle("GET /api/v1/admin/audit-events", s.roleGuarded(s.handleListAudit, admin))
}

// actor extracts the authenticated principal of a guarded request.
func actor(request *http.Request) (identity.Actor, error) {
	value, ok := middleware.ActorFromContext(request.Context())
	if !ok {
		return identity.Actor{}, apperr.New(apperr.CodeUnauthenticated, "a valid session token is required")
	}
	return value, nil
}

// idempotent runs a mutating handler under replay protection. The stored outcome
// of an identical earlier call is replayed instead of performing the change twice.
func (s *Server) idempotent(
	writer http.ResponseWriter, request *http.Request, body []byte, current identity.Actor,
	perform func() (int, any, error),
) {
	key := request.Header.Get("Idempotency-Key")
	if err := idempotency.ValidateKey(key); err != nil {
		writeError(writer, request, err)
		return
	}
	replayRequest := idempotency.Request{
		ActorID: current.UserID,
		Method:  request.Method,
		Path:    request.URL.Path,
		Key:     key,
		Body:    body,
	}
	if key != "" {
		replay, found, err := s.guard.Lookup(request.Context(), replayRequest)
		if err != nil {
			writeError(writer, request, err)
			return
		}
		if found {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.Header().Set("Idempotent-Replay", "true")
			writer.WriteHeader(replay.Status)
			if _, err := writer.Write(replay.Body); err != nil {
				writeError(writer, request, err)
			}
			return
		}
	}

	status, payload, err := perform()
	if err != nil {
		writeError(writer, request, err)
		return
	}
	encoded, err := encodePayload(payload)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	if key != "" {
		if err := s.guard.Remember(request.Context(), replayRequest, status, encoded); err != nil {
			writeError(writer, request, err)
			return
		}
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if _, err := writer.Write(encoded); err != nil {
		writeError(writer, request, err)
	}
}
