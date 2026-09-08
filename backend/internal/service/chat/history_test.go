package chat_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// historyRecorder is a provider double that CAN do the history operations, so a test
// can tell "the driver refuses" from "the driver does not offer this at all". The
// plain fakeConversation implements none of the three, which is what the unsupported
// paths exercise.
type historyRecorder struct {
	*fakeConversation

	mu           sync.Mutex
	rolledBack   []string
	titles       []string
	forkedTo     string
	rollbackErr  error
	setTitleErr  error
	forkErr      error
	forkAnchors  []*string
	echoRenameTo string
}

func newHistoryRecorder() *historyRecorder {
	return &historyRecorder{fakeConversation: newFakeConversation(), forkedTo: "thread-forked"}
}

func (h *historyRecorder) Rollback(_ context.Context, providerTurnID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rollbackErr != nil {
		return h.rollbackErr
	}
	h.rolledBack = append(h.rolledBack, providerTurnID)
	return nil
}

func (h *historyRecorder) Fork(_ context.Context, anchor *string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if anchor == nil {
		h.forkAnchors = append(h.forkAnchors, nil)
	} else {
		anchorCopy := *anchor
		h.forkAnchors = append(h.forkAnchors, &anchorCopy)
	}
	if h.forkErr != nil {
		return "", h.forkErr
	}
	return h.forkedTo, nil
}

func (h *historyRecorder) lastForkAnchor() *string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.forkAnchors) == 0 {
		return nil
	}
	anchor := h.forkAnchors[len(h.forkAnchors)-1]
	if anchor == nil {
		return nil
	}
	anchorCopy := *anchor
	return &anchorCopy
}

// SetTitle records the name and, like the real provider, reports it back on the event
// stream. That echo is the only path by which a title reaches AO's rows.
func (h *historyRecorder) SetTitle(_ context.Context, title string) error {
	h.mu.Lock()
	if h.setTitleErr != nil {
		err := h.setTitleErr
		h.mu.Unlock()
		return err
	}
	h.titles = append(h.titles, title)
	echo := h.echoRenameTo
	h.mu.Unlock()

	if echo == "" {
		echo = title
	}
	h.emit(ports.ChatEvent{Kind: ports.ChatEventThreadRenamed, Title: echo})
	return nil
}

func (h *historyRecorder) rollbackTargets() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.rolledBack...)
}

func (h *historyRecorder) setTitles() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.titles...)
}

// refusedError is what a driver returns when the provider itself declined. The
// service classifies it structurally, which is what keeps an ordinary "not right now"
// from surfacing as an internal failure.
type refusedError struct{ msg string }

func (e refusedError) Error() string     { return e.msg }
func (e refusedError) ChatRefusal() bool { return true }

// completeTurn sends a message and drives it to completion, leaving one settled turn.
func completeTurn(t *testing.T, h *harness, text, providerTurn string) string {
	t.Helper()
	turn, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{
		Text:   text,
		Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send %q: %v", text, err)
	}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: providerTurn},
		ports.ChatEvent{
			Kind: ports.ChatEventMessageCompleted, ProviderTurnID: providerTurn,
			ProviderItemID: "msg-" + providerTurn, Text: "reply to " + text,
		},
		ports.ChatEvent{
			Kind: ports.ChatEventTurnCompleted, ProviderTurnID: providerTurn,
			TurnState: domain.TurnStateCompleted,
		},
	)
	return turn.ID
}

func requireMessageTexts(t *testing.T, messages []domain.ConversationMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(messages))
	for _, message := range messages {
		got = append(got, message.Text)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("message texts = %#v, want %#v", got, want)
	}
}

func requireBranchPoint(
	t *testing.T,
	snapshot chatsvc.Snapshot,
	turnID, previousBranchID, nextBranchID string,
) {
	t.Helper()
	for _, point := range snapshot.BranchPoints {
		if point.TurnID != turnID {
			continue
		}
		if point.PreviousBranchID != previousBranchID || point.NextBranchID != nextBranchID || point.Total != 2 {
			t.Fatalf("branch point for %s = %+v, want previous=%q next=%q total=2",
				turnID, point, previousBranchID, nextBranchID)
		}
		return
	}
	t.Fatalf("snapshot has no branch point for turn %s: %+v", turnID, snapshot.BranchPoints)
}

// The end-to-end shape of an undo: the provider is asked to forget, and AO's timeline
// stops showing what it forgot.
func TestRollbackDiscardsTheTurnAndEverythingAfterIt(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)
	ctx := context.Background()

	completeTurn(t, h, "first", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "second", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	discarded, err := h.svc.Rollback(ctx, testSession, second)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if discarded != 1 {
		t.Errorf("discarded = %d, want 1", discarded)
	}

	// The provider is named by ITS turn id, not AO's: they are different namespaces
	// and sending AO's would roll back nothing.
	if targets := recorder.rollbackTargets(); len(targets) != 1 || targets[0] != "provider-turn-2" {
		t.Fatalf("provider rollback targets = %v, want [provider-turn-2]", targets)
	}

	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 {
		t.Fatalf("messages = %d, want only the surviving turn's pair", len(snapshot.Messages))
	}
	for _, msg := range snapshot.Messages {
		if strings.Contains(msg.Text, "second") {
			t.Errorf("discarded message still in the timeline: %q", msg.Text)
		}
	}
}

func TestRollbackRemovesLaterLegacyCompactionState(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)
	ctx := context.Background()

	completeTurn(t, h, "first", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "second", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	// Builds before compaction turn correlation shipped stored this boundary with
	// no turn_id. It still belongs to the history after the second prompt.
	compactedAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if err := h.st.UpsertActivity(ctx, h.ctrl.ConversationID(), "", domain.ConversationActivity{
		ID: "legacy-compaction", Kind: domain.ActivityKindSystem,
		Status: domain.ActivityStatusCompleted, Summary: "Compacted history",
		Detail: []byte(`{"event":"compaction"}`), ProviderItemID: "legacy-compaction-item",
	}, compactedAt); err != nil {
		t.Fatalf("seed legacy compaction: %v", err)
	}
	if err := h.st.MarkCompacted(ctx, h.ctrl.ConversationID(), compactedAt); err != nil {
		t.Fatalf("mark compacted: %v", err)
	}

	if _, err := h.svc.Rollback(ctx, testSession, second); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot.Conversation.CompactedAt != nil {
		t.Fatalf("compactedAt = %v, want cleared after its history was rolled back", snapshot.Conversation.CompactedAt)
	}
	for _, activity := range snapshot.Activities {
		if activity.ID == "legacy-compaction" {
			t.Fatal("rolled-back legacy compaction remained visible")
		}
	}
}

// Refused, not raced. A rollback while the agent is mid-turn would leave AO hiding
// rows the agent is still writing into, so the check happens before the provider is
// asked at all.
func TestRollbackIsRefusedWhileATurnIsRunning(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)
	ctx := context.Background()

	turnID := completeTurn(t, h, "first", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })

	// A second turn that never completes: the agent is working.
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "still going", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	_, err := h.svc.Rollback(ctx, testSession, turnID)
	if !errors.Is(err, chatsvc.ErrTurnRunning) {
		t.Fatalf("err = %v, want ErrTurnRunning", err)
	}
	if targets := recorder.rollbackTargets(); len(targets) != 0 {
		t.Fatalf("provider was asked to roll back mid-turn: %v", targets)
	}

	// Nothing was hidden either. A refused rollback must leave the timeline alone.
	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	for _, turn := range snapshot.Turns {
		if turn.RolledBackAt != nil {
			t.Errorf("turn %s was marked rolled back by a refused rollback", turn.ID)
		}
	}
}

// A provider without the capability gets a typed answer the client can render as an
// absent affordance, following the Models precedent.
func TestRollbackReportsAnUnsupportedDriver(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	turnID := completeTurn(t, h, "first", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })

	_, err := h.svc.Rollback(ctx, testSession, turnID)
	if !errors.Is(err, chatsvc.ErrRollbackUnsupported) {
		t.Fatalf("err = %v, want ErrRollbackUnsupported", err)
	}
}

// A turn the provider never accepted holds no provider history. Hiding AO's rows for
// it would leave the agent remembering more than the timeline shows, which is the
// exact disagreement rollback exists to prevent.
func TestRollbackRefusesATurnTheProviderNeverAccepted(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)
	ctx := context.Background()

	completeTurn(t, h, "first", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })

	// Busy, so this one is recorded and queued rather than dispatched.
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "queued", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send queued: %v", err)
	}
	if queued.State != domain.TurnStateQueued {
		t.Fatalf("second send state = %q, want queued", queued.State)
	}

	_, err = h.svc.Rollback(ctx, testSession, queued.ID)
	// It is refused, though which refusal depends on whether the running turn is
	// noticed first; both are honest and neither is a 500.
	if !errors.Is(err, chatsvc.ErrTurnNotRollbackable) && !errors.Is(err, chatsvc.ErrTurnRunning) {
		t.Fatalf("err = %v, want ErrTurnNotRollbackable or ErrTurnRunning", err)
	}
}

// A turn id from nowhere is a 404-shaped answer, not a conflict.
func TestRollbackReportsAnUnknownTurn(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)

	_, err := h.svc.Rollback(context.Background(), testSession, "turn-that-never-was")
	if !errors.Is(err, domain.ErrNoConversationTurn) {
		t.Fatalf("err = %v, want domain.ErrNoConversationTurn", err)
	}
}

// The provider's own refusal must arrive as a conflict carrying its explanation. A
// generic failure would tell the user nothing they could act on.
func TestRollbackClassifiesAProviderRefusal(t *testing.T) {
	recorder := newHistoryRecorder()
	recorder.rollbackErr = refusedError{msg: "Cannot rollback while a turn is in progress."}
	h := newHarnessWithConversation(t, recorder)
	ctx := context.Background()

	turnID := completeTurn(t, h, "first", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })

	_, err := h.svc.Rollback(ctx, testSession, turnID)
	if !errors.Is(err, chatsvc.ErrProviderRefused) {
		t.Fatalf("err = %v, want ErrProviderRefused", err)
	}
	if !strings.Contains(err.Error(), "turn is in progress") {
		t.Errorf("err = %v, want the provider's explanation carried through", err)
	}

	// And AO hid nothing: the provider still remembers the turn.
	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 {
		t.Errorf("messages = %d, want the timeline untouched by a refused rollback",
			len(snapshot.Messages))
	}
}

// The title round trip: AO asks, the provider confirms on its own event, and only
// then does the session label move. Nothing is written optimistically.
func TestSetTitleFlowsThroughTheProviderIntoTheSessionName(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)
	ctx := context.Background()

	title, err := h.svc.SetTitle(ctx, testSession, "## \"Fix OAuth Return URL Loss.\"  ")
	if err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	// Normalized before it ever reaches the provider: heading markers, wrapper
	// quotes and trailing punctuation are model habits, not part of the title.
	if title != "Fix OAuth Return URL Loss" {
		t.Fatalf("normalized title = %q", title)
	}
	if titles := recorder.setTitles(); len(titles) != 1 || titles[0] != title {
		t.Fatalf("provider received %v, want [%q]", titles, title)
	}

	awaitSessionName(t, h, "Fix OAuth Return URL Loss")

	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot.Conversation.ProviderTitle != "Fix OAuth Return URL Loss" {
		t.Errorf("provider title = %q", snapshot.Conversation.ProviderTitle)
	}
}

// A title AO never asked for still lands: another client naming the thread is how a
// provider-derived title arrives at all.
func TestAProviderRenameFromElsewhereNamesTheSession(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)

	h.conv.emit(ports.ChatEvent{
		Kind:  ports.ChatEventThreadRenamed,
		Title: "Restore Canvas Renderer Fallback",
	})
	awaitSessionName(t, h, "Restore Canvas Renderer Fallback")
}

