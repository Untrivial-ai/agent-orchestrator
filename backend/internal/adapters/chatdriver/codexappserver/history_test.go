package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type testDuplexWriteCloser struct {
	io.Writer
	close func() error
}

func (w testDuplexWriteCloser) Close() error { return w.close() }

// Every scripted reply below is a SINGLE line. readFrame is newline-delimited, so a
// pretty-printed reply is read as a truncated frame and the client waits forever.

// threadWithTurns is a thread/read result carrying an ordered turn list.
const threadWithTurns = `{"thread":{"id":"thread-1","turns":[` +
	`{"id":"turn-a","status":"completed"},` +
	`{"id":"turn-b","status":"completed"},` +
	`{"id":"turn-c","status":"completed"}]}}`

const threadWithRenderedHistory = `{"thread":{"id":"thread-1","turns":[` +
	`{"id":"turn-a","status":"completed","items":[` +
	`{"type":"userMessage","id":"user-1","clientId":"client-1","content":[{"type":"text","text":"Inspect the repository"}]},` +
	`{"type":"commandExecution","id":"cmd-1","command":"/bin/zsh -lc 'pwd'","cwd":"/tmp/ws","aggregatedOutput":"/tmp/ws\n","exitCode":0,"durationMs":12,"status":"completed"},` +
	`{"type":"agentMessage","id":"answer-1","text":"The repository is ready."}` +
	`]}]}}`

func openConversation(t *testing.T) (*conversation, *scriptedServer) {
	t.Helper()
	d, srv := newTestDriver(t)
	conv, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = conv.Close() })
	return conv.(*conversation), srv
}

