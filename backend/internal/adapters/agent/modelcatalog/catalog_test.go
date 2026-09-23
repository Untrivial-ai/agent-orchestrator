package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestModelCommandUsesProjectWorkingDirectory(t *testing.T) {
	cmd := modelCommand(context.Background(), "agent", []string{"models"}, "/work/project", map[string]string{"OPENCODE_CONFIG": "/project/opencode.json"})
	if cmd.Dir != "/work/project" {
		t.Fatalf("Dir = %q, want /work/project", cmd.Dir)
	}
	if cmd.WaitDelay != commandTerminationWait {
		t.Fatalf("WaitDelay = %s, want %s", cmd.WaitDelay, commandTerminationWait)
	}
	if !environmentContains(cmd.Env, "OPENCODE_CONFIG=/project/opencode.json") {
		t.Fatalf("Env does not contain project override: %#v", cmd.Env)
	}
}

func environmentContains(env []string, wanted string) bool {
	for _, item := range env {
		if item == wanted {
			return true
		}
	}
	return false
}

func TestCommandDiscoveryTimeoutAllowsSlowModelRegistries(t *testing.T) {
	if commandTimeout < 20*time.Second {
		t.Fatalf("commandTimeout = %s, want at least 20s", commandTimeout)
	}
}

func TestModelDiscoveryErrorExplainsTimeout(t *testing.T) {
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	err := modelDiscoveryError(deadlineCtx, "kilocode", errors.New("signal: killed"))
	if !strings.Contains(err.Error(), "kilocode model discovery timed out after 20s") {
		t.Fatalf("error = %q, want clear timeout", err)
	}
}

func TestOpenCodeDiscoveryUsesPureMode(t *testing.T) {
	spec := commandSpecs["opencode"]
	if len(spec.args) != 2 || spec.args[0] != "--pure" || spec.args[1] != "models" {
		t.Fatalf("opencode discovery args = %q, want [--pure models]", spec.args)
	}
}

func TestAiderUsesDocumentedDiscoveryCommand(t *testing.T) {
	spec := commandSpecs["aider"]
	want := []string{"--no-check-update", "--no-git", "--no-gitignore", "--no-analytics", "--list-models", "."}
	if strings.Join(spec.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("aider discovery args = %q, want %q", spec.args, want)
	}
}

func TestOMPAndHelpBackedAgentsUseDocumentedDiscoveryCommands(t *testing.T) {
	tests := []struct {
		agent string
		want  []string
	}{
		{agent: "omp", want: []string{"models", "--json"}},
		{agent: "copilot", want: []string{"help", "config"}},
		{agent: "droid", want: []string{"exec", "--help"}},
		{agent: "crush", want: []string{"models"}},
	}
	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			spec, ok := commandSpecs[tc.agent]
			if !ok {
				t.Fatalf("%s has no discovery command", tc.agent)
			}
			if !reflect.DeepEqual(spec.args, tc.want) {
				t.Fatalf("%s discovery args = %q, want %q", tc.agent, spec.args, tc.want)
			}
			if spec.parser == nil {
				t.Fatalf("%s discovery parser is nil", tc.agent)
			}
		})
	}
}

func TestBaseClassifiesStaticTextAndModeAgents(t *testing.T) {
	tests := []struct {
		agent string
		mode  ports.ModelSelectionMode
		count int
	}{
		{agent: "claude-code", mode: ports.ModelSelectionCatalog},
		{agent: "codex", mode: ports.ModelSelectionCatalog},
		{agent: "amp", mode: ports.ModelSelectionModeList, count: 4},
		{agent: "muse", mode: ports.ModelSelectionCatalog, count: 3},
		{agent: "aider", mode: ports.ModelSelectionCatalog},
		{agent: "autohand", mode: ports.ModelSelectionCatalog},
		{agent: "kimchi", mode: ports.ModelSelectionCatalog},
		{agent: "prime-agent", mode: ports.ModelSelectionCatalog},
		{agent: "qwen", mode: ports.ModelSelectionCatalog},
		{agent: "copilot", mode: ports.ModelSelectionCatalog},
		{agent: "droid", mode: ports.ModelSelectionCatalog},
		{agent: "continue", mode: ports.ModelSelectionCatalog},
		{agent: "crush", mode: ports.ModelSelectionCatalog},
		{agent: "omp", mode: ports.ModelSelectionCatalog},
	}
	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			got := Base(tc.agent)
			if got.SelectionMode != tc.mode || len(got.Models) != tc.count {
				t.Fatalf("Base(%q) = %#v", tc.agent, got)
			}
		})
	}
}

