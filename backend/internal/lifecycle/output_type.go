package lifecycle

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionartifacts"
)

// ReconcileSessionOutputType recomputes a session's durable OutputType from
// its live PR list and artifact directory contents, and persists it when it
// has changed. This is the sole writer of the session_output_type column;
// every reader (the API, Kanban derivation) trusts that column instead of
// rescanning on every read.
//
// A session row created before artifact_dir existed carries it as ” (the
// migration's default), even though session_manager always tells the agent
// to write into the deterministic dataDir/artifacts/<id> path regardless of
// what is stored. Backfilling that path here — the moment any reconcile call
// touches the row — means every session, not just ones that happen to
// restore, gets a correct, persisted ArtifactDir on its next poll tick.
//
// The write goes through UpdateSessionArtifactOutput, not UpdateSession: this
// method reads the session once, and callers now include the Get/List API
// read path, so an ordinary UI read can run concurrently with termination or
// another lifecycle write. A read-modify-write UpdateSession here would
// persist the stale is_terminated/activity/runtime-identity/preview-state
// captured at read time, potentially resurrecting a session that terminated
// in between. UpdateSessionArtifactOutput only ever names artifact_dir and
// session_output_type, so it cannot touch those other columns regardless of
// how stale the read was.
//
// That narrow write closed the forward direction, but left the reverse one
// open: mutate (the reducer every other lifecycle write — including
// MarkTerminated — funnels through) reads a full SessionRecord and later
// writes it back whole via UpdateSession, including whatever
// artifact_dir/OutputType it saw at read time. Without synchronization, this
// method's write could land in the middle of that window, and mutate's
// later full-row write would silently revert it. mutate holds m.mu for its
// entire read-to-write span, so acquiring the same lock here makes the two
// mutually exclusive: this method's read-then-write can no longer straddle
// a mutate() call, and vice versa.
func (m *Manager) ReconcileSessionOutputType(ctx context.Context, id domain.SessionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil || !ok {
		return err
	}
	artifactDir := rec.Metadata.ArtifactDir
	backfilled := false
	if artifactDir == "" {
		if dir := sessionartifacts.Dir(m.dataDir, id); dir != "" {
			artifactDir = dir
			backfilled = true
		}
	}
	prs, err := m.store.ListPRsBySession(ctx, id)
	if err != nil {
		return err
	}
	artifacts, err := sessionartifacts.List(artifactDir)
	if err != nil {
		return err
	}
	next := sessionartifacts.DeriveOutputType(len(prs), len(artifacts))
	if next == rec.OutputType && !backfilled {
		return nil
	}
	_, err = m.store.UpdateSessionArtifactOutput(ctx, id, artifactDir, next)
	return err
}
