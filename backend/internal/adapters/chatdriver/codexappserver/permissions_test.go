package codexappserver

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const nativeReadOnlyResponse = `{"thread":{"id":"thread-1"},"approvalPolicy":"untrusted","approvalsReviewer":"user","sandbox":{"type":"readOnly","networkAccess":false}}`

func permissionParams(t *testing.T, f frame) map[string]any {
	t.Helper()
	var params map[string]any
	if err := json.Unmarshal(f.Params, &params); err != nil {
		t.Fatal(err)
	}
	return params
}

func TestPermissionModesAcrossChatStartResumeAndTurn(t *testing.T) {
	for _, tc := range []struct {
		mode                      ports.PermissionMode
		policy, sandbox, reviewer string
	}{
		{ports.PermissionModeDefault, "", "", ""},
		{ports.PermissionModeManual, "on-request", "read-only", "user"},
		{ports.PermissionModeDontAsk, "never", "workspace-write", "user"},
		{ports.PermissionMode("unknown"), "", "", ""},
		{ports.PermissionModeAcceptEdits, "on-request", "workspace-write", "user"},
		{ports.PermissionModeAuto, "on-request", "workspace-write", "auto_review"},
		{ports.PermissionModeBypassPermissions, "never", "danger-full-access", "user"},
	} {
		for _, resume := range []bool{false, true} {
			name := string(tc.mode) + "/start"
			if resume {
				name = string(tc.mode) + "/resume"
			}
			t.Run(name, func(t *testing.T) {
				d, srv := newTestDriver(t)
				srv.respondTo("thread/start", nativeReadOnlyResponse)
				var conv ports.ChatConversation
				var err error
				method := "thread/start"
				policy, sandbox, reviewer := tc.policy, tc.sandbox, tc.reviewer
				if resume {
					method = "thread/resume"
					conv, err = d.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "saved-thread", Permissions: tc.mode})
					if policy == "" {
						policy, sandbox, reviewer = "untrusted", "read-only", "user"
					}
				} else {
					conv, err = d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: tc.mode})
				}
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conv.Close() }()
				params := permissionParams(t, srv.awaitFrame(func(f frame) bool { return f.Method == method }))
				if policy == "" {
					for _, key := range []string{"approvalPolicy", "sandbox", "approvalsReviewer"} {
						if _, ok := params[key]; ok {
							t.Fatalf("Default sent %s: %#v", key, params)
						}
					}
				} else if params["approvalPolicy"] != policy || params["sandbox"] != sandbox || params["approvalsReviewer"] != reviewer {
					t.Fatalf("thread policy = %#v", params)
				}
				if _, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "test", Settings: ports.ChatTurnSettings{Approval: tc.mode}}); err != nil {
					t.Fatal(err)
				}
				turn := permissionParams(t, srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" }))
				if tc.policy == "" && resume {
					if turn["approvalPolicy"] != "untrusted" || !reflect.DeepEqual(turn["sandboxPolicy"], map[string]any{"type": "readOnly", "networkAccess": false}) {
						t.Fatalf("resumed native settings = %#v", turn)
					}
				} else if tc.policy == "" {
					for _, key := range []string{"approvalPolicy", "sandboxPolicy", "approvalsReviewer"} {
						if _, ok := turn[key]; ok {
							t.Fatalf("fresh native turn sent %s: %#v", key, turn)
						}
					}
				} else if turn["approvalPolicy"] != tc.policy || !reflect.DeepEqual(turn["sandboxPolicy"], turnSandboxPolicy(tc.sandbox)) || turn["approvalsReviewer"] != tc.reviewer {
					t.Fatalf("turn policy = %#v", turn)
				}
			})
		}
	}
}

func TestExplicitToDefaultRestoresProviderNativePermissions(t *testing.T) {
	for _, mode := range []ports.PermissionMode{ports.PermissionModeBypassPermissions, ports.PermissionModeManual, ports.PermissionModeDontAsk} {
		t.Run(string(mode), func(t *testing.T) {
			d, srv := newTestDriver(t)
			srv.respondTo("thread/start", nativeReadOnlyResponse)
			conv, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: mode})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conv.Close() }()
			if _, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "use defaults", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeDefault}}); err != nil {
				t.Fatal(err)
			}
			probe := srv.awaitFrame(func(f frame) bool {
				return f.Method == "thread/start" && strings.Contains(string(f.Params), `"ephemeral":true`)
			})
			probeParams := permissionParams(t, probe)
			if _, ok := probeParams["approvalPolicy"]; ok {
				t.Fatal("native probe overrode approvals")
			}
			if _, ok := probeParams["sandbox"]; ok {
				t.Fatal("native probe overrode sandbox")
			}
			if !srv.sentMethod("thread/unsubscribe") {
				t.Fatal("native probe left loaded")
			}
			turn := permissionParams(t, srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" }))
			if turn["approvalPolicy"] != "untrusted" || turn["approvalsReviewer"] != "user" || !reflect.DeepEqual(turn["sandboxPolicy"], map[string]any{"type": "readOnly", "networkAccess": false}) {
				t.Fatalf("native policy not restored: %#v", turn)
			}
		})
	}
}

func TestDefaultResetFailsClosedWithoutNativePermissions(t *testing.T) {
	for _, response := range []string{`{"thread":{"id":"thread-1"}}`, `{"thread":{"id":"thread-1"},"approvalPolicy":"never","approvalsReviewer":"user","sandbox":{"type":"unknown"}}`} {
		t.Run(response, func(t *testing.T) {
			d, srv := newTestDriver(t)
			conv, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: ports.PermissionModeBypassPermissions})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conv.Close() }()
			srv.respondTo("thread/start", response)
			if _, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "defaults", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeDefault}}); err == nil {
				t.Fatal("unresolved default admitted a turn")
			}
			if srv.sentMethod("turn/start") {
				t.Fatal("turn sent while native policy was unresolved")
			}
		})
	}
}