func TestMuseReturnsStaticCatalogWithoutStartingAgent(t *testing.T) {
	got, err := (Discoverer{}).Discover(context.Background(), ports.AgentModelDiscoveryRequest{
		AgentID: "muse",
		Binary:  "/missing/muse",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "muse-spark", Label: "Muse Spark", IsDefault: true},
		{ID: "muse-spark-1.1", Label: "Muse Spark 1.1"},
		{ID: "muse-spark-1.2", Label: "Muse Spark 1.2"},
	}
	if got.Source != "official-catalog" || !reflect.DeepEqual(got.Models, want) {
		t.Fatalf("catalog = %#v, want models %#v", got, want)
	}
}

func TestClaudeReturnsStaticCatalogWithConfiguredFallback(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("HOME", t.TempDir())
	got, err := (Discoverer{}).Discover(context.Background(), ports.AgentModelDiscoveryRequest{
		AgentID: "claude-code",
		Binary:  "/missing/claude",
		Env:     map[string]string{"ANTHROPIC_MODEL": "claude-opus-4-5-20251101"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantLabels := map[string]string{
		"sonnet": "Sonnet", "fable": "Fable 5.1", "opus": "Opus",
		"haiku": "Haiku", "opus[1m]": "Opus (1M context)",
		"claude-opus-4-5-20251101": "claude-opus-4-5-20251101",
	}
	if got.Source != "catalog" || len(got.Models) != len(wantLabels) {
		t.Fatalf("catalog = %#v", got)
	}
	for _, item := range got.Models {
		if wantLabels[item.ID] != item.Label {
			t.Fatalf("unexpected model %#v", item)
		}
		if item.IsDefault != (item.ID == "claude-opus-4-5-20251101") {
			t.Fatalf("default marker = %#v", item)
		}
	}
}

func TestCustomModelEntryPolicy(t *testing.T) {
	tests := []struct {
		agent         string
		wantEntryMode string
		wantSelection ports.ModelSelectionMode
	}{
		{agent: "claude-code", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "codex", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "opencode", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "grok", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "cursor", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "qwen", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "copilot", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "kimi", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "muse", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "droid", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "amp", wantEntryMode: "none", wantSelection: ports.ModelSelectionModeList},
		{agent: "agy", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "crush", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "aider", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "goose", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "auggie", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "continue", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "devin", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "omp", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "cline", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "kiro", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "kilocode", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "vibe", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "pi", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "kimchi", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "prime-agent", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "autohand", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
	}

	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			got := Base(tc.agent)
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if wire["customModelEntry"] != tc.wantEntryMode {
				t.Fatalf("Base(%q) customModelEntry = %#v, want %q", tc.agent, wire["customModelEntry"], tc.wantEntryMode)
			}
			if got.AllowCustom != (tc.wantEntryMode == "direct") {
				t.Fatalf("Base(%q) allowCustom = %v, want %v", tc.agent, got.AllowCustom, tc.wantEntryMode == "direct")
			}
			if got.SelectionMode != tc.wantSelection {
				t.Fatalf("Base(%q) selectionMode = %q, want %q", tc.agent, got.SelectionMode, tc.wantSelection)
			}
		})
	}
}

func TestPrimeAgentDiscoveryUsesDocumentedModelCommand(t *testing.T) {
	spec := commandSpecs["prime-agent"]
	want := []string{"model", "list"}
	if !reflect.DeepEqual(spec.args, want) {
		t.Fatalf("prime-agent discovery args = %q, want %q", spec.args, want)
	}
	if spec.parser == nil {
		t.Fatal("prime-agent parser is nil")
	}
}

func TestParsePrimeAgentModelsBuildsProviderQualifiedIDs(t *testing.T) {
	got, err := parsePiModels([]byte(`provider   model                 context  max-out  thinking  images
anthropic  claude-opus-4-8       200K     64K      yes       yes
openai     gpt-5.6-sol           400K     128K     yes       yes
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "anthropic/claude-opus-4-8", Label: "claude-opus-4-8", Provider: "anthropic"},
		{ID: "openai/gpt-5.6-sol", Label: "gpt-5.6-sol", Provider: "openai"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestBaseDynamicCatalogsContainNoAOOwnedModelIDs(t *testing.T) {
	for _, agentID := range []string{"claude-code", "codex"} {
		t.Run(agentID, func(t *testing.T) {
			got := Base(agentID)
			if got.SelectionMode != ports.ModelSelectionCatalog || !got.AllowCustom || got.Source != "cli" {
				t.Fatalf("Base(%q) = %#v", agentID, got)
			}
			if len(got.Models) != 0 {
				t.Fatalf("Base(%q) models = %#v, want no AO-owned model IDs", agentID, got.Models)
			}
		})
	}
}

func TestCodexDiscoveryUsesStructuredProviderCatalog(t *testing.T) {
	discoverer := Discoverer{CodexModels: func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.ChatModel, error) {
		return []ports.ChatModel{
			{ID: "gpt-current", DisplayName: "GPT Current", Default: true},
			{ID: "gpt-other", DisplayName: "GPT Other"},
		}, nil
	}}
	got, err := discoverer.Discover(context.Background(), ports.AgentModelDiscoveryRequest{AgentID: "codex", Binary: "/bin/codex"})
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "gpt-current", Label: "GPT Current", IsDefault: true},
		{ID: "gpt-other", Label: "GPT Other"},
	}
	if !reflect.DeepEqual(got.Models, want) || got.Source != "cli" {
		t.Fatalf("catalog = %#v, want models %#v", got, want)
	}
}

func TestClineDiscoveryUsesACPModelOptions(t *testing.T) {
	discoverer := Discoverer{ClineOptions: func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.ChatConfigOption, error) {
		return []ports.ChatConfigOption{
			{
				ID: "model", Name: "Model", Category: "model", Type: ports.ChatConfigOptionSelect,
				Current: ports.ChatConfigOptionValue{Select: "anthropic/claude-sonnet-4-6"},
				Choices: []ports.ChatConfigOptionChoice{
					{Value: "anthropic/claude-sonnet-4-6", Name: "Claude Sonnet 4.6", Group: "anthropic", GroupName: "Anthropic"},
					{Value: "openai/gpt-5.4", Name: "GPT-5.4", Group: "openai", GroupName: "OpenAI"},
				},
			},
			{ID: "mode", Name: "Mode", Category: "mode", Type: ports.ChatConfigOptionSelect},
		}, nil
	}}
	got, err := discoverer.Discover(context.Background(), ports.AgentModelDiscoveryRequest{AgentID: "cline", Binary: "/bin/cline"})
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "anthropic/claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Provider: "anthropic", IsDefault: true},
		{ID: "openai/gpt-5.4", Label: "GPT-5.4", Provider: "openai"},
	}
	if !reflect.DeepEqual(got.Models, want) || got.Source != "acp" {
		t.Fatalf("catalog = %#v, want models %#v from ACP", got, want)
	}
}

func TestParseIDLinesAcceptsOnlyWholeModelIDs(t *testing.T) {
	got, err := parseIDLines([]byte("\x1b[32mModels\x1b[0m\nanthropic/claude-sonnet\nopenai/gpt-5.4\nTip: use --model <id>\nopenai/gpt-5.4 duplicate\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "anthropic/claude-sonnet" || got[1].ID != "openai/gpt-5.4" {
		t.Fatalf("models = %#v", got)
	}
}

func TestParseAgyModelsUsesFirstColumnAsModelID(t *testing.T) {
	got, err := parseAgyModels([]byte(`gemini-3.7-flash-high  Gemini 3.7 Flash (High)
claude-sonnet-4-6  Claude Sonnet 4.6 (Thinking)
gpt-oss-120b-medium  GPT-OSS 120B (Medium)
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6 (Thinking)"},
		{ID: "gemini-3.7-flash-high", Label: "Gemini 3.7 Flash (High)"},
		{ID: "gpt-oss-120b-medium", Label: "GPT-OSS 120B (Medium)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestParseGrokModelsIgnoresAuthAndDefaultStatus(t *testing.T) {
	got, err := parseGrokModels([]byte(`You are not authenticated.

Default model: grok-4.5

Available models:
  * grok-4.5 (default)
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "grok-4.5" || !got[0].IsDefault {
		t.Fatalf("models = %#v", got)
	}
}

func TestParseCursorModelsStopsBeforeTip(t *testing.T) {
	got, err := parseCursorModels([]byte(`Available models

auto - Auto (default)
gpt-5.6-sol-high - GPT-5.6 Sol 1M High

Tip: use --model <id> to switch.
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "auto" || got[0].Label != "Auto" || !got[0].IsDefault {
		t.Fatalf("models = %#v", got)
	}
	if got[1].ID != "gpt-5.6-sol-high" || got[1].Label != "GPT-5.6 Sol 1M High" {
		t.Fatalf("models = %#v", got)
	}
}

func TestKimchiDiscoveryUsesListModelsFlag(t *testing.T) {
	spec := commandSpecs["kimchi"]
	if len(spec.args) != 1 || spec.args[0] != "--list-models" {
		t.Fatalf("kimchi discovery args = %q, want [--list-models]", spec.args)
	}
	if spec.parser == nil {
		t.Fatalf("kimchi parser is nil")
	}
}

func TestParseKimchiModelsBuildsProviderQualifiedIDs(t *testing.T) {
	got, err := parsePiModels([]byte(`provider              model                 context  max-out  thinking  images
kimchi-dev            deepseek-v4-flash     1.0M     1.0M     yes       no
kimchi-dev            glm-5.2-fp8           1.0M     1.0M     yes       no
kimchi-dev/anthropic  claude-sonnet-5       1M       128K     yes       yes
kimchi-dev/anthropic  claude-opus-4-8       1M       128K     yes       yes
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("models = %#v, want 4", got)
	}
	want := map[string]bool{
		"kimchi-dev/deepseek-v4-flash":         true,
		"kimchi-dev/glm-5.2-fp8":               true,
		"kimchi-dev/anthropic/claude-sonnet-5": true,
		"kimchi-dev/anthropic/claude-opus-4-8": true,
	}
	for _, m := range got {
		delete(want, m.ID)
		if m.Provider == "" {
			t.Fatalf("model %q has empty Provider", m.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("models = %#v, missing %#v", got, want)
	}
}

func TestParsePiModelsBuildsProviderQualifiedIDs(t *testing.T) {
	got, err := parsePiModels([]byte(`provider   model                       context  max-out  thinking  images
anthropic  claude-sonnet-4-6           1M       64K      yes       yes
openai     gpt-5.5                     272K     128K     yes       yes
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "anthropic/claude-sonnet-4-6" || got[1].ID != "openai/gpt-5.5" {
		t.Fatalf("models = %#v", got)
	}
}

func TestParseJSONModelsFindsNestedModels(t *testing.T) {
	got, err := parseJSONModels([]byte(`{"providers":[{"id":"anthropic","models":[{"modelId":"claude-sonnet","displayName":"Claude Sonnet","isDefault":true}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("models = %#v", got)
	}
	var found bool
	for _, model := range got {
		if model.ID == "claude-sonnet" && model.Label == "Claude Sonnet" && model.IsDefault {
			found = true
		}
	}
	if !found {
		t.Fatalf("models = %#v, want nested claude-sonnet", got)
	}
}

func TestParseOMPModelsUsesSelectorAsLaunchID(t *testing.T) {
	got, err := parseJSONModels([]byte(`{"models":[{"provider":"anthropic","id":"claude-opus-5","selector":"anthropic/claude-opus-5","name":"Claude Opus 5"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{{ID: "anthropic/claude-opus-5", Label: "Claude Opus 5", Provider: "anthropic"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestParseCopilotConfigModels(t *testing.T) {
	got, err := parseCopilotConfigModels([]byte("`model`: AI model to use.\n  - \"claude-fable-5\"\n  - \"gpt-5.6-sol\"\n`contextTier`: context tier.\n  - ignored\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{{ID: "claude-fable-5", Label: "claude-fable-5"}, {ID: "gpt-5.6-sol", Label: "gpt-5.6-sol"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestParseDroidHelpModels(t *testing.T) {
	got, err := parseDroidHelpModels([]byte("Available Models:\n  auto                    Auto Model\n  claude-opus-5           Opus 5 (default)\n  gpt-5.6-sol             GPT-5.6 Sol\n\nTool Controls:\n  --list-tools            List tools\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "claude-opus-5", Label: "Opus 5", IsDefault: true},
		{ID: "auto", Label: "Auto Model"},
		{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestParseJSONModelsUsesModelMapKeysAsSelectableIDs(t *testing.T) {
	got, err := parseJSONModels([]byte(`{
		"models": {
			"kimi-code/kimi-for-coding": {
				"provider": "managed:kimi-code",
				"model": "kimi-for-coding",
				"displayName": "K2.7 Coding"
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "kimi-code/kimi-for-coding" || got[0].Label != "K2.7 Coding" || got[0].Provider != "managed:kimi-code" {
		t.Fatalf("models = %#v, want provider-qualified Kimi config alias", got)
	}
}

func TestParseJSONModelsWalksGroupedModelsMaps(t *testing.T) {
	got, err := parseJSONModels([]byte(`{
		"models": {
			"available": [{"modelId": "claude-sonnet", "displayName": "Claude Sonnet"}]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "claude-sonnet" || got[0].Label != "Claude Sonnet" {
		t.Fatalf("models = %#v, want recursively discovered claude-sonnet", got)
	}
}

func TestParseJSONModelsWalksProviderGroupsWithNestedModels(t *testing.T) {
	got, err := parseJSONModels([]byte(`{
		"models": {
			"anthropic": {
				"provider": "anthropic",
				"models": [{"modelId": "claude-sonnet", "displayName": "Claude Sonnet"}]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "claude-sonnet" || got[0].Label != "Claude Sonnet" {
		t.Fatalf("models = %#v, want nested claude-sonnet without provider-group alias", got)
	}
}

func TestParseJSONModelsSupportsKiroAndDevinFields(t *testing.T) {
	got, err := parseJSONModels([]byte(`{
		"models": [{"model_name": "Auto", "model_id": "auto"}],
		"families": [{
			"slug": "claude-opus-5",
			"family_label": "Claude Opus 5",
			"variants": [{"model_uid": "claude-opus-5-high", "label": "Claude Opus 5 High"}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"auto":               true,
		"claude-opus-5":      true,
		"claude-opus-5-high": true,
	}
	for _, item := range got {
		delete(want, item.ID)
	}
	if len(want) != 0 {
		t.Fatalf("models = %#v, missing %#v", got, want)
	}
}

func writeClaudeSettings(t *testing.T, dir, model string) {
	t.Helper()
	settingsDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{}"
	if model != "" {
		body = `{"model": "` + model + `"}`
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogFingerprintTracksTheConfiguredClaudeCodeModel(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "")
	dir := t.TempDir()
	writeClaudeSettings(t, dir, "opus")

	first := CatalogFingerprint(context.Background(), "claude-code", "", dir, nil)
	if first == "" {
		t.Fatal("fingerprint is empty for a configured model")
	}

	// A settings edit changes the catalog, so it has to change the fingerprint —
	// otherwise the cached catalog stays authoritative forever.
	writeClaudeSettings(t, dir, "haiku")
	second := CatalogFingerprint(context.Background(), "claude-code", "", dir, nil)
	if second == first {
		t.Fatalf("fingerprint unchanged (%q) after the configured model changed", second)
	}

	writeClaudeSettings(t, dir, "opus")
	if again := CatalogFingerprint(context.Background(), "claude-code", "", dir, nil); again != first {
		t.Fatalf("fingerprint = %q, want %q for identical inputs", again, first)
	}
}

func TestCatalogFingerprintKeepsTheExecutableOnlyValueForConfiglessAgents(t *testing.T) {
	dir := t.TempDir()
	writeClaudeSettings(t, dir, "opus")
	// codex reads no configuration, so its fingerprint must stay byte-identical
	// to the executable fingerprint earlier daemons cached under.
	got := CatalogFingerprint(context.Background(), "codex", "codex", dir, nil)
	if want := BinaryVersion(context.Background(), "codex"); got != want {
		t.Fatalf("fingerprint = %q, want the executable fingerprint %q", got, want)
	}
}

func TestCatalogFingerprintDistinguishesConfiguredFromUnconfigured(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("HOME", t.TempDir())
	unset := t.TempDir()
	writeClaudeSettings(t, unset, "")
	configured := t.TempDir()
	writeClaudeSettings(t, configured, "opus")

	if CatalogFingerprint(context.Background(), "claude-code", "", unset, nil) ==
		CatalogFingerprint(context.Background(), "claude-code", "", configured, nil) {
		t.Fatal("configuring a model must change the fingerprint")
	}
}