func openLiveRecoveryConversation(
	t *testing.T,
	threadReadResult string,
) (*conversation, *scriptedServer) {
	t.Helper()
	d, srv := newTestDriver(t)
	proc, err := d.spawn(context.Background(), "codex", "/tmp/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.reply("thread/read", threadReadResult)
	d.persistent = true
	d.connectHost = func(context.Context, persistenthost.Config) (*persistenthost.Transport, error) {
		return &persistenthost.Transport{
			// A real host transport is one net.Conn, whose Close also wakes the
			// stdout reader. Mirror that duplex-close behavior across these two
			// independent in-memory pipes so abort/retry tests exercise real detach.
			Stdin: testDuplexWriteCloser{
				Writer: proc.stdin,
				close: func() error {
					return errors.Join(proc.stdin.Close(), srv.toClient.Close())
				},
			},
			Stdout: proc.stdout, Reconnected: true,
		}, nil
	}
	opened, err := d.Resume(context.Background(), ports.ChatResumeConfig{
		SessionID: "ao-recovery", ProviderConversationID: "thread-1",
		DataDir: t.TempDir(), WorkspacePath: "/tmp/ws",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	conv := opened.(*conversation)
	t.Cleanup(func() {
		conv.CommitLiveRecovery()
		_ = conv.Close()
	})
	return conv, srv
}

func TestRecoverLiveUsesPaginatedHistoryAndAMetadataOnlyBoundary(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}`)
	srv.reply(methodThreadTurnsList, `{"data":[`+
		`{"id":"turn-1","status":"completed","items":[],"itemsView":"full"}`+
		`]}`)

	snapshot, err := conv.RecoverLive(context.Background())
	if err != nil {
		t.Fatalf("RecoverLive: %v", err)
	}
	if len(snapshot.HistoryEvents) != 2 ||
		snapshot.HistoryEvents[0].Kind != ports.ChatEventTurnStarted ||
		snapshot.HistoryEvents[1].Kind != ports.ChatEventTurnCompleted {
		t.Fatalf("history events = %#v, want paginated completed turn", snapshot.HistoryEvents)
	}

	list := srv.awaitFrame(func(f frame) bool { return f.Method == methodThreadTurnsList })
	var listParams struct {
		ThreadID      string `json:"threadId"`
		Limit         uint32 `json:"limit"`
		SortDirection string `json:"sortDirection"`
		ItemsView     string `json:"itemsView"`
	}
	if err := json.Unmarshal(list.Params, &listParams); err != nil {
		t.Fatalf("thread/turns/list params: %v", err)
	}
	if listParams.ThreadID != "thread-1" ||
		listParams.Limit != historyTurnPageSize ||
		listParams.SortDirection != "asc" ||
		listParams.ItemsView != "full" {
		t.Errorf("thread/turns/list params = %+v, want full oldest-first history", listParams)
	}

	read := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/read" })
	var readParams map[string]json.RawMessage
	if err := json.Unmarshal(read.Params, &readParams); err != nil {
		t.Fatalf("thread/read params: %v", err)
	}
	if _, included := readParams["includeTurns"]; included {
		t.Error("modern recovery boundary requested deprecated thread/read turn hydration")
	}
}

func TestRecoverLiveAcceptsAnIdleThreadBeforeItsFirstUserMessage(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}`)
	srv.replyError(methodThreadTurnsList, -32600,
		"thread thread-1 is not materialized yet; thread/turns/list is unavailable before first user message")

	snapshot, err := conv.RecoverLive(context.Background())
	if err != nil {
		t.Fatalf("RecoverLive: %v", err)
	}
	if snapshot.ActivityState != domain.ActivityIdle || len(snapshot.HistoryEvents) != 0 {
		t.Fatalf("snapshot = %#v, want idle empty unmaterialized thread", snapshot)
	}
	read := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/read" })
	var readParams map[string]json.RawMessage
	if err := json.Unmarshal(read.Params, &readParams); err != nil {
		t.Fatalf("thread/read params: %v", err)
	}
	if _, included := readParams["includeTurns"]; included {
		t.Error("unmaterialized recovery fell back to the broken includeTurns path")
	}
}

func TestRecoverLiveRejectsUnmaterializedHistoryForAnActiveThread(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"active","activeFlags":[]},"turns":[]}}`)
	srv.replyError(methodThreadTurnsList, -32600,
		"thread thread-1 is not materialized yet; thread/turns/list is unavailable before first user message")

	_, err := conv.RecoverLive(context.Background())
	if !errors.Is(err, ports.ErrChatRecoveryInconclusive) ||
		!strings.Contains(err.Error(), "unmaterialized") {
		t.Fatalf("RecoverLive error = %v, want active/unmaterialized contradiction", err)
	}
}

func TestReadHistoryTreatsTheExactUnmaterializedRefusalAsEmpty(t *testing.T) {
	conv, srv := openConversation(t)
	srv.replyError(methodThreadTurnsList, -32600,
		"thread thread-1 is not materialized yet; thread/turns/list is unavailable before first user message")

	events, err := conv.ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want no history before first user message", events)
	}
	if srv.sentMethod("thread/read") {
		t.Fatal("unmaterialized history used deprecated includeTurns fallback")
	}
}

func TestReadHistoryFollowsEveryOpaqueTurnCursor(t *testing.T) {
	conv, srv := openConversation(t)
	srv.replySequence(methodThreadTurnsList,
		`{"data":[{"id":"turn-a","status":"completed","items":[],"itemsView":"full"}],"nextCursor":"cursor-a"}`,
		`{"data":[{"id":"turn-b","status":"completed","items":[],"itemsView":"full"}]}`,
	)

	events, err := conv.ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(events) != 4 ||
		events[0].ProviderTurnID != "turn-a" ||
		events[2].ProviderTurnID != "turn-b" {
		t.Fatalf("events = %#v, want both ordered pages", events)
	}
	if srv.sentMethod("thread/read") {
		t.Fatal("paginated history unexpectedly fell back to thread/read")
	}

	srv.mu.Lock()
	var pages []frame
	for _, seen := range srv.seen {
		if seen.Method == methodThreadTurnsList {
			pages = append(pages, seen)
		}
	}
	srv.mu.Unlock()
	if len(pages) != 2 {
		t.Fatalf("thread/turns/list requests = %d, want two", len(pages))
	}
	var second struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal(pages[1].Params, &second); err != nil {
		t.Fatalf("second page params: %v", err)
	}
	if second.Cursor != "cursor-a" {
		t.Errorf("second page cursor = %q, want cursor-a", second.Cursor)
	}
}

func TestReadHistoryRejectsTruncatedPaginatedItems(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply(methodThreadTurnsList, `{"data":[`+
		`{"id":"turn-a","status":"completed","items":[],"itemsView":"summary"}`+
		`]}`)

	_, err := conv.ReadHistory(context.Background())
	if err == nil || !strings.Contains(err.Error(), "want full") {
		t.Fatalf("ReadHistory error = %v, want truncated-items rejection", err)
	}
	if srv.sentMethod("thread/read") {
		t.Fatal("supported but truncated pagination silently fell back to legacy history")
	}
}

func TestReadHistoryRejectsAPaginationCursorLoop(t *testing.T) {
	conv, srv := openConversation(t)
	srv.replySequence(methodThreadTurnsList,
		`{"data":[{"id":"turn-a","status":"completed","items":[],"itemsView":"full"}],"nextCursor":"again"}`,
		`{"data":[{"id":"turn-b","status":"completed","items":[],"itemsView":"full"}],"nextCursor":"again"}`,
	)

	_, err := conv.ReadHistory(context.Background())
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("ReadHistory error = %v, want cursor-loop rejection", err)
	}
}

func TestRecoverLiveReturnsIdleAfterDetachedTurnCompletion(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[`+
			`{"id":"turn-1","status":"completed","items":[]}]}}`)
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1",` +
		`"turn":{"id":"turn-1","status":"completed","items":[]}}}`)

	snapshot, err := conv.RecoverLive(context.Background())
	if err != nil {
		t.Fatalf("RecoverLive: %v", err)
	}
	if snapshot.ActivityState != domain.ActivityIdle {
		t.Fatalf("activity = %q, want idle", snapshot.ActivityState)
	}
	if len(snapshot.ReplayEvents) != 1 || snapshot.ReplayEvents[0].Kind != ports.ChatEventTurnCompleted {
		t.Fatalf("replay events = %#v, want detached completion", snapshot.ReplayEvents)
	}
	if len(snapshot.HistoryEvents) != 2 ||
		snapshot.HistoryEvents[0].Kind != ports.ChatEventTurnStarted ||
		snapshot.HistoryEvents[1].Kind != ports.ChatEventTurnCompleted {
		t.Fatalf("history events = %#v, want settled authoritative turn", snapshot.HistoryEvents)
	}
}