// The rule the user cares about: their own name is never taken away by a model.
func TestAProviderTitleDoesNotOverwriteAUserRename(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)
	ctx := context.Background()

	if renamed, err := h.st.RenameSession(ctx, testSession, "Mine", h.now()); err != nil || !renamed {
		t.Fatalf("rename: renamed=%v err=%v", renamed, err)
	}

	if _, err := h.svc.SetTitle(ctx, testSession, "Something Else Entirely"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	// The provider title is still recorded; only the label is left alone.
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.ProviderTitle == "Something Else Entirely"
	})
	rec, ok, err := h.st.GetSession(ctx, testSession)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if rec.DisplayName != "Mine" {
		t.Errorf("display name = %q, want the user's name to have survived", rec.DisplayName)
	}
}

// Clearing the thread name is not a reason to strip AO's label.
func TestAClearedProviderTitleLeavesTheSessionNameAlone(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)
	ctx := context.Background()

	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventThreadRenamed, Title: "First Name"})
	awaitSessionName(t, h, "First Name")

	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventThreadRenamed, Title: ""})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.ProviderTitle == ""
	})

	rec, ok, err := h.st.GetSession(ctx, testSession)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if rec.DisplayName != "First Name" {
		t.Errorf("display name = %q, want the label kept when the thread lost its name",
			rec.DisplayName)
	}
}

func TestSetTitleRefusesABlankTitle(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)

	if _, err := h.svc.SetTitle(context.Background(), testSession, "  ###  "); !errors.Is(err, chatsvc.ErrTitleRequired) {
		t.Fatalf("err = %v, want ErrTitleRequired", err)
	}
	if titles := recorder.setTitles(); len(titles) != 0 {
		t.Fatalf("provider was asked to set %v", titles)
	}
}

func TestSetTitleReportsAnUnsupportedDriver(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.SetTitle(context.Background(), testSession, "A Name"); !errors.Is(err, chatsvc.ErrRenameUnsupported) {
		t.Fatalf("err = %v, want ErrRenameUnsupported", err)
	}
}

func TestForkReturnsTheNewProviderConversationID(t *testing.T) {
	recorder := newHistoryRecorder()
	h := newHarnessWithConversation(t, recorder)

	forked, err := h.svc.ForkConversation(context.Background(), testSession)
	if err != nil {
		t.Fatalf("ForkConversation: %v", err)
	}
	if forked != "thread-forked" {
		t.Errorf("forked conversation = %q, want thread-forked", forked)
	}
}

func TestForkReportsAnUnsupportedDriver(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.ForkConversation(context.Background(), testSession); !errors.Is(err, chatsvc.ErrForkUnsupported) {
		t.Fatalf("err = %v, want ErrForkUnsupported", err)
	}
}

func TestForkClassifiesAProviderRefusal(t *testing.T) {
	recorder := newHistoryRecorder()
	recorder.forkErr = refusedError{msg: "lastTurnId identifies an in-progress turn"}
	h := newHarnessWithConversation(t, recorder)

	_, err := h.svc.ForkConversation(context.Background(), testSession)
	if !errors.Is(err, chatsvc.ErrProviderRefused) {
		t.Fatalf("err = %v, want ErrProviderRefused", err)
	}
}

type editDriverState struct {
	mu           sync.Mutex
	startCalls   int
	startConfigs []ports.ChatStartConfig
	resumeCalls  []ports.ChatResumeConfig
	startErr     error
	resumeErr    error
	beforeResume func(ports.ChatResumeConfig) error
	fresh        *fakeConversation
	resumed      map[string]*fakeConversation
}

func newEditHarness(t *testing.T, supportsPromptReplay bool) (*harness, *historyRecorder, *editDriverState) {
	return newEditHarnessWithStore(
		t, supportsPromptReplay, func(st *store.Store) chatsvc.Store { return st })
}

func newEditHarnessWithStore(
	t *testing.T,
	supportsPromptReplay bool,
	wrapStore func(*store.Store) chatsvc.Store,
) (*harness, *historyRecorder, *editDriverState) {
	return newEditHarnessWithStoreAndReader(
		t, supportsPromptReplay, wrapStore, func(reader chatsvc.SnapshotReader) chatsvc.SnapshotReader {
			return reader
		})
}

func newEditHarnessWithStoreAndReader(
	t *testing.T,
	supportsPromptReplay bool,
	wrapStore func(*store.Store) chatsvc.Store,
	wrapReader func(chatsvc.SnapshotReader) chatsvc.SnapshotReader,
) (*harness, *historyRecorder, *editDriverState) {
	return newEditHarnessWithOptions(t, supportsPromptReplay, wrapStore, wrapReader, nil)
}

func newEditHarnessWithControllerEnv(
	t *testing.T,
	supportsPromptReplay bool,
	prepare func(context.Context, domain.SessionControllerOwner) (map[string]string, error),
) (*harness, *historyRecorder, *editDriverState) {
	return newEditHarnessWithOptions(t, supportsPromptReplay,
		func(st *store.Store) chatsvc.Store { return st },
		func(reader chatsvc.SnapshotReader) chatsvc.SnapshotReader { return reader }, prepare)
}

func newEditHarnessWithOptions(
	t *testing.T,
	supportsPromptReplay bool,
	wrapStore func(*store.Store) chatsvc.Store,
	wrapReader func(chatsvc.SnapshotReader) chatsvc.SnapshotReader,
	prepare func(context.Context, domain.SessionControllerOwner) (map[string]string, error),
) (*harness, *historyRecorder, *editDriverState) {
	t.Helper()
	st := openStore(t)
	source := newHistoryRecorder()
	if supportsPromptReplay {
		sourceCapabilities := productionCaps()
		sourceCapabilities[ports.ChatCapabilityPromptReplay] = true
		sourceCapabilities[ports.ChatCapabilityEmbeddedContext] = true
		source.setCapabilities(sourceCapabilities)
	}
	fresh := newFakeConversation()
	fresh.providerConversationID = "thread-fresh"
	fresh.turnSeq = 100
	if supportsPromptReplay {
		fresh.setCapabilities(source.Capabilities())
	}
	forked := newFakeConversation()
	forked.providerConversationID = "thread-forked"
	forked.turnSeq = 200
	root := newFakeConversation()
	root.providerConversationID = "thread-1"
	root.turnSeq = 300
	state := &editDriverState{
		fresh: fresh,
		resumed: map[string]*fakeConversation{
			"thread-forked": forked,
			"thread-1":      root,
		},
	}
	initial := ports.ChatConversation(source)
	if supportsPromptReplay {
		// Use the plain conversation for this scenario: historyRecorder implements
		// native fork, while the real Claude path does not.
		initial = source.fakeConversation
	}
	driver := fakeDriver{conv: initial}
	driver.start = func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.startConfigs = append(state.startConfigs, cfg)
		state.startCalls++
		if state.startCalls == 1 {
			return initial, nil
		}
		if state.startErr != nil {
			return nil, state.startErr
		}
		if state.startCalls == 2 {
			return state.fresh, nil
		}
		freshBranch := newFakeConversation()
		freshBranch.providerConversationID = fmt.Sprintf("thread-fresh-%d", state.startCalls)
		freshBranch.turnSeq = state.startCalls * 100
		if supportsPromptReplay {
			freshBranch.setCapabilities(source.Capabilities())
		}
		state.fresh = freshBranch
		return freshBranch, nil
	}
	driver.resume = func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
		state.mu.Lock()
		state.resumeCalls = append(state.resumeCalls, cfg)
		if state.resumeErr != nil {
			state.mu.Unlock()
			return nil, state.resumeErr
		}
		beforeResume := state.beforeResume
		conv := state.resumed[cfg.ProviderConversationID]
		state.mu.Unlock()
		if beforeResume != nil {
			if err := beforeResume(cfg); err != nil {
				return nil, err
			}
		}
		if conv == nil {
			return nil, errors.New("unexpected provider conversation: " + cfg.ProviderConversationID)
		}
		return conv, nil
	}

	clock := time.Date(2026, 8, 9, 15, 0, 0, 0, time.UTC)
	activity := &recordingActivity{}
	var idMu sync.Mutex
	nextID := 0
	reader := chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
		snapshot, err := st.LoadConversationSnapshot(ctx, conversationID)
		if err != nil {
			return chatsvc.ConversationRows{}, err
		}
		return chatsvc.ConversationRows{
			Conversation: snapshot.Conversation, ActiveBranch: snapshot.ActiveBranch,
			Turns: snapshot.Turns, Messages: snapshot.Messages,
			Activities: snapshot.Activities, BranchPoints: snapshot.BranchPoints,
			BranchedFromEarlierMessage: snapshot.BranchedFromEarlierMessage,
		}, nil
	})
	svc := chatsvc.New(chatsvc.Options{
		Store: wrapStore(st), Sessions: st,
		Reader:   wrapReader(reader),
		Drivers:  fakeRegistry{driver: driver},
		Log:      slog.New(slog.DiscardHandler),
		Activity: activity,
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			nextID++
			return fmt.Sprintf("edit-id-%d", nextID)
		},
		Now: func() time.Time { return clock },
	})
	workspace := t.TempDir()
	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: workspace,
		Env:          map[string]string{"AO_EDIT_TEST": "yes", "AO_BROWSER_CAPABILITY": "stale"},
		SystemPrompt: "preserved prompt", PrepareControllerEnv: prepare,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, found, err := st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	record.Metadata.ProviderConversationID = "thread-1"
	record.Metadata.ControllerGeneration = ctrl.Generation()
	if err := st.UpdateSession(context.Background(), record); err != nil {
		t.Fatalf("UpdateSession provider controller: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	return &harness{
		svc: svc, st: st, conv: source.fakeConversation, ctrl: ctrl,
		activity: activity, clock: clock,
	}, source, state
}

func TestEditAndBranchActivationRotateControllerCredentialsAfterStoppingSource(t *testing.T) {
	var mu sync.Mutex
	prepareCalls := 0
	var expectedStopped *chatsvc.Controller
	prepare := func(_ context.Context, _ domain.SessionControllerOwner) (map[string]string, error) {
		mu.Lock()
		defer mu.Unlock()
		prepareCalls++
		if expectedStopped != nil && expectedStopped.State() != ports.ChatControllerStopped {
			t.Fatalf("credential rotation %d ran while source state was %q", prepareCalls, expectedStopped.State())
		}
		return map[string]string{
			"AO_EDIT_TEST":          "yes",
			"AO_BROWSER_CAPABILITY": fmt.Sprintf("token-%d", prepareCalls),
		}, nil
	}
	h, _, driver := newEditHarnessWithControllerEnv(t, true, prepare)
	ctx := context.Background()
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })

	mu.Lock()
	expectedStopped = h.ctrl
	mu.Unlock()
	edited, err := h.svc.EditMessage(ctx, testSession, first, ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "credential-edit", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	driver.mu.Lock()
	if got := driver.startConfigs[len(driver.startConfigs)-1].Env["AO_BROWSER_CAPABILITY"]; got != "token-2" {
		driver.mu.Unlock()
		t.Fatalf("edited controller capability = %q, want token-2", got)
	}
	driver.mu.Unlock()
	driver.fresh.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-101"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-101", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		for _, turn := range snapshot.Turns {
			if turn.ID == edited.Turn.ID {
				return turn.State.Terminal()
			}
		}
		return false
	})
	current, err := h.svc.Controller(testSession)
	if err != nil {
		t.Fatalf("Controller after edit: %v", err)
	}
	mu.Lock()
	expectedStopped = current
	mu.Unlock()
	if _, err := h.svc.ActivateBranch(ctx, testSession, edited.SourceBranchID); err != nil {
		t.Fatalf("ActivateBranch: %v", err)
	}
	driver.mu.Lock()
	lastResume := driver.resumeCalls[len(driver.resumeCalls)-1]
	driver.mu.Unlock()
	if got := lastResume.Env["AO_BROWSER_CAPABILITY"]; got != "token-3" {
		t.Fatalf("activated controller capability = %q, want token-3", got)
	}
	mu.Lock()
	gotCalls := prepareCalls
	mu.Unlock()
	if gotCalls != 3 {
		t.Fatalf("credential rotations = %d, want initial start + edit + activation", gotCalls)
	}
}

