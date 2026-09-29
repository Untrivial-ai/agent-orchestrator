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

func TestOpenCodeLaunchArgs(t *testing.T) {
	got := openCodeLaunchArgs("opencode", "s1", "", nil, agentruntime.PermissionBypassPermissions, "do it")
	want := []string{"opencode", "--dangerously-skip-permissions", "--agent", "ao", "--prompt", "do it"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("launch argv = %v, want %v", got, want)
	}
	got = openCodeLaunchArgs("opencode", "s2", "", nil, agentruntime.PermissionAuto, "")
	want = []string{"opencode", "--auto", "--agent", "ao"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("auto launch argv = %v, want %v", got, want)
	}
	got = openCodeRestoreArgs("opencode", "s1", "", nil, agentruntime.PermissionDefault, "", "native-9")
	want = []string{"opencode", "--agent", "ao", "--session", "native-9"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("restore argv = %v, want %v", got, want)
	}
	// A selected model becomes opencode's --model; empty leaves it on its default.
	got = openCodeLaunchArgs("opencode", "s3", "anthropic/claude-opus-4-8", nil, agentruntime.PermissionDefault, "")
	want = []string{"opencode", "--model", "anthropic/claude-opus-4-8", "--agent", "ao"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("model launch argv = %v, want %v", got, want)
	}
}

func TestWriteOpenCodeConfig(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "system.md")
	if err := os.WriteFile(promptFile, []byte("be helpful"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if doc.Permission["edit"] != "allow" {
		t.Errorf("accept-edits -> permission.edit=allow; got %v", doc.Permission)
	}
	agent, ok := doc.Agent[openCodeAgentName("s1")]
	if !ok || agent.Mode != "primary" || agent.Prompt != "{file:./system.md}" {
		t.Errorf("agent config wrong: ok=%v agent=%+v", ok, agent)
	}
	// The selected model is pinned on the agent (opencode's authoritative layer)
	// so the TUI adopts it rather than its persisted default.
	if agent.Model != "opencode/space-bunny-free" {
		t.Errorf("agent model = %q, want opencode/space-bunny-free", agent.Model)
	}
	// Empty model leaves the agent model unset (harness default).
	pathNoModel, err := writeOpenCodeConfig(promptFile, agentruntime.PermissionDefault, "s2", "")
	if err != nil {
		t.Fatalf("writeOpenCodeConfig (no model): %v", err)
	}
	var doc2 openCodeInlineConfig
	data2, _ := os.ReadFile(pathNoModel)
	_ = json.Unmarshal(data2, &doc2)
	if m := doc2.Agent[openCodeAgentName("s2")].Model; m != "" {
		t.Errorf("empty model should leave agent model unset; got %q", m)
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
