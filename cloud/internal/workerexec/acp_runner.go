package workerexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
)

type approvalControl interface {
	CreateChatApproval(context.Context, worker.ChatApproval) error
	ChatApprovalDecision(context.Context, string, int, string) (string, error)
}

type capabilityPublisher interface {
	PublishTurnCapabilities(context.Context, string, int, bool) error
}

type acpSession struct {
	conn      *acp.ClientSideConnection
	sessionID acp.SessionId
	turnID    string
	steering  bool
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.Buffer.String() }

func (s *acpSession) Steer(ctx context.Context, turnID, text string) error {
	if s == nil || turnID != s.turnID || !s.steering {
		return errors.New("the active provider turn does not support steering")
	}
	raw, err := s.conn.CallExtension(ctx, "_session/steering", map[string]any{
		"sessionId": s.sessionID,
		"prompt":    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: text}}},
		"_meta":     map[string]any{"steering": map[string]any{"idleBehavior": "promptRequired"}},
	})
	if err != nil {
		return fmt.Errorf("ACP steer: %w", err)
	}
	var result struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.Outcome != "injected" {
		return fmt.Errorf("ACP did not inject guidance: %s", result.Outcome)
	}
	return nil
}

func acpSteeringSupported(meta map[string]any) bool {
	value, _ := meta["steering"].(map[string]any)
	supported, _ := value["supported"].(bool)
	return supported
}

func (s *Supervisor) runACP(ctx context.Context, turn worker.Turn, command Command, publish func(Output) error, identity func(string) error) error {
	if len(turn.DeniedCommands) > 0 {
		return fmt.Errorf("%w: ACP cannot enforce command-prefix deny rules", ErrUnsupportedPolicy)
	}
	control, ok := s.Control.(approvalControl)
	if !ok {
		return errors.New("Cloud approval control is unavailable")
	}
	path, args, env, err := acpLaunch(turn, command)
	if err != nil {
		return err
	}
	process := exec.CommandContext(ctx, path, args...)
	configureProviderProcess(process)
	process.Dir = command.Dir
	process.Env = mergedEnvironment(env)
	stdin, err := process.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr lockedBuffer
	process.Stderr = &stderr
	if err := process.Start(); err != nil {
		return fmt.Errorf("start ACP agent: %w", err)
	}
	defer func() { _ = stopProviderProcess(process); _ = process.Wait() }()
	client := &cloudACPClient{control: control, turn: turn, publish: publish}
	conn := acp.NewClientSideConnection(client, stdin, stdout)
	handshakeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	initialized, err := conn.Initialize(handshakeCtx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	if err != nil {
		return fmt.Errorf("initialize ACP agent: %w: %s", err, boundedError(stderr.String()))
	}
	var meta map[string]any
	if turn.Harness == "claude-code" && command.SystemPrompt != "" {
		meta = map[string]any{"systemPrompt": map[string]any{
			"type": "preset", "preset": "claude_code", "append": command.SystemPrompt,
		}}
	}
	var sessionID acp.SessionId
	var configOptions []acp.SessionConfigOption
	var modes *acp.SessionModeState
	if turn.AgentSessionID != "" {
		loaded, loadErr := conn.LoadSession(handshakeCtx, acp.LoadSessionRequest{Meta: meta, Cwd: command.Dir, McpServers: []acp.McpServer{}, SessionId: acp.SessionId(turn.AgentSessionID)})
		err = loadErr
		if err != nil {
			return fmt.Errorf("restore ACP session: %w", err)
		}
		sessionID = acp.SessionId(turn.AgentSessionID)
		configOptions, modes = loaded.ConfigOptions, loaded.Modes
	} else {
		created, createErr := conn.NewSession(handshakeCtx, acp.NewSessionRequest{Meta: meta, Cwd: command.Dir, McpServers: []acp.McpServer{}})
		if createErr != nil {
			return fmt.Errorf("create ACP session: %w: %s", createErr, boundedError(stderr.String()))
		}
		sessionID = created.SessionId
		configOptions, modes = created.ConfigOptions, created.Modes
		if err := identity(string(sessionID)); err != nil {
			return err
		}
	}
	if err := configureACPSession(ctx, conn, sessionID, turn, configOptions, modes); err != nil {
		return err
	}
	active := &acpSession{conn: conn, sessionID: sessionID, turnID: turn.ID, steering: acpSteeringSupported(initialized.Meta)}
	if publisher, ok := s.Control.(capabilityPublisher); ok {
		if err := publisher.PublishTurnCapabilities(ctx, turn.ID, turn.Attempt, active.steering); err != nil {
			return err
		}
	}
	s.activeMu.Lock()
	s.activeACP = active
	s.activeMu.Unlock()
	defer func() {
		s.activeMu.Lock()
		if s.activeACP == active {
			s.activeACP = nil
		}
		s.activeMu.Unlock()
	}()
	// The LoadSession/NewSession handshake above is complete, so any replayed
	// history has already been dropped; record from here on — this turn's output.
	client.live.Store(true)
	_, err = conn.Prompt(ctx, acp.PromptRequest{SessionId: sessionID, Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: turn.Prompt}}}})
	if err != nil {
		return fmt.Errorf("ACP prompt: %w: %s", err, boundedError(stderr.String()))
	}
	return nil
}