func TestEditMessageReplaysDurableContextWhenNativeForkIsUnavailable(t *testing.T) {
	h, _, driver := newEditHarness(t, true)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "B", Origin: domain.MessageOriginHuman})
	if err != nil {
		t.Fatalf("Send B: %v", err)
	}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-2"},
		ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "provider-turn-2", ProviderItemID: "answer-b", Text: "answer B"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-2", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	if _, err := h.svc.EditMessage(ctx, testSession, second.ID, ports.ChatUserMessage{
		Text: "B edited", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	driver.mu.Lock()
	starts := append([]ports.ChatStartConfig(nil), driver.startConfigs...)
	driver.mu.Unlock()
	if len(starts) != 2 {
		t.Fatalf("start calls = %d, want initial plus replay", len(starts))
	}
	if starts[0].ProviderScopeID == "" {
		t.Fatalf("initial start has no provider scope: %#v", starts[0])
	}
	if starts[1].SystemPrompt != "preserved prompt" || starts[1].ProviderScopeID == "" ||
		starts[1].ProviderScopeID == starts[0].ProviderScopeID {
		t.Fatalf("approximate start config = %#v", starts[1])
	}
	sent := driver.fresh.sentMessages()
	if len(sent) != 1 || len(sent[0].Content) != 1 || !strings.Contains(sent[0].Content[0].Text, "reply to A") || strings.Contains(sent[0].Content[0].Text, "answer B") {
		t.Fatalf("replay content = %#v", sent)
	}
}

func TestEditMessageRejectsReplayWhenFreshProviderNegotiatesFewerCapabilities(t *testing.T) {
	h, _, driver := newEditHarness(t, true)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		for _, turn := range snapshot.Turns {
			if turn.ID == second {
				return turn.State == domain.TurnStateCompleted
			}
		}
		return false
	})
	before, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot before edit: %v", err)
	}

	capabilities := driver.fresh.Capabilities()
	delete(capabilities, ports.ChatCapabilityEmbeddedContext)
	driver.fresh.setCapabilities(capabilities)

	edit := ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "fresh-capability-rejection", Origin: domain.MessageOriginHuman,
	}
	_, err = h.svc.EditMessage(ctx, testSession, second, edit)
	if !errors.Is(err, chatsvc.ErrForkUnsupported) {
		t.Fatalf("EditMessage error = %v, want ErrForkUnsupported", err)
	}
	driver.mu.Lock()
	startsAfterRejection := driver.startCalls
	driver.mu.Unlock()
	_, retryErr := h.svc.EditMessage(ctx, testSession, second, edit)
	if !errors.Is(retryErr, chatsvc.ErrForkUnsupported) {
		t.Fatalf("same-controller replay error = %v, want ErrForkUnsupported", retryErr)
	}
	driver.mu.Lock()
	startsAfterReplay := driver.startCalls
	driver.mu.Unlock()
	if startsAfterReplay != startsAfterRejection {
		t.Fatalf("same-controller replay started provider: calls %d -> %d", startsAfterRejection, startsAfterReplay)
	}
	after, snapshotErr := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if snapshotErr != nil {
		t.Fatalf("LoadConversationSnapshot after edit: %v", snapshotErr)
	}
	if after.ActiveBranch.ID != before.ActiveBranch.ID {
		t.Fatalf("active branch = %q, want source %q", after.ActiveBranch.ID, before.ActiveBranch.ID)
	}
	if sent := driver.fresh.sentMessages(); len(sent) != 0 {
		t.Fatalf("fresh provider received %d replay sends, want none: %#v", len(sent), sent)
	}
	branches, branchErr := h.st.ConversationBranches(ctx, h.ctrl.ConversationID())
	if branchErr != nil {
		t.Fatalf("ConversationBranches: %v", branchErr)
	}
	if len(branches) != 1 {
		t.Fatalf("branches = %d, want only source after capability refusal: %#v", len(branches), branches)
	}

	restarted, provider, driverCalls := restartEditServiceWithDriverCalls(t, h)
	startsBefore, resumesBefore := driverCalls.counts()
	_, restartErr := restarted.EditMessage(ctx, testSession, second, edit)
	if !errors.Is(restartErr, chatsvc.ErrForkUnsupported) {
		t.Fatalf("restart replay error = %v, want ErrForkUnsupported", restartErr)
	}
	startsAfter, resumesAfter := driverCalls.counts()
	if startsAfter != startsBefore || resumesAfter != resumesBefore {
		t.Fatalf("restart replay reached driver: starts %d->%d resumes %d->%d",
			startsBefore, startsAfter, resumesBefore, resumesAfter)
	}
	if sends := provider.sendCallCount(); sends != 0 {
		t.Fatalf("restart replay sent %d prompts, want none", sends)
	}
}

func TestEditMessageReportsUndispatchedReplayPreparationFailureAsRejected(t *testing.T) {
	var failReplay atomic.Bool
	h, _, driver := newEditHarnessWithStoreAndReader(
		t,
		true,
		func(st *store.Store) chatsvc.Store { return st },
		func(reader chatsvc.SnapshotReader) chatsvc.SnapshotReader {
			return chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
				if failReplay.Load() {
					return chatsvc.ConversationRows{}, errors.New("read replay transcript")
				}
				return reader.LoadConversationSnapshot(ctx, conversationID)
			})
		},
	)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool { return len(snapshot.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool { return len(snapshot.Messages) == 4 })
	failReplay.Store(true)

	edit := ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "replay-preparation-rejection", Origin: domain.MessageOriginHuman,
	}
	_, err := h.svc.EditMessage(ctx, testSession, second, edit)
	if !errors.Is(err, chatsvc.ErrEditDeliveryRejected) {
		t.Fatalf("first response error = %v, want ErrEditDeliveryRejected", err)
	}
	if errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("first response error = %v, must not claim delivery uncertainty", err)
	}
	_, retryErr := h.svc.EditMessage(ctx, testSession, second, edit)
	if !errors.Is(retryErr, chatsvc.ErrEditDeliveryRejected) {
		t.Fatalf("same-ID replay error = %v, want ErrEditDeliveryRejected", retryErr)
	}
	driver.mu.Lock()
	startCalls := driver.startCalls
	driver.mu.Unlock()
	if startCalls != 1 {
		t.Fatalf("provider Start calls = %d, want no replacement provider start", startCalls)
	}
}

func TestEditMessageKeepsReplacementPrivateUntilEditedPromptIsRecorded(t *testing.T) {
	h, _, driver := newEditHarness(t, true)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	sendStarted := make(chan struct{})
	releaseSend := make(chan struct{})
	var startedOnce sync.Once
	driver.fresh.mu.Lock()
	driver.fresh.onSend = func(providerTurnID string) {
		driver.fresh.emit(
			ports.ChatEvent{
				Kind: ports.ChatEventApprovalRequested, ProviderTurnID: providerTurnID,
				ProviderItemID: "replacement-approval", RequestID: "replacement-approval-request",
				ActivityKind: domain.ActivityKindCommand, ActivityStatus: domain.ActivityStatusPending,
				Summary: "Approve replacement",
			},
			ports.ChatEvent{
				Kind: ports.ChatEventInputRequested, ProviderTurnID: providerTurnID,
				ProviderItemID: "replacement-input", RequestID: "replacement-input-request",
				Input: &ports.ChatInputRequest{
					Mode: ports.ChatInputModeForm, Message: "Replacement input",
					Schema: map[string]any{"type": "object"},
				},
			},
		)
		startedOnce.Do(func() { close(sendStarted) })
		<-releaseSend
	}
	driver.fresh.mu.Unlock()

	type editOutcome struct {
		result chatsvc.EditMessageResult
		err    error
	}
	editDone := make(chan editOutcome, 1)
	go func() {
		result, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
			Text: "B edited", ClientMessageID: "edit-private-bootstrap", Origin: domain.MessageOriginHuman,
		})
		editDone <- editOutcome{result: result, err: err}
	}()
	select {
	case <-sendStarted:
	case <-time.After(3 * time.Second):
		close(releaseSend)
		t.Fatal("edited prompt did not reach the replacement provider")
	}

	concurrentDone := make(chan error, 1)
	go func() {
		_, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
			Text: "unrelated prompt", ClientMessageID: "concurrent-during-edit", Origin: domain.MessageOriginHuman,
		})
		concurrentDone <- err
	}()
	select {
	case err := <-concurrentDone:
		if !errors.Is(err, chatsvc.ErrControllerHandoff) {
			close(releaseSend)
			t.Fatalf("concurrent send error = %v, want ErrControllerHandoff", err)
		}
	case <-time.After(250 * time.Millisecond):
		close(releaseSend)
		<-editDone
		<-concurrentDone
		t.Fatal("concurrent send reached the unpublished replacement controller")
	}

	close(releaseSend)
	outcome := <-editDone
	if outcome.err != nil {
		t.Fatalf("EditMessage: %v", outcome.err)
	}
	if got := driver.fresh.sentTexts(); len(got) != 1 || got[0] != "B edited" {
		t.Fatalf("replacement provider received %v, want only edited prompt", got)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, activity := range s.Activities {
			if activity.RequestID == "replacement-input-request" {
				return true
			}
		}
		return false
	})
	if got := turnStateByText(t, snapshot)["B edited"]; got != domain.TurnStateRunning {
		t.Fatalf("replacement turn after source shutdown = %q, want running", got)
	}
	for _, requestID := range []string{"replacement-approval-request", "replacement-input-request"} {
		found := false
		for _, activity := range snapshot.Activities {
			if activity.RequestID != requestID {
				continue
			}
			found = true
			if activity.Status != domain.ActivityStatusPending {
				t.Errorf("replacement request %s status = %q, want pending", requestID, activity.Status)
			}
		}
		if !found {
			t.Errorf("replacement request %s is missing", requestID)
		}
	}
}

