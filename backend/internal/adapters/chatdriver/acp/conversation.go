package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	eventBuffer  = 4096
	approvalWait = 30 * time.Minute
)

var (
	errConversationClosed = errors.New("ACP conversation closed")
	errClientCapability   = errors.New("ACP client capability not advertised")

	// ErrACPSetterUnsupported is returned by applyTurnSettings when the agent
	// does not implement session/set_mode or session/set_config_option
	// (JSON-RPC -32601). Start() and Resume() tolerate it because the initial
	// model and permission mode may have been applied via launch-time flags;
	// SendTurn propagates it so a runtime change that cannot be applied is
	// surfaced to the user instead of silently running with the wrong mode.
	ErrACPSetterUnsupported = errors.New("ACP agent does not support runtime session configuration")
)

type preparedTurn struct {
	id              string
	clientMessageID string
	prompt          []acpsdk.ContentBlock
}

type interruptAttempt struct {
	turnID string
	done   chan struct{}
	err    error
}

type parkedPermission struct {
	options map[string]acpsdk.PermissionOption
	result  chan string
}

type parkedInput struct {
	request ports.ChatInputRequest
	result  chan ports.ChatInputResponse
}

type toolState struct {
	id             string
	title          string
	kind           acpsdk.ToolKind
	status         acpsdk.ToolCallStatus
	locations      []acpsdk.ToolCallLocation
	content        []acpsdk.ToolCallContent
	rawInput       any
	rawOutput      any
	meta           map[string]any
	terminalOutput string
}

type nestedMessageState struct {
	text     string
	parentID string
}

type conversation struct {
	conn *acpsdk.ClientSideConnection
	proc *process
	log  *slog.Logger

	mu             sync.Mutex
	sessionID      string
	capabilities   ports.ChatCapabilities
	prepared       *preparedTurn
	activeTurn     string
	settlingTurn   string
	turnCancel     context.CancelFunc
	interrupt      *interruptAttempt
	pending        map[string]*parkedPermission
	pendingInputs  map[string]*parkedInput
	messages       map[string]string
	thoughts       map[string]string
	nestedMessages map[string]nestedMessageState
	tools          map[string]*toolState
	configOptions  []ports.ChatConfigOption
	skills         []ports.ChatSkill
	skillsKnown    bool
	closed         bool
	modeFor        func(ports.PermissionMode) string
	optionsFor     func(ports.ChatTurnSettings) []SessionOption

	eventMu      sync.RWMutex
	events       chan ports.ChatEvent
	eventsClosed bool
	closeOnce    sync.Once

	historyMu     sync.Mutex
	history       *historyCapture
	historyEvents []ports.ChatEvent
	historyErr    error
	historyLoaded bool
}

var _ ports.ChatConversation = (*conversation)(nil)
var _ ports.ChatHistoryReader = (*conversation)(nil)
var _ ports.ChatDeferredTurnStarter = (*conversation)(nil)
var _ ports.ChatConfigOptionController = (*conversation)(nil)
var _ ports.ChatSkillLister = (*conversation)(nil)
var _ ports.ChatSteerer = (*conversation)(nil)
var _ ports.ChatInputResponder = (*conversation)(nil)
var _ acpsdk.Client = (*conversation)(nil)
var _ acpsdk.ClientExperimental = (*conversation)(nil)

func newConversation(proc *process, log *slog.Logger) *conversation {
	c := &conversation{
		proc:           proc,
		log:            log,
		pending:        make(map[string]*parkedPermission),
		pendingInputs:  make(map[string]*parkedInput),
		capabilities:   make(ports.ChatCapabilities),
		messages:       make(map[string]string),
		thoughts:       make(map[string]string),
		nestedMessages: make(map[string]nestedMessageState),
		tools:          make(map[string]*toolState),
		events:         make(chan ports.ChatEvent, eventBuffer),
	}
	c.conn = acpsdk.NewClientSideConnection(c, proc.stdin, proc.stdout)
	c.conn.SetLogger(log)
	go c.watchConnection()
	return c
}

