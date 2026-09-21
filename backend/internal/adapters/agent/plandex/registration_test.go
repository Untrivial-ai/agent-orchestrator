package plandex_test

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/plandex"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestPlandexRemainsUnregisteredWhileObservedContractFails(t *testing.T) {
	if err := plandex.ValidateContract(plandex.ObservedContract()); err == nil {
		t.Fatal("observed contract unexpectedly passed; update the conformance evidence before registration")
	}

	for _, harness := range domain.AllHarnesses {
		if harness == domain.AgentHarness("plandex") {
			t.Fatal("plandex is in domain.AllHarnesses before its upstream contract passes")
		}
	}
	for _, adapter := range registry.Constructors() {
		if adapter.Manifest().ID == "plandex" {
			t.Fatal("plandex is in the production registry before its upstream contract passes")
		}
	}
}
