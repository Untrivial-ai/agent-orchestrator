package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// replayConversation builds the smallest conversation that can run a history
// replay: the SDK delivery path only touches the capture, the maps, the log,
// and (for live assertions) the events channel.
func replayConversation() *conversation {
	return &conversation{
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		providerScopeID: "test-scope",
		events:          make(chan ports.ChatEvent, 64),
	}
}

func textBlock(text string) acpsdk.ContentBlock {
	return acpsdk.ContentBlock{Text: &acpsdk.ContentBlockText{Text: text}}
}

func replayUserChunk(sessionID, messageID, text string) acpsdk.SessionNotification {
	return acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(sessionID),
		Update: acpsdk.SessionUpdate{
			UserMessageChunk: &acpsdk.SessionUpdateUserMessageChunk{
				MessageId: &messageID,
				Content:   textBlock(text),
			},
		},
	}
}

func replayAgentChunk(sessionID, messageID, text string) acpsdk.SessionNotification {
	return acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(sessionID),
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				MessageId: &messageID,
				Content:   textBlock(text),
			},
		},
	}
}

// The SDK owns a bounded (1024) notification queue drained by sequential
// handler calls: normalizing a whole transcript inline overflows it and kills
// the connection. Replay notifications must be captured verbatim on delivery
// and normalized only by the post-load drain.
func TestACPReplayCaptureDefersNormalization(t *testing.T) {
	conv := replayConversation()
	conv.beginHistoryReplay("session-1")

	if err := conv.SessionUpdate(context.Background(), replayAgentChunk("session-1", "a1", "hi")); err != nil {
		t.Fatalf("SessionUpdate: %v", err)
	}
	if got := len(conv.history.events); got != 0 {
		t.Fatalf("normalized %d events on delivery, want 0 until drain", got)
	}
	if got := len(conv.replayUpdates); got != 1 {
		t.Fatalf("captured %d raw updates, want 1", got)
	}

	if err := conv.drainAndFinishReplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(conv.historyEvents) == 0 {
		t.Fatal("drain produced no history events")
	}
}

func TestACPReplayDrainReconstructsTurnsInOrder(t *testing.T) {
	conv := replayConversation()
	conv.beginHistoryReplay("session-1")

	updates := []acpsdk.SessionNotification{
		replayUserChunk("session-1", "u1", "hello"),
		replayAgentChunk("session-1", "a1", "hi"),
		replayAgentChunk("session-1", "a1", " there"),
		replayUserChunk("session-1", "u2", "bye"),
		replayAgentChunk("session-1", "a2", "later"),
	}
	for _, update := range updates {
		if err := conv.SessionUpdate(context.Background(), update); err != nil {
			t.Fatalf("SessionUpdate: %v", err)
		}
	}
	if err := conv.drainAndFinishReplay(context.Background()); err != nil {
		t.Fatal(err)
	}

	history, err := conv.ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	var kinds []ports.ChatEventKind
	var deltas, userTexts, completed []string
	for _, event := range history {
		kinds = append(kinds, event.Kind)
		if event.ProviderEventID == "" {
			t.Fatalf("replayed event missing stable identity: %+v", event)
		}
		switch event.Kind {
		case ports.ChatEventMessageDelta:
			deltas = append(deltas, event.Delta)
		case ports.ChatEventUserMessageCompleted:
			userTexts = append(userTexts, event.Text)
		case ports.ChatEventTurnCompleted:
			completed = append(completed, string(event.TurnState))
		}
	}

	wantKinds := []ports.ChatEventKind{
		ports.ChatEventTurnStarted,
		ports.ChatEventUserMessageCompleted,
		ports.ChatEventMessageDelta,
		ports.ChatEventMessageDelta,
		ports.ChatEventMessageCompleted,
		ports.ChatEventTurnCompleted,
		ports.ChatEventTurnStarted,
		ports.ChatEventUserMessageCompleted,
		ports.ChatEventMessageDelta,
		ports.ChatEventMessageCompleted,
		ports.ChatEventTurnCompleted,
	}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("event kinds = %v, want %v", kinds, wantKinds)
	}
	for i := range wantKinds {
		if kinds[i] != wantKinds[i] {
			t.Fatalf("event kinds = %v, want %v", kinds, wantKinds)
		}
	}
	if len(deltas) != 3 || deltas[0] != "hi" || deltas[1] != " there" || deltas[2] != "later" {
		t.Fatalf("deltas = %q, want [hi \" there\" later] in order", deltas)
	}
	if len(userTexts) != 2 || userTexts[0] != "hello" || userTexts[1] != "bye" {
		t.Fatalf("user texts = %q, want [hello bye]", userTexts)
	}
	if len(completed) != 2 || completed[0] != string(domain.TurnStateRecovered) || completed[1] != string(domain.TurnStateRecovered) {
		t.Fatalf("turn states = %q, want [recovered recovered]", completed)
	}
}

