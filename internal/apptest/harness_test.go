// Package apptest holds the integration suite of the service. It exercises the
// real SQLite database, the real service layer, the real HTTP router and the real
// background workers against a temporary database file.
package apptest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/app"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/config"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/member"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/notify"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/adminsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/authsvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/service/membersvc"
	"github.com/vance1852/heartbridge-matchmaking/internal/worker"
)

// anchor is the deterministic instant every test starts from. It is a Monday
// morning in UTC, which is Monday afternoon in the business timezone.
var anchor = time.Date(2026, time.March, 2, 2, 0, 0, 0, time.UTC)

// recordingSender captures delivered notifications and can be told to fail a
// fixed number of times, which is how the retry policy is exercised without any
// network access or sleeping.
type recordingSender struct {
	mu           sync.Mutex
	delivered    []notify.Job
	failures     int
	failWith     error
	failuresLeft int
}

// Send implements worker.Sender.
func (s *recordingSender) Send(_ context.Context, job notify.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failuresLeft > 0 {
		s.failuresLeft--
		s.failures++
		return s.failWith
	}
	s.delivered = append(s.delivered, job)
	return nil
}

// failNext makes the next count deliveries fail with the given error.
func (s *recordingSender) failNext(count int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failuresLeft = count
	s.failWith = err
}

// deliveredCount returns how many notifications were accepted.
func (s *recordingSender) deliveredCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.delivered)
}

// failureCount returns how many delivery attempts were rejected.
func (s *recordingSender) failureCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures
}

// harness is one fully wired application instance backed by a temporary file.
type harness struct {
	t        *testing.T
	app      *app.Application
	clk      *clock.Fixed
	sender   *recordingSender
	server   *httptest.Server
	dbPath   string
	cfg      config.Config
	adminTok string
	admin    identity.Actor
}

// TestMain silences the package level default logger so that the suite output
// only contains test results.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	os.Exit(m.Run())
}

// newHarness builds an application on a fresh temporary database.
func newHarness(t *testing.T) *harness {
	t.Helper()
	directory := t.TempDir()
	return newHarnessAt(t, filepath.Join(directory, "heartbridge.sqlite"))
}

