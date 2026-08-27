package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeAgent struct {
	conn *acpsdk.AgentSideConnection

	mu                  sync.Mutex
	initParams          acpsdk.InitializeRequest
	capabilities        *acpsdk.AgentCapabilities
	initErr             error
	newParams           acpsdk.NewSessionRequest
	loadParams          acpsdk.LoadSessionRequest
	resumeParams        acpsdk.ResumeSessionRequest
	loadUpdates         []acpsdk.SessionUpdate
	loadCalls           int
	resumeCalls         int
	promptParams        acpsdk.PromptRequest
	promptNoPermission  bool
	elicitation         *acpsdk.UnstableCreateElicitationRequest
	elicitationResponse acpsdk.UnstableCreateElicitationResponse
	promptErr           error
	promptBlock         bool
	promptStarted       chan struct{}
	cancelErr           error
	cancelCalls         int
	mode                string
	modeNotFound        bool // SetSessionMode returns -32601
	configNotFound      bool // SetSessionConfigOption returns -32601
	configErr           error
	newSessionUpdates   []acpsdk.SessionUpdate
	options             map[string]string
	newConfig           []acpsdk.SessionConfigOption
	setConfig           []acpsdk.SessionConfigOption
	setCalls            int
	steering            bool
	steerText           string
	steerPrompt         []acpsdk.ContentBlock
	steerMeta           map[string]any
	steerOut            string
}

type legacyKimiAgent struct {
	mu          sync.Mutex
	model       string
	modelCalls  int
	mode        string
	modeCalls   int
	configCalls int
}

func fakeLegacyKimiSpawn(agent *legacyKimiAgent) spawnFunc {
	return func(Launch, string) (*process, error) {
		clientToAgentR, clientToAgentW := io.Pipe()
		agentToClientR, agentToClientW := io.Pipe()
		go serveLegacyKimi(agent, clientToAgentR, agentToClientW)
		var once sync.Once
		return &process{
			stdin: clientToAgentW, stdout: agentToClientR,
			stop: func() error {
				once.Do(func() {
					_ = clientToAgentW.Close()
					_ = clientToAgentR.Close()
					_ = agentToClientW.Close()
					_ = agentToClientR.Close()
				})
				return nil
			},
		}, nil
	}
}

func serveLegacyKimi(agent *legacyKimiAgent, in io.Reader, out io.Writer) {
	decoder := json.NewDecoder(in)
	encoder := json.NewEncoder(out)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&request); err != nil {
			return
		}
		result := any(map[string]any{})
		var responseError any
		switch request.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": acpsdk.ProtocolVersionNumber,
				"agentCapabilities": map[string]any{
					"sessionCapabilities": map[string]any{"resume": map[string]any{}},
				},
			}
		case "session/new":
			result = map[string]any{
				"sessionId": "kimi-session-1",
				"models": map[string]any{
					"currentModelId": "kimi-code/kimi-for-coding",
					"availableModels": []map[string]any{{
						"modelId": "kimi-code/kimi-for-coding",
						"name":    "Kimi for Coding", "description": "Kimi coding model",
					}},
				},
				"modes": map[string]any{
					"currentModeId": "default",
					"availableModes": []map[string]any{{
						"id": "default", "name": "Default", "description": "The default mode.",
					}},
				},
			}
		case "session/set_model":
			var params struct {
				ModelID string `json:"modelId"`
			}
			_ = json.Unmarshal(request.Params, &params)
			agent.mu.Lock()
			agent.model = params.ModelID
			agent.modelCalls++
			agent.mu.Unlock()
		case "session/set_mode":
			var params struct {
				ModeID string `json:"modeId"`
			}
			_ = json.Unmarshal(request.Params, &params)
			agent.mu.Lock()
			agent.mode = params.ModeID
			agent.modeCalls++
			agent.mu.Unlock()
		case "session/set_config_option":
			agent.mu.Lock()
			agent.configCalls++
			agent.mu.Unlock()
			responseError = map[string]any{"code": -32601, "message": "Method not found"}
		default:
			responseError = map[string]any{"code": -32601, "message": "Method not found"}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		if responseError != nil {
			response["error"] = responseError
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return
		}
	}
}

var _ acpsdk.Agent = (*fakeAgent)(nil)

func (a *fakeAgent) Authenticate(context.Context, acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, nil
}
func (a *fakeAgent) Initialize(_ context.Context, params acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	a.mu.Lock()
	a.initParams = params
	initErr := a.initErr
	caps := a.capabilities
	a.mu.Unlock()
	if initErr != nil {
		return acpsdk.InitializeResponse{}, initErr
	}
	meta := map[string]any(nil)
	if a.steering {
		meta = map[string]any{"steering": map[string]any{"supported": true}}
	}
	defaultCaps := acpsdk.AgentCapabilities{
		SessionCapabilities: acpsdk.SessionCapabilities{Resume: &acpsdk.SessionResumeCapabilities{}},
	}
	if caps != nil {
		defaultCaps = *caps
	}
	return acpsdk.InitializeResponse{
		ProtocolVersion:   acpsdk.ProtocolVersionNumber,
		Meta:              meta,
		AgentCapabilities: defaultCaps,
	}, nil
}

