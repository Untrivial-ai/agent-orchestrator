package chat_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// writerPreferenceStore gives concurrency tests a deterministic version of the
// writer preference enforced by the real per-project RWMutex. Persistence still
// goes through SQLite; only admission timing is controlled here.
type writerPreferenceStore struct {
	*sqlite.Store

	mu                sync.Mutex
	readers           int
	writerWaiting     bool
	readersGone       chan struct{}
	writerQueued      chan struct{}
	writerBlocked     chan struct{}
	writerDone        chan struct{}
	lateReaderAttempt chan struct{}
	allowLateReader   chan struct{}
	acquireObserved   chan struct{}
	writerOnce        sync.Once
	lateReaderOnce    sync.Once
	writerBlockedOnce sync.Once

	rateLimitsRecorded chan struct{}
	rateLimitsOnce     sync.Once

	appendMu      sync.Mutex
	appendReached chan struct{}
	appendRelease chan struct{}
	nextMu        sync.Mutex
	nextReached   chan struct{}
	nextRelease   chan struct{}
}

func newWriterPreferenceStore(st *sqlite.Store) *writerPreferenceStore {
	return &writerPreferenceStore{
		Store:              st,
		readersGone:        make(chan struct{}),
		writerQueued:       make(chan struct{}),
		writerBlocked:      make(chan struct{}),
		writerDone:         make(chan struct{}),
		lateReaderAttempt:  make(chan struct{}),
		allowLateReader:    make(chan struct{}),
		rateLimitsRecorded: make(chan struct{}),
	}
}

func (s *writerPreferenceStore) AcquireProjectConversationDispatch(
	ctx context.Context,
	conversationID string,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) (func(), error) {
	s.mu.Lock()
	if s.writerWaiting {
		s.lateReaderOnce.Do(func() { close(s.lateReaderAttempt) })
		allow := s.allowLateReader
		s.mu.Unlock()
		select {
		case <-allow:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
	}
	s.readers++
	s.mu.Unlock()

	releaseStore, err := s.Store.AcquireProjectConversationDispatch(
		ctx, conversationID, projectID, sessionID,
	)
	if err != nil {
		s.releaseReader()
		return nil, err
	}
	s.mu.Lock()
	observed := s.acquireObserved
	s.acquireObserved = nil
	s.mu.Unlock()
	if observed != nil {
		close(observed)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseStore()
			s.releaseReader()
		})
	}, nil
}

func (s *writerPreferenceStore) observeNextOwnershipAcquire() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireObserved = make(chan struct{})
	return s.acquireObserved
}

func (s *writerPreferenceStore) releaseReader() {
	s.mu.Lock()
	s.readers--
	if s.writerWaiting && s.readers == 0 {
		close(s.readersGone)
	}
	s.mu.Unlock()
}

