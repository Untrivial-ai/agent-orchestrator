package modelcatalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestParseOpenCodeModelsUsesConfiguredProviderModels(t *testing.T) {
	models, err := parseOpenCodeModels([]byte(`{
		// JSONC comments are allowed.
		"model": "anthropic/claude-sonnet-4-6",
		"provider": {
			"anthropic": {
				"options": {"baseURL": "https://api.anthropic.com/v1"}, // not a comment
				"models": {
					"claude-sonnet-4-6": {},
					"claude-opus-5": {"name": "Opus 5"}
				}
			},
			"openai": {
				/* provider without a models map resolves registry defaults */
				"options": {"apiKey": "{env:OPENAI_API_KEY}"}
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "anthropic/claude-sonnet-4-6", Label: "claude-sonnet-4-6", Provider: "anthropic", IsDefault: true},
		{ID: "anthropic/claude-opus-5", Label: "claude-opus-5", Provider: "anthropic"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestParseOpenCodeModelsAppendsUndeclaredConfiguredDefault(t *testing.T) {
	models, err := parseOpenCodeModels([]byte(`{
		"model": "zai/glm-5.2",
		"provider": {"anthropic": {"models": {"claude-haiku-4-5": {}}}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "zai/glm-5.2", Label: "zai/glm-5.2", Provider: "zai", IsDefault: true},
		{ID: "anthropic/claude-haiku-4-5", Label: "claude-haiku-4-5", Provider: "anthropic"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestParseOpenCodeModelsWithoutProviderModelsReturnsEmpty(t *testing.T) {
	models, err := parseOpenCodeModels([]byte(`{"provider": {"anthropic": {"options": {}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("models = %#v, want none", models)
	}
}

func TestParseOpenCodeModelsRejectsMalformedJSON(t *testing.T) {
	if _, err := parseOpenCodeModels([]byte(`{"provider": {`)); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestStripJSONCommentsKeepsCommentLikeStrings(t *testing.T) {
	raw := []byte(`{"url": "https://example.com/*", "a": 1} // trailing`)
	var parsed struct {
		URL string `json:"url"`
		A   int    `json:"a"`
	}
	if err := json.Unmarshal(stripJSONComments(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.URL != "https://example.com/*" || parsed.A != 1 {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestOpenCodeDiscoveryPrefersConfiguredModelsWithoutRunningBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	work := t.TempDir()
	config := filepath.Join(work, "opencode.json")
	if err := os.WriteFile(config, []byte(`{
		"model": "anthropic/claude-sonnet-4-6",
		"provider": {"anthropic": {"models": {"claude-sonnet-4-6": {}, "claude-opus-5": {}}}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// No binary is passed: a CLI attempt would fail, so a successful config
	// catalog proves the CLI fallback was not needed.
	got, err := Discover(context.Background(), "opencode", "", work, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "config" || len(got.Models) != 2 ||
		got.Models[0].ID != "anthropic/claude-sonnet-4-6" || !got.Models[0].IsDefault {
		t.Fatalf("catalog = %#v", got)
	}
}

func TestOpenCodeDiscoveryFallsBackToCLIWithoutUsableConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	work := t.TempDir()

	// No config anywhere: discovery must fall through to the CLI path, which
	// reports the missing binary rather than the missing configuration.
	_, err := Discover(context.Background(), "opencode", "", work, nil)
	if err == nil || !strings.Contains(err.Error(), "agent binary is not installed") {
		t.Fatalf("error = %v, want CLI fallback missing-binary error", err)
	}

	// A config that pins no provider models also falls back to the CLI.
	if err := os.WriteFile(filepath.Join(work, "opencode.json"), []byte(`{"provider": {"anthropic": {}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Discover(context.Background(), "opencode", "", work, nil)
	if err == nil || !strings.Contains(err.Error(), "agent binary is not installed") {
		t.Fatalf("error = %v, want CLI fallback missing-binary error", err)
	}
}

func TestKiloCodeDiscoveryReadsKiloConfigWithoutRunningBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "kilo.jsonc"), []byte(`{
		// Kilo Code CLI 1.0 reads kilo.json / kilo.jsonc (opencode fork format).
		"model": "kilocode/kimi-for-coding",
		"provider": {"kilocode": {"models": {"kimi-for-coding": {}}}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(context.Background(), "kilocode", "", work, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "config" || len(got.Models) != 1 ||
		got.Models[0].ID != "kilocode/kimi-for-coding" || !got.Models[0].IsDefault {
		t.Fatalf("catalog = %#v", got)
	}
}

func TestKiloCodeDiscoveryFallsBackToCLIWithoutConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	_, err := Discover(context.Background(), "kilocode", "", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "agent binary is not installed") {
		t.Fatalf("error = %v, want CLI fallback missing-binary error", err)
	}
}

func TestOpenCodeDiscoveryUsesInheritedConfigOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	config := filepath.Join(t.TempDir(), "custom-opencode.json")
	if err := os.WriteFile(config, []byte(`{
		"provider": {"anthropic": {"models": {"claude-sonnet-4-6": {}}}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG", config)

	got, err := Discover(context.Background(), "opencode", "", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("catalog = %#v", got)
	}
}

func TestOpenCodeDiscoveryUsesXDGAncestorAndLegacyConfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalDir := filepath.Join(xdg, "opencode")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"provider": {"global": {"models": {"global-model": {}}}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), []byte(`{
		"provider": {"project": {"models": {"project-model": {}}}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(project, "nested", "package")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(context.Background(), "opencode", "", nested, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"global/global-model": true, "project/project-model": true}
	for _, model := range got.Models {
		delete(want, model.ID)
	}
	if len(want) != 0 {
		t.Fatalf("catalog = %#v, missing models = %#v", got, want)
	}
}

func TestKiloCodeDiscoveryUsesAncestorKilocodeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(project, ".kilocode")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "kilo.json"), []byte(`{
		"provider": {"kilocode": {"models": {"kimi-for-coding": {}}}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(project, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(context.Background(), "kilocode", "", nested, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "kilocode/kimi-for-coding" {
		t.Fatalf("catalog = %#v", got)
	}
}

func TestProjectConfigEnvironmentOverridesInheritedConfigPath(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "/inherited/opencode.json")
	paths := modelConfigPaths("opencode", "/work/project", map[string]string{
		"OPENCODE_CONFIG": "project-opencode.json",
	})
	want := filepath.Join("/work/project", "project-opencode.json")
	if !containsPath(paths, want) || containsPath(paths, "/inherited/opencode.json") {
		t.Fatalf("paths = %#v, want project override %q without inherited path", paths, want)
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func TestConfigDiscoveryFingerprintTracksOpenCodeConfigEdits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	work := t.TempDir()
	path := filepath.Join(work, "opencode.json")
	if err := os.WriteFile(path, []byte(`{"provider":{"anthropic":{"models":{"claude-sonnet-4-6":{}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := CatalogFingerprint(context.Background(), "opencode", "", work, nil)
	if before == "" {
		t.Fatal("fingerprint is empty for a configured opencode project")
	}
	if err := os.WriteFile(path, []byte(`{"provider":{"anthropic":{"models":{"claude-opus-5":{}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if after := CatalogFingerprint(context.Background(), "opencode", "", work, nil); after == before {
		t.Fatalf("fingerprint did not change after config edit: %q", before)
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