func (c *conversation) start(
	sessionID string,
	capabilities ports.ChatCapabilities,
	modeFor func(ports.PermissionMode) string,
	optionsFor func(ports.ChatTurnSettings) []SessionOption,
	configOptions []acpsdk.SessionConfigOption,
) {
	c.mu.Lock()
	c.sessionID = sessionID
	c.capabilities = capabilities
	// Preserve config options received via session/update during session/new.
	// An agent may send config_option_update before start() runs; only overwrite
	// the catalog when the response actually carries one, so an early update is
	// not lost to an empty response snapshot.
	if len(configOptions) > 0 {
		c.configOptions = normalizeConfigOptions(configOptions)
	}
	if len(c.configOptions) > 0 {
		c.capabilities[ports.ChatCapabilityConfigOptions] = true
	}
	if c.skillsKnown {
		c.capabilities[ports.ChatCapabilitySkills] = true
	}
	c.modeFor = modeFor
	c.optionsFor = optionsFor
	c.mu.Unlock()
	c.emit(ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady})
}

func (c *conversation) ProviderConversationID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

func (c *conversation) Capabilities() ports.ChatCapabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneCapabilities(c.capabilities)
}

func (c *conversation) Events() <-chan ports.ChatEvent { return c.events }

// SendTurn prepares the long-lived ACP prompt request. AO's controller starts it
// through StartDeferredTurn only after the provider turn id is durable.
func (c *conversation) SendTurn(ctx context.Context, msg ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	if err := ctx.Err(); err != nil {
		return ports.ChatTurnRef{}, err
	}
	prompt, err := c.promptContent(msg)
	if err != nil {
		return ports.ChatTurnRef{}, err
	}
	c.mu.Lock()
	busy := c.closed || c.prepared != nil || c.activeTurn != ""
	c.mu.Unlock()
	if busy {
		return ports.ChatTurnRef{}, errors.New("ACP conversation already has a turn in flight")
	}
	if err := c.applyTurnSettings(ctx, msg.Settings); err != nil {
		return ports.ChatTurnRef{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ports.ChatTurnRef{}, errConversationClosed
	}
	if c.prepared != nil || c.activeTurn != "" {
		return ports.ChatTurnRef{}, errors.New("ACP conversation already has a turn in flight")
	}
	id := uuid.NewString()
	c.prepared = &preparedTurn{id: id, clientMessageID: msg.ClientMessageID, prompt: prompt}
	return ports.ChatTurnRef{ProviderTurnID: id}, nil
}

func (c *conversation) DiscardDeferredTurn(providerTurnID string) {
	c.mu.Lock()
	if c.prepared != nil && c.prepared.id == providerTurnID {
		c.prepared = nil
	}
	c.mu.Unlock()
}

func (c *conversation) applyTurnSettings(ctx context.Context, settings ports.ChatTurnSettings) error {
	c.mu.Lock()
	sessionID := c.sessionID
	modeFor := c.modeFor
	optionsFor := c.optionsFor
	c.mu.Unlock()
	if sessionID == "" {
		return errors.New("ACP session is not open")
	}
	if modeFor != nil {
		if mode := modeFor(settings.Approval); mode != "" {
			if _, err := c.conn.SetSessionMode(ctx, acpsdk.SetSessionModeRequest{
				SessionId: acpsdk.SessionId(sessionID), ModeId: acpsdk.SessionModeId(mode),
			}); err != nil {
				if isACPMethodNotFound(err) {
					return fmt.Errorf("%w: session/set_mode %q", ErrACPSetterUnsupported, mode)
				}
				return fmt.Errorf("set ACP session mode %q: %w", mode, err)
			}
		}
	}
	if optionsFor != nil {
		for _, option := range optionsFor(settings) {
			if option.ID == "" || option.Value == "" {
				continue
			}
			resp, err := c.conn.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{
				ValueId: &acpsdk.SetSessionConfigOptionValueId{
					SessionId: acpsdk.SessionId(sessionID), ConfigId: acpsdk.SessionConfigId(option.ID),
					Value: acpsdk.SessionConfigValueId(option.Value),
				},
			})
			if err != nil {
				if isACPMethodNotFound(err) {
					return fmt.Errorf("%w: session/set_config_option %q", ErrACPSetterUnsupported, option.ID)
				}
				return fmt.Errorf("set ACP session option %q: %w", option.ID, err)
			}
			c.replaceConfigOptions(resp.ConfigOptions)
		}
	}
	return nil
}

