package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/commanddetail"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// AO deliberately advertises neither client-side filesystem nor terminal
// capabilities. Claude's ACP adapter uses Claude Code's native tools inside the
// worktree; routing those operations through Electron or the daemon would create
// a second execution/security model beside AO's existing one.
func (c *conversation) ReadTextFile(context.Context, acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{}, errClientCapability
}

func (c *conversation) WriteTextFile(context.Context, acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, errClientCapability
}

func (c *conversation) CreateTerminal(context.Context, acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, errClientCapability
}

func (c *conversation) KillTerminal(context.Context, acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, errClientCapability
}

func (c *conversation) TerminalOutput(context.Context, acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, errClientCapability
}

func (c *conversation) ReleaseTerminal(context.Context, acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, errClientCapability
}

func (c *conversation) WaitForTerminalExit(context.Context, acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, errClientCapability
}

func (c *conversation) RequestPermission(
	ctx context.Context,
	params acpsdk.RequestPermissionRequest,
) (acpsdk.RequestPermissionResponse, error) {
	if len(params.Options) == 0 {
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeCancelled()}, nil
	}
	c.mu.Lock()
	policy, mode := c.permissionFor, c.permissionMode
	c.mu.Unlock()
	if policy != nil {
		if selected, handled := policy(mode, params); handled {
			for _, option := range params.Options {
				if option.OptionId == selected {
					return acpsdk.RequestPermissionResponse{
						Outcome: acpsdk.NewRequestPermissionOutcomeSelected(selected),
					}, nil
				}
			}
			return acpsdk.RequestPermissionResponse{}, fmt.Errorf(
				"provider permission policy selected unoffered option %q", selected)
		}
	}
	decisions := make([]ports.ChatDecisionOption, 0, len(params.Options))
	for _, option := range params.Options {
		id := string(option.OptionId)
		raw, _ := json.Marshal(option)
		decisions = append(decisions, ports.ChatDecisionOption{
			ID: id, Label: option.Name, Kind: ports.ChatDecisionKind(option.Kind), Raw: raw,
		})
	}
	summary := "Permission required"
	if params.ToolCall.Title != nil && strings.TrimSpace(*params.ToolCall.Title) != "" {
		summary = *params.ToolCall.Title
	}
	selected, err := c.RequestApproval(ctx, ClientApprovalRequest{
		Summary: summary, ActivityKind: activityKindFromTool(pointerValue(params.ToolCall.Kind)),
		Detail:    approvalToolDetail(params.ToolCall, activityKindFromTool(pointerValue(params.ToolCall.Kind))),
		Decisions: decisions,
	})
	if err != nil || selected == "" {
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeCancelled()}, err
	}
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeSelected(acpsdk.PermissionOptionId(selected)),
	}, nil
}

// RequestApproval parks a provider extension on AO's durable approval flow.
func (c *conversation) RequestApproval(
	ctx context.Context,
	params ClientApprovalRequest,
) (string, error) {
	if len(params.Decisions) == 0 {
		return "", nil
	}
	requestID := uuid.NewString()
	options := make(map[string]json.RawMessage, len(params.Decisions))
	for _, option := range params.Decisions {
		options[option.ID] = append(json.RawMessage(nil), option.Raw...)
	}
	request := &parkedPermission{options: options, result: make(chan string, 1)}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", nil
	}
	c.pending[requestID] = request
	turnID := c.activeTurn
	c.mu.Unlock()
	c.emit(ports.ChatEvent{
		Kind:           ports.ChatEventApprovalRequested,
		ProviderTurnID: turnID,
		ProviderItemID: requestID,
		ActivityKind:   params.ActivityKind,
		ActivityStatus: domain.ActivityStatusPending,
		Summary:        params.Summary,
		Detail:         params.Detail,
		RequestID:      requestID,
		Decisions:      params.Decisions,
	})

	timer := timeAfter(approvalWait)
	select {
	case selected := <-request.result:
		return selected, nil
	case <-ctx.Done():
		c.discardPermission(requestID)
		c.emit(ports.ChatEvent{Kind: ports.ChatEventApprovalResolved, RequestID: requestID})
		return "", nil
	case <-timer:
		c.discardPermission(requestID)
		c.emit(ports.ChatEvent{Kind: ports.ChatEventApprovalResolved, RequestID: requestID})
		return "", nil
	}
}

