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
func (m *Manager) ReconcileSessionOutputType(ctx context.Context, id domain.SessionID) error {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil || !ok {
		return err
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
	if next == rec.OutputType {
		return nil
	}
	rec.OutputType = next
	return m.store.UpdateSession(ctx, rec)
}
