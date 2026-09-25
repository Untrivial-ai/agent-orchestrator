//go:build darwin || linux

package unrealagent

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/coordinator"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/fireworks"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/ollama"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openai"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openaicodex"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openrouter"
	"github.com/unreallabsai/unreal-agent/harness/llm/responsesapi"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
	"github.com/unreallabsai/unreal-agent/harness/tool/bash"
	"github.com/unreallabsai/unreal-agent/harness/tool/viewimage"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	defaultProvider     = "openai"
	defaultOpenAIModel  = "gpt-6-astra"
	defaultSystemPrompt = `You are an AI agent running inside an isolated workspace.

Work directly in the workspace, use tools when useful, and report the completed result concisely.`
)

type providerClient interface {
	llm.Adapter
	Close() error
}

type activeTurnState struct {
	ProviderTurnID string `json:"providerTurnId"`
	MessageID      string `json:"messageId"`
}

type providerRuntime struct {
	ctx     context.Context
	cfg     providerConfig
	store   *localfile.Store
	client  providerClient
	model   string
	journal *eventJournal

	mu           sync.Mutex
	active       activeTurnState
	inputs       *inbox.Inbox
	done         chan error
	cancel       context.CancelFunc
	pendingTools map[string]domain.ActivityKind
	seenInputs   map[string]struct{}

	usage ports.ChatUsage
}

// RunProvider runs the embedded Unreal Agent harness over AO's private JSONL protocol.
func RunProvider(ctx context.Context, configPath string, input io.Reader, output io.Writer) error {
	cfg, err := readProviderConfig(configPath)
	if err != nil {
		return err
	}
	client, model, err := newProviderClient(cfg.Model, os.Getenv)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return runProvider(ctx, cfg, input, output, client, model)
}

func runProvider(ctx context.Context, cfg providerConfig, input io.Reader, output io.Writer, client providerClient, model string) error {
	sessionDirectory := filepath.Join(cfg.DataDir, "agent-runtime", string(domain.HarnessUnreal), "sessions")
	store, err := localfile.New(sessionDirectory)
	if err != nil {
		return fmt.Errorf("open Unreal Agent session store: %w", err)
	}
	journal, err := newEventJournal(providerStatePath(cfg, "journals"), output)
	if err != nil {
		return err
	}
	runtime := &providerRuntime{
		ctx: ctx, cfg: cfg, store: store, client: client, model: model, journal: journal,
		pendingTools: make(map[string]domain.ActivityKind),
	}
	if runtime.active, err = runtime.readActiveTurn(); err != nil {
		return err
	}
	if runtime.usage, err = runtime.readUsage(); err != nil {
		return err
	}
	if runtime.active.ProviderTurnID != "" && journal.hasTurnCompletion(runtime.active.ProviderTurnID) {
		if err := runtime.clearActiveTurn(); err != nil {
			return err
		}
	}
	observerID := store.AddObserver(runtime.observe)
	defer store.RemoveObserver(observerID)
	if err := runtime.startCoordinator(); err != nil {
		return err
	}
	defer runtime.stopCoordinator()
	if err := journal.writeControl(frame{Version: protocolVersion, Type: "ready"}); err != nil {
		return err
	}

	commands := make(chan command)
	decodeErrors := make(chan error, 1)
	go decodeCommands(input, commands, decodeErrors)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-runtime.done:
			if err == nil || errors.Is(err, context.Canceled) {
				return nil
			}
			active := runtime.activeTurnID()
			_ = runtime.emit(wireEvent{Kind: ports.ChatEventError, ProviderTurnID: active, Error: err.Error()})
			_ = runtime.completeTurn(active, domain.TurnStateFailed)
			return fmt.Errorf("run Unreal Agent coordinator: %w", err)
		case err := <-decodeErrors:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode Unreal Agent command: %w", err)
		case cmd := <-commands:
			err := runtime.handleCommand(cmd)
			if writeErr := journal.writeControl(resultFrame(cmd.RequestID, err)); writeErr != nil {
				return writeErr
			}
		}
	}
}

