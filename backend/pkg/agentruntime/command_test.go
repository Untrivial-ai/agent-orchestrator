package agentruntime

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
			name: "opencode default permissions",
			cfg: LaunchConfig{
				Harness:   HarnessOpenCode,
				Binary:    "/usr/bin/opencode",
				SessionID: "session-1",
				Model:     " claude-sonnet ",
				Prompt:    "-fix auth",
			},
			want: []string{
				"/usr/bin/opencode",
				"--model", "claude-sonnet",
				"--prompt", "-fix auth",
			},
		},
		{
			name: "opencode bypass permissions with system prompt",
			cfg: LaunchConfig{
				Harness:          HarnessOpenCode,
				Binary:           "/usr/bin/opencode",
				SessionID:        "session-1",
				Permission:       PermissionBypassPermissions,
				SystemPrompt:     "act as worker",
				SystemPromptFile: systemPrompt,
				Prompt:           "fix auth",
			},
			want: []string{
				"/usr/bin/opencode",
				"--dangerously-skip-permissions",
				"--agent", OpenCodeAgentName("session-1"),
				"--prompt", "fix auth",
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
			name: "opencode metadata identity",
			cfg: RestoreConfig{
				Harness:    HarnessOpenCode,
				Binary:     "opencode",
				SessionID:  "session-1",
				Metadata:   map[string]string{MetadataKeyAgentSessionID: "thread-1"},
				Permission: PermissionBypassPermissions,
				Prompt:     "continue",
			},
			want: []string{
				"opencode",
				"--dangerously-skip-permissions",
				"--session", "thread-1",
				"--prompt", "continue",
			},
		},
		{
			name: "opencode restore re-applies agent selection",
			cfg: RestoreConfig{
				Harness:      HarnessOpenCode,
				Binary:       "opencode",
				SessionID:    "session-1",
				Metadata:     map[string]string{MetadataKeyAgentSessionID: "thread-1"},
				Model:        "  claude-opus-4-5  ",
				SystemPrompt: "act as worker",
			},
			want: []string{
				"opencode",
				"--model", "claude-opus-4-5",
				"--agent", OpenCodeAgentName("session-1"),
				"--session", "thread-1",
			},
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

func TestRestoreIdentityRequiresCapturedID(t *testing.T) {
	cmd, ok, err := BuildRestoreCommand(RestoreConfig{
		Harness:   HarnessOpenCode,
		Binary:    "opencode",
		SessionID: "session-1",
	})
	if err != nil || ok || cmd != nil {
		t.Fatalf("opencode restore = (%#v, %v, %v), want unavailable", cmd, ok, err)
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

func TestOpenCodePermissionArgs(t *testing.T) {
	tests := map[PermissionPolicy][]string{
		PermissionDefault:           nil,
		PermissionAcceptEdits:       nil,
		PermissionAuto:              nil,
		PermissionBypassPermissions: {"--dangerously-skip-permissions"},
		"unknown":                   nil,
	}
	for policy, want := range tests {
		if got := OpenCodePermissionArgs(policy); !reflect.DeepEqual(got, want) {
			t.Errorf("OpenCodePermissionArgs(%q) = %#v, want %#v", policy, got, want)
		}
	}
}

func TestOpenCodeAgentName(t *testing.T) {
	if name := OpenCodeAgentName("session-1"); name != "ao-session-1" {
		t.Fatalf("OpenCodeAgentName(session-1) = %q", name)
	}
	if name := OpenCodeAgentName("id with spaces"); name != "ao-id-with-spaces" {
		t.Fatalf("OpenCodeAgentName(id with spaces) = %q", name)
	}
	if name := OpenCodeAgentName(""); name != "ao-system-prompt" {
		t.Fatalf("OpenCodeAgentName() = %q", name)
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}