package chat

import (
	"context"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	liveMaxEvents = 4096
	liveMaxBytes  = 1024 * 1024
	liveQueueSize = 4096
)

// LiveEvent is transient text observed before its durable projection. Its
// sequence belongs to one controller generation, not the database timeline.
type LiveEvent struct {
	Sequence       int64
	Kind           ports.ChatEventKind
	ProviderItemID string
	ProviderTurnID string
	Delta          string
	Text           string
	CreatedAt      time.Time
}

// LiveFrame contains observations after AfterSequence. A gap between the
// client's cursor and AfterSequence requires a fresh durable snapshot.
type LiveFrame struct {
	Generation     string
	ConversationID string
	BranchID       string
	AfterSequence  int64
	Sequence       int64
	// ResetSequence identifies the last failed/rejected projection. Clients
	// refresh their durable checkpoint so failed previews cannot linger.
	ResetSequence int64
	Events        []LiveEvent
}

type sequencedChatEvent struct {
	ports.ChatEvent
	sequence int64
}

type liveJournal struct {
	mu             sync.Mutex
	generation     string
	conversationID string
	branchID       string
	sequence       int64
	resetSequence  int64
	floor          int64
	bytes          int
	events         []LiveEvent
	subscribers    map[*LiveSubscription]struct{}
	closed         bool
}

// LiveSubscription reads a controller's bounded in-memory journal. Changed is
// an edge notification: callers take Snapshot after waking, and on closure.
type LiveSubscription struct {
	journal *liveJournal
	changed chan struct{}
}

// SubscribeLive validates the persisted mode and subscribes to its current
// controller. Later frames use memory only, independently of SQLite progress.
func (s *Service) SubscribeLive(ctx context.Context, sessionID domain.SessionID) (*LiveSubscription, error) {
	record, err := s.requireChatSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	controller, err := s.Controller(sessionID)
	if err != nil {
		return nil, err
	}
	if controller.generation != record.Metadata.ControllerGeneration {
		return nil, ErrNoController
	}
	return controller.live.subscribe(), nil
}

func (j *liveJournal) subscribe() *LiveSubscription {
	j.mu.Lock()
	defer j.mu.Unlock()
	sub := &LiveSubscription{journal: j, changed: make(chan struct{}, 1)}
	if j.closed {
		close(sub.changed)
	} else {
		j.subscribers[sub] = struct{}{}
	}
	return sub
}

// Changed closes when the controller has drained its provider stream or the
// subscription closes. Slow subscribers never block provider consumption.
func (s *LiveSubscription) Changed() <-chan struct{} { return s.changed }

// Snapshot copies the available observations so the caller owns its frame.
func (s *LiveSubscription) Snapshot(after int64) LiveFrame {
	j := s.journal
	j.mu.Lock()
	defer j.mu.Unlock()
	frame := LiveFrame{
		Generation: j.generation, ConversationID: j.conversationID,
		BranchID: j.branchID, AfterSequence: max(after, j.floor), Sequence: j.sequence,
		ResetSequence: j.resetSequence,
	}
	for _, event := range j.events {
		if event.Sequence > frame.AfterSequence {
			frame.Events = append(frame.Events, event)
		}
	}
	return frame
}

// Close releases the subscriber without changing the provider or its journal.
func (s *LiveSubscription) Close() {
	j := s.journal
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.subscribers[s]; ok {
		delete(j.subscribers, s)
		close(s.changed)
	}
}

