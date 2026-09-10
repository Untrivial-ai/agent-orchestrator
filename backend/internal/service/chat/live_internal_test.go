package chat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestLiveJournalBoundsAndReconnect(t *testing.T) {
	for name, text := range map[string]string{
		"event count": "a",
		"bytes":       strings.Repeat("a", liveMaxBytes/2),
		"oversized":   strings.Repeat("a", liveMaxBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			journal := &liveJournal{generation: "generation", conversationID: "conversation", branchID: "branch", subscribers: make(map[*LiveSubscription]struct{})}
			sub := journal.subscribe()
			defer sub.Close()
			count := 3
			if name == "event count" {
				count = liveMaxEvents + 1
			}
			for range count {
				journal.observe(ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "item", Delta: text}, time.Time{})
			}
			frame := sub.Snapshot(0)
			if frame.AfterSequence == 0 || frame.Sequence != int64(count) || len(frame.Events) > liveMaxEvents || journal.bytes > liveMaxBytes {
				t.Fatalf("unbounded journal or missing gap: floor=%d, sequence=%d, events=%d, bytes=%d", frame.AfterSequence, frame.Sequence, len(frame.Events), journal.bytes)
			}
			if frame.Generation != "generation" || frame.ConversationID != "conversation" || frame.BranchID != "branch" {
				t.Fatalf("lost journal identity: %+v", frame)
			}
			// Reconnecting reads only newer observations, including after the
			// journal evicted a prefix; already-consumed text is never repeated.
			reconnected := journal.subscribe()
			defer reconnected.Close()
			journal.observe(ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "item", Delta: "tail"}, time.Time{})
			next := reconnected.Snapshot(frame.Sequence)
			if next.AfterSequence != frame.Sequence || len(next.Events) != 1 || next.Events[0].Delta != "tail" {
				t.Fatalf("reconnect repeated text: %+v", next)
			}
		})
	}
}

func TestLiveJournalSkipsNativeDeltaAndClosesSubscribers(t *testing.T) {
	journal := &liveJournal{subscribers: make(map[*LiveSubscription]struct{})}
	sub := journal.subscribe()
	journal.observe(ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "item", ProviderEventID: "duplicate", Delta: "do not preview"}, time.Time{})
	journal.observe(ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderItemID: "item", ProviderEventID: "old-completion", Text: "do not replace newer text"}, time.Time{})
	journal.observe(ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderItemID: "item", Text: "final"}, time.Time{})
	frame := sub.Snapshot(0)
	if frame.Sequence != 3 || frame.AfterSequence != 0 || len(frame.Events) != 1 || frame.Events[0].Text != "final" {
		t.Fatalf("native delta was previewed or source cursor skipped: %+v", frame)
	}
	journal.close()
	// A final buffered edge may precede the closed-channel signal.
	for range sub.Changed() {
	}
	sub.Close()
	late := journal.subscribe()
	defer late.Close()
	if _, open := <-late.Changed(); open {
		t.Fatal("subscription to ended controller remained open")
	}
}

func TestCoalescedDeltasKeepLastSourceSequence(t *testing.T) {
	events := make(chan sequencedChatEvent, 3)
	for sequence := int64(1); sequence <= 2; sequence++ {
		events <- sequencedChatEvent{ChatEvent: ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "item", Delta: "a"}, sequence: sequence}
	}
	events <- sequencedChatEvent{ChatEvent: ports.ChatEvent{Kind: ports.ChatEventTurnCompleted}, sequence: 3}
	close(events)
	var got []sequencedChatEvent
	for event := range coalesceChatDeltas(events) {
		got = append(got, event)
	}
	if len(got) != 2 || got[0].Delta != "aa" || got[0].sequence != 2 || got[1].sequence != 3 {
		t.Fatalf("projection lost source checkpoint: %+v", got)
	}
}

type liveSnapshotOwner struct {
	mu         sync.Mutex
	generation string
}

func (s *liveSnapshotOwner) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := domain.SessionRecord{Mode: domain.SessionModeChat}
	record.Metadata.ControllerGeneration = s.generation
	return record, true, nil
}

func TestLiveSnapshotDoesNotStampOwnerReplacedDuringRead(t *testing.T) {
	owner := &liveSnapshotOwner{generation: "old"}
	controller := &Controller{
		generation: "old", projectionGate: make(controllerGate, 1),
		conversation: domain.ConversationRecord{ID: "conversation", ActiveBranchID: "branch"},
	}
	service := &Service{sessions: owner, controllers: map[domain.SessionID]*Controller{"session": controller}}
	reading := make(chan struct{})
	release := make(chan struct{})
	result := make(chan string, 1)
	go func() {
		_, generation, _, err := service.readLiveSnapshot(context.Background(), "session", "old",
			func(context.Context) (ConversationRows, error) {
				close(reading)
				<-release
				return ConversationRows{Conversation: controller.conversation}, nil
			})
		if err != nil {
			t.Errorf("snapshot: %v", err)
		}
		result <- generation
	}()
	<-reading
	// A replacement commits its fence before registry publication. The pointer
	// and branch are unchanged, so registry identity alone cannot detect this.
	owner.mu.Lock()
	owner.generation = "replacement"
	owner.mu.Unlock()
	close(release)
	if generation := <-result; generation != "" {
		t.Fatalf("old controller stamped replacement history with %q", generation)
	}
}