func approvalToolDetail(tool acpsdk.ToolCallUpdate, activityKind domain.ActivityKind) []byte {
	detail := map[string]any{
		"protocol": "acp", "toolKind": pointerValue(tool.Kind),
		"subjectKind": string(activityKind),
	}
	if tool.RawInput != nil {
		detail["input"] = tool.RawInput
	}
	if claude := nestedMap(tool.Meta, "claudeCode"); claude != nil {
		copyDetail(detail, claude, "toolName", "providerToolName")
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil
	}
	return encoded
}

// UnstableCreateElicitation bridges ACP's structured input request into AO's
// ordinary durable conversation/event path. The JSON-RPC call remains parked
// until a client answers, exactly like a permission request, but it has a
// separate response contract so form data can never be mistaken for consent.
func (c *conversation) UnstableCreateElicitation(
	ctx context.Context,
	params acpsdk.UnstableCreateElicitationRequest,
) (acpsdk.UnstableCreateElicitationResponse, error) {
	request := ports.ChatInputRequest{}
	switch {
	case params.Form != nil:
		request.Mode = ports.ChatInputModeForm
		request.Message = params.Form.Message
		schema, err := schemaMap(params.Form.RequestedSchema)
		if err != nil {
			return acpsdk.NewUnstableCreateElicitationResponseCancel(), err
		}
		request.Schema = schema
	case params.Url != nil:
		request.Mode = ports.ChatInputModeURL
		request.Message = params.Url.Message
		request.URL = params.Url.Url
		request.ElicitationID = string(params.Url.ElicitationId)
	default:
		return acpsdk.NewUnstableCreateElicitationResponseCancel(), errors.New("ACP elicitation has no mode")
	}

	response, err := c.RequestInput(ctx, request)
	if err != nil {
		return acpsdk.NewUnstableCreateElicitationResponseCancel(), err
	}
	return acpInputResponse(response), nil
}

// RequestInput parks a provider extension on AO's durable structured-input flow.
func (c *conversation) RequestInput(
	ctx context.Context,
	request ports.ChatInputRequest,
) (ports.ChatInputResponse, error) {
	requestID := uuid.NewString()
	parked := &parkedInput{request: request, result: make(chan ports.ChatInputResponse, 1)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ports.ChatInputResponse{Action: ports.ChatInputActionCancel}, nil
	}
	c.pendingInputs[requestID] = parked
	turnID := c.activeTurn
	c.mu.Unlock()

	c.emit(ports.ChatEvent{
		Kind: ports.ChatEventInputRequested, ProviderTurnID: turnID,
		RequestID: requestID, Input: &request, Summary: request.Message,
	})

	timer := timeAfter(approvalWait)
	select {
	case response := <-parked.result:
		return response, nil
	case <-ctx.Done():
		c.discardInput(requestID)
		c.emit(ports.ChatEvent{Kind: ports.ChatEventInputResolved, RequestID: requestID})
		return ports.ChatInputResponse{Action: ports.ChatInputActionCancel}, nil
	case <-timer:
		c.discardInput(requestID)
		c.emit(ports.ChatEvent{Kind: ports.ChatEventInputResolved, RequestID: requestID})
		return ports.ChatInputResponse{Action: ports.ChatInputActionCancel}, nil
	}
}

// UpdatePlan publishes a provider extension plan on the active AO turn.
func (c *conversation) UpdatePlan(plan *domain.ConversationPlan) {
	c.mu.Lock()
	turnID := c.activeTurn
	c.mu.Unlock()
	c.emit(ports.ChatEvent{Kind: ports.ChatEventPlanUpdated, ProviderTurnID: turnID, Plan: plan})
}