func TestRecoverLiveReturnsActiveWithRunningTurnOwnership(t *testing.T) {
	conv, _ := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"active","activeFlags":[]},"turns":[`+
			`{"id":"turn-1","status":"inProgress","items":[`+
			`{"type":"agentMessage","id":"partial","text":"not settled"}]}]}}`)

	snapshot, err := conv.RecoverLive(context.Background())
	if err != nil {
		t.Fatalf("RecoverLive: %v", err)
	}
	if snapshot.ActivityState != domain.ActivityActive {
		t.Fatalf("activity = %q, want active", snapshot.ActivityState)
	}
	if len(snapshot.HistoryEvents) != 1 ||
		snapshot.HistoryEvents[0].Kind != ports.ChatEventTurnStarted ||
		snapshot.HistoryEvents[0].ProviderTurnID != "turn-1" {
		t.Fatalf("history events = %#v, want only live turn ownership", snapshot.HistoryEvents)
	}
}

func TestRecoverLiveFailsClosedOnUnknownActiveFlag(t *testing.T) {
	conv, _ := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"active",`+
			`"activeFlags":["futureWaitState"]},"turns":[]}}`)

	_, err := conv.RecoverLive(context.Background())
	if !errors.Is(err, ports.ErrChatRecoveryInconclusive) {
		t.Fatalf("RecoverLive error = %v, want unknown active flag to fail closed", err)
	}
}

func TestRecoverLiveCapturesPendingApprovalBeforeWaitingState(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"active",`+
			`"activeFlags":["waitingOnApproval"]},"turns":[`+
			`{"id":"turn-1","status":"inProgress","items":[]}]}}`)
	srv.push(`{"id":7,"method":"item/commandExecution/requestApproval","params":{` +
		`"threadId":"thread-1","turnId":"turn-1","itemId":"command-1",` +
		`"command":"git push","availableDecisions":["accept","decline"]}}`)

	snapshot, err := conv.RecoverLive(context.Background())
	if err != nil {
		t.Fatalf("RecoverLive: %v", err)
	}
	if snapshot.ActivityState != domain.ActivityWaitingInput {
		t.Fatalf("activity = %q, want waiting_input", snapshot.ActivityState)
	}
	if len(snapshot.ReplayEvents) != 1 ||
		snapshot.ReplayEvents[0].Kind != ports.ChatEventApprovalRequested ||
		snapshot.ReplayEvents[0].RequestID != "7" {
		t.Fatalf("replay events = %#v, want registered pending approval", snapshot.ReplayEvents)
	}
}

func TestRecoverLiveFailsClosedOnUnknownProviderState(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"futureState"},"turns":[]}}`)
	srv.push(`{"id":7,"method":"item/commandExecution/requestApproval","params":{` +
		`"threadId":"thread-1","turnId":"turn-1","itemId":"command-1",` +
		`"command":"git push","availableDecisions":["accept","decline"]}}`)

	snapshot, err := conv.RecoverLive(context.Background())
	if !errors.Is(err, ports.ErrChatRecoveryInconclusive) {
		t.Fatalf("RecoverLive error = %v, want ErrChatRecoveryInconclusive", err)
	}
	if len(snapshot.ReplayEvents) != 1 ||
		snapshot.ReplayEvents[0].Kind != ports.ChatEventApprovalRequested ||
		snapshot.ReplayEvents[0].RequestID != "7" {
		t.Fatalf("replay events = %#v, want approval captured before semantic failure",
			snapshot.ReplayEvents)
	}
}