func decodeCommands(input io.Reader, commands chan<- command, result chan<- error) {
	decoder := json.NewDecoder(bufio.NewReader(input))
	for {
		var cmd command
		if err := decoder.Decode(&cmd); err != nil {
			result <- err
			return
		}
		commands <- cmd
	}
}

func resultFrame(requestID string, err error) frame {
	result := frame{Version: protocolVersion, Type: "result", RequestID: requestID}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func (runtime *providerRuntime) handleCommand(cmd command) error {
	if cmd.Version != protocolVersion || strings.TrimSpace(cmd.RequestID) == "" {
		return errors.New("invalid Unreal Agent command envelope")
	}
	switch cmd.Type {
	case "turn":
		return runtime.submitTurn(cmd)
	case "interrupt":
		return runtime.interrupt(cmd.ProviderTurnID)
	case "ack":
		return runtime.journal.ack(cmd.EventID)
	case "replay":
		return runtime.journal.replay()
	default:
		return fmt.Errorf("unsupported Unreal Agent command %q", cmd.Type)
	}
}

func (runtime *providerRuntime) submitTurn(cmd command) error {
	turnID, messageID := strings.TrimSpace(cmd.ProviderTurnID), strings.TrimSpace(cmd.MessageID)
	if turnID == "" || messageID == "" {
		return errors.New("unreal agent turn and message ids are required")
	}
	runtime.mu.Lock()
	if runtime.active.ProviderTurnID != "" && runtime.active.ProviderTurnID != turnID {
		runtime.mu.Unlock()
		return errors.New("unreal agent already has an active turn")
	}
	if runtime.active.ProviderTurnID == turnID && runtime.active.MessageID != messageID {
		runtime.mu.Unlock()
		return errors.New("unreal agent active turn has a different message id")
	}
	if _, seen := runtime.seenInputs[messageID]; seen {
		runtime.mu.Unlock()
		return nil
	}
	if runtime.active.ProviderTurnID == "" {
		runtime.active = activeTurnState{ProviderTurnID: turnID, MessageID: messageID}
	}
	inputs := runtime.inputs
	runtime.mu.Unlock()
	if err := runtime.writeActiveTurn(); err != nil {
		return errors.Join(err, runtime.clearActiveTurn())
	}
	if effort := strings.TrimSpace(cmd.Effort); effort != "" {
		payload, err := jsonv2.Marshal(inbox.ControlMessage{
			Mode:       inbox.UpdateSettings,
			Parameters: inbox.Settings{ReasoningEffort: reasoningEffort(effort)},
		})
		if err != nil {
			return errors.Join(fmt.Errorf("encode Unreal Agent settings: %w", err), runtime.clearActiveTurn())
		}
		if err := inputs.Submit(runtime.ctx, inbox.Input{ID: inbox.ID(uuid.NewString()), Kind: inbox.InputControl, Payload: jsontext.Value(payload)}); err != nil {
			return errors.Join(fmt.Errorf("submit Unreal Agent settings: %w", err), runtime.clearActiveTurn())
		}
	}
	payload, err := jsonv2.Marshal(cmd.Text)
	if err != nil {
		return errors.Join(fmt.Errorf("encode Unreal Agent message: %w", err), runtime.clearActiveTurn())
	}
	if err := inputs.Submit(runtime.ctx, inbox.Input{ID: inbox.ID(messageID), Kind: inbox.InputExternal, Payload: jsontext.Value(payload)}); err != nil {
		return errors.Join(fmt.Errorf("submit Unreal Agent message: %w", err), runtime.clearActiveTurn())
	}
	runtime.mu.Lock()
	runtime.seenInputs[messageID] = struct{}{}
	runtime.mu.Unlock()
	if err := runtime.emit(wireEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turnID}); err != nil {
		return err
	}
	return runtime.emit(wireEvent{Kind: ports.ChatEventControllerState, ProviderTurnID: turnID, ControllerState: ports.ChatControllerBusy})
}

