package codexappserver

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestResumeNamespacesRepeatedNativeHistoryByOwnershipScope(t *testing.T) {
	ctx := context.Background()
	st := sqlitetest.MustOpenAt(t, t.TempDir())
	now := time.Now()
	if err := st.UpsertProject(ctx, domain.ProjectRecord{ID: "scope-test", Path: t.TempDir(), RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	session, err := st.CreateSession(ctx, domain.SessionRecord{ID: "scope-test-1", ProjectID: "scope-test", Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, Kind: domain.KindWorker, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := st.CreateConversation(ctx, "scope-test", domain.ConversationScopeSession, session.ProjectID, session.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	seenTurns, seenItems, seenEvents := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, scope := range []string{"owner-A", "owner-B", "owner-A-again"} {
		threadID := []string{"thread-A", "thread-B", "thread-A"}[i]
		d, srv := newTestDriver(t)
		srv.reply("thread/fork", `{"thread":{"id":"child"}}`)
		provider, err := d.Resume(ctx, ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: threadID, ProviderScopeID: scope})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = provider.Close() })
		srv.reply("thread/read", strings.ReplaceAll(threadWithRenderedHistory, "thread-1", threadID))
		events, err := provider.(ports.ChatHistoryReader).ReadHistory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		turn := events[0].ProviderTurnID
		if seenTurns[turn] {
			t.Fatalf("native turn collides across ownership scopes: %q", turn)
		}
		seenTurns[turn] = true
		if err := st.AdoptProviderTurn(ctx, conversation.ID, session.ID, scope, scope+"-turn", turn, now); err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind == ports.ChatEventUserMessageCompleted {
				message := domain.ConversationMessage{ID: scope + "-user", Role: domain.MessageRoleUser, Text: event.Text, ProviderItemID: event.ProviderItemID, ClientMessageID: event.ClientMessageID}
				if err := st.AppendImportedUserMessage(ctx, conversation.ID, turn, message, now); err != nil {
					t.Fatalf("project native thread under %s: %v", scope, err)
				}
				if err := st.AppendImportedUserMessage(ctx, conversation.ID, turn, message, now); err != nil {
					t.Fatalf("repeat projection: %v", err)
				}
			}
			if seenEvents[event.ProviderEventID] {
				t.Fatalf("replay event collides across scopes: %q", event.ProviderEventID)
			}
			seenEvents[event.ProviderEventID] = true
			if event.ProviderItemID != "" {
				if seenItems[event.ProviderItemID] {
					t.Fatalf("item collides across scopes: %q", event.ProviderItemID)
				}
				seenItems[event.ProviderItemID] = true
			}
		}
		if _, err := provider.(ports.ChatForker).Fork(ctx, &turn); err != nil {
			t.Fatal(err)
		}
		var params map[string]any
		if err := json.Unmarshal(srv.awaitFrame(func(f frame) bool { return f.Method == "thread/fork" }).Params, &params); err != nil {
			t.Fatal(err)
		}
		if params["lastTurnId"] != "turn-a" {
			t.Fatalf("AO-scoped ID escaped onto the native wire: %v", params)
		}
		again, err := provider.(ports.ChatHistoryReader).ReadHistory(ctx)
		if err != nil || again[0].ProviderTurnID != turn {
			t.Fatalf("unstable same-scope replay: %v", err)
		}
	}
	rows, err := st.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil || len(rows.Turns) != 3 || len(rows.Messages) != 3 {
		t.Fatalf("A→B→A projection lost independent history: turns=%d messages=%d err=%v", len(rows.Turns), len(rows.Messages), err)
	}
}

func TestScopedLiveTurnKeepsNativeIDsOnProviderWire(t *testing.T) {
	d, srv := newTestDriver(t)
	ctx := context.Background()
	provider, err := d.Start(ctx, ports.ChatStartConfig{WorkspacePath: "/tmp/ws", ProviderScopeID: "scope"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	turn, err := provider.SendTurn(ctx, ports.ChatUserMessage{Text: "do work"})
	if err != nil || turn.ProviderTurnID != "codex:5:scopeturn-1" {
		t.Fatalf("send turn scope: %+v err=%v", turn, err)
	}
	srv.push(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"answer-1","type":"agentMessage","text":"done"}}}`)
	message := nextEvent(t, provider.Events(), ports.ChatEventMessageCompleted)
	if message.ProviderTurnID != turn.ProviderTurnID || message.ProviderItemID != "codex:5:scopeanswer-1" {
		t.Fatalf("live event lost scope: %+v", message)
	}
	srv.push(`{"id":42,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","command":"date","availableDecisions":["accept","cancel"]}}`)
	approval := nextEvent(t, provider.Events(), ports.ChatEventApprovalRequested)
	if approval.RequestID != "codex:5:scope42" || approval.ProviderTurnID != turn.ProviderTurnID {
		t.Fatalf("approval lost scope: %+v", approval)
	}
	if err := provider.ResolveRequest(ctx, approval.RequestID, ports.ChatDecision{ID: "accept"}); err != nil {
		t.Fatal(err)
	}
	reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && string(*f.ID) == "42" && f.Method == "" })
	if string(reply.Result) != `{"decision":"accept"}` {
		t.Fatalf("native approval reply: %s", reply.Result)
	}
	srv.reply("turn/steer", `{"turnId":"turn-1"}`)
	steered, err := provider.(ports.ChatSteerer).Steer(ctx, turn.ProviderTurnID, ports.ChatUserMessage{Text: "change course"})
	if err != nil || steered.ProviderTurnID != turn.ProviderTurnID {
		t.Fatalf("steer: %+v err=%v", steered, err)
	}
	if err := provider.Interrupt(ctx, turn.ProviderTurnID); err != nil {
		t.Fatal(err)
	}
	for method, field := range map[string]string{"turn/steer": "expectedTurnId", "turn/interrupt": "turnId"} {
		request := srv.awaitFrame(func(f frame) bool { return f.Method == method })
		var params map[string]any
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params[field] != "turn-1" || params["threadId"] != "thread-1" {
			t.Fatalf("scoped ID escaped onto %s wire: %v", method, params)
		}
	}
}

