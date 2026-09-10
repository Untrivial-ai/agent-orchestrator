package agentruntime

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestBuildLaunchCommands(t *testing.T) {
	systemPrompt := filepath.Join(t.TempDir(), "system prompt.md")
	if err := writeTestFile(systemPrompt, "worker instructions"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cfg  LaunchConfig
		want []string
	}{
		{
			name: "claude",
			cfg: LaunchConfig{
				Harness:          HarnessClaudeCode,
				Binary:           "/usr/bin/claude",
				SessionID:        "session-1",
				Permission:       PermissionAcceptEdits,
				AllowedTools:     []string{"Read", "Bash(git diff:*)"},
				DisallowedTools:  []string{"Write"},
				Model:            " claude-sonnet ",
				SystemPromptFile: systemPrompt,
				Prompt:           "-fix auth",
			},
			want: []string{
				"/usr/bin/claude",
				"--session-id", ClaudeSessionID("session-1"),
				"--permission-mode", "acceptEdits",
				"--allowedTools", "Read,Bash(git diff:*)",
				"--disallowedTools", "Write",
				"--model", "claude-sonnet",
				"--append-system-prompt-file", systemPrompt,
				"--", "-fix auth",
			},
		},
		{
			name: "codex",
			cfg: LaunchConfig{
				Harness:       HarnessCodex,
				Binary:        "/usr/bin/codex",
				WorkspacePath: "/workspace",
				Permission:    PermissionAuto,
				ProviderArgs:  []string{"-c", "hooks.SessionStart=[]"},
				Model:         " gpt-5 ",
				SystemPrompt:  "act as worker",
				Prompt:        "fix auth",
			},
			want: []string{
				"/usr/bin/codex",
				"-c", "check_for_update_on_startup=false",
				"-c", "notice.hide_rate_limit_model_nudge=true",
				"--dangerously-bypass-hook-trust",
				"--ask-for-approval", "on-request",
				"-c", `approvals_reviewer="auto_review"`,
				"-c", "hooks.SessionStart=[]",
				"-c", "projects={'/workspace'={trust_level=\"trusted\"}}",
				"--model", "gpt-5",
				"-c", "developer_instructions='act as worker'",
				"--", "fix auth",
			},
		},
		{
			name: "cursor",
			cfg: LaunchConfig{
				Harness:    HarnessCursor,
				Binary:     "/usr/bin/cursor-agent",
				Permission: PermissionBypassPermissions,
				Model:      " claude-4 ",
				Prompt:     "-fix auth",
			},
			want: []string{
				"/usr/bin/cursor-agent",
				"--yolo",
				"--model", "claude-4",
				"--", "-fix auth",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildLaunchCommand(test.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("command\nwant: %#v\n got: %#v", test.want, got)
			}
		})
	}
}

func TestBuildRestoreCommands(t *testing.T) {
	tests := []struct {
		name string
		cfg  RestoreConfig
		want []string
	}{
		{
			name: "claude fallback identity",
			cfg: RestoreConfig{
				Harness:    HarnessClaudeCode,
				Binary:     "claude",
				SessionID:  "session-1",
				Permission: PermissionBypassPermissions,
				Prompt:     "continue",
			},
			want: []string{
				"claude",
				"--permission-mode", "bypassPermissions",
				"--resume", ClaudeSessionID("session-1"),
				"--", "continue",
			},
		},
		{
			name: "codex metadata identity",
			cfg: RestoreConfig{
				Harness:    HarnessCodex,
				Binary:     "codex",
				Metadata:   map[string]string{MetadataKeyAgentSessionID: "thread-1"},
				Permission: PermissionAcceptEdits,
			},
			want: []string{
				"codex", "resume",
				"-c", "check_for_update_on_startup=false",
				"-c", "notice.hide_rate_limit_model_nudge=true",
				"--dangerously-bypass-hook-trust",
				"--ask-for-approval", "on-request",
				"thread-1",
			},
		},
		{
			name: "cursor metadata identity",
			cfg: RestoreConfig{
				Harness:    HarnessCursor,
				Binary:     "cursor-agent",
				Metadata:   map[string]string{MetadataKeyAgentSessionID: "chat-1"},
				Permission: PermissionAuto,
			},
			want: []string{"cursor-agent", "--force", "--resume", "chat-1"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok, err := BuildRestoreCommand(test.cfg)
			if err != nil || !ok {
				t.Fatalf("BuildRestoreCommand() = (%#v, %v, %v), want command", got, ok, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("command\nwant: %#v\n got: %#v", test.want, got)
			}
		})
	}
}

func TestRestoreIdentityRequiresCapturedIDOutsideClaude(t *testing.T) {
	for _, harness := range []Harness{HarnessCodex, HarnessCursor} {
		cmd, ok, err := BuildRestoreCommand(RestoreConfig{
			Harness:   harness,
			Binary:    "agent",
			SessionID: "session-1",
		})
		if err != nil || ok || cmd != nil {
			t.Fatalf("%s restore = (%#v, %v, %v), want unavailable", harness, cmd, ok, err)
		}
	}
}

func TestPermissionPolicyForMode(t *testing.T) {
	tests := map[SessionMode]PermissionPolicy{
		SessionModeReadOnly: PermissionDefault,
		SessionModeStandard: PermissionAuto,
		SessionModeTrusted:  PermissionBypassPermissions,
		"unknown":           PermissionDefault,
	}
	for mode, want := range tests {
		if got := PermissionPolicyForMode(mode); got != want {
			t.Errorf("PermissionPolicyForMode(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestClaudeNativeSessionIDValidation(t *testing.T) {
	id := uuid.NewString()
	cmd, err := BuildLaunchCommand(LaunchConfig{
		Harness:         HarnessClaudeCode,
		Binary:          "claude",
		SessionID:       "ignored",
		NativeSessionID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cmd, []string{"claude", "--session-id", id}) {
		t.Fatalf("command = %#v", cmd)
	}

	if _, err := BuildLaunchCommand(LaunchConfig{
		Harness:         HarnessClaudeCode,
		Binary:          "claude",
		NativeSessionID: "not-a-uuid",
	}); err == nil {
		t.Fatal("invalid native identity was accepted")
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
