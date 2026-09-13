package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCodexUpdateRefreshesCatalogAndPreservesStaleFailure(t *testing.T) {
	ctx := context.Background()
	cache := &fakeModelCache{}
	discoverer := successfulModelDiscoverer()
	svc := newService([]agentregistry.HarnessAgent{{Harness: domain.HarnessCodex, Manifest: adapters.Manifest{ID: "codex", Name: "Codex"}, Agent: fakeAuthAgent{status: ports.AgentAuthStatusAuthorized}}}, cache, nil, discoverer)
	if _, err := svc.Models(ctx, "codex", "", false); err != nil {
		t.Fatal(err)
	}
	discoverer.catalog.Models = []ports.AgentModelInfo{{ID: "provider-returned-future-model"}}
	if err := svc.RefreshCodexInstallation(ctx); err != nil {
		t.Fatal(err)
	}
	catalog, err := svc.Models(ctx, "codex", "", false)
	if err != nil || len(catalog.Models) != 1 || catalog.Models[0].ID != "provider-returned-future-model" || catalog.Stale {
		t.Fatalf("%+v %v", catalog, err)
	}
	discoverer.catalog.Models = nil
	discoverer.err = errors.New("model/list timed out")
	if err = svc.RefreshCodexInstallation(ctx); err == nil || !strings.Contains(err.Error(), "model/list timed out") {
		t.Fatal(err)
	}
	catalog, err = svc.Models(ctx, "codex", "", false)
	if err != nil || !catalog.Stale || len(catalog.Models) != 1 || catalog.Models[0].ID != "provider-returned-future-model" {
		t.Fatalf("%+v %v", catalog, err)
	}
}
