package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// maxWorkerErrorsPerSession bounds the watchdog log: the writer prunes each
// session to its newest rows, so one wedged session cannot grow the table
// without bound and list reads stay small.
const maxWorkerErrorsPerSession = 25

// maxWorkerErrorMessageLen bounds one recorded excerpt. Provider errors can
// carry stack traces; the watchdog only needs the signal, and the full text
// stays on the failed turn row it was recorded from.
const maxWorkerErrorMessageLen = 2000

// RecordSessionWorkerError appends one durable worker-error fact and prunes
// the session back to its newest rows. The message is sanitized and bounded;
// an empty message records nothing. A record failure is returned so callers
// notice a broken watchdog log rather than silently losing coverage.
func (s *Store) RecordSessionWorkerError(ctx context.Context, sessionID domain.SessionID, source, turnID, message string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return recordSessionWorkerError(ctx, s.qw, sessionID, source, turnID, message, at)
}

// recordSessionWorkerError is the lock-held core shared by RecordSessionWorkerError
// and the conversation-turn failure paths, which already hold the writer (or a
// projection transaction) and pass its query set in.
func recordSessionWorkerError(ctx context.Context, q *gen.Queries, sessionID domain.SessionID, source, turnID, message string, at time.Time) error {
	message = boundWorkerErrorMessage(message)
	if message == "" {
		return nil
	}
	if err := q.RecordSessionWorkerError(ctx, gen.RecordSessionWorkerErrorParams{
		SessionID:    string(sessionID),
		Source:       source,
		TurnID:       turnID,
		ErrorMessage: message,
		OccurredAt:   at.UTC(),
	}); err != nil {
		return fmt.Errorf("record worker error for %s: %w", sessionID, err)
	}
	count, err := q.CountSessionWorkerErrors(ctx, string(sessionID))
	if err != nil {
		return fmt.Errorf("count worker errors for %s: %w", sessionID, err)
	}
	if excess := count - maxWorkerErrorsPerSession; excess > 0 {
		if _, err := q.DeleteOldestSessionWorkerErrors(ctx, gen.DeleteOldestSessionWorkerErrorsParams{
			SessionID: string(sessionID),
			Limit:     excess,
		}); err != nil {
			return fmt.Errorf("prune worker errors for %s: %w", sessionID, err)
		}
	}
	return nil
}

// boundWorkerErrorMessage sanitizes control characters and caps the excerpt so
// provider stack traces cannot bloat the watchdog log. Overlong messages are
// truncated on a rune boundary with an ellipsis marker.
func boundWorkerErrorMessage(message string) string {
	message = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, message))
	if len(message) <= maxWorkerErrorMessageLen {
		return message
	}
	cut := message[:maxWorkerErrorMessageLen]
	for cut != "" && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "...[truncated]"
}

// LatestSessionWorkerErrors returns a session's recorded worker errors, newest
// first. The writer prunes every session to its newest rows, so the result is
// already bounded. Errors for rolled-back turns are excluded by the query:
// rollback discards that history provider-side, so its failure is moot.
func (s *Store) LatestSessionWorkerErrors(ctx context.Context, sessionID domain.SessionID) ([]domain.WorkerErrorEvent, error) {
	rows, err := s.qr.SelectRecentSessionWorkerErrors(ctx, string(sessionID))
	if err != nil {
		return nil, fmt.Errorf("list worker errors for %s: %w", sessionID, err)
	}
	out := make([]domain.WorkerErrorEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, workerErrorFromGen(domain.SessionID(r.SessionID), r.Source, r.TurnID, r.ErrorMessage, r.OccurredAt))
	}
	return out, nil
}

// LatestSessionWorkerErrorsForSessions batches LatestSessionWorkerErrors for
// session-list reads. It returns every requested session id in the map, using
// an empty slice when a session recorded no (live) errors.
func (s *Store) LatestSessionWorkerErrorsForSessions(ctx context.Context, ids []domain.SessionID) (map[domain.SessionID][]domain.WorkerErrorEvent, error) {
	out := make(map[domain.SessionID][]domain.WorkerErrorEvent, len(ids))
	for _, id := range ids {
		out[id] = []domain.WorkerErrorEvent{}
	}
	if len(ids) == 0 {
		return out, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("marshal session ids: %w", err)
	}
	rows, err := s.qr.SelectRecentSessionWorkerErrorsForSessions(ctx, string(encoded))
	if err != nil {
		return nil, fmt.Errorf("list worker errors for sessions: %w", err)
	}
	for _, r := range rows {
		id := domain.SessionID(r.SessionID)
		out[id] = append(out[id], workerErrorFromGen(id, r.Source, r.TurnID, r.ErrorMessage, r.OccurredAt))
	}
	return out, nil
}

func workerErrorFromGen(sessionID domain.SessionID, source, turnID, message string, at time.Time) domain.WorkerErrorEvent {
	return domain.WorkerErrorEvent{
		SessionID:  sessionID,
		Source:     source,
		TurnID:     turnID,
		Message:    message,
		OccurredAt: at.UTC(),
	}
}
