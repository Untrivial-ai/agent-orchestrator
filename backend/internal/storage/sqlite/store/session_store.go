package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ---- sessions ----

// CreateSession assigns the per-project identity ("{project}-{num}") and inserts
// the record, returning it with ID populated. The next-num read and the insert
// run on the writer connection under writeMu, so two concurrent creates in the
// same project can't collide on num.
func (s *Store) CreateSession(ctx context.Context, rec domain.SessionRecord) (domain.SessionRecord, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	num, err := s.qw.NextSessionNum(ctx, rec.ProjectID)
	if err != nil {
		return domain.SessionRecord{}, fmt.Errorf("next session num for %s: %w", rec.ProjectID, err)
	}
	rec.ID = domain.SessionID(fmt.Sprintf("%s-%d", rec.ProjectID, num))
	if err := s.qw.InsertSession(ctx, recordToInsert(rec, num)); err != nil {
		return domain.SessionRecord{}, fmt.Errorf("insert session %s: %w", rec.ID, err)
	}
	return rec, nil
}

// UpdateSession writes the full mutable state of an existing session. The
// id/project/num/created_at are immutable and not touched here.
func (s *Store) UpdateSession(ctx context.Context, rec domain.SessionRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.UpdateSession(ctx, recordToUpdate(rec))
}

// UpdateSessionFromActivitySignal projects activity-derived session metadata
// only when the signal still belongs to the session's active harness launch.
func (s *Store) UpdateSessionFromActivitySignal(ctx context.Context, rec domain.SessionRecord) (bool, error) {
	activity := normalActivity(rec.Activity, rec.UpdatedAt)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.UpdateSessionFromActivitySignal(ctx, gen.UpdateSessionFromActivitySignalParams{
		ActivityState:                activity.State,
		ActivityLastAt:               activity.LastActivityAt,
		FirstSignalAt:                timeToNullTime(rec.FirstSignalAt),
		AgentSessionID:               rec.Metadata.AgentSessionID,
		AgentSessionIDLaunchID:       rec.Metadata.AgentSessionIDLaunchID,
		LatestUserPrompt:             rec.Metadata.LatestUserPrompt,
		LatestUserPromptAt:           timeToNullTime(rec.Metadata.LatestUserPromptAt),
		LatestAssistantUpdate:        rec.Metadata.LatestAssistantUpdate,
		NativeTranscriptPath:         rec.Metadata.NativeTranscriptPath,
		UpdatedAt:                    rec.UpdatedAt,
		ID:                           rec.ID,
		ExpectedHarness:              rec.Harness,
		ExpectedSessionMode:          domain.NormalizeSessionMode(rec.Mode),
		ExpectedRuntimeLaunchID:      rec.Metadata.RuntimeLaunchID,
		ExpectedControllerGeneration: rec.Metadata.ControllerGeneration,
	})
	if err != nil {
		return false, fmt.Errorf("update session %s from activity signal: %w", rec.ID, err)
	}
	return rows > 0, nil
}

// RecordSessionLatestUserPrompt persists the latest real user direction without
// rewriting lifecycle state that another goroutine may have advanced.
func (s *Store) RecordSessionLatestUserPrompt(ctx context.Context, id domain.SessionID, prompt string, updatedAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.RecordSessionLatestUserPrompt(ctx, gen.RecordSessionLatestUserPromptParams{
		LatestUserPrompt: prompt,
		UpdatedAt:        timeToNullTime(updatedAt),
		ID:               id,
	})
	if err != nil {
		return false, fmt.Errorf("record latest user prompt for session %s: %w", id, err)
	}
	return rows > 0, nil
}