func (runtime *providerRuntime) interrupt(requested string) error {
	active := runtime.activeTurnID()
	if active == "" {
		return nil
	}
	if requested = strings.TrimSpace(requested); requested != "" && requested != active {
		return fmt.Errorf("unreal agent active turn is %q, not %q", active, requested)
	}
	payload, err := jsonv2.Marshal(inbox.ControlMessage{Mode: inbox.StopHard, Reason: "Interrupted by user"})
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	inputs, done := runtime.inputs, runtime.done
	runtime.mu.Unlock()
	if err := inputs.Submit(runtime.ctx, inbox.Input{ID: inbox.ID(uuid.NewString()), Kind: inbox.InputControl, Payload: jsontext.Value(payload)}); err != nil {
		return err
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			// The provider loop is the owner of coordinator failures. Preserve a
			// failure observed while interrupt waits so the loop can project the
			// terminal error and clear the active turn instead of being stranded.
			done <- err
			return err
		}
	case <-runtime.ctx.Done():
		return runtime.ctx.Err()
	}
	completion := wireEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderConversationID: runtime.cfg.ProviderConversationID,
		ProviderTurnID: active, TurnState: domain.TurnStateInterrupted,
	}
	if err := runtime.journal.emitAfterPersist(completion, runtime.clearActiveTurn); err != nil {
		return err
	}
	return runtime.startCoordinator()
}

func (runtime *providerRuntime) startCoordinator() error {
	restored, err := runtime.store.Resume(runtime.ctx, session.ID(runtime.cfg.ProviderConversationID))
	if errors.Is(err, fs.ErrNotExist) {
		snapshot, createErr := runtime.store.Create(runtime.ctx, session.ID(runtime.cfg.ProviderConversationID))
		if createErr != nil {
			return fmt.Errorf("create Unreal Agent session: %w", createErr)
		}
		restored = sessionstore.ResumeState{Snapshot: snapshot}
	} else if err != nil {
		return fmt.Errorf("resume Unreal Agent session: %w", err)
	}
	seenInputs := make(map[string]struct{}, len(restored.ExternalInputIDs))
	for _, id := range restored.ExternalInputIDs {
		seenInputs[string(id)] = struct{}{}
	}
	runtime.mu.Lock()
	for id := range runtime.seenInputs {
		seenInputs[id] = struct{}{}
	}
	runtime.mu.Unlock()
	skills, skillErrors := tool.DiscoverSkills(filepath.Join(runtime.cfg.WorkspacePath, ".harness", "skills"))
	if err := errors.Join(skillErrors...); err != nil {
		return fmt.Errorf("discover Unreal Agent skills: %w", err)
	}
	toolNames := []string{tool.BashName, tool.ViewImageName}
	if len(skills) != 0 {
		toolNames = append(toolNames, tool.SkillUseName)
	}
	registry := tool.NewRegistry(tool.StaticTranslators{
		Bash: bash.New(bash.Config{
			Shell: shell(), Directory: runtime.cfg.WorkspacePath,
			BaseDirectory: filepath.Join(runtime.cfg.DataDir, "agent-runtime", string(domain.HarnessUnreal), "operations", runtime.cfg.ProviderConversationID),
		}),
		ViewImage: viewimage.New(viewimage.Config{Directory: runtime.cfg.WorkspacePath}),
	}, toolNames...)
	for _, skill := range skills {
		if _, err := registry.RegisterSkill(skill); err != nil {
			return fmt.Errorf("register Unreal Agent skill %q: %w", skill.Path, err)
		}
	}
	builder := contextbuilder.NewBuilder(registry.Skills()...)
	builder.SetModel(llm.Model{ID: runtime.model, ReasoningEffort: reasoningEffort(runtime.cfg.Effort)})
	systemPrompt := runtime.cfg.SystemPrompt
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = defaultSystemPrompt
	}
	builder.SetSystemPrompt(systemPrompt)
	for _, definition := range registry.StaticDefinitions() {
		builder.AddTool(definition.Tool)
	}
	runCtx, cancel := context.WithCancel(runtime.ctx)
	inputs, err := inbox.New(runCtx, restored.ExternalInputIDs)
	if err != nil {
		cancel()
		return fmt.Errorf("open Unreal Agent inbox: %w", err)
	}
	operations := operation.NewLocalOperationManager(runCtx)
	current := coordinator.New(coordinator.Dependencies{
		ToolHeartbeatInterval: 10 * time.Minute,
		SessionID:             session.ID(runtime.cfg.ProviderConversationID), Inbox: inputs,
		Restored: restored, Sessions: runtime.store, ContextBuilder: builder,
		LLM: runtime.client, Tools: registry, Operations: operations,
	})
	done := make(chan error, 1)
	runtime.mu.Lock()
	runtime.inputs, runtime.done, runtime.cancel, runtime.seenInputs = inputs, done, cancel, seenInputs
	runtime.mu.Unlock()
	go func() { done <- current.Run(runCtx) }()
	return nil
}

