package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// fakeSessionGetter stands in for the real session service so sessionWorkspaceLocator
// can be tested without a full session stack.
type fakeSessionGetter struct {
	sessions map[domain.SessionID]domain.Session
	err      error
}

func (f *fakeSessionGetter) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	if f.err != nil {
		return domain.Session{}, f.err
	}
	sess, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, errors.New("session not found")
	}
	return sess, nil
}

// TestSessionWorkspaceLocator_ReturnsPathWhenItStillExists is the ordinary
// live-session case: the worktree is still on disk, so the shell opens there.
func TestSessionWorkspaceLocator_ReturnsPathWhenItStillExists(t *testing.T) {
	dir := t.TempDir()
	getter := &fakeSessionGetter{sessions: map[domain.SessionID]domain.Session{
		"mer-1": {SessionRecord: domain.SessionRecord{
			ID: "mer-1", ProjectID: "mer",
			Metadata: domain.SessionMetadata{WorkspacePath: dir},
		}},
	}}
	loc := &sessionWorkspaceLocator{sessions: getter}

	path, projectID, err := loc.SessionWorkspace(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	if path != dir {
		t.Errorf("path = %q, want %q", path, dir)
	}
	if projectID != "mer" {
		t.Errorf("projectID = %q, want mer", projectID)
	}
}

// TestSessionWorkspaceLocator_FallsBackWhenRecordedPathWasRemoved is the
// regression this covers: Kill/Cleanup can reclaim a worktree without
// clearing the session's durable Metadata.WorkspacePath (that field doubles
// as "what to recreate on restore"). A shell opened against an archived
// session must not trust a path that no longer exists — it must signal "no
// workspace" so the caller falls back to the project root instead of trying
// to chdir into nothing.
func TestSessionWorkspaceLocator_FallsBackWhenRecordedPathWasRemoved(t *testing.T) {
	dir := t.TempDir()
	removed := filepath.Join(dir, "worktree-that-is-gone")
	getter := &fakeSessionGetter{sessions: map[domain.SessionID]domain.Session{
		"mer-1": {SessionRecord: domain.SessionRecord{
			ID: "mer-1", ProjectID: "mer",
			Metadata: domain.SessionMetadata{WorkspacePath: removed},
		}},
	}}
	loc := &sessionWorkspaceLocator{sessions: getter}

	path, projectID, err := loc.SessionWorkspace(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty so the caller falls back to the project root", path)
	}
	if projectID != "mer" {
		t.Errorf("projectID = %q, want mer even on fallback", projectID)
	}
}

// TestSessionWorkspaceLocator_KeepsDirtyPreservedWorktree: a worktree Kill
// refused to remove because of uncommitted work still exists on disk, so it
// must be returned as-is, not treated as reclaimed.
func TestSessionWorkspaceLocator_KeepsDirtyPreservedWorktree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	getter := &fakeSessionGetter{sessions: map[domain.SessionID]domain.Session{
		"mer-1": {SessionRecord: domain.SessionRecord{
			ID: "mer-1", ProjectID: "mer",
			Metadata: domain.SessionMetadata{WorkspacePath: dir},
		}},
	}}
	loc := &sessionWorkspaceLocator{sessions: getter}

	path, _, err := loc.SessionWorkspace(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	if path != dir {
		t.Errorf("path = %q, want the preserved worktree %q", path, dir)
	}
}

func TestSessionWorkspaceLocator_NoWorkspacePathPassesThrough(t *testing.T) {
	getter := &fakeSessionGetter{sessions: map[domain.SessionID]domain.Session{
		"mer-orch": {SessionRecord: domain.SessionRecord{ID: "mer-orch", ProjectID: "mer"}},
	}}
	loc := &sessionWorkspaceLocator{sessions: getter}

	path, projectID, err := loc.SessionWorkspace(context.Background(), "mer-orch")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
	if projectID != "mer" {
		t.Errorf("projectID = %q, want mer", projectID)
	}
}

func TestSessionWorkspaceLocator_PropagatesUnknownSessionError(t *testing.T) {
	loc := &sessionWorkspaceLocator{sessions: &fakeSessionGetter{}}

	if _, _, err := loc.SessionWorkspace(context.Background(), "ghost"); err == nil {
		t.Fatal("SessionWorkspace: want error for an unknown session")
	}
}

func TestSessionWorkspaceLocator_CueCommandTargetPreservesStrictFacts(t *testing.T) {
	removed := filepath.Join(t.TempDir(), "removed-worktree")
	getter := &fakeSessionGetter{sessions: map[domain.SessionID]domain.Session{
		"mer-1": {SessionRecord: domain.SessionRecord{
			ID: "mer-1", ProjectID: "mer", IsTerminated: true,
			Activity: domain.Activity{State: domain.ActivityExited},
			Metadata: domain.SessionMetadata{WorkspacePath: removed},
		}},
	}}
	loc := &sessionWorkspaceLocator{sessions: getter}

	target, err := loc.CueCommandSessionTarget(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if target.ProjectID != "mer" || target.WorkspacePath != removed || !target.IsTerminated || target.Activity != domain.ActivityExited {
		t.Fatalf("target = %+v", target)
	}
}