func TestRecoverLiveReturnsCapturedReplayWithThreadReadError(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}`)
	srv.replyError("thread/read", -32603, "read failed")
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1",` +
		`"turn":{"id":"turn-1","status":"completed","items":[]}}}`)

	snapshot, err := conv.RecoverLive(context.Background())
	if !errors.Is(err, ports.ErrChatRecoveryInconclusive) {
		t.Fatalf("RecoverLive error = %v, want ErrChatRecoveryInconclusive", err)
	}
	if len(snapshot.ReplayEvents) != 1 || snapshot.ReplayEvents[0].Kind != ports.ChatEventTurnCompleted {
		t.Fatalf("replay events = %#v, want completion captured before RPC failure",
			snapshot.ReplayEvents)
	}
}

func TestRecoverLiveReturnsCapturedReplayWithThreadReadDecodeError(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t, `[]`)
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1",` +
		`"turn":{"id":"turn-1","status":"completed","items":[]}}}`)

	snapshot, err := conv.RecoverLive(context.Background())
	if !errors.Is(err, ports.ErrChatRecoveryInconclusive) {
		t.Fatalf("RecoverLive error = %v, want ErrChatRecoveryInconclusive", err)
	}
	if len(snapshot.ReplayEvents) != 1 || snapshot.ReplayEvents[0].Kind != ports.ChatEventTurnCompleted {
		t.Fatalf("replay events = %#v, want completion captured before decode failure",
			snapshot.ReplayEvents)
	}
}

func TestClosingLiveReconnectBeforeRecoveryReleasesEventPump(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}`)

	if err := conv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The test transport uses separate one-way pipes; close provider output too
	// to model the full-duplex host socket that Close releases in production.
	if err := srv.toClient.Close(); err != nil {
		t.Fatalf("close provider output: %v", err)
	}
	select {
	case <-conv.pumpDone:
	case <-time.After(2 * time.Second):
		t.Fatal("event pump remained blocked on recovery that never started")
	}
}

func TestConcurrentProviderRequestAndNotificationPreserveWireOrder(t *testing.T) {
	conv, srv := openConversation(t)
	// Hold the approval registration at the conversation lock after the read loop
	// has assigned it the earlier sequence. The later completion must not overtake
	// it through the independent notification pump.
	conv.mu.Lock()
	srv.push(`{"id":7,"method":"item/commandExecution/requestApproval","params":{` +
		`"threadId":"thread-1","turnId":"turn-1","itemId":"command-1",` +
		`"command":"git push","availableDecisions":["accept","decline"]}}`)
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1",` +
		`"turn":{"id":"turn-1","status":"completed","items":[]}}}`)

	select {
	case event := <-conv.Events():
		conv.mu.Unlock()
		t.Fatalf("later %s event overtook blocked approval registration", event.Kind)
	case <-time.After(25 * time.Millisecond):
	}
	conv.mu.Unlock()

	approval := nextEvent(t, conv.Events(), ports.ChatEventApprovalRequested)
	if approval.RequestID != "7" {
		t.Fatalf("first request id = %q, want 7", approval.RequestID)
	}
	completed := nextEvent(t, conv.Events(), ports.ChatEventTurnCompleted)
	if completed.ProviderTurnID != "turn-1" {
		t.Fatalf("completion turn = %q, want turn-1", completed.ProviderTurnID)
	}
}

