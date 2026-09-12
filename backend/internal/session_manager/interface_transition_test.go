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

func (s *transitionStore) AcknowledgeSessionInterfaceTransitionNotice(
	_ context.Context,
	sessionID domain.SessionID,
	transitionID string,
	now time.Time,
) (domain.SessionInterfaceTransition, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.transitions[transitionID]
	if !ok || rec.SessionID != sessionID ||
		(rec.Phase != domain.SessionInterfaceTransitionFailed && rec.Phase != domain.SessionInterfaceTransitionRecovery) {
		return domain.SessionInterfaceTransition{}, false, nil
	}
	if rec.NoticeAcknowledgedAt.IsZero() {
		rec.NoticeAcknowledgedAt = now
		s.transitions[transitionID] = rec
	}
	return rec, true, nil
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

type transitionSurfaceAgent struct{ transitionAgent }

func (transitionSurfaceAgent) InspectTerminalSurface(output string) ports.TerminalSurfaceObservation {
	switch output {
	case idleTerminalOutput:
		return ports.TerminalSurfaceObservation{
			Work: ports.TerminalSurfaceWorkIdle, Composer: ports.TerminalComposerEmpty,
		}
	case draftTerminalOutput:
		return ports.TerminalSurfaceObservation{
			Work: ports.TerminalSurfaceWorkIdle, Composer: ports.TerminalComposerDraft,
		}
	case activeDraftTerminalOutput:
		return ports.TerminalSurfaceObservation{
			Work: ports.TerminalSurfaceWorkActive, Composer: ports.TerminalComposerDraft,
		}
	case decisionTerminalOutput:
		return ports.TerminalSurfaceObservation{
			Work: ports.TerminalSurfaceWorkBlocked, Composer: ports.TerminalComposerDraft,
		}
	case activeTerminalOutput:
		return ports.TerminalSurfaceObservation{Work: ports.TerminalSurfaceWorkActive}
	default:
		return ports.TerminalSurfaceObservation{}
	}
}

type transitionSurfaceDetectorAgent struct{ transitionSurfaceAgent }

func (transitionSurfaceDetectorAgent) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	if output == idleTerminalOutput || output == draftTerminalOutput {
		return domain.ActivityIdle, true
	}
	return "", false
}

func (transitionSurfaceDetectorAgent) ComposerIsEmpty(output string) bool {
	return output == idleTerminalOutput
}

const (
	idleTerminalOutput        = "idle"
	draftTerminalOutput       = "draft"
	activeDraftTerminalOutput = "active-draft"
	decisionTerminalOutput    = "decision"
	activeTerminalOutput      = "active"
	ambiguousTerminalOutput   = "ambiguous"
)

type emptyTransitionAgent struct{ transitionAgent }

func (emptyTransitionAgent) NativeConversationExists(context.Context, ports.SessionRef, string, map[string]string) (bool, error) {
	return false, nil
}

type untouchedEmptyTransitionAgent struct{ emptyTransitionAgent }

func (untouchedEmptyTransitionAgent) InspectTerminalSurface(string) ports.TerminalSurfaceObservation {
	return ports.TerminalSurfaceObservation{
		Work: ports.TerminalSurfaceWorkIdle, Composer: ports.TerminalComposerEmpty,
		NativeConversationNotStarted: true,
	}
}

type transitionRuntime struct {
	*fakeRuntime
	log                        *[]string
	stopErrors                 []error
	runtimeOccupied            bool
	outputForCall              func(int) string
	outputCallTimes            []time.Time
	styledOutputCalls          int
	styledOutputErr            error
	blockAliveUntilContextDone bool
}

// unstyledTransitionRuntime models a legacy runtime that cannot return a
// current rendered screen with SGR styling. Embedding would promote the optional
// styled capability, so delegate the required Runtime methods only.
type unstyledTransitionRuntime struct{ runtime *transitionRuntime }

func (r *unstyledTransitionRuntime) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	return r.runtime.Create(ctx, cfg)
}

func (r *unstyledTransitionRuntime) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	return r.runtime.Destroy(ctx, handle)
}

func (r *unstyledTransitionRuntime) GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	return r.runtime.GetOutput(ctx, handle, lines)
}

func (r *unstyledTransitionRuntime) IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	return r.runtime.IsAlive(ctx, handle)
}