// Block normalization while a second burst arrives. Delivery must keep making
// progress, and the second batch must follow the first in the history snapshot.
func TestACPReplayAcceptsUpdatesDuringDrain(t *testing.T) {
	conv := replayConversation()
	conv.beginHistoryReplay("session-1")
	if err := conv.SessionUpdate(context.Background(), replayUserChunk("session-1", "u1", "hello")); err != nil {
		t.Fatal(err)
	}
	conv.mu.Lock()
	locked := true
	defer func() {
		if locked {
			conv.mu.Unlock()
		}
	}()
	drained := make(chan error, 1)
	go func() { drained <- conv.drainAndFinishReplay(context.Background()) }()
	deadline := time.After(5 * time.Second)
	for {
		conv.replayMu.Lock()
		taken := len(conv.replayUpdates) == 0
		conv.replayMu.Unlock()
		if taken {
			break
		}
		select {
		case <-deadline:
			t.Fatal("drain did not take initial batch")
		case <-time.After(time.Millisecond):
		}
	}
	delivered := make(chan error, 1)
	go func() {
		for i := 0; i < 2048; i++ {
			if err := conv.SessionUpdate(context.Background(), replayAgentChunk("session-1", "a1", fmt.Sprintf("%d,", i))); err != nil {
				delivered <- err
				return
			}
		}
		delivered <- nil
	}()
	select {
	case err := <-delivered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("notification delivery blocked on replay normalization")
	}
	conv.mu.Unlock()
	locked = false
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish")
	}
	history, err := conv.ReadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	index := 0
	for _, event := range history {
		if event.Kind == ports.ChatEventMessageDelta {
			if event.Delta != fmt.Sprintf("%d,", index) {
				t.Fatalf("delta %d out of order", index)
			}
			index++
		}
	}
	if index != 2048 {
		t.Fatalf("got %d deltas, want 2048", index)
	}
	if len(conv.events) != 0 {
		t.Fatal("replay leaked onto live channel")
	}
	if err := conv.SessionUpdate(context.Background(), replayAgentChunk("session-1", "live", "after replay")); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-conv.events:
		if event.Kind != ports.ChatEventMessageDelta || event.Delta != "after replay" {
			t.Fatalf("live event = %+v", event)
		}
	default:
		t.Fatal("post-replay update not delivered live")
	}
	after, err := conv.ReadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertReplayHistoryEqual(t, history, after)
}

func TestACPReplayLoadLargeHistoryThroughSDK(t *testing.T) {
	// More than the SDK queue capacity, with about 20 MiB of text. Distinct
	// messages keep this test about replay delivery, not quadratic concatenation.
	const count = 4096
	updates := make([]acpsdk.SessionUpdate, 0, count*2)
	for i := 0; i < count; i++ {
		updates = append(updates, replayUserChunk("session-1", fmt.Sprintf("u%d", i), "question").Update,
			replayAgentChunk("session-1", fmt.Sprintf("a%d", i), strings.Repeat("x", 5*1024)).Update)
	}
	conv := replayConversation()
	clientR, clientW := io.Pipe()
	agentR, agentW := io.Pipe()
	agent := &fakeAgent{loadUpdates: updates}
	agent.conn = acpsdk.NewAgentSideConnection(agent, agentW, clientR)
	conv.conn = acpsdk.NewClientSideConnection(conv, clientW, agentR)
	t.Cleanup(func() { _ = clientR.Close(); _ = clientW.Close(); _ = agentR.Close(); _ = agentW.Close() })
	loader := newRefreshableConversation(conv, acpsdk.LoadSessionRequest{SessionId: "session-1", Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conv.beginHistoryReplay("session-1")
	// Hold the normalization mutex until the SDK response barrier returns.
	// An inline handler cannot finish even its first update, fills the SDK
	// queue, and disconnects. Capturing must deliver the complete burst anyway.
	func() {
		conv.mu.Lock()
		defer conv.mu.Unlock()
		if _, err := conv.conn.LoadSession(ctx, loader.loadRequest); err != nil {
			t.Fatal(err)
		}
	}()
	if err := conv.drainAndFinishReplay(ctx); err != nil {
		t.Fatal(err)
	}
	history, err := conv.ReadHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, event := range history {
		if event.Kind == ports.ChatEventMessageCompleted {
			completed++
			if len(event.Text) != 5*1024 {
				t.Fatalf("message has %d bytes", len(event.Text))
			}
		}
	}
	if completed != count {
		t.Fatalf("got %d completed messages, want %d", completed, count)
	}
	if len(conv.events) != 0 {
		t.Fatal("replay leaked onto live channel")
	}
	select {
	case <-conv.conn.Done():
		t.Fatal("SDK connection closed")
	default:
	}
	// An identical refresh must retain every identity, for archive deduplication.
	if _, err := loader.loadHistory(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := conv.ReadHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertReplayHistoryEqual(t, history, again)
}

func TestACPReplayAbortDiscardsRawUpdates(t *testing.T) {
	conv := replayConversation()
	conv.beginHistoryReplay("session-1")
	if err := conv.SessionUpdate(context.Background(), replayAgentChunk("session-1", "old", "discard")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := conv.drainAndFinishReplay(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("drain error = %v", err)
	}
	conv.abortHistoryReplay()
	if conv.replaying || len(conv.replayUpdates) != 0 {
		t.Fatal("aborted replay retained updates")
	}
	if _, err := conv.ReadHistory(context.Background()); err == nil {
		t.Fatal("aborted history reported as loaded")
	}
	conv.beginHistoryReplay("session-1")
	if err := conv.SessionUpdate(context.Background(), replayAgentChunk("session-1", "new", "keep")); err != nil {
		t.Fatal(err)
	}
	if err := conv.drainAndFinishReplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	history, err := conv.ReadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range history {
		if event.Delta == "discard" {
			t.Fatal("aborted update imported on retry")
		}
	}
}

func assertReplayHistoryEqual(t *testing.T, want, got []ports.ChatEvent) {
	t.Helper()
	expected, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, actual) {
		t.Fatal("replay history or event identities changed")
	}
}
