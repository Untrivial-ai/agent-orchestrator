package codexappserver

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func openProviderFailureConversation(t *testing.T) (*conversation, *scriptedServer) {
	t.Helper()
	driver, server := newTestDriver(t)
	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conv.Close() })
	return conv.(*conversation), server
}

func TestProviderFailureSequences(t *testing.T) {
	const retry = `{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","willRetry":true,"error":{"message":"Reconnecting 1/5"}}}`
	const failure = `{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","willRetry":false,"error":{"message":"Usage limit reached","additionalDetails":"Resets tomorrow."}}}`
	const output = `{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"answer","delta":"Recovered"}}`
	const failed = `{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","items":[]}}}`
	const completed = `{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}}`
	const interrupted = `{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`
	for _, tc := range []struct {
		name      string
		frames    []string
		state     domain.TurnState
		message   string
		retryEnds int
	}{
		{"retry then success", []string{retry, output, completed}, domain.TurnStateCompleted, "", 1},
		{"retry exhausted", []string{retry, failure, failed}, domain.TurnStateFailed, "Usage limit reached\n\nResets tomorrow.", 0},
		{"failure only on completion", []string{retry, `{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"Usage limit reached","additionalDetails":"Resets tomorrow."}}}}`}, domain.TurnStateFailed, "Usage limit reached\n\nResets tomorrow.", 0},
		{"terminal error without retry", []string{failure, failed}, domain.TurnStateFailed, "Usage limit reached\n\nResets tomorrow.", 0},
		{"cancellation preserves error", []string{retry, failure, interrupted}, domain.TurnStateInterrupted, "Usage limit reached\n\nResets tomorrow.", 0},
		{"recovered warning then unrelated failure", []string{retry, output, failure, failed}, domain.TurnStateFailed, "Usage limit reached\n\nResets tomorrow.", 1},
		{"error then completed", []string{failure, completed}, domain.TurnStateCompleted, "Usage limit reached\n\nResets tomorrow.", 0},
		{"error then interrupted", []string{failure, interrupted}, domain.TurnStateInterrupted, "Usage limit reached\n\nResets tomorrow.", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conv, srv := openProviderFailureConversation(t)
			for _, frame := range tc.frames {
				srv.push(frame)
			}
			srv.push(`{"method":"thread/name/updated","params":{"threadId":"thread-1","threadName":"sentinel"}}`)
			var completions, retryStarts, retryEnds, errorsSeen int
			var message string
			for {
				select {
				case event := <-conv.Events():
					switch event.Kind {
					case ports.ChatEventError:
						errorsSeen++
						message = event.Err.Error()
					case ports.ChatEventActivityStarted:
						retryStarts++
						if event.ActivityStatus != domain.ActivityStatusRunning {
							t.Fatalf("retry is not progress: %#v", event)
						}
					case ports.ChatEventActivityCompleted:
						retryEnds++

					case ports.ChatEventTurnCompleted:
						completions++
						if event.Err != nil {
							message = event.Err.Error()
							errorsSeen++
						}
						if event.TurnState != tc.state || message != tc.message {
							t.Fatalf("outcome = %s, %q; want %s, %q", event.TurnState, message, tc.state, tc.message)
						}
					case ports.ChatEventThreadRenamed:
						wantRetries := 1
						if tc.frames[0] != retry {
							wantRetries = 0
						}
						if completions != 1 || retryStarts != wantRetries || retryEnds != tc.retryEnds || errorsSeen > 1 {
							t.Fatalf("completions=%d, retry starts/ends=%d/%d", completions, retryStarts, retryEnds)
						}
						return
					}
				case <-time.After(5 * time.Second):
					t.Fatal("timed out awaiting sequence")
				}
			}
		})
	}
}

func TestProviderFailureSurvivesStreamClosingWithoutCompletion(t *testing.T) {
	conv, srv := openProviderFailureConversation(t)
	srv.push(`{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","error":{"message":"Connection lost"}}}`)
	_ = srv.toClient.Close()
	event := nextEvent(t, conv.Events(), ports.ChatEventError)
	if event.Err == nil || event.Err.Error() != "Connection lost" {
		t.Fatalf("lost final explanation: %#v", event)
	}
}

func TestProviderFailureIsEmittedBeforeCompletion(t *testing.T) {
	conv, srv := openProviderFailureConversation(t)
	srv.push(`{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","willRetry":false,"error":{"message":"Usage limit reached"}}}`)
	srv.push(`{"method":"thread/name/updated","params":{"threadId":"thread-1","threadName":"sentinel"}}`)
	select {
	case event := <-conv.Events():
		if event.Kind != ports.ChatEventError || event.Err == nil || event.Err.Error() != "Usage limit reached" {
			t.Fatalf("provider error was held back: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider error was not emitted")
	}
}

func TestChildOutputSettlesOnlyItsOwnRetry(t *testing.T) {
	conv, srv := openProviderFailureConversation(t)
	for _, thread := range []string{"thread-1", "child-thread"} {
		srv.push(`{"method":"error","params":{"threadId":"` + thread + `","turnId":"same-turn","willRetry":true,"error":{"message":"Retrying"}}}`)
	}
	srv.push(`{"method":"item/agentMessage/delta","params":{"threadId":"child-thread","turnId":"same-turn","itemId":"answer","delta":"Recovered"}}`)
	event := nextEvent(t, conv.Events(), ports.ChatEventActivityCompleted)
	if event.ProviderConversationID != "child-thread" {
		t.Fatalf("child output settled another retry: %#v", event)
	}
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"same-turn","status":"completed"}}}`)
	event = nextEvent(t, conv.Events(), ports.ChatEventActivityCompleted)
	if event.ProviderConversationID != "thread-1" {
		t.Fatalf("root retry was not retained: %#v", event)
	}
}

func TestRetryEpisodesKeepRecoveredDiagnostics(t *testing.T) {
	conv, srv := openProviderFailureConversation(t)
	const retry = `{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","willRetry":true,"error":{"message":"Retrying"}}}`
	srv.push(retry)
	first := nextEvent(t, conv.Events(), ports.ChatEventActivityStarted)
	srv.push(retry)
	attempt := nextEvent(t, conv.Events(), ports.ChatEventActivityStarted)
	srv.push(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"answer","delta":"Recovered"}}`)
	recovered := nextEvent(t, conv.Events(), ports.ChatEventActivityCompleted)
	srv.push(retry)
	second := nextEvent(t, conv.Events(), ports.ChatEventActivityStarted)
	srv.push(`{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","willRetry":false,"error":{"message":"Failed"}}}`)
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed"}}}`)
	failed := nextEvent(t, conv.Events(), ports.ChatEventTurnCompleted)
	if first.ProviderItemID != attempt.ProviderItemID || first.ProviderItemID != recovered.ProviderItemID || first.ProviderItemID == second.ProviderItemID {
		t.Fatalf("retry attempts overwrote another episode: %q %q %q %q %q", first.ProviderItemID, attempt.ProviderItemID, recovered.ProviderItemID, second.ProviderItemID, failed.ProviderItemID)
	}
	if string(recovered.Detail) != `{"event":"provider.failure"}` || failed.TurnState != domain.TurnStateFailed {
		t.Fatalf("wrong episode hidden: recovered=%s failed=%s", recovered.Detail, failed.Detail)
	}
}
