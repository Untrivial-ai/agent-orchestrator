package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	codexagent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type transitionStore struct {
	*fakeStore
	mu             sync.Mutex
	transitions    map[string]domain.SessionInterfaceTransition
	messages       map[string][]domain.SessionInterfaceTransitionMessage
	nextMessage    int64
	loseCancelCAS  bool
	activeErr      error
	messenger      *fakeMessenger
	markMessageErr error
}

func newTransitionStore() *transitionStore {
	return &transitionStore{
		fakeStore:   newFakeStore(),
		transitions: make(map[string]domain.SessionInterfaceTransition),
		messages:    make(map[string][]domain.SessionInterfaceTransitionMessage),
	}
}

func (s *transitionStore) CreateSessionInterfaceTransition(_ context.Context, rec domain.SessionInterfaceTransition) (domain.SessionInterfaceTransition, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.transitions {
		if existing.SessionID == rec.SessionID && existing.Active() {
			return existing, false, nil
		}
	}
	s.transitions[rec.ID] = rec
	return rec, true, nil
}

func (s *transitionStore) GetSessionInterfaceTransition(_ context.Context, id string) (domain.SessionInterfaceTransition, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.transitions[id]
	return rec, ok, nil
}

func (s *transitionStore) GetActiveSessionInterfaceTransition(_ context.Context, id domain.SessionID) (domain.SessionInterfaceTransition, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeErr != nil {
		return domain.SessionInterfaceTransition{}, false, s.activeErr
	}
	for _, rec := range s.transitions {
		if rec.SessionID == id && rec.Active() {
			return rec, true, nil
		}
	}
	return domain.SessionInterfaceTransition{}, false, nil
}

func (s *transitionStore) GetLatestSessionInterfaceTransition(_ context.Context, id domain.SessionID) (domain.SessionInterfaceTransition, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest domain.SessionInterfaceTransition
	found := false
	for _, rec := range s.transitions {
		if rec.SessionID == id && (!found || rec.CreatedAt.After(latest.CreatedAt)) {
			latest, found = rec, true
		}
	}
	return latest, found, nil
}

func (s *transitionStore) ListActiveSessionInterfaceTransitions(context.Context) ([]domain.SessionInterfaceTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.SessionInterfaceTransition
	for _, rec := range s.transitions {
		if rec.Active() {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (s *transitionStore) ListDeliverableSessionInterfaceTransitions(context.Context) ([]domain.SessionInterfaceTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.SessionInterfaceTransition
	for _, rec := range s.transitions {
		if !rec.Phase.Terminal() {
			continue
		}
		for _, message := range s.messages[rec.ID] {
			if message.DeliveredAt.IsZero() {
				out = append(out, rec)
				break
			}
		}
	}
	return out, nil
}

func (s *transitionStore) AdvanceSessionInterfaceTransition(_ context.Context, id string, expected, next domain.SessionInterfaceTransitionPhase, nativeID, errorCode, errorDetail string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.transitions[id]
	if !ok || rec.Phase != expected {
		return false, nil
	}
	if next == domain.SessionInterfaceTransitionCancelled && s.loseCancelCAS {
		rec.Phase = domain.SessionInterfaceTransitionSourceStopping
		s.transitions[id] = rec
		return false, nil
	}
	rec.Phase = next
	rec.NativeConversationID = nativeID
	rec.ErrorCode = errorCode
	rec.ErrorDetail = errorDetail
	rec.UpdatedAt = now
	if next.Terminal() {
		rec.CompletedAt = now
	}
	s.transitions[id] = rec
	return true, nil
}

func (s *transitionStore) EnqueueSessionInterfaceTransitionMessage(_ context.Context, transitionID, clientMessageID, message string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextMessage++
	s.messages[transitionID] = append(s.messages[transitionID], domain.SessionInterfaceTransitionMessage{
		ID: s.nextMessage, TransitionID: transitionID, ClientMessageID: clientMessageID,
		Message: message, CreatedAt: now,
	})
	return nil
}

func (s *transitionStore) ListPendingSessionInterfaceTransitionMessages(_ context.Context, transitionID string) ([]domain.SessionInterfaceTransitionMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.SessionInterfaceTransitionMessage
	for _, message := range s.messages[transitionID] {
		if message.DeliveredAt.IsZero() {
			out = append(out, message)
		}
	}
	return out, nil
}

func (s *transitionStore) MarkSessionInterfaceTransitionMessageDelivered(_ context.Context, id int64, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markMessageErr != nil {
		return s.markMessageErr
	}
	for transitionID, messages := range s.messages {
		for i := range messages {
			if messages[i].ID == id {
				messages[i].DeliveredAt = now
				s.messages[transitionID] = messages
				return nil
			}
		}
	}
	return nil
}

type transitionAgent struct{ fakeAgent }

func (transitionAgent) NativeConversationID(_ context.Context, session ports.SessionRef, mode domain.SessionMode, providerID string) (string, bool, error) {
	if mode == domain.SessionModeChat {
		return providerID, providerID != "", nil
	}
	id := session.Metadata[ports.MetadataKeyAgentSessionID]
	return id, id != "", nil
}

type failingRestoreTransitionAgent struct {
	transitionAgent
	err error
}

func (a failingRestoreTransitionAgent) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error) {
	return nil, false, a.err
}

type transitionDetectorAgent struct{ transitionAgent }

func (transitionDetectorAgent) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	if output == "idle" {
		return domain.ActivityIdle, true
	}
	return "", false
}

const (
	idleTerminalOutput      = "idle"
	ambiguousTerminalOutput = "ambiguous"
)

type emptyTransitionAgent struct{ transitionAgent }

func (emptyTransitionAgent) NativeConversationExists(context.Context, ports.SessionRef, string, map[string]string) (bool, error) {
	return false, nil
}

type transitionRuntime struct {
	*fakeRuntime
	log                        *[]string
	stopErrors                 []error
	runtimeOccupied            bool
	outputForCall              func(int) string
	outputCallTimes            []time.Time
	blockAliveUntilContextDone bool
}

func (r *transitionRuntime) Interrupt(_ context.Context, handle ports.RuntimeHandle) error {
	*r.log = append(*r.log, "interrupt:tui:"+handle.ID)
	return nil
}

func (r *transitionRuntime) IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	if r.blockAliveUntilContextDone {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return r.fakeRuntime.IsAlive(ctx, handle)
}

func (r *transitionRuntime) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	*r.log = append(*r.log, "stop:tui:"+handle.ID)
	if len(r.stopErrors) > 0 {
		err := r.stopErrors[0]
		r.stopErrors = r.stopErrors[1:]
		if err != nil {
			return err
		}
	}
	err := r.fakeRuntime.Destroy(ctx, handle)
	if err == nil {
		r.runtimeOccupied = false
	}
	return err
}

