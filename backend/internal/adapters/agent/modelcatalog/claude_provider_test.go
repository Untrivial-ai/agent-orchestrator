package modelcatalog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// claudeRequest builds a discovery request isolated from this machine.
//
// The configured-default pass reads ANTHROPIC_MODEL and ~/.claude/settings.json,
// so without this the developer's own configured model is appended to every
// catalog under test and the assertions drift per machine.
func claudeRequest(t *testing.T) ports.AgentModelDiscoveryRequest {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("ANTHROPIC_MODEL", "")
	return ports.AgentModelDiscoveryRequest{
		AgentID: "claude-code", WorkingDir: t.TempDir(), Env: map[string]string{},
	}
}

// The picker must show what the configured provider actually serves. On
// Bedrock and Vertex the static aliases are simply wrong IDs.
func TestClaudeCatalogPrefersProviderModels(t *testing.T) {
	list := func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.AgentModelInfo, error) {
		return []ports.AgentModelInfo{
			{ID: "us.anthropic.claude-opus-4-5-v1:0"},
			{ID: "us.anthropic.claude-sonnet-4-5-v1:0"},
		}, nil
	}
	catalog, err := discoverClaudeCatalog(context.Background(), claudeRequest(t), list)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Source != "provider" {
		t.Fatalf("source = %q, want provider", catalog.Source)
	}
	if len(catalog.Models) != 2 {
		t.Fatalf("models = %+v, want the two provider IDs", catalog.Models)
	}
	for _, model := range catalog.Models {
		if model.ID == "sonnet" || model.ID == "opus" {
			t.Fatalf("static alias %q leaked into a provider-sourced catalog", model.ID)
		}
	}
}

// Discovery must never empty the picker. Every way of failing to reach the
// provider falls back to the aliases that shipped before.
func TestClaudeCatalogReturnsStaticFallbackWithProviderError(t *testing.T) {
	tests := []struct {
		name string
		list ClaudeModelListFunc
	}{
		{name: "no lister wired", list: nil},
		{
			name: "provider unreachable",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.AgentModelInfo, error) {
				return nil, errors.New("could not reach Anthropic")
			},
		},
		{
			name: "credential rejected",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.AgentModelInfo, error) {
				return nil, errors.New("provider rejected the credential")
			},
		},
		{
			name: "chain-sourced credential, nothing readable",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.AgentModelInfo, error) {
				return nil, errors.New("no credential could be resolved")
			},
		},
		{
			name: "provider returned an empty list",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.AgentModelInfo, error) {
				return []ports.AgentModelInfo{}, nil
			},
		},
		{
			name: "provider returned only blanks",
			list: func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.AgentModelInfo, error) {
				return []ports.AgentModelInfo{{ID: ""}, {ID: "   "}}, nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			catalog, err := discoverClaudeCatalog(context.Background(), claudeRequest(t), tc.list)
			if len(catalog.Models) == 0 {
				t.Fatal("the picker must never be emptied by a discovery failure")
			}
			if catalog.Source != "catalog" {
				t.Fatalf("source = %q, want the static catalog", catalog.Source)
			}
			ids := map[string]bool{}
			for _, model := range catalog.Models {
				ids[model.ID] = true
			}
			if !ids["sonnet"] || !ids["opus"] {
				t.Fatalf("fallback lost the static aliases: %+v", catalog.Models)
			}
			if tc.list != nil && err == nil {
				t.Fatal("provider discovery failure was suppressed")
			}
		})
	}
}

func TestClaudeProviderModelsAreDeduped(t *testing.T) {
	list := func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.AgentModelInfo, error) {
		return []ports.AgentModelInfo{
			{ID: "claude-opus-4-5-20251101"},
			{ID: "claude-opus-4-5-20251101"},
			{ID: " claude-haiku-4-5 "},
		}, nil
	}
	catalog, err := discoverClaudeCatalog(context.Background(), claudeRequest(t), list)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 2 {
		t.Fatalf("models = %+v, want duplicates collapsed and whitespace trimmed", catalog.Models)
	}
}

func TestProviderModelsPreserveProviderLabelsAndFallBackToRawIDs(t *testing.T) {
	list := func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.AgentModelInfo, error) {
		return []ports.AgentModelInfo{
			{ID: "claude-opus-5", Label: "Claude Opus 5", Efforts: []string{"low", "max"}},
			{ID: "claude-sonnet-4-5-20250929", Label: "Claude Sonnet 4.5"},
			// No display name: the raw provider ID is the honest fallback.
			{ID: "claude-haiku-4-5-20251001"},
		}, nil
	}
	catalog, err := discoverClaudeCatalog(context.Background(), claudeRequest(t), list)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, model := range catalog.Models {
		labels[model.ID] = model.Label
	}
	if labels["claude-opus-5"] != "Claude Opus 5" {
		t.Fatalf("label = %q, want provider label", labels["claude-opus-5"])
	}
	if labels["claude-sonnet-4-5-20250929"] != "Claude Sonnet 4.5" {
		t.Fatalf("label = %q, want provider label", labels["claude-sonnet-4-5-20250929"])
	}
	if labels["claude-haiku-4-5-20251001"] != "claude-haiku-4-5-20251001" {
		t.Fatalf("fallback label = %q, want raw model ID", labels["claude-haiku-4-5-20251001"])
	}
}

// Efforts must survive normalization attached to their own model.
func TestProviderEffortsSurviveNormalization(t *testing.T) {
	list := func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.AgentModelInfo, error) {
		return []ports.AgentModelInfo{
			{ID: "claude-opus-5", Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
			{ID: "claude-opus-4-6", Efforts: []string{"low", "medium", "high", "max"}},
			{ID: "claude-sonnet-4-5-20250929"},
		}, nil
	}
	catalog, err := discoverClaudeCatalog(context.Background(), claudeRequest(t), list)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, model := range catalog.Models {
		got[model.ID] = len(model.Efforts)
	}
	if got["claude-opus-5"] != 5 || got["claude-opus-4-6"] != 4 {
		t.Fatalf("effort counts = %v, want per-model levels preserved", got)
	}
	if got["claude-sonnet-4-5-20250929"] != 0 {
		t.Fatalf("a model with no efforts must carry none, got %d", got["claude-sonnet-4-5-20250929"])
	}
}