// ClaimChatControllerGeneration makes generation the only Chat controller that
// may project provider events for this session. The narrow update avoids writing
// a stale full SessionRecord over lifecycle facts changed by another goroutine.
func (s *Store) ClaimChatControllerGeneration(
	ctx context.Context,
	id domain.SessionID,
	generation string,
	updatedAt time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.ClaimChatControllerGeneration(ctx, gen.ClaimChatControllerGenerationParams{
		ControllerGeneration: generation,
		UpdatedAt:            updatedAt,
		ID:                   id,
	})
	if err != nil {
		return fmt.Errorf("claim chat controller generation for %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("claim chat controller generation for %s: chat session not found", id)
	}
	return nil
}

// RenameSession updates only the user-facing display name for an existing
// session. It returns ok=false when the session id does not exist. The
// sessions_cdc_update trigger fans out a session_updated CDC event when the
// display name actually changes.
func (s *Store) RenameSession(ctx context.Context, id domain.SessionID, displayName string, updatedAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.RenameSession(ctx, gen.RenameSessionParams{
		ID:          id,
		DisplayName: displayName,
		UpdatedAt:   updatedAt,
	})
	if err != nil {
		return false, fmt.Errorf("rename session %s: %w", id, err)
	}
	return rows > 0, nil
}

// SetSessionPinned updates the pinned status of a session.
func (s *Store) SetSessionPinned(ctx context.Context, id domain.SessionID, isPinned bool, pinnedAt *time.Time, updatedAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.SetSessionPinned(ctx, gen.SetSessionPinnedParams{
		ID:        id,
		IsPinned:  isPinned,
		PinnedAt:  timePtrToNullTime(pinnedAt),
		UpdatedAt: updatedAt,
	})
	if err != nil {
		return false, fmt.Errorf("set session pinned %s: %w", id, err)
	}
	return rows > 0, nil
}

// SetSessionPreviewURL updates only the browser preview URL for an existing
// session. It returns ok=false when the session id does not exist. The
// sessions_cdc_update trigger fans out a session_updated CDC event when the
// preview URL actually changes.
func (s *Store) SetSessionPreviewURL(ctx context.Context, id domain.SessionID, previewURL string, updatedAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.SetSessionPreviewURL(ctx, gen.SetSessionPreviewURLParams{
		ID:         id,
		PreviewURL: previewURL,
		UpdatedAt:  updatedAt,
	})
	if err != nil {
		return false, fmt.Errorf("set preview url for session %s: %w", id, err)
	}
	return rows > 0, nil
}

// SetSessionTerminateOnPRMerge updates the user's merge-completion lifecycle
// policy. It returns ok=false when the session id does not exist.
func (s *Store) SetSessionTerminateOnPRMerge(ctx context.Context, id domain.SessionID, terminate bool, updatedAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.SetSessionTerminateOnPRMerge(ctx, gen.SetSessionTerminateOnPRMergeParams{
		ID:                 id,
		TerminateOnPRMerge: terminate,
		UpdatedAt:          updatedAt,
	})
	if err != nil {
		return false, fmt.Errorf("set terminate-on-pr-merge for session %s: %w", id, err)
	}
	return rows > 0, nil
}

// SetSessionAutoInjectReview persists a session's automatic review-injection policy.
func (s *Store) SetSessionAutoInjectReview(ctx context.Context, id domain.SessionID, autoInject bool, updatedAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.SetSessionAutoInjectReview(ctx, gen.SetSessionAutoInjectReviewParams{
		ID:               id,
		AutoInjectReview: autoInject,
		UpdatedAt:        updatedAt,
	})
	if err != nil {
		return false, fmt.Errorf("set auto-inject review for session %s: %w", id, err)
	}
	return rows > 0, nil
}

// SetSessionAutoInjectCI persists the CI-failure injection policy for the
// session and every PR currently owned by it. Future PRs still inherit this
// value from the session row when first observed.
func (s *Store) SetSessionAutoInjectCI(ctx context.Context, id domain.SessionID, autoInject bool, updatedAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var updated bool
	err := s.inTx(ctx, fmt.Sprintf("set auto-inject CI for session %s", id), func(q *gen.Queries) error {
		rows, err := q.SetSessionAutoInjectCI(ctx, gen.SetSessionAutoInjectCIParams{
			ID:           id,
			AutoInjectCI: autoInject,
			UpdatedAt:    updatedAt,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return nil
		}
		updated = true
		if err := q.SetPRAutoInjectCIBySession(ctx, gen.SetPRAutoInjectCIBySessionParams{
			AutoInjectCI: autoInject,
			SessionID:    id,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return updated, nil
}

// SetSessionReviewerHarness persists the reviewer preference for one session.
func (s *Store) SetSessionReviewerHarness(ctx context.Context, id domain.SessionID, harness domain.ReviewerHarness, updatedAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.SetSessionReviewerHarness(ctx, gen.SetSessionReviewerHarnessParams{
		ReviewerHarness: harness,
		UpdatedAt:       updatedAt,
		ID:              id,
	})
	if err != nil {
		return false, fmt.Errorf("set reviewer harness for %s: %w", id, err)
	}
	return rows > 0, nil
}

// SetSessionAutoReview persists the daemon-side review automation toggle.
func (s *Store) SetSessionAutoReview(ctx context.Context, id domain.SessionID, enabled bool, updatedAt time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.SetSessionAutoReview(ctx, gen.SetSessionAutoReviewParams{
		AutoReviewEnabled: enabled,
		UpdatedAt:         updatedAt,
		ID:                id,
	})
	if err != nil {
		return false, fmt.Errorf("set auto review for %s: %w", id, err)
	}
	return rows > 0, nil
}

// DeleteSession removes a session row, but only if it is still in seed state
// (no workspace, no runtime handle, no agent session id, no prompt, no handoff
// metadata, and not already terminated). Rows that have observable spawn output are immutable
// to preserve the no-resurrection guarantee — for those, callers fall back to
// MarkTerminated (lifecycle.Manager) instead.
//
// The deletion runs in a transaction. It first probes seed state with
// SessionIsSeed; only if that returns true does it clear the session's
// change_log rows (required because change_log FKs sessions(id) without
// ON DELETE CASCADE) and then delete the session row. For live or absent
// sessions the transaction commits with no rows touched — critically, the
// session_created / session_updated CDC events for live sessions are NOT
// destroyed when callers (e.g. RollbackSpawn's delete-then-kill fallback)
// invoke DeleteSession on a fully-spawned row.
//
// Returns deleted=true when a seed row was removed; deleted=false when the
// session id did not match a seed row (either it never existed, or it had
// already progressed past seed state). The latter case is benign — the caller
// should fall back to MarkTerminated.
func (s *Store) DeleteSession(ctx context.Context, id domain.SessionID) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin delete seed session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)

	isSeed, err := q.SessionIsSeed(ctx, id)
	if err != nil {
		return false, fmt.Errorf("delete seed session: probe seed state for %s: %w", id, err)
	}
	if !isSeed {
		// Commit the empty tx so we don't leak a transaction. Critically, do
		// NOT touch change_log here — for a live session that contains real
		// session_created / session_updated CDC events.
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("delete seed session: commit no-op: %w", err)
		}
		return false, nil
	}

	// Drop change_log rows for this session id first so the FK doesn't reject
	// the session DELETE. We do not touch project-level events (session_id IS
	// NULL) — those belong to the project, not this session. Both this DELETE
	// and the session DELETE below run via raw ExecContext to sidestep sqlc
	// 1.31's SQLite-parser bug, which strips trailing `?` placeholders and
	// string literals from DELETE statements (see queries/changelog.sql and
	// queries/sessions.sql for the documented workaround context).
	if _, err := tx.ExecContext(ctx, `DELETE FROM change_log WHERE session_id = ?`, id); err != nil {
		return false, fmt.Errorf("delete seed session: clear change log for %s: %w", id, err)
	}
	res, err := tx.ExecContext(ctx, `
DELETE FROM sessions
WHERE id = ?
  AND is_terminated = 0
  AND workspace_path = ''
  AND runtime_handle_id = ''
  AND agent_session_id = ''
  AND prompt = ''
  AND latest_user_prompt = ''
  AND latest_assistant_update = ''
  AND native_transcript_path = ''`, id)
	if err != nil {
		return false, fmt.Errorf("delete seed session %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete seed session %s: rows affected: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("delete seed session: commit: %w", err)
	}
	return n > 0, nil
}

// GetSession returns the full record for a session, or ok=false if absent.
func (s *Store) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	row, err := s.qr.GetSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionRecord{}, false, nil
	}
	if err != nil {
		return domain.SessionRecord{}, false, fmt.Errorf("get session %s: %w", id, err)
	}
	return getSessionRowToRecord(row), true, nil
}

// ListSessions returns every session in a project, ordered by num.
func (s *Store) ListSessions(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error) {
	rows, err := s.qr.ListSessionsByProject(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list sessions for %s: %w", project, err)
	}
	return mapListSessionsByProjectRows(rows), nil
}

// ListAllSessions returns every session across all projects.
func (s *Store) ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error) {
	rows, err := s.qr.ListAllSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all sessions: %w", err)
	}
	return mapListAllSessionsRows(rows), nil
}