func (r *transitionRuntime) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	*r.log = append(*r.log, "start:tui")
	if r.runtimeOccupied {
		return ports.RuntimeHandle{}, fmt.Errorf("session %q already exists", cfg.SessionID)
	}
	handle, err := r.fakeRuntime.Create(ctx, cfg)
	if err == nil {
		r.runtimeOccupied = true
	}
	return handle, err
}

func (r *transitionRuntime) GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	r.outputCallTimes = append(r.outputCallTimes, time.Now())
	if r.outputForCall == nil {
		return r.fakeRuntime.GetOutput(ctx, handle, lines)
	}
	r.outputCalls++
	return r.outputForCall(r.outputCalls), nil
}

type transitionChat struct {
	log              *[]string
	armed            chan domain.SessionInterfaceTransitionPolicy
	aborted          chan struct{}
	armErr           error
	preparedPolicy   domain.SessionInterfaceTransitionPolicy
	start            ChatStart
	preflightErr     error
	startErr         error
	preflightStarted chan struct{}
	preflightRelease chan struct{}
	relayMessages    []string
	relayIDs         []string
	supportsChat     bool
}

func (c *transitionChat) SupportsChat(_ domain.AgentHarness) bool {
	return c.supportsChat
}

func (c *transitionChat) PreflightChat(
	ctx context.Context,
	_ domain.AgentHarness,
	_ ports.PermissionMode,
) error {
	if c.preflightStarted != nil {
		select {
		case c.preflightStarted <- struct{}{}:
		default:
		}
	}
	if c.preflightRelease != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.preflightRelease:
		}
	}
	return c.preflightErr
}
func (c *transitionChat) StartChat(_ context.Context, cfg ChatStart) (ChatStarted, error) {
	c.start = cfg
	*c.log = append(*c.log, "start:chat")
	if c.startErr != nil {
		return ChatStarted{}, c.startErr
	}
	started := ChatStarted{ProviderConversationID: cfg.ProviderConversationID, ControllerGeneration: "chat-generation"}
	if cfg.ControllerReady != nil {
		if err := cfg.ControllerReady(started); err != nil {
			return ChatStarted{}, err
		}
	}
	return started, nil
}
func (*transitionChat) StartChatTurn(context.Context, domain.SessionID, string) (string, error) {
	return "", nil
}
func (c *transitionChat) RelayChatTurn(_ context.Context, _ domain.SessionID, text string) (string, error) {
	c.relayMessages = append(c.relayMessages, text)
	c.relayIDs = append(c.relayIDs, "")
	return "", nil
}
func (c *transitionChat) RelayChatTurnWithID(_ context.Context, _ domain.SessionID, text, clientMessageID string) (string, error) {
	c.relayMessages = append(c.relayMessages, text)
	c.relayIDs = append(c.relayIDs, clientMessageID)
	return "", nil
}
func (*transitionChat) HasLiveChatController(domain.SessionID) bool { return false }
func (c *transitionChat) StopChat(_ context.Context, _ domain.SessionID) error {
	*c.log = append(*c.log, "stop:chat")
	return nil
}
func (c *transitionChat) ArmChatHandoff(_ context.Context, _ domain.SessionID, policy domain.SessionInterfaceTransitionPolicy) error {
	select {
	case c.armed <- policy:
	default:
	}
	return c.armErr
}
func (c *transitionChat) PrepareChatHandoff(_ context.Context, _ domain.SessionID, policy domain.SessionInterfaceTransitionPolicy) error {
	c.preparedPolicy = policy
	*c.log = append(*c.log, "prepare:chat:"+string(policy))
	return nil
}
func (c *transitionChat) AbortChatHandoff(domain.SessionID) {
	select {
	case c.aborted <- struct{}{}:
	default:
	}
}

type transitionInputGate struct {
	acquired    chan string
	released    chan string
	lastInputAt time.Time
}

func (g *transitionInputGate) BeginInputDrain(terminalID string) (time.Time, func()) {
	g.acquired <- terminalID
	var once sync.Once
	return g.lastInputAt, func() { once.Do(func() { g.released <- terminalID }) }
}

func TestTUIIdleAfterInputRequiresANewerIdleFact(t *testing.T) {
	inputAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	rec := domain.SessionRecord{Activity: domain.Activity{
		State: domain.ActivityIdle, LastActivityAt: inputAt.Add(-time.Millisecond),
	}}
	if tuiIdleAfterInput(rec, inputAt) {
		t.Fatal("an idle fact older than accepted terminal input was treated as drained")
	}
	rec.Activity.LastActivityAt = inputAt
	if !tuiIdleAfterInput(rec, inputAt) {
		t.Fatal("an idle fact at the terminal input barrier was not accepted")
	}
	rec.Activity.State = domain.ActivityActive
	if tuiIdleAfterInput(rec, inputAt) {
		t.Fatal("active work was treated as drained")
	}
}

