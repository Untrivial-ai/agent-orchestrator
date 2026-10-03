package workerexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	acp "github.com/coder/acp-go-sdk"
)

func TestReadCodexTurnStatusFindsExactCompletedTurn(t *testing.T) {
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	defer requestReader.Close()
	defer requestWriter.Close()
	defer responseReader.Close()
	defer responseWriter.Close()
	conn := newCodexRPC(requestWriter, responseReader)
	served := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(requestReader).ReadBytes('\n')
		if err != nil {
			served <- err
			return
		}
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				ThreadID     string `json:"threadId"`
				IncludeTurns bool   `json:"includeTurns"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			served <- err
			return
		}
		if request.Method != "thread/read" || request.Params.ThreadID != "thread-1" || !request.Params.IncludeTurns {
			served <- errors.New("thread status read omitted the exact thread or turns")
			return
		}
		_, err = responseWriter.Write([]byte(`{"id":1,"result":{"thread":{"id":"thread-1","turns":[{"id":"older","status":"completed"},{"id":"current","status":"completed"}]}}}` + "\n"))
		served <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := readCodexTurnStatus(ctx, conn, "thread-1", "current")
	if err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("turn status = %q, want completed", status)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

func TestCodexSteerWaitsForProviderAcknowledgement(t *testing.T) {
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	defer requestReader.Close()
	defer requestWriter.Close()
	defer responseReader.Close()
	defer responseWriter.Close()
	connection := newCodexRPC(requestWriter, responseReader)
	session := &codexSession{conn: connection, threadID: "thread-1", turnID: "cloud-turn", providerTurnID: "provider-turn"}
	served := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(requestReader).ReadBytes('\n')
		if err != nil {
			served <- err
			return
		}
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				ThreadID       string `json:"threadId"`
				ExpectedTurnID string `json:"expectedTurnId"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			served <- err
			return
		}
		if request.Method != "turn/steer" || request.Params.ThreadID != "thread-1" || request.Params.ExpectedTurnID != "provider-turn" {
			served <- errors.New("steer did not target the active provider turn")
			return
		}
		_, err = responseWriter.Write([]byte(`{"id":1,"result":{"turnId":"provider-turn"}}` + "\n"))
		served <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Steer(ctx, "cloud-turn", "change course"); err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

func TestACPDoesNotDiscardCommandDenyRules(t *testing.T) {
	s := &Supervisor{}
	err := s.runACP(context.Background(), worker.Turn{Harness: "claude-code", DeniedCommands: []string{"git push"}}, Command{}, nil, nil)
	if !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("ACP deny rule = %v, want fail closed", err)
	}
}

func TestCodexAppServerApprovalSettingsMatchLocal(t *testing.T) {
	for _, test := range []struct{ mode, approval, policy, sandbox, reviewer string }{
		{"trusted", "default", "never", "danger-full-access", "user"},
		{"trusted", "bypass-permissions", "never", "danger-full-access", "user"},
		{"standard", "accept-edits", "on-request", "workspace-write", "user"},
		{"standard", "auto", "on-request", "workspace-write", "auto_review"},
		{"read-only", "", "never", "read-only", "user"},
	} {
		policy, sandbox, reviewer := codexApprovalSettings(worker.Turn{Mode: test.mode, ApprovalMode: test.approval})
		if policy != test.policy || sandbox != test.sandbox || reviewer != test.reviewer {
			t.Fatalf("%s/%s = %s/%s/%s", test.mode, test.approval, policy, sandbox, reviewer)
		}
	}
}