func mapListSessionsByProjectRows(rows []gen.ListSessionsByProjectRow) []domain.SessionRecord {
	out := make([]domain.SessionRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, listSessionsByProjectRowToRecord(r))
	}
	return out
}

func mapListAllSessionsRows(rows []gen.ListAllSessionsRow) []domain.SessionRecord {
	out := make([]domain.SessionRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, listAllSessionsRowToRecord(r))
	}
	return out
}

func rowToRecord(row gen.GetSessionRow) domain.SessionRecord {
	return domain.SessionRecord{
		ID:                row.ID,
		ProjectID:         row.ProjectID,
		IssueID:           row.IssueID,
		Kind:              row.Kind,
		Harness:           row.Harness,
		ReviewerHarness:   row.ReviewerHarness,
		AutoReviewEnabled: row.AutoReviewEnabled,
		DisplayName:       row.DisplayName,
		Mode:              domain.NormalizeSessionMode(row.SessionMode),
		Activity: domain.Activity{
			State:          row.ActivityState,
			LastActivityAt: row.ActivityLastAt,
		},
		FirstSignalAt:      nullTimeToTime(row.FirstSignalAt),
		IsTerminated:       row.IsTerminated,
		IsPinned:           row.IsPinned,
		PinnedAt:           nullTimeToTimePtr(row.PinnedAt),
		TerminateOnPRMerge: row.TerminateOnPRMerge,
		AutoInjectReview:   row.AutoInjectReview,
		AutoInjectCI:       row.AutoInjectCI,
		Metadata: domain.SessionMetadata{
			Branch:                    row.Branch,
			WorkspacePath:             row.WorkspacePath,
			WorkspaceRepoPath:         row.WorkspaceRepoPath,
			DiffBaseSHA:               row.DiffBaseSha,
			DiffBaseRef:               row.DiffBaseRef,
			RuntimeHandleID:           row.RuntimeHandleID,
			RuntimeLaunchID:           row.RuntimeLaunchID,
			AgentSessionID:            row.AgentSessionID,
			AgentSessionIDLaunchID:    row.AgentSessionIDLaunchID,
			Prompt:                    row.Prompt,
			LatestUserPrompt:          row.LatestUserPrompt,
			LatestUserPromptAt:        nullTimeToTime(row.LatestUserPromptAt),
			LatestAssistantUpdate:     row.LatestAssistantUpdate,
			NativeTranscriptPath:      row.NativeTranscriptPath,
			PreviewURL:                row.PreviewURL,
			PreviewRevision:           row.PreviewRevision,
			BrowserCapabilityVerifier: row.BrowserCapabilityVerifier,
			ProviderConversationID:    row.ProviderConversationID,
			ControllerGeneration:      row.ControllerGeneration,
			Model:                     row.Model,
		},
		CleanupGeneration: row.CleanupGeneration,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func getSessionRowToRecord(row gen.GetSessionRow) domain.SessionRecord {
	return rowToRecord(row)
}

func listSessionsByProjectRowToRecord(row gen.ListSessionsByProjectRow) domain.SessionRecord {
	return rowToRecord(gen.GetSessionRow(row))
}

func listAllSessionsRowToRecord(row gen.ListAllSessionsRow) domain.SessionRecord {
	return rowToRecord(gen.GetSessionRow(row))
}

func recordToInsert(rec domain.SessionRecord, num int64) gen.InsertSessionParams {
	activity := normalActivity(rec.Activity, rec.CreatedAt)
	return gen.InsertSessionParams{
		ID:                        rec.ID,
		ProjectID:                 rec.ProjectID,
		Num:                       num,
		IssueID:                   rec.IssueID,
		Kind:                      rec.Kind,
		Harness:                   rec.Harness,
		ReviewerHarness:           rec.ReviewerHarness,
		AutoReviewEnabled:         rec.AutoReviewEnabled,
		DisplayName:               rec.DisplayName,
		ActivityState:             activity.State,
		ActivityLastAt:            activity.LastActivityAt,
		FirstSignalAt:             timeToNullTime(rec.FirstSignalAt),
		IsTerminated:              rec.IsTerminated,
		IsPinned:                  rec.IsPinned,
		PinnedAt:                  timePtrToNullTime(rec.PinnedAt),
		Branch:                    rec.Metadata.Branch,
		WorkspacePath:             rec.Metadata.WorkspacePath,
		WorkspaceRepoPath:         rec.Metadata.WorkspaceRepoPath,
		DiffBaseSha:               rec.Metadata.DiffBaseSHA,
		DiffBaseRef:               rec.Metadata.DiffBaseRef,
		RuntimeHandleID:           rec.Metadata.RuntimeHandleID,
		RuntimeLaunchID:           rec.Metadata.RuntimeLaunchID,
		AgentSessionID:            rec.Metadata.AgentSessionID,
		AgentSessionIDLaunchID:    rec.Metadata.AgentSessionIDLaunchID,
		Prompt:                    rec.Metadata.Prompt,
		LatestUserPrompt:          rec.Metadata.LatestUserPrompt,
		LatestUserPromptAt:        timeToNullTime(rec.Metadata.LatestUserPromptAt),
		LatestAssistantUpdate:     rec.Metadata.LatestAssistantUpdate,
		NativeTranscriptPath:      rec.Metadata.NativeTranscriptPath,
		PreviewURL:                rec.Metadata.PreviewURL,
		PreviewRevision:           rec.Metadata.PreviewRevision,
		TerminateOnPRMerge:        rec.TerminateOnPRMerge,
		AutoInjectReview:          rec.AutoInjectReview,
		AutoInjectCI:              rec.AutoInjectCI,
		CleanupGeneration:         rec.CleanupGeneration,
		BrowserCapabilityVerifier: rec.Metadata.BrowserCapabilityVerifier,
		SessionMode:               domain.NormalizeSessionMode(rec.Mode),
		ProviderConversationID:    rec.Metadata.ProviderConversationID,
		ControllerGeneration:      rec.Metadata.ControllerGeneration,
		Model:                     rec.Metadata.Model,
		CreatedAt:                 rec.CreatedAt,
		UpdatedAt:                 rec.UpdatedAt,
	}
}

func recordToUpdate(rec domain.SessionRecord) gen.UpdateSessionParams {
	activity := normalActivity(rec.Activity, rec.UpdatedAt)
	return gen.UpdateSessionParams{
		ID:                        rec.ID,
		IssueID:                   rec.IssueID,
		Kind:                      rec.Kind,
		Harness:                   rec.Harness,
		ReviewerHarness:           rec.ReviewerHarness,
		AutoReviewEnabled:         rec.AutoReviewEnabled,
		DisplayName:               rec.DisplayName,
		ActivityState:             activity.State,
		ActivityLastAt:            activity.LastActivityAt,
		FirstSignalAt:             timeToNullTime(rec.FirstSignalAt),
		IsTerminated:              rec.IsTerminated,
		IsPinned:                  rec.IsPinned,
		PinnedAt:                  timePtrToNullTime(rec.PinnedAt),
		Branch:                    rec.Metadata.Branch,
		WorkspacePath:             rec.Metadata.WorkspacePath,
		WorkspaceRepoPath:         rec.Metadata.WorkspaceRepoPath,
		DiffBaseSha:               rec.Metadata.DiffBaseSHA,
		DiffBaseRef:               rec.Metadata.DiffBaseRef,
		RuntimeHandleID:           rec.Metadata.RuntimeHandleID,
		RuntimeLaunchID:           rec.Metadata.RuntimeLaunchID,
		AgentSessionID:            rec.Metadata.AgentSessionID,
		AgentSessionIDLaunchID:    rec.Metadata.AgentSessionIDLaunchID,
		Prompt:                    rec.Metadata.Prompt,
		LatestUserPrompt:          rec.Metadata.LatestUserPrompt,
		LatestUserPromptAt:        timeToNullTime(rec.Metadata.LatestUserPromptAt),
		LatestAssistantUpdate:     rec.Metadata.LatestAssistantUpdate,
		NativeTranscriptPath:      rec.Metadata.NativeTranscriptPath,
		PreviewURL:                rec.Metadata.PreviewURL,
		PreviewRevision:           rec.Metadata.PreviewRevision,
		TerminateOnPRMerge:        rec.TerminateOnPRMerge,
		AutoInjectReview:          rec.AutoInjectReview,
		AutoInjectCI:              rec.AutoInjectCI,
		CleanupGeneration:         rec.CleanupGeneration,
		BrowserCapabilityVerifier: rec.Metadata.BrowserCapabilityVerifier,
		ProviderConversationID:    rec.Metadata.ProviderConversationID,
		ControllerGeneration:      rec.Metadata.ControllerGeneration,
		Model:                     rec.Metadata.Model,
		UpdatedAt:                 rec.UpdatedAt,
	}
}

// nullTimeToTime / timeToNullTime bridge the nullable first_signal_at column
// to the domain's zero-time convention (zero = no signal received yet).
func nullTimeToTime(t sql.NullTime) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}

func timeToNullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

func nullTimeToTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func timePtrToNullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

func normalActivity(a domain.Activity, fallback time.Time) domain.Activity {
	if a.State == "" {
		a.State = domain.ActivityIdle
	}
	if a.LastActivityAt.IsZero() {
		a.LastActivityAt = fallback
	}
	if a.LastActivityAt.IsZero() {
		a.LastActivityAt = time.Now().UTC()
	}
	return a
}