func (s *writerPreferenceStore) rebind(
	ctx context.Context,
	session domain.SessionID,
	now time.Time,
) error {
	s.mu.Lock()
	s.writerWaiting = true
	s.writerOnce.Do(func() { close(s.writerQueued) })
	wait := s.readersGone
	readers := s.readers
	s.mu.Unlock()
	if readers > 0 {
		s.writerBlockedOnce.Do(func() { close(s.writerBlocked) })
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	_, err := s.CreateConversation(
		ctx, "unused-writer-preference-rebind", domain.ConversationScopeProject,
		testProject, session, now,
	)
	s.mu.Lock()
	s.writerWaiting = false
	s.mu.Unlock()
	close(s.writerDone)
	return err
}

func (s *writerPreferenceStore) RecordRateLimits(
	ctx context.Context,
	conversationID string,
	limits domain.ConversationRateLimits,
) error {
	err := s.Store.RecordRateLimits(ctx, conversationID, limits)
	s.rateLimitsOnce.Do(func() { close(s.rateLimitsRecorded) })
	return err
}

func (s *writerPreferenceStore) pauseNextAppend() (chan struct{}, chan struct{}) {
	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	s.appendReached = make(chan struct{})
	s.appendRelease = make(chan struct{})
	return s.appendReached, s.appendRelease
}

func closeGate(gate chan struct{}) {
	select {
	case <-gate:
	default:
		close(gate)
	}
}

func (s *writerPreferenceStore) AppendUserMessage(
	ctx context.Context,
	conversationID string,
	session domain.SessionID,
	generation string,
	message domain.ConversationMessage,
	turnID string,
	now time.Time,
) (bool, error) {
	created, err := s.Store.AppendUserMessage(
		ctx, conversationID, session, generation, message, turnID, now,
	)
	if err != nil || !created {
		return created, err
	}
	s.appendMu.Lock()
	reached, release := s.appendReached, s.appendRelease
	s.appendReached, s.appendRelease = nil, nil
	s.appendMu.Unlock()
	if reached != nil {
		close(reached)
		select {
		case <-release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return created, nil
}

func (s *writerPreferenceStore) pauseNextQueuedRead() (chan struct{}, chan struct{}) {
	s.nextMu.Lock()
	defer s.nextMu.Unlock()
	s.nextReached = make(chan struct{})
	s.nextRelease = make(chan struct{})
	return s.nextReached, s.nextRelease
}

func (s *writerPreferenceStore) NextQueuedTurn(
	ctx context.Context,
	conversationID string,
) (domain.QueuedTurn, error) {
	queued, err := s.Store.NextQueuedTurn(ctx, conversationID)
	if err != nil {
		return queued, err
	}
	s.nextMu.Lock()
	reached, release := s.nextReached, s.nextRelease
	s.nextReached, s.nextRelease = nil, nil
	s.nextMu.Unlock()
	if reached != nil {
		close(reached)
		select {
		case <-release:
		case <-ctx.Done():
			return domain.QueuedTurn{}, ctx.Err()
		}
	}
	return queued, nil
}

func TestEditMessageNewBranchDoesNotReacquireOwnedProjectLease(t *testing.T) {
	st := openStore(t)
	ownedStore := newWriterPreferenceStore(st)
	h, _, _ := newEditHarnessWithOwnedStore(t, st, ownedStore, domain.KindOrchestrator, false, func(r chatsvc.SnapshotReader) chatsvc.SnapshotReader { return r }, nil)
	ctx := context.Background()
	first := completeTurn(t, h, "first", "provider-turn-1")
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Messages) == 2
	})

	exerciseEditAgainstWaitingRebind(t, st, ownedStore, func() error {
		_, err := h.svc.EditMessage(ctx, testSession, first, ports.ChatUserMessage{
			Text: "edited first", ClientMessageID: "owned-new-branch", Origin: domain.MessageOriginHuman,
		})
		return err
	}, nil)
}

func TestEditMessageActiveBranchRetryDoesNotReacquireOwnedProjectLease(t *testing.T) {
	st := openStore(t)
	ownedStore := newWriterPreferenceStore(st)
	h, _, driver := newEditHarnessWithOwnedStore(t, st, ownedStore, domain.KindOrchestrator, true, func(r chatsvc.SnapshotReader) chatsvc.SnapshotReader { return r }, nil)
	ctx := context.Background()
	completeTurn(t, h, "first", "provider-turn-1")
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Messages) == 2
	})
	second := completeTurn(t, h, "second", "provider-turn-2")
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Messages) == 4
	})
	driver.mu.Lock()
	replacement := driver.fresh
	replacement.mu.Lock()
	replacement.sendErr = errors.New("provider unavailable")
	replacement.mu.Unlock()
	driver.mu.Unlock()
	failed, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "edited second", ClientMessageID: "owned-retry-seed", Origin: domain.MessageOriginHuman,
	})
	if err == nil {
		t.Fatal("seed EditMessage succeeded despite provider refusal")
	}
	if failed.Turn.ID == "" {
		t.Fatalf("seed EditMessage did not preserve its durable retry turn: %+v", failed)
	}
	replacement.mu.Lock()
	replacement.sendErr = nil
	replacement.mu.Unlock()

	exerciseEditAgainstWaitingRebind(t, st, ownedStore, func() error {
		_, err := h.svc.EditMessage(ctx, testSession, failed.Turn.ID, ports.ChatUserMessage{
			Text: "edited second", ClientMessageID: "owned-retry", Origin: domain.MessageOriginHuman,
		})
		return err
	}, nil)
}