func TestPostRecoveryTailPreservesProviderWireOrder(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}`)
	// Answer thread/read manually so the recovery boundary is fully finalized
	// before injecting its concurrent live tail.
	srv.mu.Lock()
	delete(srv.responses, "thread/read")
	srv.mu.Unlock()
	type recoveryResult struct {
		snapshot ports.ChatLiveRecoverySnapshot
		err      error
	}
	recovered := make(chan recoveryResult, 1)
	go func() {
		snapshot, err := conv.RecoverLive(context.Background())
		recovered <- recoveryResult{snapshot: snapshot, err: err}
	}()
	request := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/read" })
	srv.push(`{"id":` + string(*request.ID) + `,"result":{` +
		`"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}}`)
	result := <-recovered
	if result.err != nil {
		t.Fatalf("RecoverLive: %v", result.err)
	}
	if len(result.snapshot.ReplayEvents) != 0 {
		t.Fatalf("boundary replay = %#v, want no pre-response events", result.snapshot.ReplayEvents)
	}
	conv.CommitLiveRecovery()

	conv.mu.Lock()
	locked := true
	defer func() {
		if locked {
			conv.mu.Unlock()
		}
	}()
	srv.push(`{"id":7,"method":"item/commandExecution/requestApproval","params":{` +
		`"threadId":"thread-1","turnId":"turn-1","itemId":"command-1",` +
		`"command":"git push","availableDecisions":["accept","decline"]}}`)
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1",` +
		`"turn":{"id":"turn-1","status":"completed","items":[]}}}`)
	select {
	case event := <-conv.Events():
		conv.mu.Unlock()
		locked = false
		t.Fatalf("post-boundary %s overtook earlier approval registration", event.Kind)
	case <-time.After(25 * time.Millisecond):
	}
	conv.mu.Unlock()
	locked = false

	if event := nextEvent(t, conv.Events(), ports.ChatEventApprovalRequested); event.RequestID != "7" {
		t.Fatalf("first tail request id = %q, want 7", event.RequestID)
	}
	if event := nextEvent(t, conv.Events(), ports.ChatEventTurnCompleted); event.ProviderTurnID != "turn-1" {
		t.Fatalf("second tail turn = %q, want turn-1", event.ProviderTurnID)
	}
}

func TestAbortLiveRecoveryReturnsPostBoundaryEventsForDurableRetry(t *testing.T) {
	conv, srv := openLiveRecoveryConversation(t,
		`{"thread":{"id":"thread-1","status":{"type":"active","activeFlags":[]},"turns":[`+
			`{"id":"turn-1","status":"inProgress","items":[]}]}}`)

	snapshot, err := conv.RecoverLive(context.Background())
	if err != nil {
		t.Fatalf("RecoverLive: %v", err)
	}
	if len(snapshot.ReplayEvents) != 0 {
		t.Fatalf("boundary replay = %#v, want no pre-response events", snapshot.ReplayEvents)
	}

	// Both frames were accepted by the attached transport after thread/read. A
	// startup validation/persistence failure must get them back before detaching;
	// the persistent host has already handed them to this daemon and cannot replay
	// the completion on the next attachment.
	srv.push(`{"id":7,"method":"item/commandExecution/requestApproval","params":{` +
		`"threadId":"thread-1","turnId":"turn-1","itemId":"command-1",` +
		`"command":"git push","availableDecisions":["accept","decline"]}}`)
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1",` +
		`"turn":{"id":"turn-1","status":"completed","items":[]}}}`)

	retryEvents, err := conv.AbortLiveRecovery(context.Background())
	if err != nil {
		t.Fatalf("AbortLiveRecovery: %v", err)
	}
	if len(retryEvents) != 2 ||
		retryEvents[0].Kind != ports.ChatEventApprovalRequested ||
		retryEvents[1].Kind != ports.ChatEventTurnCompleted {
		t.Fatalf("retry events = %#v, want approval then completion", retryEvents)
	}
}

func TestReadHistoryReconstructsNativeTurnsForTheChatTimeline(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/read", threadWithRenderedHistory)

	events, err := conv.ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("events = %d, want start + user + command + assistant + completion: %#v", len(events), events)
	}
	wantKinds := []ports.ChatEventKind{
		ports.ChatEventTurnStarted,
		ports.ChatEventUserMessageCompleted,
		ports.ChatEventActivityCompleted,
		ports.ChatEventMessageCompleted,
		ports.ChatEventTurnCompleted,
	}
	seenIDs := map[string]bool{}
	for i, event := range events {
		if event.Kind != wantKinds[i] {
			t.Errorf("event %d kind = %q, want %q", i, event.Kind, wantKinds[i])
		}
		if event.ProviderEventID == "" || seenIDs[event.ProviderEventID] {
			t.Errorf("event %d has missing or duplicate identity %q", i, event.ProviderEventID)
		}
		seenIDs[event.ProviderEventID] = true
		if event.ProviderTurnID != "turn-a" {
			t.Errorf("event %d turn = %q, want turn-a", i, event.ProviderTurnID)
		}
	}
	if events[1].Text != "Inspect the repository" || events[1].ClientMessageID != "client-1" {
		t.Errorf("recovered user message = %#v", events[1])
	}
	if events[2].ActivityKind != "command" || !strings.Contains(string(events[2].Detail), `/tmp/ws\n`) {
		t.Errorf("recovered command = %#v", events[2])
	}
	if events[3].Text != "The repository is ready." {
		t.Errorf("recovered answer = %q", events[3].Text)
	}
	if events[4].TurnState != "completed" {
		t.Errorf("recovered turn state = %q", events[4].TurnState)
	}
}