func TestCursorACPAcceptEditsOnlyApprovesFileChanges(t *testing.T) {
	options := []acp.PermissionOption{
		{OptionId: "allow", Kind: acp.PermissionOptionKindAllowOnce},
		{OptionId: "reject", Kind: acp.PermissionOptionKindRejectOnce},
	}
	for _, test := range []struct {
		kind    acp.ToolKind
		approve bool
	}{
		{acp.ToolKindEdit, true}, {acp.ToolKindDelete, true}, {acp.ToolKindMove, true}, {acp.ToolKindExecute, false},
	} {
		kind := test.kind
		option, approved := automaticACPDecision(worker.Turn{Harness: "cursor", ApprovalMode: "accept-edits"}, acp.RequestPermissionRequest{
			ToolCall: acp.ToolCallUpdate{Kind: &kind}, Options: options,
		})
		if approved != test.approve || (approved && option != "allow") {
			t.Fatalf("%s = %s/%v", kind, option, approved)
		}
	}
}

type approvalFlowControl struct {
	created       chan worker.ChatApproval
	decision      string
	waitForCancel bool
}

type modeSpyAgent struct {
	acp.Agent
	selected chan acp.SessionModeId
}

func (a *modeSpyAgent) SetSessionMode(_ context.Context, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	a.selected <- request.ModeId
	return acp.SetSessionModeResponse{}, nil
}

func TestClaudeACPApprovalModesUseSessionSetMode(t *testing.T) {
	modes := &acp.SessionModeState{AvailableModes: []acp.SessionMode{
		{Id: "default"}, {Id: "plan"}, {Id: "acceptEdits"}, {Id: "auto"}, {Id: "bypassPermissions"},
	}}
	for _, test := range []struct {
		name, mode, approval, want string
	}{
		{"ask for approval", "standard", "default", "default"},
		{"read only", "read-only", "", "plan"},
		{"accept edits", "standard", "accept-edits", "acceptEdits"},
		{"auto review", "standard", "auto", "auto"},
		{"bypass permissions", "trusted", "bypass-permissions", "bypassPermissions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientToAgentR, clientToAgentW := io.Pipe()
			agentToClientR, agentToClientW := io.Pipe()
			defer clientToAgentR.Close()
			defer clientToAgentW.Close()
			defer agentToClientR.Close()
			defer agentToClientW.Close()
			agent := &modeSpyAgent{selected: make(chan acp.SessionModeId, 1)}
			_ = acp.NewAgentSideConnection(agent, agentToClientW, clientToAgentR)
			conn := acp.NewClientSideConnection(&cloudACPClient{}, clientToAgentW, agentToClientR)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := configureACPSession(ctx, conn, "session-1", worker.Turn{
				Harness: "claude-code", Mode: test.mode, ApprovalMode: test.approval,
			}, nil, modes); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-agent.selected:
				if string(got) != test.want {
					t.Fatalf("ACP mode = %q, want %q", got, test.want)
				}
			default:
				t.Fatal("ACP did not receive session/set_mode")
			}
		})
	}
}

func TestCursorACPLaunchApprovalFlags(t *testing.T) {
	for _, test := range []struct {
		approval string
		want     []string
	}{
		{"default", []string{"--trust", "acp"}},
		{"auto", []string{"--trust", "--auto-review", "acp"}},
		{"bypass-permissions", []string{"--trust", "--force", "acp"}},
	} {
		t.Run(test.approval, func(t *testing.T) {
			path, args, _, err := acpLaunch(worker.Turn{Harness: "cursor", ApprovalMode: test.approval}, Command{Path: "cursor-agent"})
			if err != nil {
				t.Fatal(err)
			}
			if path != "cursor-agent" || !reflect.DeepEqual(args, test.want) {
				t.Fatalf("Cursor launch = %q %v, want cursor-agent %v", path, args, test.want)
			}
		})
	}
}

func (c *approvalFlowControl) CreateChatApproval(_ context.Context, request worker.ChatApproval) error {
	c.created <- request
	return nil
}

