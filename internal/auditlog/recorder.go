// Package auditlog turns a service level action into a persisted audit row. It
// deliberately depends on the repository contract rather than on a database so
// that the trail is written inside the caller's transaction.
package auditlog

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
	"github.com/vance1852/heartbridge-matchmaking/internal/security"
)

// Recorder appends audit rows.
type Recorder struct {
	repo  repository.AuditRepository
	clock clock.Clock
}

// NewRecorder builds a Recorder.
func NewRecorder(repo repository.AuditRepository, source clock.Clock) *Recorder {
	return &Recorder{repo: repo, clock: source}
}

// Entry is the input of one audit row.
type Entry struct {
	Actor      identity.Actor
	Action     audit.Action
	ObjectType audit.ObjectType
	ObjectID   string
	Result     audit.Result
	Detail     map[string]any
}

// Record appends one audit row, taking the request id from the context so that a
// row can always be traced back to the originating request.
func (r *Recorder) Record(ctx context.Context, entry Entry) error {
	id, err := security.NewID("aud")
	if err != nil {
		return err
	}
	event := audit.Event{
		ID:         id,
		ActorID:    entry.Actor.UserID,
		ActorRole:  string(entry.Actor.Role),
		Action:     entry.Action,
		ObjectType: entry.ObjectType,
		ObjectID:   entry.ObjectID,
		Result:     entry.Result,
		Detail:     encodeDetail(entry.Detail),
		RequestID:  logging.RequestIDFromContext(ctx),
		CreatedAt:  r.clock.Now(),
	}
	return r.repo.Append(ctx, event)
}

// Success records a completed operation.
func (r *Recorder) Success(
	ctx context.Context, actor identity.Actor, action audit.Action,
	objectType audit.ObjectType, objectID string, detail map[string]any,
) error {
	return r.Record(ctx, Entry{
		Actor:      actor,
		Action:     action,
		ObjectType: objectType,
		ObjectID:   objectID,
		Result:     audit.ResultSuccess,
		Detail:     detail,
	})
}

// Rejected records an operation refused by a business rule. It never fails the
// caller: a rejected action must still surface its own error, so a failure to
// write the trail is logged rather than propagated.
func (r *Recorder) Rejected(
	ctx context.Context, actor identity.Actor, action audit.Action,
	objectType audit.ObjectType, objectID string, reason error,
) {
	detail := map[string]any{}
	if reason != nil {
		detail["reason"] = reason.Error()
	}
	if err := r.Record(ctx, Entry{
		Actor:      actor,
		Action:     action,
		ObjectType: objectType,
		ObjectID:   objectID,
		Result:     audit.ResultRejected,
		Detail:     detail,
	}); err != nil {
		logging.FromContext(ctx).Warn("could not persist rejection audit row",
			"action", string(action), "object_id", objectID, "error", err.Error())
	}
}

// List exposes the trail to the operations console.
func (r *Recorder) List(ctx context.Context, filter repository.AuditFilter) (repository.AuditPage, error) {
	return r.repo.List(ctx, filter)
}

// encodeDetail renders the detail map deterministically. Sorted keys keep the
// stored payload comparable between runs, which matters for the tests that
// assert on audit content.
func encodeDetail(detail map[string]any) string {
	if len(detail) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(detail))
	for key := range detail {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	builder := strings.Builder{}
	builder.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			continue
		}
		builder.Write(encodedKey)
		builder.WriteByte(':')
		encodedValue, err := json.Marshal(normalizeValue(detail[key]))
		if err != nil {
			builder.WriteString(`"<unencodable>"`)
			continue
		}
		builder.Write(encodedValue)
	}
	builder.WriteByte('}')
	return builder.String()
}

// normalizeValue renders instants in the fixed UTC form used everywhere else.
func normalizeValue(value any) any {
	if instant, ok := value.(time.Time); ok {
		return instant.UTC().Format(time.RFC3339Nano)
	}
	return value
}
