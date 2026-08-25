package matchsvc

import (
	"context"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/auditlog"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/audit"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/matching"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
)

// MaxBatchSize caps how many pairs one batch request may contain.
const MaxBatchSize = 20

// BatchItemResult is the outcome of one pair inside a batch request.
type BatchItemResult struct {
	Index          int
	FirstMemberID  string
	SecondMemberID string
	MatchID        string
	Accepted       bool
	ErrorCode      apperr.Code
	ErrorMessage   string
}

// BatchResult aggregates the per-item outcomes of a batch request.
type BatchResult struct {
	Requested int
	Accepted  int
	Rejected  int
	Items     []BatchItemResult
}

// ProposeBatch creates several introductions in one call.
//
// Each pair is its own unit of work: one rejected pair must not roll back the
// pairs that already succeeded, and it must not leave behind a reservation or a
// half-written match either. The caller receives a per-item verdict instead of a
// single all-or-nothing error.
func (s *Service) ProposeBatch(
	ctx context.Context, actor identity.Actor, inputs []ProposeInput,
) (BatchResult, error) {
	if err := actor.RequireRole(identity.RoleMatchmaker, identity.RoleAdmin); err != nil {
		return BatchResult{}, err
	}
	if len(inputs) == 0 {
		return BatchResult{}, apperr.New(apperr.CodeInvalidArgument, "at least one pair is required")
	}
	if len(inputs) > MaxBatchSize {
		return BatchResult{}, apperr.Newf(apperr.CodeInvalidArgument,
			"a batch may contain at most %d pairs", MaxBatchSize)
	}
	if err := rejectDuplicatePairs(inputs); err != nil {
		return BatchResult{}, err
	}

	// Read the concurrent-introduction counters once for the whole batch instead
	// of repeating the same aggregate query per pair.
	snapshot := liveCountSnapshot{}
	for _, input := range inputs {
		for _, memberID := range []string{input.FirstMemberID, input.SecondMemberID} {
			if _, seen := snapshot[memberID]; seen {
				continue
			}
			count, err := s.matches.CountActiveByMember(ctx, memberID)
			if err != nil {
				return BatchResult{}, err
			}
			snapshot[memberID] = count
		}
	}

	result := BatchResult{Requested: len(inputs), Items: make([]BatchItemResult, 0, len(inputs))}
	for index, input := range inputs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		first, second := matching.CanonicalPair(input.FirstMemberID, input.SecondMemberID)
		item := BatchItemResult{Index: index, FirstMemberID: first, SecondMemberID: second}
		detail, err := s.propose(ctx, actor, first, second, snapshot)
		if err != nil {
			// A cancelled or timed out context is an infrastructure failure, not a
			// business rejection, so it aborts the remaining items.
			code := apperr.CodeOf(err)
			if code == apperr.CodeCanceled || code == apperr.CodeDeadlineExceeded {
				return result, err
			}
			item.ErrorCode = code
			item.ErrorMessage = apperr.MessageOf(err)
			result.Rejected++
			result.Items = append(result.Items, item)
			continue
		}
		item.Accepted = true
		item.MatchID = detail.Match.ID
		result.Accepted++
		result.Items = append(result.Items, item)
	}

	logging.FromContext(ctx).Info("batch of introductions processed",
		"requested", result.Requested, "accepted", result.Accepted, "rejected", result.Rejected)
	if err := s.audit.Record(ctx, auditEntryForBatch(actor, result)); err != nil {
		return result, err
	}
	return result, nil
}

// rejectDuplicatePairs refuses a batch that lists the same pair twice, which
// would otherwise turn into a self-inflicted uniqueness conflict.
func rejectDuplicatePairs(inputs []ProposeInput) error {
	seen := make(map[string]int, len(inputs))
	for index, input := range inputs {
		if input.FirstMemberID == "" || input.SecondMemberID == "" {
			return apperr.Newf(apperr.CodeInvalidArgument,
				"pair %d must reference two members", index)
		}
		if input.FirstMemberID == input.SecondMemberID {
			return apperr.Newf(apperr.CodeInvalidArgument,
				"pair %d references the same member twice", index)
		}
		key := matching.PairKey(input.FirstMemberID, input.SecondMemberID)
		if previous, duplicate := seen[key]; duplicate {
			return apperr.Newf(apperr.CodeInvalidArgument,
				"pairs %d and %d reference the same two members", previous, index)
		}
		seen[key] = index
	}
	return nil
}

// auditEntryForBatch summarises a batch run for the operational trail.
func auditEntryForBatch(actor identity.Actor, result BatchResult) auditlog.Entry {
	accepted := make([]string, 0, result.Accepted)
	for _, item := range result.Items {
		if item.Accepted {
			accepted = append(accepted, item.MatchID)
		}
	}
	return auditlog.Entry{
		Actor:      actor,
		Action:     audit.ActionMatchProposed,
		ObjectType: audit.ObjectMatch,
		ObjectID:   "batch",
		Result:     audit.ResultSuccess,
		Detail: map[string]any{
			"requested":          result.Requested,
			"accepted":           result.Accepted,
			"rejected":           result.Rejected,
			"accepted_match_ids": accepted,
		},
	}
}