func (j *liveJournal) observe(event ports.ChatEvent, now time.Time) sequencedChatEvent {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.sequence++
	text := event.ProviderItemID != "" && (event.Kind == ports.ChatEventMessageDelta || event.Kind == ports.ChatEventMessageCompleted)
	fresh := event.ProviderEventID == "" || event.ProviderEventFresh
	// Replayed identified text needs durable deduplication. Fresh output after
	// that replay also needs its committed prefix before it can be appended.
	if text && !fresh {
		j.floor, j.events, j.bytes = j.sequence, nil, 0
	}
	preview := event.Err == nil && fresh && (text || event.Kind == ports.ChatEventTurnCompleted)
	if preview {
		live := LiveEvent{
			Sequence: j.sequence, Kind: event.Kind, ProviderItemID: event.ProviderItemID,
			ProviderTurnID: event.ProviderTurnID, Delta: event.Delta, Text: event.Text, CreatedAt: now,
		}
		j.events = append(j.events, live)
		j.bytes += liveEventBytes(live)
		for len(j.events) > liveMaxEvents || j.bytes > liveMaxBytes {
			oldest := j.events[0]
			j.bytes -= liveEventBytes(oldest)
			j.floor = oldest.Sequence
			j.events[0] = LiveEvent{}
			j.events = j.events[1:]
		}
	}
	j.notify()
	return sequencedChatEvent{ChatEvent: event, sequence: j.sequence}
}

// notify is called with j.mu held. An edge is enough because subscribers read
// the journal after waking rather than relying on one notification per event.
func (j *liveJournal) notify() {
	for sub := range j.subscribers {
		select {
		case sub.changed <- struct{}{}:
		default:
		}
	}
}

func (j *liveJournal) reset(sequence int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.resetSequence = sequence
	j.notify()
}

// discard removes previews that the stopped writer will never commit. Intake
// must already have stopped, so the returned checkpoint covers every preview.
func (j *liveJournal) discard() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.floor, j.resetSequence, j.events, j.bytes = j.sequence, j.sequence, nil, 0
	j.notify()
	return j.sequence
}

func liveEventBytes(event LiveEvent) int {
	return 128 + len(event.Kind) + len(event.ProviderItemID) + len(event.ProviderTurnID) + len(event.Delta) + len(event.Text)
}

func (j *liveJournal) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = true
	for sub := range j.subscribers {
		delete(j.subscribers, sub)
		close(sub.changed)
	}
}

// receiveLive is the only provider-stream consumer. Publication precedes the
// bounded persistence queue; sustained storage stalls eventually backpressure
// intake instead of retaining an unbounded second copy of provider output.
func (c *Controller) receiveLive(ctx context.Context, events chan<- sequencedChatEvent) {
	defer close(events)
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-c.conv.Events():
			if !open {
				return
			}
			received := c.live.observe(event, c.now())
			select {
			case events <- received:
			case <-ctx.Done():
				return
			}
		}
	}
}

// readLiveSnapshot prevents a provider commit from landing between the durable
// read and its processed cursor. Session validation precedes this lock: command
// dispatch may hold sendMu while accessing the store, and snapshots never take
// sendMu. A replaced controller cannot stamp a new owner's durable history.
func (s *Service) readLiveSnapshot(
	ctx context.Context,
	sessionID domain.SessionID,
	generation string,
	load func(context.Context) (ConversationRows, error),
) (ConversationRows, string, int64, error) {
	controller, _ := s.Controller(sessionID)
	if controller == nil || controller.generation != generation {
		rows, err := load(ctx)
		return rows, "", 0, err
	}
	if err := controller.projectionGate.lock(ctx); err != nil {
		return ConversationRows{}, "", 0, err
	}
	defer controller.projectionGate.unlock()
	rows, err := load(ctx)
	if err != nil {
		return rows, "", 0, err
	}
	// Ownership can commit before the replacement is published in the registry.
	// Recheck its durable fence after reading, not only the registry pointer.
	owner, found, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return rows, "", 0, err
	}
	current, _ := s.Controller(sessionID)
	if !found || owner.Metadata.ControllerGeneration != generation || domain.NormalizeSessionMode(owner.Mode) != domain.SessionModeChat ||
		current != controller || rows.Conversation.ID != controller.conversation.ID ||
		rows.Conversation.ActiveBranchID != controller.conversation.ActiveBranchID {
		return rows, "", 0, nil
	}
	return rows, generation, controller.liveSequence, nil
}