func TestStartRestoresSourceAfterCrashBeforeEditedPromptWasRecorded(t *testing.T) {
	h, _, _ := newEditHarness(t, true)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	conversation, err := h.st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	anchor, err := h.st.ConversationEditAnchor(ctx, conversation.ID, second)
	if err != nil {
		t.Fatalf("ConversationEditAnchor: %v", err)
	}
	child := domain.ConversationBranch{
		ID: "crashed-empty-edit", ConversationID: conversation.ID, SessionID: testSession,
		ProviderConversationID: "thread-crashed-empty", ProviderScopeID: "crashed-empty-scope",
		ParentBranchID: anchor.SourceBranchID, ReplacedTurnID: second,
		ForkAfterSequence: anchor.ForkAfterSequence, Strategy: domain.ConversationBranchStrategyApproximateContext,
		ReplayCutoffSequence: anchor.ForkAfterSequence, CreatedAt: h.clock,
	}
	if err := h.st.CreateAndActivateConversationBranch(
		ctx, testSession, child, "crashed-empty-generation", h.clock); err != nil {
		t.Fatalf("CreateAndActivateConversationBranch: %v", err)
	}
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("Stop before restart: %v", err)
	}
	record, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}

	var resumed ports.ChatResumeConfig
	restartDriver := fakeDriver{conv: newFakeConversation()}
	restartDriver.resume = func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
		resumed = cfg
		conv := newFakeConversation()
		conv.providerConversationID = cfg.ProviderConversationID
		return conv, nil
	}
	restarted := newRestartedEditService(t, h, restartDriver, "empty-edit-restart-id")
	if _, err := restarted.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ProviderConversationID: record.Metadata.ProviderConversationID,
	}); err != nil {
		t.Fatalf("Start after empty edit crash: %v", err)
	}
	if resumed.ProviderConversationID != "thread-1" {
		t.Fatalf("resumed provider conversation = %q, want source thread-1", resumed.ProviderConversationID)
	}
	after, err := h.st.ConversationForSession(ctx, testSession)
	if err != nil || after.ActiveBranchID != anchor.SourceBranchID {
		t.Fatalf("active branch after recovery = %q, want source %q, err=%v",
			after.ActiveBranchID, anchor.SourceBranchID, err)
	}
	afterRecord, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found || afterRecord.Metadata.ProviderConversationID != "thread-1" {
		t.Fatalf("session provider after recovery = %q, found=%v err=%v",
			afterRecord.Metadata.ProviderConversationID, found, err)
	}
}

func TestStartLinksDurableEditedPromptAfterCrashBeforeBranchLink(t *testing.T) {
	h, _, _ := newEditHarness(t, true)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	conversation, err := h.st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	anchor, err := h.st.ConversationEditAnchor(ctx, conversation.ID, second)
	if err != nil {
		t.Fatalf("ConversationEditAnchor: %v", err)
	}
	child := domain.ConversationBranch{
		ID: "crashed-linked-edit", ConversationID: conversation.ID, SessionID: testSession,
		ProviderConversationID: "thread-crashed-linked", ProviderScopeID: "crashed-linked-scope",
		ParentBranchID: anchor.SourceBranchID, ReplacedTurnID: second,
		ForkAfterSequence: anchor.ForkAfterSequence, Strategy: domain.ConversationBranchStrategyApproximateContext,
		ReplayCutoffSequence: anchor.ForkAfterSequence, CreatedAt: h.clock,
	}
	const replacementTurnID = "durable-unlinked-edit-turn"
	if err := h.st.CreateAndActivateConversationBranch(
		ctx, testSession, child, "crashed-linked-generation", h.clock); err != nil {
		t.Fatalf("CreateAndActivateConversationBranch: %v", err)
	}
	created, err := h.st.AppendUserMessage(ctx, conversation.ID, testSession,
		"crashed-linked-generation", domain.ConversationMessage{
			ID: "durable-unlinked-edit-message", Text: "B edited", Origin: domain.MessageOriginHuman,
			ClientMessageID: "durable-unlinked-edit-client",
		}, replacementTurnID, h.clock)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage: created=%v err=%v", created, err)
	}
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("Stop before restart: %v", err)
	}
	record, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}

	var resumed ports.ChatResumeConfig
	restartDriver := fakeDriver{conv: newFakeConversation()}
	restartDriver.resume = func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
		resumed = cfg
		conv := newFakeConversation()
		conv.providerConversationID = cfg.ProviderConversationID
		return conv, nil
	}
	restarted := newRestartedEditService(t, h, restartDriver, "linked-edit-restart-id")
	if _, err := restarted.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ProviderConversationID: record.Metadata.ProviderConversationID,
	}); err != nil {
		t.Fatalf("Start after unlinked edit crash: %v", err)
	}
	if resumed.ProviderConversationID != child.ProviderConversationID {
		t.Fatalf("resumed provider conversation = %q, want child %q",
			resumed.ProviderConversationID, child.ProviderConversationID)
	}
	repaired, err := h.st.ConversationBranch(ctx, conversation.ID, child.ID)
	if err != nil || repaired.ReplacementTurnID != replacementTurnID || !repaired.Active {
		t.Fatalf("repaired child = %+v, err=%v", repaired, err)
	}
}

func newRestartedEditService(
	t *testing.T,
	h *harness,
	driver ports.ChatDriver,
	newID string,
) *chatsvc.Service {
	t.Helper()
	restarted := chatsvc.New(chatsvc.Options{
		Store: h.st, Sessions: h.st,
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			snapshot, err := h.st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation: snapshot.Conversation, ActiveBranch: snapshot.ActiveBranch,
				Turns: snapshot.Turns, Messages: snapshot.Messages,
				Activities: snapshot.Activities, BranchPoints: snapshot.BranchPoints,
				BranchedFromEarlierMessage: snapshot.BranchedFromEarlierMessage,
			}, nil
		}),
		Drivers: fakeRegistry{driver: driver}, Log: slog.New(slog.DiscardHandler),
		NewID: func() string { return newID },
		Now:   func() time.Time { return h.clock },
	})
	t.Cleanup(func() { _ = restarted.Stop(context.Background(), testSession) })
	return restarted
}

func TestEditMessageAmbiguousApproximateFailureRemainsNavigableAcrossRestart(t *testing.T) {
	h, _, driver := newEditHarness(t, true)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	driver.fresh.mu.Lock()
	driver.fresh.sendErr = errors.New("provider unavailable")
	driver.fresh.mu.Unlock()
	failed, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "edit-b-failed-replay", Origin: domain.MessageOriginHuman,
	})
	if err == nil {
		t.Fatal("EditMessage succeeded after approximate provider send failure")
	}
	if failed.Turn.ID == "" {
		t.Fatalf("failed edit has no durable turn: %+v", failed)
	}
	failedSnapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("Snapshot failed edit: %v", err)
	}
	requireMessageTexts(t, failedSnapshot.Messages, []string{"A", "reply to A", "B edited"})
	requireBranchPoint(t, failedSnapshot, failed.Turn.ID, failed.SourceBranchID, "")

	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("Stop before restart: %v", err)
	}
	record, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	restartDriver := fakeDriver{conv: newFakeConversation()}
	restartDriver.resume = func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
		resumed := newFakeConversation()
		resumed.providerConversationID = cfg.ProviderConversationID
		return resumed, nil
	}
	restarted := chatsvc.New(chatsvc.Options{
		Store: h.st, Sessions: h.st,
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			snapshot, loadErr := h.st.LoadConversationSnapshot(ctx, conversationID)
			if loadErr != nil {
				return chatsvc.ConversationRows{}, loadErr
			}
			return chatsvc.ConversationRows{
				Conversation: snapshot.Conversation, ActiveBranch: snapshot.ActiveBranch,
				Turns: snapshot.Turns, Messages: snapshot.Messages,
				Activities: snapshot.Activities, BranchPoints: snapshot.BranchPoints,
				BranchedFromEarlierMessage: snapshot.BranchedFromEarlierMessage,
			}, nil
		}),
		Drivers: fakeRegistry{driver: restartDriver}, Log: slog.New(slog.DiscardHandler),
		NewID: func() string { return "failed-edit-restart-id" },
		Now:   func() time.Time { return h.clock },
	})
	t.Cleanup(func() { _ = restarted.Stop(context.Background(), testSession) })
	if _, err := restarted.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ProviderConversationID: record.Metadata.ProviderConversationID,
	}); err != nil {
		t.Fatalf("Start after failed edit: %v", err)
	}
	restartedSnapshot, err := restarted.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("Snapshot after restart: %v", err)
	}
	if restartedSnapshot.ActiveBranch.ID != failed.ActiveBranchID {
		t.Fatalf("active branch after restart = %q, want %q", restartedSnapshot.ActiveBranch.ID, failed.ActiveBranchID)
	}
	requireMessageTexts(t, restartedSnapshot.Messages, []string{"A", "reply to A", "B edited"})
	requireBranchPoint(t, restartedSnapshot, failed.Turn.ID, failed.SourceBranchID, "")
}

type failEditCompletionStore struct{ chatsvc.Store }

func (s *failEditCompletionStore) CompleteEditDelivery(
	context.Context,
	string,
	string,
	string,
	string,
	domain.ConversationTurn,
	time.Time,
) error {
	return errors.New("injected edit completion failure")
}

type failEditOperationStore struct {
	chatsvc.Store

	mu                    sync.Mutex
	bindErr               error
	bindFailures          int
	branchInstallErr      error
	branchInstallFailures int
}

func (s *failEditOperationStore) BindTurnToProvider(
	ctx context.Context,
	turnID, providerTurnID string,
	now time.Time,
) error {
	s.mu.Lock()
	err := s.bindErr
	if err != nil {
		s.bindFailures++
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Store.BindTurnToProvider(ctx, turnID, providerTurnID, now)
}

func (s *failEditOperationStore) CreateAndActivateConversationBranch(
	ctx context.Context,
	sessionID domain.SessionID,
	branch domain.ConversationBranch,
	generation string,
	now time.Time,
) error {
	s.mu.Lock()
	err := s.branchInstallErr
	if err != nil {
		s.branchInstallFailures++
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Store.CreateAndActivateConversationBranch(ctx, sessionID, branch, generation, now)
}

type editRestartDriverCalls struct {
	mu      sync.Mutex
	starts  int
	resumes int
}

func (c *editRestartDriverCalls) counts() (starts, resumes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts, c.resumes
}

func restartEditService(
	t *testing.T,
	h *harness,
) (*chatsvc.Service, *historyRecorder) {
	t.Helper()
	svc, provider, _ := restartEditServiceWithDriverCalls(t, h)
	return svc, provider
}

func restartEditServiceWithDriverCalls(
	t *testing.T,
	h *harness,
) (*chatsvc.Service, *historyRecorder, *editRestartDriverCalls) {
	t.Helper()
	controller, err := h.svc.Controller(testSession)
	if err != nil {
		t.Fatalf("Controller before restart: %v", err)
	}
	providerConversationID := controller.ProviderConversationID()
	if err := h.svc.Stop(context.Background(), testSession); err != nil {
		t.Fatalf("stop original edit service: %v", err)
	}
	provider := newHistoryRecorder()
	provider.providerConversationID = providerConversationID
	driverCalls := &editRestartDriverCalls{}
	driver := fakeDriver{conv: provider}
	driver.start = func(ports.ChatStartConfig) (ports.ChatConversation, error) {
		driverCalls.mu.Lock()
		driverCalls.starts++
		driverCalls.mu.Unlock()
		return provider, nil
	}
	driver.resume = func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
		driverCalls.mu.Lock()
		driverCalls.resumes++
		driverCalls.mu.Unlock()
		return provider, nil
	}
	var (
		idMu sync.Mutex
		id   int
	)
	svc := chatsvc.New(chatsvc.Options{
		Store: h.st, Sessions: h.st,
		Drivers: fakeRegistry{driver: driver},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			id++
			return fmt.Sprintf("restart-edit-%d", id)
		},
		Now: h.now,
	})
	if _, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: providerConversationID,
	}); err != nil {
		t.Fatalf("restart edit service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	return svc, provider, driverCalls
}

func requireUncertainEditReplayAfterRestart(
	t *testing.T,
	h *harness,
	turnID string,
	msg ports.ChatUserMessage,
) {
	t.Helper()
	restarted, provider, driverCalls := restartEditServiceWithDriverCalls(t, h)
	startsBefore, resumesBefore := driverCalls.counts()
	_, err := restarted.EditMessage(context.Background(), testSession, turnID, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("restart replay error = %v, want ErrEditDeliveryUncertain", err)
	}
	startsAfter, resumesAfter := driverCalls.counts()
	if startsAfter != startsBefore || resumesAfter != resumesBefore {
		t.Fatalf("restart replay reached driver: starts %d->%d resumes %d->%d",
			startsBefore, startsAfter, resumesBefore, resumesAfter)
	}
	if calls := provider.sendCallCount(); calls != 0 {
		t.Fatalf("restarted provider received %d sends for uncertain edit replay, want none", calls)
	}
}

func TestEditMessageClosesSourceWriterBeforeResumingNativeFork(t *testing.T) {
	h, source, driver := newEditHarness(t, false)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	driver.mu.Lock()
	driver.beforeResume = func(ports.ChatResumeConfig) error {
		select {
		case _, open := <-source.events:
			if !open {
				return nil
			}
			return errors.New("source writer emitted an unexpected event during handoff")
		default:
			return fmt.Errorf("%w: forked thread already has an active writer", ports.ErrChatResumeFailed)
		}
	}
	driver.mu.Unlock()

	if _, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "B edited", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
}

func TestEditMessageNativeForkDoesNotPublishSourceExitBeforeReplacementTurn(t *testing.T) {
	h, _, _ := newEditHarness(t, false)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	if _, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "B edited", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	for _, signal := range h.activity.snapshot() {
		if signal.ControllerGeneration == h.ctrl.Generation() &&
			signal.State == domain.ActivityExited && signal.Event == "chat.controller.stopped" {
			t.Fatalf("successful native edit published source exit: %+v", signal)
		}
	}
}

func TestActivateBranchKeepsCapturedSourceIntakeFencedUntilClose(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })
	result, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "B edited", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}

	captured, err := h.svc.Controller(testSession)
	if err != nil {
		t.Fatalf("Controller before activation: %v", err)
	}
	driver.mu.Lock()
	active := driver.resumed["thread-forked"]
	driver.mu.Unlock()
	active.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-201"},
		ports.ChatEvent{
			Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "provider-turn-201",
			ProviderItemID: "edited-answer", Text: "edited answer",
		},
		ports.ChatEvent{
			Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-201",
			TurnState: domain.TurnStateCompleted,
		},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["B edited"] == domain.TurnStateCompleted
	})
	closeStarted := make(chan struct{})
	closeRelease := make(chan struct{})
	active.mu.Lock()
	active.closeStarted = closeStarted
	active.closeEventsRelease = closeRelease
	active.mu.Unlock()

	activateDone := make(chan error, 1)
	go func() {
		_, activateErr := h.svc.ActivateBranch(ctx, testSession, result.SourceBranchID)
		activateDone <- activateErr
	}()
	select {
	case <-closeStarted:
	case err := <-activateDone:
		close(closeRelease)
		t.Fatalf("ActivateBranch returned before source close: %v", err)
	case <-time.After(3 * time.Second):
		close(closeRelease)
		t.Fatal("source close did not start after branch registry swap")
	}
	_, sendErr := captured.Send(ctx, ports.ChatUserMessage{
		Text: "stale captured send", ClientMessageID: "stale-captured-send",
		Origin: domain.MessageOriginHuman,
	})
	close(closeRelease)
	if !errors.Is(sendErr, chatsvc.ErrControllerHandoff) {
		t.Fatalf("captured source send = %v, want ErrControllerHandoff", sendErr)
	}
	if err := <-activateDone; err != nil {
		t.Fatalf("ActivateBranch: %v", err)
	}
}