func (runtime *providerRuntime) stopCoordinator() {
	runtime.mu.Lock()
	cancel := runtime.cancel
	runtime.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (runtime *providerRuntime) observe(id session.ID, item sessionstore.Item) {
	if id != session.ID(runtime.cfg.ProviderConversationID) {
		return
	}
	turnID := runtime.activeTurnID()
	if turnID == "" {
		return
	}
	switch item.Kind {
	case sessionstore.ItemModelResponse:
		stored, ok := item.Data.(sessionstore.ModelResponse)
		if !ok {
			runtime.failTurn(turnID, errors.New("unreal agent stored an invalid model response"))
			return
		}
		runtime.observeModelResponse(turnID, stored)
	case sessionstore.ItemToolCallStatus:
		status, ok := item.Data.(sessionstore.ToolCallStatus)
		if !ok {
			runtime.failTurn(turnID, errors.New("unreal agent stored an invalid tool status"))
			return
		}
		runtime.observeToolStatus(turnID, status)
	}
}

func (runtime *providerRuntime) observeModelResponse(turnID string, stored sessionstore.ModelResponse) {
	response := stored.Response
	toolCalls := 0
	for index, item := range response.Output {
		itemID := strings.TrimSpace(item.ProviderID)
		if itemID == "" {
			itemID = fmt.Sprintf("%s:%d", response.ID, index)
		}
		switch item.Type {
		case llm.ItemMessage:
			message, ok := item.Data.(llm.Message)
			if !ok {
				runtime.failTurn(turnID, errors.New("unreal agent returned an invalid message"))
				return
			}
			if message.Role != llm.RoleAssistant {
				continue
			}
			if message.Text != "" {
				_ = runtime.emit(wireEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turnID, ProviderItemID: itemID, Delta: message.Text})
			}
			_ = runtime.emit(wireEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: turnID, ProviderItemID: itemID, Text: message.Text})
		case llm.ItemReasoning:
			reasoning, ok := item.Data.(llm.Reasoning)
			if !ok {
				runtime.failTurn(turnID, errors.New("unreal agent returned invalid reasoning"))
				return
			}
			text := strings.Join(reasoning.Summary, "\n")
			_ = runtime.emit(wireEvent{Kind: ports.ChatEventActivityStarted, ProviderTurnID: turnID, ProviderItemID: itemID, ActivityKind: domain.ActivityKindReasoning, ActivityStatus: domain.ActivityStatusRunning, Summary: "Reasoning"})
			if text != "" {
				_ = runtime.emit(wireEvent{Kind: ports.ChatEventReasoningDelta, ProviderTurnID: turnID, ProviderItemID: itemID, Delta: text})
			}
			_ = runtime.emit(wireEvent{Kind: ports.ChatEventActivityCompleted, ProviderTurnID: turnID, ProviderItemID: itemID, ActivityKind: domain.ActivityKindReasoning, ActivityStatus: domain.ActivityStatusCompleted, Text: text, Summary: "Reasoning"})
		case llm.ItemToolCall:
			toolCalls++
			call, ok := item.Data.(llm.ToolCall)
			if !ok {
				runtime.failTurn(turnID, errors.New("unreal agent returned an invalid tool call"))
				return
			}
			detail, _ := json.Marshal(map[string]string{"name": call.Name, "arguments": call.Arguments})
			kind := domain.ActivityKindMCPTool
			if call.Name == tool.BashName {
				kind = domain.ActivityKindCommand
			}
			runtime.mu.Lock()
			runtime.pendingTools[call.CallID] = kind
			runtime.mu.Unlock()
			_ = runtime.emit(wireEvent{Kind: ports.ChatEventActivityStarted, ProviderTurnID: turnID, ProviderItemID: call.CallID, ActivityKind: kind, ActivityStatus: domain.ActivityStatusRunning, Summary: call.Name, Detail: detail})
		}
	}
	usage, err := runtime.recordUsage(response.Usage)
	if err != nil {
		runtime.failTurn(turnID, fmt.Errorf("persist Unreal Agent usage: %w", err))
		return
	}
	_ = runtime.emit(wireEvent{Kind: ports.ChatEventUsage, ProviderTurnID: turnID, Usage: &usage})
	if response.Failure != nil {
		runtime.failTurn(turnID, errors.New(strings.TrimSpace(response.Failure.Code+": "+response.Failure.Message)))
	} else if toolCalls == 0 && !runtime.hasPendingTools() {
		_ = runtime.completeTurn(turnID, domain.TurnStateCompleted)
	}
}