func (c *approvalFlowControl) ChatApprovalDecision(ctx context.Context, _ string, _ int, _ string) (string, error) {
	if c.waitForCancel {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return c.decision, nil
}

func TestACPRequestPermissionPolicies(t *testing.T) {
	options := []acp.PermissionOption{
		{OptionId: "always", Name: "Allow always", Kind: acp.PermissionOptionKindAllowAlways},
		{OptionId: "once", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
		{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
	}
	for _, test := range []struct {
		name, harness, mode, approval, decision, selected string
		kind, cancel                                      bool
		prompted                                          bool
	}{
		{name: "read only", harness: "claude-code", mode: "read-only", approval: "default", cancel: true},
		{name: "ask user", harness: "claude-code", mode: "standard", approval: "default", decision: "reject", selected: "reject", prompted: true},
		{name: "accept edits prompts for execution", harness: "cursor", mode: "standard", approval: "accept-edits", decision: "once", selected: "once", prompted: true},
		{name: "accept edits allows file changes", harness: "cursor", mode: "standard", approval: "accept-edits", kind: true, selected: "once"},
		{name: "Claude bypass", harness: "claude-code", mode: "trusted", approval: "bypass-permissions", selected: "always"},
		{name: "Cursor bypass", harness: "cursor", mode: "trusted", approval: "bypass-permissions", selected: "always"},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := &approvalFlowControl{created: make(chan worker.ChatApproval, 1), decision: test.decision}
			client := &cloudACPClient{control: control, turn: worker.Turn{ID: "turn-1", Attempt: 1, Harness: test.harness, Mode: test.mode, ApprovalMode: test.approval}}
			kind := acp.ToolKindExecute
			if test.kind {
				kind = acp.ToolKindEdit
			}
			response, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
				ToolCall: acp.ToolCallUpdate{Kind: &kind}, Options: options,
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.cancel {
				if response.Outcome.Cancelled == nil {
					t.Fatal("read-only permission was not cancelled")
				}
			} else if response.Outcome.Selected == nil || string(response.Outcome.Selected.OptionId) != test.selected {
				t.Fatalf("permission outcome = %+v, want %q", response.Outcome, test.selected)
			}
			select {
			case request := <-control.created:
				if !test.prompted || request.TurnID != "turn-1" || request.Attempt != 1 {
					t.Fatalf("unexpected approval request: %+v", request)
				}
			default:
				if test.prompted {
					t.Fatal("approval was not sent to the user")
				}
			}
		})
	}
}

func TestACPRequestPermissionCancellationRepliesCancelled(t *testing.T) {
	control := &approvalFlowControl{created: make(chan worker.ChatApproval, 1), waitForCancel: true}
	client := &cloudACPClient{control: control, turn: worker.Turn{ID: "turn-1", Attempt: 1, Harness: "claude-code", Mode: "standard"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		response acp.RequestPermissionResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := client.RequestPermission(ctx, acp.RequestPermissionRequest{
			Options: []acp.PermissionOption{{OptionId: "once", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce}},
		})
		done <- result{response, err}
	}()
	select {
	case <-control.created:
	case <-time.After(time.Second):
		t.Fatal("approval request was not created")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != nil || got.response.Outcome.Cancelled == nil {
			t.Fatalf("cancelled approval = %+v, %v; want ACP cancelled outcome", got.response.Outcome, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled approval did not return")
	}
}

func TestACPRequestPermissionCancelledBeforeBypass(t *testing.T) {
	control := &approvalFlowControl{created: make(chan worker.ChatApproval, 1)}
	client := &cloudACPClient{control: control, turn: worker.Turn{Harness: "claude-code", Mode: "trusted", ApprovalMode: "bypass-permissions"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err := client.RequestPermission(ctx, acp.RequestPermissionRequest{
		Options: []acp.PermissionOption{{OptionId: "always", Kind: acp.PermissionOptionKindAllowAlways}},
	})
	if err != nil || response.Outcome.Cancelled == nil {
		t.Fatalf("cancelled bypass request = %+v, %v; want ACP cancelled outcome", response.Outcome, err)
	}
}

func TestACPSteeringRequiresProviderAdvertisement(t *testing.T) {
	if acpSteeringSupported(nil) || acpSteeringSupported(map[string]any{"steering": map[string]any{"supported": false}}) {
		t.Fatal("advertisement was absent")
	}
	if !acpSteeringSupported(map[string]any{"steering": map[string]any{"supported": true}}) {
		t.Fatal("advertised steering was hidden")
	}
}

func TestACPSettingsUseAdvertisedChoices(t *testing.T) {
	choices := acp.SessionConfigSelectOptionsUngrouped{{Value: "sonnet"}, {Value: "opus"}}
	options := []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "model", Options: acp.SessionConfigSelectOptions{Ungrouped: &choices},
	}}}
	if !acpOptionOffered(options, "model", "opus") || acpOptionOffered(options, "model", "unlisted") {
		t.Fatal("ACP model choices were not validated against the provider catalog")
	}
	if acpModeOffered(options, nil, "auto") {
		t.Fatal("unadvertised ACP approval mode was accepted")
	}
	modes := &acp.SessionModeState{AvailableModes: []acp.SessionMode{{Id: "default"}, {Id: "auto"}}}
	if !acpModeOffered(options, modes, "auto") {
		t.Fatal("advertised ACP approval mode was hidden")
	}
}

func TestCodexNotificationsProjectStructuredActivities(t *testing.T) {
	frames := []struct {
		method string
		params string
		kind   string
		status string
	}{
		{"item/started", `{"threadId":"thread-1","item":{"id":"cmd-1","type":"commandExecution","command":"go test ./..."}}`, "command", "running"},
		{"item/completed", `{"threadId":"thread-1","item":{"id":"cmd-1","type":"commandExecution","command":"go test ./...","aggregatedOutput":"ok","exitCode":0}}`, "command", "completed"},
		{"item/completed", `{"threadId":"thread-1","item":{"id":"edit-1","type":"fileChange","changes":[{"path":"main.go","kind":{"type":"update"}}]}}`, "file_change", "completed"},
	}
	for _, test := range frames {
		outputs := projectCodexNotification(codexFrame{Method: test.method, Params: json.RawMessage(test.params)}, "thread-1")
		if len(outputs) != 1 || outputs[0].Activity == nil || outputs[0].Activity.Kind != test.kind || outputs[0].Activity.Status != test.status {
			t.Fatalf("%s: %#v", test.method, outputs)
		}
	}
}

func TestCodexProjectsToolCallsAndFilePatches(t *testing.T) {
	tool := projectCodexNotification(codexFrame{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread-1","item":{"id":"mcp-1","type":"mcpToolCall","server":"github","tool":"search","arguments":{"query":"bug"},"result":{"count":1},"success":true}}`)}, "thread-1")
	if len(tool) != 1 || tool[0].Activity == nil || tool[0].Activity.Kind != "mcp_tool" || tool[0].Activity.Detail["server"] != "github" || tool[0].Activity.Detail["toolName"] != "search" {
		t.Fatalf("MCP tool projection = %#v", tool)
	}
	search := projectCodexNotification(codexFrame{Method: "item/started", Params: json.RawMessage(`{"threadId":"thread-1","item":{"id":"web-1","type":"webSearch","query":"release notes"}}`)}, "thread-1")
	if len(search) != 1 || search[0].Activity.Kind != "command" || search[0].Activity.Summary != "Searched the web for release notes" {
		t.Fatalf("web search projection = %#v", search)
	}
	edit := projectCodexNotification(codexFrame{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread-1","item":{"id":"edit-1","type":"fileChange","changes":[{"path":"old.go","kind":{"type":"update","move_path":"new.go"},"diff":"@@ -1 +1 @@\n-old\n+new\n"}]}}`)}, "thread-1")
	if len(edit) != 1 || edit[0].Activity == nil || edit[0].Activity.Kind != "file_change" {
		t.Fatalf("file change projection = %#v", edit)
	}
	files, ok := edit[0].Activity.Detail["files"].([]map[string]any)
	if !ok || len(files) != 1 || files[0]["status"] != "renamed" || files[0]["oldPath"] != "old.go" || files[0]["path"] != "new.go" || files[0]["additions"] != 1 || files[0]["deletions"] != 1 || files[0]["patch"] == "" {
		t.Fatalf("file patch = %#v", edit[0].Activity.Detail["files"])
	}
}
