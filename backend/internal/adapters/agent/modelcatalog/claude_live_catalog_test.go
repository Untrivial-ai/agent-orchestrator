package modelcatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClaudeDiscoveryUsesAdvertisedVersionedChoices(t *testing.T) {
	called := false
	d := Discoverer{ClaudeOptions: func(_ context.Context, request ports.AgentModelDiscoveryRequest) ([]ports.ChatConfigOption, error) {
		called = true
		if request.Binary != "/bin/claude" || request.WorkingDir != "/workspace" {
			t.Fatalf("request lost context: %+v", request)
		}
		return []ports.ChatConfigOption{{ID: "model", Type: ports.ChatConfigOptionSelect, Category: "model", Current: ports.ChatConfigOptionValue{Select: "opus[1m]"}, Choices: []ports.ChatConfigOptionChoice{
			{Value: "opus[1m]", Name: "Opus (1M context)", Description: "Opus 5 with 1M context · Best for complex tasks"},
			{Value: "claude-opus-4-8", Name: "Opus 4.8"},
			{Value: "claude-fable-5-1[1m]", Name: "Fable", Description: "Fable 5.1 · Most capable"},
		}}}, nil
	}}
	got, err := d.Discover(context.Background(), ports.AgentModelDiscoveryRequest{AgentID: "claude-code", Binary: "/bin/claude", WorkingDir: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if !called || got.Source != "acp" || len(got.Models) != 3 {
		t.Fatalf("wrong catalog: %+v", got)
	}
	labels := map[string]string{}
	for _, m := range got.Models {
		labels[m.ID] = m.Label
	}
	if labels["opus[1m]"] != "Opus 5 with 1M context" || labels["claude-fable-5-1[1m]"] != "Fable 5.1" {
		t.Fatalf("lost versions or changed selectors: %+v", labels)
	}
	if _, ok := labels["fable"]; ok {
		t.Fatal("injected unadvertised alias")
	}
}

func TestClaudeLiveDiscoveryFailureDoesNotClaimStaticCatalogSuccess(t *testing.T) {
	d := Discoverer{ClaudeOptions: func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.ChatConfigOption, error) {
		return nil, errors.New("provider unavailable")
	}}
	if _, err := d.Discover(context.Background(), ports.AgentModelDiscoveryRequest{AgentID: "claude-code"}); err == nil {
		t.Fatal("failed live discovery returned success")
	}
}

func TestClaudeDiscoveryFingerprintTracksAllowedModels(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(path, []byte(`{"availableModels":["opus"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	before := discoveryConfigInputs("claude-code", dir, nil)
	if err := os.WriteFile(path, []byte(`{"availableModels":["sonnet"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if before == discoveryConfigInputs("claude-code", dir, nil) {
		t.Fatal("allowed model change left stale fingerprint")
	}
}

func TestClaudeDiscoveryFingerprintTracksRoutingAndConfigDir(t *testing.T) {
	dir := t.TempDir()
	a := discoveryConfigInputs("claude-code", dir, map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "opus-a"})
	b := discoveryConfigInputs("claude-code", dir, map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "opus-b"})
	if a == b {
		t.Fatal("routing ignored")
	}
	env := map[string]string{"CLAUDE_CONFIG_DIR": dir}
	a = discoveryConfigInputs("claude-code", "", env)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"availableModels":["opus"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if a == discoveryConfigInputs("claude-code", "", env) {
		t.Fatal("custom config directory ignored")
	}
}