func (r *unstyledTransitionRuntime) ProbeFencedRuntime(ctx context.Context, ref ports.FencedRuntimeRef) ports.FencedProbeResult {
	return r.runtime.ProbeFencedRuntime(ctx, ref)
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

func (r *transitionRuntime) GetStyledOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	r.outputCallTimes = append(r.outputCallTimes, time.Now())
	r.styledOutputCalls++
	if r.styledOutputErr != nil {
		return "", r.styledOutputErr
	}
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
func (c *transitionChat) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	var err error
	cfg, err = prepareTestChatStart(ctx, cfg)
	if err != nil {
		return ChatStarted{}, err
	}
	c.start = cfg
	*c.log = append(*c.log, "start:chat")
	if c.startErr != nil {
		return ChatStarted{}, c.startErr
	}
	started := ChatStarted{ProviderConversationID: cfg.ProviderConversationID, ControllerGeneration: "chat-generation"}
	if cfg.ControllerReady != nil {
		if _, err := cfg.ControllerReady(started); err != nil {
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
		metadata.AgentSessionIDLaunchID = "old-tui-generation"
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

func TestInterfaceTransitionRejectsNativeIdentityNotConfirmedByCurrentTUILaunch(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	rec := store.sessions["session-1"]
	// MarkSpawned clears this receipt for every new runtime generation. Until a
	// hook from that generation lands, AgentSessionID can only be an old resume
	// hint; it is not proof that the visible TUI is still on that conversation.
	rec.FirstSignalAt = time.Time{}
	rec.Metadata.AgentSessionIDLaunchID = "previous-tui-generation"
	store.sessions["session-1"] = rec

	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("InterfaceTransitionStatus: %v", err)
	}
	if status.Supported {
		t.Fatal("stale native identity unexpectedly enabled interface switching")
	}
	if status.ReasonCode != "NATIVE_SESSION_UNVERIFIED" {
		t.Fatalf("reasonCode = %q, want NATIVE_SESSION_UNVERIFIED", status.ReasonCode)
	}
	if _, err := manager.StartInterfaceTransition(
		context.Background(), "session-1", domain.SessionModeChat, domain.SessionInterfaceTransitionDrain,
	); err == nil {
		t.Fatal("interface transition started without current-launch native identity proof")
	}
}

func TestInterfaceTransitionChatIdentityDoesNotDependOnTUIHookReceipt(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeChat)
	rec := store.sessions["session-1"]
	rec.FirstSignalAt = time.Time{}
	store.sessions["session-1"] = rec

	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("InterfaceTransitionStatus: %v", err)
	}
	if !status.Supported {
		t.Fatalf("Chat controller identity should remain supported: %s (%s)", status.Reason, status.ReasonCode)
	}
}

func TestInterfaceTransitionStatusBlocksFreshStartWithoutPositiveTerminalProof(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	manager.agents = singleAgent{agent: emptyTransitionAgent{}}
	rec := store.sessions["session-1"]
	rec.Metadata.AgentSessionID = ""
	rec.Metadata.AgentSessionIDLaunchID = ""
	store.sessions["session-1"] = rec

	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("InterfaceTransitionStatus: %v", err)
	}
	if status.Supported {
		t.Fatal("history probing alone enabled a fresh handoff without positive current-screen proof")
	}
	if status.ReasonCode != "NATIVE_SESSION_MISSING" {
		t.Fatalf("reasonCode = %q, want NATIVE_SESSION_MISSING", status.ReasonCode)
	}
}

func TestInterfaceTransitionStatusBlocksFreshStartWhenConversationMetadataExists(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	manager.agents = singleAgent{agent: codexagent.New()}
	runtime.outputForCall = func(int) string {
		return "╭────────────────────────╮\n" +
			"│ >_ OpenAI Codex (v0.147.0) │\n" +
			"╰────────────────────────╯\n\n" +
			"Tip: Try the Desktop app.\n\n" +
			"\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m\n\n" +
			"gpt-5.6-sol low · /ws/session-1\n"
	}
	rec := store.sessions["session-1"]
	rec.Harness = domain.HarnessCodex
	rec.Metadata.AgentSessionID = ""
	rec.Metadata.AgentSessionIDLaunchID = ""
	rec.Metadata.LatestUserPrompt = "implement the feature"
	store.sessions["session-1"] = rec

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
	manager.browserCapabilities = &scriptedBrowserCapabilities{issues: []browserCapabilityIssue{{
		token: "transition-chat-token", verifier: "transition-chat-verifier",
	}}}
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
	if !chat.start.RequireNativeHistory {
		t.Fatal("TUI to Chat handoff did not require native history replay")
	}
	if got := chat.start.Env[EnvBrowserCapability]; got != "transition-chat-token" {
		t.Fatalf("Chat transition capability = %q, want transition-chat-token", got)
	}
	if got := rec.Metadata.BrowserCapabilityVerifier; got != "transition-chat-verifier" {
		t.Fatalf("Chat transition verifier = %q, want transition-chat-verifier", got)
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

func TestInterfaceTransitionReportsNativeHistoryReplayFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "unavailable", err: ports.ErrChatHistoryUnavailable, code: "TARGET_HISTORY_UNAVAILABLE"},
		{name: "unsettled", err: ports.ErrChatHistoryUnsettled, code: "TARGET_HISTORY_UNSETTLED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, store, runtime, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
			runtime.aliveByHandle = map[string]bool{"runtime-1": true}
			chat.startErr = tt.err

			transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
				domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
			if err != nil {
				t.Fatal(err)
			}
			settled := awaitTransition(t, store, transition.ID)
			if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != tt.code {
				t.Fatalf("transition = %+v, want failed %s", settled, tt.code)
			}
			if got := store.sessions["session-1"].Mode; got != domain.SessionModeTUI {
				t.Fatalf("mode = %s, want restored TUI", got)
			}
		})
	}
}