func (c *conversation) ResolveInput(
	ctx context.Context,
	requestID string,
	response ports.ChatInputResponse,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	request, ok := c.pendingInputs[requestID]
	if !ok {
		c.mu.Unlock()
		return ports.ErrChatRequestNotPending
	}
	if err := validateInputResponse(request.request, response); err != nil {
		c.mu.Unlock()
		return err
	}
	delete(c.pendingInputs, requestID)
	c.mu.Unlock()

	select {
	case request.result <- response:
		c.emit(ports.ChatEvent{Kind: ports.ChatEventInputResolved, RequestID: requestID})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *conversation) UnstableCompleteElicitation(
	context.Context,
	acpsdk.UnstableCompleteElicitationNotification,
) error {
	// URL completion describes provider-side progress after the user has already
	// consented. The actionable AO request was resolved when that consent was sent.
	return nil
}

func (c *conversation) UnstableConnectMcp(
	context.Context,
	acpsdk.UnstableConnectMcpRequest,
) (acpsdk.UnstableConnectMcpResponse, error) {
	return acpsdk.UnstableConnectMcpResponse{}, errClientCapability
}

func (c *conversation) UnstableDisconnectMcp(
	context.Context,
	acpsdk.UnstableDisconnectMcpRequest,
) (acpsdk.UnstableDisconnectMcpResponse, error) {
	return acpsdk.UnstableDisconnectMcpResponse{}, errClientCapability
}

func schemaMap(schema acpsdk.UnstableElicitationSchema) (map[string]any, error) {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode ACP elicitation schema: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("normalize ACP elicitation schema: %w", err)
	}
	return out, nil
}

func acpInputResponse(response ports.ChatInputResponse) acpsdk.UnstableCreateElicitationResponse {
	switch response.Action {
	case ports.ChatInputActionAccept:
		result := acpsdk.NewUnstableCreateElicitationResponseAccept()
		result.Accept.Content = response.Content
		return result
	case ports.ChatInputActionDecline:
		return acpsdk.NewUnstableCreateElicitationResponseDecline()
	default:
		return acpsdk.NewUnstableCreateElicitationResponseCancel()
	}
}

func validateInputResponse(request ports.ChatInputRequest, response ports.ChatInputResponse) error {
	switch response.Action {
	case ports.ChatInputActionDecline, ports.ChatInputActionCancel:
		return nil
	case ports.ChatInputActionAccept:
		if request.Mode == ports.ChatInputModeURL {
			return nil
		}
	default:
		return fmt.Errorf("%w: unsupported input action %q", ports.ErrChatDecisionNotOffered, response.Action)
	}
	if request.Mode != ports.ChatInputModeForm {
		return fmt.Errorf("%w: unsupported input mode %q", ports.ErrChatDecisionNotOffered, request.Mode)
	}
	if err := validateFormContent(request.Schema, response.Content); err != nil {
		return fmt.Errorf("%w: %s", ports.ErrChatDecisionNotOffered, err.Error())
	}
	return nil
}

