package agent

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func usageSession(project, harness, model string, at time.Time) domain.SessionRecord {
	return domain.SessionRecord{
		ProjectID: domain.ProjectID(project),
		Harness:   domain.AgentHarness(harness),
		Metadata:  domain.SessionMetadata{Model: model},
		CreatedAt: at,
		UpdatedAt: at,
	}
}

func claudeCatalog(ids ...string) ports.AgentModelCatalog {
	models := make([]ports.AgentModelInfo, 0, len(ids))
	for _, id := range ids {
		models = append(models, ports.AgentModelInfo{ID: id, Label: id})
	}
	return ports.AgentModelCatalog{AgentID: "claude-code", Models: models}
}

func modelIDs(catalog ports.AgentModelCatalog) []string {
	ids := make([]string, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		ids = append(ids, model.ID)
	}
	return ids
}

func assertIDs(t *testing.T, catalog ports.AgentModelCatalog, want ...string) {
	t.Helper()
	got := modelIDs(catalog)
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// The models someone actually runs lead the picker, most recent first, and the
// untouched ones keep the catalog's own newest-family-first order behind them.
func TestModelUsageFloatsRecentModelsToTheTop(t *testing.T) {
	now := time.Now().UTC()
	svc := newService(nil, nil, nil, nil)
	svc.sessions = fakeSessionUsageLookup{records: []domain.SessionRecord{
		usageSession("p1", "claude-code", "fable", now.Add(-2*time.Hour)),
		usageSession("p1", "claude-code", "sonnet", now.Add(-10*time.Minute)),
	}}

	got := svc.withModelUsage(context.Background(), "claude-code", "p1", claudeCatalog("fable", "opus", "sonnet", "haiku"))

	assertIDs(t, got, "sonnet", "fable", "opus", "haiku")
	if got.Models[0].LastUsedAt == nil || got.Models[2].LastUsedAt != nil {
		t.Fatalf("stamps = %+v, want only the used models stamped", got.Models)
	}
}

// Another agent's sessions say nothing about this agent's picker.
func TestModelUsageIgnoresOtherAgents(t *testing.T) {
	now := time.Now().UTC()
	svc := newService(nil, nil, nil, nil)
	svc.sessions = fakeSessionUsageLookup{records: []domain.SessionRecord{
		usageSession("p1", "codex", "sonnet", now),
	}}

	got := svc.withModelUsage(context.Background(), "claude-code", "p1", claudeCatalog("fable", "sonnet"))

	assertIDs(t, got, "fable", "sonnet")
}

// The model someone uses in one repository says little about another, so a
// project's own history wins where it exists.
func TestModelUsagePrefersProjectHistory(t *testing.T) {
	now := time.Now().UTC()
	svc := newService(nil, nil, nil, nil)
	svc.sessions = fakeSessionUsageLookup{records: []domain.SessionRecord{
		usageSession("other", "claude-code", "sonnet", now),
		usageSession("p1", "claude-code", "opus", now.Add(-time.Hour)),
	}}

	got := svc.withModelUsage(context.Background(), "claude-code", "p1", claudeCatalog("fable", "sonnet", "opus"))

	assertIDs(t, got, "opus", "fable", "sonnet")
}

// A project with no history of its own inherits the agent-wide answer, so the
// first task in a new project still opens on the model the user works with.
func TestModelUsageFallsBackToAgentWideHistory(t *testing.T) {
	now := time.Now().UTC()
	svc := newService(nil, nil, nil, nil)
	svc.sessions = fakeSessionUsageLookup{records: []domain.SessionRecord{
		usageSession("other", "claude-code", "sonnet", now),
	}}

	got := svc.withModelUsage(context.Background(), "claude-code", "fresh", claudeCatalog("fable", "sonnet"))

	assertIDs(t, got, "sonnet", "fable")
}

// A model switched into a running session is used from that moment, not from
// whenever the session happened to start.
func TestModelUsageUsesLastActivityNotCreation(t *testing.T) {
	now := time.Now().UTC()
	old := usageSession("p1", "claude-code", "opus", now.Add(-48*time.Hour))
	old.UpdatedAt = now
	svc := newService(nil, nil, nil, nil)
	svc.sessions = fakeSessionUsageLookup{records: []domain.SessionRecord{
		old,
		usageSession("p1", "claude-code", "sonnet", now.Add(-time.Hour)),
	}}

	got := svc.withModelUsage(context.Background(), "claude-code", "p1", claudeCatalog("sonnet", "opus"))

	assertIDs(t, got, "opus", "sonnet")
}

// Losing the recency hint must not empty or reorder the picker.
func TestModelUsageDegradesToCatalogOrder(t *testing.T) {
	svc := newService(nil, nil, nil, nil)
	svc.sessions = fakeSessionUsageLookup{err: context.DeadlineExceeded}

	got := svc.withModelUsage(context.Background(), "claude-code", "p1", claudeCatalog("fable", "opus"))

	assertIDs(t, got, "fable", "opus")
}