func TestDefaultResetAfterUncertainBypassTurn(t *testing.T) {
	d, srv := newTestDriver(t)
	srv.respondTo("thread/start", nativeReadOnlyResponse)
	conv, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: ports.PermissionModeDefault})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conv.Close() }()
	srv.replyError("turn/start", -32603, "turn could have been applied")
	if _, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "explicit", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions}}); err == nil {
		t.Fatal("expected uncertain send failure")
	}
	srv.mu.Lock()
	delete(srv.failures, "turn/start")
	srv.mu.Unlock()
	if _, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "native", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeDefault}}); err != nil {
		t.Fatal(err)
	}
	sent := srv.awaitFrame(func(f frame) bool {
		return f.Method == "turn/start" && strings.Contains(string(f.Params), `"untrusted"`)
	})
	if got := permissionParams(t, sent)["sandboxPolicy"]; !reflect.DeepEqual(got, map[string]any{"type": "readOnly", "networkAccess": false}) {
		t.Fatalf("uncertain bypass persisted: %#v", got)
	}
}

func TestDefaultResetFailsClosedOnProbeOrCleanupFailure(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/unsubscribe"} {
		t.Run(method, func(t *testing.T) {
			d, srv := newTestDriver(t)
			conv, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: ports.PermissionModeBypassPermissions})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conv.Close() }()
			srv.replyError(method, -32601, "unavailable")
			if _, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "native", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeDefault}}); err == nil {
				t.Fatal("unconfirmed reset admitted turn")
			}
			if srv.sentMethod("turn/start") {
				t.Fatal("unconfirmed reset sent turn")
			}
		})
	}
}

func TestReconnectedDefaultResetsStickyPermissionsBeforeAnUnspecifiedTurn(t *testing.T) {
	d, srv := newTestDriver(t)
	srv.respondTo("thread/start", nativeReadOnlyResponse)
	proc, err := d.spawn(context.Background(), "codex", "/tmp/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.persistent = true
	d.connectHost = func(context.Context, persistenthost.Config) (*persistenthost.Transport, error) {
		return &persistenthost.Transport{Stdin: proc.stdin, Stdout: proc.stdout, Reconnected: true, NextRequestID: 41}, nil
	}
	conv, err := d.Resume(context.Background(), ports.ChatResumeConfig{SessionID: "reconnected", ProviderConversationID: "retained-thread", DataDir: t.TempDir(), WorkspacePath: "/tmp/ws", Permissions: ports.PermissionModeDefault})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conv.Close() }()
	if srv.sentMethod("thread/start") || srv.sentMethod("thread/resume") {
		t.Fatal("reconnect changed a possibly running thread")
	}
	if _, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "inherit native defaults"}); err != nil {
		t.Fatal(err)
	}
	turn := permissionParams(t, srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" }))
	if turn["approvalPolicy"] != "untrusted" || !reflect.DeepEqual(turn["sandboxPolicy"], map[string]any{"type": "readOnly", "networkAccess": false}) {
		t.Fatalf("sticky host policy retained: %#v", turn)
	}
}

func TestResumeRestoresGranularApprovalsAndCompleteNativeSandbox(t *testing.T) {
	d, srv := newTestDriver(t)
	approval := map[string]any{"granular": map[string]any{"sandbox_approval": true, "rules": false, "mcp_elicitations": true, "request_permissions": false, "skill_approval": true}}
	sandbox := map[string]any{"type": "workspaceWrite", "networkAccess": true, "writableRoots": []any{"/tmp/ws", "/tmp/native-extra"}, "excludeSlashTmp": true, "excludeTmpdirEnvVar": true}
	response, err := json.Marshal(map[string]any{"thread": map[string]any{"id": "native-probe"}, "approvalPolicy": approval, "approvalsReviewer": "auto_review", "sandbox": sandbox})
	if err != nil {
		t.Fatal(err)
	}
	srv.respondTo("thread/start", string(response))
	conv, err := d.Resume(context.Background(), ports.ChatResumeConfig{ProviderConversationID: "saved", WorkspacePath: "/tmp/ws", Permissions: ports.PermissionModeDefault})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conv.Close() }()
	if _, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "native"}); err != nil {
		t.Fatal(err)
	}
	turn := permissionParams(t, srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" }))
	if !reflect.DeepEqual(turn["approvalPolicy"], approval) || !reflect.DeepEqual(turn["sandboxPolicy"], sandbox) || turn["approvalsReviewer"] != "auto_review" {
		t.Fatalf("native settings lost during resume: %#v", turn)
	}
}

func TestNativeProbeThreadStateCannotAffectConversation(t *testing.T) {
	d, srv := newTestDriver(t)
	conv, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conv.Close() }()
	for _, raw := range []string{
		`{"method":"thread/status/changed","params":{"threadId":"native-defaults","status":{"type":"active","activeFlags":[]}}}`,
		`{"method":"thread/closed","params":{"threadId":"native-defaults"}}`,
		`{"method":"thread/archived","params":{"threadId":"native-defaults"}}`,
		`{"method":"thread/status/changed","params":{"threadId":"thread-1","status":{"type":"idle"}}}`,
	} {
		srv.push(raw)
	}
	ev := nextEvent(t, conv.Events(), ports.ChatEventThreadState)
	if ev.ThreadState.Status != "idle" || ev.ThreadState.Closed || ev.ThreadState.Archived != nil {
		t.Fatalf("probe state escaped into conversation: %+v", ev.ThreadState)
	}
}