func (a *fakeAgent) HandleExtensionMethod(_ context.Context, method string, raw json.RawMessage) (any, error) {
	if method != steeringMethod {
		return nil, acpsdk.NewMethodNotFound(method)
	}
	var params struct {
		Prompt []acpsdk.ContentBlock `json:"prompt"`
		Meta   map[string]any        `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	text := ""
	if len(params.Prompt) > 0 && params.Prompt[0].Text != nil {
		text = params.Prompt[0].Text.Text
	}
	a.mu.Lock()
	a.steerText = text
	a.steerPrompt = append([]acpsdk.ContentBlock(nil), params.Prompt...)
	a.steerMeta = params.Meta
	outcome := a.steerOut
	a.mu.Unlock()
	if outcome == "" {
		outcome = "injected"
	}
	return steeringResponse{Outcome: outcome}, nil
}
func (a *fakeAgent) Logout(context.Context, acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, nil
}
func (a *fakeAgent) Cancel(context.Context, acpsdk.CancelNotification) error {
	a.mu.Lock()
	a.cancelCalls++
	err := a.cancelErr
	a.mu.Unlock()
	return err
}
func (a *fakeAgent) CloseSession(context.Context, acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	return acpsdk.CloseSessionResponse{}, nil
}
func (a *fakeAgent) ListSessions(context.Context, acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	return acpsdk.ListSessionsResponse{}, nil
}
func (a *fakeAgent) NewSession(ctx context.Context, params acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	a.mu.Lock()
	a.newParams = params
	updates := append([]acpsdk.SessionUpdate(nil), a.newSessionUpdates...)
	a.mu.Unlock()
	for _, update := range updates {
		if err := a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{SessionId: "claude-session-1", Update: update}); err != nil {
			return acpsdk.NewSessionResponse{}, err
		}
	}
	return acpsdk.NewSessionResponse{SessionId: "claude-session-1", ConfigOptions: a.newConfig}, nil
}
func (a *fakeAgent) ResumeSession(_ context.Context, params acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	a.mu.Lock()
	a.resumeParams = params
	a.resumeCalls++
	a.mu.Unlock()
	return acpsdk.ResumeSessionResponse{}, nil
}
func (a *fakeAgent) LoadSession(ctx context.Context, params acpsdk.LoadSessionRequest) (acpsdk.LoadSessionResponse, error) {
	a.mu.Lock()
	a.loadParams = params
	a.loadCalls++
	updates := append([]acpsdk.SessionUpdate(nil), a.loadUpdates...)
	a.mu.Unlock()
	for _, update := range updates {
		if err := a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{SessionId: params.SessionId, Update: update}); err != nil {
			return acpsdk.LoadSessionResponse{}, err
		}
	}
	return acpsdk.LoadSessionResponse{}, nil
}
func (a *fakeAgent) SetSessionConfigOption(_ context.Context, params acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	a.mu.Lock()
	a.setCalls++
	if a.configErr != nil {
		err := a.configErr
		a.mu.Unlock()
		return acpsdk.SetSessionConfigOptionResponse{}, err
	}
	if a.configNotFound {
		a.mu.Unlock()
		return acpsdk.SetSessionConfigOptionResponse{}, acpsdk.NewMethodNotFound("session/set_config_option")
	}
	if params.ValueId != nil {
		if a.options == nil {
			a.options = make(map[string]string)
		}
		a.options[string(params.ValueId.ConfigId)] = string(params.ValueId.Value)
	}
	if params.Boolean != nil {
		if a.options == nil {
			a.options = make(map[string]string)
		}
		a.options[string(params.Boolean.ConfigId)] = fmt.Sprintf("%t", params.Boolean.Value)
	}
	response := append([]acpsdk.SessionConfigOption(nil), a.setConfig...)
	a.mu.Unlock()
	return acpsdk.SetSessionConfigOptionResponse{ConfigOptions: response}, nil
}
func (a *fakeAgent) SetSessionMode(_ context.Context, params acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	a.mu.Lock()
	if a.modeNotFound {
		a.mu.Unlock()
		return acpsdk.SetSessionModeResponse{}, acpsdk.NewMethodNotFound("session/set_mode")
	}
	a.mode = string(params.ModeId)
	a.mu.Unlock()
	return acpsdk.SetSessionModeResponse{}, nil
}
func (a *fakeAgent) Prompt(ctx context.Context, params acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	a.mu.Lock()
	a.promptParams = params
	promptNoPermission := a.promptNoPermission
	elicitation := a.elicitation
	promptErr := a.promptErr
	promptBlock := a.promptBlock
	promptStarted := a.promptStarted
	a.mu.Unlock()
	if promptErr != nil {
		return acpsdk.PromptResponse{}, promptErr
	}
	if promptBlock {
		if promptStarted != nil {
			select {
			case promptStarted <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return acpsdk.PromptResponse{}, ctx.Err()
	}
	if elicitation != nil {
		response, err := a.conn.UnstableCreateElicitation(ctx, *elicitation)
		a.mu.Lock()
		a.elicitationResponse = response
		a.mu.Unlock()
		if err != nil {
			return acpsdk.PromptResponse{}, err
		}
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
	}
	if promptNoPermission {
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
	}
	_ = a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
		SessionId: params.SessionId,
		Update:    acpsdk.UpdateAgentMessageText("working"),
	})
	permission, err := a.conn.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
		SessionId: params.SessionId,
		ToolCall: acpsdk.ToolCallUpdate{
			ToolCallId: "tool-1", Title: acpsdk.Ptr("Edit file"), Kind: acpsdk.Ptr(acpsdk.ToolKindEdit),
		},
		Options: []acpsdk.PermissionOption{
			{OptionId: "allow", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce},
			{OptionId: "reject", Name: "Reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
		},
	})
	if err != nil {
		return acpsdk.PromptResponse{}, err
	}
	if permission.Outcome.Selected != nil {
		_ = a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
			SessionId: params.SessionId,
			Update:    acpsdk.UpdateAgentMessageText(" done"),
		})
	}
	return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
}

func TestACPDriverDefersPromptUntilDurableTurnBinding(t *testing.T) {
	agent := &fakeAgent{}
	driver := New(Config{
		Harness: domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true, ports.ChatCapabilityApprovals: true,
			ports.ChatCapabilityInterrupt: true, ports.ChatCapabilityResume: true,
		},
		Probe: func(context.Context) error { return nil },
		Launch: func(context.Context, LaunchConfig) (Launch, error) {
			return Launch{Command: "fake"}, nil
		},
		SessionMeta: func(cfg LaunchConfig) map[string]any {
			return map[string]any{"systemPrompt": map[string]any{"append": cfg.SystemPrompt}}
		},
		SessionMode: func(permission ports.PermissionMode) string {
			if permission == ports.PermissionModeAcceptEdits {
				return "acceptEdits"
			}
			return ""
		},
		SessionOptions: func(settings ports.ChatTurnSettings) []SessionOption {
			if settings.Model == "" {
				return nil
			}
			return []SessionOption{{ID: "model", Value: settings.Model}}
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conversation, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath: t.TempDir(), SystemPrompt: "AO instructions",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.Close()
	if got := conversation.ProviderConversationID(); got != "claude-session-1" {
		t.Fatalf("provider conversation id = %q", got)
	}
	agent.mu.Lock()
	meta := agent.newParams.Meta
	agent.mu.Unlock()
	if meta["systemPrompt"] == nil {
		t.Fatal("session/new did not receive provider metadata")
	}

	// Consume controller.ready from session setup.
	_ = nextEvent(t, conversation.Events())
	ref, err := conversation.SendTurn(context.Background(), ports.ChatUserMessage{
		Text: "change it", Settings: ports.ChatTurnSettings{
			Model: "test-model", Approval: ports.PermissionModeAcceptEdits,
		},
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	select {
	case event := <-conversation.Events():
		t.Fatalf("event %q arrived before StartDeferredTurn; it could race durable binding", event.Kind)
	case <-time.After(30 * time.Millisecond):
	}
	agent.mu.Lock()
	mode, model := agent.mode, agent.options["model"]
	agent.mu.Unlock()
	if mode != "acceptEdits" || model != "test-model" {
		t.Fatalf("ACP settings = mode %q, model %q", mode, model)
	}
	deferred := conversation.(ports.ChatDeferredTurnStarter)
	if err := deferred.StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}

	var approvalID string
	for approvalID == "" {
		event := nextEvent(t, conversation.Events())
		if event.ProviderTurnID != "" && event.ProviderTurnID != ref.ProviderTurnID {
			t.Fatalf("event turn id = %q, want %q", event.ProviderTurnID, ref.ProviderTurnID)
		}
		if event.Kind == ports.ChatEventApprovalRequested {
			approvalID = event.RequestID
			if len(event.Decisions) != 2 || event.Decisions[0].ID != "allow" ||
				event.Decisions[0].Kind != ports.ChatDecisionAllowOnce ||
				event.Decisions[1].Kind != ports.ChatDecisionRejectOnce {
				t.Fatalf("approval decisions = %#v", event.Decisions)
			}
			var detail struct {
				SubjectKind string          `json:"subjectKind"`
				ToolKind    acpsdk.ToolKind `json:"toolKind"`
			}
			if err := json.Unmarshal(event.Detail, &detail); err != nil {
				t.Fatalf("approval detail: %v (%s)", err, event.Detail)
			}
			if detail.SubjectKind != string(domain.ActivityKindFileChange) || detail.ToolKind != acpsdk.ToolKindEdit {
				t.Fatalf("approval detail = %+v, want an ACP file edit", detail)
			}
		}
	}
	if err := conversation.ResolveRequest(context.Background(), approvalID, ports.ChatDecision{ID: "allow"}); err != nil {
		t.Fatalf("ResolveRequest: %v", err)
	}

	var completed bool
	for !completed {
		event := nextEvent(t, conversation.Events())
		if event.Kind == ports.ChatEventTurnCompleted {
			completed = true
			if event.TurnState != domain.TurnStateCompleted {
				t.Fatalf("turn state = %q", event.TurnState)
			}
		}
	}
}

func TestACPDriverValidatesHandshakeIdentityBeforeOpeningSession(t *testing.T) {
	agent := &fakeAgent{}
	validated := false
	driver := New(Config{
		Harness: domain.HarnessPi,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityApprovals: true,
			ports.ChatCapabilityInterrupt: true,
			ports.ChatCapabilityResume:    true,
		},
		Probe:  func(context.Context) error { return nil },
		Launch: func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		ValidateInitialize: func(acpsdk.InitializeResponse) error {
			validated = true
			return errors.New("unsupported adapter version")
		},
	}, nil)
	driver.spawn = fakeSpawn(agent)

	_, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if !validated {
		t.Fatal("initialize response was not validated")
	}
	if !errors.Is(err, ports.ErrChatDriverIncompatible) || !strings.Contains(err.Error(), "unsupported adapter version") {
		t.Fatalf("Start error = %v", err)
	}
	agent.mu.Lock()
	opened := agent.newParams.Cwd != ""
	agent.mu.Unlock()
	if opened {
		t.Fatal("session/new ran before the handshake identity was admitted")
	}
}

func TestACPInterruptCancelsTheLocalPromptAfterNotifyingTheAgent(t *testing.T) {
	agent := &fakeAgent{promptBlock: true, promptStarted: make(chan struct{}, 1)}
	driver := New(Config{
		Harness: domain.HarnessOpenCode,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityInterrupt: true,
		},
		Probe: func(context.Context) error { return nil },
		Launch: func(context.Context, LaunchConfig) (Launch, error) {
			return Launch{Command: "fake"}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conversation, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.Close()
	_ = nextEvent(t, conversation.Events()) // controller.ready
	ref, err := conversation.SendTurn(context.Background(), ports.ChatUserMessage{Text: "wait"})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conversation.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}
	select {
	case <-agent.promptStarted:
	case <-time.After(time.Second):
		t.Fatal("Prompt did not start")
	}
	if err := conversation.Interrupt(context.Background(), ref.ProviderTurnID); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	for {
		event := nextEvent(t, conversation.Events())
		if event.Kind == ports.ChatEventTurnCompleted {
			if event.TurnState != domain.TurnStateInterrupted {
				t.Fatalf("turn state = %q, want interrupted", event.TurnState)
			}
			break
		}
	}
	// The SDK may emit a second idempotent session/cancel while unwinding the
	// locally cancelled Prompt request. What matters is that the explicit
	// notification was sent and the local request settled. Notification handling
	// is asynchronous, so observe it rather than assuming it ran before the turn
	// completion event.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		agent.mu.Lock()
		cancelCalls := agent.cancelCalls
		agent.mu.Unlock()
		if cancelCalls >= 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("ACP cancel notification was not handled")
}

func TestACPDriverNegotiatesRichClientCapabilitiesAndNativePromptContent(t *testing.T) {
	agent := &fakeAgent{
		promptNoPermission: true,
		capabilities: &acpsdk.AgentCapabilities{
			PromptCapabilities: acpsdk.PromptCapabilities{Image: true, EmbeddedContext: true},
			McpCapabilities:    acpsdk.McpCapabilities{Http: true},
			SessionCapabilities: acpsdk.SessionCapabilities{
				Resume:                &acpsdk.SessionResumeCapabilities{},
				AdditionalDirectories: &acpsdk.SessionAdditionalDirectoriesCapabilities{},
			},
		},
	}
	driver := New(Config{
		Harness: domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityImages:    true,
		},
		Probe: func(context.Context) error { return nil },
		Launch: func(context.Context, LaunchConfig) (Launch, error) {
			return Launch{Command: "fake"}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	root := t.TempDir()
	extra := t.TempDir()
	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath:         root,
		AdditionalDirectories: []string{extra},
		MCPServers: []ports.ChatMCPServerConfig{
			{Name: "local", Type: "stdio", Command: "mcp-local", Args: []string{"serve"}, Env: map[string]string{"TOKEN": "secret"}},
			{Name: "remote", Type: "http", URL: "https://mcp.example.test", Headers: map[string]string{"Authorization": "Bearer secret"}},
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conv.Close()
	_ = nextEvent(t, conv.Events())

	agent.mu.Lock()
	initParams, newParams := agent.initParams, agent.newParams
	agent.mu.Unlock()
	if initParams.ClientCapabilities.Elicitation == nil ||
		initParams.ClientCapabilities.Elicitation.Form == nil ||
		initParams.ClientCapabilities.Elicitation.Url == nil {
		t.Fatalf("elicitation capabilities = %#v", initParams.ClientCapabilities.Elicitation)
	}
	if initParams.ClientCapabilities.Meta["subagent-transcript"] != true ||
		initParams.ClientCapabilities.Meta["terminal_output"] != true {
		t.Fatalf("client extension capabilities = %#v", initParams.ClientCapabilities.Meta)
	}
	if len(newParams.AdditionalDirectories) != 1 || newParams.AdditionalDirectories[0] != extra {
		t.Fatalf("additional directories = %#v", newParams.AdditionalDirectories)
	}
	if len(newParams.McpServers) != 2 || newParams.McpServers[0].Stdio == nil || newParams.McpServers[1].Http == nil {
		t.Fatalf("MCP servers = %#v", newParams.McpServers)
	}
	if !conv.Capabilities().Has(ports.ChatCapabilityImages) ||
		!conv.Capabilities().Has(ports.ChatCapabilityEmbeddedContext) ||
		!conv.Capabilities().Has(ports.ChatCapabilityResourceLinks) ||
		!conv.Capabilities().Has(ports.ChatCapabilityElicitation) {
		t.Fatalf("conversation capabilities = %#v", conv.Capabilities())
	}

	ref, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{
		Text: "inspect these", ClientMessageID: "ao-client-message-1",
		Content: []ports.ChatContent{
			{Type: "image", Data: "aW1hZ2U=", MIMEType: "image/png"},
			{Type: "resource_link", URI: "file:///repo/README.md", Name: "README.md"},
			{Type: "resource", URI: "file:///repo/notes.txt", Name: "notes.txt", MIMEType: "text/plain", Text: "notes"},
			{Type: "resource", URI: ports.ChatInternalReplayResourceURI, Name: "replay", MIMEType: "application/json", Text: `{}`, Internal: true},
		},
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conv.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}
	for {
		if event := nextEvent(t, conv.Events()); event.Kind == ports.ChatEventTurnCompleted {
			break
		}
	}
	agent.mu.Lock()
	prompt := agent.promptParams.Prompt
	promptMessageID := agent.promptParams.MessageId
	agent.mu.Unlock()
	if len(prompt) != 5 || prompt[1].Image == nil || prompt[2].ResourceLink == nil ||
		prompt[3].Resource == nil || prompt[4].Resource == nil {
		t.Fatalf("native prompt = %#v", prompt)
	}
	internalResource := prompt[4].Resource.Resource.TextResourceContents
	if internalResource == nil || internalResource.Meta[aoInternalReplayMetaKey] != true {
		t.Fatalf("internal replay ACP metadata = %#v", internalResource)
	}
	if promptMessageID == nil || *promptMessageID != "ao-client-message-1" {
		t.Fatalf("ACP prompt message id = %v, want AO's durable client id", promptMessageID)
	}
}

func TestACPDriverReappliesLaunchContextWhenResuming(t *testing.T) {
	agent := &fakeAgent{}
	var got LaunchConfig
	driver := New(Config{
		Harness:      domain.HarnessOpenCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch: func(_ context.Context, cfg LaunchConfig) (Launch, error) {
			got = cfg
			return Launch{Command: "fake"}, nil
		},
		SessionMeta: func(cfg LaunchConfig) map[string]any {
			return map[string]any{"systemPrompt": map[string]any{"append": cfg.SystemPrompt}}
		},
		SessionOptions: func(settings ports.ChatTurnSettings) []SessionOption {
			if settings.Model == "" {
				return nil
			}
			return []SessionOption{{ID: "model", Value: settings.Model}}
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	workspace := t.TempDir()
	conv, err := driver.Resume(context.Background(), ports.ChatResumeConfig{
		SessionID:              "worker-1",
		ProviderConversationID: "provider-session-1",
		WorkspacePath:          workspace,
		Env:                    map[string]string{"KEEP": "yes"},
		Model:                  "selected-resume-model",
		SystemPrompt:           "Recomputed AO instructions",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conv.Close()
	if got.SessionID != "worker-1" || got.WorkspacePath != workspace || got.Model != "selected-resume-model" ||
		got.Env["KEEP"] != "yes" || got.SystemPrompt != "Recomputed AO instructions" {
		t.Fatalf("launch config = %#v", got)
	}
	agent.mu.Lock()
	resumeCalls, loadCalls := agent.resumeCalls, agent.loadCalls
	resumeMeta := agent.resumeParams.Meta
	resumeModel := agent.options["model"]
	agent.mu.Unlock()
	if resumeCalls != 1 || loadCalls != 0 {
		t.Fatalf("resume calls = %d, load calls = %d; want resume fallback", resumeCalls, loadCalls)
	}
	prompt, ok := resumeMeta["systemPrompt"].(map[string]any)
	if !ok || prompt["append"] != "Recomputed AO instructions" {
		t.Fatalf("session/resume metadata = %#v, want recomputed system prompt", resumeMeta)
	}
	if resumeModel != "selected-resume-model" {
		t.Fatalf("resumed ACP model = %q, want selected-resume-model", resumeModel)
	}
	if conv.Capabilities().Has(ports.ChatCapabilityHistory) {
		t.Fatal("resume-only ACP conversation advertised replayable history")
	}
	if _, err := conv.(ports.ChatHistoryReader).ReadHistory(context.Background()); !errors.Is(err, ports.ErrChatHistoryUnavailable) {
		t.Fatalf("ReadHistory error = %v, want ErrChatHistoryUnavailable after session/resume", err)
	}
}

func TestACPDriverClosesReplayTurnsAsRecoveredWithoutBlockingResume(t *testing.T) {
	userOneID := "11111111-1111-4111-8111-111111111111"
	answerOneID := "22222222-2222-4222-8222-222222222222"
	userTwoID := "33333333-3333-4333-8333-333333333333"
	answerTwoID := "44444444-4444-4444-8444-444444444444"
	userOne := acpsdk.UpdateUserMessageText("Inspect the repository")
	userOne.UserMessageChunk.MessageId = &userOneID
	replayBlock := acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
		TextResourceContents: &acpsdk.TextResourceContents{
			Uri: ports.ChatInternalReplayResourceURI, Text: `{"kind":"approximate_conversation_context"}`,
			Meta: map[string]any{aoInternalReplayMetaKey: true},
		},
	})
	replaySeed := acpsdk.UpdateUserMessage(replayBlock)
	replaySeed.UserMessageChunk.MessageId = &userOneID
	answerOneA := acpsdk.UpdateAgentMessageText("The repository ")
	answerOneA.AgentMessageChunk.MessageId = &answerOneID
	answerOneB := acpsdk.UpdateAgentMessageText("is ready.")
	answerOneB.AgentMessageChunk.MessageId = &answerOneID
	userTwo := acpsdk.UpdateUserMessageText("Run the tests")
	userTwo.UserMessageChunk.MessageId = &userTwoID
	answerTwo := acpsdk.UpdateAgentMessageText("All tests pass.")
	answerTwo.AgentMessageChunk.MessageId = &answerTwoID
	pendingTool := acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
		SessionUpdate: "tool_call", ToolCallId: "history-tool", Title: "Run tests",
		Kind: acpsdk.ToolKindExecute, Status: acpsdk.ToolCallStatusInProgress,
	}}

	agent := &fakeAgent{
		capabilities: &acpsdk.AgentCapabilities{
			LoadSession: true,
		},
		loadUpdates: []acpsdk.SessionUpdate{
			userOne, replaySeed, answerOneA, answerOneB, userTwo, answerTwo, pendingTool,
		},
	}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		SessionMeta: func(cfg LaunchConfig) map[string]any {
			return map[string]any{"systemPrompt": map[string]any{"append": cfg.SystemPrompt}}
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conv, err := driver.Resume(context.Background(), ports.ChatResumeConfig{
		ProviderConversationID: "provider-session-1",
		WorkspacePath:          t.TempDir(),
		SystemPrompt:           "AO load instructions",
		ProviderScopeID:        "history-scope",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conv.Close()
	if _, ok := conv.(ports.ChatHistoryRefresher); ok {
		t.Fatal("ACP session/load replay is a frozen snapshot, not refreshable history")
	}

	agent.mu.Lock()
	loadCalls, resumeCalls := agent.loadCalls, agent.resumeCalls
	loadedSession := string(agent.loadParams.SessionId)
	loadMeta := agent.loadParams.Meta
	agent.mu.Unlock()
	if loadCalls != 1 || resumeCalls != 0 || loadedSession != "provider-session-1" {
		t.Fatalf("load calls = %d, resume calls = %d, session = %q", loadCalls, resumeCalls, loadedSession)
	}
	prompt, ok := loadMeta["systemPrompt"].(map[string]any)
	if !ok || prompt["append"] != "AO load instructions" {
		t.Fatalf("session/load metadata = %#v, want recomputed system prompt", loadMeta)
	}

	history, err := conv.(ports.ChatHistoryReader).ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	var states []domain.TurnState
	var recoveredActivity bool
	for _, event := range history {
		if event.Kind == ports.ChatEventTurnCompleted {
			states = append(states, event.TurnState)
		}
		if event.ProviderItemID == (&conversation{providerScopeID: "history-scope"}).providerItemID("history-tool") &&
			event.Kind == ports.ChatEventActivityCompleted {
			recoveredActivity = event.ActivityStatus == domain.ActivityStatusRecovered
		}
	}
	if len(states) != 2 || states[0] != domain.TurnStateRecovered ||
		states[1] != domain.TurnStateRecovered {
		t.Fatalf("replayed turn states = %v, want [recovered recovered]", states)
	}
	if !recoveredActivity {
		t.Fatalf("history = %#v, want pending replay tool settled as recovered", history)
	}
	if history[0].ProviderTurnID != historyTurnID("history-scope", userOneID) ||
		history[1].ProviderItemID != (&conversation{providerScopeID: "history-scope"}).providerItemID(userOneID) ||
		history[1].ClientMessageID != (&conversation{providerScopeID: "history-scope"}).providerItemID(userOneID) ||
		history[2].ProviderItemID != (&conversation{providerScopeID: "history-scope"}).providerItemID(answerOneID) {
		t.Fatalf("history ids do not use the durable provider scope: %#v", history[:3])
	}
	if len(history[1].ProviderItemAliases) != 1 || history[1].ProviderItemAliases[0] != userOneID ||
		len(history[2].ProviderItemAliases) != 1 || history[2].ProviderItemAliases[0] != answerOneID {
		t.Fatalf("history does not preserve legacy raw item aliases: %#v", history[:3])
	}
	if !conv.Capabilities().Has(ports.ChatCapabilityHistory) {
		t.Fatal("session/load conversation did not advertise replayable history")
	}

	ready := nextEvent(t, conv.Events())
	if ready.Kind != ports.ChatEventControllerState || ready.ControllerState != ports.ChatControllerReady {
		t.Fatalf("first live event = %#v, want controller ready", ready)
	}
	select {
	case event := <-conv.Events():
		t.Fatalf("history leaked onto the live event stream: %#v", event)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestHistoricalUserContentSuppressesOnlyMarkedInternalReplayResources(t *testing.T) {
	resource := acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
		TextResourceContents: &acpsdk.TextResourceContents{
			Uri: ports.ChatInternalReplayResourceURI, Text: `{"kind":"approximate_conversation_context"}`,
		},
	})
	if got := historicalUserContent(resource); got != "[Embedded context]" {
		t.Fatalf("unmarked reserved resource = %q, want visible embedded context", got)
	}

	resource.Resource.Resource.TextResourceContents.Meta = map[string]any{aoInternalReplayMetaKey: true}
	if got := historicalUserContent(resource); got != "" {
		t.Fatalf("marked internal replay resource = %q, want hidden", got)
	}
}

func TestACPDriverClosesTrailingUserOnlyHistoryAsRecovered(t *testing.T) {
	userID := "55555555-5555-4555-8555-555555555555"
	user := acpsdk.UpdateUserMessageText("Work that has not produced a provider event yet")
	user.UserMessageChunk.MessageId = &userID
	agent := &fakeAgent{
		capabilities: &acpsdk.AgentCapabilities{
			LoadSession: true,
			SessionCapabilities: acpsdk.SessionCapabilities{
				Resume: &acpsdk.SessionResumeCapabilities{},
			},
		},
		loadUpdates: []acpsdk.SessionUpdate{user},
	}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conv, err := driver.Resume(context.Background(), ports.ChatResumeConfig{
		ProviderConversationID: "provider-session-1",
		WorkspacePath:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conv.Close()

	history, err := conv.(ports.ChatHistoryReader).ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(history) != 3 || history[2].Kind != ports.ChatEventTurnCompleted ||
		history[2].TurnState != domain.TurnStateRecovered {
		t.Fatalf("history = %#v, want trailing recovered turn", history)
	}
}

func TestConversationCapabilitiesTreatLoadSessionAsResume(t *testing.T) {
	configured := ports.ChatCapabilities{ports.ChatCapabilityResume: true}
	init := acpsdk.InitializeResponse{AgentCapabilities: acpsdk.AgentCapabilities{
		LoadSession: true,
	}}
	if !conversationCapabilities(configured, init)[ports.ChatCapabilityResume] {
		t.Fatal("loadSession-only ACP agent was reported as non-resumable")
	}
}

func TestACPDriverUsesProviderPermissionPolicyBeforeParking(t *testing.T) {
	agent := &fakeAgent{promptNoPermission: true}
	driver := New(Config{
		Harness:      domain.HarnessCursor,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityApprovals: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		PermissionPolicy: func(mode ports.PermissionMode, params acpsdk.RequestPermissionRequest) (acpsdk.PermissionOptionId, bool) {
			if mode != ports.PermissionModeBypassPermissions {
				return "", false
			}
			return params.Options[0].OptionId, true
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	opened, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath: t.TempDir(), Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer opened.Close()
	ready := nextEvent(t, opened.Events())
	if ready.Kind != ports.ChatEventControllerState {
		t.Fatalf("first event = %#v, want controller state", ready)
	}

	conv := opened.(*conversation)
	if err := conv.applyTurnSettings(context.Background(), ports.ChatTurnSettings{}); err != nil {
		t.Fatalf("apply empty settings: %v", err)
	}
	conv.mu.Lock()
	modeAfterEmpty := conv.permissionMode
	conv.mu.Unlock()
	if modeAfterEmpty != ports.PermissionModeBypassPermissions {
		t.Fatalf("permission mode after empty turn settings = %q, want launch mode %q",
			modeAfterEmpty, ports.PermissionModeBypassPermissions)
	}
	response, err := conv.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		ToolCall: acpsdk.ToolCallUpdate{ToolCallId: "edit-1", Kind: acpsdk.Ptr(acpsdk.ToolKindEdit)},
		Options: []acpsdk.PermissionOption{{
			OptionId: "allow-once", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce,
		}},
	})
	if err != nil {
		t.Fatalf("RequestPermission: %v", err)
	}
	if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow-once" {
		t.Fatalf("permission response = %#v", response)
	}
	select {
	case event := <-opened.Events():
		t.Fatalf("automatic policy emitted a parked approval: %#v", event)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestACPDriverKeepsPermissionPolicyWhenLaterTurnSettingFails(t *testing.T) {
	agent := &fakeAgent{}
	driver := New(Config{
		Harness: domain.HarnessCursor,
		Probe:   func(context.Context) error { return nil },
		Launch:  func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		SessionOptions: func(settings ports.ChatTurnSettings) []SessionOption {
			if settings.Model == "" {
				return nil
			}
			return []SessionOption{{ID: "model", Value: settings.Model}}
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	opened, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer opened.Close()
	_ = nextEvent(t, opened.Events())

	agent.mu.Lock()
	agent.configErr = errors.New("set model failed")
	agent.mu.Unlock()
	conv := opened.(*conversation)
	err = conv.applyTurnSettings(context.Background(), ports.ChatTurnSettings{
		Approval: ports.PermissionModeAcceptEdits,
		Model:    "cursor-model",
	})
	if err == nil {
		t.Fatal("applyTurnSettings succeeded, want config error")
	}
	conv.mu.Lock()
	mode := conv.permissionMode
	conv.mu.Unlock()
	if mode != ports.PermissionModeDefault {
		t.Fatalf("permission mode after rejected settings = %q, want %q", mode, ports.PermissionModeDefault)
	}
}

func TestACPDriverParksAndResolvesStructuredElicitation(t *testing.T) {
	request := acpsdk.NewUnstableCreateElicitationRequestForm(acpsdk.UnstableElicitationSchema{
		Type:       acpsdk.UnstableElicitationSchemaTypeObject,
		Properties: map[string]any{"choice": map[string]any{"type": "string"}},
		Required:   []string{"choice"},
	})
	request.Form.Message = "Which approach?"
	agent := &fakeAgent{elicitation: &request}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conv.Close()
	_ = nextEvent(t, conv.Events())
	ref, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "ask me"})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conv.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}

	var requestID string
	for requestID == "" {
		event := nextEvent(t, conv.Events())
		if event.Kind == ports.ChatEventInputRequested {
			requestID = event.RequestID
			if event.Input == nil || event.Input.Mode != "form" || event.Input.Message != "Which approach?" {
				t.Fatalf("input request = %#v", event.Input)
			}
		}
	}
	if err := conv.(ports.ChatInputResponder).ResolveInput(context.Background(), requestID,
		ports.ChatInputResponse{Action: "accept", Content: map[string]any{"choice": "native"}}); err != nil {
		t.Fatalf("ResolveInput: %v", err)
	}
	for {
		if event := nextEvent(t, conv.Events()); event.Kind == ports.ChatEventTurnCompleted {
			break
		}
	}
	agent.mu.Lock()
	response := agent.elicitationResponse
	agent.mu.Unlock()
	if response.Accept == nil || response.Accept.Content["choice"] != "native" {
		t.Fatalf("elicitation response = %#v", response)
	}
}

func TestValidateInputResponseRejectsValuesOutsideTheProviderSchema(t *testing.T) {
	request := ports.ChatInputRequest{Mode: "form", Schema: map[string]any{
		"required": []any{"choice", "fast"},
		"properties": map[string]any{
			"choice": map[string]any{"type": "string", "oneOf": []any{
				map[string]any{"const": "native"}, map[string]any{"const": "bridge"},
			}},
			"fast": map[string]any{"type": "boolean"},
		},
	}}
	for name, response := range map[string]ports.ChatInputResponse{
		"missing required": {Action: "accept", Content: map[string]any{"choice": "native"}},
		"unknown option":   {Action: "accept", Content: map[string]any{"choice": "other", "fast": true}},
		"wrong type":       {Action: "accept", Content: map[string]any{"choice": "native", "fast": "yes"}},
		"unknown field":    {Action: "accept", Content: map[string]any{"choice": "native", "fast": true, "secret": "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateInputResponse(request, response); !errors.Is(err, ports.ErrChatDecisionNotOffered) {
				t.Fatalf("error = %v, want ErrChatDecisionNotOffered", err)
			}
		})
	}
}

func TestACPDriverPreservesNestedToolAndTerminalMetadata(t *testing.T) {
	agent := &fakeAgent{}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)
	opened, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer opened.Close()
	_ = nextEvent(t, opened.Events())
	conv := opened.(*conversation)
	conv.mu.Lock()
	conv.activeTurn = "turn-1"
	conv.mu.Unlock()

	if err := agent.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(opened.ProviderConversationID()),
		Update: acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
			SessionUpdate: "tool_call", ToolCallId: "child-tool", Title: "Run tests",
			Kind: acpsdk.ToolKindExecute, Status: acpsdk.ToolCallStatusPending,
			Meta: map[string]any{
				"claudeCode":    map[string]any{"toolName": "Bash", "parentToolUseId": "agent-tool"},
				"terminal_info": map[string]any{"terminal_id": "child-tool"},
			},
		}},
	}); err != nil {
		t.Fatalf("tool start: %v", err)
	}
	started := nextEvent(t, opened.Events())
	if started.Kind != ports.ChatEventActivityStarted {
		t.Fatalf("started event = %#v", started)
	}

	status := acpsdk.ToolCallStatusCompleted
	if err := agent.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(opened.ProviderConversationID()),
		Update: acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
			SessionUpdate: "tool_call_update", ToolCallId: "child-tool", Status: &status,
			Meta: map[string]any{
				"terminal_output": map[string]any{"terminal_id": "child-tool", "data": "ok\n"},
				"terminal_exit":   map[string]any{"terminal_id": "child-tool", "exit_code": float64(0)},
			},
		}},
	}); err != nil {
		t.Fatalf("tool completion: %v", err)
	}
	output := nextEvent(t, opened.Events())
	completed := nextEvent(t, opened.Events())
	if output.Kind != ports.ChatEventCommandOutputDelta || output.Delta != "ok\n" {
		t.Fatalf("terminal output = %#v", output)
	}
	var detail map[string]any
	if err := json.Unmarshal(completed.Detail, &detail); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail["parentProviderItemId"] != "agent-tool" || detail["terminalId"] != "child-tool" || detail["output"] != "ok\n" {
		t.Fatalf("tool detail = %#v", detail)
	}
}

func TestACPDriverNamespacesOpaqueItemIDsByProviderScope(t *testing.T) {
	type observed struct {
		messageID string
		toolID    string
		parentID  string
	}
	open := func(t *testing.T, providerScopeID string) observed {
		t.Helper()
		agent := &fakeAgent{}
		driver := New(Config{
			Harness:      domain.HarnessClaudeCode,
			Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
			Probe:        func(context.Context) error { return nil },
			Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		driver.spawn = fakeSpawn(agent)
		opened, err := driver.Start(context.Background(), ports.ChatStartConfig{
			SessionID: domain.SessionID("session-1"), WorkspacePath: t.TempDir(),
			ProviderScopeID: providerScopeID,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = opened.Close() })
		_ = nextEvent(t, opened.Events())
		conv := opened.(*conversation)
		conv.mu.Lock()
		conv.activeTurn = "turn-1"
		conv.mu.Unlock()

		messageID := "reused-message"
		message := acpsdk.UpdateAgentMessageText("working")
		message.AgentMessageChunk.MessageId = &messageID
		if err := agent.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
			SessionId: acpsdk.SessionId(opened.ProviderConversationID()), Update: message,
		}); err != nil {
			t.Fatalf("message update: %v", err)
		}
		messageEvent := nextEvent(t, opened.Events())

		if err := agent.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
			SessionId: acpsdk.SessionId(opened.ProviderConversationID()),
			Update: acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
				SessionUpdate: "tool_call", ToolCallId: "reused-tool", Title: "Run tests",
				Kind: acpsdk.ToolKindExecute, Status: acpsdk.ToolCallStatusPending,
				Meta: map[string]any{"claudeCode": map[string]any{"parentToolUseId": "reused-parent"}},
			}},
		}); err != nil {
			t.Fatalf("tool update: %v", err)
		}
		toolEvent := nextEvent(t, opened.Events())
		var detail map[string]any
		if err := json.Unmarshal(toolEvent.Detail, &detail); err != nil {
			t.Fatalf("tool detail: %v", err)
		}
		parentID, _ := detail["parentProviderItemId"].(string)
		return observed{
			messageID: messageEvent.ProviderItemID,
			toolID:    toolEvent.ProviderItemID,
			parentID:  parentID,
		}
	}

	first := open(t, "scope-one")
	second := open(t, "scope-two")
	if first.messageID == second.messageID || first.toolID == second.toolID || first.parentID == second.parentID {
		t.Fatalf("provider scopes reused opaque ids: first=%+v second=%+v", first, second)
	}
	firstScope := &conversation{providerScopeID: "scope-one"}
	if first.messageID != firstScope.providerItemID("reused-message") ||
		first.toolID != firstScope.providerItemID("reused-tool") ||
		first.parentID != firstScope.providerItemID("reused-parent") {
		t.Fatalf("first scoped ids = %+v", first)
	}
	secondScope := &conversation{providerScopeID: "scope-two"}
	if second.messageID != secondScope.providerItemID("reused-message") ||
		second.toolID != secondScope.providerItemID("reused-tool") ||
		second.parentID != secondScope.providerItemID("reused-parent") {
		t.Fatalf("second scoped ids = %+v", second)
	}
}

func TestACPOpaqueIDNamespaceIsUnambiguous(t *testing.T) {
	first := &conversation{providerScopeID: "a"}
	second := &conversation{providerScopeID: "a:b"}

	firstID := first.providerItemID("b:c")
	secondID := second.providerItemID("c")
	if firstID == secondID {
		t.Fatalf("distinct scope/id pairs collided at %q", firstID)
	}

	firstTurn := historyTurnID("a", "b:c")
	secondTurn := historyTurnID("a:b", "c")
	if firstTurn == secondTurn {
		t.Fatalf("distinct history scope/id pairs collided at %q", firstTurn)
	}
}

func TestACPLegacyUnscopedHistoryIdentityRemainsCompatible(t *testing.T) {
	conv := &conversation{
		history: &historyCapture{
			sessionID: "legacy-session", occurrences: make(map[string]int),
		},
	}
	conv.startHistoryTurn("legacy-user")
	if got := conv.history.turnID; got != "acp-history-turn:legacy-session:legacy-user" {
		t.Fatalf("legacy history turn = %q", got)
	}
	if got := conv.providerItemID("legacy-item"); got != "legacy-item" {
		t.Fatalf("legacy provider item = %q, want raw identity", got)
	}
}

func TestACPHistoryEventIdentityIsUnambiguousWithNULInOpaqueIDs(t *testing.T) {
	capture := func(event ports.ChatEvent) string {
		t.Helper()
		conv := &conversation{
			providerScopeID: "scope",
			history: &historyCapture{
				sessionID:   "session",
				occurrences: make(map[string]int),
			},
		}
		if !conv.captureHistoryEvent(event) {
			t.Fatal("captureHistoryEvent did not capture replay event")
		}
		return conv.history.events[0].ProviderEventID
	}

	first := capture(ports.ChatEvent{
		Kind: ports.ChatEventMessageDelta, ProviderTurnID: "a", ProviderItemID: "b\x00c",
	})
	second := capture(ports.ChatEvent{
		Kind: ports.ChatEventMessageDelta, ProviderTurnID: "a\x00b", ProviderItemID: "c",
	})
	if first == second {
		t.Fatalf("distinct history events collided at %q", first)
	}
}

func TestACPDriverExtractsCommandFromExecuteToolInput(t *testing.T) {
	agent := &fakeAgent{}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)
	opened, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer opened.Close()
	_ = nextEvent(t, opened.Events())
	conv := opened.(*conversation)
	conv.mu.Lock()
	conv.activeTurn = "turn-1"
	conv.mu.Unlock()

	// claude-code's Bash tool reports rawInput as {"command": "..."} — exactly
	// the shape the neutral `detail.command` contract must be filled from.
	rawInput := map[string]any{"command": "/bin/zsh -lc 'ao session ls'"}
	if err := agent.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(opened.ProviderConversationID()),
		Update: acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
			SessionUpdate: "tool_call", ToolCallId: "bash-1", Title: "List sessions",
			Kind: acpsdk.ToolKindExecute, Status: acpsdk.ToolCallStatusPending,
			RawInput: rawInput,
		}},
	}); err != nil {
		t.Fatalf("tool start: %v", err)
	}
	started := nextEvent(t, opened.Events())
	if started.Kind != ports.ChatEventActivityStarted {
		t.Fatalf("started event = %#v", started)
	}
	var detail map[string]any
	if err := json.Unmarshal(started.Detail, &detail); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail["command"] != "ao session ls" {
		t.Fatalf("detail.command = %#v, want unwrapped %q", detail["command"], "ao session ls")
	}
	if detail["rawCommand"] != "/bin/zsh -lc 'ao session ls'" {
		t.Fatalf("detail.rawCommand = %#v, want the verbatim provider command", detail["rawCommand"])
	}
	if detail["input"] == nil {
		t.Fatalf("detail.input dropped: %#v", detail)
	}
}

func TestRawCommandFromInput(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want string
	}{
		{name: "nil", raw: nil, want: ""},
		{name: "string passthrough is not an object", raw: "go test ./...", want: ""},
		{
			name: "claude-code bash",
			raw:  map[string]any{"command": "rg -n pattern src/", "description": "search"},
			want: "rg -n pattern src/",
		},
		{
			name: "shell-wrapped command",
			raw:  map[string]any{"command": "/bin/bash -c 'go build ./...'"},
			want: "/bin/bash -c 'go build ./...'",
		},
		{name: "empty command", raw: map[string]any{"command": "  "}, want: ""},
		{name: "cmd key", raw: map[string]any{"cmd": "ls"}, want: "ls"},
		{
			name: "edit tool input has no command",
			raw:  map[string]any{"file_path": "/tmp/x", "old": "a", "new": "b"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rawCommandFromInput(tt.raw); got != tt.want {
				t.Fatalf("rawCommandFromInput(%v) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestToolOutputTextNormalizesProviderDefinedRawOutput(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want string
	}{
		{name: "plain text", raw: "ok\n", want: "ok\n"},
		{
			name: "OpenCode output envelope",
			raw: map[string]any{
				"metadata": map[string]any{"exit": float64(0), "output": "metadata copy"},
				"output":   "command output\n",
			},
			want: "command output\n",
		},
		{
			name: "error envelope",
			raw:  map[string]any{"error": "Tool execution aborted"},
			want: "Tool execution aborted",
		},
		{
			name: "unknown structured output remains visible",
			raw:  map[string]any{"result": true},
			want: `{"result":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolOutputText(tt.raw); got != tt.want {
				t.Fatalf("toolOutputText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestACPDriverMapsCostRateLimitsAndAuthRecovery(t *testing.T) {
	agent := &fakeAgent{promptNoPermission: true}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)
	opened, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer opened.Close()
	_ = nextEvent(t, opened.Events())

	if err := agent.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(opened.ProviderConversationID()),
		Update: acpsdk.SessionUpdate{UsageUpdate: &acpsdk.SessionUsageUpdate{
			SessionUpdate: "usage_update", Used: 25, Size: 100, Cost: &acpsdk.Cost{Amount: 1.25, Currency: "USD"},
			Meta: map[string]any{"_claude/rateLimit": map[string]any{
				"utilization": 0.8, "resetsAt": float64(time.Now().Add(time.Hour).Unix()),
				"rateLimitType": "five_hour",
			}},
		}},
	}); err != nil {
		t.Fatalf("usage update: %v", err)
	}
	usageEvent := nextEvent(t, opened.Events())
	limitEvent := nextEvent(t, opened.Events())
	if usageEvent.Usage == nil || usageEvent.Usage.Cost == nil || *usageEvent.Usage.Cost != 1.25 || usageEvent.Usage.Currency != "USD" {
		t.Fatalf("usage event = %#v", usageEvent)
	}
	if limitEvent.RateLimits == nil || limitEvent.RateLimits.PrimaryUsedPercent != 80 || limitEvent.RateLimits.PrimaryResetsInSeconds < 3500 {
		t.Fatalf("rate-limit event = %#v", limitEvent)
	}

	agent.mu.Lock()
	agent.promptNoPermission = false
	agent.promptErr = acpsdk.NewAuthRequired(nil)
	agent.mu.Unlock()
	ref, err := opened.SendTurn(context.Background(), ports.ChatUserMessage{Text: "continue"})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := opened.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}
	foundAccount := false
	for {
		event := nextEvent(t, opened.Events())
		if event.Kind == ports.ChatEventAccountChanged {
			foundAccount = event.Account != nil && event.Account.ReauthRequired
		}
		if event.Kind == ports.ChatEventTurnCompleted {
			if event.TurnState != domain.TurnStateFailed {
				t.Fatalf("turn state = %q", event.TurnState)
			}
			break
		}
	}
	if !foundAccount {
		t.Fatal("authentication failure did not emit an account recovery event")
	}
}