func newTransitionManager(t *testing.T, mode domain.SessionMode) (*Manager, *transitionStore, *transitionRuntime, *transitionChat, *[]string) {
	t.Helper()
	store := newTransitionStore()
	store.projects["proj"] = domain.ProjectRecord{ID: "proj", Path: "/repo"}
	metadata := domain.SessionMetadata{
		WorkspacePath: "/ws/session-1", Branch: "ao/session-1", AgentSessionID: "native-1",
	}
	if mode == domain.SessionModeChat {
		metadata.ProviderConversationID = "native-1"
		metadata.ControllerGeneration = "old-chat-generation"
		metadata.RuntimeHandleID = ""
	} else {
		metadata.RuntimeHandleID = "runtime-1"
		metadata.RuntimeLaunchID = "old-tui-generation"
	}
	store.sessions["session-1"] = domain.SessionRecord{
		ID: "session-1", ProjectID: "proj", Kind: domain.KindWorker,
		Harness: domain.HarnessClaudeCode, Mode: mode, Metadata: metadata,
		Activity:      domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
		FirstSignalAt: time.Now(),
	}
	log := &[]string{}
	runtime := &transitionRuntime{fakeRuntime: &fakeRuntime{}, log: log}
	chat := &transitionChat{
		log: log, supportsChat: true,
		armed:   make(chan domain.SessionInterfaceTransitionPolicy, 1),
		aborted: make(chan struct{}, 1),
	}
	messenger := &fakeMessenger{}
	store.messenger = messenger
	counter := 0
	manager := New(Deps{
		Runtime: runtime, Agents: singleAgent{agent: transitionAgent{}}, Workspace: &fakeWorkspace{},
		Store: store, Messenger: messenger, Chat: chat,
		Lifecycle: &fakeLCM{store: store.fakeStore}, LookPath: func(string) (string, error) { return "/bin/true", nil },
		NewLaunchID: func() string { counter++; return fmt.Sprintf("generation-%d", counter) },
	})
	return manager, store, runtime, chat, log
}

func useFastInterfaceTransitionTimings(manager *Manager) {
	manager.interfaceTransition = interfaceTransitionConfig{
		pollInterval:   time.Millisecond,
		idleSettle:     5 * time.Millisecond,
		staleIdleLimit: 60 * time.Millisecond,
	}
}

func TestInterfaceTransitionStatusHidesSwitchWhenChatUnsupported(t *testing.T) {
	manager, _, _, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	chat.supportsChat = false

	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("InterfaceTransitionStatus: %v", err)
	}
	if status.TargetMode != domain.SessionModeChat {
		t.Fatalf("target mode = %q, want chat", status.TargetMode)
	}
	if status.Supported {
		t.Fatal("expected switch to be unsupported")
	}
	if status.ReasonCode != "CHAT_UNSUPPORTED" {
		t.Fatalf("reasonCode = %q, want CHAT_UNSUPPORTED", status.ReasonCode)
	}
}

func TestInterfaceTransitionStatusAllowsSwitchToTUIWhenChatUnsupported(t *testing.T) {
	manager, _, _, chat, _ := newTransitionManager(t, domain.SessionModeChat)
	chat.supportsChat = false

	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("InterfaceTransitionStatus: %v", err)
	}
	if status.TargetMode != domain.SessionModeTUI {
		t.Fatalf("target mode = %q, want tui", status.TargetMode)
	}
	if status.ReasonCode == "CHAT_UNSUPPORTED" {
		t.Fatal("switching back to TUI should not report CHAT_UNSUPPORTED")
	}
}

func TestInterfaceTransitionStatusAllowsFreshStartWithHistoryProbe(t *testing.T) {
	store := newTransitionStore()
	store.projects["proj"] = domain.ProjectRecord{ID: "proj", Path: "/repo"}
	store.sessions["session-1"] = domain.SessionRecord{
		ID: "session-1", ProjectID: "proj", Kind: domain.KindWorker,
		Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeTUI,
		Metadata:      domain.SessionMetadata{WorkspacePath: "/ws/session-1", Branch: "ao/session-1"},
		Activity:      domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
		FirstSignalAt: time.Now(),
	}
	log := &[]string{}
	runtime := &transitionRuntime{fakeRuntime: &fakeRuntime{}, log: log}
	chat := &transitionChat{log: log, supportsChat: true}
	manager := New(Deps{
		Runtime: runtime, Agents: singleAgent{agent: emptyTransitionAgent{}}, Workspace: &fakeWorkspace{},
		Store: store, Chat: chat, Lifecycle: &fakeLCM{store: store.fakeStore},
		LookPath:    func(string) (string, error) { return "/bin/true", nil },
		NewLaunchID: func() string { return "generation-1" },
	})

	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("InterfaceTransitionStatus: %v", err)
	}
	if !status.Supported {
		t.Fatalf("expected switch to be supported with history probe, got reasonCode=%q reason=%q",
			status.ReasonCode, status.Reason)
	}
}

func TestInterfaceTransitionStatusBlocksFreshStartWithoutHistoryProbe(t *testing.T) {
	store := newTransitionStore()
	store.projects["proj"] = domain.ProjectRecord{ID: "proj", Path: "/repo"}
	store.sessions["session-1"] = domain.SessionRecord{
		ID: "session-1", ProjectID: "proj", Kind: domain.KindWorker,
		Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeTUI,
		Metadata:      domain.SessionMetadata{WorkspacePath: "/ws/session-1", Branch: "ao/session-1"},
		Activity:      domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
		FirstSignalAt: time.Now(),
	}
	log := &[]string{}
	runtime := &transitionRuntime{fakeRuntime: &fakeRuntime{}, log: log}
	chat := &transitionChat{log: log, supportsChat: true}
	manager := New(Deps{
		Runtime: runtime, Agents: singleAgent{agent: transitionAgent{}}, Workspace: &fakeWorkspace{},
		Store: store, Chat: chat, Lifecycle: &fakeLCM{store: store.fakeStore},
		LookPath:    func(string) (string, error) { return "/bin/true", nil },
		NewLaunchID: func() string { return "generation-1" },
	})

	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("InterfaceTransitionStatus: %v", err)
	}
	if status.Supported {
		t.Fatal("expected switch to be unsupported without history probe and empty native id")
	}
	if status.ReasonCode != "NATIVE_SESSION_MISSING" {
		t.Fatalf("reasonCode = %q, want NATIVE_SESSION_MISSING", status.ReasonCode)
	}
}