func TestEditMessageRestoresSourceAfterRequestCancellationDuringClose(t *testing.T) {
	h, source, driver := newEditHarness(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	closeStarted := make(chan struct{})
	closeRelease := make(chan struct{})
	source.mu.Lock()
	source.onClose = cancel
	source.closeStarted = closeStarted
	source.closeEventsRelease = closeRelease
	source.mu.Unlock()
	driver.mu.Lock()
	driver.beforeResume = func(cfg ports.ChatResumeConfig) error {
		if cfg.ProviderConversationID == "thread-forked" {
			return errors.New("replacement resume unavailable")
		}
		return nil
	}
	driver.mu.Unlock()

	editDone := make(chan error, 1)
	go func() {
		_, editErr := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
			Text: "B edited", Origin: domain.MessageOriginHuman,
		})
		editDone <- editErr
	}()
	select {
	case <-closeStarted:
	case <-time.After(3 * time.Second):
		close(closeRelease)
		t.Fatal("source close did not start")
	}
	// Leave the provider stream open long enough for the canceled request to win
	// on the buggy path. The corrected path is detached and remains in recovery.
	time.Sleep(75 * time.Millisecond)
	close(closeRelease)
	editErr := <-editDone
	if editErr == nil || !strings.Contains(editErr.Error(), "replacement resume unavailable") {
		t.Fatalf("EditMessage error = %v, want replacement resume failure", editErr)
	}
	if errors.Is(editErr, context.Canceled) {
		t.Fatalf("post-close recovery reused canceled request context: %v", editErr)
	}
	restored, err := h.svc.Controller(testSession)
	if err != nil {
		t.Fatalf("restored source controller: %v", err)
	}
	if got := restored.ProviderConversationID(); got != "thread-1" {
		t.Fatalf("restored provider conversation = %q, want thread-1", got)
	}
}

func TestEditMessageReportsExitWhenReplacementAndRecoveryBothFail(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })
	driver.mu.Lock()
	driver.beforeResume = func(cfg ports.ChatResumeConfig) error {
		return fmt.Errorf("resume %s unavailable", cfg.ProviderConversationID)
	}
	driver.mu.Unlock()

	if _, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "B edited", Origin: domain.MessageOriginHuman,
	}); err == nil {
		t.Fatal("EditMessage succeeded when replacement and recovery both failed")
	}
	for _, signal := range h.activity.snapshot() {
		if signal.ControllerGeneration == h.ctrl.Generation() &&
			signal.State == domain.ActivityExited && signal.Event == "chat.controller.stopped" {
			return
		}
	}
	t.Fatalf("unrecoverable native edit did not publish source exit: %+v", h.activity.snapshot())
}

func TestEditMessageForksBeforeMiddlePromptAndReusesStoredContent(t *testing.T) {
	h, source, driver := newEditHarness(t, false)
	ctx := context.Background()
	first := completeTurn(t, h, "A", "provider-turn-1")
	_ = first
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "B", Origin: domain.MessageOriginHuman, ClientMessageID: "original-b",
		Content: []ports.ChatContent{
			{Type: "resource", URI: ports.ChatInternalReplayResourceURI, MIMEType: "application/json", Text: `{"userSupplied":true}`},
			{Type: "resource", URI: ports.ChatInternalReplayResourceURI, MIMEType: "application/json", Text: `{"internal":true}`, Internal: true},
			{Type: "image", Data: "data:image/png;base64,AA==", MIMEType: "image/png", Name: "diagram.png"},
		},
	})
	if err != nil {
		t.Fatalf("Send B: %v", err)
	}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-2"},
		ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "provider-turn-2", ProviderItemID: "answer-b", Text: "answer B"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-2", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	result, err := h.svc.EditMessage(ctx, testSession, second.ID, ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "edit-b", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if result.SourceBranchID == "" || result.ActiveBranchID == "" || result.SourceBranchID == result.ActiveBranchID {
		t.Fatalf("branch result = %+v", result)
	}
	if anchor := source.lastForkAnchor(); anchor == nil || *anchor != "provider-turn-1" {
		t.Fatalf("fork anchor = %#v", anchor)
	}
	if got := source.rollbackTargets(); len(got) != 0 {
		t.Fatalf("editing called rollback: %v", got)
	}
	driver.mu.Lock()
	replacement := driver.resumed["thread-forked"]
	resumes := append([]ports.ChatResumeConfig(nil), driver.resumeCalls...)
	driver.mu.Unlock()
	sent := replacement.sentMessages()
	if len(sent) != 1 || sent[0].Text != "B edited" || len(sent[0].Content) != 2 ||
		sent[0].Content[0].Text != `{"userSupplied":true}` ||
		sent[0].Content[1].Data != "data:image/png;base64,AA==" {
		t.Fatalf("replacement send = %#v", sent)
	}
	driver.mu.Lock()
	starts := append([]ports.ChatStartConfig(nil), driver.startConfigs...)
	driver.mu.Unlock()
	if len(resumes) != 1 || resumes[0].WorkspacePath == "" || resumes[0].Env["AO_EDIT_TEST"] != "yes" ||
		resumes[0].SystemPrompt != "preserved prompt" || resumes[0].ProviderScopeID == "" ||
		len(starts) != 1 || resumes[0].ProviderScopeID != starts[0].ProviderScopeID {
		t.Fatalf("resume config = %#v", resumes)
	}
}

func TestEditMessageFirstPromptStartsFreshConversation(t *testing.T) {
	h, source, driver := newEditHarness(t, false)
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	result, err := h.svc.EditMessage(context.Background(), testSession, first, ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-a", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("EditMessage first: %v", err)
	}
	driver.mu.Lock()
	startCalls := driver.startCalls
	driver.mu.Unlock()
	if startCalls != 2 {
		t.Fatalf("driver Start calls = %d, want 2", startCalls)
	}
	if anchor := source.lastForkAnchor(); anchor != nil {
		t.Fatalf("first prompt forked existing provider history at %#v", anchor)
	}
	if sent := driver.fresh.sentTexts(); len(sent) != 1 || sent[0] != "A edited" {
		t.Fatalf("fresh provider sends = %v", sent)
	}
	branch, err := h.st.ConversationBranch(context.Background(), h.ctrl.ConversationID(), result.ActiveBranchID)
	if err != nil {
		t.Fatalf("ConversationBranch: %v", err)
	}
	if branch.ProviderScopeID == "" {
		t.Fatal("fresh first-prompt branch reused the source provider scope")
	}
}

func TestEditOfEditedFirstPromptDoesNotReplayExcludedSiblingHistory(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	firstEdit, err := h.svc.EditMessage(ctx, testSession, first, ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-a-first", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("first EditMessage: %v", err)
	}
	firstFresh := driver.fresh
	firstFresh.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-101"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-101", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		for _, turn := range snapshot.Turns {
			if turn.ID == firstEdit.Turn.ID {
				return turn.State.Terminal()
			}
		}
		return false
	})
	secondEdit, err := h.svc.EditMessage(ctx, testSession, firstEdit.Turn.ID, ports.ChatUserMessage{
		Text: "A edited again", ClientMessageID: "edit-a-second", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("second EditMessage: %v", err)
	}
	branch, err := h.st.ConversationBranch(ctx, h.ctrl.ConversationID(), secondEdit.ActiveBranchID)
	if err != nil {
		t.Fatalf("ConversationBranch: %v", err)
	}
	if branch.Strategy != domain.ConversationBranchStrategyNative {
		t.Fatalf("second first-prompt edit strategy = %q, want native", branch.Strategy)
	}
	driver.mu.Lock()
	startCalls := driver.startCalls
	driver.mu.Unlock()
	if startCalls != 3 {
		t.Fatalf("provider starts = %d, want initial plus two exact first-prompt branches", startCalls)
	}
}

