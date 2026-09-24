//go:build darwin || linux

package unrealagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeProviderClient struct {
	request chan llm.Request
}

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(value []byte) (int, error) { return write(value) }

func (client *fakeProviderClient) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	client.request <- request
	return llm.Response{
		ID: "response-1",
		Output: []llm.Item{{
			ProviderID: "message-1",
			Type:       llm.ItemMessage,
			Data:       llm.Message{Role: llm.RoleAssistant, Text: "done"},
		}},
		Usage: llm.Usage{InputTokens: 3, OutputTokens: 1},
	}, nil
}

func (*fakeProviderClient) Close() error { return nil }

func TestProviderRunsPersistentTurnProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	client := &fakeProviderClient{request: make(chan llm.Request, 1)}
	cfg := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-session",
		DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
	}
	done := make(chan error, 1)
	go func() {
		done <- runProvider(ctx, cfg, inputReader, outputWriter, client, "test-model")
		_ = outputWriter.Close()
	}()

	decoder := json.NewDecoder(outputReader)
	var incoming frame
	if err := decoder.Decode(&incoming); err != nil || incoming.Type != "ready" {
		t.Fatalf("ready frame = (%#v, %v)", incoming, err)
	}
	if err := json.NewEncoder(inputWriter).Encode(command{
		Version: protocolVersion, Type: "turn", RequestID: "request-1",
		ProviderTurnID: "turn-1", MessageID: "message-1", Text: "hello",
	}); err != nil {
		t.Fatal(err)
	}

	seen := map[ports.ChatEventKind]bool{}
	for !seen[ports.ChatEventTurnCompleted] {
		incoming = frame{}
		if err := decoder.Decode(&incoming); err != nil {
			t.Fatal(err)
		}
		if incoming.Type == "event" && incoming.Event != nil {
			seen[incoming.Event.Kind] = true
			if incoming.EventID == "" {
				t.Fatalf("event %#v has no durable id", incoming.Event)
			}
		}
	}
	for _, kind := range []ports.ChatEventKind{
		ports.ChatEventTurnStarted,
		ports.ChatEventMessageDelta,
		ports.ChatEventMessageCompleted,
		ports.ChatEventUsage,
		ports.ChatEventTurnCompleted,
	} {
		if !seen[kind] {
			t.Errorf("missing %s event", kind)
		}
	}
	select {
	case request := <-client.request:
		if request.Model.ID != "test-model" || !requestContainsUserText(request, "hello") {
			t.Errorf("model request = %#v", request)
		}
	default:
		t.Error("model was not called")
	}
	if err := json.NewEncoder(inputWriter).Encode(command{
		Version: protocolVersion, Type: "turn", RequestID: "request-2",
		ProviderTurnID: "turn-1", MessageID: "message-1", Text: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	for incoming.RequestID != "request-2" {
		incoming = frame{}
		if err := decoder.Decode(&incoming); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case duplicate := <-client.request:
		t.Fatalf("duplicate message called model again: %#v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}

	_ = inputWriter.Close()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.DataDir, "agent-runtime", string(domain.HarnessUnreal), "sessions", "*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("persisted Unreal session files = (%v, %v)", matches, err)
	}
}

func requestContainsUserText(request llm.Request, text string) bool {
	for _, item := range request.Input {
		if item.Type == llm.ItemMessage {
			message := item.Data.(llm.Message)
			if message.Role == llm.RoleUser && message.Text == text {
				return true
			}
		}
	}
	return false
}

func TestEventJournalReplaysUntilAcknowledged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	firstOutput := new(bytes.Buffer)
	journal, err := newEventJournal(path, firstOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.emit(wireEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	var persisted frame
	if err := json.NewDecoder(firstOutput).Decode(&persisted); err != nil {
		t.Fatal(err)
	}

	replayOutput := new(bytes.Buffer)
	restarted, err := newEventJournal(path, replayOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.replay(); err != nil {
		t.Fatal(err)
	}
	var replayed frame
	if err := json.NewDecoder(replayOutput).Decode(&replayed); err != nil || replayed.EventID != persisted.EventID {
		t.Fatalf("replayed frame = (%#v, %v), want event %q", replayed, err, persisted.EventID)
	}
	if err := restarted.ack(replayed.EventID); err != nil {
		t.Fatal(err)
	}

	afterAck := new(bytes.Buffer)
	restarted, err = newEventJournal(path, afterAck)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.replay(); err != nil {
		t.Fatal(err)
	}
	if afterAck.Len() != 0 {
		t.Fatalf("acknowledged event replayed: %s", afterAck.String())
	}
}

func TestEventJournalRunsCompletionBeforePublishingEvent(t *testing.T) {
	completed := false
	journal, err := newEventJournal(filepath.Join(t.TempDir(), "events.json"), writerFunc(func(value []byte) (int, error) {
		if !completed {
			t.Error("event published before durable completion callback")
		}
		return len(value), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.emitAfterPersist(wireEvent{Kind: ports.ChatEventTurnCompleted}, func() error {
		completed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRecoveryStateIsScopedToProviderConversation(t *testing.T) {
	dataDir := t.TempDir()
	firstConfig := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-one",
		DataDir: dataDir, WorkspacePath: t.TempDir(),
	}
	secondConfig := firstConfig
	secondConfig.ProviderConversationID = "provider-two"

	firstRuntime := &providerRuntime{
		cfg: firstConfig,
		active: activeTurnState{
			ProviderTurnID: "stale-turn", MessageID: "stale-message",
		},
	}
	if err := firstRuntime.writeActiveTurn(); err != nil {
		t.Fatal(err)
	}
	secondRuntime := &providerRuntime{cfg: secondConfig}
	active, err := secondRuntime.readActiveTurn()
	if err != nil {
		t.Fatal(err)
	}
	if active.ProviderTurnID != "" || active.MessageID != "" {
		t.Fatalf("new provider conversation inherited active turn %#v", active)
	}

	firstJournal, err := newEventJournal(providerStatePath(firstConfig, "journals"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstJournal.emit(wireEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "stale-turn"}); err != nil {
		t.Fatal(err)
	}
	secondOutput := new(bytes.Buffer)
	secondJournal, err := newEventJournal(providerStatePath(secondConfig, "journals"), secondOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondJournal.replay(); err != nil {
		t.Fatal(err)
	}
	if secondOutput.Len() != 0 {
		t.Fatalf("new provider conversation replayed stale frames: %s", secondOutput.String())
	}
}

func TestInterruptPreservesCoordinatorFailureForProviderLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inputs, err := inbox.New(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorErr := errors.New("coordinator failed")
	done := make(chan error, 1)
	done <- coordinatorErr
	runtime := &providerRuntime{
		ctx: ctx,
		active: activeTurnState{
			ProviderTurnID: "turn-1", MessageID: "message-1",
		},
		inputs: inputs,
		done:   done,
	}

	if err := runtime.interrupt("turn-1"); !errors.Is(err, coordinatorErr) {
		t.Fatalf("interrupt error = %v, want %v", err, coordinatorErr)
	}
	select {
	case got := <-done:
		if !errors.Is(got, coordinatorErr) {
			t.Fatalf("preserved coordinator error = %v, want %v", got, coordinatorErr)
		}
	default:
		t.Fatal("interrupt consumed the coordinator failure")
	}
}

func TestProviderUsageSurvivesRestart(t *testing.T) {
	cfg := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-session",
		DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
	}
	first := &providerRuntime{cfg: cfg}
	usage, err := first.recordUsage(llm.Usage{InputTokens: 3, OutputTokens: 2, CachedInputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 3 || usage.OutputTokens != 2 || usage.CachedTokens != 1 || usage.TotalTokens != 5 {
		t.Fatalf("first usage = %#v", usage)
	}

	restarted := &providerRuntime{cfg: cfg}
	restarted.usage, err = restarted.readUsage()
	if err != nil {
		t.Fatal(err)
	}
	usage, err = restarted.recordUsage(llm.Usage{InputTokens: 4, OutputTokens: 1, CachedInputTokens: 2})
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.CachedTokens != 3 || usage.TotalTokens != 10 || !usage.TotalsKnown {
		t.Fatalf("restarted cumulative usage = %#v", usage)
	}
}