func TestInterfaceTransitionStatusBlocksFreshStartWithHistoryProbeAndConversationHistory(t *testing.T) {
	store := newTransitionStore()
	store.projects["proj"] = domain.ProjectRecord{ID: "proj", Path: "/repo"}
	store.sessions["session-1"] = domain.SessionRecord{
		ID: "session-1", ProjectID: "proj", Kind: domain.KindWorker,
		Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeTUI,
		Metadata:      domain.SessionMetadata{WorkspacePath: "/ws/session-1", Branch: "ao/session-1", LatestUserPrompt: "implement the feature"},
		Activity:      domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
		FirstSignalAt: time.Now(),
	}
	log := &[]string{}
	runtime := &transitionRuntime{fakeRuntime: &fakeRuntime{}, log: log}
	chat := &transitionChat{log: log, supportsChat: true}
	manager := New(Deps{
		Runtime: runtime, Agents: singleAgent{agent: emptyTransitionAgent{}}, Workspace: &fakeWorkspace{},
		Store: store, Chat: chat, Lifecycle: &fakeLCM{store: store.fakeStore},
		LookPath:    func(string) (string, error) { return "/bin/true", nil },
		NewLaunchID: func() string { return "generation-1" },
	})

	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("InterfaceTransitionStatus: %v", err)
	}
	if status.Supported {
		t.Fatal("expected switch to be blocked when conversation history exists but native id is empty")
	}
	if status.ReasonCode != "NATIVE_SESSION_MISSING" {
		t.Fatalf("reasonCode = %q, want NATIVE_SESSION_MISSING", status.ReasonCode)
	}
}

func awaitTransition(t *testing.T, store *transitionStore, id string) domain.SessionInterfaceTransition {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		transition, ok, err := store.GetSessionInterfaceTransition(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if ok && transition.Phase.Terminal() {
			return transition
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("interface transition did not settle")
	return domain.SessionInterfaceTransition{}
}

func TestInterfaceTransitionTUIToChatStopsBeforeStartingAndReusesNativeConversation(t *testing.T) {
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeTUI)
	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1", domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	rec := store.sessions["session-1"]
	if rec.Mode != domain.SessionModeChat {
		t.Fatalf("mode = %s, want chat", rec.Mode)
	}
	if chat.start.ProviderConversationID != "native-1" {
		t.Fatalf("provider conversation = %q, want native-1", chat.start.ProviderConversationID)
	}
	if runtime.created != 0 {
		t.Fatalf("terminal runtime created %d times while switching to Chat", runtime.created)
	}
	if got := fmt.Sprint(*log); got != "[stop:tui:runtime-1 start:chat]" {
		t.Fatalf("controller order = %s", got)
	}
}

func TestInterfaceTransitionRollbackClearsStaleTUIRuntimeBeforeRestore(t *testing.T) {
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeTUI)
	runtime.runtimeOccupied = true
	runtime.stopErrors = []error{errors.New("teardown timed out")}
	runtime.aliveByHandle = map[string]bool{"runtime-1": false}
	chat.startErr = errors.New("ACP session/new: spawn EINVAL")

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "TARGET_RESUME_FAILED" {
		t.Fatalf("transition = %+v, want failed target resume after successful rollback", settled)
	}
	if got := store.sessions["session-1"].Mode; got != domain.SessionModeTUI {
		t.Fatalf("mode = %s, want restored TUI", got)
	}
	if runtime.created != 1 {
		t.Fatalf("restored runtime create count = %d, want 1", runtime.created)
	}
	wantLog := "[stop:tui:runtime-1 start:chat stop:chat stop:tui:runtime-1 start:tui]"
	if got := fmt.Sprint(*log); got != wantLog {
		t.Fatalf("controller order = %s, want %s", got, wantLog)
	}
}

func TestInterfaceTransitionTUIToChatDrainsAVisibleIdleComposerAfterNonSubmittingInput(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionDetectorAgent{}}
	now := time.Now()
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	runtime.outputs = []string{idleTerminalOutput}
	manager.SetTerminalInputGate(&transitionInputGate{
		acquired:    make(chan string, 1),
		released:    make(chan string, 1),
		lastInputAt: now,
	})

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if runtime.outputCalls < interfaceTransitionStaleIdleSamples {
		t.Fatalf("terminal output calls = %d, want at least %d consecutive idle samples",
			runtime.outputCalls, interfaceTransitionStaleIdleSamples)
	}
	if firstCapture := runtime.outputCallTimes[0]; firstCapture.Before(now.Add(manager.interfaceTransition.idleSettle)) {
		t.Fatalf("first terminal capture at %s, before input settled at %s",
			firstCapture, now.Add(manager.interfaceTransition.idleSettle))
	}
}

func TestInterfaceTransitionTUIToChatUsesANewerIdleFactWithoutReadingTerminalOutput(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionDetectorAgent{}}
	now := time.Now()
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	manager.SetTerminalInputGate(&transitionInputGate{
		acquired:    make(chan string, 1),
		released:    make(chan string, 1),
		lastInputAt: now,
	})

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if runtime.outputCalls != 0 {
		t.Fatalf("terminal output calls = %d, want timestamp proof to avoid capture", runtime.outputCalls)
	}
}