func TestEditMessageRefusedRetryRestoresBranchWithoutReacquiringOwnedProjectLease(t *testing.T) {
	st := openStore(t)
	ownedStore := newWriterPreferenceStore(st)
	h, _, _ := newEditHarnessWithOwnedStore(t, st, ownedStore, domain.KindOrchestrator, false, func(r chatsvc.SnapshotReader) chatsvc.SnapshotReader { return r }, nil)
	ctx := context.Background()
	completeTurn(t, h, "first", "provider-turn-1")
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Messages) == 2
	})
	second := completeTurn(t, h, "second", "provider-turn-2")
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Messages) == 4
	})
	conversation, err := st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	anchor, err := st.ConversationEditAnchor(ctx, conversation.ID, second)
	if err != nil {
		t.Fatalf("ConversationEditAnchor: %v", err)
	}
	incomplete := domain.ConversationBranch{
		ID: "owned-incomplete-edit", ConversationID: conversation.ID, SessionID: testSession,
		ProviderConversationID: h.ctrl.ProviderConversationID(), ParentBranchID: conversation.ActiveBranchID,
		ReplacedTurnID: second, ForkAfterSequence: anchor.ForkAfterSequence,
		Strategy: domain.ConversationBranchStrategyNative, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateAndActivateConversationBranch(
		ctx, testSession, incomplete, h.ctrl.Generation(), incomplete.CreatedAt); err != nil {
		t.Fatalf("CreateAndActivateConversationBranch: %v", err)
	}
	h.conv.mu.Lock()
	h.conv.sendErr = refusedError{msg: "provider declined edit retry"}
	h.conv.mu.Unlock()

	exerciseEditAgainstWaitingRebind(t, st, ownedStore, func() error {
		_, err := h.svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
			Text: "edited second", ClientMessageID: "owned-refusal-retry", Origin: domain.MessageOriginHuman,
		})
		return err
	}, chatsvc.ErrProviderRefused)
}

func exerciseEditAgainstWaitingRebind(
	t *testing.T,
	st *sqlite.Store,
	ownedStore *writerPreferenceStore,
	edit func() error,
	wantErr error,
) {
	t.Helper()
	ctx := context.Background()
	appendReached, appendRelease := ownedStore.pauseNextAppend()
	t.Cleanup(func() {
		closeGate(appendRelease)
		closeGate(ownedStore.allowLateReader)
	})
	editDone := make(chan error, 1)
	go func() { editDone <- edit() }()
	select {
	case <-appendReached:
	case err := <-editDone:
		t.Fatalf("EditMessage returned before replacement append pause: %v", err)
	case <-time.After(4 * time.Second):
		t.Fatal("EditMessage did not append its replacement prompt")
	}

	replacement, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create replacement owner: %v", err)
	}
	rebindDone := make(chan error, 1)
	go func() { rebindDone <- ownedStore.rebind(ctx, replacement.ID, time.Now().UTC()) }()
	<-ownedStore.writerQueued
	closeGate(appendRelease)

	select {
	case <-ownedStore.lateReaderAttempt:
		closeGate(ownedStore.allowLateReader)
		<-editDone
		<-rebindDone
		t.Fatal("EditMessage recursively acquired project ownership behind a waiting rebind")
	case editErr := <-editDone:
		if wantErr == nil && editErr != nil {
			t.Fatalf("EditMessage: %v", editErr)
		}
		if wantErr != nil && !errors.Is(editErr, wantErr) {
			t.Fatalf("EditMessage error = %v, want %v", editErr, wantErr)
		}
	}
	if err := <-rebindDone; err != nil {
		t.Fatalf("rebind after EditMessage: %v", err)
	}
}

