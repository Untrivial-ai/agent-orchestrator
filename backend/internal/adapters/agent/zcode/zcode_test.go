package zcode

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "zcode" {
		t.Fatalf("ID = %q, want zcode", m.ID)
	}
	if m.Name != "ZCode" {
		t.Fatalf("Name = %q, want ZCode", m.Name)
	}
	hasAgent := false
	for _, c := range m.Capabilities {
		if c == adapters.CapabilityAgent {
			hasAgent = true
		}
	}
	if !hasAgent {
		t.Fatal("missing CapabilityAgent")
	}
}

func TestGetConfigSpecReportsMode(t *testing.T) {
	// zcode 0.16.5 exposes no --model launch flag (the model is pinned in
	// ZCode's own config), so the adapter exposes the --mode permission
	// modes instead.
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "mode" {
		t.Fatalf("unexpected fields: %#v", spec.Fields)
	}
	want := []string{"build", "edit", "plan", "yolo"}
	if !reflect.DeepEqual(spec.Fields[0].Enum, want) {
		t.Fatalf("enum = %#v, want %#v", spec.Fields[0].Enum, want)
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryAfterStart {
		t.Fatalf("strategy = %q, want after_start", s)
	}
}

func TestPromptReadinessHints(t *testing.T) {
	hints, err := (&Plugin{}).PromptReadinessHints(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hints.InitialDelay != 750*time.Millisecond || hints.PollInterval != 200*time.Millisecond || hints.Timeout != 10*time.Second || hints.Lines != 80 {
		t.Fatalf("hints = %#v", hints)
	}
	if !reflect.DeepEqual(hints.Patterns, []string{"Ask a task about this workspace", "/help commands"}) {
		t.Fatalf("patterns = %#v, want the shipped composer placeholder", hints.Patterns)
	}
}

func TestGetLaunchCommand(t *testing.T) {
	tests := []struct {
		name        string
		permissions ports.PermissionMode
		want        []string
	}{
		{"default omits mode flag", ports.PermissionModeDefault, []string{"zcode"}},
		{"accept edits maps to edit", ports.PermissionModeAcceptEdits, []string{"zcode", "--mode", "edit"}},
		{"auto maps to build", ports.PermissionModeAuto, []string{"zcode", "--mode", "build"}},
		{"bypass permissions maps to yolo", ports.PermissionModeBypassPermissions, []string{"zcode", "--mode", "yolo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "zcode"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Prompt:      "do the thing",
				Permissions: tt.permissions,
			})
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(cmd, tt.want) {
				t.Fatalf("cmd = %#v, want %#v", cmd, tt.want)
			}
			assertNoPromptFlag(t, cmd)
		})
	}
}

func TestGetLaunchCommandConfigModeWins(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "zcode"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:      "fix it",
		Permissions: ports.PermissionModeBypassPermissions,
		Config:      ports.AgentConfig{Mode: "plan"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"zcode", "--mode", "plan"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandRejectsInvalidConfigMode(t *testing.T) {
	// The real binary rejects unknown --mode values at launch ("Unsupported
	// --mode value"); a bad persisted config must fail here as a clean input
	// error, not later as a dead terminal session.
	plugin := &Plugin{resolvedBinary: "zcode"}
	_, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Config:      ports.AgentConfig{Mode: "nope"},
	})
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	// ValidateMode names the harness in the error; "invalid zcode mode" is
	// still the contract.
	if !strings.Contains(err.Error(), "invalid zcode mode") {
		t.Fatalf("err = %v, want invalid zcode mode", err)
	}
}

func TestGetLaunchCommandForwardsDisallowedTools(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "zcode"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:          "fix it",
		DisallowedTools: []string{"Bash(git-push*)", "WebFetch"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"zcode", "--disallowed-tools", "Bash(git-push*),WebFetch"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	assertNoPromptFlag(t, cmd)
}

func TestGetLaunchCommandRejectsCommaInToolRule(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "zcode"}
	_, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:          "fix it",
		DisallowedTools: []string{"Bash(git push,rebase)"},
	})
	if err == nil {
		t.Fatal("expected error for comma in disallowed tool rule")
	}
	if !strings.Contains(err.Error(), "zcode: disallowed tool rule") {
		t.Fatalf("err = %v, want disallowed tool rule error", err)
	}
}