func TestInterfaceTransitionTUIToChatFailsClosedWhenStaleIdleCannotBeVerified(t *testing.T) {
	tests := []struct {
		name                       string
		agent                      ports.Agent
		outputs                    []string
		outputForCall              func(int) string
		blockAliveUntilContextDone bool
		wantCaptures               bool
	}{
		{name: "detector unavailable", agent: transitionAgent{}},
		{name: "detector ambiguous", agent: transitionDetectorAgent{}, outputs: []string{ambiguousTerminalOutput}, wantCaptures: true},
		{
			name:  "idle and ambiguous captures keep alternating",
			agent: transitionDetectorAgent{},
			outputForCall: func(call int) string {
				if call%2 == 1 {
					return idleTerminalOutput
				}
				return ambiguousTerminalOutput
			},
			wantCaptures: true,
		},
		{
			name:                       "liveness probe reaches proof deadline",
			agent:                      transitionDetectorAgent{},
			outputs:                    []string{ambiguousTerminalOutput},
			blockAliveUntilContextDone: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
			useFastInterfaceTransitionTimings(manager)
			manager.agents = singleAgent{agent: tt.agent}
			now := time.Now()
			rec := store.sessions["session-1"]
			rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}
			store.sessions["session-1"] = rec
			runtime.aliveByHandle = map[string]bool{"runtime-1": true}
			runtime.outputs = tt.outputs
			runtime.outputForCall = tt.outputForCall
			runtime.blockAliveUntilContextDone = tt.blockAliveUntilContextDone
			gate := &transitionInputGate{
				acquired:    make(chan string, 1),
				released:    make(chan string, 1),
				lastInputAt: now,
			}
			manager.SetTerminalInputGate(gate)

			transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
				domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
			if err != nil {
				t.Fatal(err)
			}
			settled := awaitTransition(t, store, transition.ID)
			if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "DRAIN_QUIESCENCE_UNVERIFIED" {
				t.Fatalf("transition = %+v, want failed DRAIN_QUIESCENCE_UNVERIFIED", settled)
			}
			if !strings.Contains(settled.ErrorDetail, "source interface was left untouched") ||
				strings.Contains(settled.ErrorDetail, "session:") {
				t.Fatalf("error detail = %q, want actionable user-facing source-preservation text", settled.ErrorDetail)
			}
			if runtime.destroyed != 0 {
				t.Fatalf("source runtime destroyed %d times after unverified drain", runtime.destroyed)
			}
			if tt.wantCaptures && runtime.outputCalls == 0 {
				t.Fatal("terminal detector was not consulted")
			}
			select {
			case <-gate.released:
			case <-time.After(time.Second):
				t.Fatal("terminal input gate remained closed after drain failure")
			}
		})
	}
}

func TestInterfaceTransitionTUIToChatAcceptsConfirmedRuntimeExitDuringStaleIdle(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	now := time.Now()
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": false}
	manager.SetTerminalInputGate(&transitionInputGate{
		acquired:    make(chan string, 1),
		released:    make(chan string, 1),
		lastInputAt: now,
	})

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("transition = %+v, want confirmed runtime exit to complete the handoff", settled)
	}
	if runtime.outputCalls != 0 {
		t.Fatalf("terminal output calls = %d, want confirmed exit before the proof window", runtime.outputCalls)
	}
}