func TestInterfaceTransitionTUIToChatDrainsAVisibleIdleComposerAfterNonSubmittingInput(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionSurfaceAgent{}}
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
	if runtime.outputCalls < interfaceTransitionSurfaceIdleSamples {
		t.Fatalf("terminal output calls = %d, want at least %d consecutive idle samples",
			runtime.outputCalls, interfaceTransitionSurfaceIdleSamples)
	}
	if firstCapture := runtime.outputCallTimes[0]; firstCapture.Before(now.Add(manager.interfaceTransition.idleSettle)) {
		t.Fatalf("first terminal capture at %s, before input settled at %s",
			firstCapture, now.Add(manager.interfaceTransition.idleSettle))
	}
}

func TestInterfaceTransitionTUIToChatPreservesAVisibleDraftEvenAfterFreshIdle(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionSurfaceAgent{}}
	now := time.Now()
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	runtime.outputs = []string{draftTerminalOutput}
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
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "DRAIN_DRAFT_PRESENT" {
		t.Fatalf("transition = %+v, want failed DRAIN_DRAFT_PRESENT", settled)
	}
	if !strings.Contains(settled.ErrorDetail, "unsent text") || !strings.Contains(settled.ErrorDetail, "left untouched") {
		t.Fatalf("error detail = %q, want preserved-draft guidance", settled.ErrorDetail)
	}
	if runtime.destroyed != 0 {
		t.Fatalf("source runtime destroyed %d times with a visible draft", runtime.destroyed)
	}
	if runtime.outputCalls == 0 {
		t.Fatal("terminal surface was not inspected despite a fresh idle timestamp")
	}
	select {
	case <-gate.released:
	case <-time.After(time.Second):
		t.Fatal("terminal input gate remained closed after draft detection")
	}
}

func TestInterfaceTransitionTUIToChatPreservesDraftWhenSurfaceAlsoLooksActive(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionSurfaceAgent{}}
	now := time.Now()
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	runtime.outputs = []string{activeDraftTerminalOutput}
	gate := &transitionInputGate{
		acquired: make(chan string, 1),
		released: make(chan string, 1),
	}
	manager.SetTerminalInputGate(gate)

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "DRAIN_DRAFT_PRESENT" {
		t.Fatalf("transition = %+v, want failed DRAIN_DRAFT_PRESENT", settled)
	}
	if runtime.destroyed != 0 {
		t.Fatalf("source runtime destroyed %d times with a visible draft", runtime.destroyed)
	}
	select {
	case <-gate.released:
	case <-time.After(time.Second):
		t.Fatal("terminal input gate remained closed after draft detection")
	}
}

func TestInterfaceTransitionTUIToChatReportsAPendingDecisionBeforeComposerDraft(t *testing.T) {
	manager, store, runtime, _, log := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionSurfaceAgent{}}
	now := time.Now()
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	runtime.outputs = []string{decisionTerminalOutput}
	gate := &transitionInputGate{
		acquired: make(chan string, 2),
		released: make(chan string, 2),
	}
	manager.SetTerminalInputGate(gate)

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "DRAIN_DECISION_PENDING" {
		t.Fatalf("transition = %+v, want failed DRAIN_DECISION_PENDING", settled)
	}
	if !strings.Contains(settled.ErrorDetail, "decision") || !strings.Contains(settled.ErrorDetail, "Terminal") {
		t.Fatalf("error detail = %q, want guidance to answer the decision in Terminal", settled.ErrorDetail)
	}
	if strings.Contains(settled.ErrorDetail, "unsent text") {
		t.Fatalf("error detail = %q, pending decision was misreported as a draft", settled.ErrorDetail)
	}
	if runtime.destroyed != 0 {
		t.Fatalf("source runtime destroyed %d times with a pending decision", runtime.destroyed)
	}
	select {
	case <-gate.released:
	case <-time.After(time.Second):
		t.Fatal("terminal input gate remained closed after decision detection")
	}

	recovery, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionInterrupt)
	if err != nil {
		t.Fatalf("start decision cancellation: %v", err)
	}
	completed := awaitTransition(t, store, recovery.ID)
	if completed.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("decision cancellation = %+v, want completed", completed)
	}
	if !strings.Contains(fmt.Sprint(*log), "interrupt:tui:runtime-1") {
		t.Fatalf("controller log = %v, explicit decision cancellation did not interrupt the provider", *log)
	}
}