func (runtime *providerRuntime) failTurn(turnID string, err error) {
	_ = runtime.emit(wireEvent{Kind: ports.ChatEventError, ProviderTurnID: turnID, Error: err.Error()})
	_ = runtime.completeTurn(turnID, domain.TurnStateFailed)
}

func (runtime *providerRuntime) observeToolStatus(turnID string, status sessionstore.ToolCallStatus) {
	if len(status.Status.WaitingFor) != 0 {
		return
	}
	state := domain.ActivityStatusCompleted
	if status.Status.Error != "" {
		state = domain.ActivityStatusFailed
	}
	runtime.mu.Lock()
	kind := runtime.pendingTools[status.CallID]
	delete(runtime.pendingTools, status.CallID)
	runtime.mu.Unlock()
	if kind == "" {
		kind = domain.ActivityKindMCPTool
	}
	detail, _ := json.Marshal(map[string]string{"error": status.Status.Error})
	_ = runtime.emit(wireEvent{Kind: ports.ChatEventActivityCompleted, ProviderTurnID: turnID, ProviderItemID: status.CallID, ActivityKind: kind, ActivityStatus: state, Detail: detail})
}

func (runtime *providerRuntime) completeTurn(turnID string, state domain.TurnState) error {
	if runtime.activeTurnID() != turnID {
		return nil
	}
	completion := wireEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderConversationID: runtime.cfg.ProviderConversationID,
		ProviderTurnID: turnID, TurnState: state,
	}
	if err := runtime.journal.emitAfterPersist(completion, runtime.clearActiveTurn); err != nil {
		return err
	}
	if err := runtime.emit(wireEvent{Kind: ports.ChatEventControllerState, ProviderTurnID: turnID, ControllerState: ports.ChatControllerReady}); err != nil {
		return err
	}
	return nil
}

func (runtime *providerRuntime) emit(event wireEvent) error {
	event.ProviderConversationID = runtime.cfg.ProviderConversationID
	return runtime.journal.emit(event)
}

func (runtime *providerRuntime) activeTurnID() string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.active.ProviderTurnID
}

func (runtime *providerRuntime) hasPendingTools() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return len(runtime.pendingTools) != 0
}

func (runtime *providerRuntime) activeTurnPath() string {
	return providerStatePath(runtime.cfg, "active")
}

func (runtime *providerRuntime) usagePath() string {
	return providerStatePath(runtime.cfg, "usage")
}

func providerStatePath(cfg providerConfig, kind string) string {
	return filepath.Join(
		cfg.DataDir, "agent-runtime", string(domain.HarnessUnreal), kind,
		cfg.AOSessionID, cfg.ProviderConversationID+".json",
	)
}

func (runtime *providerRuntime) readActiveTurn() (activeTurnState, error) {
	encoded, err := os.ReadFile(runtime.activeTurnPath()) //nolint:gosec // AO-owned path from validated session id
	if errors.Is(err, os.ErrNotExist) {
		return activeTurnState{}, nil
	}
	if err != nil {
		return activeTurnState{}, fmt.Errorf("read Unreal Agent active turn: %w", err)
	}
	var active activeTurnState
	if err := json.Unmarshal(encoded, &active); err != nil {
		return activeTurnState{}, fmt.Errorf("decode Unreal Agent active turn: %w", err)
	}
	return active, nil
}

