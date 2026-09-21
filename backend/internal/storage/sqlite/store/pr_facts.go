package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// GetDisplayPRFactsForSession returns the PR snapshot that should represent a
// session in derived display status: active PRs first, otherwise the newest
// historical PR. ok=false means the session has no associated PRs.
func (s *Store) GetDisplayPRFactsForSession(ctx context.Context, id domain.SessionID) (domain.PRFacts, bool, error) {
	r, err := s.qr.GetDisplayPRFactsBySession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PRFacts{}, false, nil
	}
	if err != nil {
		return domain.PRFacts{}, false, fmt.Errorf("display pr facts for %s: %w", id, err)
	}
	return prFactsFromGen(r), true, nil
}

// ListPRFactsForSession returns the PR snapshot for every PR a session owns
// (open, merged, and closed), newest first. The status aggregator filters and
// builds stacks from these; an empty slice means the session has no PRs.
func (s *Store) ListPRFactsForSession(ctx context.Context, id domain.SessionID) ([]domain.PRFacts, error) {
	rows, err := s.qr.ListPRFactsBySession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list pr facts for %s: %w", id, err)
	}
	out := make([]domain.PRFacts, 0, len(rows))
	for _, r := range rows {
		out = append(out, prFactsFromListRow(r))
	}
	return out, nil
}

// ListPRFactsForSessions batches ListPRFactsForSession for session-list reads.
// It returns every requested session id in the map, using an empty slice when
// a session owns no PRs.
func (s *Store) ListPRFactsForSessions(ctx context.Context, ids []domain.SessionID) (map[domain.SessionID][]domain.PRFacts, error) {
	out := make(map[domain.SessionID][]domain.PRFacts, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("marshal session ids: %w", err)
	}
	rows, err := s.qr.ListPRFactsBySessions(ctx, string(encoded))
	if err != nil {
		return nil, fmt.Errorf("list pr facts for sessions: %w", err)
	}
	for _, r := range rows {
		out[r.SessionID] = append(out[r.SessionID], prFactsFromBatchRow(r))
	}
	for _, id := range ids {
		if out[id] == nil {
			out[id] = []domain.PRFacts{}
		}
	}
	return out, nil
}

func prFactsFromListRow(r gen.ListPRFactsBySessionRow) domain.PRFacts {
	return domain.PRFacts{
		URL:                      r.URL,
		Number:                   int(r.Number),
		Draft:                    r.PRState == domain.PRStateDraft,
		Merged:                   r.PRState == domain.PRStateMerged,
		Closed:                   r.PRState == domain.PRStateClosed,
		CI:                       r.CIState,
		Review:                   r.ReviewDecision,
		Mergeability:             r.Mergeability,
		ReviewComments:           r.ReviewComments,
		SourceBranch:             r.SourceBranch,
		TargetBranch:             r.TargetBranch,
		HeadSHA:                  r.HeadSha,
		UpdatedAt:                r.UpdatedAt,
		ExternalComments:         r.ExternalComments,
		ExternalApproved:         r.ExternalApproved,
		ExternalChangesRequested: r.ExternalChangesRequested,
	}
}

func prFactsFromBatchRow(r gen.ListPRFactsBySessionsRow) domain.PRFacts {
	return domain.PRFacts{
		URL:                      r.URL,
		Number:                   int(r.Number),
		Draft:                    r.PRState == domain.PRStateDraft,
		Merged:                   r.PRState == domain.PRStateMerged,
		Closed:                   r.PRState == domain.PRStateClosed,
		CI:                       r.CIState,
		Review:                   r.ReviewDecision,
		Mergeability:             r.Mergeability,
		ReviewComments:           r.ReviewComments,
		SourceBranch:             r.SourceBranch,
		TargetBranch:             r.TargetBranch,
		HeadSHA:                  r.HeadSha,
		UpdatedAt:                r.UpdatedAt,
		ExternalComments:         r.ExternalComments,
		ExternalApproved:         r.ExternalApproved,
		ExternalChangesRequested: r.ExternalChangesRequested,
	}
}

func prFactsFromGen(r gen.GetDisplayPRFactsBySessionRow) domain.PRFacts {
	state := r.PRState
	return domain.PRFacts{
		URL:            r.URL,
		Number:         int(r.Number),
		Draft:          state == domain.PRStateDraft,
		Merged:         state == domain.PRStateMerged,
		Closed:         state == domain.PRStateClosed,
		CI:             r.CIState,
		Review:         r.ReviewDecision,
		Mergeability:   r.Mergeability,
		ReviewComments: r.ReviewComments,
		HeadSHA:        r.HeadSha,
		UpdatedAt:      r.UpdatedAt,
	}
}