func TestReadHistoryMakesMissingItemIDsUniqueAcrossTurns(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/read", `{"thread":{"id":"thread-1","turns":[`+
		`{"id":"turn-a","status":"completed","items":[{"type":"agentMessage","text":"first"}]},`+
		`{"id":"turn-b","status":"completed","items":[{"type":"agentMessage","text":"second"}]}`+
		`]}}`)

	events, err := conv.ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	var itemIDs []string
	for _, event := range events {
		if event.Kind == ports.ChatEventMessageCompleted {
			itemIDs = append(itemIDs, event.ProviderItemID)
		}
	}
	if len(itemIDs) != 2 || itemIDs[0] == "" || itemIDs[0] == itemIDs[1] {
		t.Fatalf("provider item ids = %#v, want two conversation-wide identities", itemIDs)
	}
}

func TestReadHistoryRejectsAnUnsettledNativeTurnInsteadOfOmittingIt(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/read", `{"thread":{"id":"thread-1","turns":[`+
		`{"id":"turn-a","status":"completed","items":[{"type":"agentMessage","text":"settled"}]},`+
		`{"id":"turn-b","status":"inProgress","items":[{"type":"agentMessage","text":"partial"}]}`+
		`]}}`)

	events, err := conv.ReadHistory(context.Background())
	if !errors.Is(err, ports.ErrChatHistoryUnsettled) {
		t.Fatalf("ReadHistory error = %v, want ErrChatHistoryUnsettled", err)
	}
	if len(events) != 0 {
		t.Fatalf("ReadHistory returned %d partial events with an unsettled turn", len(events))
	}
}

func TestRefreshHistoryPerformsANewThreadRead(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/read", `{"thread":{"id":"thread-1","turns":[`+
		`{"id":"turn-a","status":"inProgress"}`+
		`]}}`)

	if _, err := conv.ReadHistory(context.Background()); !errors.Is(err, ports.ErrChatHistoryUnsettled) {
		t.Fatalf("ReadHistory error = %v, want ErrChatHistoryUnsettled", err)
	}

	srv.reply("thread/read", `{"thread":{"id":"thread-1","turns":[`+
		`{"id":"turn-a","status":"completed"}`+
		`]}}`)
	events, err := conv.RefreshHistory(context.Background())
	if err != nil {
		t.Fatalf("RefreshHistory: %v", err)
	}
	if len(events) != 2 || events[0].Kind != ports.ChatEventTurnStarted ||
		events[1].Kind != ports.ChatEventTurnCompleted || events[1].TurnState != "completed" {
		t.Fatalf("refreshed events = %#v, want settled turn replay", events)
	}

	srv.mu.Lock()
	reads := 0
	for _, seen := range srv.seen {
		if seen.Method == "thread/read" {
			reads++
		}
	}
	srv.mu.Unlock()
	if reads != 2 {
		t.Fatalf("thread/read requests = %d, want one initial read and one refresh", reads)
	}
}

