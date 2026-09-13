package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReadOnlyStartResumeAndTurn(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "start"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			d, srv := newTestDriver(t)
			method := "thread/" + name
			srv.reply(method, `{"thread":{"id":"thread-1"},"approvalPolicy":"never","sandbox":{"type":"readOnly"}}`)
			var conv ports.ChatConversation
			var err error
			if resume {
				conv, err = d.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", Permissions: ports.PermissionModeReadOnly})
			} else {
				conv, err = d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: ports.PermissionModeReadOnly})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conv.Close() }()
			request := srv.awaitFrame(func(f frame) bool { return f.Method == method })
			var opened struct{ ApprovalPolicy, Sandbox string }
			if err := json.Unmarshal(request.Params, &opened); err != nil {
				t.Fatal(err)
			}
			if opened.ApprovalPolicy != "never" || opened.Sandbox != "read-only" {
				t.Fatalf("thread policy = %+v", opened)
			}
			// An omitted per-turn override must retain the verified thread default.
			if _, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "inspect probe.txt"}); err != nil {
				t.Fatal(err)
			}
			assertReadOnlyTurn(t, srv)
		})
	}
}

func assertReadOnlyTurn(t *testing.T, srv *scriptedServer) {
	t.Helper()
	request := srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" })
	var sent struct {
		ApprovalPolicy string
		SandboxPolicy  struct{ Type string }
	}
	if err := json.Unmarshal(request.Params, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.ApprovalPolicy != "never" || sent.SandboxPolicy.Type != "readOnly" {
		t.Fatalf("turn policy = %+v", sent)
	}
}

func TestReadOnlyRequiresProviderConfirmation(t *testing.T) {
	for _, response := range []string{
		`{"thread":{"id":"thread-1"}}`,
		`{"thread":{"id":"thread-1"},"approvalPolicy":{"granular":{"sandbox_approval":true,"rules":false,"mcp_elicitations":true,"request_permissions":false,"skill_approval":true}},"sandbox":{"type":"readOnly"}}`,
		`{"thread":{"id":"thread-1"},"approvalPolicy":"on-request","sandbox":{"type":"readOnly"}}`,
		`{"thread":{"id":"thread-1"},"approvalPolicy":"never","sandbox":{"type":"workspaceWrite"}}`,
	} {
		for _, resume := range []bool{false, true} {
			d, srv := newTestDriver(t)
			srv.reply("thread/start", response)
			srv.reply("thread/resume", response)
			var conv ports.ChatConversation
			var err error
			if resume {
				conv, err = d.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", Permissions: ports.PermissionModeReadOnly})
			} else {
				conv, err = d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: ports.PermissionModeReadOnly})
			}
			if conv != nil || !errors.Is(err, ports.ErrChatPermissionModeUnsupported) {
				t.Fatalf("resume=%v response=%s: conversation=%v error=%v", resume, response, conv, err)
			}
			if srv.sentMethod("turn/start") {
				t.Fatal("unconfirmed controller dispatched work")
			}
		}
	}
}

func TestStartResumeAcceptGranularApprovalPolicyOutsideReadOnly(t *testing.T) {
	for _, mode := range []ports.PermissionMode{ports.PermissionModeDefault, ports.PermissionModeAcceptEdits, ports.PermissionModeAuto, ports.PermissionModeBypassPermissions} {
		for _, method := range []string{"thread/start", "thread/resume"} {
			t.Run(string(mode)+"/"+method, func(t *testing.T) {
				d, srv := newTestDriver(t)
				srv.reply(method, `{"thread":{"id":"thread-1"},"approvalPolicy":{"granular":{"sandbox_approval":true,"rules":false,"mcp_elicitations":true,"request_permissions":false,"skill_approval":true}},"sandbox":{"type":"readOnly"}}`)
				var conv ports.ChatConversation
				var err error
				if method == "thread/resume" {
					conv, err = d.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", Permissions: mode})
				} else {
					conv, err = d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: mode})
				}
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conv.Close() }()
				if _, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "inspect"}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestReadOnlyReconnectRequiresRetainedProviderState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    map[string]persistenthost.CodexPermissions
		accepted bool
	}{
		{"legacy host unknown", nil, false},
		{"different thread", map[string]persistenthost.CodexPermissions{"other": {ApprovalPolicy: "never", SandboxType: "readOnly"}}, false},
		{"write access", map[string]persistenthost.CodexPermissions{"thread-1": {ApprovalPolicy: "never", SandboxType: "dangerFullAccess"}}, false},
		{"confirmed", map[string]persistenthost.CodexPermissions{"thread-1": {ApprovalPolicy: "never", SandboxType: "readOnly"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, srv := newTestDriver(t)
			proc, err := d.spawn(context.Background(), "codex", "/tmp/ws", nil)
			if err != nil {
				t.Fatal(err)
			}
			d.persistent = true
			d.connectHost = func(context.Context, persistenthost.Config) (*persistenthost.Transport, error) {
				return &persistenthost.Transport{Stdin: proc.stdin, Stdout: proc.stdout, Reconnected: true, CodexPermissions: tc.state}, nil
			}
			conv, err := d.Resume(context.Background(), ports.ChatResumeConfig{SessionID: "readonly", DataDir: t.TempDir(), WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", Permissions: ports.PermissionModeReadOnly})
			if tc.accepted {
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conv.Close() }()
				if _, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "continue reading"}); err != nil {
					t.Fatal(err)
				}
				assertReadOnlyTurn(t, srv)
			} else if conv != nil || !errors.Is(err, ports.ErrChatRecoveryInconclusive) {
				t.Fatalf("unknown/broader reconnect = %v, %v", conv, err)
			}
			if srv.sentMethod("initialize") || srv.sentMethod("thread/resume") {
				t.Fatal("reattachment restarted the live provider")
			}
		})
	}
}

func TestReadOnlyTurnOverridesMutableThread(t *testing.T) {
	d, srv := newTestDriver(t)
	conv, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: ports.PermissionModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conv.Close() }()
	if _, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "inspect", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeReadOnly}}); err != nil {
		t.Fatal(err)
	}
	assertReadOnlyTurn(t, srv)
}
