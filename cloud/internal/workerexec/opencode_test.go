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
	cases := map[string]string{
		"":            "ao-system-prompt",
		"   ":         "ao-system-prompt",
		"abc123":      "ao-abc123",
		"a/b c":       "ao-a-b-c",
		"--weird--":   "ao-weird",
		"sess_01-XYZ": "ao-sess_01-XYZ",
	}
	for in, want := range cases {
		if got := openCodeAgentName(in); got != want {
			t.Errorf("openCodeAgentName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenCodeLaunchArgs(t *testing.T) {
	got := openCodeLaunchArgs("opencode", "s1", nil, agentruntime.PermissionBypassPermissions, "do it")
	want := []string{"opencode", "--dangerously-skip-permissions", "--agent", "ao-s1", "--prompt", "do it"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("launch argv = %v, want %v", got, want)
	}
	got = openCodeLaunchArgs("opencode", "s2", nil, agentruntime.PermissionAuto, "")
	want = []string{"opencode", "--auto", "--agent", "ao-s2"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("auto launch argv = %v, want %v", got, want)
	}
	got = openCodeRestoreArgs("opencode", "s1", nil, agentruntime.PermissionDefault, "", "native-9")
	want = []string{"opencode", "--agent", "ao-s1", "--session", "native-9"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("restore argv = %v, want %v", got, want)
	}
}

func TestWriteOpenCodeConfig(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "system.md")
	if err := os.WriteFile(promptFile, []byte("be helpful"), 0o600); err != nil {
		t.Fatal(err)
	}
	if path, err := writeOpenCodeConfig("", agentruntime.PermissionDefault, "s1"); err != nil || path != "" {
		t.Fatalf("no prompt file: got %q, %v; want \"\", nil", path, err)
	}
	path, err := writeOpenCodeConfig(promptFile, agentruntime.PermissionAcceptEdits, "s1")
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