func TestEditMessageRefusesBusyControllerAndLeavesSourceActive(t *testing.T) {
	h, source, driver := newEditHarness(t, false)
	turn, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{
		Text: "running", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	conversation, err := h.st.ConversationForSession(context.Background(), testSession)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	_, err = h.svc.EditMessage(context.Background(), testSession, turn.ID, ports.ChatUserMessage{Text: "edited"})
	if !errors.Is(err, chatsvc.ErrTurnRunning) {
		t.Fatalf("EditMessage busy error = %v, want ErrTurnRunning", err)
	}
	after, err := h.st.ConversationForSession(context.Background(), testSession)
	if err != nil || after.ActiveBranchID != conversation.ActiveBranchID {
		t.Fatalf("active branch after busy refusal = %q, want %q, err=%v", after.ActiveBranchID, conversation.ActiveBranchID, err)
	}
	if anchor := source.lastForkAnchor(); anchor != nil {
		t.Fatalf("busy edit called Fork at %#v", anchor)
	}
	driver.mu.Lock()
	startCalls := driver.startCalls
	driver.mu.Unlock()
	if startCalls != 1 {
		t.Fatalf("busy edit started replacement provider; starts=%d", startCalls)
	}
}

func TestEditMessageForkFailureReopensSource(t *testing.T) {
	h, source, _ := newEditHarness(t, false)
	first := completeTurn(t, h, "A", "provider-turn-1")
	_ = first
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })
	source.mu.Lock()
	source.forkErr = errors.New("fork unavailable")
	source.mu.Unlock()
	conversation, err := h.st.ConversationForSession(context.Background(), testSession)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	if _, err := h.svc.EditMessage(context.Background(), testSession, second,
		ports.ChatUserMessage{Text: "B edited"}); err == nil {
		t.Fatal("EditMessage succeeded after provider fork failure")
	}
	after, err := h.st.ConversationForSession(context.Background(), testSession)
	if err != nil || after.ActiveBranchID != conversation.ActiveBranchID {
		t.Fatalf("active branch after fork failure = %q, want %q, err=%v", after.ActiveBranchID, conversation.ActiveBranchID, err)
	}
	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{Text: "source reopened"}); err != nil {
		t.Fatalf("Send after fork failure: %v", err)
	}
}

func TestEditMessageExplicitRefusalRestoresSourceBranch(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	driver.mu.Lock()
	replacement := driver.resumed["thread-forked"]
	replacement.mu.Lock()
	replacement.sendErr = refusedError{msg: "provider declined edit"}
	replacement.mu.Unlock()
	driver.mu.Unlock()

	failed, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "edit-b-failed", Origin: domain.MessageOriginHuman,
	})
	if err == nil {
		t.Fatal("EditMessage succeeded after replacement send failure")
	}
	if !errors.Is(err, chatsvc.ErrProviderRefused) {
		t.Fatalf("EditMessage error = %v, want ErrProviderRefused", err)
	}
	if failed.ActiveBranchID == "" || failed.ActiveBranchID == failed.SourceBranchID {
		t.Fatalf("failed edit branch result = %+v", failed)
	}
	snapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot failed branch: %v", err)
	}
	requireMessageTexts(t, snapshot.Messages, []string{"A", "reply to A", "B", "reply to B"})
	requireBranchPoint(t, snapshot, second, "", failed.ActiveBranchID)
	if snapshot.ActiveBranch.ID != failed.SourceBranchID {
		t.Fatalf("active branch after explicit refusal = %q, want source %q", snapshot.ActiveBranch.ID, failed.SourceBranchID)
	}
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "source remains usable"}); err != nil {
		t.Fatalf("Send on restored source: %v", err)
	}
}

func TestEditMessageUndispatchedAttemptRestoresSourceBranch(t *testing.T) {
	h, _, _ := newEditHarness(t, false)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "B", ClientMessageID: "duplicate-edit-delivery", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send B: %v", err)
	}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-2"},
		ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "provider-turn-2", ProviderItemID: "answer-b", Text: "reply to B"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-2", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	failed, err := h.svc.EditMessage(ctx, testSession, second.ID, ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "duplicate-edit-delivery", Origin: domain.MessageOriginHuman,
	})
	if err == nil {
		t.Fatal("EditMessage accepted an attempt that was never dispatched")
	}
	if failed.Turn.ID != "" {
		t.Fatalf("undispatched edit turn = %+v, want empty", failed.Turn)
	}
	snapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("Snapshot after undispatched edit: %v", err)
	}
	if snapshot.ActiveBranch.ID != failed.SourceBranchID {
		t.Fatalf("active branch after undispatched edit = %q, want source %q", snapshot.ActiveBranch.ID, failed.SourceBranchID)
	}
	requireMessageTexts(t, snapshot.Messages, []string{"A", "reply to A", "B", "reply to B"})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "source remains usable"}); err != nil {
		t.Fatalf("Send on restored source: %v", err)
	}
}

func TestAcceptedEditReplaysBeforeAnchorLookupAndRejectsChangedPayload(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	msg := ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-replay", Origin: domain.MessageOriginHuman,
	}

	original, err := h.svc.EditMessage(ctx, testSession, first, msg)
	if err != nil {
		t.Fatalf("first EditMessage: %v", err)
	}
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("stop controller before replay: %v", err)
	}
	replayed, err := h.svc.EditMessage(ctx, testSession, first, msg)
	if err != nil {
		t.Fatalf("replayed EditMessage: %v", err)
	}
	if replayed != original {
		t.Fatalf("replayed result = %+v, want original %+v", replayed, original)
	}
	if sent := driver.fresh.sentTexts(); len(sent) != 1 || sent[0] != "A edited" {
		t.Fatalf("provider sends after replay = %v, want one", sent)
	}

	_, err = h.svc.EditMessage(ctx, testSession, first, ports.ChatUserMessage{
		Text: "different edit", ClientMessageID: msg.ClientMessageID,
		Origin: domain.MessageOriginHuman,
	})
	if !errors.Is(err, chatsvc.ErrEditIdempotencyConflict) {
		t.Fatalf("changed edit error = %v, want ErrEditIdempotencyConflict", err)
	}
	if sent := driver.fresh.sentTexts(); len(sent) != 1 {
		t.Fatalf("changed payload reached provider; sends=%v", sent)
	}
}

func TestAmbiguousEditSendFailureStaysUncertainWithoutProviderRedispatch(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	driver.fresh.mu.Lock()
	driver.fresh.sendErr = errors.New("provider unavailable")
	driver.fresh.mu.Unlock()
	msg := ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-rejected", Origin: domain.MessageOriginHuman,
	}

	failed, err := h.svc.EditMessage(ctx, testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("first EditMessage error = %v, want ErrEditDeliveryUncertain", err)
	}
	if failed.ActiveBranchID == "" {
		t.Fatalf("failed edit did not identify its selected branch: %+v", failed)
	}
	if calls := driver.fresh.sendCallCount(); calls != 1 {
		t.Fatalf("provider send calls after ambiguous failure = %d, want one", calls)
	}
	driver.fresh.mu.Lock()
	driver.fresh.sendErr = nil
	driver.fresh.mu.Unlock()
	_, retryErr := h.svc.EditMessage(ctx, testSession, first, msg)
	if !errors.Is(retryErr, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("same-controller replay error = %v, want ErrEditDeliveryUncertain", retryErr)
	}
	if calls := driver.fresh.sendCallCount(); calls != 1 {
		t.Fatalf("provider send calls after same-id replay = %d, want one", calls)
	}
	restarted, restartedProvider := restartEditService(t, h)
	_, retryErr = restarted.EditMessage(ctx, testSession, first, msg)
	if !errors.Is(retryErr, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("restart replay error = %v, want ErrEditDeliveryUncertain", retryErr)
	}
	if calls := restartedProvider.sendCallCount(); calls != 0 {
		t.Fatalf("restarted provider received %d sends for uncertain edit replay, want none", calls)
	}
}

func TestGenericEditBranchStartFailureStaysUncertainWithoutProviderRedispatch(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	driver.mu.Lock()
	driver.startErr = errors.New("branch start transport failed")
	driver.mu.Unlock()
	msg := ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-start-uncertain", Origin: domain.MessageOriginHuman,
	}

	_, err := h.svc.EditMessage(context.Background(), testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("first EditMessage error = %v, want ErrEditDeliveryUncertain", err)
	}
	driver.mu.Lock()
	startCalls := driver.startCalls
	resumeCalls := len(driver.resumeCalls)
	if resumeCalls != 1 || driver.resumeCalls[0].ProviderConversationID != "thread-1" {
		t.Fatalf("failed branch start must restore only the source controller: %+v", driver.resumeCalls)
	}
	driver.mu.Unlock()
	if startCalls != 2 || resumeCalls != 1 || driver.fresh.sendCallCount() != 0 {
		t.Fatalf("provider operations after branch start failure: starts=%d resumes=%d sends=%d",
			startCalls, resumeCalls, driver.fresh.sendCallCount())
	}
	_, err = h.svc.EditMessage(context.Background(), testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("same-controller replay error = %v, want ErrEditDeliveryUncertain", err)
	}
	driver.mu.Lock()
	startCalls = driver.startCalls
	resumeCalls = len(driver.resumeCalls)
	driver.mu.Unlock()
	if startCalls != 2 || resumeCalls != 1 || driver.fresh.sendCallCount() != 0 {
		t.Fatalf("same-id replay reached provider: starts=%d resumes=%d sends=%d",
			startCalls, resumeCalls, driver.fresh.sendCallCount())
	}
	requireUncertainEditReplayAfterRestart(t, h, first, msg)
}

func TestGenericEditBranchResumeFailureStaysUncertainWithoutProviderRedispatch(t *testing.T) {
	h, source, driver := newEditHarness(t, false)
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })
	driver.mu.Lock()
	driver.beforeResume = func(cfg ports.ChatResumeConfig) error {
		if cfg.ProviderConversationID == "thread-forked" {
			return errors.New("branch resume transport failed")
		}
		return nil
	}
	driver.mu.Unlock()
	msg := ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "edit-resume-uncertain", Origin: domain.MessageOriginHuman,
	}

	_, err := h.svc.EditMessage(context.Background(), testSession, second, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("first EditMessage error = %v, want ErrEditDeliveryUncertain", err)
	}
	source.mu.Lock()
	forkCalls := len(source.forkAnchors)
	source.mu.Unlock()
	driver.mu.Lock()
	resumeCalls := len(driver.resumeCalls)
	if resumeCalls != 2 || driver.resumeCalls[0].ProviderConversationID != "thread-forked" || driver.resumeCalls[1].ProviderConversationID != "thread-1" {
		t.Fatalf("failed branch resume must restore the source controller: %+v", driver.resumeCalls)
	}
	driver.mu.Unlock()
	if forkCalls != 1 || resumeCalls != 2 {
		t.Fatalf("provider operations after branch resume failure: forks=%d resumes=%d", forkCalls, resumeCalls)
	}
	_, err = h.svc.EditMessage(context.Background(), testSession, second, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("same-controller replay error = %v, want ErrEditDeliveryUncertain", err)
	}
	source.mu.Lock()
	forkCalls = len(source.forkAnchors)
	source.mu.Unlock()
	driver.mu.Lock()
	resumeCalls = len(driver.resumeCalls)
	driver.mu.Unlock()
	if forkCalls != 1 || resumeCalls != 2 {
		t.Fatalf("same-id replay reached provider: forks=%d resumes=%d", forkCalls, resumeCalls)
	}
	requireUncertainEditReplayAfterRestart(t, h, second, msg)
}

func TestGenericEditBindFailureStaysUncertainWithoutProviderRedispatch(t *testing.T) {
	var faults *failEditOperationStore
	h, _, driver := newEditHarnessWithStore(t, false, func(st *store.Store) chatsvc.Store {
		faults = &failEditOperationStore{Store: st}
		return faults
	})
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	faults.mu.Lock()
	faults.bindErr = errors.New("bind provider turn failed")
	faults.mu.Unlock()
	msg := ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-bind-uncertain", Origin: domain.MessageOriginHuman,
	}

	_, err := h.svc.EditMessage(context.Background(), testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("first EditMessage error = %v, want ErrEditDeliveryUncertain", err)
	}
	faults.mu.Lock()
	bindFailures := faults.bindFailures
	faults.mu.Unlock()
	if bindFailures != 1 || driver.fresh.sendCallCount() != 1 {
		t.Fatalf("provider operations after bind failure: binds=%d sends=%d",
			bindFailures, driver.fresh.sendCallCount())
	}
	_, err = h.svc.EditMessage(context.Background(), testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("same-controller replay error = %v, want ErrEditDeliveryUncertain", err)
	}
	faults.mu.Lock()
	bindFailures = faults.bindFailures
	faults.mu.Unlock()
	if bindFailures != 1 || driver.fresh.sendCallCount() != 1 {
		t.Fatalf("same-id replay repeated ambiguous bind/send: binds=%d sends=%d",
			bindFailures, driver.fresh.sendCallCount())
	}
	requireUncertainEditReplayAfterRestart(t, h, first, msg)
}