func TestResumeScopesRequestsBeforeHandshakeCompletes(t *testing.T) {
	d, srv := newTestDriver(t)
	srv.mu.Lock()
	delete(srv.responses, "thread/resume")
	srv.mu.Unlock()
	type result struct {
		provider ports.ChatConversation
		err      error
	}
	opened := make(chan result, 1)
	go func() {
		provider, err := d.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", ProviderScopeID: "scope"})
		opened <- result{provider, err}
	}()
	resume := srv.awaitFrame(func(f frame) bool { return f.Method == "thread/resume" })
	// Queue requests while resume is blocked, before the notification pump starts.
	for i := 0; i < 32; i++ {
		srv.push(`{"id":` + strconv.Itoa(i+42) + `,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"exec-1","command":"date","availableDecisions":["accept","cancel"]}}`)
	}
	srv.push(`{"id":` + string(*resume.ID) + `,"result":{"thread":{"id":"thread-1"}}}`)
	select {
	case result := <-opened:
		if result.err != nil {
			t.Fatal(result.err)
		}
		t.Cleanup(func() { _ = result.provider.Close() })
		event := nextEvent(t, result.provider.Events(), ports.ChatEventApprovalRequested)
		if event.ProviderTurnID != "codex:5:scopeturn-1" || event.ProviderItemID != event.RequestID || !strings.HasPrefix(event.RequestID, "codex:5:scope") {
			t.Fatalf("request escaped its ownership scope during resume: %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resume did not complete")
	}
}

func TestResumePreservesLegacyProjectionIDs(t *testing.T) {
	d, srv := newTestDriver(t)
	provider, err := d.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", ProviderScopeID: "old-root", LegacyProviderIDs: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	srv.reply("thread/read", threadWithRenderedHistory)
	events, err := provider.(ports.ChatHistoryReader).ReadHistory(context.Background())
	if err != nil || events[0].ProviderTurnID != "turn-a" || events[1].ProviderItemID != "user-1" {
		t.Fatalf("legacy projection identities changed on upgrade: %+v err=%v", events, err)
	}
}

func TestStartInEmptyLegacyScopeKeepsResumeIDFormat(t *testing.T) {
	d, _ := newTestDriver(t)
	provider, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", ProviderScopeID: "old-empty-root", LegacyProviderIDs: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	turn, err := provider.SendTurn(context.Background(), ports.ChatUserMessage{Text: "first prompt after upgrade"})
	if err != nil || turn.ProviderTurnID != "turn-1" {
		t.Fatalf("start disagrees with legacy resume ID format: %+v err=%v", turn, err)
	}
}
