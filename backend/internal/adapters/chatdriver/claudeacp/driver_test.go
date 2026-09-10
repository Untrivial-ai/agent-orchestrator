package claudeacp

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClaudeSessionMetaAppendsWithoutReplacingPreset(t *testing.T) {
	if got := claudeSessionMeta(acpdriver.LaunchConfig{}); got != nil {
		t.Fatalf("empty prompt metadata = %#v", got)
	}
	meta := claudeSessionMeta(acpdriver.LaunchConfig{SystemPrompt: "AO standing instructions"})
	prompt, ok := meta["systemPrompt"].(map[string]any)
	if !ok {
		t.Fatalf("systemPrompt = %#v", meta["systemPrompt"])
	}
	if prompt["type"] != "preset" || prompt["preset"] != "claude_code" || prompt["append"] != "AO standing instructions" {
		t.Fatalf("systemPrompt = %#v", prompt)
	}
}

func TestClaudeSessionMetaNeverIncludesReplayContext(t *testing.T) {
	meta := claudeSessionMeta(acpdriver.LaunchConfig{SystemPrompt: "AO standing instructions"})
	prompt := meta["systemPrompt"].(map[string]any)
	if strings.Contains(prompt["append"].(string), "replayed-conversation") {
		t.Fatal("replay context entered the system prompt")
	}
}

func TestClaudeSessionModeUsesAdapterModeIDs(t *testing.T) {
	tests := map[ports.PermissionMode]string{
		ports.PermissionModeDefault:           "",
		ports.PermissionModeAcceptEdits:       "acceptEdits",
		ports.PermissionModeAuto:              "auto",
		ports.PermissionModeBypassPermissions: "bypassPermissions",
	}
	for permission, want := range tests {
		if got := claudeSessionMode(permission); got != want {
			t.Errorf("mode(%q) = %q, want %q", permission, got, want)
		}
	}
}

func TestClaudeSessionOptionsUseACPConfigIDs(t *testing.T) {
	got := claudeSessionOptions(ports.ChatTurnSettings{Model: "sonnet", Effort: "high"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "sonnet"}, {ID: "effort", Value: "high"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}

func TestValidateClaudeACPExecutableRejectsWindowsCommandShims(t *testing.T) {
	tests := []struct {
		name    string
		binary  string
		goos    string
		wantErr bool
	}{
		{name: "native executable", binary: `C:\\npm\\claude.exe`, goos: "windows"},
		{name: "cmd shim", binary: `C:\\npm\\claude.cmd`, goos: "windows", wantErr: true},
		{name: "bat shim", binary: `C:\\npm\\claude.BAT`, goos: "windows", wantErr: true},
		{name: "non-Windows shim", binary: "/tmp/claude.cmd", goos: "linux"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateClaudeACPExecutable(tc.binary, tc.goos)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateClaudeACPExecutable(%q, %q) error = %v, wantErr %v", tc.binary, tc.goos, err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "native claude.exe") {
				t.Fatalf("error = %q, want actionable native executable guidance", err)
			}
		})
	}
}

func TestRuntimeCommandOverride(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_CLAUDE_ACP_COMMAND", executable)
	launch, err := resolveRuntime(context.Background())
	if err != nil {
		t.Fatalf("resolveRuntime: %v", err)
	}
	if launch.command != executable || len(launch.args) != 0 {
		t.Fatalf("runtime = %#v", launch)
	}
}

// I1: the auth check is strictly additive. It may turn unknown into a definite
// answer, and it may never block a launch that would otherwise have succeeded.
// Every way of failing to resolve or reach a credential — an unreadable
// keychain, a timeout, an unparsable CLI, a credential that is merely present
// — must let the session proceed exactly as before.
func TestPreflightOnlyBlocksOnAVerifiedRejection(t *testing.T) {
	tests := []struct {
		name      string
		status    ports.AgentAuthStatus
		err       error
		wantBlock bool
	}{
		{name: "configured but unverified", status: ports.AgentAuthStatusConfigured},
		{name: "inconclusive", status: ports.AgentAuthStatusUnknown},
		{name: "probe errored", status: ports.AgentAuthStatusUnknown, err: context.DeadlineExceeded},
		{name: "binary missing", status: ports.AgentAuthStatusUnavailable},
		{name: "unreadable credential reported as unauthorized alongside an error",
			status: ports.AgentAuthStatusUnauthorized, err: context.DeadlineExceeded},
		{name: "verified rejection", status: ports.AgentAuthStatusUnauthorized, wantBlock: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := authPreflightError(tc.status, tc.err)
			blocked := errors.Is(err, ports.ErrChatAuthRequired)
			if blocked != tc.wantBlock {
				t.Fatalf("blocked = %v (err %v), want %v", blocked, err, tc.wantBlock)
			}
		})
	}
}