func TestGenericEditBranchInstallationFailureStaysUncertainWithoutProviderRedispatch(t *testing.T) {
	var faults *failEditOperationStore
	h, _, driver := newEditHarnessWithStore(t, false, func(st *store.Store) chatsvc.Store {
		faults = &failEditOperationStore{Store: st}
		return faults
	})
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	faults.mu.Lock()
	faults.branchInstallErr = errors.New("install branch failed")
	faults.mu.Unlock()
	msg := ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-install-uncertain", Origin: domain.MessageOriginHuman,
	}

	_, err := h.svc.EditMessage(context.Background(), testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("first EditMessage error = %v, want ErrEditDeliveryUncertain", err)
	}
	faults.mu.Lock()
	installFailures := faults.branchInstallFailures
	faults.mu.Unlock()
	driver.mu.Lock()
	startCalls := driver.startCalls
	driver.mu.Unlock()
	if installFailures != 1 || startCalls != 2 || driver.fresh.sendCallCount() != 0 {
		t.Fatalf("provider operations after branch installation failure: installs=%d starts=%d sends=%d",
			installFailures, startCalls, driver.fresh.sendCallCount())
	}
	_, err = h.svc.EditMessage(context.Background(), testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("same-controller replay error = %v, want ErrEditDeliveryUncertain", err)
	}
	faults.mu.Lock()
	installFailures = faults.branchInstallFailures
	faults.mu.Unlock()
	driver.mu.Lock()
	startCalls = driver.startCalls
	driver.mu.Unlock()
	if installFailures != 1 || startCalls != 2 || driver.fresh.sendCallCount() != 0 {
		t.Fatalf("same-id replay repeated branch installation: installs=%d starts=%d sends=%d",
			installFailures, startCalls, driver.fresh.sendCallCount())
	}
	requireUncertainEditReplayAfterRestart(t, h, first, msg)
}

func TestTypedProviderEditRefusalDurablyReplaysWithoutProviderRedispatch(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	driver.fresh.mu.Lock()
	driver.fresh.sendErr = refusedError{msg: "provider declined the replacement"}
	driver.fresh.mu.Unlock()
	msg := ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-provider-refused", Origin: domain.MessageOriginHuman,
	}

	_, err := h.svc.EditMessage(ctx, testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrProviderRefused) {
		t.Fatalf("first EditMessage error = %v, want ErrProviderRefused", err)
	}
	if calls := driver.fresh.sendCallCount(); calls != 1 {
		t.Fatalf("provider send calls after refusal = %d, want one", calls)
	}
	driver.fresh.mu.Lock()
	driver.fresh.sendErr = nil
	driver.fresh.mu.Unlock()
	_, err = h.svc.EditMessage(ctx, testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrProviderRefused) {
		t.Fatalf("same-controller replay error = %v, want ErrProviderRefused", err)
	}
	if calls := driver.fresh.sendCallCount(); calls != 1 {
		t.Fatalf("provider send calls after refusal replay = %d, want one", calls)
	}

	restarted, restartedProvider := restartEditService(t, h)
	_, err = restarted.EditMessage(ctx, testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrProviderRefused) {
		t.Fatalf("restart replay error = %v, want ErrProviderRefused", err)
	}
	if calls := restartedProvider.sendCallCount(); calls != 0 {
		t.Fatalf("restarted provider received %d sends for refused edit replay, want none", calls)
	}
}

func TestAcceptedEditReplaysAfterControllerRestartWithoutProviderRedispatch(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	msg := ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-restart", Origin: domain.MessageOriginHuman,
	}
	original, err := h.svc.EditMessage(context.Background(), testSession, first, msg)
	if err != nil {
		t.Fatalf("first EditMessage: %v", err)
	}
	if sent := driver.fresh.sentTexts(); len(sent) != 1 {
		t.Fatalf("original provider sends = %v, want one", sent)
	}

	restarted, restartedProvider := restartEditService(t, h)
	replayed, err := restarted.EditMessage(context.Background(), testSession, first, msg)
	if err != nil {
		t.Fatalf("EditMessage after restart: %v", err)
	}
	if replayed != original {
		t.Fatalf("restart replay = %+v, want %+v", replayed, original)
	}
	if sent := restartedProvider.sentTexts(); len(sent) != 0 {
		t.Fatalf("restarted provider received edit replay: %v", sent)
	}
}

func TestEditCompletionGapStaysUncertainAcrossRetryAndControllerRestart(t *testing.T) {
	var flaky *failEditCompletionStore
	h, _, driver := newEditHarnessWithStore(t, false, func(st *store.Store) chatsvc.Store {
		flaky = &failEditCompletionStore{Store: st}
		return flaky
	})
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	msg := ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-uncertain", Origin: domain.MessageOriginHuman,
	}

	_, err := h.svc.EditMessage(context.Background(), testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("completion gap error = %v, want ErrEditDeliveryUncertain", err)
	}
	_, err = h.svc.EditMessage(context.Background(), testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("same-controller retry error = %v, want ErrEditDeliveryUncertain", err)
	}
	if sent := driver.fresh.sentTexts(); len(sent) != 1 {
		t.Fatalf("provider received %d sends after uncertain retry, want one", len(sent))
	}

	restarted, restartedProvider := restartEditService(t, h)
	_, err = restarted.EditMessage(context.Background(), testSession, first, msg)
	if !errors.Is(err, chatsvc.ErrEditDeliveryUncertain) {
		t.Fatalf("restart retry error = %v, want ErrEditDeliveryUncertain", err)
	}
	if sent := restartedProvider.sentTexts(); len(sent) != 0 {
		t.Fatalf("restarted provider received uncertain edit replay: %v", sent)
	}
}

func TestEditMessageRejectsMalformedStoredContentBeforeFork(t *testing.T) {
	h, source, driver := newEditHarness(t, false)
	created, err := h.st.AppendUserMessage(context.Background(), h.ctrl.ConversationID(), testSession,
		h.ctrl.Generation(), domain.ConversationMessage{
			ID: "legacy-message", Text: "legacy", Origin: domain.MessageOriginHuman,
			ClientMessageID: "legacy-client", DeliveryContentJSON: `{broken`,
		}, "legacy-turn", h.now())
	if err != nil || !created {
		t.Fatalf("AppendUserMessage legacy: created=%v err=%v", created, err)
	}
	msg := ports.ChatUserMessage{
		Text: "edited", ClientMessageID: "edit-invalid-content", Origin: domain.MessageOriginHuman,
	}
	_, err = h.svc.EditMessage(context.Background(), testSession, "legacy-turn", msg)
	if !errors.Is(err, chatsvc.ErrEditTurnInvalid) {
		t.Fatalf("EditMessage malformed content error = %v, want ErrEditTurnInvalid", err)
	}
	_, err = h.svc.EditMessage(context.Background(), testSession, "legacy-turn", msg)
	if !errors.Is(err, chatsvc.ErrEditTurnInvalid) {
		t.Fatalf("same-controller replay error = %v, want ErrEditTurnInvalid", err)
	}
	source.mu.Lock()
	forkCalls := len(source.forkAnchors)
	source.mu.Unlock()
	driver.mu.Lock()
	startCalls := driver.startCalls
	resumeCalls := len(driver.resumeCalls)
	driver.mu.Unlock()
	if forkCalls != 0 || startCalls != 1 || resumeCalls != 0 || source.sendCallCount() != 0 {
		t.Fatalf("provider calls after pre-provider rejection: forks=%d starts=%d resumes=%d sends=%d",
			forkCalls, startCalls, resumeCalls, source.sendCallCount())
	}

	restarted, restartedProvider := restartEditService(t, h)
	_, err = restarted.EditMessage(context.Background(), testSession, "legacy-turn", msg)
	if !errors.Is(err, chatsvc.ErrEditTurnInvalid) {
		t.Fatalf("restart replay error = %v, want ErrEditTurnInvalid", err)
	}
	if calls := restartedProvider.sendCallCount(); calls != 0 {
		t.Fatalf("restarted provider received %d sends for validation rejection, want none", calls)
	}
}

func TestActivateBranchResumesWithoutSending(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	result, err := h.svc.EditMessage(context.Background(), testSession, first, ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-a", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	driver.fresh.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-101"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-101", TurnState: domain.TurnStateCompleted},
	)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		controller, controllerErr := h.svc.Controller(testSession)
		if controllerErr == nil && controller.State() == ports.ChatControllerReady {
			snapshot, snapshotErr := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
			if snapshotErr == nil && len(snapshot.Turns) == 1 && snapshot.Turns[0].State.Terminal() {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	active, err := h.svc.ActivateBranch(context.Background(), testSession, result.SourceBranchID)
	if err != nil {
		t.Fatalf("ActivateBranch: %v", err)
	}
	if active != result.SourceBranchID {
		t.Fatalf("active branch = %q, want %q", active, result.SourceBranchID)
	}
	driver.mu.Lock()
	root := driver.resumed["thread-1"]
	driver.mu.Unlock()
	if sent := root.sentTexts(); len(sent) != 0 {
		t.Fatalf("branch activation sent messages: %v", sent)
	}
}

func TestActivateBranchResumeFailureKeepsCurrentControllerActive(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	first := completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	edited, err := h.svc.EditMessage(ctx, testSession, first, ports.ChatUserMessage{
		Text: "A edited", ClientMessageID: "edit-a-resume-failure", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	driver.fresh.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-101"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-101", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		for _, turn := range snapshot.Turns {
			if turn.ID == edited.Turn.ID {
				return turn.State.Terminal()
			}
		}
		return false
	})
	current, err := h.svc.Controller(testSession)
	if err != nil {
		t.Fatalf("Controller before activation: %v", err)
	}
	driver.mu.Lock()
	failedTargetResume := false
	recovered := newFakeConversation()
	recovered.providerConversationID = current.ProviderConversationID()
	recovered.turnSeq = 400
	driver.resumed[current.ProviderConversationID()] = recovered
	driver.beforeResume = func(cfg ports.ChatResumeConfig) error {
		if cfg.ProviderConversationID == "thread-1" && !failedTargetResume {
			failedTargetResume = true
			return errors.New("target resume failed")
		}
		return nil
	}
	driver.mu.Unlock()
	if _, err := h.svc.ActivateBranch(ctx, testSession, edited.SourceBranchID); err == nil {
		t.Fatal("ActivateBranch succeeded after target resume failure")
	}
	after, err := h.svc.Controller(testSession)
	afterProviderID := ""
	if after != nil {
		afterProviderID = after.ProviderConversationID()
	}
	if err != nil || after == current || afterProviderID != current.ProviderConversationID() {
		t.Fatalf("controller after resume failure = %p (%q), want recovered replacement for %p (%q), err=%v",
			after, afterProviderID, current, current.ProviderConversationID(), err)
	}
	conversation, err := h.st.ConversationForSession(ctx, testSession)
	if err != nil || conversation.ActiveBranchID != edited.ActiveBranchID {
		t.Fatalf("durable branch after resume failure = %q, want %q, err=%v",
			conversation.ActiveBranchID, edited.ActiveBranchID, err)
	}
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "continue edited branch", ClientMessageID: "continue-after-resume-failure", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send on retained controller: %v", err)
	}
}

func TestActivateBranchSwitchesBetweenCompatibleApproximateProviderScopes(t *testing.T) {
	h, _, driver := newEditHarness(t, true)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })

	edited, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "edit-b", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	driver.fresh.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-101"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-101", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		for _, turn := range snapshot.Turns {
			if turn.ID == edited.Turn.ID {
				return turn.State.Terminal()
			}
		}
		return false
	})

	if _, err := h.svc.ActivateBranch(ctx, testSession, edited.SourceBranchID); err != nil {
		t.Fatalf("activate original branch: %v", err)
	}
	originalSnapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("Snapshot original branch: %v", err)
	}
	requireBranchPoint(t, originalSnapshot, second, "", edited.ActiveBranchID)
	resumedEdit := newFakeConversation()
	resumedEdit.providerConversationID = "thread-fresh"
	driver.mu.Lock()
	driver.resumed["thread-fresh"] = resumedEdit
	driver.mu.Unlock()
	if _, err := h.svc.ActivateBranch(ctx, testSession, edited.ActiveBranchID); err != nil {
		t.Fatalf("reactivate edited branch: %v", err)
	}
	editedSnapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("Snapshot edited branch: %v", err)
	}
	requireBranchPoint(t, editedSnapshot, edited.Turn.ID, edited.SourceBranchID, "")
	resumedOriginal := newFakeConversation()
	resumedOriginal.providerConversationID = "thread-1"
	driver.mu.Lock()
	driver.resumed["thread-1"] = resumedOriginal
	driver.mu.Unlock()
	if _, err := h.svc.ActivateBranch(ctx, testSession, edited.SourceBranchID); err != nil {
		t.Fatalf("reactivate original branch: %v", err)
	}
	originalSnapshot, err = h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("Snapshot original branch again: %v", err)
	}
	requireBranchPoint(t, originalSnapshot, second, "", edited.ActiveBranchID)
	resumedEditAgain := newFakeConversation()
	resumedEditAgain.providerConversationID = "thread-fresh"
	driver.mu.Lock()
	driver.resumed["thread-fresh"] = resumedEditAgain
	driver.mu.Unlock()
	if active, err := h.svc.ActivateBranch(ctx, testSession, edited.ActiveBranchID); err != nil {
		t.Fatalf("reactivate edited branch again: %v", err)
	} else if active != edited.ActiveBranchID {
		t.Fatalf("active branch = %q, want %q", active, edited.ActiveBranchID)
	}
	editedSnapshot, err = h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("Snapshot edited branch again: %v", err)
	}
	requireBranchPoint(t, editedSnapshot, edited.Turn.ID, edited.SourceBranchID, "")
}

