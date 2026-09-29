package agent

import (
	"context"
	"sort"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// withModelUsage stamps each model with when a session last ran it and floats
// the used ones to the front, most recent first.
//
// Recency outranks the catalog's own order deliberately. A static order encodes
// what is newest; it cannot encode what this user works with. Someone who ran
// Sonnet last almost certainly wants Sonnet again, even though the family order
// puts Fable and Opus ahead of it. Models never picked keep the catalog order
// behind the used ones, so a fresh install still reads newest-family-first.
//
// The stamp is derived on read and never persisted: the cached catalog stays a
// record of what the provider reported, not of what the user did with it.
func (s *Service) withModelUsage(ctx context.Context, agentID, projectID string, catalog ports.AgentModelCatalog) ports.AgentModelCatalog {
	if len(catalog.Models) == 0 {
		return catalog
	}
	usage := s.modelUsage(ctx, agentID, projectID)
	if len(usage) == 0 {
		return catalog
	}
	models := make([]ports.AgentModelInfo, len(catalog.Models))
	copy(models, catalog.Models)
	for i := range models {
		if at, ok := usage[models[i].ID]; ok {
			usedAt := at
			models[i].LastUsedAt = &usedAt
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		left, right := models[i].LastUsedAt, models[j].LastUsedAt
		if (left == nil) != (right == nil) {
			return left != nil
		}
		if left == nil {
			return false
		}
		return left.After(*right)
	})
	catalog.Models = models
	return catalog
}

// modelUsage maps model ID to when a session last ran it.
//
// Project history wins where it exists, because the model someone uses in one
// repository says little about another. A project with no history inherits the
// agent-wide answer instead of starting blind, which is what makes the first
// task in a new project open on the model the user actually works with.
func (s *Service) modelUsage(ctx context.Context, agentID, projectID string) map[string]time.Time {
	if s.sessions == nil || agentID == "" {
		return nil
	}
	records, err := s.sessions.ListAllSessions(ctx)
	if err != nil {
		// A picker without its recency hint is still a working picker, and this
		// runs on every catalog read. Degrade to the catalog order.
		if s.logger != nil {
			s.logger.Debug("model usage lookup failed", "agent", agentID, "error", err)
		}
		return nil
	}
	scoped := make(map[string]time.Time)
	global := make(map[string]time.Time)
	for _, record := range records {
		model := record.Metadata.Model
		if model == "" || string(record.Harness) != agentID {
			continue
		}
		at := sessionModelUsedAt(record)
		recordUsage(global, model, at)
		if projectID != "" && string(record.ProjectID) == projectID {
			recordUsage(scoped, model, at)
		}
	}
	if len(scoped) > 0 {
		return scoped
	}
	return global
}

func recordUsage(usage map[string]time.Time, model string, at time.Time) {
	if existing, ok := usage[model]; ok && !at.After(existing) {
		return
	}
	usage[model] = at
}

// sessionModelUsedAt prefers the session's last activity over its creation
// time. A model switched into an existing session is used from that moment, not
// from whenever the session happened to start.
func sessionModelUsedAt(record domain.SessionRecord) time.Time {
	if record.UpdatedAt.After(record.CreatedAt) {
		return record.UpdatedAt
	}
	return record.CreatedAt
}
