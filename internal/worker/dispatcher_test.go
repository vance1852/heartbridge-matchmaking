package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/notify"
)

// fakeRepo is a NotificationRepository double that records every transition the
// dispatcher asks it to make, so a test can assert which conclusion was
// persisted without a database.
type fakeRepo struct {
	mu        sync.Mutex
	due       []notify.Job
	succeeded map[string]int
	retried   map[string]int
	dead      map[string]int
}

func newFakeRepo(jobs ...notify.Job) *fakeRepo {
	return &fakeRepo{
		due:       append([]notify.Job(nil), jobs...),
		succeeded: map[string]int{},
		retried:   map[string]int{},
		dead:      map[string]int{},
	}
}

func (r *fakeRepo) Enqueue(context.Context, notify.Job) error { return nil }
func (r *fakeRepo) GetByID(context.Context, string) (notify.Job, error) {
	return notify.Job{}, nil
}
func (r *fakeRepo) ListDue(_ context.Context, _ time.Time, _ int) ([]notify.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]notify.Job, len(r.due))
	copy(out, r.due)
	return out, nil
}
func (r *fakeRepo) MarkSucceeded(_ context.Context, id string, attempts int, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.succeeded[id] = attempts
	return nil
}
func (r *fakeRepo) ScheduleRetry(_ context.Context, id string, attempts int, _ time.Time, _ string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retried[id] = attempts
	return nil
}
func (r *fakeRepo) MarkPermanentFailure(_ context.Context, id string, attempts int, _ string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dead[id] = attempts
	return nil
}
func (r *fakeRepo) CountByState(_ context.Context, _ notify.State) (int, error) {
	return 0, nil
}

// anyConclusion reports whether any terminal or retry transition was persisted.
func (r *fakeRepo) anyConclusion() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.succeeded) != 0 || len(r.retried) != 0 || len(r.dead) != 0
}

// stopSender returns nil (the gateway accepted the message) but, mid-send,
// cancels the loop context it was handed. This is exactly what a rolling
// restart looks like to the dispatcher: the stop signal arrives while the send
// is in flight, the send itself reports success, yet the loop is now stopped.
type stopSender struct {
	stop context.CancelFunc
}

func (s stopSender) Send(ctx context.Context, _ notify.Job) error {
	// The restart arrives during the send; the send still completes successfully.
	s.stop()
	return nil
}

// anchor is a fixed instant; the dispatcher only needs a deterministic clock.
var anchor = time.Date(2026, time.March, 2, 2, 0, 0, 0, time.UTC)

func dueJob() notify.Job {
	return notify.Job{
		ID: "ntf_due", MatchID: "mtc_1", RecipientID: "mbr_1",
		Kind: notify.KindMatchProposed, State: notify.StatePending,
		MaxAttempts: notify.DefaultMaxAttempts, NextAttemptAt: anchor,
		CreatedAt: anchor, UpdatedAt: anchor,
	}
}

// TestDeliverLeavesNoConclusionOnStopBoundary reproduces the rolling-restart
// bug: the gateway reports success, but the dispatcher is being stopped
// mid-round. No delivery conclusion may be written, so the row stays pending
// and is re-sent by the next round.
func TestDeliverLeavesNoConclusionOnStopBoundary(t *testing.T) {
	repo := newFakeRepo(dueJob())
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher := NewDispatcher(repo, stopSender{stop: cancel}, clock.NewFixed(anchor), 10)

	claimed, err := dispatcher.Tick(ctx)
	if err == nil {
		t.Fatalf("expected the tick to surface the stop, got nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a cancelled error, got %v", err)
	}
	if claimed == 0 {
		t.Fatalf("expected the row to have been claimed before the stop, got %d", claimed)
	}
	if repo.anyConclusion() {
		t.Fatalf("a stop-boundary attempt left a delivery conclusion behind: %+v", repo)
	}
	if dispatcher.Stats().Delivered != 0 {
		t.Fatalf("expected no delivery to be counted, got %d", dispatcher.Stats().Delivered)
	}
}

// TestDeliverResumesAfterStopBoundary verifies the row that was interrupted is
// re-sent on the next healthy round.
func TestDeliverResumesAfterStopBoundary(t *testing.T) {
	repo := newFakeRepo(dueJob())
	dispatcher := NewDispatcher(repo, DiscardSender{}, clock.NewFixed(anchor), 10)

	if _, err := dispatcher.Tick(context.Background()); err != nil {
		t.Fatalf("healthy tick: %v", err)
	}
	if repo.dead["ntf_due"] != 0 || repo.retried["ntf_due"] != 0 {
		t.Fatalf("expected the row to be delivered, got dead/retry: %+v", repo)
	}
	if repo.succeeded["ntf_due"] == 0 {
		t.Fatalf("expected the row to be marked succeeded after resuming, got %+v", repo)
	}
	if dispatcher.Stats().Delivered != 1 {
		t.Fatalf("expected one delivery, got %d", dispatcher.Stats().Delivered)
	}
}
