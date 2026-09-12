package modelcatalog

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestParseQwenModelsUsesConfiguredProviderSelectors(t *testing.T) {
	models, err := parseQwenModels([]byte(`{
		"modelProviders": {
			"openai": [{"id":"gpt-5.6-sol","name":"GPT-5.6 Sol"}],
			"anthropic": [{"id":"claude-fable-5","name":"Fable 5"}]
		},
		"model": {"name":"gpt-5.6-sol"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol", Provider: "openai", IsDefault: true},
		{ID: "claude-fable-5", Label: "Fable 5", Provider: "anthropic"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestParseAutoHandModelsUsesConfiguredProviderModel(t *testing.T) {
	models, err := parseAutoHandModels([]byte(`{
		"provider": "zai",
		"zai": {"model": "glm-5.1"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{{
		ID: "zai/glm-5.1", Label: "glm-5.1", Provider: "zai", IsDefault: true,
	}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestAutoHandDiscoveryReadsConfigurationWithoutRunningBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".autohand", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"provider":"zai","zai":{"model":"glm-5.1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(context.Background(), "autohand", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "zai/glm-5.1" || got.Source != "config" {
		t.Fatalf("catalog = %#v", got)
	}
}

func TestQwenConfigDiscoveryUsesQwenHome(t *testing.T) {
	home := t.TempDir()
	qwenHome := filepath.Join(home, "custom-qwen")
	t.Setenv("HOME", home)
	t.Setenv("QWEN_HOME", qwenHome)
	if err := os.MkdirAll(qwenHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(qwenHome, "settings.json"), []byte(`{
		"modelProviders":{"openai":[{"id":"gpt-5.6-sol","name":"Sol"}]}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(context.Background(), "qwen", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "gpt-5.6-sol" {
		t.Fatalf("catalog = %#v", got)
	}
}

func TestParseContinueModels(t *testing.T) {
	models, err := parseContinueModels([]byte(`
models:
  - name: Claude Sonnet 4.6
    provider: anthropic
    model: claude-sonnet-4-6
  - name: GLM 5.2
    provider: openai
    model: glm-5.2
defaults:
  chat: Claude Sonnet 4.6
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Provider: "anthropic", IsDefault: true},
		{ID: "glm-5.2", Label: "GLM 5.2", Provider: "openai"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestParseGooseModelsReturnsOnlyActiveProviderModels(t *testing.T) {
	models, err := parseGooseModels([]byte(`
active_provider: anthropic
providers:
  anthropic:
    enabled: true
    model: claude-sonnet-4-6
    models: [claude-haiku-4-5]
  openrouter:
    enabled: true
    model: openai/gpt-5.6-sol
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "claude-sonnet-4-6", Label: "claude-sonnet-4-6", Provider: "anthropic", IsDefault: true},
		{ID: "claude-haiku-4-5", Label: "claude-haiku-4-5", Provider: "anthropic"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestParseVibeModelsUsesAliasesAndActiveModel(t *testing.T) {
	models, err := parseVibeModels([]byte(`
active_model = "zai-glm"
[[models]]
name = "glm-4.7"
provider = "zai"
alias = "zai-glm"
[[models]]
name = "mistral-vibe-cli-latest"
provider = "mistral"
alias = "mistral-medium-3.5"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "zai-glm", Label: "glm-4.7", Provider: "zai", IsDefault: true},
		{ID: "mistral-medium-3.5", Label: "mistral-vibe-cli-latest", Provider: "mistral"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestParseClineModelsUsesConfiguredProviderSelections(t *testing.T) {
	models, err := parseClineModels([]byte(`{
		"lastUsedProvider":"zai-coding-plan",
		"providers": {
			"cline": {"settings":{"apiModelId":"claude-sonnet-4-6"}},
			"zai-coding-plan": {"settings":{"apiModelId":"glm-5.2"}}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "glm-5.2", Label: "glm-5.2", Provider: "zai-coding-plan", IsDefault: true},
		{ID: "claude-sonnet-4-6", Label: "claude-sonnet-4-6", Provider: "cline"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestConfigCatalogDiscoveryAndFingerprint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".continue", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("models:\n  - name: Sonnet\n    provider: anthropic\n    model: claude-sonnet-4-6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := CatalogFingerprint(context.Background(), "continue", "", "", nil)
	got, err := Discover(context.Background(), "continue", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "claude-sonnet-4-6" || got.SelectionMode != ports.ModelSelectionCatalog {
		t.Fatalf("catalog = %#v", got)
	}
	if err := os.WriteFile(path, []byte("models:\n  - name: Opus\n    provider: anthropic\n    model: claude-opus-5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := CatalogFingerprint(context.Background(), "continue", "", "", nil)
	if before == after {
		t.Fatalf("fingerprint did not change after config edit: %q", before)
	}
}