func acpLaunch(turn worker.Turn, command Command) (string, []string, map[string]string, error) {
	env := make(map[string]string, len(command.Env)+1)
	for key, value := range command.Env {
		env[key] = value
	}
	switch turn.Harness {
	case "claude-code":
		env["CLAUDE_CODE_EXECUTABLE"] = command.Path
		return "claude-agent-acp", nil, env, nil
	case "cursor":
		args := []string{"--trust"}
		switch turn.ApprovalMode {
		case "auto":
			args = append(args, "--auto-review")
		case "bypass-permissions":
			args = append(args, "--force")
		}
		return command.Path, append(args, "acp"), env, nil
	default:
		return "", nil, nil, fmt.Errorf("ACP is unavailable for %s", turn.Harness)
	}
}

func configureACPSession(ctx context.Context, conn *acp.ClientSideConnection, sessionID acp.SessionId, turn worker.Turn, options []acp.SessionConfigOption, modes *acp.SessionModeState) error {
	setOption := func(id, value string) error {
		if value == "" {
			return nil
		}
		if !acpOptionOffered(options, id, value) {
			return fmt.Errorf("ACP session does not offer %s %q", id, value)
		}
		response, err := conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: sessionID, ConfigId: acp.SessionConfigId(id), Value: acp.SessionConfigValueId(value),
		}})
		if err != nil {
			return fmt.Errorf("set ACP %s: %w", id, err)
		}
		options = response.ConfigOptions
		return nil
	}
	// A model change can alter the available permission modes and effort levels.
	if err := setOption("model", turn.Model); err != nil {
		return err
	}
	if turn.Harness == "claude-code" {
		mode := "default"
		if turn.Mode == "read-only" {
			mode = "plan"
		} else {
			switch turn.ApprovalMode {
			case "accept-edits":
				mode = "acceptEdits"
			case "auto":
				mode = "auto"
			case "bypass-permissions":
				mode = "bypassPermissions"
			}
		}
		if mode == "auto" && !acpModeOffered(options, modes, mode) && acpModeOffered(options, modes, "default") {
			mode = "default"
		}
		if !acpModeOffered(options, modes, mode) {
			return fmt.Errorf("ACP session does not offer approval mode %q", mode)
		}
		if _, err := conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sessionID, ModeId: acp.SessionModeId(mode)}); err != nil {
			return fmt.Errorf("set Claude approval mode: %w", err)
		}
	}
	return setOption("effort", turn.ReasoningEffort)
}

func acpOptionOffered(options []acp.SessionConfigOption, id, value string) bool {
	for _, option := range options {
		if option.Select == nil || string(option.Select.Id) != id {
			continue
		}
		if option.Select.Options.Ungrouped != nil {
			for _, choice := range *option.Select.Options.Ungrouped {
				if string(choice.Value) == value {
					return true
				}
			}
		}
		if option.Select.Options.Grouped != nil {
			for _, group := range *option.Select.Options.Grouped {
				for _, choice := range group.Options {
					if string(choice.Value) == value {
						return true
					}
				}
			}
		}
	}
	return false
}

func acpModeOffered(options []acp.SessionConfigOption, modes *acp.SessionModeState, mode string) bool {
	for _, option := range options {
		if option.Select != nil && option.Select.Id == "mode" {
			return acpOptionOffered(options, "mode", mode)
		}
	}
	if modes != nil {
		for _, offered := range modes.AvailableModes {
			if string(offered.Id) == mode {
				return true
			}
		}
	}
	return false
}