// newHarnessAt builds an application on the given database path, which lets the
// restart test reopen the very same file.
func newHarnessAt(t *testing.T, dbPath string) *harness {
	t.Helper()
	cfg := config.Config{
		Environment:            "test",
		HTTPAddress:            "127.0.0.1:0",
		DatabasePath:           dbPath,
		DatabaseBusyTimeout:    5 * time.Second,
		DatabaseMaxOpenConns:   8,
		SessionTTL:             2 * time.Hour,
		IdempotencyTTL:         time.Hour,
		RequestTimeout:         10 * time.Second,
		ShutdownTimeout:        5 * time.Second,
		NotificationInterval:   time.Second,
		NotificationBatch:      50,
		SweeperInterval:        time.Second,
		SweeperBatch:           50,
		LogLevel:               "error",
		BootstrapAdminEmail:    "ops@heartbridge.test",
		BootstrapAdminPassword: "opsPassword2026",
	}
	fixed := clock.NewFixed(anchor)
	sender := &recordingSender{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	instance, err := app.New(context.Background(), cfg, app.Options{
		Clock:  fixed,
		Logger: logger,
		Sender: sender,
	})
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	server := httptest.NewServer(instance.Handler)
	h := &harness{
		t: t, app: instance, clk: fixed, sender: sender,
		server: server, dbPath: dbPath, cfg: cfg,
	}
	t.Cleanup(func() {
		server.Close()
		if err := instance.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})
	h.adminTok = h.login("ops@heartbridge.test", "opsPassword2026")
	resolved, err := instance.Auth.Authenticate(context.Background(), h.adminTok)
	if err != nil {
		t.Fatalf("authenticate operations token: %v", err)
	}
	h.admin = resolved
	return h
}

// ctx returns a request scoped context for direct service calls.
func (h *harness) ctx() context.Context { return context.Background() }

// adminActor returns the bootstrap operations principal. It is resolved once at
// setup time so that a test advancing the clock past the session lifetime can
// still perform operations actions.
func (h *harness) adminActor() identity.Actor { return h.admin }

// login exchanges credentials for a bearer token.
func (h *harness) login(email, password string) string {
	h.t.Helper()
	credential, err := h.app.Auth.Login(h.ctx(), email, password)
	if err != nil {
		h.t.Fatalf("login %s: %v", email, err)
	}
	return credential.Token
}

// staff creates a matchmaker or operations account and returns its actor.
func (h *harness) staff(email string, role identity.Role) (identity.Actor, string) {
	h.t.Helper()
	if _, err := h.app.Auth.RegisterStaff(h.ctx(), authsvc.RegisterStaffInput{
		Email:    email,
		Password: "staffPassword2026",
		Role:     role,
	}); err != nil {
		h.t.Fatalf("register staff %s: %v", email, err)
	}
	token := h.login(email, "staffPassword2026")
	actor, err := h.app.Auth.Authenticate(h.ctx(), token)
	if err != nil {
		h.t.Fatalf("authenticate staff %s: %v", email, err)
	}
	return actor, token
}

// memberFixture is an enrolled member ready to be introduced.
type memberFixture struct {
	Actor      identity.Actor
	Token      string
	MemberID   string
	Profile    member.Member
	Preference member.Preference
}

// memberSpec describes the member a test wants.
type memberSpec struct {
	Email         string
	DisplayName   string
	Gender        member.Gender
	SeekingGender member.Gender
	BirthYear     int
	City          string
	Marital       member.MaritalStatus
	Education     member.Education
	Plan          string
}

// withDefaults fills the fields a test did not care about.
func (s memberSpec) withDefaults() memberSpec {
	filled := s
	if filled.DisplayName == "" {
		filled.DisplayName = filled.Email
	}
	if filled.Gender == "" {
		filled.Gender = member.GenderFemale
	}
	if filled.SeekingGender == "" {
		if filled.Gender == member.GenderFemale {
			filled.SeekingGender = member.GenderMale
		} else {
			filled.SeekingGender = member.GenderFemale
		}
	}
	if filled.BirthYear == 0 {
		filled.BirthYear = 1994
	}
	if filled.City == "" {
		filled.City = "Hangzhou"
	}
	if filled.Marital == "" {
		filled.Marital = member.MaritalSingle
	}
	if filled.Education == "" {
		filled.Education = member.EducationBachelor
	}
	if filled.Plan == "" {
		filled.Plan = "starter"
	}
	return filled
}

// enrollMember registers a member, stores wide-open partner criteria and grants
// the requested service plan.
func (h *harness) enrollMember(spec memberSpec) memberFixture {
	h.t.Helper()
	filled := spec.withDefaults()
	_, profile, err := h.app.Auth.RegisterMember(h.ctx(), authsvc.RegisterMemberInput{
		Email:         filled.Email,
		Password:      "memberPassword2026",
		DisplayName:   filled.DisplayName,
		Gender:        filled.Gender,
		BirthDate:     time.Date(filled.BirthYear, time.June, 15, 0, 0, 0, 0, time.UTC),
		City:          filled.City,
		MaritalStatus: filled.Marital,
		Education:     filled.Education,
	})
	if err != nil {
		h.t.Fatalf("register member %s: %v", filled.Email, err)
	}
	token := h.login(filled.Email, "memberPassword2026")
	actor, err := h.app.Auth.Authenticate(h.ctx(), token)
	if err != nil {
		h.t.Fatalf("authenticate member %s: %v", filled.Email, err)
	}
	preference, err := h.app.Members.SavePreference(h.ctx(), actor, membersvc.PreferenceInput{
		SeekingGender:   filled.SeekingGender,
		MinAge:          22,
		MaxAge:          55,
		Cities:          []string{"Hangzhou", "Shanghai", "Suzhou"},
		MaritalStatuses: []member.MaritalStatus{member.MaritalSingle, member.MaritalDivorced},
		MinEducation:    member.EducationCollege,
	})
	if err != nil {
		h.t.Fatalf("save preference for %s: %v", filled.Email, err)
	}
	if filled.Plan != "none" {
		if _, err := h.app.Members.GrantEntitlement(h.ctx(), h.adminActor(), profile.ID, filled.Plan); err != nil {
			h.t.Fatalf("grant plan %s to %s: %v", filled.Plan, filled.Email, err)
		}
	}
	// Re-authenticate so the actor carries the member id resolved after the
	// profile became active.
	actor, err = h.app.Auth.Authenticate(h.ctx(), token)
	if err != nil {
		h.t.Fatalf("re-authenticate member %s: %v", filled.Email, err)
	}
	return memberFixture{
		Actor: actor, Token: token, MemberID: profile.ID,
		Profile: profile, Preference: preference,
	}
}

// publishSlot creates a bookable venue window relative to the frozen clock.
func (h *harness) publishSlot(code string, offset time.Duration, duration time.Duration, capacity int) string {
	h.t.Helper()
	start := h.clk.Now().Add(offset)
	slot, err := h.app.Admin.PublishSlot(h.ctx(), h.adminActor(), adminsvc.SlotInput{
		VenueCode: code,
		VenueName: "Tea house " + code,
		City:      "Hangzhou",
		StartAt:   start,
		EndAt:     start.Add(duration),
		Capacity:  capacity,
	})
	if err != nil {
		h.t.Fatalf("publish slot %s: %v", code, err)
	}
	return slot.ID
}

// apiResponse is a decoded HTTP response.
type apiResponse struct {
	Status  int
	Headers http.Header
	Body    map[string]any
	Raw     []byte
}

// errorCode returns the stable business code of an error response.
func (r apiResponse) errorCode() string {
	payload, ok := r.Body["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := payload["code"].(string)
	return code
}

// requestID returns the correlation id of an error response body.
func (r apiResponse) errorRequestID() string {
	payload, ok := r.Body["error"].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := payload["request_id"].(string)
	return value
}

// str reads a top level string field of the response body.
func (r apiResponse) str(key string) string {
	value, _ := r.Body[key].(string)
	return value
}

// number reads a top level numeric field of the response body.
func (r apiResponse) number(key string) float64 {
	value, _ := r.Body[key].(float64)
	return value
}

// do performs an HTTP call against the test server.
func (h *harness) do(method, path, token string, payload any, headers map[string]string) apiResponse {
	h.t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatalf("encode request payload: %v", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, h.server.URL+path, body)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := h.server.Client().Do(request)
	if err != nil {
		h.t.Fatalf("perform request: %v", err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatalf("read response body: %v", err)
	}
	decoded := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			h.t.Fatalf("decode response body %q: %v", string(raw), err)
		}
	}
	return apiResponse{Status: response.StatusCode, Headers: response.Header, Body: decoded, Raw: raw}
}

// drainNotifications runs the dispatcher until the outbox has nothing due left.
func (h *harness) drainNotifications() int {
	h.t.Helper()
	total := 0
	for round := 0; round < 10; round++ {
		claimed, err := h.app.Dispatcher.Tick(h.ctx())
		if err != nil {
			h.t.Fatalf("dispatcher tick: %v", err)
		}
		total += claimed
		if claimed == 0 {
			break
		}
	}
	return total
}

// pendingNotifications counts the rows still waiting for delivery.
func (h *harness) pendingNotifications() int {
	h.t.Helper()
	count, err := h.app.Repositories.Notifications.CountByState(h.ctx(), notify.StatePending)
	if err != nil {
		h.t.Fatalf("count pending notifications: %v", err)
	}
	return count
}

// senderPlaceholder keeps the worker import meaningful for readers of this file.
var _ worker.Sender = (*recordingSender)(nil)