func TestApproximateBranchScopePersistsAcrossServiceRestartAndSwitching(t *testing.T) {
	h, _, driver := newEditHarness(t, true)
	ctx := context.Background()
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })
	edited, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "edit-b-restart", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	driver.fresh.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-101"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-101", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		for _, turn := range snapshot.Turns {
			if turn.ID == edited.Turn.ID {
				return turn.State.Terminal()
			}
		}
		return false
	})
	activeBranch, err := h.st.ConversationBranch(ctx, h.ctrl.ConversationID(), edited.ActiveBranchID)
	if err != nil {
		t.Fatalf("ConversationBranch: %v", err)
	}
	if activeBranch.ProviderScopeID == "" {
		t.Fatal("approximate branch has no durable provider scope")
	}
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("Stop before restart: %v", err)
	}
	record, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}

	restartDriver := fakeDriver{conv: newFakeConversation()}
	restartDriver.resume = func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
		resumed := newFakeConversation()
		resumed.providerConversationID = cfg.ProviderConversationID
		return resumed, nil
	}
	nextID := 0
	restarted := chatsvc.New(chatsvc.Options{
		Store: h.st, Sessions: h.st,
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			snapshot, err := h.st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation: snapshot.Conversation, ActiveBranch: snapshot.ActiveBranch,
				Turns: snapshot.Turns, Messages: snapshot.Messages,
				Activities: snapshot.Activities, BranchPoints: snapshot.BranchPoints,
				BranchedFromEarlierMessage: snapshot.BranchedFromEarlierMessage,
			}, nil
		}),
		Drivers: fakeRegistry{driver: restartDriver}, Log: slog.New(slog.DiscardHandler),
		NewID: func() string {
			nextID++
			return fmt.Sprintf("restart-edit-id-%d", nextID)
		},
		Now: func() time.Time { return h.clock },
	})
	t.Cleanup(func() { _ = restarted.Stop(context.Background(), testSession) })
	if _, err := restarted.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ProviderConversationID: record.Metadata.ProviderConversationID,
	}); err != nil {
		t.Fatalf("Start after restart: %v", err)
	}
	restartedSnapshot, err := restarted.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("Snapshot after restart: %v", err)
	}
	if restartedSnapshot.ActiveBranch.ID != edited.ActiveBranchID ||
		restartedSnapshot.ActiveBranch.ProviderScopeID != activeBranch.ProviderScopeID {
		t.Fatalf("active branch after restart = %+v, want id=%q scope=%q",
			restartedSnapshot.ActiveBranch, edited.ActiveBranchID, activeBranch.ProviderScopeID)
	}
	if _, err := restarted.ActivateBranch(ctx, testSession, edited.SourceBranchID); err != nil {
		t.Fatalf("activate source after restart: %v", err)
	}
	if active, err := restarted.ActivateBranch(ctx, testSession, edited.ActiveBranchID); err != nil {
		t.Fatalf("reactivate edit after restart: %v", err)
	} else if active != edited.ActiveBranchID {
		t.Fatalf("active branch = %q, want %q", active, edited.ActiveBranchID)
	}
}

func TestProviderBoundaryRejectsSourceProviderBranchAndEdit(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	record, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	record.Metadata.ProviderConversationID = "source-provider-thread"
	record.Metadata.ControllerGeneration = "source-generation"
	if err := st.UpdateSession(ctx, record); err != nil {
		t.Fatalf("seed source controller identity: %v", err)
	}
	conversation, err := st.CreateConversation(ctx, "provider-boundary-conversation",
		domain.ConversationScopeProject, testProject, testSession, now)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	created, err := st.AppendUserMessage(ctx, conversation.ID, testSession, "source-generation",
		domain.ConversationMessage{
			ID: "source-message", Origin: domain.MessageOriginHuman, Text: "source task",
			ClientMessageID: "source-client-message",
		}, "source-turn", now)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage: created=%v err=%v", created, err)
	}
	if err := st.BindTurnToProvider(ctx, "source-turn", "source-provider-turn", now); err != nil {
		t.Fatalf("BindTurnToProvider: %v", err)
	}
	conversation, err = st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatalf("reload source conversation: %v", err)
	}
	target := newHistoryRecorder()
	target.providerConversationID = "target-provider-thread"
	startCalls := 0
	resumeCalls := 0
	driver := fakeDriver{conv: target}
	driver.start = func(ports.ChatStartConfig) (ports.ChatConversation, error) {
		startCalls++
		if startCalls == 1 {
			return target, nil
		}
		return nil, errors.New("source-provider edit reached target driver")
	}
	driver.resume = func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
		resumeCalls++
		return nil, errors.New("source-provider branch reached target driver")
	}
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: driver},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			nextID++
			return fmt.Sprintf("provider-boundary-%d", nextID)
		},
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	_, err = svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ProviderScopeID: "target-provider-boundary", ControllerGeneration: "target-generation",
		ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			if err := st.CreateAndActivateConversationBranch(ctx, testSession, domain.ConversationBranch{
				ID: "target-provider-boundary", ConversationID: conversation.ID, SessionID: testSession,
				ProviderConversationID: started.ProviderConversationID,
				ParentBranchID:         conversation.ActiveBranchID, ForkAfterSequence: conversation.LatestSequence,
				CreatedAt: now.Add(time.Minute),
			}, started.ControllerGeneration, now.Add(time.Minute)); err != nil {
				return chatsvc.ControllerCommit{}, err
			}
			committed := started.Conversation
			committed.ActiveBranchID = "target-provider-boundary"
			committed.UpdatedAt = now.Add(time.Minute)
			return chatsvc.ControllerCommit{Conversation: committed}, nil
		},
	})
	if err != nil {
		t.Fatalf("Start target controller: %v", err)
	}

	if _, err := svc.EditMessage(ctx, testSession, "source-turn", ports.ChatUserMessage{
		Text: "rewrite source task", Origin: domain.MessageOriginHuman,
	}); !errors.Is(err, chatsvc.ErrEditTurnInvalid) {
		t.Fatalf("source-provider edit error = %v, want ErrEditTurnInvalid", err)
	}
	if startCalls != 1 {
		t.Fatalf("driver starts = %d, want source-provider edit rejected before target driver", startCalls)
	}
	if _, err := svc.Rollback(ctx, testSession, "source-turn"); !errors.Is(err, chatsvc.ErrTurnProviderMismatch) {
		t.Fatalf("source-provider rollback error = %v, want ErrTurnProviderMismatch", err)
	}
	target.mu.Lock()
	rollbackCalls := append([]string(nil), target.rolledBack...)
	target.mu.Unlock()
	if len(rollbackCalls) != 0 {
		t.Fatalf("target provider rollback calls = %v, want source turn rejected before target driver", rollbackCalls)
	}

	if _, err := svc.ActivateBranch(ctx, testSession, conversation.ActiveBranchID); !errors.Is(err, chatsvc.ErrBranchProviderMismatch) {
		t.Fatalf("source-provider branch activation error = %v, want ErrBranchProviderMismatch", err)
	}
	if resumeCalls != 0 {
		t.Fatalf("driver resumes = %d, want source-provider branch rejected before target driver", resumeCalls)
	}
}

// The contract from the automatic-semantic-task-titles design, applied to whatever
// the provider says rather than trusted.
func TestNormalizeTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Fix OAuth Return URL Loss", "Fix OAuth Return URL Loss"},
		{"heading marker", "## Review PR in Fresh Worktree", "Review PR in Fresh Worktree"},
		{"list marker", "- Restore Canvas Renderer Fallback", "Restore Canvas Renderer Fallback"},
		{"quoted", `"Fix the login redirect"`, "Fix the login redirect"},
		{"backticked", "`Rebuild the index`", "Rebuild the index"},
		{"trailing period", "Fix the login redirect.", "Fix the login redirect"},
		{"multi line keeps the first", "Fix the redirect\nand then some prose", "Fix the redirect"},
		{"collapses whitespace", "Fix   the    redirect", "Fix the redirect"},
		{"identifiers survive", "Fix OAuth callback in auth.go #3421", "Fix OAuth callback in auth.go #3421"},
		{"blank", "   ", ""},
		{"punctuation only", " -- ... ", ""},
		{
			"over length truncates at a word",
			strings.Repeat("alpha ", 20),
			"alpha alpha alpha alpha alpha alpha alpha alpha alpha alpha alpha alpha alpha",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chatsvc.NormalizeTitle(tc.in); got != tc.want {
				t.Errorf("NormalizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// awaitSessionName polls until the label moves, because the title arrives on the
// projection goroutine rather than on the caller's.
func awaitSessionName(t *testing.T, h *harness, want string) {
	t.Helper()
	h.awaitSnapshot(t, func(store.ConversationSnapshot) bool {
		rec, ok, err := h.st.GetSession(context.Background(), testSession)
		return err == nil && ok && rec.DisplayName == want
	})
}