func validateFormContent(schema, content map[string]any) error {
	for _, name := range formRequired(schema["required"]) {
		if name == "" {
			continue
		}
		if _, present := content[name]; !present {
			return fmt.Errorf("required input %q is missing", name)
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, value := range content {
		rawProperty, known := properties[name]
		if !known {
			return fmt.Errorf("input %q is not in the requested schema", name)
		}
		property, ok := rawProperty.(map[string]any)
		if !ok {
			return fmt.Errorf("input %q has an invalid requested schema", name)
		}
		if err := validateFormValue(value, property); err != nil {
			return fmt.Errorf("input %q %s", name, err.Error())
		}
	}
	return nil
}

func validateFormValue(value any, property map[string]any) error {
	typeName, _ := property["type"].(string)
	switch typeName {
	case "string", "":
		text, ok := value.(string)
		if !ok {
			return errors.New("must be a string")
		}
		if !formOptionOffered(text, property) {
			return errors.New("is not one of the offered values")
		}
	case "number", "integer":
		numeric, ok := number(value)
		if !ok || math.IsNaN(numeric) || math.IsInf(numeric, 0) {
			return errors.New("must be a finite number")
		}
		if typeName == "integer" && math.Trunc(numeric) != numeric {
			return errors.New("must be an integer")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("must be a boolean")
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return errors.New("must be an array")
		}
		items, _ := property["items"].(map[string]any)
		for _, item := range values {
			text, ok := item.(string)
			if !ok || !formOptionOffered(text, items) {
				return errors.New("contains a value that was not offered")
			}
		}
	default:
		return fmt.Errorf("uses unsupported type %q", typeName)
	}
	return nil
}

func formRequired(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func formOptionOffered(value string, schema map[string]any) bool {
	var options []any
	if candidates, ok := schema["oneOf"].([]any); ok {
		options = candidates
	} else if candidates, ok := schema["anyOf"].([]any); ok {
		options = candidates
	} else if candidates, ok := schema["enum"].([]any); ok {
		for _, candidate := range candidates {
			if candidate == value {
				return true
			}
		}
		return len(candidates) == 0
	}
	if len(options) == 0 {
		return true
	}
	for _, raw := range options {
		option, _ := raw.(map[string]any)
		if option["const"] == value {
			return true
		}
	}
	return false
}

func (c *conversation) discardInput(requestID string) {
	c.mu.Lock()
	delete(c.pendingInputs, requestID)
	c.mu.Unlock()
}

// timeAfter is a variable so permission timeout behavior can be tested without
// sleeping for the production interval.
var timeAfter = time.After

func (c *conversation) discardPermission(requestID string) {
	c.mu.Lock()
	delete(c.pending, requestID)
	c.mu.Unlock()
}

func (c *conversation) SessionUpdate(_ context.Context, params acpsdk.SessionNotification) error {
	if c.prepareHistoryUpdate(params.Update) {
		return nil
	}
	c.mu.Lock()
	sessionID := c.sessionID
	turnID := c.activeTurn
	c.mu.Unlock()
	if sessionID != "" && string(params.SessionId) != sessionID {
		return fmt.Errorf("ACP update for unexpected session %q", params.SessionId)
	}

	update := params.Update
	switch {
	case update.AgentMessageChunk != nil:
		id := c.providerItemID(messageID(update.AgentMessageChunk.MessageId, "assistant", turnID))
		if delta := contentText(update.AgentMessageChunk.Content); delta != "" {
			if parentID := c.providerItemID(parentToolUseID(update.AgentMessageChunk.Meta)); parentID != "" {
				c.mu.Lock()
				item, existed := c.nestedMessages[id]
				item.text += delta
				item.parentID = parentID
				c.nestedMessages[id] = item
				c.mu.Unlock()
				detail, _ := json.Marshal(map[string]any{"parentProviderItemId": parentID, "nestedAgent": true})
				if !existed {
					c.emit(ports.ChatEvent{Kind: ports.ChatEventActivityStarted, ProviderTurnID: turnID,
						ProviderItemID: id, ActivityKind: domain.ActivityKindMCPTool,
						ActivityStatus: domain.ActivityStatusRunning, Summary: "Subagent response", Detail: detail})
				}
				c.emit(ports.ChatEvent{Kind: ports.ChatEventActivityText, ProviderTurnID: turnID,
					ProviderItemID: id, Delta: delta})
				break
			}
			c.mu.Lock()
			c.messages[id] += delta
			c.mu.Unlock()
			c.emit(ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turnID, ProviderItemID: id, Delta: delta})
		}
	case update.AgentThoughtChunk != nil:
		id := c.providerItemID(messageID(update.AgentThoughtChunk.MessageId, "thought", turnID))
		if delta := contentText(update.AgentThoughtChunk.Content); delta != "" {
			c.mu.Lock()
			_, existed := c.thoughts[id]
			c.thoughts[id] += delta
			c.mu.Unlock()
			if !existed {
				c.emit(ports.ChatEvent{Kind: ports.ChatEventActivityStarted, ProviderTurnID: turnID,
					ProviderItemID: id, ActivityKind: domain.ActivityKindReasoning,
					ActivityStatus: domain.ActivityStatusRunning, Summary: "Reasoning"})
			}
			c.emit(ports.ChatEvent{Kind: ports.ChatEventReasoningDelta, ProviderTurnID: turnID, ProviderItemID: id, Delta: delta})
		}
	case update.ToolCall != nil:
		tool := &toolState{
			id: string(update.ToolCall.ToolCallId), title: update.ToolCall.Title,
			kind: update.ToolCall.Kind, status: update.ToolCall.Status,
			locations: update.ToolCall.Locations, content: update.ToolCall.Content,
			rawInput: update.ToolCall.RawInput, rawOutput: update.ToolCall.RawOutput,
			meta: cloneMeta(update.ToolCall.Meta),
		}
		c.mu.Lock()
		c.tools[tool.id] = tool
		c.mu.Unlock()
		c.emit(c.toolEvent(turnID, tool, toolTerminal(tool.status)))
		c.emitDiffs(turnID, tool.content)
	case update.ToolCallUpdate != nil:
		tool := c.mergeToolUpdate(update.ToolCallUpdate)
		if delta := terminalOutput(update.ToolCallUpdate.Meta); delta != "" {
			c.emit(ports.ChatEvent{Kind: ports.ChatEventCommandOutputDelta, ProviderTurnID: turnID,
				ProviderItemID: c.providerItemID(tool.id), Delta: delta})
		}
		c.emit(c.toolEvent(turnID, tool, toolTerminal(tool.status)))
		c.emitDiffs(turnID, tool.content)
	case update.Plan != nil:
		c.emit(ports.ChatEvent{Kind: ports.ChatEventPlanUpdated, ProviderTurnID: turnID, Plan: normalizePlan(update.Plan.Entries)})
	case update.SessionInfoUpdate != nil && update.SessionInfoUpdate.Title != nil:
		c.emit(ports.ChatEvent{Kind: ports.ChatEventThreadRenamed, Title: *update.SessionInfoUpdate.Title})
	case update.ConfigOptionUpdate != nil:
		// The update is a complete replacement, not a delta. Model changes can
		// rebuild effort and fast-mode choices, including removing an option.
		c.replaceConfigOptions(update.ConfigOptionUpdate.ConfigOptions)
	case update.AvailableCommandsUpdate != nil:
		// Like config options, ACP command updates replace the entire catalog. The
		// provider may discover project commands after session setup or remove one
		// when its configuration changes, so retaining absent entries is wrong.
		c.replaceAvailableCommands(update.AvailableCommandsUpdate.AvailableCommands)
	case update.UsageUpdate != nil:
		usage := &ports.ChatUsage{
			ContextUsed: int64(update.UsageUpdate.Used), ContextWindow: int64(update.UsageUpdate.Size),
			ContextKnown: true,
		}
		if update.UsageUpdate.Cost != nil {
			cost := update.UsageUpdate.Cost.Amount
			usage.Cost = &cost
			usage.Currency = update.UsageUpdate.Cost.Currency
		}
		c.emit(ports.ChatEvent{Kind: ports.ChatEventUsage, Usage: usage})
		if limits := claudeRateLimits(update.UsageUpdate.Meta); limits != nil {
			c.emit(ports.ChatEvent{Kind: ports.ChatEventRateLimits, RateLimits: limits})
		}
	}
	return nil
}

func contentText(content acpsdk.ContentBlock) string {
	if content.Text == nil {
		return ""
	}
	return content.Text.Text
}

func messageID(id *string, prefix, turnID string) string {
	if id != nil && *id != "" {
		return *id
	}
	return prefix + "-" + turnID
}

func (c *conversation) mergeToolUpdate(update *acpsdk.SessionToolCallUpdate) *toolState {
	id := string(update.ToolCallId)
	c.mu.Lock()
	defer c.mu.Unlock()
	tool := c.tools[id]
	if tool == nil {
		tool = &toolState{id: id}
		c.tools[id] = tool
	}
	if update.Title != nil {
		tool.title = *update.Title
	}
	if update.Kind != nil {
		tool.kind = *update.Kind
	}
	if update.Status != nil {
		tool.status = *update.Status
	}
	if update.Locations != nil {
		tool.locations = update.Locations
	}
	if update.Content != nil {
		tool.content = update.Content
	}
	if update.RawInput != nil {
		tool.rawInput = update.RawInput
	}
	if update.RawOutput != nil {
		tool.rawOutput = update.RawOutput
	}
	tool.meta = mergeMeta(tool.meta, update.Meta)
	if delta := terminalOutput(update.Meta); delta != "" {
		tool.terminalOutput += delta
	}
	snapshot := *tool
	return &snapshot
}

func (c *conversation) toolEvent(turnID string, tool *toolState, completed bool) ports.ChatEvent {
	output := toolOutputText(tool.rawOutput)
	if tool.terminalOutput != "" {
		output = tool.terminalOutput
	}
	detailMap := map[string]any{
		"protocol": "acp", "toolKind": tool.kind, "locations": tool.locations,
		"input": tool.rawInput, "output": output, "content": tool.content,
	}
	if claude := nestedMap(tool.meta, "claudeCode"); claude != nil {
		copyDetail(detailMap, claude, "toolName", "providerToolName")
		if parentID, ok := claude["parentToolUseId"].(string); ok && parentID != "" {
			detailMap["parentProviderItemId"] = c.providerItemID(parentID)
		}
		copyDetail(detailMap, claude, "subagent", "nestedAgent")
		copyDetail(detailMap, claude, "subagentType", "subagentType")
		copyDetail(detailMap, claude, "subagentRetry", "subagentRetry")
		if title, ok := claude["title"].(string); ok && strings.TrimSpace(title) != "" {
			detailMap["providerTitle"] = title
		}
	}
	if terminal := nestedMap(tool.meta, "terminal_info"); terminal != nil {
		copyDetail(detailMap, terminal, "terminal_id", "terminalId")
	}
	if terminal := nestedMap(tool.meta, "terminal_exit"); terminal != nil {
		copyDetail(detailMap, terminal, "exit_code", "exitCode")
		copyDetail(detailMap, terminal, "signal", "signal")
	}
	if activityKindFromTool(tool.kind) == domain.ActivityKindCommand {
		if rawCommand := rawCommandFromInput(tool.rawInput); rawCommand != "" {
			// The neutral command-detail contract (`detail.command`) is what the
			// chat timeline renders as the row's subject. rawInput is a
			// provider-shaped object (claude-code Bash: {"command": "..."}); the
			// codex driver sets this key directly, and ACP-backed harnesses must
			// too or the UI can only ever say "Ran command".
			detailMap["command"] = commanddetail.UnwrapShell(rawCommand)
			// Keep the exact provider value beside the display form. The timeline is
			// an audit surface, so callers must be able to recover what actually ran
			// even when a provider wraps it in a shell invocation.
			detailMap["rawCommand"] = rawCommand
		}
	}
	detail, _ := json.Marshal(detailMap)
	status := activityStatusFromTool(tool.status)
	kind := ports.ChatEventActivityStarted
	if completed {
		kind = ports.ChatEventActivityCompleted
	}
	summary := strings.TrimSpace(tool.title)
	if summary == "" {
		summary = "Agent tool"
	}
	return ports.ChatEvent{
		Kind: kind, ProviderTurnID: turnID, ProviderItemID: c.providerItemID(tool.id),
		ActivityKind: activityKindFromTool(tool.kind), ActivityStatus: status,
		Summary: summary, Detail: detail,
	}
}

// toolOutputText translates ACP's provider-defined rawOutput into AO's neutral
// command-detail contract, where output is always text. ACP deliberately permits
// any JSON value here; OpenCode, for example, wraps the text as
// {"output":"...","metadata":{...}}. Persisting that object unchanged makes the
// typed frontend contract untrue and crashes text-only renderers such as ANSI
// cleanup.
func toolOutputText(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return value
	case map[string]any:
		for _, key := range []string{"output", "text", "error"} {
			if text := toolOutputText(value[key]); text != "" {
				return text
			}
		}
		if text := toolOutputText(value["metadata"]); text != "" {
			return text
		}
	}

	encoded, err := json.Marshal(raw)
	if err != nil || string(encoded) == "null" {
		return ""
	}
	return string(encoded)
}

// rawCommandFromInput extracts the verbatim shell command from a tool's
// provider-defined rawInput so execute activities can carry AO's neutral command
// detail without making the provider object itself part of that contract.
// Providers wrap the command differently (claude-code Bash uses
// {"command": "..."}); anything unrecognizable stays empty rather than putting
// a provider DTO on the wire as if it were the command.
func rawCommandFromInput(raw any) string {
	value, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"command", "cmd"} {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func parentToolUseID(meta map[string]any) string {
	claude := nestedMap(meta, "claudeCode")
	if claude == nil {
		return ""
	}
	value, _ := claude["parentToolUseId"].(string)
	return value
}

func terminalOutput(meta map[string]any) string {
	terminal := nestedMap(meta, "terminal_output")
	if terminal == nil {
		return ""
	}
	value, _ := terminal["data"].(string)
	return value
}

func nestedMap(meta map[string]any, key string) map[string]any {
	if meta == nil {
		return nil
	}
	value, _ := meta[key].(map[string]any)
	return value
}

func cloneMeta(meta map[string]any) map[string]any {
	return mergeMeta(nil, meta)
}

func mergeMeta(existing, update map[string]any) map[string]any {
	if len(existing) == 0 && len(update) == 0 {
		return nil
	}
	out := make(map[string]any, len(existing)+len(update))
	for key, value := range existing {
		out[key] = value
	}
	for key, value := range update {
		if current, ok := out[key].(map[string]any); ok {
			if incoming, ok := value.(map[string]any); ok {
				out[key] = mergeMeta(current, incoming)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func copyDetail(target, source map[string]any, sourceKey, targetKey string) {
	if value, ok := source[sourceKey]; ok {
		target[targetKey] = value
	}
}

func claudeRateLimits(meta map[string]any) *ports.ChatRateLimits {
	value := nestedMap(meta, "_claude/rateLimit")
	if value == nil {
		return nil
	}
	limits := &ports.ChatRateLimits{PrimaryUsedPercent: -1, SecondaryUsedPercent: -1}
	if utilization, ok := number(value["utilization"]); ok {
		// The Claude SDK reports utilization as 0..1.
		limits.PrimaryUsedPercent = utilization * 100
	}
	if resetsAt, ok := number(value["resetsAt"]); ok {
		remaining := int64(resetsAt) - time.Now().Unix()
		if remaining < 0 {
			remaining = 0
		}
		limits.PrimaryResetsInSeconds = remaining
	}
	if kind, ok := value["rateLimitType"].(string); ok {
		limits.PlanLabel = strings.ReplaceAll(kind, "_", " ")
	}
	return limits
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func activityKindFromTool(kind acpsdk.ToolKind) domain.ActivityKind {
	switch kind {
	case acpsdk.ToolKindExecute:
		return domain.ActivityKindCommand
	case acpsdk.ToolKindEdit, acpsdk.ToolKindDelete, acpsdk.ToolKindMove:
		return domain.ActivityKindFileChange
	case acpsdk.ToolKindThink:
		return domain.ActivityKindReasoning
	default:
		return domain.ActivityKindMCPTool
	}
}

func activityStatusFromTool(status acpsdk.ToolCallStatus) domain.ActivityStatus {
	switch status {
	case acpsdk.ToolCallStatusCompleted:
		return domain.ActivityStatusCompleted
	case acpsdk.ToolCallStatusFailed:
		return domain.ActivityStatusFailed
	case acpsdk.ToolCallStatusPending:
		return domain.ActivityStatusPending
	default:
		return domain.ActivityStatusRunning
	}
}

func toolTerminal(status acpsdk.ToolCallStatus) bool {
	return status == acpsdk.ToolCallStatusCompleted || status == acpsdk.ToolCallStatusFailed
}

func (c *conversation) emitDiffs(turnID string, content []acpsdk.ToolCallContent) {
	files := make([]ports.ChatDiffFile, 0)
	for _, item := range content {
		if item.Diff == nil {
			continue
		}
		status := "modified"
		deletions := 0
		if item.Diff.OldText == nil {
			status = "added"
		} else {
			deletions = lineCount(*item.Diff.OldText)
			if item.Diff.NewText == "" {
				status = "deleted"
			}
		}
		files = append(files, ports.ChatDiffFile{
			Path: item.Diff.Path, Status: status,
			Additions: lineCount(item.Diff.NewText), Deletions: deletions,
		})
	}
	if len(files) > 0 {
		c.emit(ports.ChatEvent{Kind: ports.ChatEventTurnDiff, ProviderTurnID: turnID, Diff: &ports.ChatTurnDiff{Files: files}})
	}
}

func lineCount(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}

func normalizePlan(entries []acpsdk.PlanEntry) *domain.ConversationPlan {
	plan := &domain.ConversationPlan{Steps: make([]domain.ConversationPlanStep, 0, len(entries))}
	for _, entry := range entries {
		status := domain.PlanStepPending
		switch entry.Status {
		case acpsdk.PlanEntryStatusInProgress:
			status = domain.PlanStepInProgress
		case acpsdk.PlanEntryStatusCompleted:
			status = domain.PlanStepCompleted
		}
		plan.Steps = append(plan.Steps, domain.ConversationPlanStep{Text: entry.Content, Status: status})
	}
	return plan
}

func pointerValue[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}