func TestHandoffDrainDoesNotReacquireOwnedProjectLease(t *testing.T) {
	provider := newFakeConversation()
	var ownedStore *writerPreferenceStore
	h := newHarnessWithConversationAndStoreKind(
		t,
		provider,
		func(st *sqlite.Store) chatsvc.Store {
			ownedStore = newWriterPreferenceStore(st)
			return ownedStore
		},
		domain.KindOrchestrator,
	)
	ctx := context.Background()
	created, err := h.st.AppendUserMessage(
		ctx, h.ctrl.ConversationID(), testSession, h.ctrl.Generation(),
		domain.ConversationMessage{
			ID: "handoff-owned-message", Text: "accepted queue", Origin: domain.MessageOriginHuman,
			ClientMessageID: "handoff-owned-client",
		},
		"handoff-owned-turn", h.now(),
	)
	if err != nil || !created {
		t.Fatalf("seed accepted queue: created=%v err=%v", created, err)
	}
	nextReached, nextRelease := ownedStore.pauseNextQueuedRead()
	t.Cleanup(func() {
		closeGate(nextRelease)
		closeGate(ownedStore.allowLateReader)
	})
	handoffDone := make(chan error, 1)
	go func() {
		handoffDone <- h.svc.PrepareChatHandoff(
			ctx, testSession, domain.SessionInterfaceTransitionDrain,
		)
	}()
	select {
	case <-nextReached:
	case err := <-handoffDone:
		t.Fatalf("handoff returned before reading its accepted queue: %v", err)
	case <-time.After(4 * time.Second):
		t.Fatal("handoff did not reach its accepted queue")
	}

	replacement, err := h.st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create replacement owner: %v", err)
	}
	rebindDone := make(chan error, 1)
	go func() { rebindDone <- ownedStore.rebind(ctx, replacement.ID, time.Now().UTC()) }()
	<-ownedStore.writerQueued
	closeGate(nextRelease)

	deadline := time.Now().Add(4 * time.Second)
	for len(provider.sentTexts()) == 0 && time.Now().Before(deadline) {
		select {
		case <-ownedStore.lateReaderAttempt:
			closeGate(ownedStore.allowLateReader)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	if sent := provider.sentTexts(); len(sent) != 1 || sent[0] != "accepted queue" {
		t.Fatalf("handoff provider sends = %v, want its accepted queue", sent)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})
	if err := <-handoffDone; err != nil {
		t.Fatalf("PrepareChatHandoff: %v", err)
	}
	if err := <-rebindDone; err != nil {
		t.Fatalf("rebind after handoff drain: %v", err)
	}
	select {
	case <-ownedStore.lateReaderAttempt:
		t.Fatal("handoff drain recursively acquired project ownership behind a waiting rebind")
	default:
	}
}

func TestRunningHandoffCompletionDoesNotReacquireOwnedProjectLease(t *testing.T) {
	provider := newFakeConversation()
	var ownedStore *writerPreferenceStore
	h := newHarnessWithConversationAndStoreKind(
		t,
		provider,
		func(st *sqlite.Store) chatsvc.Store {
			ownedStore = newWriterPreferenceStore(st)
			return ownedStore
		},
		domain.KindOrchestrator,
	)
	ctx := context.Background()
	if _, err := h.ctrl.Send(ctx, ports.ChatUserMessage{
		Text: "running root", ClientMessageID: "running-root", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send running root: %v", err)
	}
	if _, err := h.ctrl.Send(ctx, ports.ChatUserMessage{
		Text: "accepted queue", ClientMessageID: "running-queue", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("queue accepted turn: %v", err)
	}

	ownershipAcquired := ownedStore.observeNextOwnershipAcquire()
	t.Cleanup(func() { closeGate(ownedStore.allowLateReader) })
	handoffDone := make(chan error, 1)
	go func() {
		handoffDone <- h.svc.PrepareChatHandoff(
			ctx, testSession, domain.SessionInterfaceTransitionDrain,
		)
	}()
	select {
	case <-ownershipAcquired:
	case err := <-handoffDone:
		t.Fatalf("handoff returned before acquiring project ownership: %v", err)
	case <-time.After(4 * time.Second):
		t.Fatal("handoff did not acquire project ownership")
	}

	replacement, err := h.st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create replacement owner: %v", err)
	}
	rebindDone := make(chan error, 1)
	go func() { rebindDone <- ownedStore.rebind(ctx, replacement.ID, time.Now().UTC()) }()
	<-ownedStore.writerQueued
	<-ownedStore.writerBlocked

	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})
	reacquired := false
	deadline := time.Now().Add(4 * time.Second)
	for len(provider.sentTexts()) < 2 && time.Now().Before(deadline) {
		select {
		case <-ownedStore.lateReaderAttempt:
			reacquired = true
			closeGate(ownedStore.allowLateReader)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	if sent := provider.sentTexts(); len(sent) != 2 || sent[1] != "accepted queue" {
		t.Fatalf("handoff provider sends = %v, want accepted queue after root", sent)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-2",
		TurnState: domain.TurnStateCompleted,
	})
	if err := <-handoffDone; err != nil {
		t.Fatalf("PrepareChatHandoff: %v", err)
	}
	if err := <-rebindDone; err != nil {
		t.Fatalf("rebind after running handoff: %v", err)
	}
	if reacquired {
		t.Fatal("running completion recursively acquired project ownership behind a waiting rebind")
	}
	select {
	case <-ownedStore.lateReaderAttempt:
		t.Fatal("running completion recursively acquired project ownership behind a waiting rebind")
	default:
	}
}