func (runtime *providerRuntime) writeActiveTurn() error {
	runtime.mu.Lock()
	active := runtime.active
	runtime.mu.Unlock()
	return writeJSONAtomically(runtime.activeTurnPath(), active)
}

func (runtime *providerRuntime) clearActiveTurn() error {
	runtime.mu.Lock()
	runtime.active = activeTurnState{}
	clear(runtime.pendingTools)
	runtime.mu.Unlock()
	if err := os.Remove(runtime.activeTurnPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear Unreal Agent active turn: %w", err)
	}
	return nil
}

func (runtime *providerRuntime) readUsage() (ports.ChatUsage, error) {
	encoded, err := os.ReadFile(runtime.usagePath()) //nolint:gosec // AO-owned path from validated provider id
	if errors.Is(err, os.ErrNotExist) {
		return ports.ChatUsage{}, nil
	}
	if err != nil {
		return ports.ChatUsage{}, fmt.Errorf("read Unreal Agent usage: %w", err)
	}
	var usage ports.ChatUsage
	if err := json.Unmarshal(encoded, &usage); err != nil {
		return ports.ChatUsage{}, fmt.Errorf("decode Unreal Agent usage: %w", err)
	}
	return usage, nil
}

func (runtime *providerRuntime) recordUsage(delta llm.Usage) (ports.ChatUsage, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	previous := runtime.usage
	runtime.usage.InputTokens += delta.InputTokens
	runtime.usage.OutputTokens += delta.OutputTokens
	runtime.usage.CachedTokens += delta.CachedInputTokens
	runtime.usage.TotalTokens = runtime.usage.InputTokens + runtime.usage.OutputTokens
	runtime.usage.TotalsKnown = true
	if err := writeJSONAtomically(runtime.usagePath(), runtime.usage); err != nil {
		runtime.usage = previous
		return ports.ChatUsage{}, err
	}
	return runtime.usage, nil
}

func shell() string {
	if value := strings.TrimSpace(os.Getenv("SHELL")); value != "" {
		return value
	}
	return "/bin/sh"
}

func reasoningEffort(value string) llm.ReasoningEffort {
	switch normalizeEffort(value) {
	case "low":
		return llm.ReasoningEffortLow
	case "medium":
		return llm.ReasoningEffortMedium
	case "xhigh":
		return llm.ReasoningEffortXHigh
	case "max":
		return llm.ReasoningEffortMax
	default:
		return llm.ReasoningEffortHigh
	}
}