func TestInterfaceTransitionTUIToChatDoesNotTimeOutActiveWorkOrDecisions(t *testing.T) {
	for _, state := range []domain.ActivityState{
		domain.ActivityActive,
		domain.ActivityWaitingInput,
		domain.ActivityBlocked,
	} {
		t.Run(string(state), func(t *testing.T) {
			manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
			useFastInterfaceTransitionTimings(manager)
			rec := store.sessions["session-1"]
			rec.Activity = domain.Activity{State: state, LastActivityAt: time.Now().Add(-time.Hour)}
			store.sessions["session-1"] = rec
			runtime.aliveByHandle = map[string]bool{"runtime-1": true}
			gate := &transitionInputGate{
				acquired:    make(chan string, 1),
				released:    make(chan string, 1),
				lastInputAt: time.Now(),
			}
			manager.SetTerminalInputGate(gate)

			transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
				domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				current, _, readErr := store.GetSessionInterfaceTransition(context.Background(), transition.ID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if current.Phase == domain.SessionInterfaceTransitionDraining {
					break
				}
				time.Sleep(time.Millisecond)
			}
			time.Sleep(2 * manager.interfaceTransition.staleIdleLimit)
			current, _, err := store.GetSessionInterfaceTransition(context.Background(), transition.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Phase != domain.SessionInterfaceTransitionDraining {
				t.Fatalf("phase = %s, want draining while source is %s", current.Phase, state)
			}
			if err := manager.CancelInterfaceTransition(context.Background(), "session-1"); err != nil {
				t.Fatal(err)
			}
			if runtime.destroyed != 0 {
				t.Fatalf("source runtime destroyed %d times while cancelling %s drain", runtime.destroyed, state)
			}
			select {
			case <-gate.released:
			case <-time.After(time.Second):
				t.Fatal("terminal input gate remained closed after cancellation")
			}
		})
	}
}

func TestInterfaceTransitionGatesTUIInputBeforePreflightAndReleasesIt(t *testing.T) {
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	gate := &transitionInputGate{acquired: make(chan string, 1), released: make(chan string, 1)}
	manager.SetTerminalInputGate(gate)
	chat.preflightStarted = make(chan struct{}, 1)
	chat.preflightRelease = make(chan struct{})

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case terminalID := <-gate.acquired:
		if terminalID != "runtime-1" {
			t.Fatalf("gated terminal = %q, want runtime-1", terminalID)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal input was not gated")
	}
	select {
	case <-chat.preflightStarted:
	case <-time.After(time.Second):
		t.Fatal("preflight did not start")
	}
	select {
	case <-gate.released:
		t.Fatal("terminal input gate released while transition was still preflighting")
	default:
	}

	close(chat.preflightRelease)
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	select {
	case terminalID := <-gate.released:
		if terminalID != "runtime-1" {
			t.Fatalf("released terminal = %q, want runtime-1", terminalID)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal input gate was not released after transition")
	}
}

func TestInterfaceTransitionReleasesTUIInputAfterPreflightFailure(t *testing.T) {
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	gate := &transitionInputGate{acquired: make(chan string, 1), released: make(chan string, 1)}
	manager.SetTerminalInputGate(gate)
	chat.preflightErr = errors.New("provider unavailable")

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed {
		t.Fatalf("phase = %s, want failed", settled.Phase)
	}
	select {
	case terminalID := <-gate.released:
		if terminalID != "runtime-1" {
			t.Fatalf("released terminal = %q, want runtime-1", terminalID)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal input gate remained closed after preflight failure")
	}
}

func TestInterfaceTransitionTUIToChatRebuildsOrchestratorStandingContext(t *testing.T) {
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	rec := store.sessions["session-1"]
	rec.Kind = domain.KindOrchestrator
	store.sessions["session-1"] = rec

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if !strings.Contains(chat.start.SystemPrompt, "human-facing orchestrator") {
		t.Fatalf("Chat target did not receive orchestrator standing context: %q", chat.start.SystemPrompt)
	}
}

func TestInterfaceTransitionTUIToChatStartsFreshWhenReservedIDHasNoHistory(t *testing.T) {
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeTUI)
	manager.agents = singleAgent{agent: emptyTransitionAgent{}}

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if settled.NativeConversationID != "" {
		t.Fatalf("native conversation = %q, want fresh sentinel", settled.NativeConversationID)
	}
	if chat.start.ProviderConversationID != "" {
		t.Fatalf("Chat resumed %q, want a fresh conversation", chat.start.ProviderConversationID)
	}
	if runtime.created != 0 {
		t.Fatalf("terminal runtime created %d times while switching to Chat", runtime.created)
	}
	if got := fmt.Sprint(*log); got != "[stop:tui:runtime-1 start:chat]" {
		t.Fatalf("controller order = %s", got)
	}
}

func TestInterfaceTransitionTUIToChatStartsFreshWhenCodexRolloutIsMissing(t *testing.T) {
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	t.Setenv("CODEX_HOME", t.TempDir())
	manager.agents = singleAgent{agent: codexagent.New()}
	rec := store.sessions["session-1"]
	rec.Harness = domain.HarnessCodex
	rec.Metadata.AgentSessionID = "019fc430-1234-7abc-8def-0123456789ab"
	store.sessions["session-1"] = rec

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if settled.NativeConversationID != "" {
		t.Fatalf("native conversation = %q, want fresh sentinel", settled.NativeConversationID)
	}
	if chat.start.ProviderConversationID != "" {
		t.Fatalf("Chat resumed missing Codex rollout %q, want a fresh conversation",
			chat.start.ProviderConversationID)
	}
}

func TestInterfaceTransitionTUIToChatReusesPersistedCodexRollout(t *testing.T) {
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	manager.agents = singleAgent{agent: codexagent.New()}
	id := "019fc430-1234-7abc-8def-0123456789ab"
	rec := store.sessions["session-1"]
	rec.Harness = domain.HarnessCodex
	rec.Metadata.AgentSessionID = id
	store.sessions["session-1"] = rec
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "08", "08")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(rolloutDir, "rollout-2026-08-08T10-00-00-"+id+".jsonl")
	if err := os.WriteFile(rollout, []byte("{\"type\":\"session_meta\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if settled.NativeConversationID != id {
		t.Fatalf("native conversation = %q, want %q", settled.NativeConversationID, id)
	}
	if chat.start.ProviderConversationID != id {
		t.Fatalf("Chat resumed %q, want persisted Codex rollout %q",
			chat.start.ProviderConversationID, id)
	}
}

// TestInterfaceTransitionFreshTUISessionResumesAfterHookCapture is a regression
// test for a fresh TUI session where the SessionStart hook captures the native
// session id. The transition must resume the persisted Codex conversation
// rather than starting fresh, proving the identifiers are populated before
// transition and the original conversation is resumed afterward.
func TestInterfaceTransitionFreshTUISessionResumesAfterHookCapture(t *testing.T) {
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	manager.agents = singleAgent{agent: codexagent.New()}

	id := "019fc430-1234-7abc-8def-0123456789ab"
	rec := store.sessions["session-1"]
	rec.Harness = domain.HarnessCodex
	rec.Metadata.AgentSessionID = ""
	store.sessions["session-1"] = rec

	rec = store.sessions["session-1"]
	rec.Metadata.AgentSessionID = id
	store.sessions["session-1"] = rec

	preRec, ok, err := store.GetSession(context.Background(), "session-1")
	if err != nil || !ok {
		t.Fatalf("read pre-transition session: %v %v", err, ok)
	}
	if preRec.Metadata.AgentSessionID != id {
		t.Fatalf("pre-transition AgentSessionID = %q, want %q (hook capture failed)",
			preRec.Metadata.AgentSessionID, id)
	}

	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "08", "08")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(rolloutDir, "rollout-2026-08-08T10-00-00-"+id+".jsonl")
	if err := os.WriteFile(rollout, []byte("{\"type\":\"session_meta\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if settled.NativeConversationID != id {
		t.Fatalf("native conversation = %q, want %q (transition did not resume the captured id)",
			settled.NativeConversationID, id)
	}
	if chat.start.ProviderConversationID != id {
		t.Fatalf("Chat resumed %q, want persisted Codex rollout %q (fresh-started instead of resuming)",
			chat.start.ProviderConversationID, id)
	}
}

func TestInterfaceTransitionChatToTUIStartsFreshWhenReservedIDHasNoHistory(t *testing.T) {
	manager, store, runtime, _, log := newTransitionManager(t, domain.SessionModeChat)
	manager.agents = singleAgent{agent: emptyTransitionAgent{}}

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeTUI, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if settled.NativeConversationID != "" {
		t.Fatalf("native conversation = %q, want fresh sentinel", settled.NativeConversationID)
	}
	if runtime.created != 1 {
		t.Fatalf("terminal runtime created %d times, want 1", runtime.created)
	}
	if got := runtime.lastCfg.Argv; len(got) != 1 || got[0] != "launch" {
		t.Fatalf("terminal argv = %#v, want fresh launch", got)
	}
	if got := fmt.Sprint(*log); got != "[prepare:chat:drain stop:chat start:tui]" {
		t.Fatalf("controller order = %s", got)
	}
}

func TestInterfaceTransitionChatToTUIInterruptsThenStopsBeforeStarting(t *testing.T) {
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeChat)
	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1", domain.SessionModeTUI, domain.SessionInterfaceTransitionInterrupt)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if chat.preparedPolicy != domain.SessionInterfaceTransitionInterrupt {
		t.Fatalf("prepared policy = %s", chat.preparedPolicy)
	}
	rec := store.sessions["session-1"]
	if rec.Mode != domain.SessionModeTUI {
		t.Fatalf("mode = %s, want tui", rec.Mode)
	}
	if rec.Metadata.AgentSessionID != "native-1" {
		t.Fatalf("agent session = %q, want native-1", rec.Metadata.AgentSessionID)
	}
	if runtime.created != 1 {
		t.Fatalf("terminal runtime created %d times, want 1", runtime.created)
	}
	if got := fmt.Sprint(*log); got != "[prepare:chat:interrupt stop:chat start:tui]" {
		t.Fatalf("controller order = %s", got)
	}
}