type blockingRateLimitConversation struct {
	*fakeConversation
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRateLimitConversation() *blockingRateLimitConversation {
	return &blockingRateLimitConversation{
		fakeConversation: newFakeConversation(),
		started:          make(chan struct{}),
		release:          make(chan struct{}),
	}
}

func (c *blockingRateLimitConversation) ReadRateLimits(ctx context.Context) (ports.ChatRateLimits, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return ports.ChatRateLimits{
			PrimaryUsedPercent: 12, SecondaryUsedPercent: -1, PlanLabel: "retired",
		}, nil
	case <-ctx.Done():
		return ports.ChatRateLimits{}, ctx.Err()
	}
}

func TestStartupRateLimitReadCannotPublishAfterProjectRebind(t *testing.T) {
	provider := newBlockingRateLimitConversation()
	var ownedStore *writerPreferenceStore
	h := newHarnessWithConversationAndStoreOptions(
		t,
		provider,
		func(st *sqlite.Store) chatsvc.Store {
			ownedStore = newWriterPreferenceStore(st)
			return ownedStore
		},
		domain.KindOrchestrator, domain.HarnessClaudeCode,
	)
	t.Cleanup(func() {
		closeGate(provider.release)
		_ = provider.Close()
	})
	select {
	case <-provider.started:
	case <-time.After(4 * time.Second):
		t.Fatal("startup rate-limit read did not reach the provider")
	}

	ctx := context.Background()
	replacement, err := h.st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create replacement owner: %v", err)
	}
	rebindDone := make(chan error, 1)
	go func() { rebindDone <- ownedStore.rebind(ctx, replacement.ID, time.Now().UTC()) }()
	<-ownedStore.writerQueued

	newLimits := domain.ConversationRateLimits{
		PrimaryUsedPercent: 88, SecondaryUsedPercent: -1, PlanLabel: "current",
	}
	select {
	case <-ownedStore.writerDone:
		if err := <-rebindDone; err != nil {
			t.Fatalf("rebind before retired rate-limit response: %v", err)
		}
		if err := h.st.RecordRateLimits(ctx, h.ctrl.ConversationID(), newLimits); err != nil {
			t.Fatalf("record current owner limits: %v", err)
		}
		closeGate(provider.release)
		<-ownedStore.rateLimitsRecorded
	case <-ownedStore.writerBlocked:
		closeGate(provider.release)
		<-ownedStore.rateLimitsRecorded
		if err := <-rebindDone; err != nil {
			t.Fatalf("rebind after startup rate-limit read: %v", err)
		}
		if err := h.st.RecordRateLimits(ctx, h.ctrl.ConversationID(), newLimits); err != nil {
			t.Fatalf("record current owner limits: %v", err)
		}
	}

	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if snapshot.Conversation.RateLimits == nil ||
		snapshot.Conversation.RateLimits.PrimaryUsedPercent != 88 ||
		snapshot.Conversation.RateLimits.PlanLabel != "current" {
		t.Fatalf("rate limits after rebind = %+v, want current owner values", snapshot.Conversation.RateLimits)
	}
}