func TestGetLaunchCommandDoesNotPassLeadingDashPromptAsSubcommand(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "zcode"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "-add a health check",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"zcode"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	assertNoPromptFlag(t, cmd)
}

func assertNoPromptFlag(t *testing.T, cmd []string) {
	t.Helper()
	for _, arg := range cmd {
		if arg == "--prompt" || arg == "-p" || arg == "--print" {
			t.Fatalf("cmd = %#v unexpectedly contains single-turn prompt flag %q", cmd, arg)
		}
	}
}

func TestGetRestoreCommand(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "zcode"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "sess_abc123",
			},
		},
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"zcode", "--mode", "yolo", "--resume", "sess_abc123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandMapsPermissionModes(t *testing.T) {
	tests := []struct {
		name        string
		permissions ports.PermissionMode
		want        []string
	}{
		{"accept edits", ports.PermissionModeAcceptEdits, []string{"zcode", "--mode", "edit", "--resume", "sess_abc123"}},
		{"auto maps to build", ports.PermissionModeAuto, []string{"zcode", "--mode", "build", "--resume", "sess_abc123"}},
		{"bypass permissions", ports.PermissionModeBypassPermissions, []string{"zcode", "--mode", "yolo", "--resume", "sess_abc123"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "zcode"}
			cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				Session: ports.SessionRef{
					Metadata: map[string]string{
						ports.MetadataKeyAgentSessionID: "sess_abc123",
					},
				},
				Permissions: tt.permissions,
			})
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !ok {
				t.Fatal("ok=false, want true")
			}
			if !reflect.DeepEqual(cmd, tt.want) {
				t.Fatalf("cmd = %#v, want %#v", cmd, tt.want)
			}
		})
	}
}

func TestGetRestoreCommandForwardsDisallowedTools(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "zcode"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "sess_abc123",
			},
		},
		DisallowedTools: []string{"WebFetch"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"zcode", "--disallowed-tools", "WebFetch", "--resume", "sess_abc123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandNoID(t *testing.T) {
	tests := []struct {
		name string
		ref  ports.SessionRef
	}{
		{"empty metadata", ports.SessionRef{Metadata: map[string]string{}}},
		{"blank agent session metadata", ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "   "}}},
		{"ao session id only", ports.SessionRef{ID: "ao-7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "zcode"}
			cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				Session: tt.ref,
			})
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if ok {
				t.Fatal("ok=true with no agentSessionId, want false")
			}
			if cmd != nil {
				t.Fatalf("cmd = %#v, want nil", cmd)
			}
		})
	}
}

func TestGetRestoreCommandRejectsCommaInToolRule(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "zcode"}
	_, _, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "sess_abc123",
			},
		},
		DisallowedTools: []string{"a,b"},
	})
	if err == nil {
		t.Fatal("expected error for comma in disallowed tool rule")
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "zcode"}
	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "sess_zcode-1",
			ports.MetadataKeyTitle:          "Fix login redirect",
			ports.MetadataKeySummary:        "Updated the auth callback and tests.",
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if info.AgentSessionID != "sess_zcode-1" {
		t.Fatalf("AgentSessionID = %q, want sess_zcode-1", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q, want Fix login redirect", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q", info.Summary)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "zcode"}
	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatalf("ok=true with empty metadata, want false")
	}
	if !reflect.DeepEqual(info, ports.SessionInfo{}) {
		t.Fatalf("info = %#v, want zero", info)
	}
}

func TestGetAgentHooksIsNoOp(t *testing.T) {
	// Zcode's hook config is user-global (~/.zcode/cli/config.json); there is
	// no workspace-scoped hook file, so the adapter must not install anything.
	plugin := &Plugin{resolvedBinary: "zcode"}
	err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: t.TempDir(),
		SessionID:     "zcode-test-1",
	})
	if err != nil {
		t.Fatalf("GetAgentHooks: %v", err)
	}
}
