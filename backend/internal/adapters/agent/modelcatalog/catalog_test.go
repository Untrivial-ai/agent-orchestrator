package modelcatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestAiderAndAutohandUseDocumentedDiscoveryCommands(t *testing.T) {
	tests := []struct {
		agent string
		want  []string
	}{
		{agent: "aider", want: []string{"--no-check-update", "--no-git", "--no-gitignore", "--no-analytics", "--list-models", "."}},
		{agent: "autohand", want: []string{"models", "list"}},
	}
	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			spec := commandSpecs[tc.agent]
			if strings.Join(spec.args, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("%s discovery args = %q, want %q", tc.agent, spec.args, tc.want)
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
		{agent: "claude-code", mode: ports.ModelSelectionCatalog, count: 3},
		{agent: "codex", mode: ports.ModelSelectionCatalog, count: 7},
		{agent: "amp", mode: ports.ModelSelectionModeList, count: 4},
		{agent: "muse", mode: ports.ModelSelectionCatalog, count: 3},
		{agent: "aider", mode: ports.ModelSelectionCatalog},
		{agent: "autohand", mode: ports.ModelSelectionCatalog},
		{agent: "kimchi", mode: ports.ModelSelectionCatalog},
		{agent: "qwen", mode: ports.ModelSelectionText},
		{agent: "continue", mode: ports.ModelSelectionText},
		{agent: "crush", mode: ports.ModelSelectionText},
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

func TestBaseMuseCatalogAllowsCustomModels(t *testing.T) {
	got := Base("muse")
	if got.SelectionMode != ports.ModelSelectionCatalog {
		t.Fatalf("SelectionMode = %q, want catalog", got.SelectionMode)
	}
	if !got.AllowCustom {
		t.Fatal("AllowCustom = false, want true")
	}
	if got.Source != "official-catalog" {
		t.Fatalf("Source = %q, want official-catalog", got.Source)
	}
	if len(got.Models) != 3 || got.Models[0].ID != "muse-spark" || !got.Models[0].IsDefault {
		t.Fatalf("models = %#v", got.Models)
	}
}

func TestParseAgyModelsAcceptsTabSeparatedLabels(t *testing.T) {
	got, err := parseAgyModels([]byte(
		"Fetching available models...\n" +
			"gemini-3.6-flash-high\tGemini 3.6 Flash (High)\n" +
			"claude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n",
	))
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("models = %#v, want 2", got)
	}

	byID := make(map[string]ports.AgentModelInfo, len(got))
	for _, model := range got {
		byID[model.ID] = model
	}

	gemini, ok := byID["gemini-3.6-flash-high"]
	if !ok {
		t.Fatalf("models = %#v, missing gemini-3.6-flash-high", got)
	}
	if gemini.Label != "Gemini 3.6 Flash (High)" {
		t.Fatalf("gemini label = %q, want %q", gemini.Label, "Gemini 3.6 Flash (High)")
	}

	claude, ok := byID["claude-sonnet-4-6"]
	if !ok {
		t.Fatalf("models = %#v, missing claude-sonnet-4-6", got)
	}
	if claude.Label != "Claude Sonnet 4.6 (Thinking)" {
		t.Fatalf("claude label = %q, want %q", claude.Label, "Claude Sonnet 4.6 (Thinking)")
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

func claudeDefaultID(catalog ports.AgentModelCatalog) string {
	for _, item := range catalog.Models {
		if item.IsDefault {
			return item.ID
		}
	}
	return ""
}

func TestClaudeCodeDiscoveryFlagsTheConfiguredAliasAsDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "")
	dir := t.TempDir()
	writeClaudeSettings(t, dir, "opus")

	got, err := Discover(context.Background(), "claude-code", "", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id := claudeDefaultID(got); id != "opus" {
		t.Fatalf("default = %q, want opus (models %#v)", id, got.Models)
	}
	if len(got.Models) != 3 {
		t.Fatalf("models = %#v, want the three published aliases", got.Models)
	}
}

func TestClaudeCodeDiscoveryAddsAConfiguredModelOutsideTheAliases(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "")
	dir := t.TempDir()
	writeClaudeSettings(t, dir, "opus[1m]")

	got, err := Discover(context.Background(), "claude-code", "", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Without the appended entry the picker would name a default it cannot select.
	if id := claudeDefaultID(got); id != "opus[1m]" {
		t.Fatalf("default = %q, want opus[1m] (models %#v)", id, got.Models)
	}
	if len(got.Models) != 4 {
		t.Fatalf("models = %#v, want the aliases plus the configured model", got.Models)
	}
}

func TestClaudeCodeDiscoveryPrefersNearerScopesAndProjectEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "")
	dir := t.TempDir()
	writeClaudeSettings(t, dir, "haiku")
	local := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.WriteFile(local, []byte(`{"model": "sonnet"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(context.Background(), "claude-code", "", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id := claudeDefaultID(got); id != "sonnet" {
		t.Fatalf("default = %q, want the local settings model", id)
	}

	// The project's own environment outranks every settings file.
	got, err = Discover(context.Background(), "claude-code", "", dir, map[string]string{"ANTHROPIC_MODEL": "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	if id := claudeDefaultID(got); id != "haiku" {
		t.Fatalf("default = %q, want the project env model", id)
	}
}

func TestClaudeCodeDiscoveryKeepsNoDefaultWhenNothingConfigured(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeClaudeSettings(t, dir, "")

	got, err := Discover(context.Background(), "claude-code", "", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// AO passes no --model, so the CLI decides. Guessing here would assert a
	// default AO cannot verify.
	if id := claudeDefaultID(got); id != "" {
		t.Fatalf("default = %q, want none", id)
	}
	if len(got.Models) != 3 {
		t.Fatalf("models = %#v, want the three published aliases", got.Models)
	}
}

func TestClaudeCodeDiscoveryIgnoresMalformedSettings(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{"model": `), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(context.Background(), "claude-code", "", dir, nil)
	if err != nil {
		t.Fatalf("malformed settings must not fail discovery: %v", err)
	}
	if id := claudeDefaultID(got); id != "" {
		t.Fatalf("default = %q, want none", id)
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