func TestInterfaceTransitionTUIToChatCanInterruptAfterDraftDrainFailure(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionSurfaceAgent{}}
	now := time.Now()
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	runtime.outputs = []string{draftTerminalOutput}
	manager.SetTerminalInputGate(&transitionInputGate{
		acquired: make(chan string, 2), released: make(chan string, 2), lastInputAt: now,
	})

	drain, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	failed := awaitTransition(t, store, drain.ID)
	if failed.Phase != domain.SessionInterfaceTransitionFailed || failed.ErrorCode != "DRAIN_DRAFT_PRESENT" {
		t.Fatalf("drain transition = %+v, want failed DRAIN_DRAFT_PRESENT", failed)
	}
	if runtime.destroyed != 0 {
		t.Fatalf("source runtime destroyed %d times before explicit discard", runtime.destroyed)
	}

	recovery, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionInterrupt)
	if err != nil {
		t.Fatalf("start interrupt recovery: %v", err)
	}
	settled := awaitTransition(t, store, recovery.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("interrupt recovery = %+v, want completed", settled)
	}
	if settled.Policy != domain.SessionInterfaceTransitionInterrupt {
		t.Fatalf("recovery policy = %q, want interrupt", settled.Policy)
	}
	if runtime.destroyed != 1 {
		t.Fatalf("source runtime destroyed %d times after explicit discard, want 1", runtime.destroyed)
	}
	if got := store.sessions["session-1"].Mode; got != domain.SessionModeChat {
		t.Fatalf("session mode = %q, want chat", got)
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
		{name: "surface inspector ambiguous", agent: transitionSurfaceAgent{}, outputs: []string{ambiguousTerminalOutput}, wantCaptures: true},
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

func TestInterfaceTransitionTUIToChatFallsBackWhenStyledOutputIsUnavailable(t *testing.T) {
	tests := []struct {
		name         string
		agent        ports.Agent
		activityAt   func(time.Time) time.Time
		wantCaptures bool
	}{
		{
			name:       "causally fresh durable idle",
			agent:      transitionSurfaceAgent{},
			activityAt: func(inputAt time.Time) time.Time { return inputAt },
		},
		{
			name:         "stale idle verified by legacy detector",
			agent:        transitionSurfaceDetectorAgent{},
			activityAt:   func(inputAt time.Time) time.Time { return inputAt.Add(-time.Minute) },
			wantCaptures: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
			useFastInterfaceTransitionTimings(manager)
			manager.agents = singleAgent{agent: tt.agent}
			manager.runtime = &unstyledTransitionRuntime{runtime: runtime}
			runtime.aliveByHandle = map[string]bool{"runtime-1": true}
			runtime.outputs = []string{idleTerminalOutput}
			inputAt := time.Now()
			rec := store.sessions["session-1"]
			rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: tt.activityAt(inputAt)}
			store.sessions["session-1"] = rec
			manager.SetTerminalInputGate(&transitionInputGate{
				acquired: make(chan string, 1), released: make(chan string, 1), lastInputAt: inputAt,
			})

			transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
				domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
			if err != nil {
				t.Fatal(err)
			}
			settled := awaitTransition(t, store, transition.ID)
			if settled.Phase != domain.SessionInterfaceTransitionCompleted {
				t.Fatalf("transition = %+v, want completed fallback drain", settled)
			}
			if tt.wantCaptures && runtime.outputCalls == 0 {
				t.Fatal("legacy terminal detector was not consulted")
			}
			if !tt.wantCaptures && runtime.outputCalls != 0 {
				t.Fatalf("plain terminal captures = %d, want fresh durable idle to avoid capture", runtime.outputCalls)
			}
		})
	}
}