func (c *conversation) StartDeferredTurn(providerTurnID string) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errConversationClosed
	}
	if c.prepared == nil || c.prepared.id != providerTurnID {
		c.mu.Unlock()
		return errors.New("ACP prepared turn not found")
	}
	turn := *c.prepared
	c.prepared = nil
	c.activeTurn = turn.id
	c.settlingTurn = ""
	turnCtx, cancel := context.WithCancel(context.Background())
	c.turnCancel = cancel
	sessionID := c.sessionID
	c.messages = make(map[string]string)
	c.thoughts = make(map[string]string)
	c.nestedMessages = make(map[string]nestedMessageState)
	c.tools = make(map[string]*toolState)
	c.mu.Unlock()

	go c.runTurn(turnCtx, sessionID, turn)
	return nil
}

func (c *conversation) runTurn(ctx context.Context, sessionID string, turn preparedTurn) {
	c.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.id})
	c.emit(ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerBusy})
	// ACP message ids are opaque idempotency/correlation keys. Preserve AO's
	// durable client id when possible so an agent that echoes it from session/load
	// can be reconciled without provider-specific knowledge. Some agents (notably
	// claude-agent-acp) assign their own persisted user uuid; the service's history
	// reconciliation is the fallback for those conforming-but-different agents.
	messageID := strings.TrimSpace(turn.clientMessageID)
	if messageID == "" {
		messageID = uuid.NewString()
	}
	resp, err := c.conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(sessionID),
		MessageId: &messageID,
		Prompt:    turn.prompt,
	})

	c.mu.Lock()
	c.settlingTurn = turn.id
	interrupt := c.interrupt
	c.mu.Unlock()
	c.settleOpenItems(turn.id)
	interruptedLocally := false
	if interrupt != nil && interrupt.turnID == turn.id {
		// ACP cancellation and Prompt completion can race. Wait for the sender's
		// definitive result before classifying the turn so a failed notification
		// cannot look interrupted and an accepted one cannot look failed.
		<-interrupt.done
		interruptedLocally = interrupt.err == nil
	}
	var state domain.TurnState
	if err != nil {
		if interruptedLocally || errors.Is(err, context.Canceled) {
			state = domain.TurnStateInterrupted
		} else {
			state = domain.TurnStateFailed
			if isACPAuthRequired(err) {
				c.emit(ports.ChatEvent{Kind: ports.ChatEventAccountChanged, Account: &ports.ChatAccount{
					ReauthRequired: true, ReauthReason: "Provider authentication expired",
				}})
				err = normalizeACPError("ACP session/prompt", err)
			}
			c.emit(ports.ChatEvent{Kind: ports.ChatEventError, ProviderTurnID: turn.id, Err: err})
		}
	} else {
		state = turnState(resp.StopReason)
		if resp.Usage != nil {
			cached := 0
			if resp.Usage.CachedReadTokens != nil {
				cached += *resp.Usage.CachedReadTokens
			}
			if resp.Usage.CachedWriteTokens != nil {
				cached += *resp.Usage.CachedWriteTokens
			}
			c.emit(ports.ChatEvent{Kind: ports.ChatEventUsage, Usage: &ports.ChatUsage{
				InputTokens: int64(resp.Usage.InputTokens), OutputTokens: int64(resp.Usage.OutputTokens),
				CachedTokens: int64(cached), TotalTokens: int64(resp.Usage.TotalTokens),
				TotalsKnown: true,
			}})
		}
	}
	c.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.id, TurnState: state})
	c.emit(ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady})

	c.mu.Lock()
	if c.activeTurn == turn.id {
		c.activeTurn = ""
		c.settlingTurn = ""
		c.turnCancel = nil
		if c.interrupt == interrupt {
			c.interrupt = nil
		}
	}
	c.mu.Unlock()
}

func turnState(reason acpsdk.StopReason) domain.TurnState {
	switch reason {
	case acpsdk.StopReasonCancelled:
		return domain.TurnStateInterrupted
	case acpsdk.StopReasonEndTurn:
		return domain.TurnStateCompleted
	default:
		return domain.TurnStateFailed
	}
}