func TestInterfaceTransitionChatToTUIArmsInterruptBeforeReturning(t *testing.T) {
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeChat)
	transition, err := manager.StartInterfaceTransition(
		context.Background(), "session-1", domain.SessionModeTUI,
		domain.SessionInterfaceTransitionInterrupt,
	)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case policy := <-chat.armed:
		if policy != domain.SessionInterfaceTransitionInterrupt {
			t.Fatalf("armed policy = %s, want interrupt", policy)
		}
	default:
		t.Fatal("transition returned before Chat dispatch was fenced; a queued turn can still reach the provider")
	}

	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
}

func TestInterfaceTransitionChatToTUIFailsBeforeBackgroundWorkWhenArmFails(t *testing.T) {
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeChat)
	chat.armErr = errors.New("arm Chat dispatch gate: controller unavailable")

	_, err := manager.StartInterfaceTransition(
		context.Background(), "session-1", domain.SessionModeTUI,
		domain.SessionInterfaceTransitionInterrupt,
	)
	if err == nil || !strings.Contains(err.Error(), "controller unavailable") {
		t.Fatalf("start transition error = %v, want source-fence failure", err)
	}
	latest, found, getErr := store.GetLatestSessionInterfaceTransition(context.Background(), "session-1")
	if getErr != nil || !found {
		t.Fatalf("latest failed transition: found=%v err=%v", found, getErr)
	}
	if latest.Phase != domain.SessionInterfaceTransitionFailed || latest.ErrorCode != "SOURCE_FENCE_FAILED" {
		t.Fatalf("transition = %+v, want failed SOURCE_FENCE_FAILED", latest)
	}
	if runtime.created != 0 || runtime.destroyed != 0 || len(*log) != 0 {
		t.Fatalf("source-fence failure started background controller work: runtime=%d/%d log=%v",
			runtime.created, runtime.destroyed, *log)
	}
}

func TestInterfaceTransitionChatToTUIPreflightFailureReopensArmedSource(t *testing.T) {
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeChat)
	manager.agents = singleAgent{agent: failingRestoreTransitionAgent{
		transitionAgent: transitionAgent{},
		err:             errors.New("native terminal resume unavailable"),
	}}

	transition, err := manager.StartInterfaceTransition(
		context.Background(), "session-1", domain.SessionModeTUI,
		domain.SessionInterfaceTransitionInterrupt,
	)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "TARGET_PREFLIGHT_FAILED" {
		t.Fatalf("transition = %+v, want failed target preflight", settled)
	}
	select {
	case <-chat.aborted:
	default:
		t.Fatal("target preflight failed without reopening the synchronously armed Chat source")
	}
	if runtime.created != 0 || runtime.destroyed != 0 || len(*log) != 0 {
		t.Fatalf("preflight failure mutated controllers: runtime=%d/%d log=%v",
			runtime.created, runtime.destroyed, *log)
	}
}

func TestSendQueuesDuringInterfaceTransition(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	now := time.Now()
	store.transitions["transition-1"] = domain.SessionInterfaceTransition{
		ID: "transition-1", SessionID: "session-1", SourceMode: domain.SessionModeTUI,
		TargetMode: domain.SessionModeChat, Policy: domain.SessionInterfaceTransitionDrain,
		Phase: domain.SessionInterfaceTransitionDraining, CreatedAt: now, UpdatedAt: now,
	}
	if err := manager.Send(context.Background(), "session-1", "CI failed on linux", nil); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListPendingSessionInterfaceTransitionMessages(context.Background(), "transition-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Message != "CI failed on linux" {
		t.Fatalf("queued messages = %+v", messages)
	}
	if messages[0].ClientMessageID == "" {
		t.Fatal("queued message has no durable idempotency key")
	}
}

func TestTransitionMessagesReturnToSourceAfterPreflightFailure(t *testing.T) {
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	chat.preflightErr = ports.ErrChatDriverUnavailable
	chat.preflightStarted = make(chan struct{}, 1)
	chat.preflightRelease = make(chan struct{})

	transition, err := manager.StartInterfaceTransition(
		context.Background(), "session-1", domain.SessionModeChat,
		domain.SessionInterfaceTransitionDrain,
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-chat.preflightStarted:
	case <-time.After(time.Second):
		t.Fatal("preflight did not start")
	}
	if err := manager.Send(context.Background(), "session-1", "CI failed on linux", nil); err != nil {
		t.Fatal(err)
	}
	close(chat.preflightRelease)
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed {
		t.Fatalf("phase = %q, want failed", settled.Phase)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pending, listErr := store.ListPendingSessionInterfaceTransitionMessages(context.Background(), transition.ID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(pending) == 0 && len(store.messenger.msgs) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queued message was not returned to source: pending=%+v delivered=%+v",
		store.messages[transition.ID], store.messenger.msgs)
}

func TestTransitionMessageRetryUsesStableChatIdempotencyKey(t *testing.T) {
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeChat)
	now := time.Now()
	transition := domain.SessionInterfaceTransition{
		ID: "transition-completed", SessionID: "session-1",
		SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Policy:    domain.SessionInterfaceTransitionDrain,
		Phase:     domain.SessionInterfaceTransitionCompleted,
		CreatedAt: now, UpdatedAt: now, CompletedAt: now,
	}
	store.transitions[transition.ID] = transition
	if err := store.EnqueueSessionInterfaceTransitionMessage(
		context.Background(), transition.ID, "handoff-message-1", "review is ready", now,
	); err != nil {
		t.Fatal(err)
	}
	store.markMessageErr = errors.New("temporary acknowledgement failure")
	if err := manager.deliverAllTransitionMessages(context.Background()); err == nil {
		t.Fatal("first delivery unexpectedly succeeded")
	}
	store.markMessageErr = nil
	if err := manager.deliverAllTransitionMessages(context.Background()); err != nil {
		t.Fatalf("retry delivery: %v", err)
	}
	if fmt.Sprint(chat.relayIDs) != "[handoff-message-1 handoff-message-1]" {
		t.Fatalf("relay ids = %v", chat.relayIDs)
	}
	pending, err := store.ListPendingSessionInterfaceTransitionMessages(context.Background(), transition.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after retry = %+v err=%v", pending, err)
	}
}

