package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

var _ shelltermsvc.Store = (*Store)(nil)

// InsertShellTerminal records a standalone shell terminal against an app run.
func (s *Store) InsertShellTerminal(ctx context.Context, rec shelltermsvc.ShellTerminalRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.qw.InsertShellTerminal(ctx, gen.InsertShellTerminalParams{
		HandleID:   rec.HandleID,
		ProjectID:  optionalProjectID(rec.ProjectID),
		SessionID:  optionalSessionID(rec.SessionID),
		WorkingDir: rec.WorkingDir,
		Title:      rec.Title,
		AppRunID:   rec.AppRunID,
		Transient:  rec.Transient,
		CreatedAt:  rec.CreatedAt,
	})
	if err != nil {
		return fmt.Errorf("insert shell terminal %s: %w", rec.HandleID, err)
	}
	return nil
}

// SelectShellTerminalByHandleID looks up one shell terminal's row, reporting
// whether it existed so the caller can answer 404 for an unknown handle.
// CloseShellTerminal uses this to learn the row's session id BEFORE
// destroying it, so it can take that session's teardown gate first.
func (s *Store) SelectShellTerminalByHandleID(ctx context.Context, handleID string) (shelltermsvc.ShellTerminalRecord, bool, error) {
	row, err := s.qr.SelectShellTerminalByHandleID(ctx, handleID)
	if errors.Is(err, sql.ErrNoRows) {
		return shelltermsvc.ShellTerminalRecord{}, false, nil
	}
	if err != nil {
		return shelltermsvc.ShellTerminalRecord{}, false, fmt.Errorf("select shell terminal %s: %w", handleID, err)
	}
	return shellTerminalFromGen(row), true, nil
}

// SelectShellTerminalsByAppRunID returns the shell terminals owned by one app
// run, oldest first so the UI renders tabs in the order they were opened.
func (s *Store) SelectShellTerminalsByAppRunID(ctx context.Context, appRunID string) ([]shelltermsvc.ShellTerminalRecord, error) {
	rows, err := s.qr.SelectShellTerminalsByAppRunID(ctx, appRunID)
	if err != nil {
		return nil, fmt.Errorf("select shell terminals for app run %s: %w", appRunID, err)
	}
	return shellTerminalsFromGen(rows), nil
}

// SelectShellTerminalsBySessionID returns the shell terminals scoped to one
// session, oldest first. Session Manager uses this to close them before the
// session's worktree is torn down.
func (s *Store) SelectShellTerminalsBySessionID(ctx context.Context, sessionID domain.SessionID) ([]shelltermsvc.ShellTerminalRecord, error) {
	rows, err := s.qr.SelectShellTerminalsBySessionID(ctx, optionalSessionID(sessionID))
	if err != nil {
		return nil, fmt.Errorf("select shell terminals for session %s: %w", sessionID, err)
	}
	return shellTerminalsFromGen(rows), nil
}

// SelectShellTerminalsFromPreviousAppRuns returns shell terminals left behind
// by any app run other than the one given. Boot reconciliation preserves live
// durable shells and destroys only abandoned transient commands.
func (s *Store) SelectShellTerminalsFromPreviousAppRuns(ctx context.Context, appRunID string) ([]shelltermsvc.ShellTerminalRecord, error) {
	rows, err := s.qr.SelectShellTerminalsFromPreviousAppRuns(ctx, appRunID)
	if err != nil {
		return nil, fmt.Errorf("select orphaned shell terminals: %w", err)
	}
	return shellTerminalsFromGen(rows), nil
}

// UpdateShellTerminalTitle renames one shell terminal, returning the updated row
// and whether a row existed so the caller can answer 404 for an unknown handle.
func (s *Store) UpdateShellTerminalTitle(ctx context.Context, handleID, title string) (shelltermsvc.ShellTerminalRecord, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.UpdateShellTerminalTitle(ctx, gen.UpdateShellTerminalTitleParams{Title: title, HandleID: handleID})
	if errors.Is(err, sql.ErrNoRows) {
		return shelltermsvc.ShellTerminalRecord{}, false, nil
	}
	if err != nil {
		return shelltermsvc.ShellTerminalRecord{}, false, fmt.Errorf("rename shell terminal %s: %w", handleID, err)
	}
	return shellTerminalFromGen(row), true, nil
}

// DeleteShellTerminalByHandleID forgets one shell terminal, reporting whether a
// row actually existed so the caller can answer 404 for an unknown handle.
func (s *Store) DeleteShellTerminalByHandleID(ctx context.Context, handleID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	affected, err := s.qw.DeleteShellTerminalByHandleID(ctx, handleID)
	if err != nil {
		return false, fmt.Errorf("delete shell terminal %s: %w", handleID, err)
	}
	return affected > 0, nil
}

// DeleteShellTerminalsFromPreviousAppRuns clears every orphaned row in one
// statement and returns how many were removed.
func (s *Store) DeleteShellTerminalsFromPreviousAppRuns(ctx context.Context, appRunID string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	affected, err := s.qw.DeleteShellTerminalsFromPreviousAppRuns(ctx, appRunID)
	if err != nil {
		return 0, fmt.Errorf("delete orphaned shell terminals: %w", err)
	}
	return affected, nil
}

// optionalProjectID maps the service's "no project" empty string onto the
// nullable column, so a project-less shell stores NULL rather than an empty
// string that would violate the projects FK.
func optionalProjectID(id domain.ProjectID) *domain.ProjectID {
	if id == "" {
		return nil
	}
	return &id
}

// optionalSessionID maps the service's "no session" empty string onto the
// nullable column, so a session-less shell stores NULL rather than an empty
// string that would violate the sessions FK.
func optionalSessionID(id domain.SessionID) sql.NullString {
	if id == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: string(id), Valid: true}
}

func shellTerminalFromGen(row gen.ShellTerminal) shelltermsvc.ShellTerminalRecord {
	rec := shelltermsvc.ShellTerminalRecord{
		HandleID:   row.HandleID,
		WorkingDir: row.WorkingDir,
		Title:      row.Title,
		AppRunID:   row.AppRunID,
		Transient:  row.Transient,
		CreatedAt:  row.CreatedAt,
	}
	if row.ProjectID != nil {
		rec.ProjectID = *row.ProjectID
	}
	if row.SessionID.Valid {
		rec.SessionID = domain.SessionID(row.SessionID.String)
	}
	return rec
}

func shellTerminalsFromGen(rows []gen.ShellTerminal) []shelltermsvc.ShellTerminalRecord {
	out := make([]shelltermsvc.ShellTerminalRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, shellTerminalFromGen(row))
	}
	return out
}

// SelectRestorableShellTerminals includes durable shells from all app launches
// and trusted command terminals belonging to the current launch.
func (s *Store) SelectRestorableShellTerminals(ctx context.Context, appRunID string) ([]shelltermsvc.ShellTerminalRecord, error) {
	rows, err := s.qr.SelectRestorableShellTerminals(ctx, appRunID)
	if err != nil {
		return nil, fmt.Errorf("select restorable shell terminals: %w", err)
	}
	return shellTerminalsFromGen(rows), nil
}