func TestInterfaceTransitionTUIToChatFallsBackForARecoveredHostWithoutStyledOutput(t *testing.T) {
	tests := []struct {
		name         string
		agent        ports.Agent
		activityAt   func(time.Time) time.Time
		wantCaptures bool
	}{
		{
			name:       "causally fresh durable idle",
			agent:      transitionSurfaceAgent{},
			activityAt: func(inputAt time.Time) time.Time { return inputAt },
		},
		{
			name:         "stale idle verified by legacy detector",
			agent:        transitionSurfaceDetectorAgent{},
			activityAt:   func(inputAt time.Time) time.Time { return inputAt.Add(-time.Minute) },
			wantCaptures: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
			useFastInterfaceTransitionTimings(manager)
			manager.agents = singleAgent{agent: tt.agent}
			runtime.aliveByHandle = map[string]bool{"runtime-1": true}
			runtime.outputs = []string{idleTerminalOutput}
			runtime.styledOutputErr = ports.ErrStyledTerminalOutputUnavailable
			inputAt := time.Now()
			rec := store.sessions["session-1"]
			rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: tt.activityAt(inputAt)}
			store.sessions["session-1"] = rec
			manager.SetTerminalInputGate(&transitionInputGate{
				acquired: make(chan string, 1), released: make(chan string, 1), lastInputAt: inputAt,
			})

			transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
				domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
			if err != nil {
				t.Fatal(err)
			}
			settled := awaitTransition(t, store, transition.ID)
			if settled.Phase != domain.SessionInterfaceTransitionCompleted {
				t.Fatalf("transition = %+v, want completed per-handle fallback drain", settled)
			}
			if runtime.styledOutputCalls != 1 {
				t.Fatalf("styled output probes = %d, want one capability probe", runtime.styledOutputCalls)
			}
			if tt.wantCaptures && runtime.outputCalls == 0 {
				t.Fatal("legacy terminal detector was not consulted")
			}
			if !tt.wantCaptures && runtime.outputCalls != 0 {
				t.Fatalf("plain terminal captures = %d, want fresh durable idle to avoid capture", runtime.outputCalls)
			}
		})
	}
}

func TestInterfaceTransitionTUIToChatUnstyledFallbackDoesNotApproveAnIdleDraft(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionSurfaceDetectorAgent{}}
	manager.runtime = &unstyledTransitionRuntime{runtime: runtime}
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	runtime.outputs = []string{draftTerminalOutput}
	inputAt := time.Now()
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: inputAt.Add(-time.Minute)}
	store.sessions["session-1"] = rec
	manager.SetTerminalInputGate(&transitionInputGate{
		acquired: make(chan string, 1), released: make(chan string, 1), lastInputAt: inputAt,
	})

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "DRAIN_QUIESCENCE_UNVERIFIED" {
		t.Fatalf("transition = %+v, want failed DRAIN_QUIESCENCE_UNVERIFIED", settled)
	}
	if runtime.destroyed != 0 {
		t.Fatalf("source runtime destroyed %d times with an unverified draft", runtime.destroyed)
	}
}

func TestInterfaceTransitionTUIToChatTreatsCurrentSurfaceActivityAsBusy(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionSurfaceAgent{}}
	now := time.Now()
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	runtime.outputs = []string{activeTerminalOutput}

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * manager.interfaceTransition.staleIdleLimit)
	current, _, err := store.GetSessionInterfaceTransition(context.Background(), transition.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Phase != domain.SessionInterfaceTransitionDraining {
		t.Fatalf("phase = %s, want draining while current terminal surface is active", current.Phase)
	}
	if err := manager.CancelInterfaceTransition(context.Background(), "session-1"); err != nil {
		t.Fatal(err)
	}
	if runtime.destroyed != 0 {
		t.Fatalf("source runtime destroyed %d times while surface was active", runtime.destroyed)
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

func TestInterfaceTransitionTUIToChatDoesNotTimeOutActiveWork(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: transitionSurfaceAgent{}}
	rec := store.sessions["session-1"]
	rec.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: time.Now().Add(-time.Hour)}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	runtime.outputs = []string{idleTerminalOutput}
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
		t.Fatalf("phase = %s, want draining while source is active", current.Phase)
	}
	if err := manager.CancelInterfaceTransition(context.Background(), "session-1"); err != nil {
		t.Fatal(err)
	}
	if runtime.destroyed != 0 {
		t.Fatalf("source runtime destroyed %d times while cancelling active drain", runtime.destroyed)
	}
	select {
	case <-gate.released:
	case <-time.After(time.Second):
		t.Fatal("terminal input gate remained closed after cancellation")
	}
}