func TestTransitionDeliveryWaitsForFirstTUISignal(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	rec := store.sessions["session-1"]
	rec.FirstSignalAt = time.Time{}
	store.sessions["session-1"] = rec

	// Longer than the normal idle-settle window: without the first-signal check,
	// this would incorrectly return ready.
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	_, err := manager.waitForTransitionDeliveryReady(ctx, "session-1", time.Time{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness error = %v, want deadline while target has not signalled", err)
	}

	rec.FirstSignalAt = time.Now()
	store.sessions["session-1"] = rec
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readyCancel()
	if _, err := manager.waitForTransitionDeliveryReady(readyCtx, "session-1", time.Time{}); err != nil {
		t.Fatalf("readiness after first signal: %v", err)
	}
}

func TestInterfaceTransitionRequiresExplicitAdapterCapability(t *testing.T) {
	manager, store, runtime, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	manager.agents = singleAgent{agent: fakeAgent{}}
	_, err := manager.StartInterfaceTransition(context.Background(), "session-1", domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if !errors.Is(err, ErrInterfaceHandoffUnsupported) {
		t.Fatalf("error = %v, want ErrInterfaceHandoffUnsupported", err)
	}
	if len(store.transitions) != 0 || runtime.destroyed != 0 || chat.start.ProviderConversationID != "" {
		t.Fatal("unsupported handoff mutated session or controllers")
	}
}

func TestInterfaceTransitionRejectsAlreadySelectedModeWithoutMutation(t *testing.T) {
	manager, store, runtime, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	_, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeTUI, domain.SessionInterfaceTransitionDrain)
	if !errors.Is(err, ErrInterfaceAlreadySelected) {
		t.Fatalf("error = %v, want ErrInterfaceAlreadySelected", err)
	}
	if len(store.transitions) != 0 || runtime.destroyed != 0 || chat.start.ProviderConversationID != "" {
		t.Fatal("already-selected request mutated session or controllers")
	}
}

func TestCancelInterfaceTransitionBeforeSourceStop(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	now := time.Now()
	store.transitions["transition-1"] = domain.SessionInterfaceTransition{
		ID: "transition-1", SessionID: "session-1",
		SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Policy: domain.SessionInterfaceTransitionDrain,
		Phase:  domain.SessionInterfaceTransitionDraining, CreatedAt: now, UpdatedAt: now,
	}
	if err := manager.CancelInterfaceTransition(context.Background(), "session-1"); err != nil {
		t.Fatalf("cancel transition: %v", err)
	}
	transition, _, _ := store.GetSessionInterfaceTransition(context.Background(), "transition-1")
	if transition.Phase != domain.SessionInterfaceTransitionCancelled || transition.Active() {
		t.Fatalf("cancelled transition = %+v", transition)
	}
	if got := store.sessions["session-1"].Mode; got != domain.SessionModeTUI {
		t.Fatalf("cancel changed mode to %q", got)
	}
}

func TestCancelInterfaceTransitionAfterSourceStoppingIsRefused(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	now := time.Now()
	store.transitions["transition-1"] = domain.SessionInterfaceTransition{
		ID: "transition-1", SessionID: "session-1",
		SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Policy: domain.SessionInterfaceTransitionDrain,
		Phase:  domain.SessionInterfaceTransitionSourceStopping, CreatedAt: now, UpdatedAt: now,
	}
	if err := manager.CancelInterfaceTransition(context.Background(), "session-1"); !errors.Is(err, ErrInterfaceTransitionNotCancellable) {
		t.Fatalf("cancel error = %v, want ErrInterfaceTransitionNotCancellable", err)
	}
	transition, _, _ := store.GetSessionInterfaceTransition(context.Background(), "transition-1")
	if transition.Phase != domain.SessionInterfaceTransitionSourceStopping {
		t.Fatalf("refused cancel changed phase to %q", transition.Phase)
	}
}

func TestCancelInterfaceTransitionDoesNotAcknowledgeALostStopBoundaryRace(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	now := time.Now()
	store.transitions["transition-1"] = domain.SessionInterfaceTransition{
		ID: "transition-1", SessionID: "session-1",
		SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Policy: domain.SessionInterfaceTransitionDrain,
		Phase:  domain.SessionInterfaceTransitionDraining, CreatedAt: now, UpdatedAt: now,
	}
	store.loseCancelCAS = true

	err := manager.CancelInterfaceTransition(context.Background(), "session-1")
	if !errors.Is(err, ErrInterfaceTransitionNotCancellable) {
		t.Fatalf("cancel error = %v, want ErrInterfaceTransitionNotCancellable", err)
	}
	transition, _, _ := store.GetSessionInterfaceTransition(context.Background(), "transition-1")
	if transition.Phase != domain.SessionInterfaceTransitionSourceStopping {
		t.Fatalf("phase = %q, want source_stopping", transition.Phase)
	}
}

func TestInterfaceTransitionRetriesAnAmbiguousSourceStopBeforeStartingTarget(t *testing.T) {
	manager, store, runtime, _, log := newTransitionManager(t, domain.SessionModeTUI)
	runtime.stopErrors = []error{errors.New("tmux command timed out"), nil}
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, error = %s", settled.Phase, settled.ErrorDetail)
	}
	if got := fmt.Sprint(*log); got != "[stop:tui:runtime-1 stop:tui:runtime-1 start:chat]" {
		t.Fatalf("controller order = %s", got)
	}
}

func TestInterfaceTransitionDoesNotStartTargetWhenSourceStopRemainsAmbiguous(t *testing.T) {
	manager, store, runtime, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	runtime.stopErrors = []error{errors.New("first stop failed"), errors.New("retry failed")}
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionRecovery || settled.ErrorCode != "SOURCE_STOP_UNCERTAIN" {
		t.Fatalf("transition = %+v", settled)
	}
	if got := store.sessions["session-1"].Mode; got != domain.SessionModeTUI {
		t.Fatalf("mode = %s, want source TUI", got)
	}
	if chat.start.ProviderConversationID != "" {
		t.Fatalf("target Chat controller started with %q", chat.start.ProviderConversationID)
	}
}