type cloudACPClient struct {
	control approvalControl
	turn    worker.Turn
	publish func(Output) error
	// live gates recording of session updates. LoadSession replays the entire
	// prior conversation back through SessionUpdate before it returns; recording
	// that replay would re-emit every earlier turn's assistant text at the head
	// of the current turn. It stays false across the LoadSession/NewSession
	// handshake and is flipped true just before Prompt, so only the live turn's
	// output is published.
	live atomic.Bool
}

func (c *cloudACPClient) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	if !c.live.Load() {
		return nil
	}
	if chunk := notification.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
		return c.publish(Output{Stream: "stdout", Text: chunk.Content.Text.Text})
	}
	return nil
}

func (c *cloudACPClient) RequestPermission(ctx context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cancelled := acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
	if ctx.Err() != nil || c.turn.Mode == "read-only" {
		return cancelled, nil
	}
	if selected, ok := automaticACPDecision(c.turn, request); ok {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(selected)}, nil
	}
	options := make([]map[string]string, 0, len(request.Options))
	for _, option := range request.Options {
		options = append(options, map[string]string{"id": string(option.OptionId), "label": option.Name, "kind": string(option.Kind)})
	}
	decisions, err := json.Marshal(options)
	if err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	requestID := uuid.NewString()
	summary := "Permission required"
	if request.ToolCall.Title != nil && strings.TrimSpace(*request.ToolCall.Title) != "" {
		summary = *request.ToolCall.Title
	}
	toolKind := ""
	if request.ToolCall.Kind != nil {
		toolKind = string(*request.ToolCall.Kind)
	}
	if err := c.control.CreateChatApproval(ctx, worker.ChatApproval{
		RequestID: requestID, TurnID: c.turn.ID, Attempt: c.turn.Attempt,
		Summary: summary, ToolKind: toolKind, Decisions: decisions,
	}); err != nil {
		if ctx.Err() != nil {
			return cancelled, nil
		}
		return acp.RequestPermissionResponse{}, err
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		decision, err := c.control.ChatApprovalDecision(ctx, c.turn.ID, c.turn.Attempt, requestID)
		if err != nil {
			if ctx.Err() != nil {
				return cancelled, nil
			}
			return acp.RequestPermissionResponse{}, err
		}
		if ctx.Err() != nil {
			return cancelled, nil
		}
		if decision != "" {
			for _, option := range request.Options {
				if string(option.OptionId) == decision {
					return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(option.OptionId)}, nil
				}
			}
			return acp.RequestPermissionResponse{}, errors.New("approval decision was not offered by the agent")
		}
		select {
		case <-ctx.Done():
			return cancelled, nil
		case <-ticker.C:
		}
	}
}

func automaticACPDecision(turn worker.Turn, request acp.RequestPermissionRequest) (acp.PermissionOptionId, bool) {
	allow := false
	if turn.ApprovalMode == "bypass-permissions" {
		allow = true
	}
	if turn.Harness == "cursor" && turn.ApprovalMode == "accept-edits" && request.ToolCall.Kind != nil {
		switch *request.ToolCall.Kind {
		case acp.ToolKindEdit, acp.ToolKindDelete, acp.ToolKindMove:
			allow = true
		}
	}
	if !allow {
		return "", false
	}
	kinds := []acp.PermissionOptionKind{acp.PermissionOptionKindAllowOnce}
	if turn.ApprovalMode == "bypass-permissions" {
		kinds = append([]acp.PermissionOptionKind{acp.PermissionOptionKindAllowAlways}, kinds...)
	}
	for _, kind := range kinds {
		for _, option := range request.Options {
			if option.Kind == kind {
				return option.OptionId, true
			}
		}
	}
	return "", false
}

var errACPClientCapability = errors.New("ACP client capability is unavailable")

func (*cloudACPClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errACPClientCapability
}
func (*cloudACPClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errACPClientCapability
}
func (*cloudACPClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errACPClientCapability
}
func (*cloudACPClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errACPClientCapability
}
func (*cloudACPClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errACPClientCapability
}
func (*cloudACPClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errACPClientCapability
}
func (*cloudACPClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errACPClientCapability
}

var _ acp.Client = (*cloudACPClient)(nil)