func newProviderClient(requestedModel string, getenv func(string) string) (providerClient, string, error) {
	provider := strings.TrimSpace(getenv("UNREAL_HARNESS_LLM_PROVIDER"))
	if provider == "" {
		provider = defaultProvider
	}
	model := strings.TrimSpace(requestedModel)
	if model == "" {
		model = strings.TrimSpace(getenv("UNREAL_HARNESS_LLM_MODEL"))
	}
	if model == "" && provider == defaultProvider {
		model = defaultOpenAIModel
	}
	if model == "" {
		return nil, "", errors.New("unreal agent model must be set in AO or UNREAL_HARNESS_LLM_MODEL")
	}
	maxAttempts := responsesapi.DefaultMaxAttempts
	if raw := strings.TrimSpace(getenv("UNREAL_HARNESS_LLM_MAX_ATTEMPTS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return nil, "", errors.New("UNREAL_HARNESS_LLM_MAX_ATTEMPTS must be a positive integer")
		}
		maxAttempts = parsed
	}
	baseURL := strings.TrimSpace(getenv("UNREAL_HARNESS_LLM_BASE_URL"))
	key := strings.TrimSpace(getenv("UNREAL_HARNESS_LLM_API_KEY"))
	var client providerClient
	var err error
	switch provider {
	case "openai":
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		if key == "" {
			key = strings.TrimSpace(getenv("OPENAI_API_KEY"))
		}
		client, err = openai.NewClient(openai.Config{APIKey: key, BaseURL: baseURL, MaxAttempts: &maxAttempts})
	case "openrouter":
		if baseURL == "" {
			baseURL = "https://openrouter.ai/api/v1"
		}
		if key == "" {
			key = strings.TrimSpace(getenv("OPENROUTER_API_KEY"))
		}
		client, err = openrouter.NewClient(openrouter.Config{APIKey: key, BaseURL: baseURL, MaxAttempts: &maxAttempts})
	case "fireworks":
		if baseURL == "" {
			baseURL = "https://api.fireworks.ai/inference/v1"
		}
		if key == "" {
			key = strings.TrimSpace(getenv("FIREWORKS_API_KEY"))
		}
		client, err = fireworks.NewClient(fireworks.Config{APIKey: key, BaseURL: baseURL, MaxAttempts: &maxAttempts})
	case "ollama":
		if baseURL == "" {
			baseURL = ollama.BaseURL
		}
		client, err = ollama.NewClient(ollama.Config{BaseURL: baseURL, MaxAttempts: &maxAttempts})
	case "openai-codex":
		config, configErr := openaicodex.EnvironmentConfig(getenv)
		if configErr != nil {
			return nil, "", configErr
		}
		if baseURL == "" {
			baseURL = openaicodex.BaseURL
		}
		config.BaseURL, config.MaxAttempts = baseURL, &maxAttempts
		client, err = openaicodex.NewClient(config)
	default:
		return nil, "", fmt.Errorf("unsupported Unreal Agent provider %q", provider)
	}
	if err != nil {
		return nil, "", fmt.Errorf("create Unreal Agent %s client: %w", provider, err)
	}
	return client, model, nil
}

type eventJournal struct {
	path   string
	output io.Writer
	mu     sync.Mutex
	frames []frame
}

func newEventJournal(path string, output io.Writer) (*eventJournal, error) {
	journal := &eventJournal{path: path, output: output}
	encoded, err := os.ReadFile(path) //nolint:gosec // AO-owned journal path
	if err == nil {
		if err := json.Unmarshal(encoded, &journal.frames); err != nil {
			return nil, fmt.Errorf("decode Unreal Agent event journal: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read Unreal Agent event journal: %w", err)
	}
	return journal, nil
}

func (journal *eventJournal) emit(event wireEvent) error {
	return journal.emitAfterPersist(event, nil)
}

func (journal *eventJournal) emitAfterPersist(event wireEvent, afterPersist func() error) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	current := frame{Version: protocolVersion, Type: "event", EventID: uuid.NewString(), Event: &event}
	journal.frames = append(journal.frames, current)
	if err := journal.persistLocked(); err != nil {
		journal.frames = journal.frames[:len(journal.frames)-1]
		return err
	}
	if afterPersist != nil {
		if err := afterPersist(); err != nil {
			return err
		}
	}
	return journal.writeLocked(current)
}

func (journal *eventJournal) ack(eventID string) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	for index := range journal.frames {
		if journal.frames[index].EventID == eventID {
			journal.frames = append(journal.frames[:index], journal.frames[index+1:]...)
			return journal.persistLocked()
		}
	}
	return nil
}

func (journal *eventJournal) replay() error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	for _, current := range journal.frames {
		if err := journal.writeLocked(current); err != nil {
			return err
		}
	}
	return nil
}

func (journal *eventJournal) hasTurnCompletion(turnID string) bool {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	for _, current := range journal.frames {
		if current.Event != nil && current.Event.Kind == ports.ChatEventTurnCompleted && current.Event.ProviderTurnID == turnID {
			return true
		}
	}
	return false
}

func (journal *eventJournal) writeControl(current frame) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.writeLocked(current)
}

func (journal *eventJournal) writeLocked(current frame) error {
	encoded, err := json.Marshal(current)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if _, err := journal.output.Write(encoded); err != nil {
		return fmt.Errorf("write Unreal Agent frame: %w", err)
	}
	return nil
}

func (journal *eventJournal) persistLocked() error {
	return writeJSONAtomically(journal.path, journal.frames)
}

func writeJSONAtomically(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary := path + ".tmp-" + uuid.NewString()
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