func (c *conversation) Interrupt(ctx context.Context, providerTurnID string) error {
	c.mu.Lock()
	active := c.activeTurn
	sessionID := c.sessionID
	turnCancel := c.turnCancel
	if active == "" || c.settlingTurn == active || (providerTurnID != "" && providerTurnID != active) {
		c.mu.Unlock()
		return ports.ErrChatNoActiveTurn
	}
	if c.interrupt != nil {
		c.mu.Unlock()
		return fmt.Errorf("ACP session/cancel is already in progress")
	}
	attempt := &interruptAttempt{turnID: active, done: make(chan struct{})}
	c.interrupt = attempt
	c.mu.Unlock()

	err := c.conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: acpsdk.SessionId(sessionID)})
	c.mu.Lock()
	attempt.err = err
	close(attempt.done)
	if err != nil && c.interrupt == attempt {
		// The notification never crossed the connection, so keep the local
		// Prompt alive and allow a later retry to make a fresh attempt.
		c.interrupt = nil
	}
	c.mu.Unlock()
	if err != nil {
		return fmt.Errorf("ACP session/cancel: %w", err)
	}
	// session/cancel is a notification: a conforming agent may accept it without
	// completing the outstanding Prompt RPC. Once the notification is accepted,
	// cancel AO's local request too so Stop cannot report success while the
	// controller remains busy forever. Do this only after a successful write;
	// otherwise the caller must see that cancellation was not delivered.
	if turnCancel != nil {
		turnCancel()
	}
	return nil
}

func (c *conversation) ResolveRequest(
	ctx context.Context,
	requestID string,
	decision ports.ChatDecision,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	request, ok := c.pending[requestID]
	if !ok {
		c.mu.Unlock()
		return ports.ErrChatRequestNotPending
	}
	option, offered := request.options[decision.ID]
	if !offered {
		c.mu.Unlock()
		return ports.ErrChatDecisionNotOffered
	}
	if len(decision.Raw) > 0 {
		offeredRaw, _ := json.Marshal(option)
		if !bytes.Equal(bytes.TrimSpace(decision.Raw), bytes.TrimSpace(offeredRaw)) {
			c.mu.Unlock()
			return ports.ErrChatDecisionNotOffered
		}
	}
	delete(c.pending, requestID)
	c.mu.Unlock()

	select {
	case request.result <- decision.ID:
		c.emit(ports.ChatEvent{Kind: ports.ChatEventApprovalResolved, RequestID: requestID})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *conversation) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		cancel := c.turnCancel
		sessionID := c.sessionID
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		c.failPendingPermissions()
		c.failPendingInputs()
		if sessionID != "" {
			closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
			_, _ = c.conn.CloseSession(closeCtx, acpsdk.CloseSessionRequest{SessionId: acpsdk.SessionId(sessionID)})
			cancelClose()
		}
		closeErr = c.proc.stop()
	})
	return closeErr
}

func (c *conversation) watchConnection() {
	<-c.conn.Done()
	c.failPendingPermissions()
	c.failPendingInputs()
	c.emit(ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerStopped})
	c.eventMu.Lock()
	if !c.eventsClosed {
		c.eventsClosed = true
		close(c.events)
	}
	c.eventMu.Unlock()
}

func (c *conversation) emit(event ports.ChatEvent) {
	if c.captureHistoryEvent(event) {
		return
	}
	c.eventMu.RLock()
	defer c.eventMu.RUnlock()
	if c.eventsClosed {
		return
	}
	select {
	case c.events <- event:
		return
	default:
	}
	if event.Kind == ports.ChatEventMessageDelta || event.Kind == ports.ChatEventReasoningDelta {
		c.log.Warn("dropped ACP chat delta: consumer behind", "kind", event.Kind, "item", event.ProviderItemID)
		return
	}
	select {
	case c.events <- event:
	case <-time.After(5 * time.Second):
		c.log.Error("dropped ACP lifecycle event: consumer stalled", "kind", event.Kind)
	}
}

