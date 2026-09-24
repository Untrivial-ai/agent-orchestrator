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
func (m *Manager) ReconcileSessionOutputType(ctx context.Context, id domain.SessionID) error {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil || !ok {
		return err
	}
	backfilled := false
	if rec.Metadata.ArtifactDir == "" {
		if dir := sessionartifacts.Dir(m.dataDir, id); dir != "" {
			rec.Metadata.ArtifactDir = dir
			backfilled = true
		}
	}
	prs, err := m.store.ListPRsBySession(ctx, id)
	if err != nil {
		return err
	}
	artifacts, err := sessionartifacts.List(rec.Metadata.ArtifactDir)
	if err != nil {
		return err
	}
	next := sessionartifacts.DeriveOutputType(len(prs), len(artifacts))
	if next == rec.OutputType && !backfilled {
		return nil
	}
	rec.OutputType = next
	return m.store.UpdateSession(ctx, rec)
}