func TestInterfaceTransitionTUIToChatReportsDurablePendingDecisionWithoutSurfaceProof(t *testing.T) {
	for _, state := range []domain.ActivityState{domain.ActivityWaitingInput, domain.ActivityBlocked} {
		t.Run(string(state), func(t *testing.T) {
			manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
			useFastInterfaceTransitionTimings(manager)
			rec := store.sessions["session-1"]
			rec.Activity = domain.Activity{State: state, LastActivityAt: time.Now()}
			store.sessions["session-1"] = rec
			runtime.aliveByHandle = map[string]bool{"runtime-1": true}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := manager.prepareSourceHandoff(
				ctx, rec, domain.SessionInterfaceTransitionDrain, time.Time{},
			)
			if !errors.Is(err, errDrainDecisionPending) {
				t.Fatalf("prepare source with durable %s = %v, want errDrainDecisionPending", state, err)
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

func TestInterfaceTransitionTUIToChatRejectsReservedIDWhenHistoryIsMissing(t *testing.T) {
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeTUI)
	manager.agents = singleAgent{agent: emptyTransitionAgent{}}

	_, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if !errors.Is(err, ErrNativeConversationMissing) {
		t.Fatalf("StartInterfaceTransition error = %v, want ErrNativeConversationMissing", err)
	}
	if len(store.transitions) != 0 || runtime.destroyed != 0 || chat.start.ProviderConversationID != "" || len(*log) != 0 {
		t.Fatalf("missing native history mutated the source: transitions=%d destroyed=%d chat=%q log=%v",
			len(store.transitions), runtime.destroyed, chat.start.ProviderConversationID, *log)
	}
}

func TestInterfaceTransitionTUIToChatStartsFreshWithPositiveUntouchedSurfaceProof(t *testing.T) {
	manager, store, runtime, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: untouchedEmptyTransitionAgent{}}
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatalf("StartInterfaceTransition: %v", err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("transition = %+v, want completed", settled)
	}
	if settled.NativeConversationID != "" || chat.start.ProviderConversationID != "" {
		t.Fatalf("untouched native session did not start fresh: transition=%q chat=%q",
			settled.NativeConversationID, chat.start.ProviderConversationID)
	}
}

func TestInterfaceTransitionTUIToChatRejectsExistingCodexIDWhenRolloutIsMissing(t *testing.T) {
	manager, store, runtime, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	t.Setenv("CODEX_HOME", t.TempDir())
	manager.agents = singleAgent{agent: codexagent.New()}
	rec := store.sessions["session-1"]
	rec.Harness = domain.HarnessCodex
	rec.Metadata.AgentSessionID = "019fc430-1234-7abc-8def-0123456789ab"
	rec.Metadata.LatestUserPrompt = "keep the completed terminal work"
	store.sessions["session-1"] = rec

	_, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if !errors.Is(err, ErrNativeConversationMissing) {
		t.Fatalf("StartInterfaceTransition error = %v, want ErrNativeConversationMissing", err)
	}
	if runtime.destroyed != 0 || chat.start.ProviderConversationID != "" {
		t.Fatalf("missing Codex rollout destroyed=%d or started Chat=%q",
			runtime.destroyed, chat.start.ProviderConversationID)
	}
}

func TestInterfaceTransitionPromptlessCodexStartsFreshWithoutNativeID(t *testing.T) {
	manager, store, runtime, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	manager.agents = singleAgent{agent: codexagent.New()}
	useFastInterfaceTransitionTimings(manager)
	t.Setenv("CODEX_HOME", t.TempDir())

	initialSurface := "╭────────────────────────╮\n" +
		"│ >_ OpenAI Codex (v0.147.0) │\n" +
		"╰────────────────────────╯\n\n" +
		"Tip: Try the Desktop app.\n\n" +
		"\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m\n\n" +
		"gpt-5.6-sol low · /ws/session-1\n"
	runtime.outputForCall = func(int) string { return initialSurface }
	rec := store.sessions["session-1"]
	rec.Harness = domain.HarnessCodex
	rec.Metadata.AgentSessionID = ""
	rec.Metadata.AgentSessionIDLaunchID = ""
	rec.Metadata.Prompt = ""
	store.sessions["session-1"] = rec

	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported {
		t.Fatalf("promptless initial Codex TUI should be switchable: %+v", status)
	}

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, code = %s, error = %s", settled.Phase, settled.ErrorCode, settled.ErrorDetail)
	}
	if settled.NativeConversationID != "" || chat.start.ProviderConversationID != "" {
		t.Fatalf("promptless Codex handoff did not start fresh: transition=%q target=%q",
			settled.NativeConversationID, chat.start.ProviderConversationID)
	}
}

func TestInterfaceTransitionRefreshesNativeIDAfterPromptlessAdmission(t *testing.T) {
	manager, store, runtime, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	manager.agents = singleAgent{agent: codexagent.New()}
	useFastInterfaceTransitionTimings(manager)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	id := "019fc430-1234-7abc-8def-0123456789ab"
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "08", "14")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(rolloutDir, "rollout-2026-08-14T10-00-00-"+id+".jsonl"),
		[]byte("{\"type\":\"session_meta\"}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	initialSurface := "╭────────────────────────╮\n" +
		"│ >_ OpenAI Codex (v0.147.0) │\n" +
		"╰────────────────────────╯\n\n" +
		"Tip: Try the Desktop app.\n\n" +
		"\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m\n\n" +
		"gpt-5.6-sol low · /ws/session-1\n"
	runtime.outputForCall = func(call int) string {
		if call == 1 {
			// Model the provider hook arriving immediately after the admission
			// snapshot but before the background worker freezes terminal input.
			rec := store.sessions["session-1"]
			rec.Metadata.AgentSessionID = id
			rec.Metadata.AgentSessionIDLaunchID = rec.Metadata.RuntimeLaunchID
			store.sessions["session-1"] = rec
		}
		return initialSurface
	}
	rec := store.sessions["session-1"]
	rec.Harness = domain.HarnessCodex
	rec.Metadata.AgentSessionID = ""
	rec.Metadata.AgentSessionIDLaunchID = ""
	store.sessions["session-1"] = rec

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	if transition.NativeConversationID != "" {
		t.Fatalf("admission native id = %q, want initial fresh sentinel", transition.NativeConversationID)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("phase = %s, code = %s, error = %s", settled.Phase, settled.ErrorCode, settled.ErrorDetail)
	}
	if settled.NativeConversationID != id || chat.start.ProviderConversationID != id {
		t.Fatalf("late native id was not transferred: transition=%q target=%q, want %q",
			settled.NativeConversationID, chat.start.ProviderConversationID, id)
	}
}

func TestInterfaceTransitionPromptlessAdmissionFailsBeforeStoppingWhenTurnStartsWithoutNativeID(t *testing.T) {
	manager, store, runtime, _, log := newTransitionManager(t, domain.SessionModeTUI)
	manager.agents = singleAgent{agent: codexagent.New()}
	useFastInterfaceTransitionTimings(manager)
	t.Setenv("CODEX_HOME", t.TempDir())

	initialSurface := "╭────────────────────────╮\n" +
		"│ >_ OpenAI Codex (v0.147.0) │\n" +
		"╰────────────────────────╯\n\n" +
		"Tip: Try the Desktop app.\n\n" +
		"\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m\n\n" +
		"gpt-5.6-sol low · /ws/session-1\n"
	completedTurnSurface := "╭────────────────────────╮\n" +
		"│ >_ OpenAI Codex (v0.147.0) │\n" +
		"╰────────────────────────╯\n\n" +
		"› Do work\n\n• Done\n\n" +
		"\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m\n\n" +
		"gpt-5.6-sol low · /ws/session-1\n"
	runtime.outputForCall = func(call int) string {
		if call == 1 {
			return initialSurface
		}
		return completedTurnSurface
	}
	rec := store.sessions["session-1"]
	rec.Harness = domain.HarnessCodex
	rec.Metadata.AgentSessionID = ""
	rec.Metadata.AgentSessionIDLaunchID = ""
	store.sessions["session-1"] = rec

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "NATIVE_SESSION_MISSING" {
		t.Fatalf("transition = %+v, want fail-closed missing native identity", settled)
	}
	if got := fmt.Sprint(*log); strings.Contains(got, "stop:tui") || strings.Contains(got, "start:chat") {
		t.Fatalf("source was stopped or incomplete target started: %s", got)
	}
	if got := domain.NormalizeSessionMode(store.sessions["session-1"].Mode); got != domain.SessionModeTUI {
		t.Fatalf("session mode = %s, want TUI source preserved", got)
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

func TestInterfaceTransitionChatToTUIRejectsReservedIDWhenHistoryIsMissing(t *testing.T) {
	manager, store, runtime, chat, log := newTransitionManager(t, domain.SessionModeChat)
	manager.agents = singleAgent{agent: emptyTransitionAgent{}}

	_, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeTUI, domain.SessionInterfaceTransitionDrain)
	if !errors.Is(err, ErrNativeConversationMissing) {
		t.Fatalf("StartInterfaceTransition error = %v, want ErrNativeConversationMissing", err)
	}
	if len(store.transitions) != 0 || runtime.created != 0 || chat.preparedPolicy != "" || len(*log) != 0 {
		t.Fatalf("missing native history mutated the source: transitions=%d created=%d policy=%q log=%v",
			len(store.transitions), runtime.created, chat.preparedPolicy, *log)
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
	if rec.Metadata.AgentSessionIDLaunchID == "" || rec.Metadata.AgentSessionIDLaunchID != rec.Metadata.RuntimeLaunchID {
		t.Fatalf("native identity launch proof = %q, runtime launch = %q; controlled Chat-to-TUI resume must be immediately switchable",
			rec.Metadata.AgentSessionIDLaunchID, rec.Metadata.RuntimeLaunchID)
	}
	status, err := manager.InterfaceTransitionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || status.TargetMode != domain.SessionModeChat {
		t.Fatalf("resumed TUI is not immediately switchable: %+v", status)
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

func TestAcknowledgeInterfaceTransitionNoticePersistsAndIsIdempotent(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	settledAt := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	store.transitions["transition-1"] = domain.SessionInterfaceTransition{
		ID: "transition-1", SessionID: "session-1",
		SourceMode: domain.SessionModeChat, TargetMode: domain.SessionModeTUI,
		Policy: domain.SessionInterfaceTransitionDrain,
		Phase:  domain.SessionInterfaceTransitionRecovery, ErrorCode: "DAEMON_RESTARTED",
		CreatedAt: settledAt.Add(-time.Minute), UpdatedAt: settledAt, CompletedAt: settledAt,
	}

	acknowledged, err := manager.AcknowledgeInterfaceTransitionNotice(
		context.Background(), "session-1", "transition-1",
	)
	if err != nil {
		t.Fatalf("acknowledge notice: %v", err)
	}
	if acknowledged.NoticeAcknowledgedAt.IsZero() {
		t.Fatal("notice acknowledgement was not persisted")
	}
	if !acknowledged.UpdatedAt.Equal(settledAt) {
		t.Fatalf("acknowledgement changed transition settlement time to %v", acknowledged.UpdatedAt)
	}

	again, err := manager.AcknowledgeInterfaceTransitionNotice(
		context.Background(), "session-1", "transition-1",
	)
	if err != nil {
		t.Fatalf("acknowledge notice again: %v", err)
	}
	if !again.NoticeAcknowledgedAt.Equal(acknowledged.NoticeAcknowledgedAt) {
		t.Fatalf("idempotent acknowledgement changed timestamp from %v to %v",
			acknowledged.NoticeAcknowledgedAt, again.NoticeAcknowledgedAt)
	}
}

func TestAcknowledgeInterfaceTransitionNoticeIsScopedToTerminalFailureNotice(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	now := time.Now()
	store.transitions["transition-1"] = domain.SessionInterfaceTransition{
		ID: "transition-1", SessionID: "session-1",
		SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Policy: domain.SessionInterfaceTransitionDrain,
		Phase:  domain.SessionInterfaceTransitionCompleted, CreatedAt: now, UpdatedAt: now,
	}

	if _, err := manager.AcknowledgeInterfaceTransitionNotice(
		context.Background(), "session-1", "transition-1",
	); !errors.Is(err, ErrInterfaceTransitionNoticeNotAcknowledgeable) {
		t.Fatalf("completed acknowledgement error = %v, want ErrInterfaceTransitionNoticeNotAcknowledgeable", err)
	}

	transition := store.transitions["transition-1"]
	transition.Phase = domain.SessionInterfaceTransitionFailed
	store.transitions["transition-1"] = transition
	if _, err := manager.AcknowledgeInterfaceTransitionNotice(
		context.Background(), "different-session", "transition-1",
	); !errors.Is(err, ErrInterfaceTransitionNotFound) {
		t.Fatalf("cross-session acknowledgement error = %v, want ErrInterfaceTransitionNotFound", err)
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

func TestRecoverInterruptedTUIToChatRollsBackCommittedModeBeforeReconcile(t *testing.T) {
	manager, store, _, _, _ := newTransitionManager(t, domain.SessionModeChat)
	now := time.Now()
	store.transitions["transition-1"] = domain.SessionInterfaceTransition{
		ID: "transition-1", SessionID: "session-1",
		SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Policy:               domain.SessionInterfaceTransitionDrain,
		Phase:                domain.SessionInterfaceTransitionTargetStarting,
		NativeConversationID: "native-1",
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	recovered, err := manager.recoverInterruptedInterfaceTransitions(context.Background())
	if err != nil {
		t.Fatalf("recover interrupted transition: %v", err)
	}
	if len(recovered) != 1 || recovered[0].Phase != domain.SessionInterfaceTransitionRecovery {
		t.Fatalf("recovered transitions = %+v", recovered)
	}
	rec := store.sessions["session-1"]
	if rec.Mode != domain.SessionModeTUI {
		t.Fatalf("committed mode = %s, want source TUI until native replay can be required again", rec.Mode)
	}
	if rec.Metadata.AgentSessionID != "native-1" {
		t.Fatalf("source native conversation = %q, want native-1", rec.Metadata.AgentSessionID)
	}
}