func (c *conversation) settleOpenItems(turnID string) {
	c.mu.Lock()
	messages := c.messages
	thoughts := c.thoughts
	nestedMessages := c.nestedMessages
	tools := c.tools
	c.mu.Unlock()
	messageIDs := sortedKeys(messages)
	for _, id := range messageIDs {
		text := messages[id]
		c.emit(ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: turnID, ProviderItemID: id, Text: text})
	}
	thoughtIDs := sortedKeys(thoughts)
	for _, id := range thoughtIDs {
		text := thoughts[id]
		c.emit(ports.ChatEvent{Kind: ports.ChatEventActivityCompleted, ProviderTurnID: turnID,
			ProviderItemID: id, ActivityKind: domain.ActivityKindReasoning,
			ActivityStatus: domain.ActivityStatusCompleted, Summary: "Reasoning", Text: text})
	}
	nestedIDs := sortedKeys(nestedMessages)
	for _, id := range nestedIDs {
		item := nestedMessages[id]
		detail, _ := json.Marshal(map[string]any{"parentProviderItemId": item.parentID, "nestedAgent": true})
		c.emit(ports.ChatEvent{Kind: ports.ChatEventActivityCompleted, ProviderTurnID: turnID,
			ProviderItemID: id, ActivityKind: domain.ActivityKindMCPTool,
			ActivityStatus: domain.ActivityStatusCompleted, Summary: "Subagent response",
			Text: item.text, Detail: detail})
	}
	toolIDs := sortedKeys(tools)
	for _, id := range toolIDs {
		tool := tools[id]
		if tool.status == acpsdk.ToolCallStatusPending || tool.status == acpsdk.ToolCallStatusInProgress || tool.status == "" {
			snapshot := *tool
			snapshot.status = acpsdk.ToolCallStatusFailed
			c.emit(c.toolEvent(turnID, &snapshot, true))
		}
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (c *conversation) failPendingPermissions() {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]*parkedPermission)
	c.mu.Unlock()
	for _, request := range pending {
		select {
		case request.result <- "":
		default:
		}
	}
}

func (c *conversation) failPendingInputs() {
	c.mu.Lock()
	pending := c.pendingInputs
	c.pendingInputs = make(map[string]*parkedInput)
	c.mu.Unlock()
	for _, request := range pending {
		select {
		case request.result <- ports.ChatInputResponse{Action: ports.ChatInputActionCancel}:
		default:
		}
	}
}

func (c *conversation) promptContent(message ports.ChatUserMessage) ([]acpsdk.ContentBlock, error) {
	c.mu.Lock()
	caps := cloneCapabilities(c.capabilities)
	c.mu.Unlock()
	prompt := make([]acpsdk.ContentBlock, 0, 1+len(message.Content))
	if strings.TrimSpace(message.Text) != "" {
		prompt = append(prompt, acpsdk.TextBlock(message.Text))
	}
	for _, item := range message.Content {
		switch item.Type {
		case "image":
			if !caps.Has(ports.ChatCapabilityImages) {
				return nil, fmt.Errorf("ACP agent does not support image prompts")
			}
			if item.Data == "" || item.MIMEType == "" {
				return nil, errors.New("image content requires data and MIME type")
			}
			prompt = append(prompt, acpsdk.ImageBlock(item.Data, item.MIMEType))
		case "resource_link":
			if item.URI == "" || item.Name == "" {
				return nil, errors.New("resource link requires a URI and name")
			}
			prompt = append(prompt, acpsdk.ResourceLinkBlock(item.Name, item.URI))
		case "resource":
			if !caps.Has(ports.ChatCapabilityEmbeddedContext) {
				return nil, fmt.Errorf("ACP agent does not support embedded context")
			}
			if item.URI == "" {
				return nil, errors.New("embedded resource requires a URI")
			}
			mimeType := (*string)(nil)
			if item.MIMEType != "" {
				mimeType = pointer(item.MIMEType)
			}
			prompt = append(prompt, acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
				TextResourceContents: &acpsdk.TextResourceContents{
					Uri: item.URI, Text: item.Text, MimeType: mimeType,
				},
			}))
		default:
			return nil, fmt.Errorf("unsupported chat content type %q", item.Type)
		}
	}
	if len(prompt) == 0 {
		return nil, errors.New("chat message has no content")
	}
	return prompt, nil
}