func TestACPDriverExposesAndMutatesAdvertisedConfigOptions(t *testing.T) {
	initial := []acpsdk.SessionConfigOption{
		selectConfigOption("model", "Model", "model", "sonnet", "sonnet", "opus"),
		booleanConfigOption("fast", "Fast mode", true),
	}
	agent := &fakeAgent{
		newConfig: initial,
		setConfig: []acpsdk.SessionConfigOption{
			selectConfigOption("model", "Model", "model", "opus", "sonnet", "opus"),
			selectConfigOption("effort", "Effort", "thought_level", "high", "low", "high"),
			booleanConfigOption("fast", "Fast mode", true),
		},
	}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch: func(context.Context, LaunchConfig) (Launch, error) {
			return Launch{Command: "fake"}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conv.Close()
	configurer := conv.(ports.ChatConfigOptionController)
	if !conv.Capabilities()[ports.ChatCapabilityConfigOptions] {
		t.Fatal("config_options capability was not advertised")
	}

	options, err := configurer.ListConfigOptions(context.Background())
	if err != nil {
		t.Fatalf("ListConfigOptions: %v", err)
	}
	if len(options) != 2 || options[0].Current.Select != "sonnet" {
		t.Fatalf("initial options = %#v", options)
	}
	if options[1].Current.Boolean == nil || !*options[1].Current.Boolean {
		t.Fatalf("boolean option = %#v", options[1])
	}

	if _, err := configurer.SetConfigOption(context.Background(), "model", ports.ChatConfigOptionValue{Select: "unknown"}); !errors.Is(err, ports.ErrChatConfigOptionInvalid) {
		t.Fatalf("invalid selection error = %v", err)
	}
	options, err = configurer.SetConfigOption(context.Background(), "model", ports.ChatConfigOptionValue{Select: "opus"})
	if err != nil {
		t.Fatalf("SetConfigOption: %v", err)
	}
	if len(options) != 3 || options[0].Current.Select != "opus" || options[1].Category != "thought_level" {
		t.Fatalf("replacement options = %#v", options)
	}
	agent.mu.Lock()
	gotValue, calls := agent.options["model"], agent.setCalls
	agent.mu.Unlock()
	if gotValue != "opus" || calls != 1 {
		t.Fatalf("agent received model = %q across %d calls", gotValue, calls)
	}
}

func TestACPDriverConsumesLegacyKimiSelectorsOnSDK0135(t *testing.T) {
	agent := &legacyKimiAgent{}
	driver := New(Config{
		Harness:      domain.HarnessKimi,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		SessionOptions: func(settings ports.ChatTurnSettings) []SessionOption {
			if settings.Model == "" {
				return nil
			}
			return []SessionOption{{ID: "model", Value: settings.Model}}
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeLegacyKimiSpawn(agent)

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath: t.TempDir(), Model: "kimi-code/kimi-for-coding",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conv.Close()

	options, err := conv.(ports.ChatConfigOptionController).ListConfigOptions(context.Background())
	if err != nil {
		t.Fatalf("ListConfigOptions: %v", err)
	}
	if len(options) != 2 || options[0].ID != "model" || options[0].Current.Select != "kimi-code/kimi-for-coding" ||
		options[1].ID != "mode" || options[1].Current.Select != "default" {
		t.Fatalf("legacy Kimi options = %#v, want model and default mode", options)
	}

	controller := conv.(ports.ChatConfigOptionController)
	if _, err := controller.SetConfigOption(context.Background(), "model", ports.ChatConfigOptionValue{
		Select: "kimi-code/kimi-for-coding",
	}); err != nil {
		t.Fatalf("SetConfigOption model: %v", err)
	}
	if _, err := controller.SetConfigOption(context.Background(), "mode", ports.ChatConfigOptionValue{
		Select: "default",
	}); err != nil {
		t.Fatalf("SetConfigOption mode: %v", err)
	}

	agent.mu.Lock()
	model, modelCalls := agent.model, agent.modelCalls
	mode, modeCalls, configCalls := agent.mode, agent.modeCalls, agent.configCalls
	agent.mu.Unlock()
	if model != "kimi-code/kimi-for-coding" || modelCalls != 2 ||
		mode != "default" || modeCalls != 1 || configCalls != 0 {
		t.Fatalf("legacy setters: model=%q/%d mode=%q/%d config=%d",
			model, modelCalls, mode, modeCalls, configCalls)
	}
}

func TestACPDriverExposesDynamicAvailableCommandsAsSkills(t *testing.T) {
	agent := &fakeAgent{}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch: func(context.Context, LaunchConfig) (Launch, error) {
			return Launch{Command: "fake"}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conv.Close()
	lister := conv.(ports.ChatSkillLister)

	if err := agent.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(conv.ProviderConversationID()),
		Update: acpsdk.SessionUpdate{AvailableCommandsUpdate: &acpsdk.SessionAvailableCommandsUpdate{
			SessionUpdate: "available_commands_update",
			AvailableCommands: []acpsdk.AvailableCommand{{
				Name: "review", Description: "Review a pull request",
				Input: &acpsdk.AvailableCommandInput{Unstructured: &acpsdk.UnstructuredCommandInput{Hint: "<number>"}},
			}},
		}},
	}); err != nil {
		t.Fatalf("SessionUpdate: %v", err)
	}

	skills := awaitSkillCount(t, lister, 1)
	if len(skills) != 1 || skills[0] != (ports.ChatSkill{
		Name: "review", DisplayName: "review", Description: "Review a pull request",
		InputHint: "<number>", Source: "agent",
	}) {
		t.Fatalf("skills = %#v", skills)
	}
	if !conv.Capabilities()[ports.ChatCapabilitySkills] {
		t.Fatal("skills capability was not advertised after the command catalog arrived")
	}

	// ACP updates are snapshots. An empty update removes commands that are no
	// longer available but keeps the feature known, so the UI can render no menu.
	if err := agent.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(conv.ProviderConversationID()),
		Update: acpsdk.SessionUpdate{AvailableCommandsUpdate: &acpsdk.SessionAvailableCommandsUpdate{
			SessionUpdate:     "available_commands_update",
			AvailableCommands: []acpsdk.AvailableCommand{},
		}},
	}); err != nil {
		t.Fatalf("empty SessionUpdate: %v", err)
	}
	skills = awaitSkillCount(t, lister, 0)
	if len(skills) != 0 {
		t.Fatalf("skills after replacement = %#v, want empty", skills)
	}
}

func awaitSkillCount(t *testing.T, lister ports.ChatSkillLister, want int) []ports.ChatSkill {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		skills, err := lister.ListSkills(context.Background())
		if err != nil {
			t.Fatalf("ListSkills: %v", err)
		}
		if len(skills) == want {
			return skills
		}
		if time.Now().After(deadline) {
			t.Fatalf("skills = %#v, want %d", skills, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestACPDriverMapsAdvertisedSteeringOntoAO(t *testing.T) {
	agent := &fakeAgent{
		steering: true,
		capabilities: &acpsdk.AgentCapabilities{
			PromptCapabilities: acpsdk.PromptCapabilities{Image: true},
			SessionCapabilities: acpsdk.SessionCapabilities{
				Resume: &acpsdk.SessionResumeCapabilities{},
			},
		},
	}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch: func(context.Context, LaunchConfig) (Launch, error) {
			return Launch{Command: "fake"}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	opened, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer opened.Close()
	if !opened.Capabilities()[ports.ChatCapabilitySteer] {
		t.Fatal("steer capability was not derived from ACP initialize metadata")
	}
	conv := opened.(*conversation)
	conv.mu.Lock()
	conv.activeTurn = "turn-1"
	conv.mu.Unlock()

	ref, err := conv.Steer(context.Background(), "turn-1", ports.ChatUserMessage{
		Text: "focus on the API",
		Content: []ports.ChatContent{{
			Type: "image", Data: "aGVsbG8=", MIMEType: "image/png",
		}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if ref.ProviderTurnID != "turn-1" {
		t.Fatalf("steered turn = %q, want turn-1", ref.ProviderTurnID)
	}
	agent.mu.Lock()
	text, meta, prompt := agent.steerText, agent.steerMeta, agent.steerPrompt
	agent.mu.Unlock()
	if text != "focus on the API" {
		t.Fatalf("steer text = %q", text)
	}
	if len(prompt) != 2 || prompt[1].Image == nil || prompt[1].Image.MimeType != "image/png" {
		t.Fatalf("steer prompt = %#v, want text and image", prompt)
	}
	steering, _ := meta["steering"].(map[string]any)
	if steering["idleBehavior"] != "promptRequired" {
		t.Fatalf("steering meta = %#v", meta)
	}

	agent.mu.Lock()
	agent.steerOut = "promptRequired"
	agent.mu.Unlock()
	if _, err := conv.Steer(context.Background(), "turn-1", ports.ChatUserMessage{Text: "too late"}); !errors.Is(err, ports.ErrChatNoSteerableTurn) {
		t.Fatalf("late steer error = %v, want ErrChatNoSteerableTurn", err)
	}
	if _, err := conv.Steer(context.Background(), "other-turn", ports.ChatUserMessage{Text: "wrong turn"}); !errors.Is(err, ports.ErrChatNoSteerableTurn) {
		t.Fatalf("wrong-turn steer error = %v, want ErrChatNoSteerableTurn", err)
	}
}

func selectConfigOption(id, name, category, current string, values ...string) acpsdk.SessionConfigOption {
	categoryValue := acpsdk.SessionConfigOptionCategory(category)
	choices := make(acpsdk.SessionConfigSelectOptionsUngrouped, 0, len(values))
	for _, value := range values {
		choices = append(choices, acpsdk.SessionConfigSelectOption{
			Value: acpsdk.SessionConfigValueId(value), Name: value,
		})
	}
	return acpsdk.SessionConfigOption{Select: &acpsdk.SessionConfigOptionSelect{
		Id: acpsdk.SessionConfigId(id), Name: name, Category: &categoryValue,
		CurrentValue: acpsdk.SessionConfigValueId(current),
		Options:      acpsdk.SessionConfigSelectOptions{Ungrouped: &choices},
		Type:         "select",
	}}
}

func booleanConfigOption(id, name string, current bool) acpsdk.SessionConfigOption {
	return acpsdk.SessionConfigOption{Boolean: &acpsdk.SessionConfigOptionBoolean{
		Id: acpsdk.SessionConfigId(id), Name: name, CurrentValue: current, Type: "boolean",
	}}
}

func fakeSpawn(agent *fakeAgent) spawnFunc {
	return func(Launch, string) (*process, error) {
		clientToAgentR, clientToAgentW := io.Pipe()
		agentToClientR, agentToClientW := io.Pipe()
		agent.conn = acpsdk.NewAgentSideConnection(agent, agentToClientW, clientToAgentR)
		var once sync.Once
		return &process{
			stdin: clientToAgentW, stdout: agentToClientR,
			stop: func() error {
				once.Do(func() {
					_ = clientToAgentW.Close()
					_ = clientToAgentR.Close()
					_ = agentToClientW.Close()
					_ = agentToClientR.Close()
				})
				return nil
			},
		}, nil
	}
}

func nextEvent(t *testing.T, events <-chan ports.ChatEvent) ports.ChatEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event stream closed")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return ports.ChatEvent{}
	}
}

// TestACPDriverStartToleratesMethodNotFound verifies that Start() succeeds
// even when the agent returns -32601 for session/set_mode and
// session/set_config_option. The launch-time flags (model, --auto, --yolo)
// are expected to have already applied the initial settings.
func TestACPDriverStartToleratesMethodNotFound(t *testing.T) {
	agent := &fakeAgent{
		modeNotFound:   true,
		configNotFound: true,
	}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		SessionMode:  func(ports.PermissionMode) string { return "acceptEdits" },
		SessionOptions: func(settings ports.ChatTurnSettings) []SessionOption {
			if settings.Model == "" {
				return nil
			}
			return []SessionOption{{ID: "model", Value: settings.Model}}
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath: t.TempDir(),
		Model:         "glm-5.2",
		Permissions:   ports.PermissionModeAcceptEdits,
	})
	if err != nil {
		t.Fatalf("Start with -32601 setters: %v", err)
	}
	defer conv.Close()
}

// TestACPDriverSendTurnPropagatesMethodNotFound verifies that SendTurn returns
// an actionable error (not a silent skip) when the agent returns -32601 for
// session/set_mode or session/set_config_option during a runtime settings change.
func TestACPDriverSendTurnPropagatesMethodNotFound(t *testing.T) {
	agent := &fakeAgent{
		modeNotFound:   true,
		configNotFound: true,
	}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		SessionMode:  func(ports.PermissionMode) string { return "acceptEdits" },
		SessionOptions: func(settings ports.ChatTurnSettings) []SessionOption {
			if settings.Model == "" {
				return nil
			}
			return []SessionOption{{ID: "model", Value: settings.Model}}
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conv.Close()

	// A runtime mode change should fail with ErrACPSetterUnsupported, not be
	// silently swallowed.
	_, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{
		Text:     "hello",
		Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAcceptEdits},
	})
	if !errors.Is(err, ErrACPSetterUnsupported) {
		t.Fatalf("SendTurn with -32601 mode setter: err = %v, want ErrACPSetterUnsupported", err)
	}
}

func TestACPDriverRejectsUnsupportedTurnSettingsAtStartAndSend(t *testing.T) {
	agent := &fakeAgent{}
	driver := New(Config{
		Harness:      domain.HarnessKimi,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		ValidateTurnSettings: func(_ ports.PermissionMode, settings ports.ChatTurnSettings) error {
			if ports.NormalizePermissionMode(settings.Approval) == ports.PermissionModeDefault {
				return nil
			}
			return ports.ErrChatPermissionModeUnsupported
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	_, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath: t.TempDir(), Permissions: ports.PermissionModeAuto,
	})
	if !errors.Is(err, ports.ErrChatPermissionModeUnsupported) {
		t.Fatalf("Start unsupported permissions error = %v", err)
	}

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start default permissions: %v", err)
	}
	defer conv.Close()
	_, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{
		Text: "do work", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAuto},
	})
	if !errors.Is(err, ports.ErrChatPermissionModeUnsupported) {
		t.Fatalf("SendTurn unsupported permissions error = %v", err)
	}
}

// TestNormalizeMCPServersFailsWithoutCapabilities verifies that
// normalizeMCPServers returns an error when MCP server configs are provided
// but the agent does not advertise any MCP capability.
func TestNormalizeMCPServersFailsWithoutCapabilities(t *testing.T) {
	configs := []ports.ChatMCPServerConfig{{Name: "test", Type: "stdio", Command: "echo"}}
	_, err := normalizeMCPServers(configs, acpsdk.McpCapabilities{})
	if err == nil {
		t.Fatal("normalizeMCPServers with no MCP caps: err = nil, want error")
	}
	if !strings.Contains(err.Error(), "does not support per-session MCP") {
		t.Fatalf("err = %v, want mention of per-session MCP", err)
	}
}

// TestNormalizeMCPServersSucceedsWithHttpCapability verifies that stdio
// servers pass when the agent advertises HTTP MCP (any MCP capability is
// sufficient — the transport-specific check happens later).
func TestNormalizeMCPServersSucceedsWithHttpCapability(t *testing.T) {
	configs := []ports.ChatMCPServerConfig{{Name: "test", Type: "stdio", Command: "echo"}}
	servers, err := normalizeMCPServers(configs, acpsdk.McpCapabilities{Http: true})
	if err != nil {
		t.Fatalf("normalizeMCPServers with Http cap: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(servers))
	}
}

// TestNormalizeMCPServersEmptyReturnsEmptySlice verifies that empty configs
// return a non-nil empty slice (not nil) so the SDK serializes it correctly.
func TestNormalizeMCPServersEmptyReturnsEmptySlice(t *testing.T) {
	servers, err := normalizeMCPServers(nil, acpsdk.McpCapabilities{})
	if err != nil {
		t.Fatalf("normalizeMCPServers(nil): %v", err)
	}
	if servers == nil {
		t.Fatal("servers = nil, want non-nil empty slice")
	}
	if len(servers) != 0 {
		t.Fatalf("servers = %d, want 0", len(servers))
	}
}

// TestACPDriverPreservesEarlyConfigOptionUpdates verifies that config option
// updates received via session/update during session/new are not overwritten
// when the NewSession response carries an empty config options catalog.
func TestACPDriverPreservesEarlyConfigOptionUpdates(t *testing.T) {
	earlyOption := selectConfigOption("model", "Model", "model", "glm-5.2", "glm-5.2", "kimi")
	agent := &fakeAgent{
		newConfig: nil, // session/new response has no config options
		newSessionUpdates: []acpsdk.SessionUpdate{
			{ConfigOptionUpdate: &acpsdk.SessionConfigOptionUpdate{ConfigOptions: []acpsdk.SessionConfigOption{earlyOption}}},
		},
	}
	driver := New(Config{
		Harness:      domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.spawn = fakeSpawn(agent)

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conv.Close()

	configurer := conv.(ports.ChatConfigOptionController)
	options, err := configurer.ListConfigOptions(context.Background())
	if err != nil {
		t.Fatalf("ListConfigOptions: %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1 (early update preserved)", len(options))
	}
	if options[0].ID != "model" {
		t.Fatalf("option id = %q, want %q", options[0].ID, "model")
	}
}