// The provider takes a COUNT from the end of the thread, not a turn id, so the whole
// correctness of rollback rests on turning the named turn into the right number.
// Naming the middle of three turns must discard that turn and the one after it.
func TestRollbackCountsTurnsFromTheNamedOneToTheEnd(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/read", threadWithTurns)
	srv.reply("thread/rollback", `{"thread":{"id":"thread-1","turns":[{"id":"turn-a","status":"completed"}]}}`)

	if err := conv.Rollback(context.Background(), "turn-b"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	read := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/read" })
	var readParams struct {
		ThreadID     string `json:"threadId"`
		IncludeTurns bool   `json:"includeTurns"`
	}
	if err := json.Unmarshal(read.Params, &readParams); err != nil {
		t.Fatalf("thread/read params: %v", err)
	}
	if readParams.ThreadID != "thread-1" || !readParams.IncludeTurns {
		t.Errorf("thread/read params = %+v, want thread-1 with turns", readParams)
	}

	rollback := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/rollback" })
	var params struct {
		ThreadID string `json:"threadId"`
		NumTurns int    `json:"numTurns"`
	}
	if err := json.Unmarshal(rollback.Params, &params); err != nil {
		t.Fatalf("thread/rollback params: %v", err)
	}
	if params.ThreadID != "thread-1" {
		t.Errorf("threadId = %q, want thread-1", params.ThreadID)
	}
	if params.NumTurns != 2 {
		t.Errorf("numTurns = %d, want 2 (the named turn and the one after it)", params.NumTurns)
	}
}

func TestRollbackCountsCurrentPaginatedTurnsWithoutLegacyHydration(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply(methodThreadTurnsList, `{"data":[`+
		`{"id":"turn-a","status":"completed","items":[],"itemsView":"full"},`+
		`{"id":"turn-b","status":"completed","items":[],"itemsView":"full"},`+
		`{"id":"turn-c","status":"completed","items":[],"itemsView":"full"}`+
		`]}`)
	srv.reply("thread/rollback", `{"thread":{"id":"thread-1","turns":[]}}`)

	if err := conv.Rollback(context.Background(), "turn-b"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if srv.sentMethod("thread/read") {
		t.Fatal("paginated rollback unexpectedly used deprecated thread/read hydration")
	}
	rollback := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/rollback" })
	var params struct {
		NumTurns int `json:"numTurns"`
	}
	if err := json.Unmarshal(rollback.Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.NumTurns != 2 {
		t.Errorf("numTurns = %d, want 2", params.NumTurns)
	}
}

// Naming the last turn is the common case and must ask for exactly one turn: the
// provider rejects numTurns < 1, and asking for zero would be a silent no-op.
func TestRollbackOfTheLastTurnDiscardsOne(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/read", threadWithTurns)
	srv.reply("thread/rollback", `{"thread":{"id":"thread-1","turns":[]}}`)

	if err := conv.Rollback(context.Background(), "turn-c"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	rollback := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/rollback" })
	var params struct {
		NumTurns int `json:"numTurns"`
	}
	if err := json.Unmarshal(rollback.Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.NumTurns != 1 {
		t.Errorf("numTurns = %d, want 1", params.NumTurns)
	}
}

// A turn the provider does not have must not produce a rollback at all. Guessing a
// count would discard turns the user never named.
func TestRollbackRefusesATurnTheProviderDoesNotHave(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/read", threadWithTurns)

	err := conv.Rollback(context.Background(), "turn-zzz")
	if err == nil {
		t.Fatal("Rollback of an unknown turn succeeded")
	}
	if !isRefusal(err) {
		t.Errorf("err = %v, want a provider refusal", err)
	}
	if srv.sentMethod("thread/rollback") {
		t.Fatal("thread/rollback was sent for a turn the provider does not have")
	}
}

// The provider refuses a rollback while a turn runs. That refusal must reach the
// caller as a conflict, not as an internal failure: it is an ordinary thing to press
// undo a moment too early.
func TestRollbackTranslatesTheMidTurnRefusal(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/read", threadWithTurns)
	srv.replyError("thread/rollback", -32600, "Cannot rollback while a turn is in progress.")

	err := conv.Rollback(context.Background(), "turn-c")
	if err == nil {
		t.Fatal("Rollback succeeded despite the provider refusing")
	}
	if !isRefusal(err) {
		t.Errorf("err = %v, want a provider refusal", err)
	}
	if !strings.Contains(err.Error(), "turn is in progress") {
		t.Errorf("err = %v, want the provider's own explanation carried through", err)
	}
}

// A transport failure is NOT a refusal. Reporting it as one would tell the user to
// stop the agent when the real problem is that the provider is gone.
func TestRollbackKeepsATransportFailureAsAFailure(t *testing.T) {
	conv, srv := openConversation(t)
	srv.replyError("thread/read", -32603, "internal error")

	err := conv.Rollback(context.Background(), "turn-c")
	if err == nil {
		t.Fatal("Rollback succeeded despite thread/read failing")
	}
	if isRefusal(err) {
		t.Errorf("err = %v, want a plain failure rather than a refusal", err)
	}
}

func TestForkReturnsTheNewThreadIDAndInheritsTheWorkingDirectory(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/fork", `{"thread":{"id":"thread-2","forkedFromId":"thread-1"},"cwd":"/tmp/ws"}`)

	forked, err := conv.Fork(context.Background(), nil)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forked != "thread-2" {
		t.Errorf("forked thread = %q, want thread-2", forked)
	}

	fork := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/fork" })
	var params map[string]any
	if err := json.Unmarshal(fork.Params, &params); err != nil {
		t.Fatalf("thread/fork params: %v", err)
	}
	if params["threadId"] != "thread-1" {
		t.Errorf("threadId = %v, want thread-1", params["threadId"])
	}
	// cwd must be absent so the fork inherits the source thread's directory. A fork
	// pointed at another tree would remember editing files that are not there.
	if _, ok := params["cwd"]; ok {
		t.Errorf("thread/fork sent a cwd (%v); it must inherit the source thread's", params["cwd"])
	}
	// lastTurnId must be absent too: ChatForker names no turn, and a fork that
	// quietly truncated would be a rollback wearing the wrong name.
	if _, ok := params["lastTurnId"]; ok {
		t.Error("thread/fork sent a lastTurnId; a fork copies the whole history")
	}
}

func TestForkThroughTurnSendsLastTurnID(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/fork", `{"thread":{"id":"thread-2"},"cwd":"/tmp/ws"}`)
	anchor := "turn-before-edit"

	forked, err := conv.Fork(context.Background(), &anchor)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forked != "thread-2" {
		t.Fatalf("forked thread = %q, want thread-2", forked)
	}
	frame := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/fork" })
	var params map[string]any
	if err := json.Unmarshal(frame.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params["lastTurnId"] != anchor {
		t.Fatalf("lastTurnId = %#v, want %q", params["lastTurnId"], anchor)
	}
	if _, present := params["cwd"]; present {
		t.Fatal("fork must inherit the source cwd")
	}
}

func TestForkRejectsAResponseWithNoThreadID(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/fork", `{"thread":{}}`)

	if _, err := conv.Fork(context.Background(), nil); err == nil {
		t.Fatal("Fork accepted a response carrying no thread id")
	}
}

func TestSetTitleSendsTheTrimmedName(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/name/set", `{}`)

	if err := conv.SetTitle(context.Background(), "  Fix OAuth Return URL Loss  "); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	set := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/name/set" })
	var params struct {
		ThreadID string `json:"threadId"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(set.Params, &params); err != nil {
		t.Fatalf("thread/name/set params: %v", err)
	}
	if params.ThreadID != "thread-1" {
		t.Errorf("threadId = %q, want thread-1", params.ThreadID)
	}
	if params.Name != "Fix OAuth Return URL Loss" {
		t.Errorf("name = %q, want the trimmed title", params.Name)
	}
}

// The provider rejects a blank name. Refusing locally keeps the error about the
// caller's input and spends no round trip on a call that cannot succeed.
func TestSetTitleRefusesABlankName(t *testing.T) {
	conv, srv := openConversation(t)

	err := conv.SetTitle(context.Background(), "   \t ")
	if err == nil {
		t.Fatal("SetTitle accepted a blank name")
	}
	if !isRefusal(err) {
		t.Errorf("err = %v, want a refusal", err)
	}
	if srv.sentMethod("thread/name/set") {
		t.Fatal("thread/name/set was sent for a blank name")
	}
}

// A title AO did not set still has to reach it: another client naming the thread is
// how a provider-derived title arrives at all.
func TestThreadRenamedNotificationBecomesATitleEvent(t *testing.T) {
	conv, srv := openConversation(t)
	srv.push(`{"method":"thread/name/updated","params":{"threadId":"thread-1","threadName":"  Restore Canvas Renderer Fallback  "}}`)

	ev := nextEvent(t, conv.Events(), ports.ChatEventThreadRenamed)
	if ev.Title != "Restore Canvas Renderer Fallback" {
		t.Errorf("title = %q, want the trimmed name", ev.Title)
	}
}

// A cleared name is reported as an empty title rather than dropped: the projection
// has to see that the thread no longer has one.
func TestThreadRenamedNotificationReportsAClearedName(t *testing.T) {
	conv, srv := openConversation(t)
	srv.push(`{"method":"thread/name/updated","params":{"threadId":"thread-1","threadName":null}}`)

	ev := nextEvent(t, conv.Events(), ports.ChatEventThreadRenamed)
	if ev.Title != "" {
		t.Errorf("title = %q, want empty for a cleared name", ev.Title)
	}
}

// The service feature-detects each of these, so a dropped method would read as "the
// provider cannot do this" with nothing to notice.
func TestConversationAdvertisesTheHistoryCapabilities(t *testing.T) {
	caps := capabilities()
	for _, want := range []ports.ChatCapability{
		ports.ChatCapabilityRollback,
		ports.ChatCapabilityFork,
		ports.ChatCapabilityRename,
	} {
		if !caps.Has(want) {
			t.Errorf("capability %q not advertised", want)
		}
	}
}

// isRefusal asks the error the same question the Chat service does.
func isRefusal(err error) bool {
	var refusal interface{ ChatRefusal() bool }
	return errors.As(err, &refusal) && refusal.ChatRefusal()
}
