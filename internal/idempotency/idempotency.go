// Package idempotency implements replay protection for mutating HTTP requests.
// A key is scoped by actor, method and path, so the same client key reused on a
// different endpoint is a conflict rather than a silent wrong replay.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/clock"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
)

// Guard stores and replays the outcome of mutating requests.
type Guard struct {
	repo  repository.IdempotencyRepository
	clock clock.Clock
	ttl   time.Duration
}

// NewGuard builds a Guard with the given retention window.
func NewGuard(repo repository.IdempotencyRepository, source clock.Clock, ttl time.Duration) *Guard {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Guard{repo: repo, clock: source, ttl: ttl}
}

// Request identifies one mutating call.
type Request struct {
	ActorID string
	Method  string
	Path    string
	Key     string
	Body    []byte
}

// Fingerprint returns the hash of the request body. Two calls sharing a key must
// also share a body, otherwise the second call is rejected instead of receiving
// the unrelated first response.
func (r Request) Fingerprint() string {
	sum := sha256.Sum256(r.Body)
	return hex.EncodeToString(sum[:])
}

// Replay is the stored outcome of a previous identical request.
type Replay struct {
	Status int
	Body   []byte
}

// Lookup returns the stored outcome of an identical earlier request. It returns
// ok=false when the key was never used, and an error when the key was used with a
// different payload or has expired.
func (g *Guard) Lookup(ctx context.Context, request Request) (Replay, bool, error) {
	if request.Key == "" {
		return Replay{}, false, nil
	}
	record, err := g.repo.Get(ctx, request.ActorID, request.Method, request.Path, request.Key)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return Replay{}, false, nil
		}
		return Replay{}, false, err
	}
	now := g.clock.Now()
	if !now.Before(record.ExpiresAt) {
		return Replay{}, false, apperr.Newf(apperr.CodeIdempotencyMismatch,
			"idempotency key %q has expired and cannot be replayed", request.Key)
	}
	if record.RequestHash != request.Fingerprint() {
		return Replay{}, false, apperr.Newf(apperr.CodeIdempotencyMismatch,
			"idempotency key %q was first used with a different request payload", request.Key)
	}
	body, err := base64.StdEncoding.DecodeString(record.ResponseB64)
	if err != nil {
		return Replay{}, false, apperr.Wrap(apperr.CodeInternal, "decode stored idempotent response", err)
	}
	return Replay{Status: record.Status, Body: body}, true, nil
}

// Remember stores the outcome of a successful request so that a retry replays it.
func (g *Guard) Remember(ctx context.Context, request Request, status int, body []byte) error {
	if request.Key == "" {
		return nil
	}
	now := g.clock.Now()
	record := repository.IdempotencyRecord{
		ActorID:     request.ActorID,
		Method:      request.Method,
		Path:        request.Path,
		Key:         request.Key,
		RequestHash: request.Fingerprint(),
		Status:      status,
		ResponseB64: base64.StdEncoding.EncodeToString(body),
		CreatedAt:   now,
		ExpiresAt:   now.Add(g.ttl),
	}
	if err := g.repo.Put(ctx, record); err != nil {
		// A concurrent identical request already stored the outcome. That is the
		// intended end state, so the duplicate insert is not an error for the
		// caller.
		if errors.Is(err, apperr.ErrConflict) {
			return nil
		}
		return err
	}
	return nil
}

// Sweep removes expired replay records and returns how many were deleted.
func (g *Guard) Sweep(ctx context.Context) (int, error) {
	return g.repo.DeleteExpiredBefore(ctx, g.clock.Now())
}

// ValidateKey rejects a client supplied key that cannot be stored safely.
func ValidateKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > 128 {
		return apperr.New(apperr.CodeInvalidArgument, "Idempotency-Key must not exceed 128 characters")
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == ':', r == '.':
		default:
			return apperr.New(apperr.CodeInvalidArgument,
				"Idempotency-Key may only contain letters, digits and the characters -_:.")
		}
	}
	return nil
}
