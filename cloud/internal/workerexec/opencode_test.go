package workerexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestOpenCodeAgentName(t *testing.T) {
	// A short, stable constant regardless of session id: opencode renders it in
	// the TUI status bar, where a long per-session name overflowed. One AO agent
	// per session config, so the name only has to be unambiguous within that file.
	for _, in := range []string{"", "   ", "abc123", "a/b c", "sess_01-XYZ"} {
		if got := openCodeAgentName(in); got != "ao" {
			t.Errorf("openCodeAgentName(%q) = %q, want %q", in, got, "ao")
		}
	}
}

func TestWriteOpenCodeConfig(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "system.md")
	if err := os.WriteFile(promptFile, []byte("be helpful"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No prompt file -> no AO config (opencode runs on its own configuration).
	if path, err := writeOpenCodeConfig("", agentruntime.PermissionDefault, "s1", ""); err != nil || path != "" {
		t.Fatalf("no prompt file: got %q, %v; want \"\", nil", path, err)
	}

	path, err := writeOpenCodeConfig(promptFile, agentruntime.PermissionAcceptEdits, "s1", "opencode/space-bunny-free")
	if err != nil {
		t.Fatalf("writeOpenCodeConfig: %v", err)
	}
	var doc openCodeInlineConfig
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
	// accept-edits -> permission.edit = allow.
	if doc.Permission["edit"] != "allow" {
		t.Errorf("accept-edits -> permission.edit=allow; got %v", doc.Permission)
	}
	// v2: the model rides the config top level, not the argv, and not the agent.
	if doc.Model != "opencode/space-bunny-free" {
		t.Errorf("config model = %q, want opencode/space-bunny-free", doc.Model)
	}
	// v2: the AO agent is selected via default_agent (the --agent flag is gone).
	if doc.DefaultAgent != openCodeAgentName("s1") {
		t.Errorf("default_agent = %q, want %q", doc.DefaultAgent, openCodeAgentName("s1"))
	}
	agent, ok := doc.Agent[openCodeAgentName("s1")]
	if !ok || agent.Mode != "primary" || agent.Prompt != "{file:./system.md}" {
		t.Errorf("agent config wrong: ok=%v agent=%+v", ok, agent)
	}

	// Empty model leaves the config model unset (harness default), and bypass puts
	// the full-access rule on the agent so it outranks every config layer.
	bypassPath, err := writeOpenCodeConfig(promptFile, agentruntime.PermissionBypassPermissions, "s2", "")
	if err != nil {
		t.Fatalf("writeOpenCodeConfig (bypass): %v", err)
	}
	var doc2 openCodeInlineConfig
	data2, _ := os.ReadFile(bypassPath)
	if err := json.Unmarshal(data2, &doc2); err != nil {
		t.Fatalf("bypass config not valid JSON: %v", err)
	}
	if doc2.Model != "" {
		t.Errorf("empty model should leave config model unset; got %q", doc2.Model)
	}
	if perm := doc2.Agent[openCodeAgentName("s2")].Permission; perm != "allow" {
		t.Errorf("bypass -> agent permission=allow; got %v", perm)
	}
}

func TestInstallOpenCodeActivityPlugin(t *testing.T) {
	workspace := t.TempDir()
	if err := installOpenCodeActivityPlugin(workspace); err != nil {
		t.Fatalf("installOpenCodeActivityPlugin: %v", err)
	}
	pluginPath := filepath.Join(workspace, ".opencode", "plugins", "ao-activity.ts")
	data, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("plugin not written: %v", err)
	}
	// The plugin must carry the AO sentinel and shell the cloud hook bridge.
	if !strings.Contains(string(data), openCodePluginSentinel) {
		t.Error("plugin missing AO sentinel")
	}
	if !strings.Contains(string(data), "ao") || !strings.Contains(string(data), "hooks") || !strings.Contains(string(data), "opencode") {
		t.Error("plugin does not shell `ao hooks opencode <event>`")
	}
	// Re-installing overwrites AO's own file (idempotent).
	if err := installOpenCodeActivityPlugin(workspace); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	// It refuses to clobber a same-named file that is not AO-managed.
	if err := os.WriteFile(pluginPath, []byte("// user plugin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installOpenCodeActivityPlugin(workspace); err == nil {
		t.Error("install should refuse to overwrite a non-AO plugin file")
	}
}

func TestOpenCodeCredentialInjectsProviderEnv(t *testing.T) {
	cases := map[string]string{
		"opencode_api_key":   "OPENCODE_API_KEY",
		"anthropic_api_key":  "ANTHROPIC_API_KEY",
		"openai_api_key":     "OPENAI_API_KEY",
		"openrouter_api_key": "OPENROUTER_API_KEY",
	}
	for credType, wantEnv := range cases {
		cmd := &Command{Env: map[string]string{}}
		err := opencodeCredential{}.configure(HarnessBuilder{}, cmd, worker.CredentialResponse{
			Provider: "opencode", CredentialType: credType, Secret: "sk-secret",
		})
		if err != nil {
			t.Fatalf("configure(%s): %v", credType, err)
		}
		if cmd.Env[wantEnv] != "sk-secret" {
			t.Errorf("%s -> want env %s set; got env=%v", credType, wantEnv, cmd.Env)
		}
	}
	// Unsupported credential type is rejected.
	cmd := &Command{Env: map[string]string{}}
	if err := (opencodeCredential{}).configure(HarnessBuilder{}, cmd, worker.CredentialResponse{
		Provider: "opencode", CredentialType: "auth_json", Secret: "x",
	}); err == nil {
		t.Error("opencode auth_json should be rejected")
	}
}
