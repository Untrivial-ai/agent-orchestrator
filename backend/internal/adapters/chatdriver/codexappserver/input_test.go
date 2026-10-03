package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNativeQuestionsUseInputProtocolAndRemainPendingAfterInvalidAnswer(t *testing.T) {
	d, srv := newTestDriver(t)
	conv, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws"})
	if err != nil {
		t.Fatal(err)
	}
	defer conv.Close()
	srv.push(`{"id":77,"method":"item/tool/requestUserInput","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"ask-1","questions":[{"id":"branch","header":"Branch","question":"Which branch?","isOther":true,"isSecret":false,"options":[{"label":"main","description":"Default"}]}]}}`)
	event := nextEvent(t, conv.Events(), ports.ChatEventInputRequested)
	if event.Input == nil || event.Input.Mode != ports.ChatInputModeForm || event.RequestID != "77" {
		t.Fatalf("input: %+v", event)
	}
	if err := conv.ResolveRequest(context.Background(), event.RequestID, ports.ChatDecision{ID: "accept"}); !errors.Is(err, ports.ErrChatDecisionNotOffered) {
		t.Fatalf("approval consumed input: %v", err)
	}
	responder := conv.(ports.ChatInputResponder)
	if err := responder.ResolveInput(context.Background(), event.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"question_0": "invented"}}); !errors.Is(err, ports.ErrChatDecisionNotOffered) {
		t.Fatalf("invalid answer: %v", err)
	}
	if err := responder.ResolveInput(context.Background(), event.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"question_0": "main", "question_0_custom": "feature"}}); err != nil {
		t.Fatal(err)
	}
	reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && string(*f.ID) == "77" && len(f.Result) > 0 })
	if !strings.Contains(string(reply.Result), `"branch":{"answers":["feature"]}`) {
		t.Fatalf("reply: %s", reply.Result)
	}
	if err := responder.ResolveInput(context.Background(), event.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionCancel}); !errors.Is(err, ports.ErrChatRequestNotPending) {
		t.Fatalf("stale answer: %v", err)
	}
}

func TestCodexMCPElicitationFormsAndURLRoundTrip(t *testing.T) {
	for _, mode := range []string{"form", "url"} {
		t.Run(mode, func(t *testing.T) {
			d, srv := newTestDriver(t)
			conv, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws"})
			if err != nil {
				t.Fatal(err)
			}
			defer conv.Close()
			params := map[string]any{"mode": mode, "threadId": "thread-1", "turnId": "turn-1", "serverName": "example", "message": "Please continue", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}}, "url": "https://example.com/authorize", "elicitationId": "oauth-1"}
			raw, _ := json.Marshal(map[string]any{"id": 78, "method": "mcpServer/elicitation/request", "params": params})
			srv.push(string(raw))
			event := nextEvent(t, conv.Events(), ports.ChatEventInputRequested)
			if string(event.Input.Mode) != mode {
				t.Fatalf("mode: %+v", event.Input)
			}
			content := map[string]any{"name": "Pat"}
			if err := conv.(ports.ChatInputResponder).ResolveInput(context.Background(), event.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: content}); err != nil {
				t.Fatal(err)
			}
			reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && string(*f.ID) == "78" && len(f.Result) > 0 })
			var result map[string]any
			if err := json.Unmarshal(reply.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result["action"] != "accept" {
				t.Fatalf("reply: %s", reply.Result)
			}
			if mode == "url" && result["content"] != nil {
				t.Fatal("URL response leaked form content")
			}
		})
	}
}

func TestAsyncQuestionSettlesMessageAndEmitsDurableInput(t *testing.T) {
	raw := json.RawMessage(`{"turnId":"turn-1","item":{"type":"agentMessage","id":"ask-async","text":"How are you doing?","delivery":"async","questions":[{"title":"How are you doing?","options":["Fine","Busy"]}]}}`)
	if events := normalizeItem(raw, false); len(events) != 0 {
		t.Fatalf("early input: %+v", events)
	}
	events := normalizeItem(raw, true)
	if len(events) != 2 || events[0].Kind != ports.ChatEventMessageCompleted || events[1].Kind != ports.ChatEventInputRequested || events[1].Input.ResponseMode != "message" {
		t.Fatalf("events: %+v", events)
	}
	if events[1].RequestID != "async:ask-async" {
		t.Fatalf("identity: %s", events[1].RequestID)
	}
}

func TestHistoryDoesNotReopenAnsweredAsyncQuestions(t *testing.T) {
	conv, srv := openConversation(t)
	srv.reply("thread/read", `{"thread":{"id":"thread-1","turns":[{"id":"turn-1","status":"completed","items":[{"type":"agentMessage","id":"ask-async","text":"How are you?","delivery":"async","questions":[{"title":"How are you?","options":[]}]}]}]}}`)
	events, err := conv.ReadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hasText := false
	for _, event := range events {
		if event.Kind == ports.ChatEventInputRequested {
			t.Fatal("replay created pending question")
		}
		if event.Kind == ports.ChatEventMessageCompleted && event.Text == "How are you?" {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("question text lost from history")
	}
}
