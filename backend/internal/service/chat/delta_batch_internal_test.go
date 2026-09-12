package chat

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCoalesceChatDeltasPreservesBoundaries(t *testing.T) {
	first := ports.ChatEvent{
		Kind: ports.ChatEventMessageDelta, ProviderItemID: "message",
		ProviderTurnID: "turn", ProviderConversationID: "thread", Delta: "a",
	}
	for name, change := range map[string]func(*ports.ChatEvent){
		"message":         func(e *ports.ChatEvent) { e.ProviderItemID = "other" },
		"turn":            func(e *ports.ChatEvent) { e.ProviderTurnID = "other" },
		"conversation":    func(e *ports.ChatEvent) { e.ProviderConversationID = "other" },
		"native identity": func(e *ports.ChatEvent) { e.ProviderEventID = "native" },
		"metadata":        func(e *ports.ChatEvent) { e.Detail = []byte(`{"source":"other"}`) },
		"error":           func(e *ports.ChatEvent) { e.Err = errors.New("provider failure") },
		"completion":      func(e *ports.ChatEvent) { e.Kind = ports.ChatEventMessageCompleted },
		"approval":        func(e *ports.ChatEvent) { e.Kind = ports.ChatEventApprovalRequested },
	} {
		t.Run(name, func(t *testing.T) {
			second := first
			second.Delta = "b"
			change(&second)
			events := make(chan sequencedChatEvent, 2)
			events <- sequencedChatEvent{ChatEvent: first}
			events <- sequencedChatEvent{ChatEvent: second}
			close(events)
			if got := slices.Collect(coalesceChatDeltas(events)); !reflect.DeepEqual(got, []sequencedChatEvent{{ChatEvent: first}, {ChatEvent: second}}) { //nolint:govet // Assert unchanged event payloads, not semantic error equivalence.
				t.Fatalf("events changed across %s boundary: %+v", name, got)
			}
		})
	}
}

func TestCoalesceChatDeltasFlushesAtFirstChunkDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		events := make(chan sequencedChatEvent, 2)
		projected := make(chan sequencedChatEvent, 2)
		go func() {
			for event := range coalesceChatDeltas(events) {
				projected <- event
			}
			close(projected)
		}()
		events <- sequencedChatEvent{ChatEvent: ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "message", Delta: "a"}}
		synctest.Wait()
		time.Sleep(20 * time.Millisecond)
		events <- sequencedChatEvent{ChatEvent: ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "message", Delta: "b"}}
		synctest.Wait()
		time.Sleep(29 * time.Millisecond)
		synctest.Wait()
		select {
		case event := <-projected:
			t.Fatalf("flushed before deadline: %+v", event)
		default:
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		select {
		case event := <-projected:
			if event.Delta != "ab" {
				t.Fatalf("flushed text = %q, want ab", event.Delta)
			}
		default:
			t.Fatal("a later chunk postponed the first chunk's deadline")
		}
		close(events)
	})
}

func TestCoalesceChatDeltasFlushesBeforeApproval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		events := make(chan sequencedChatEvent, 2)
		projected := make(chan sequencedChatEvent, 2)
		go func() {
			for event := range coalesceChatDeltas(events) {
				projected <- event
			}
		}()
		events <- sequencedChatEvent{ChatEvent: ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "message", Delta: "pending"}}
		synctest.Wait()
		events <- sequencedChatEvent{ChatEvent: ports.ChatEvent{Kind: ports.ChatEventApprovalRequested, RequestID: "approval"}}
		synctest.Wait()
		if len(projected) != 2 {
			t.Fatal("approval and preceding text waited for the flush timer")
		}
		if first, second := <-projected, <-projected; first.Delta != "pending" || second.RequestID != "approval" {
			t.Fatalf("approval overtook text: %+v %+v", first, second)
		}
		close(events)
	})
}

func TestCoalesceChatDeltasBoundsSizeAndFlushesOnClose(t *testing.T) {
	events := make(chan sequencedChatEvent, 4)
	texts := []string{strings.Repeat("a", deltaMaxBytes-1), "bc", strings.Repeat("d", deltaMaxBytes+1), "tail"}
	for _, text := range texts {
		events <- sequencedChatEvent{ChatEvent: ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "message", Delta: text}}
	}
	close(events)
	var got []string
	for event := range coalesceChatDeltas(events) {
		got = append(got, event.Delta)
	}
	// An individually oversized provider event is passed through unchanged;
	// neither a batch nor the final tail may grow across the size boundary.
	if !slices.Equal(got, texts) {
		t.Fatalf("batch lengths = %v, want original bounded groups", func() []int {
			var lengths []int
			for _, text := range got {
				lengths = append(lengths, len(text))
			}
			return lengths
		}())
	}
}
