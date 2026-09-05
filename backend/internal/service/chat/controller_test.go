package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// These run against a real SQLite store rather than a mock, because the point is
// that provider events actually land as durable rows in the right order — which a
// mock store cannot demonstrate.

const (
	testProject = domain.ProjectID("p1")
	testSession = domain.SessionID("p1-1")
)

func openStore(t *testing.T) *sqlite.Store {
	t.Helper()
	dir := t.TempDir()
	st := sqlitetest.MustOpenAt(t, dir)

	ctx := context.Background()
	if err := st.UpsertProject(ctx, domain.ProjectRecord{
		ID:           string(testProject),
		Path:         dir,
		RegisteredAt: time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := st.CreateSession(ctx, domain.SessionRecord{
		ID:        testSession,
		ProjectID: testProject,
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessCodex,
		Mode:      domain.SessionModeChat,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return st
}

func fullSnapshotReader(st *sqlite.Store) chatsvc.SnapshotReader {
	return chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
		rows, err := st.LoadConversationSnapshot(ctx, conversationID)
		if err != nil {
			return chatsvc.ConversationRows{}, err
		}
		return chatsvc.ConversationRows{
			Conversation: rows.Conversation, Turns: rows.Turns,
			Messages: rows.Messages, Activities: rows.Activities,
			BranchPoints: rows.BranchPoints, BranchedFromEarlierMessage: rows.BranchedFromEarlierMessage,
		}, nil
	})
}

/* ---- a fake conversation the controller can drive ---------------------- */

type fakeConversation struct {
	events                 chan ports.ChatEvent
	providerConversationID string

	mu                 sync.Mutex
	sent               []ports.ChatUserMessage
	caps               ports.ChatCapabilities
	resolved           map[string]ports.ChatDecision
	turnSeq            int
	sendErr            error
	onSend             func(providerTurnID string)
	onClose            func()
	closeStarted       chan struct{}
	closeEventsRelease <-chan struct{}
	closeSignalOnce    sync.Once
	closeOnce          sync.Once
}

type nativeHistoryConversation struct {
	*fakeConversation
	events []ports.ChatEvent
	err    error
	reads  atomic.Int32
	onRead func(int)
}

type convergingHistoryConversation struct {
	*fakeConversation
	mu             sync.Mutex
	reads          int
	refreshes      int
	initialSettled bool
	initialEvents  []ports.ChatEvent
	events         []ports.ChatEvent
	onRead         func()
}

type blockingHistoryConversation struct {
	*fakeConversation
	started chan struct{}
	release chan struct{}
}

func (c *blockingHistoryConversation) ReadHistory(ctx context.Context) ([]ports.ChatEvent, error) {
	close(c.started)
	select {
	case <-c.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *nativeHistoryConversation) ReadHistory(context.Context) ([]ports.ChatEvent, error) {
	reads := int(c.reads.Add(1))
	if c.onRead != nil {
		c.onRead(reads)
	}
	return c.events, c.err
}

func (c *convergingHistoryConversation) ReadHistory(context.Context) ([]ports.ChatEvent, error) {
	c.mu.Lock()
	c.reads++
	reads := c.reads
	onRead := c.onRead
	initialSettled := c.initialSettled
	initialEvents := append([]ports.ChatEvent(nil), c.initialEvents...)
	events := append([]ports.ChatEvent(nil), c.events...)
	c.mu.Unlock()
	if onRead != nil {
		onRead()
	}
	if reads == 1 {
		if initialSettled {
			return initialEvents, nil
		}
		return nil, ports.ErrChatHistoryUnsettled
	}
	return events, nil
}

func (c *convergingHistoryConversation) RefreshHistory(context.Context) ([]ports.ChatEvent, error) {
	c.mu.Lock()
	c.refreshes++
	events := append([]ports.ChatEvent(nil), c.events...)
	c.mu.Unlock()
	return events, nil
}

func (c *convergingHistoryConversation) historyAttempts() (reads, refreshes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads, c.refreshes
}

func (c *nativeHistoryConversation) historyReads() int {
	return int(c.reads.Load())
}

type deferredConversation struct {
	*fakeConversation
	start func(string) error
}

type stuckConversation struct {
	*fakeConversation
	closeErr error
}

func (s *stuckConversation) Close() error { return s.closeErr }

type terminatingConversation struct {
	*fakeConversation
	terminated atomic.Bool
}

func (c *terminatingConversation) Terminate() error {
	c.terminated.Store(true)
	return c.Close()
}

func (c *terminatingConversation) PreservesProviderOnClose() bool { return true }

type liveReconnectedConversation struct{ *nativeHistoryConversation }

func (c *liveReconnectedConversation) ReconnectedLive() bool { return true }

func (f *deferredConversation) StartDeferredTurn(providerTurnID string) error {
	return f.start(providerTurnID)
}

func (f *deferredConversation) DiscardDeferredTurn(string) {}

func newFakeConversation() *fakeConversation {
	return &fakeConversation{
		events:                 make(chan ports.ChatEvent, 64),
		providerConversationID: "thread-1",
		resolved:               map[string]ports.ChatDecision{},
	}
}

func (f *fakeConversation) ProviderConversationID() string { return f.providerConversationID }
func (f *fakeConversation) Capabilities() ports.ChatCapabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.caps == nil {
		return productionCaps()
	}
	return maps.Clone(f.caps)
}
func (f *fakeConversation) setCapabilities(caps ports.ChatCapabilities) {
	f.mu.Lock()
	f.caps = maps.Clone(caps)
	f.mu.Unlock()
}
func (f *fakeConversation) Events() <-chan ports.ChatEvent { return f.events }

func (f *fakeConversation) SendTurn(_ context.Context, msg ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	f.mu.Lock()
	if f.sendErr != nil {
		f.mu.Unlock()
		return ports.ChatTurnRef{}, f.sendErr
	}
	f.sent = append(f.sent, msg)
	f.turnSeq++
	providerTurnID := fmt.Sprintf("provider-turn-%d", f.turnSeq)
	onSend := f.onSend
	f.mu.Unlock()
	if onSend != nil {
		onSend(providerTurnID)
	}
	return ports.ChatTurnRef{ProviderTurnID: providerTurnID}, nil
}

// sentTexts is what actually reached the provider, in order. Queuing is only real
// if a message the user typed mid-turn is absent from this until the turn ends.
func (f *fakeConversation) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	texts := make([]string, 0, len(f.sent))
	for _, msg := range f.sent {
		texts = append(texts, msg.Text)
	}
	return texts
}

func (f *fakeConversation) sentMessages() []ports.ChatUserMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.ChatUserMessage(nil), f.sent...)
}

func (f *fakeConversation) Interrupt(context.Context, string) error { return nil }

func (f *fakeConversation) ResolveRequest(_ context.Context, id string, d ports.ChatDecision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved[id] = d
	return nil
}

func (f *fakeConversation) Close() error {
	f.mu.Lock()
	onClose := f.onClose
	closeStarted := f.closeStarted
	closeEventsRelease := f.closeEventsRelease
	f.mu.Unlock()
	if onClose != nil {
		onClose()
	}
	if closeStarted != nil {
		f.closeSignalOnce.Do(func() { close(closeStarted) })
	}
	closeEvents := func() {
		f.closeOnce.Do(func() { close(f.events) })
	}
	if closeEventsRelease != nil {
		go func() {
			<-closeEventsRelease
			closeEvents()
		}()
		return nil
	}
	closeEvents()
	return nil
}

func (f *fakeConversation) emit(events ...ports.ChatEvent) {
	for _, event := range events {
		f.events <- event
	}
}

func (f *fakeConversation) decisionFor(id string) (ports.ChatDecision, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.resolved[id]
	return d, ok
}

// fakeDriver hands back whatever conversation double the test supplied, so a
// scenario can replace how the provider ANSWERS without reimplementing how it
// streams.
type fakeDriver struct {
	conv      ports.ChatConversation
	startCfg  *ports.ChatStartConfig
	resumeCfg *ports.ChatResumeConfig
	caps      ports.ChatCapabilities
	probe     func() error
	start     func(ports.ChatStartConfig) (ports.ChatConversation, error)
	resume    func(ports.ChatResumeConfig) (ports.ChatConversation, error)
}

type sequenceDriver struct {
	mu            sync.Mutex
	conversations []ports.ChatConversation
}

func (d *sequenceDriver) Harness() domain.AgentHarness { return domain.HarnessCodex }
func (d *sequenceDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return productionCaps(), nil
}
func (d *sequenceDriver) next() (ports.ChatConversation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conversations) == 0 {
		return nil, errors.New("no conversation queued")
	}
	conversation := d.conversations[0]
	d.conversations = d.conversations[1:]
	return conversation, nil
}
func (d *sequenceDriver) Start(context.Context, ports.ChatStartConfig) (ports.ChatConversation, error) {
	return d.next()
}
func (d *sequenceDriver) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	return d.next()
}

func (d fakeDriver) Harness() domain.AgentHarness { return domain.HarnessCodex }
func (d fakeDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	if d.probe != nil {
		if err := d.probe(); err != nil {
			return nil, err
		}
	}
	if d.caps != nil {
		return d.caps, nil
	}
	return productionCaps(), nil
}
func (d fakeDriver) Start(ctx context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	if cfg.PrepareEnv != nil && !conversationReconnectedLive(d.conv) {
		env, err := cfg.PrepareEnv(ctx)
		if err != nil {
			return nil, err
		}
		cfg.Env = env
	}
	if d.startCfg != nil {
		*d.startCfg = cfg
	}
	if d.start != nil {
		return d.start(cfg)
	}
	return d.conv, nil
}
func (d fakeDriver) Resume(ctx context.Context, cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
	if cfg.PrepareEnv != nil && !conversationReconnectedLive(d.conv) {
		env, err := cfg.PrepareEnv(ctx)
		if err != nil {
			return nil, err
		}
		cfg.Env = env
	}
	if d.resumeCfg != nil {
		*d.resumeCfg = cfg
	}
	if d.resume != nil {
		return d.resume(cfg)
	}
	return d.conv, nil
}

func conversationReconnectedLive(conversation ports.ChatConversation) bool {
	reconnected, ok := conversation.(ports.ChatLiveReconnector)
	return ok && reconnected.ReconnectedLive()
}

type fakeRegistry struct{ driver ports.ChatDriver }

func (r fakeRegistry) Driver(domain.AgentHarness) (ports.ChatDriver, error) { return r.driver, nil }
func (r fakeRegistry) SupportsChat(domain.AgentHarness) bool                { return true }

type recordingActivity struct {
	mu      sync.Mutex
	signals []ports.ActivitySignal
}

func (r *recordingActivity) ApplyActivitySignal(
	_ context.Context,
	_ domain.SessionID,
	signal ports.ActivitySignal,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signals = append(r.signals, signal)
	return nil
}

func (r *recordingActivity) snapshot() []ports.ActivitySignal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ports.ActivitySignal(nil), r.signals...)
}

func productionCaps() ports.ChatCapabilities {
	return ports.ChatCapabilities{
		ports.ChatCapabilityStreaming: true,
		ports.ChatCapabilityApprovals: true,
		ports.ChatCapabilityInterrupt: true,
		ports.ChatCapabilityResume:    true,
	}
}

func TestSuccessfulChatProbeIsReusedByStart(t *testing.T) {
	st := openStore(t)
	probes := 0
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{
			conv: newFakeConversation(),
			probe: func() error {
				probes++
				return nil
			},
		}},
		Log: slog.New(slog.DiscardHandler),
		NewID: func() string {
			nextID++
			return fmt.Sprintf("probe-cache-%d", nextID)
		},
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	if err := svc.PreflightChat(context.Background(), domain.HarnessCodex, ports.PermissionModeDefault); err != nil {
		t.Fatalf("PreflightChat: %v", err)
	}
	if _, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if probes != 1 {
		t.Fatalf("Probe calls = %d, want 1 successful probe reused by Start", probes)
	}
}

func TestFailedChatProbeCanBeRetriedThenCached(t *testing.T) {
	attempts := 0
	driver := fakeDriver{probe: func() error {
		attempts++
		if attempts == 1 {
			return errors.New("provider still installing")
		}
		return nil
	}}
	svc := chatsvc.New(chatsvc.Options{Drivers: fakeRegistry{driver: driver}})

	if err := svc.PreflightChat(context.Background(), domain.HarnessCodex, ports.PermissionModeDefault); err == nil {
		t.Fatal("first PreflightChat must surface the transient probe failure")
	}
	if err := svc.PreflightChat(context.Background(), domain.HarnessCodex, ports.PermissionModeDefault); err != nil {
		t.Fatalf("second PreflightChat: %v", err)
	}
	if err := svc.PreflightChat(context.Background(), domain.HarnessCodex, ports.PermissionModeDefault); err != nil {
		t.Fatalf("cached PreflightChat: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("Probe attempts = %d, want one failed attempt plus one cached success", attempts)
	}
}

func TestCapabilityCacheEvaluatesEveryRequestedPermissionMode(t *testing.T) {
	probes := 0
	driver := fakeDriver{
		caps: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityInterrupt: true,
			ports.ChatCapabilityResume:    true,
		},
		probe: func() error {
			probes++
			return nil
		},
	}
	svc := chatsvc.New(chatsvc.Options{Drivers: fakeRegistry{driver: driver}})

	if err := svc.PreflightChat(context.Background(), domain.HarnessCodex, ports.PermissionModeDefault); !errors.Is(err, ports.ErrChatUnsupported) {
		t.Fatalf("default preflight error = %v, want ErrChatUnsupported", err)
	}
	if err := svc.PreflightChat(context.Background(), domain.HarnessCodex, ports.PermissionModeBypassPermissions); err != nil {
		t.Fatalf("bypass preflight: %v", err)
	}
	if err := svc.PreflightChat(context.Background(), domain.HarnessCodex, ports.PermissionModeDefault); !errors.Is(err, ports.ErrChatUnsupported) {
		t.Fatalf("cached default preflight error = %v, want ErrChatUnsupported", err)
	}
	if probes != 1 {
		t.Fatalf("Probe calls = %d, want one raw capability probe reused across permission modes", probes)
	}
}

func TestResumeUsesPersistedBypassPermissionForCapabilityAdmission(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	conversation, err := st.CreateConversation(
		ctx, "persisted-bypass-conversation", domain.ConversationScopeProject,
		testProject, testSession, now,
	)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := st.SetConversationSettings(ctx, conversation.ID, domain.ConversationSettings{
		Model:           "gpt-5.6-luna",
		ReasoningEffort: "high",
		ApprovalMode:    domain.PermissionModeBypassPermissions,
	}, now); err != nil {
		t.Fatalf("SetConversationSettings: %v", err)
	}

	conv := newFakeConversation()
	conv.providerConversationID = "thread-bypass"
	var resumed ports.ChatResumeConfig
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{
			conv: conv, resumeCfg: &resumed,
			caps: ports.ChatCapabilities{
				ports.ChatCapabilityStreaming: true,
				ports.ChatCapabilityInterrupt: true,
				ports.ChatCapabilityResume:    true,
			},
		}},
		Log:   slog.New(slog.DiscardHandler),
		NewID: func() string { return "persisted-bypass-start" },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	if _, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-bypass",
		Permissions: ports.PermissionModeDefault,
	}); err != nil {
		t.Fatalf("Start resume: %v", err)
	}
	if resumed.Permissions != ports.PermissionModeBypassPermissions {
		t.Fatalf("resume permissions = %q, want persisted bypass", resumed.Permissions)
	}
	if resumed.Model != "gpt-5.6-luna" {
		t.Fatalf("resume model = %q, want persisted model", resumed.Model)
	}
	if resumed.Effort != "high" {
		t.Fatalf("resume effort = %q, want persisted effort", resumed.Effort)
	}
	controller, err := svc.Controller(testSession)
	if err != nil {
		t.Fatalf("Controller: %v", err)
	}
	if settings := controller.Settings(); settings.Model != "gpt-5.6-luna" || settings.ReasoningEffort != "high" {
		t.Fatalf("controller settings = %+v, want persisted model and effort", settings)
	}
}

func TestServicePassesRecomputedSystemPromptToResume(t *testing.T) {
	st := openStore(t)
	conv := newFakeConversation()
	var resumed ports.ChatResumeConfig
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv, resumeCfg: &resumed}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return "conversation-resume" },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	workspace := t.TempDir()
	dataDir := t.TempDir()
	_, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		DataDir: dataDir, WorkspacePath: workspace, ProviderConversationID: "thread-1",
		SystemPrompt: "Recomputed AO orchestrator instructions",
	})
	if err != nil {
		t.Fatalf("Start resume: %v", err)
	}
	if resumed.ProviderConversationID != "thread-1" || resumed.DataDir != dataDir || resumed.WorkspacePath != workspace ||
		resumed.SystemPrompt != "Recomputed AO orchestrator instructions" {
		t.Fatalf("resume config = %#v", resumed)
	}
}

func TestPendingAgentSwitchFreshStartUsesReservedProviderScope(t *testing.T) {
	st := openStore(t)
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record, found, err := st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	record.Metadata.ProviderConversationID = "source-provider-thread"
	if err := st.UpdateSession(context.Background(), record); err != nil {
		t.Fatalf("seed source provider handle: %v", err)
	}
	if _, err := st.CreateConversation(context.Background(), "switch-fresh-conversation",
		domain.ConversationScopeSession, testProject, testSession, now); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	var started ports.ChatStartConfig
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: newFakeConversation(), startCfg: &started}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return "unused-switch-fresh-conversation" },
		Now:     func() time.Time { return now.Add(time.Minute) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	_, err = svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ProviderScopeID: "switch-fresh:provider",
	})
	if err != nil {
		t.Fatalf("Start pending fresh target: %v", err)
	}
	if started.ProviderScopeID != "switch-fresh:provider" {
		t.Fatalf("fresh target provider scope = %q, want reserved switch boundary", started.ProviderScopeID)
	}
}

func TestPendingAgentSwitchResumeUsesReservedProviderScope(t *testing.T) {
	st := openStore(t)
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC)
	record, found, err := st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	record.Metadata.ProviderConversationID = "source-provider-thread"
	if err := st.UpdateSession(context.Background(), record); err != nil {
		t.Fatalf("seed source provider handle: %v", err)
	}
	if _, err := st.CreateConversation(context.Background(), "switch-resume-conversation",
		domain.ConversationScopeSession, testProject, testSession, now); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	resumedConversation := newFakeConversation()
	resumedConversation.providerConversationID = "target-provider-thread"
	var resumed ports.ChatResumeConfig
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: resumedConversation, resumeCfg: &resumed}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return "unused-switch-resume-conversation" },
		Now:     func() time.Time { return now.Add(time.Minute) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	_, err = svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ProviderConversationID:  "target-provider-thread",
		ProviderScopeID:         "switch-resume:provider",
		SkipNativeHistoryImport: true,
	})
	if err != nil {
		t.Fatalf("Start pending resumed target: %v", err)
	}
	if resumed.ProviderConversationID != "target-provider-thread" ||
		resumed.ProviderScopeID != "switch-resume:provider" {
		t.Fatalf("resumed target config = %#v, want target handle in reserved switch scope", resumed)
	}
}

func TestOrdinaryResumeRejectsProviderHandleOutsideActiveBranch(t *testing.T) {
	st := openStore(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	record, found, err := st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	record.Metadata.ProviderConversationID = "active-provider-thread"
	if err := st.UpdateSession(context.Background(), record); err != nil {
		t.Fatalf("seed active provider handle: %v", err)
	}
	if _, err := st.CreateConversation(context.Background(), "ordinary-resume-conversation",
		domain.ConversationScopeSession, testProject, testSession, now); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	resumeCalls := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{
			conv: newFakeConversation(),
			resume: func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
				resumeCalls++
				return newFakeConversation(), nil
			},
		}},
		Log:   slog.New(slog.DiscardHandler),
		NewID: func() string { return "unused-ordinary-resume-conversation" },
	})

	_, err = svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ProviderConversationID:  "unowned-provider-thread",
		SkipNativeHistoryImport: true,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match session handle") {
		t.Fatalf("ordinary resume error = %v, want active-branch handle mismatch", err)
	}
	if resumeCalls != 0 {
		t.Fatalf("driver resume calls = %d, want mismatch rejected before provider launch", resumeCalls)
	}
}

func seedProjectConversationWithProviderHistory(
	t *testing.T,
	st *sqlite.Store,
	conversationID string,
	now time.Time,
) (domain.ConversationRecord, domain.ConversationBranch) {
	t.Helper()
	ctx := context.Background()
	record, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	record.Metadata.ProviderConversationID = "source-provider-thread"
	record.Metadata.ControllerGeneration = "source-generation"
	if err := st.UpdateSession(ctx, record); err != nil {
		t.Fatalf("seed source provider owner: %v", err)
	}
	conversation, err := st.CreateConversation(ctx, conversationID,
		domain.ConversationScopeProject, testProject, testSession, now)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := st.UpsertActivity(ctx, conversation.ID, "", domain.ConversationActivity{
		ID: "source-history", Kind: domain.ActivityKindSystem,
		Status: domain.ActivityStatusCompleted, Summary: "prior provider history",
		ProviderItemID: "source-provider-history",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("seed provider history: %v", err)
	}
	conversation, err = st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	active, err := st.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil {
		t.Fatalf("ConversationBranch: %v", err)
	}
	return conversation, active
}

func TestFreshProjectStartPersistsNewProviderScopeForSubsequentResume(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC)
	before, sourceBranch := seedProjectConversationWithProviderHistory(
		t, st, "fresh-project-conversation", now)
	targetSession, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Create target session: %v", err)
	}

	freshConversation := newFakeConversation()
	freshConversation.providerConversationID = "fresh-provider-thread"
	var started ports.ChatStartConfig
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: freshConversation, startCfg: &started}},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			nextID++
			return fmt.Sprintf("fresh-project-%d", nextID)
		},
		Now: func() time.Time { return now.Add(2 * time.Second) },
	})
	lifecycleManager := lifecycle.New(st, nil)

	controller, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: targetSession.ID, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ControllerReady: func(result chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			if result.ProviderBoundary == nil {
				return chatsvc.ControllerCommit{}, errors.New("fresh provider boundary was not reserved")
			}
			if err := lifecycleManager.MarkChatSpawned(ctx, targetSession.ID, domain.SessionMetadata{
				ProviderConversationID: result.ProviderConversationID,
				ControllerGeneration:   result.ControllerGeneration,
			}, *result.ProviderBoundary); err != nil {
				return chatsvc.ControllerCommit{}, err
			}
			committed := result.Conversation
			committed.ActiveBranchID = result.ProviderBoundary.ID
			committed.UpdatedAt = result.ProviderBoundary.CreatedAt
			return chatsvc.ControllerCommit{Conversation: committed}, nil
		},
	})
	if err != nil {
		t.Fatalf("Start fresh project provider: %v", err)
	}
	if started.ProviderScopeID == "" || started.ProviderScopeID == sourceBranch.ProviderScopeID {
		t.Fatalf("fresh provider scope = %q, want new scope distinct from source %q",
			started.ProviderScopeID, sourceBranch.ProviderScopeID)
	}
	after, err := st.ConversationForSession(ctx, targetSession.ID)
	if err != nil {
		t.Fatalf("ConversationForSession after fresh start: %v", err)
	}
	if after.ActiveBranchID == before.ActiveBranchID || after.ActiveBranchID != started.ProviderScopeID {
		t.Fatalf("active branch = %q, want reserved provider scope %q (source %q)",
			after.ActiveBranchID, started.ProviderScopeID, before.ActiveBranchID)
	}
	freshBranch, err := st.ConversationBranch(ctx, after.ID, after.ActiveBranchID)
	if err != nil {
		t.Fatalf("fresh ConversationBranch: %v", err)
	}
	if freshBranch.ProviderScopeID != started.ProviderScopeID ||
		freshBranch.ProviderConversationID != "fresh-provider-thread" ||
		freshBranch.ParentBranchID != before.ActiveBranchID ||
		freshBranch.SessionID != targetSession.ID {
		t.Fatalf("fresh provider boundary = %+v", freshBranch)
	}
	if controller.ProviderConversationID() != "fresh-provider-thread" {
		t.Fatalf("fresh controller provider handle = %q", controller.ProviderConversationID())
	}
	if err := svc.Stop(ctx, targetSession.ID); err != nil {
		t.Fatalf("Stop fresh controller: %v", err)
	}

	resumedConversation := newFakeConversation()
	resumedConversation.providerConversationID = "fresh-provider-thread"
	var resumed ports.ChatResumeConfig
	restarted := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: resumedConversation, resumeCfg: &resumed}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return "fresh-project-resume-generation" },
		Now:     func() time.Time { return now.Add(3 * time.Second) },
	})
	t.Cleanup(func() { _ = restarted.Stop(context.Background(), targetSession.ID) })
	if _, err := restarted.Start(ctx, chatsvc.StartConfig{
		SessionID: targetSession.ID, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ProviderConversationID:  "fresh-provider-thread",
		SkipNativeHistoryImport: true,
	}); err != nil {
		t.Fatalf("Resume fresh project provider: %v", err)
	}
	if resumed.ProviderScopeID != freshBranch.ProviderScopeID {
		t.Fatalf("resumed provider scope = %q, want persisted %q",
			resumed.ProviderScopeID, freshBranch.ProviderScopeID)
	}
}

func TestFreshProjectProviderStartFailurePreservesSourceHeadAndOwner(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	before, sourceBranch := seedProjectConversationWithProviderHistory(
		t, st, "failed-fresh-project-conversation", now)
	providerErr := errors.New("provider unavailable")
	var attemptedScope string
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{
			start: func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
				attemptedScope = cfg.ProviderScopeID
				return nil, providerErr
			},
		}},
		Log:   slog.New(slog.DiscardHandler),
		NewID: func() string { return "failed-fresh-project-boundary" },
		Now:   func() time.Time { return now.Add(2 * time.Second) },
	})

	_, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("Start error = %v, want provider failure", err)
	}
	if attemptedScope == "" || attemptedScope == sourceBranch.ProviderScopeID {
		t.Fatalf("attempted provider scope = %q, want reserved fresh scope", attemptedScope)
	}
	after, err := st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatalf("ConversationForSession after failure: %v", err)
	}
	if after.ActiveBranchID != before.ActiveBranchID {
		t.Fatalf("active branch after provider failure = %q, want source %q",
			after.ActiveBranchID, before.ActiveBranchID)
	}
	record, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("GetSession after failure: found=%v err=%v", found, err)
	}
	if record.Metadata.ProviderConversationID != "source-provider-thread" ||
		record.Metadata.ControllerGeneration != "source-generation" {
		t.Fatalf("provider owner after failure = handle %q generation %q",
			record.Metadata.ProviderConversationID, record.Metadata.ControllerGeneration)
	}
}

func TestFreshProjectControllerReadyFailurePreservesSourceHeadAndOwner(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	before, sourceBranch := seedProjectConversationWithProviderHistory(
		t, st, "failed-fresh-project-ready-conversation", now)
	readyErr := errors.New("lifecycle ownership commit failed")
	var reservedBoundary *domain.ConversationBranch
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: func() *fakeConversation {
			conversation := newFakeConversation()
			conversation.providerConversationID = "fresh-provider-thread"
			return conversation
		}()}},
		Log:   slog.New(slog.DiscardHandler),
		NewID: func() string { return "failed-ready-provider-boundary" },
		Now:   func() time.Time { return now.Add(2 * time.Second) },
	})

	_, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ControllerReady: func(result chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			reservedBoundary = result.ProviderBoundary
			return chatsvc.ControllerCommit{}, readyErr
		},
	})
	if !errors.Is(err, readyErr) {
		t.Fatalf("Start error = %v, want ControllerReady failure", err)
	}
	if reservedBoundary == nil || reservedBoundary.ID == "" ||
		reservedBoundary.ID == sourceBranch.ProviderScopeID ||
		reservedBoundary.ProviderScopeID != reservedBoundary.ID {
		t.Fatalf("reserved provider boundary = %+v, source scope %q",
			reservedBoundary, sourceBranch.ProviderScopeID)
	}
	after, err := st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatalf("ConversationForSession after callback failure: %v", err)
	}
	if after.ActiveBranchID != before.ActiveBranchID {
		t.Fatalf("active branch after callback failure = %q, want source %q",
			after.ActiveBranchID, before.ActiveBranchID)
	}
	record, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("GetSession after callback failure: found=%v err=%v", found, err)
	}
	if record.Metadata.ProviderConversationID != "source-provider-thread" ||
		record.Metadata.ControllerGeneration != "source-generation" {
		t.Fatalf("provider owner after callback failure = handle %q generation %q",
			record.Metadata.ProviderConversationID, record.Metadata.ControllerGeneration)
	}
	if _, err := st.ConversationBranch(ctx, before.ID, reservedBoundary.ID); !errors.Is(err, domain.ErrNoConversationBranch) {
		t.Fatalf("failed callback boundary lookup error = %v, want ErrNoConversationBranch", err)
	}
}

func TestResumeCanSkipNativeHistoryImportWithoutStartingFresh(t *testing.T) {
	st := openStore(t)
	historyReads := 0
	conv := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
		events: []ports.ChatEvent{{
			Kind: ports.ChatEventTurnStarted, ProviderTurnID: "old-target-turn",
		}},
		onRead: func(int) { historyReads++ },
	}
	conv.providerConversationID = "target-native-thread"
	var started ports.ChatStartConfig
	var resumed ports.ChatResumeConfig
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{
			conv: conv, startCfg: &started, resumeCfg: &resumed,
		}},
		Log:   slog.New(slog.DiscardHandler),
		NewID: func() string { return "skip-native-history-import" },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	controller, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath:           t.TempDir(),
		Model:                   "selected-target-model",
		ProviderConversationID:  "target-native-thread",
		SkipNativeHistoryImport: true,
	})
	if err != nil {
		t.Fatalf("Start resume without history import: %v", err)
	}
	if resumed.ProviderConversationID != "target-native-thread" {
		t.Fatalf("resume config = %#v, want target native thread", resumed)
	}
	if resumed.Model != "selected-target-model" {
		t.Fatalf("resume model = %q, want selected-target-model", resumed.Model)
	}
	if started.SessionID != "" {
		t.Fatalf("fresh start was used instead of resume: %#v", started)
	}
	if historyReads != 0 {
		t.Fatalf("native history reads = %d, want none before provider boundary commit", historyReads)
	}
	if got := controller.ProviderConversationID(); got != "target-native-thread" {
		t.Fatalf("controller provider conversation = %q, want resumed target", got)
	}
}

func TestResumeImportsNativeHistoryBeforeTheChatControllerStarts(t *testing.T) {
	st := openStore(t)
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	existing, err := st.CreateConversation(context.Background(), "existing-conversation",
		domain.ConversationScopeSession, testProject, testSession, now)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := st.ClaimChatControllerGeneration(context.Background(), testSession, "old-generation", now); err != nil {
		t.Fatalf("ClaimChatControllerGeneration: %v", err)
	}
	created, err := st.AppendUserMessage(context.Background(), existing.ID, testSession, "old-generation",
		domain.ConversationMessage{
			ID: "existing-user", Text: "What changed?", Origin: domain.MessageOriginHuman,
			ClientMessageID: "original-chat-client-id",
		}, "existing-turn", now)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage: created=%v err=%v", created, err)
	}
	if err := st.BindTurnToProvider(context.Background(), "existing-turn", "native-turn-1", now); err != nil {
		t.Fatalf("BindTurnToProvider: %v", err)
	}
	if err := st.SettleAssistantMessage(context.Background(), existing.ID,
		"native-answer-1", "native-turn-1", "Nothing is dirty.", "existing-answer", now); err != nil {
		t.Fatalf("SettleAssistantMessage: %v", err)
	}
	if err := st.UpsertActivity(context.Background(), existing.ID, "native-turn-1",
		domain.ConversationActivity{
			ID: "existing-command", Kind: domain.ActivityKindCommand, Status: domain.ActivityStatusCompleted,
			Summary: "Ran git status", Detail: json.RawMessage(`{"command":"git status"}`), ProviderItemID: "native-command-1",
		}, now); err != nil {
		t.Fatalf("UpsertActivity: %v", err)
	}
	if err := st.SettleTurn(context.Background(), existing.ID, "native-turn-1", domain.TurnStateCompleted, "", now); err != nil {
		t.Fatalf("SettleTurn: %v", err)
	}

	base := newFakeConversation()
	conv := &nativeHistoryConversation{
		fakeConversation: base,
		events: []ports.ChatEvent{
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-start", ProviderTurnID: "native-turn-1"},
			{
				Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user",
				ProviderTurnID: "native-turn-1", ProviderItemID: "history-item-1",
				ClientMessageID: "native-client-1", Text: "What changed?",
			},
			{
				Kind: ports.ChatEventActivityCompleted, ProviderEventID: "history-command",
				ProviderTurnID: "native-turn-1", ProviderItemID: "history-item-2",
				ActivityKind: domain.ActivityKindCommand, ActivityStatus: domain.ActivityStatusCompleted,
				Summary: "Ran git status", Detail: json.RawMessage(`{"command":"git status"}`),
			},
			{
				Kind: ports.ChatEventActivityCompleted, ProviderEventID: "history-new-command",
				ProviderTurnID: "native-turn-1", ProviderItemID: "history-item-new-command",
				ActivityKind: domain.ActivityKindCommand, ActivityStatus: domain.ActivityStatusCompleted,
				Summary: "Ran git diff", Detail: json.RawMessage(`{"command":"git diff"}`),
			},
			{
				Kind: ports.ChatEventMessageCompleted, ProviderEventID: "history-answer",
				ProviderTurnID: "native-turn-1", ProviderItemID: "history-item-3", Text: "Nothing is dirty.",
			},
			{
				Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-complete",
				ProviderTurnID: "native-turn-1", TurnState: domain.TurnStateRecovered,
			},
		},
	}

	var idMu sync.Mutex
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			rows, err := st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation: rows.Conversation,
				Turns:        rows.Turns,
				Messages:     rows.Messages,
				Activities:   rows.Activities,
			}, nil
		}),
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			nextID++
			return fmt.Sprintf("history-id-%d", nextID)
		},
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if err != nil {
		t.Fatalf("Start resume: %v", err)
	}
	snapshot, err := st.LoadConversationSnapshot(context.Background(), ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	// The turn already existed from an earlier Chat interval. Codex can omit its
	// persisted item ids, so the replay uses synthetic item ids even though the
	// live assistant message used native-answer-1. Stable turn identity and the
	// settled content keep the replay from duplicating either message, while the
	// command AO already knew is deduplicated too, while the new command that AO
	// had not seen yet is still imported.
	if len(snapshot.Messages) != 2 || snapshot.Messages[0].Text != "What changed?" || snapshot.Messages[1].Text != "Nothing is dirty." {
		t.Fatalf("imported messages = %#v", snapshot.Messages)
	}
	if len(snapshot.Activities) != 2 || snapshot.Activities[0].Summary != "Ran git status" || snapshot.Activities[1].Summary != "Ran git diff" {
		t.Fatalf("imported activities = %#v", snapshot.Activities)
	}
	if len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateCompleted {
		t.Fatalf("imported turns = %#v", snapshot.Turns)
	}
	if snapshot.Turns[0].ProviderTurnID != "native-turn-1" {
		t.Fatalf("provider turn = %q, want durable native-turn-1", snapshot.Turns[0].ProviderTurnID)
	}
	if snapshot.Turns[0].CompletedAt == nil || !snapshot.Turns[0].CompletedAt.Equal(now) {
		t.Fatalf("replayed completion = %v, want original %s", snapshot.Turns[0].CompletedAt, now)
	}
}

func TestInterfaceHandoffRefreshesNativeHistoryUntilSettledBeforeStartingChat(t *testing.T) {
	st := openStore(t)
	rec, found, err := st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("load session: found=%v err=%v", found, err)
	}
	rec.Metadata.LatestUserPrompt = "Finish the work."
	rec.Metadata.LatestAssistantUpdate = "Settled answer."
	if err := st.UpdateSession(context.Background(), rec); err != nil {
		t.Fatalf("seed native replay checkpoint: %v", err)
	}
	conv := &convergingHistoryConversation{
		fakeConversation: newFakeConversation(),
		events: []ports.ChatEvent{
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-start", ProviderTurnID: "native-turn-1"},
			{
				Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user",
				ProviderTurnID: "native-turn-1", ProviderItemID: "native-user-1", Text: "Finish the work.",
			},
			{
				Kind: ports.ChatEventMessageCompleted, ProviderEventID: "history-answer",
				ProviderTurnID: "native-turn-1", ProviderItemID: "native-answer-1", Text: "Settled answer.",
			},
			{
				Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-complete",
				ProviderTurnID: "native-turn-1", TurnState: domain.TurnStateCompleted,
			},
		},
	}
	var idMu sync.Mutex
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			nextID++
			return fmt.Sprintf("converging-history-%d", nextID)
		},
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if err != nil {
		t.Fatalf("Start handoff: %v", err)
	}
	reads, refreshes := conv.historyAttempts()
	if reads != 1 || refreshes != 1 {
		t.Fatalf("history attempts = %d reads, %d refreshes; want one initial read followed by one refresh",
			reads, refreshes)
	}
	snapshot, err := st.LoadConversationSnapshot(context.Background(), ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 || snapshot.Messages[0].Text != "Finish the work." ||
		snapshot.Messages[1].Text != "Settled answer." {
		t.Fatalf("messages = %#v, want checkpoint prompt and settled native answer", snapshot.Messages)
	}
	if len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateCompleted {
		t.Fatalf("turns = %#v, want one completed native turn", snapshot.Turns)
	}
}

func TestInterfaceHandoffRefreshesNativeHistoryUntilItReachesTheCheckpoint(t *testing.T) {
	st := openStore(t)
	rec, found, err := st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("load session: found=%v err=%v", found, err)
	}
	rec.Metadata.LatestUserPrompt = "Run the final verification."
	rec.Metadata.LatestAssistantUpdate = "The final verification passed."
	if err := st.UpdateSession(context.Background(), rec); err != nil {
		t.Fatalf("seed native replay checkpoint: %v", err)
	}

	conv := &convergingHistoryConversation{
		fakeConversation: newFakeConversation(),
		initialSettled:   true,
		// The first authoritative observation is internally settled but stale.
		// RefreshHistory performs the provider read that reaches AO's checkpoint.
		initialEvents: nil,
		events: []ports.ChatEvent{
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-start", ProviderTurnID: "native-turn-1"},
			{
				Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user",
				ProviderTurnID: "native-turn-1", ProviderItemID: "native-user-1",
				Text: "Run the final verification.",
			},
			{
				Kind: ports.ChatEventMessageCompleted, ProviderEventID: "history-answer",
				ProviderTurnID: "native-turn-1", ProviderItemID: "native-answer-1",
				Text: "The final verification passed.",
			},
			{
				Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-complete",
				ProviderTurnID: "native-turn-1", TurnState: domain.TurnStateCompleted,
			},
		},
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("checkpoint-refresh-%d", time.Now().UnixNano()) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if err != nil {
		t.Fatalf("Start handoff: %v", err)
	}
	reads, refreshes := conv.historyAttempts()
	if reads != 1 || refreshes != 1 {
		t.Fatalf("history attempts = %d reads, %d refreshes; want one stale read followed by one refresh",
			reads, refreshes)
	}
	snapshot, err := st.LoadConversationSnapshot(context.Background(), ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 || snapshot.Messages[0].Text != "Run the final verification." ||
		snapshot.Messages[1].Text != "The final verification passed." {
		t.Fatalf("messages = %#v, want refreshed checkpoint transcript", snapshot.Messages)
	}
}

func TestInterfaceHandoffImportsInterruptedUserOnlyNativeHistory(t *testing.T) {
	st := openStore(t)
	conv := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
		events: []ports.ChatEvent{
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-start", ProviderTurnID: "native-turn-1"},
			{
				Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user",
				ProviderTurnID: "native-turn-1", ProviderItemID: "native-user-1",
				Text: "AO transferred the previous agent's context in hidden system instructions.",
			},
			{
				Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-interrupted",
				ProviderTurnID: "native-turn-1", TurnState: domain.TurnStateInterrupted,
			},
		},
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("interrupted-history-%d", time.Now().UnixNano()) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if err != nil {
		t.Fatalf("Start handoff: %v", err)
	}
	snapshot, err := st.LoadConversationSnapshot(context.Background(), ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].Text != conv.events[1].Text {
		t.Fatalf("messages = %#v, want preserved interrupted handoff prompt", snapshot.Messages)
	}
	if len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateInterrupted {
		t.Fatalf("turns = %#v, want one interrupted native turn", snapshot.Turns)
	}
}

func TestInterfaceHandoffImportsOutcomeUnknownNativeHistoryAsRecovered(t *testing.T) {
	st := openStore(t)
	conv := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
		events: []ports.ChatEvent{
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-start", ProviderTurnID: "native-turn-1"},
			{
				Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user",
				ProviderTurnID: "native-turn-1", ProviderItemID: "native-user-1",
				Text: "Historical work with no portable provider outcome.",
			},
			{
				Kind: ports.ChatEventMessageCompleted, ProviderEventID: "history-answer",
				ProviderTurnID: "native-turn-1", ProviderItemID: "native-answer-1",
				Text: "Partial or complete historical output.",
			},
			{
				Kind: ports.ChatEventActivityCompleted, ProviderEventID: "history-tool",
				ProviderTurnID: "native-turn-1", ProviderItemID: "native-tool-1",
				ActivityKind: domain.ActivityKindCommand, ActivityStatus: domain.ActivityStatusRecovered,
				Summary: "Historical command with no portable outcome",
			},
			{
				Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-recovered",
				ProviderTurnID: "native-turn-1", TurnState: domain.TurnStateRecovered,
			},
		},
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("recovered-history-%d", time.Now().UnixNano()) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if err != nil {
		t.Fatalf("Start handoff: %v", err)
	}
	snapshot, err := st.LoadConversationSnapshot(context.Background(), ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateRecovered ||
		!snapshot.Turns[0].State.Terminal() {
		t.Fatalf("snapshot = turns %#v messages %#v activities %#v, want one terminal recovered native turn",
			snapshot.Turns, snapshot.Messages, snapshot.Activities)
	}
	if len(snapshot.Activities) != 1 || snapshot.Activities[0].Status != domain.ActivityStatusRecovered {
		t.Fatalf("activities = %#v, want one recovered historical activity", snapshot.Activities)
	}
}

func TestInterfaceHandoffRejectsAProviderWithoutNativeHistoryReplay(t *testing.T) {
	st := openStore(t)
	conv := newFakeConversation()
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("no-history-%d", time.Now().UnixNano()) },
	})

	_, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if !errors.Is(err, ports.ErrChatHistoryUnavailable) {
		t.Fatalf("Start error = %v, want ErrChatHistoryUnavailable", err)
	}
	if _, controllerErr := svc.Controller(testSession); !errors.Is(controllerErr, chatsvc.ErrNoController) {
		t.Fatalf("Controller error = %v, want no target controller after failed replay", controllerErr)
	}
}

func TestOrdinaryResumeAllowsACPContextWithoutHistoryReplay(t *testing.T) {
	st := openStore(t)
	conv := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
		err:              ports.ErrChatHistoryUnavailable,
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("context-only-%d", time.Now().UnixNano()) },
	})

	if _, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1",
	}); err != nil {
		t.Fatalf("ordinary resume with provider context: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
}

func TestInterfaceHandoffReportsUnsettledHistoryWhenContextEndsBeforeRefresh(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conv := &convergingHistoryConversation{
		fakeConversation: newFakeConversation(),
		// End the request only after native history import is reached. A tiny
		// wall-clock deadline here used to expire during SQLite setup under the
		// race runner and test ClaimChatControllerGeneration instead.
		onRead: cancel,
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("unsettled-%d", time.Now().UnixNano()) },
	})
	_, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if !errors.Is(err, ports.ErrChatHistoryUnsettled) {
		t.Fatalf("Start error = %v, want ErrChatHistoryUnsettled", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context cancellation cause", err)
	}
	reads, refreshes := conv.historyAttempts()
	if reads != 1 || refreshes != 0 {
		t.Fatalf("history attempts = %d reads, %d refreshes; want cancellation before refresh",
			reads, refreshes)
	}
}

func TestInterfaceHandoffRejectsUnsettledImmutableHistoryWithoutRereading(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conv := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
		err:              ports.ErrChatHistoryUnsettled,
		// Cancel a forbidden second read so the test fails quickly instead of
		// waiting the full 45s settle limit on a regression.
		onRead: func(reads int) {
			if reads == 2 {
				cancel()
			}
		},
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("immutable-unsettled-%d", time.Now().UnixNano()) },
	})
	_, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if !errors.Is(err, ports.ErrChatHistoryUnsettled) {
		t.Fatalf("Start error = %v, want ErrChatHistoryUnsettled", err)
	}
	if reads := conv.historyReads(); reads != 1 {
		t.Fatalf("immutable history reads = %d, want exactly one", reads)
	}
}

func TestInterfaceHandoffRejectsSettledReplayBeforeLatestSessionCheckpoint(t *testing.T) {
	st := openStore(t)
	rec, found, err := st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("load session: found=%v err=%v", found, err)
	}
	rec.Metadata.LatestUserPrompt = "Run the final verification."
	rec.Metadata.LatestAssistantUpdate = "The final verification passed."
	if err := st.UpdateSession(context.Background(), rec); err != nil {
		t.Fatalf("seed native replay checkpoint: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conv := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
		// A provider may report a syntactically settled but stale prefix while its
		// on-disk transcript is still being flushed. Empty is the strongest form of
		// that failure: no replay event reaches the hook facts AO already observed.
		events: nil,
		// Cancel a forbidden second read so the test fails quickly instead of
		// waiting the full 45s settle limit on a regression.
		onRead: func(reads int) {
			if reads == 2 {
				cancel()
			}
		},
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("checkpoint-%d", time.Now().UnixNano()) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	_, err = svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if !errors.Is(err, ports.ErrChatHistoryUnsettled) {
		t.Fatalf("Start error = %v, want ErrChatHistoryUnsettled for stale settled replay", err)
	}
	if reads := conv.historyReads(); reads != 1 {
		t.Fatalf("immutable history reads = %d, want exactly one", reads)
	}
}

func TestInterfaceHandoffDoesNotAnchorReplayCheckpointOnFailedTurn(t *testing.T) {
	st := openStore(t)
	now := time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC)
	existing, err := st.CreateConversation(context.Background(), "failed-anchor-conversation",
		domain.ConversationScopeSession, testProject, testSession, now)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := st.ClaimChatControllerGeneration(context.Background(), testSession, "old-generation", now); err != nil {
		t.Fatalf("ClaimChatControllerGeneration: %v", err)
	}
	// An older completed Chat round trip that the provider will replay.
	created, err := st.AppendUserMessage(context.Background(), existing.ID, testSession, "old-generation",
		domain.ConversationMessage{
			ID: "settled-user", Text: "What changed?", Origin: domain.MessageOriginHuman,
			ClientMessageID: "settled-client-id",
		}, "settled-turn", now)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage settled: created=%v err=%v", created, err)
	}
	if err := st.BindTurnToProvider(context.Background(), "settled-turn", "native-turn-1", now); err != nil {
		t.Fatalf("BindTurnToProvider settled: %v", err)
	}
	if err := st.SettleAssistantMessage(context.Background(), existing.ID,
		"native-answer-1", "native-turn-1", "Nothing is dirty.", "settled-answer", now); err != nil {
		t.Fatalf("SettleAssistantMessage settled: %v", err)
	}
	if err := st.SettleTurn(context.Background(), existing.ID, "native-turn-1",
		domain.TurnStateCompleted, "", now); err != nil {
		t.Fatalf("SettleTurn settled: %v", err)
	}
	// A newer failed Chat turn. Its synthetic auth-error answer lives on a dead
	// transcript branch: the provider forked the next TUI prompt from the entry
	// before this turn, so session/load never replays these items.
	later := now.Add(time.Minute)
	created, err = st.AppendUserMessage(context.Background(), existing.ID, testSession, "old-generation",
		domain.ConversationMessage{
			ID: "failed-user", Text: "Spawn a worker to fix the link behavior.", Origin: domain.MessageOriginHuman,
			ClientMessageID: "failed-client-id",
		}, "failed-turn", later)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage failed turn: created=%v err=%v", created, err)
	}
	if err := st.BindTurnToProvider(context.Background(), "failed-turn", "native-turn-2", later); err != nil {
		t.Fatalf("BindTurnToProvider failed turn: %v", err)
	}
	if err := st.SettleAssistantMessage(context.Background(), existing.ID,
		"native-error-1", "native-turn-2",
		"Failed to authenticate: OAuth session expired and could not be refreshed", "failed-answer", later); err != nil {
		t.Fatalf("SettleAssistantMessage failed turn: %v", err)
	}
	if err := st.SettleTurn(context.Background(), existing.ID, "native-turn-2",
		domain.TurnStateFailed, "authentication_failed", later); err != nil {
		t.Fatalf("SettleTurn failed turn: %v", err)
	}

	// The user re-ran the request in the terminal after fixing auth; hooks
	// captured that newest round trip, and the provider replays it.
	rec, found, err := st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("load session: found=%v err=%v", found, err)
	}
	rec.Metadata.LatestUserPrompt = "Spawn a worker to fix the link behavior in the terminal."
	rec.Metadata.LatestAssistantUpdate = "Worker spawned."
	if err := st.UpdateSession(context.Background(), rec); err != nil {
		t.Fatalf("seed native replay checkpoint: %v", err)
	}

	conv := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
		events: []ports.ChatEvent{
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-start-1", ProviderTurnID: "native-turn-1"},
			{
				Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user-1",
				ProviderTurnID: "native-turn-1", ProviderItemID: "history-item-1", Text: "What changed?",
			},
			{
				Kind: ports.ChatEventMessageCompleted, ProviderEventID: "history-answer-1",
				ProviderTurnID: "native-turn-1", ProviderItemID: "history-item-2", Text: "Nothing is dirty.",
			},
			{
				Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-complete-1",
				ProviderTurnID: "native-turn-1", TurnState: domain.TurnStateCompleted,
			},
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-start-2", ProviderTurnID: "native-turn-tui"},
			{
				Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user-2",
				ProviderTurnID: "native-turn-tui", ProviderItemID: "history-item-3",
				Text: "Spawn a worker to fix the link behavior in the terminal.",
			},
			{
				Kind: ports.ChatEventMessageCompleted, ProviderEventID: "history-answer-2",
				ProviderTurnID: "native-turn-tui", ProviderItemID: "history-item-4", Text: "Worker spawned.",
			},
			{
				Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-complete-2",
				ProviderTurnID: "native-turn-tui", TurnState: domain.TurnStateCompleted,
			},
		},
	}
	var idMu sync.Mutex
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			rows, err := st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation: rows.Conversation,
				Turns:        rows.Turns,
				Messages:     rows.Messages,
				Activities:   rows.Activities,
			}, nil
		}),
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			nextID++
			return fmt.Sprintf("failed-anchor-%d", nextID)
		},
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	})
	if err != nil {
		t.Fatalf("Start resume = %v, want success: a failed turn's never-replayed items must not gate the handoff", err)
	}
	snapshot, err := st.LoadConversationSnapshot(context.Background(), ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	// The failed turn stays durable in AO's projection even though the provider
	// never replays it.
	var failedState domain.TurnState
	for _, turn := range snapshot.Turns {
		if turn.ProviderTurnID == "native-turn-2" {
			failedState = turn.State
		}
	}
	if failedState != domain.TurnStateFailed {
		t.Fatalf("failed turn state = %q, want preserved %q (turns = %#v)",
			failedState, domain.TurnStateFailed, snapshot.Turns)
	}
}

func TestInterfaceHandoffDoesNotAnchorReplayBeforeProviderCoordinationBoundary(t *testing.T) {
	st := openStore(t)
	now := time.Date(2026, 8, 23, 17, 0, 0, 0, time.UTC)
	existing, err := st.CreateConversation(context.Background(), "provider-boundary-conversation",
		domain.ConversationScopeSession, testProject, testSession, now)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := st.ClaimChatControllerGeneration(context.Background(), testSession, "old-generation", now); err != nil {
		t.Fatalf("ClaimChatControllerGeneration: %v", err)
	}

	// This completed turn belongs to the provider that owned the session before
	// the agent switch. The new provider receives it through hidden handoff
	// instructions, so its native thread cannot replay the old provider IDs.
	created, err := st.AppendUserMessage(context.Background(), existing.ID, testSession, "old-generation",
		domain.ConversationMessage{
			ID: "old-provider-user", Text: "Finish the earlier task.", Origin: domain.MessageOriginHuman,
			ClientMessageID: "old-provider-client",
		}, "old-provider-turn", now)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage old provider: created=%v err=%v", created, err)
	}
	if err := st.BindTurnToProvider(context.Background(), "old-provider-turn", "old-provider-turn-id", now); err != nil {
		t.Fatalf("BindTurnToProvider old provider: %v", err)
	}
	if err := st.SettleAssistantMessage(context.Background(), existing.ID,
		"old-provider-answer-id", "old-provider-turn-id", "Earlier work finished.", "old-provider-answer", now); err != nil {
		t.Fatalf("SettleAssistantMessage old provider: %v", err)
	}
	if err := st.SettleTurn(context.Background(), existing.ID, "old-provider-turn-id",
		domain.TurnStateCompleted, "", now); err != nil {
		t.Fatalf("SettleTurn old provider: %v", err)
	}

	// The first native turn in the replacement provider is AO's handoff marker.
	// It is a durable boundary even when the provider rejected that turn.
	boundaryAt := now.Add(time.Minute)
	created, err = st.AppendUserMessage(context.Background(), existing.ID, testSession, "old-generation",
		domain.ConversationMessage{
			ID:     "coordination-user",
			Text:   "AO transferred the previous agent's context in hidden system instructions. Continue the task.",
			Origin: domain.MessageOriginDaemon, ClientMessageID: "coordination-client",
		}, "coordination-turn", boundaryAt)
	if err != nil || !created {
		t.Fatalf("AppendUserMessage coordination: created=%v err=%v", created, err)
	}
	if err := st.BindTurnToProvider(context.Background(), "coordination-turn", "new-provider-boundary", boundaryAt); err != nil {
		t.Fatalf("BindTurnToProvider coordination: %v", err)
	}
	if err := st.SettleTurn(context.Background(), existing.ID, "new-provider-boundary",
		domain.TurnStateFailed, "unsupported model", boundaryAt); err != nil {
		t.Fatalf("SettleTurn coordination: %v", err)
	}

	rec, found, err := st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("load session: found=%v err=%v", found, err)
	}
	rec.Metadata.LatestUserPrompt = "Run the current provider check."
	if err := st.UpdateSession(context.Background(), rec); err != nil {
		t.Fatalf("seed native replay checkpoint: %v", err)
	}

	conv := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
		events: []ports.ChatEvent{
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "boundary-start", ProviderTurnID: "new-provider-boundary"},
			{
				Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "boundary-user",
				ProviderTurnID: "new-provider-boundary", ProviderItemID: "boundary-item",
				Text: "AO transferred the previous agent's context in hidden system instructions. Continue the task.",
			},
			{
				Kind: ports.ChatEventTurnCompleted, ProviderEventID: "boundary-complete",
				ProviderTurnID: "new-provider-boundary", TurnState: domain.TurnStateFailed,
			},
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "current-start", ProviderTurnID: "new-provider-turn"},
			{
				Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "current-user",
				ProviderTurnID: "new-provider-turn", ProviderItemID: "current-user-item",
				Text: "Run the current provider check.",
			},
			{
				Kind: ports.ChatEventMessageCompleted, ProviderEventID: "current-answer",
				ProviderTurnID: "new-provider-turn", ProviderItemID: "current-answer-item",
				Text: "The current provider check passed.",
			},
			{
				Kind: ports.ChatEventTurnCompleted, ProviderEventID: "current-complete",
				ProviderTurnID: "new-provider-turn", TurnState: domain.TurnStateCompleted,
			},
		},
	}
	conv.providerConversationID = "new-provider-thread"
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			rows, err := st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation: rows.Conversation,
				Turns:        rows.Turns, Messages: rows.Messages, Activities: rows.Activities,
			}, nil
		}),
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("provider-boundary-%d", time.Now().UnixNano()) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	if _, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "new-provider-thread", RequireNativeHistory: true,
	}); err != nil {
		t.Fatalf("Start resume = %v, want success after the replacement-provider boundary", err)
	}
}

func TestSlowNativeHistoryDoesNotBlockOtherControllerLookups(t *testing.T) {
	st := openStore(t)
	conv := &blockingHistoryConversation{
		fakeConversation: newFakeConversation(),
		started:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			rows, err := st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation: rows.Conversation,
				Turns:        rows.Turns, Messages: rows.Messages, Activities: rows.Activities,
			}, nil
		}),
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("lock-id-%d", time.Now().UnixNano()) },
	})
	workspace := t.TempDir()
	startDone := make(chan error, 1)
	go func() {
		_, err := svc.Start(context.Background(), chatsvc.StartConfig{
			SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
			WorkspacePath: workspace, ProviderConversationID: "thread-1",
		})
		startDone <- err
	}()
	select {
	case <-conv.started:
	case <-time.After(time.Second):
		t.Fatal("native history import did not start")
	}

	lookupDone := make(chan error, 1)
	go func() {
		_, err := svc.Controller(domain.SessionID("another-session"))
		lookupDone <- err
	}()
	select {
	case err := <-lookupDone:
		if !errors.Is(err, chatsvc.ErrNoController) {
			t.Fatalf("Controller error = %v, want ErrNoController", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("a slow native history import blocked an unrelated controller lookup")
	}

	close(conv.release)
	if err := <-startDone; err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
}

func TestFreshProjectControllerRecordsNativeContextBoundary(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	conversation, err := st.CreateConversation(ctx, "project-conversation",
		domain.ConversationScopeProject, testProject, testSession, now)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := st.UpsertActivity(ctx, conversation.ID, "", domain.ConversationActivity{
		ID: "old-activity", Kind: domain.ActivityKindSystem, Status: domain.ActivityStatusCompleted,
		Summary: "Earlier project history", ProviderItemID: "old-project-history",
	}, now); err != nil {
		t.Fatalf("seed project history: %v", err)
	}

	const replacement = domain.SessionID("p1-2")
	if _, err := st.CreateSession(ctx, domain.SessionRecord{
		ID:        replacement,
		ProjectID: testProject,
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessCodex,
		Mode:      domain.SessionModeChat,
		Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata:  domain.SessionMetadata{Branch: "feat/replacement", WorkspacePath: t.TempDir()},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed replacement session: %v", err)
	}

	var idMu sync.Mutex
	nextID := 0
	driver := fakeDriver{}
	driver.start = func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
		conv := newFakeConversation()
		conv.providerConversationID = "thread-" + cfg.ProviderScopeID
		return conv, nil
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: driver},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			nextID++
			return fmt.Sprintf("boundary-id-%d", nextID)
		},
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	start := chatsvc.StartConfig{
		SessionID: replacement, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
	}
	if _, err := svc.Start(ctx, start); err != nil {
		t.Fatalf("Start replacement: %v", err)
	}

	rebound, err := st.ConversationForSession(ctx, replacement)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	if rebound.ID != conversation.ID {
		t.Fatalf("conversation = %q, want project narrative %q", rebound.ID, conversation.ID)
	}
	snapshot, err := st.LoadConversationSnapshot(ctx, rebound.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if len(snapshot.Activities) != 2 {
		t.Fatalf("activities = %#v, want old history plus context boundary", snapshot.Activities)
	}
	firstScope := snapshot.ActiveBranch.ProviderScopeID
	boundary := snapshot.Activities[1]
	if boundary.Kind != domain.ActivityKindSystem ||
		boundary.ProviderItemID != domain.ConversationContextResetProviderItemID(replacement) {
		t.Fatalf("boundary = %#v", boundary)
	}
	var detail map[string]string
	if err := json.Unmarshal(boundary.Detail, &detail); err != nil {
		t.Fatalf("decode boundary detail: %v", err)
	}
	if detail["event"] != "context.reset" {
		t.Fatalf("boundary event = %q", detail["event"])
	}
	firstTurn, err := svc.Send(ctx, replacement, ports.ChatUserMessage{
		Text: "first prompt in fresh context", ClientMessageID: "fresh-context-first-prompt",
	})
	if err != nil {
		t.Fatalf("Send first fresh-context prompt: %v", err)
	}
	anchor, err := st.ConversationEditAnchor(ctx, rebound.ID, firstTurn.ID)
	if err != nil {
		t.Fatalf("ConversationEditAnchor first fresh-context prompt: %v", err)
	}
	if anchor.HasPriorContext {
		t.Fatalf("first fresh-context prompt sees display-only reset marker as prior context: %+v", anchor)
	}

	if err := svc.Stop(ctx, replacement); err != nil {
		t.Fatalf("Stop first replacement: %v", err)
	}
	if _, err := svc.Start(ctx, start); err != nil {
		t.Fatalf("Start second fresh provider: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), replacement) })
	secondSnapshot, err := st.LoadConversationSnapshot(ctx, rebound.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot second boundary: %v", err)
	}
	if len(secondSnapshot.Activities) != 2 {
		t.Fatalf("activities after second start = %#v, want the session-scoped boundary to remain unique",
			secondSnapshot.Activities)
	}
	secondScope := secondSnapshot.ActiveBranch.ProviderScopeID
	if secondScope == firstScope {
		t.Fatalf("second provider scope = %q, reused first scope", secondScope)
	}
	if secondSnapshot.Activities[1].ProviderItemID != boundary.ProviderItemID {
		t.Fatalf("session boundary changed across provider scopes: second = %#v, first = %#v",
			secondSnapshot.Activities[1], boundary)
	}
}

func TestFreshProjectControllerStartFailureKeepsPreviousHistoryHidden(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	conversation, err := st.CreateConversation(ctx, "project-conversation",
		domain.ConversationScopeProject, testProject, testSession, now)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := st.AppendUserMessage(ctx, conversation.ID, testSession, "old-generation",
		domain.ConversationMessage{
			ID: "old-message", Text: "old orchestrator history", Origin: domain.MessageOriginHuman,
		}, "old-turn", now.Add(time.Second)); err != nil {
		t.Fatalf("seed old history: %v", err)
	}

	replacementRecord, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject,
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessCodex,
		Mode:      domain.SessionModeChat,
		Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata:  domain.SessionMetadata{Branch: "feat/replacement", WorkspacePath: t.TempDir()},
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("seed replacement session: %v", err)
	}
	replacement := replacementRecord.ID

	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{
			start: func(ports.ChatStartConfig) (ports.ChatConversation, error) {
				return nil, errors.New("provider failed")
			},
		}},
		Log: slog.New(slog.DiscardHandler),
		NewID: func() string {
			return "boundary-id"
		},
		Now: func() time.Time { return now.Add(2 * time.Second) },
	})
	if _, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: replacement, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
	}); err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatalf("Start replacement error = %v, want provider failure", err)
	}

	snapshot, err := st.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if len(snapshot.Activities) != 1 ||
		snapshot.Activities[0].ProviderItemID != domain.ConversationContextResetProviderItemID(replacement) {
		t.Fatalf("reset boundary after failed start = %#v", snapshot.Activities)
	}
	page, err := st.LoadConversationSnapshotPage(ctx, conversation.ID, 0, 10)
	if err != nil {
		t.Fatalf("LoadConversationSnapshotPage: %v", err)
	}
	if len(page.Messages) != 0 || len(page.Activities) != 0 || len(page.Turns) != 0 || page.HasMoreBefore {
		t.Fatalf("page after failed start = messages %#v activities %#v turns %#v hasMore %v, want empty",
			page.Messages, page.Activities, page.Turns, page.HasMoreBefore)
	}
}

/* ---- harness ----------------------------------------------------------- */

type harness struct {
	svc      *chatsvc.Service
	st       *sqlite.Store
	conv     *fakeConversation
	ctrl     *chatsvc.Controller
	activity *recordingActivity

	clockMu sync.Mutex
	clock   time.Time
}

// advance moves the injected clock. Needed where an ordering rule is expressed in
// timestamps — the queue cancellation cutoff — rather than in call order.
func (h *harness) advance(d time.Duration) {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	h.clock = h.clock.Add(d)
}

func (h *harness) now() time.Time {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	return h.clock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWithConversation(t, nil)
}

// newHarnessWithConversation lets a test supply its own provider double, for the
// cases where the interesting behavior is how the provider answers rather than
// what it streams. A nil conv gets the plain fake.
func newHarnessWithConversation(t *testing.T, conv ports.ChatConversation) *harness {
	return newHarnessWithConversationAndStore(t, conv, func(st *sqlite.Store) chatsvc.Store { return st })
}

func newHarnessWithConversationAndStore(
	t *testing.T,
	conv ports.ChatConversation,
	wrapStore func(*sqlite.Store) chatsvc.Store,
) *harness {
	return newHarnessWithConversationAndStoreForHarness(t, conv, wrapStore, domain.HarnessCodex)
}

func newHarnessForHarness(t *testing.T, agentHarness domain.AgentHarness) *harness {
	t.Helper()
	return newHarnessWithConversationAndStoreForHarness(t, nil, func(st *sqlite.Store) chatsvc.Store { return st }, agentHarness)
}

func newHarnessWithConversationAndStoreForHarness(
	t *testing.T,
	conv ports.ChatConversation,
	wrapStore func(*sqlite.Store) chatsvc.Store,
	agentHarness domain.AgentHarness,
) *harness {
	t.Helper()
	st := openStore(t)
	base := newFakeConversation()
	if conv == nil {
		conv = base
	} else if recorder, ok := conv.(*interruptRecorder); ok {
		base = recorder.fakeConversation
	} else if recorder, ok := conv.(*historyRecorder); ok {
		base = recorder.fakeConversation
	}
	h := &harness{
		st:       st,
		conv:     base,
		activity: &recordingActivity{},
		clock:    time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC),
	}

	// Guarded because the id factory is called from both the projection goroutine and
	// whichever goroutine a test drives commands from, and an unsynchronized counter
	// is a data race the -race build fails on rather than a harmless test detail.
	var (
		counterMu sync.Mutex
		counter   int
	)
	chatStore := wrapStore(st)
	svc := chatsvc.New(chatsvc.Options{
		Store:    chatStore,
		Sessions: st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: conv}},
		Activity: h.activity,
		Log:      slog.New(slog.DiscardHandler),
		NewID: func() string {
			counterMu.Lock()
			defer counterMu.Unlock()
			counter++
			return fmt.Sprintf("id-%03d", counter)
		},
		Now: h.now,
	})

	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID:     testSession,
		ProjectID:     testProject,
		Harness:       agentHarness,
		WorkspacePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	h.svc, h.ctrl = svc, ctrl
	return h
}

// awaitSnapshot polls until pred holds, so a test does not race the projector.
func (h *harness) awaitSnapshot(t *testing.T, pred func(store.ConversationSnapshot) bool) store.ConversationSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last store.ConversationSnapshot
	for time.Now().Before(deadline) {
		snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
		if err != nil {
			t.Fatalf("load snapshot: %v", err)
		}
		last = snapshot
		if pred(snapshot) {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("snapshot never satisfied the condition; last had %d messages, %d activities, %d turns",
		len(last.Messages), len(last.Activities), len(last.Turns))
	return last
}

func TestStaleControllerEventsDoNotReachTheTimeline(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.st.ClaimChatControllerGeneration(ctx, testSession, "replacement-generation", h.now()); err != nil {
		t.Fatalf("replace controller generation: %v", err)
	}

	h.conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventMessageDelta,
		ProviderTurnID: "stale-turn",
		ProviderItemID: "stale-message",
		Delta:          "must not survive",
	})
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("stop stale controller: %v", err)
	}

	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	for _, message := range snapshot.Messages {
		if message.Text == "must not survive" {
			t.Fatalf("stale controller message was projected: %+v", message)
		}
	}
}

/* ---- tests ------------------------------------------------------------- */

// The whole point: a message goes out, provider events come back, and the durable
// timeline reflects them in sequence order.
func TestProjectsAFullTurnIntoDurableRows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text:            "what changed?",
		ClientMessageID: "client-1",
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if turn.ProviderTurnID != "provider-turn-1" {
		t.Fatalf("provider turn = %q", turn.ProviderTurnID)
	}

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityCompleted, ProviderTurnID: "provider-turn-1",
			ProviderItemID: "exec-1", ActivityKind: domain.ActivityKindCommand,
			ActivityStatus: domain.ActivityStatusCompleted, Summary: "git status --short",
		},
		// Streaming arrives in pieces and must fold into one message.
		ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: "provider-turn-1", ProviderItemID: "msg-1", Delta: "Two "},
		ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: "provider-turn-1", ProviderItemID: "msg-1", Delta: "files "},
		ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: "provider-turn-1", ProviderItemID: "msg-1", Delta: "changed."},
		ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "provider-turn-1", ProviderItemID: "msg-1", Text: "Two files changed."},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1", TurnState: domain.TurnStateCompleted},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State.Terminal() && len(s.Messages) == 2
	})

	if got := snapshot.Turns[0].State; got != domain.TurnStateCompleted {
		t.Errorf("turn state = %q, want completed", got)
	}

	user, assistant := snapshot.Messages[0], snapshot.Messages[1]
	if user.Role != domain.MessageRoleUser || user.Text != "what changed?" {
		t.Errorf("user message = %+v", user)
	}
	if user.Origin != domain.MessageOriginHuman {
		t.Errorf("user origin = %q", user.Origin)
	}
	// Three deltas folded into one message, not three timeline entries.
	if assistant.Role != domain.MessageRoleAssistant || assistant.Text != "Two files changed." {
		t.Errorf("assistant message = %+v", assistant)
	}
	if assistant.Streaming {
		t.Error("assistant message still marked streaming after completion")
	}
	if assistant.Revision == 0 {
		t.Error("assistant revision never advanced despite streaming rewrites")
	}

	// Sequence is conversation-scoped and strictly increasing across both tables.
	var sequences []int64
	for _, m := range snapshot.Messages {
		sequences = append(sequences, m.Sequence)
	}
	for _, a := range snapshot.Activities {
		sequences = append(sequences, a.Sequence)
	}
	seen := map[int64]bool{}
	for _, seq := range sequences {
		if seen[seq] {
			t.Fatalf("sequence %d was handed out twice", seq)
		}
		seen[seq] = true
	}

	if len(snapshot.Activities) != 1 || snapshot.Activities[0].Summary != "git status --short" {
		t.Fatalf("activities = %+v", snapshot.Activities)
	}
}

func TestEarlyTurnStartedBindsDispatchingTurn(t *testing.T) {
	conv := newFakeConversation()
	conv.onSend = func(providerTurnID string) {
		conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: providerTurnID})
	}
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text:            "race turn/start",
		ClientMessageID: "early-start-1",
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if turn.ProviderTurnID != "provider-turn-1" {
		t.Fatalf("provider turn = %q, want provider-turn-1", turn.ProviderTurnID)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		if len(s.Turns) != 1 {
			return false
		}
		turn := s.Turns[0]
		return turn.ProviderTurnID == "provider-turn-1" && turn.State == domain.TurnStateRunning
	})

	conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventTurnCompleted,
		ProviderTurnID: "provider-turn-1",
		TurnState:      domain.TurnStateCompleted,
	})
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
	})
	if len(snapshot.Turns) != 1 {
		t.Fatalf("turns = %d, want 1: %+v", len(snapshot.Turns), snapshot.Turns)
	}
}

func TestControllerCloseHonorsContextWhenProviderStreamStaysOpen(t *testing.T) {
	providerErr := errors.New("provider close failed")
	conv := &stuckConversation{fakeConversation: newFakeConversation(), closeErr: providerErr}
	h := newHarnessWithConversation(t, conv)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := h.ctrl.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, providerErr) {
		t.Fatalf("Close error = %v, want provider error joined with deadline", err)
	}
	// Let the projection goroutine exit so the harness cleanup remains bounded.
	close(conv.events)
	h.ctrl.Wait()
}

// A retried send under the same client message id must not create a second turn.
func TestDuplicateSendDoesNotCreateASecondTurn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	msg := ports.ChatUserMessage{Text: "hello", ClientMessageID: "client-dup", Origin: domain.MessageOriginHuman}

	if _, err := h.svc.Send(ctx, testSession, msg); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	second, err := h.svc.Send(ctx, testSession, msg)
	if err != nil {
		t.Fatalf("retried Send returned an error instead of being ignored: %v", err)
	}
	if second.ProviderTurnID != "" {
		t.Errorf("retry reported a new provider turn %q", second.ProviderTurnID)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Turns) >= 1 })
	if len(snapshot.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(snapshot.Turns))
	}
	if len(snapshot.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(snapshot.Messages))
	}
}

func TestDeferredDriverStartsOnlyAfterProviderTurnIDIsDurable(t *testing.T) {
	deferred := &deferredConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, deferred)
	deferred.start = func(providerTurnID string) error {
		snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
		if err != nil {
			return err
		}
		for _, turn := range snapshot.Turns {
			if turn.ProviderTurnID == providerTurnID {
				return nil
			}
		}
		return fmt.Errorf("provider turn %q was not bound before deferred start", providerTurnID)
	}

	turn, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{
		Text: "start through ACP", ClientMessageID: "deferred-1", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if turn.ProviderTurnID == "" {
		t.Fatal("deferred turn has no provider id")
	}
}

// An approval must be stored pending, carry the provider's own decision list, and
// only resolve through a typed action.
func TestApprovalIsStoredPendingWithProviderDecisions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// The real captured shape: no decline on offer, plus a structured decision.
	h.conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventApprovalRequested,
		ProviderTurnID: "provider-turn-1",
		ProviderItemID: "0",
		RequestID:      "0",
		ActivityKind:   domain.ActivityKindCommand,
		ActivityStatus: domain.ActivityStatusPending,
		Summary:        "Run ao spawn",
		Decisions: []ports.ChatDecisionOption{
			{ID: "accept", Label: "Approve", Kind: ports.ChatDecisionAllowOnce},
			{ID: "acceptWithExecpolicyAmendment", Label: "Approve and remember this command", Kind: ports.ChatDecisionAllowAlways},
			{ID: "cancel", Label: "Cancel", Kind: ports.ChatDecisionRejectOnce},
		},
	})

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Activities) == 1
	})
	approval := snapshot.Activities[0]
	if approval.Kind != domain.ActivityKindApproval {
		t.Fatalf("kind = %q, want approval", approval.Kind)
	}
	if approval.Status != domain.ActivityStatusPending {
		t.Fatalf("status = %q, want pending", approval.Status)
	}
	if approval.RequestID != "0" {
		t.Fatalf("request id = %q; zero is a legitimate id and must survive", approval.RequestID)
	}

	var detail struct {
		Decisions []struct{ ID, Label, Kind string } `json:"decisions"`
	}
	if err := json.Unmarshal(approval.Detail, &detail); err != nil {
		t.Fatalf("detail not decodable: %v (%s)", err, approval.Detail)
	}
	if len(detail.Decisions) != 3 {
		t.Fatalf("stored %d decisions, want the provider's 3: %+v", len(detail.Decisions), detail.Decisions)
	}
	if detail.Decisions[0].Kind != string(ports.ChatDecisionAllowOnce) ||
		detail.Decisions[1].Kind != string(ports.ChatDecisionAllowAlways) ||
		detail.Decisions[2].Kind != string(ports.ChatDecisionRejectOnce) {
		t.Fatalf("stored decision kinds = %+v", detail.Decisions)
	}
	for _, d := range detail.Decisions {
		if d.ID == "decline" {
			t.Error("stored a decline option the provider never offered")
		}
	}

	// Resolving reaches the provider and then updates the row.
	if err := h.svc.Resolve(ctx, testSession, "0", ports.ChatDecision{ID: "accept"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := h.conv.decisionFor("0"); !ok {
		t.Error("decision never reached the provider")
	}
	resolved := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Activities) == 1 && s.Activities[0].Status == domain.ActivityStatusResolved
	})
	if resolved.Activities[0].Status != domain.ActivityStatusResolved {
		t.Fatalf("status = %q, want resolved", resolved.Activities[0].Status)
	}
	var resolvedDetail struct {
		Decision  string `json:"decision"`
		Decisions []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal(resolved.Activities[0].Detail, &resolvedDetail); err != nil {
		t.Fatalf("resolved detail not decodable: %v (%s)", err, resolved.Activities[0].Detail)
	}
	if resolvedDetail.Decision != "accept" {
		t.Fatalf("resolved decision = %q, want accept", resolvedDetail.Decision)
	}
	if len(resolvedDetail.Decisions) != 3 {
		t.Fatalf("resolved decision options = %d, want preserved 3", len(resolvedDetail.Decisions))
	}
	if !hasActivitySignal(h.activity.snapshot(), domain.ActivityWaitingInput, "chat.approval.requested") {
		t.Fatalf("activity signals = %v, want waiting_input on approval request", h.activity.snapshot())
	}
	if !hasActivitySignal(h.activity.snapshot(), domain.ActivityActive, "chat.approval.resolved") {
		t.Fatalf("activity signals = %v, want active after approval resolve", h.activity.snapshot())
	}
}

func TestResolvingOneOfMultipleApprovalsKeepsWaitingInput(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for _, requestID := range []string{"approval-1", "approval-2"} {
		h.conv.emit(ports.ChatEvent{
			Kind: ports.ChatEventApprovalRequested, ProviderTurnID: "provider-turn-1",
			ProviderItemID: requestID, RequestID: requestID, Summary: "Approve " + requestID,
			Decisions: []ports.ChatDecisionOption{{ID: "accept", Label: "Approve", Kind: ports.ChatDecisionAllowOnce}},
		})
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Activities) == 2 &&
			s.Activities[0].Status == domain.ActivityStatusPending &&
			s.Activities[1].Status == domain.ActivityStatusPending
	})

	if err := h.svc.Resolve(ctx, testSession, "approval-1", ports.ChatDecision{ID: "accept"}); err != nil {
		t.Fatalf("Resolve first: %v", err)
	}
	if got := countActivitySignals(h.activity.snapshot(), domain.ActivityActive, "chat.approval.resolved"); got != 0 {
		t.Fatalf("active resolution signals after first approval = %d, want 0 while another approval is pending", got)
	}

	if err := h.svc.Resolve(ctx, testSession, "approval-2", ports.ChatDecision{ID: "accept"}); err != nil {
		t.Fatalf("Resolve second: %v", err)
	}
	if got := countActivitySignals(h.activity.snapshot(), domain.ActivityActive, "chat.approval.resolved"); got != 1 {
		t.Fatalf("active resolution signals after final approval = %d, want 1", got)
	}
}

func TestProviderResolutionKeepsWaitingInputUntilFinalApproval(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for _, requestID := range []string{"approval-1", "approval-2"} {
		h.conv.emit(ports.ChatEvent{
			Kind: ports.ChatEventApprovalRequested, ProviderTurnID: "provider-turn-1",
			ProviderItemID: requestID, RequestID: requestID, Summary: "Approve " + requestID,
		})
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Activities) == 2 })

	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventApprovalResolved, RequestID: "approval-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Activities[0].Status == domain.ActivityStatusResolved
	})
	if got := countActivitySignals(h.activity.snapshot(), domain.ActivityActive, "chat.approval.resolved"); got != 0 {
		t.Fatalf("active provider-resolution signals after first approval = %d, want 0", got)
	}

	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventApprovalResolved, RequestID: "approval-2"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := countActivitySignals(h.activity.snapshot(), domain.ActivityActive, "chat.approval.resolved"); got == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active provider-resolution signals after final approval = %d, want 1",
		countActivitySignals(h.activity.snapshot(), domain.ActivityActive, "chat.approval.resolved"))
}

// A controller that dies mid-turn must not leave the turn looking like it is still
// working, and must not leave an approval the user can never answer.
func TestControllerDeathSettlesInFlightWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"},
		startedCommand("provider-turn-1", "exec-1", "sleep 60"),
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Activities) == 1 })
	h.conv.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventApprovalRequested, ProviderTurnID: "provider-turn-1",
			ProviderItemID: "0", RequestID: "0", ActivityKind: domain.ActivityKindCommand,
			ActivityStatus: domain.ActivityStatusPending, Summary: "Run something",
		},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Activities) == 2 })

	// The provider process dies: the stream closes with the turn still open.
	_ = h.conv.Close()
	h.ctrl.Wait()
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State.Terminal()
	})
	if got := snapshot.Turns[0].State; got != domain.TurnStateFailed {
		t.Errorf("turn state = %q; an interrupted controller is not a completed turn", got)
	}
	if snapshot.Turns[0].ErrorMessage == "" {
		t.Error("orphaned turn carries no explanation")
	}
	if got := findActivity(t, snapshot, "0").Status; got == domain.ActivityStatusPending {
		t.Error("approval left pending after its controller died — the user can never answer it")
	}
	if got := findActivity(t, snapshot, "exec-1").Status; got != domain.ActivityStatusFailed {
		t.Errorf("running activity after controller death = %q, want failed", got)
	}
}

func TestControllerStreamClosureReportsSessionExited(t *testing.T) {
	h := newHarness(t)

	// A provider can disappear without emitting a final controller-state event.
	// Closing the stream is still authoritative proof that this controller ended.
	if err := h.conv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.ctrl.Wait()

	for _, signal := range h.activity.snapshot() {
		if signal.State == domain.ActivityExited && signal.Event == "chat.controller.stopped" {
			return
		}
	}
	t.Fatalf("controller stream ended without an exited lifecycle signal: %+v", h.activity.snapshot())
}

func TestControllerReadyRunsBeforeStreamProjection(t *testing.T) {
	st := openStore(t)
	conv := newFakeConversation()
	if err := conv.Close(); err != nil {
		t.Fatalf("close provider stream: %v", err)
	}
	activity := &recordingActivity{}
	ready := false
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: conv}},
		Activity: activity,
		Log:      slog.New(slog.DiscardHandler),
		NewID:    func() string { return "controller-ready-id" },
	})

	controller, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ControllerGeneration: "reserved-generation",
		ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			if signals := activity.snapshot(); len(signals) != 0 {
				t.Fatalf("provider events projected before controller-ready commit: %+v", signals)
			}
			if started.ProviderConversationID == "" || started.ControllerGeneration != "reserved-generation" {
				t.Fatalf("controller-ready result = %+v", started)
			}
			ready = true
			return chatsvc.ControllerCommit{Conversation: started.Conversation}, nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	controller.Wait()
	if !ready {
		t.Fatal("controller-ready callback was not called")
	}
	for _, signal := range activity.snapshot() {
		if signal.State == domain.ActivityExited && signal.Event == "chat.controller.stopped" {
			return
		}
	}
	t.Fatalf("stream closure was not projected after controller-ready: %+v", activity.snapshot())
}

func TestSwitchControllerReadyLeavesSourceGenerationForAtomicActivation(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	record, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("get source Chat session: found=%v err=%v", found, err)
	}
	record.Harness = domain.HarnessClaudeCode
	record.Metadata.ProviderConversationID = "source-provider"
	record.Metadata.ControllerGeneration = "source-generation"
	if err := st.UpdateSession(ctx, record); err != nil {
		t.Fatalf("seed source Chat owner: %v", err)
	}

	conv := newFakeConversation()
	readyErr := errors.New("stop after source-generation assertion")
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: conv}},
		Activity: &recordingActivity{}, Log: slog.New(slog.DiscardHandler),
		NewID: func() string { return "switch-controller-ready" },
	})

	_, err = svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderScopeID: "switch-1:provider",
		ControllerGeneration: "target-generation",
		ControllerReady: func(chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			current, found, readErr := st.GetSession(ctx, testSession)
			if readErr != nil || !found {
				t.Fatalf("get owner in ControllerReady: found=%v err=%v", found, readErr)
			}
			if current.Metadata.ControllerGeneration != "source-generation" {
				t.Fatalf("ControllerReady preclaimed generation %q, want source-generation", current.Metadata.ControllerGeneration)
			}
			return chatsvc.ControllerCommit{}, readyErr
		},
	})
	if !errors.Is(err, readyErr) {
		t.Fatalf("Start error = %v, want ControllerReady assertion failure", err)
	}
}

func TestControllerReadyDurableSettingsRefreshBeforeFirstDispatch(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	conversation, err := st.CreateConversation(
		ctx, "switch-settings-conversation", domain.ConversationScopeProject,
		testProject, testSession, now,
	)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := st.SetConversationSettings(ctx, conversation.ID, domain.ConversationSettings{
		Model: "source-provider-model", ReasoningEffort: "high",
		ApprovalMode: domain.PermissionModeAcceptEdits,
	}, now); err != nil {
		t.Fatalf("seed source settings: %v", err)
	}

	conv := newFakeConversation()
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			nextID++
			return fmt.Sprintf("switch-settings-%d", nextID)
		},
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	controller, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ControllerGeneration: "target-generation",
		ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			if err := st.SetConversationSettings(ctx, conversation.ID, domain.ConversationSettings{
				ApprovalMode: domain.PermissionModeAcceptEdits,
			}, now.Add(time.Second)); err != nil {
				return chatsvc.ControllerCommit{}, err
			}
			committed := started.Conversation
			committed.Settings = domain.ConversationSettings{ApprovalMode: domain.PermissionModeAcceptEdits}
			committed.UpdatedAt = now.Add(time.Second)
			return chatsvc.ControllerCommit{Conversation: committed}, nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := controller.Send(ctx, ports.ChatUserMessage{
		Text: "continue the handoff", ClientMessageID: "activation-message",
		Origin: domain.MessageOriginAutomation,
	}); err != nil {
		t.Fatalf("Send activation: %v", err)
	}

	sent := conv.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sent))
	}
	if sent[0].Settings.Model != "" || sent[0].Settings.Effort != "" ||
		sent[0].Settings.Approval != domain.PermissionModeAcceptEdits {
		t.Fatalf("activation settings = %+v, want target defaults with preserved approval", sent[0].Settings)
	}
}

type failConversationReadStore struct {
	*sqlite.Store
	reads int
}

func (s *failConversationReadStore) ConversationForSession(
	ctx context.Context,
	session domain.SessionID,
) (domain.ConversationRecord, error) {
	s.reads++
	return domain.ConversationRecord{}, errors.New("injected post-commit conversation read failure")
}

func TestControllerReadyDoesNotDependOnAFalliblePostCommitRead(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)
	conversation, err := st.CreateConversation(
		ctx, "switch-commit-conversation", domain.ConversationScopeProject,
		testProject, testSession, now,
	)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := st.SetConversationSettings(ctx, conversation.ID, domain.ConversationSettings{
		Model: "source-provider-model", ReasoningEffort: "high",
		ApprovalMode: domain.PermissionModeAcceptEdits,
	}, now); err != nil {
		t.Fatalf("seed source settings: %v", err)
	}

	guardedStore := &failConversationReadStore{Store: st}
	conv := newFakeConversation()
	svc := chatsvc.New(chatsvc.Options{
		Store: guardedStore, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return "post-commit-read-id" },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	controller, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ControllerGeneration: "target-generation",
		ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			if err := st.SetConversationSettings(ctx, conversation.ID, domain.ConversationSettings{
				ApprovalMode: domain.PermissionModeAcceptEdits,
			}, now.Add(time.Second)); err != nil {
				return chatsvc.ControllerCommit{}, err
			}
			committed := started.Conversation
			committed.Settings = domain.ConversationSettings{ApprovalMode: domain.PermissionModeAcceptEdits}
			committed.UpdatedAt = now.Add(time.Second)
			return chatsvc.ControllerCommit{Conversation: committed}, nil
		},
	})
	if err != nil {
		t.Fatalf("Start after durable controller commit: %v", err)
	}
	if _, err := controller.Send(ctx, ports.ChatUserMessage{
		Text: "continue the handoff", ClientMessageID: "post-commit-activation",
		Origin: domain.MessageOriginAutomation,
	}); err != nil {
		t.Fatalf("Send activation: %v", err)
	}
	if guardedStore.reads != 0 {
		t.Fatalf("post-commit conversation reads = %d, want none", guardedStore.reads)
	}
	sent := conv.sentMessages()
	if len(sent) != 1 || sent[0].Settings.Model != "" || sent[0].Settings.Effort != "" {
		t.Fatalf("activation retained source settings: %+v", sent)
	}
}

// Dispatch reads the persisted mode. A TUI session must be refused even if a
// controller somehow exists, because the mode is the authority.
func TestSendRefusedForTUISession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	tuiSession := domain.SessionID("p1-2")
	if _, err := h.st.CreateSession(ctx, domain.SessionRecord{
		ID:        tuiSession,
		ProjectID: testProject,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Mode:      domain.SessionModeTUI,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed tui session: %v", err)
	}

	_, err := h.svc.Send(ctx, tuiSession, ports.ChatUserMessage{Text: "hi", ClientMessageID: "c9"})
	if err == nil {
		t.Fatal("a TUI session accepted a chat send")
	}
	if !errorsIs(err, chatsvc.ErrNotChatMode) {
		t.Fatalf("err = %v, want ErrNotChatMode", err)
	}
}

// Every projected event is also archived, so a wrong projection can be repaired
// from the raw record instead of being the only surviving account.
func TestProviderEventsAreArchived(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"},
		ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: "provider-turn-1", ProviderItemID: "msg-1", Delta: "hi"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State.Terminal()
	})

	events, err := h.st.ProviderEventsSince(ctx, h.ctrl.ConversationID(), 0, 100)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("archived %d events, want at least the 3 emitted", len(events))
	}
}

/* ---- the send queue ---------------------------------------------------- */

// turnStateByText is how the queue tests read the timeline: a turn matters here
// only as the fate of one message the user typed.
func turnStateByText(t *testing.T, s store.ConversationSnapshot) map[string]domain.TurnState {
	t.Helper()
	turns := map[string]domain.ConversationTurn{}
	for _, turn := range s.Turns {
		turns[turn.ID] = turn
	}
	states := map[string]domain.TurnState{}
	for _, msg := range s.Messages {
		if msg.Role != domain.MessageRoleUser {
			continue
		}
		if turn, ok := turns[msg.TurnID]; ok {
			states[msg.Text] = turn.State
		}
	}
	return states
}

// The composer tells the user a mid-turn message is queued until the agent
// finishes. That has to be true of the daemon, not just of the placeholder: a
// second turn/start against a busy provider is not a thing the agent can run.
func TestSendWhileBusyQueuesUntilTheTurnEnds(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "first", ClientMessageID: "c1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})

	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "second", ClientMessageID: "c2", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("mid-turn Send: %v", err)
	}
	if queued.State != domain.TurnStateQueued {
		t.Errorf("mid-turn send reported state %q, want queued", queued.State)
	}
	if queued.ProviderTurnID != "" {
		t.Errorf("mid-turn send claimed provider turn %q; it was never dispatched", queued.ProviderTurnID)
	}
	if got := h.conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v; the second message must wait", got)
	}

	// The running turn ends, so the queued message goes out on its own.
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["second"] == domain.TurnStateRunning
	})
	states := turnStateByText(t, snapshot)
	if states["first"] != domain.TurnStateCompleted {
		t.Errorf("first turn = %q, want completed", states["first"])
	}
	if got := h.conv.sentTexts(); len(got) != 2 || got[1] != "second" {
		t.Fatalf("provider received %v, want the queued message dispatched second", got)
	}
}

// Codex can start nested turns while the root turn is still working. A child
// completion is not conversation quiescence: dispatching queued automation at
// that point injects it into the still-running root and leaves the AO turn minted
// for that automation with no matching provider lifecycle.
func TestNestedTurnCompletionDoesNotDrainQueueWhilePrimaryTurnRuns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "root work", ClientMessageID: "c1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send root turn: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
		ProviderConversationID: "thread-1",
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root work"] == domain.TurnStateRunning
	})

	queued, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "queued automation", ClientMessageID: "c2", Origin: domain.MessageOriginAutomation,
	})
	if err != nil {
		t.Fatalf("queue automation: %v", err)
	}
	if queued.State != domain.TurnStateQueued {
		t.Fatalf("automation state = %q, want queued", queued.State)
	}

	h.conv.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-child-1",
			ProviderConversationID: "child-thread-1",
		},
		ports.ChatEvent{
			Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-child-1",
			ProviderConversationID: "child-thread-1", TurnState: domain.TurnStateCompleted,
		},
		// This later root event is a deterministic barrier: once projected, the
		// child's afterProject hook (including any incorrect drain) has finished.
		ports.ChatEvent{
			Kind: ports.ChatEventActivityCompleted, ProviderTurnID: "provider-turn-1",
			ProviderItemID: "root-still-working", ActivityKind: domain.ActivityKindCommand,
			ActivityStatus: domain.ActivityStatusCompleted, Summary: "root continued",
		},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, activity := range s.Activities {
			if activity.ProviderItemID == "root-still-working" {
				return true
			}
		}
		return false
	})
	states := turnStateByText(t, snapshot)
	if states["queued automation"] != domain.TurnStateQueued {
		t.Errorf("automation state after child completion = %q, want queued", states["queued automation"])
	}
	if got := h.conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v; child completion must not drain queued automation", got)
	}
	if got := h.ctrl.State(); got != ports.ChatControllerBusy {
		t.Errorf("controller state after child completion = %q, want busy", got)
	}
	for _, signal := range h.activity.snapshot() {
		if signal.State == domain.ActivityIdle {
			t.Fatalf("child completion reported the session idle: %+v", signal)
		}
	}

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		ProviderConversationID: "thread-1", TurnState: domain.TurnStateCompleted,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["queued automation"] == domain.TurnStateRunning
	})
	if got := h.conv.sentTexts(); len(got) != 2 || got[1] != "queued automation" {
		t.Fatalf("provider received %v, want automation only after root completion", got)
	}
	idleReported := false
	for _, signal := range h.activity.snapshot() {
		idleReported = idleReported || signal.State == domain.ActivityIdle
	}
	if !idleReported {
		t.Fatal("root completion did not report the session idle")
	}
}

// A Chat -> TUI handoff waits for the root turn and everything already queued
// behind it. Nested Codex lifecycle must not make that drain look complete, nor
// may it release the accepted queue into a root turn that is still running.
func TestChatHandoffDrainWaitsForRootAfterNestedTurnCompletes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "root work", ClientMessageID: "handoff-nested-1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send root turn: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1",
		ProviderConversationID: "thread-1",
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root work"] == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "accepted queue", ClientMessageID: "handoff-nested-2", Origin: domain.MessageOriginAutomation,
	}); err != nil {
		t.Fatalf("queue accepted turn: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- h.svc.PrepareChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionDrain)
	}()

	h.conv.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-child-1",
			ProviderConversationID: "child-thread-1",
		},
		ports.ChatEvent{
			Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-child-1",
			ProviderConversationID: "child-thread-1", TurnState: domain.TurnStateCompleted,
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityCompleted, ProviderTurnID: "provider-turn-1",
			ProviderItemID: "handoff-root-still-working", ActivityKind: domain.ActivityKindCommand,
			ActivityStatus: domain.ActivityStatusCompleted, Summary: "root continued",
		},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, activity := range s.Activities {
			if activity.ProviderItemID == "handoff-root-still-working" {
				return true
			}
		}
		return false
	})

	select {
	case err := <-done:
		t.Fatalf("handoff finished after child completion while root was active: %v", err)
	default:
	}
	if got := h.conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v; nested completion released the accepted queue", got)
	}

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		ProviderConversationID: "thread-1", TurnState: domain.TurnStateCompleted,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["accepted queue"] == domain.TurnStateRunning
	})
	select {
	case err := <-done:
		t.Fatalf("handoff finished before its accepted queue completed: %v", err)
	default:
	}

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-2",
		ProviderConversationID: "thread-1", TurnState: domain.TurnStateCompleted,
	})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("prepare handoff: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handoff did not become quiescent after the root and accepted queue completed")
	}
}

// Stop is a brake. Releasing the queue when the user presses it would be the
// opposite of what the button says, so anything waiting is cancelled with the turn.
func TestInterruptCancelsWhatIsQueuedBehindTheTurn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "c1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "queued", ClientMessageID: "c2",
	}); err != nil {
		t.Fatalf("mid-turn Send: %v", err)
	}

	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateInterrupted,
	})

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["queued"].Terminal() && states["running"].Terminal()
	})
	states := turnStateByText(t, snapshot)
	if states["queued"] != domain.TurnStateInterrupted {
		t.Errorf("queued turn = %q; a message never dispatched did not fail, it was cancelled",
			states["queued"])
	}
	if got := h.conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v; stop must not release the queue", got)
	}
}

func TestChatHandoffDrainFinishesAcceptedQueueAndClosesNewIntake(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "handoff-1",
	}); err != nil {
		t.Fatalf("send running turn: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "already queued", ClientMessageID: "handoff-2",
	}); err != nil {
		t.Fatalf("queue second turn: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- h.svc.PrepareChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionDrain)
	}()
	// Completion of the first accepted turn must dispatch the accepted queue,
	// rather than declaring the source quiescent early.
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["already queued"] == domain.TurnStateRunning
	})
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-2",
		TurnState: domain.TurnStateCompleted,
	})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("prepare handoff: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handoff did not become quiescent after its accepted queue completed")
	}

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "too late", ClientMessageID: "handoff-3",
	}); !errors.Is(err, chatsvc.ErrControllerHandoff) {
		t.Fatalf("send after handoff gate = %v, want ErrControllerHandoff", err)
	}
	h.svc.AbortChatHandoff(testSession)
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "source reopened", ClientMessageID: "handoff-4",
	}); err != nil {
		t.Fatalf("send after aborting handoff: %v", err)
	}
}

func TestChatHandoffInterruptArmBlocksCompletionFromPromotingQueue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "run the dev server", ClientMessageID: "handoff-arm-1",
	}); err != nil {
		t.Fatalf("send running turn: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "touch /tmp/ao3945-queued-ran", ClientMessageID: "handoff-arm-2",
	}); err != nil {
		t.Fatalf("queue second turn: %v", err)
	}
	// Once intake is fenced, eventual cancellation must cover the whole queue
	// rather than trusting wall-clock ordering. A host clock correction cannot
	// make an older accepted command look newer than the handoff cutoff.
	h.advance(-time.Hour)

	// Session Manager performs this fast step before returning the accepted
	// transition and before target preflight. It is a reversible dispatch fence:
	// the active turn can finish, but its completion must not release the queue.
	if err := h.svc.ArmChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionInterrupt); err != nil {
		t.Fatalf("arm interrupt handoff: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateInterrupted,
	})
	fenced := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["run the dev server"].Terminal()
	})
	if got := turnStateByText(t, fenced)["touch /tmp/ao3945-queued-ran"]; got != domain.TurnStateQueued {
		t.Fatalf("queued command while target preflights = %q, want reversibly fenced", got)
	}
	if got := h.conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v before handoff preparation; armed completion released the queue", got)
	}

	// Target preflight has now succeeded. Preparation makes the fence durable
	// before touching the provider and remains independent of clock movement.
	if err := h.svc.PrepareChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionInterrupt); err != nil {
		t.Fatalf("prepare armed handoff: %v", err)
	}
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("stop source controller: %v", err)
	}

	if got := h.conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v; the queued command crossed the armed handoff", got)
	}
	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load stopped conversation: %v", err)
	}
	if got := turnStateByText(t, snapshot)["touch /tmp/ao3945-queued-ran"]; got != domain.TurnStateInterrupted {
		t.Fatalf("queued command = %q, want interrupted without provider dispatch", got)
	}
}

func TestChatHandoffInterruptAbortAfterPreflightFailureResumesFencedQueue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "run the dev server", ClientMessageID: "handoff-abort-1",
	}); err != nil {
		t.Fatalf("send running turn: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "queued work survives preflight", ClientMessageID: "handoff-abort-2",
	}); err != nil {
		t.Fatalf("queue second turn: %v", err)
	}
	if err := h.svc.ArmChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionInterrupt); err != nil {
		t.Fatalf("arm interrupt handoff: %v", err)
	}

	// Completion while target preflight is running cannot dispatch the queue.
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["run the dev server"] == domain.TurnStateCompleted &&
			states["queued work survives preflight"] == domain.TurnStateQueued
	})
	if got := h.conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v while target preflight was fenced", got)
	}

	// A failed target preflight reopens the source and explicitly resumes the
	// queue; there may be no later completion event to trigger this drain.
	h.svc.AbortChatHandoff(testSession)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["queued work survives preflight"] == domain.TurnStateRunning
	})
	if got := h.conv.sentTexts(); len(got) != 2 || got[1] != "queued work survives preflight" {
		t.Fatalf("provider received %v after abort, want preserved queue to resume", got)
	}
}

type completionOnInterruptConversation struct{ *fakeConversation }

func (c *completionOnInterruptConversation) Interrupt(_ context.Context, turn string) error {
	// ACP completes its prompt goroutine from the same cancellation handshake;
	// this event can therefore be queued before session/cancel returns.
	c.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn,
		TurnState: domain.TurnStateInterrupted,
	})
	return nil
}

func TestChatHandoffInterruptCompletionDuringProviderCancellationCannotPromoteQueue(t *testing.T) {
	provider := &completionOnInterruptConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, provider)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "run the dev server", ClientMessageID: "handoff-during-1",
	}); err != nil {
		t.Fatalf("send running turn: %v", err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "touch /tmp/ao3945-queued-ran", ClientMessageID: "handoff-during-2",
	}); err != nil {
		t.Fatalf("queue second turn: %v", err)
	}

	if err := h.svc.PrepareChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionInterrupt); err != nil {
		t.Fatalf("prepare interrupt handoff: %v", err)
	}
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("stop source controller: %v", err)
	}
	if got := provider.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v; ACP-style completion promoted the queue", got)
	}
}

func TestChatHandoffInterruptDoesNotWaitForTurnCompletion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "run the dev server", ClientMessageID: "handoff-interrupt-1",
	}); err != nil {
		t.Fatalf("send running turn: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "queued behind it", ClientMessageID: "handoff-interrupt-2",
	}); err != nil {
		t.Fatalf("queue second turn: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- h.svc.PrepareChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionInterrupt)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("prepare interrupt handoff: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		// Unblock the old implementation before failing so the test leaves no
		// handoff goroutine behind. A force switch must not need this completion.
		h.conv.emit(ports.ChatEvent{
			Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
			TurnState: domain.TurnStateInterrupted,
		})
		<-done
		t.Fatal("interrupt handoff waited for the running turn to report completion")
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["queued behind it"].Terminal()
	})
	if got := turnStateByText(t, snapshot)["queued behind it"]; got != domain.TurnStateInterrupted {
		t.Fatalf("queued turn = %q, want interrupted before the source controller stops", got)
	}
	// Codex may deliver turn/completed after turn/interrupt has already answered.
	// The armed dispatch gate must still make that late completion a no-op for the
	// queue.
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateInterrupted,
	})
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("stop armed controller: %v", err)
	}
	if got := h.conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v; late completion promoted an interrupted queue", got)
	}
}

type failFirstQueueCancellationStore struct {
	*sqlite.Store

	mu    sync.Mutex
	calls int
}

func (s *failFirstQueueCancellationStore) CancelAllQueuedTurns(
	ctx context.Context,
	conversationID string,
	now time.Time,
) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		return errors.New("injected queue cancellation failure")
	}
	return s.Store.CancelAllQueuedTurns(ctx, conversationID, now)
}

func (s *failFirstQueueCancellationStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestChatHandoffInterruptDoesNotReachProviderUntilQueueCancellationSucceeds(t *testing.T) {
	provider := newInterruptRecorder()
	var flakyStore *failFirstQueueCancellationStore
	h := newHarnessWithConversationAndStore(t, provider, func(st *sqlite.Store) chatsvc.Store {
		flakyStore = &failFirstQueueCancellationStore{Store: st}
		return flakyStore
	})
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "run the dev server", ClientMessageID: "handoff-race-1",
	}); err != nil {
		t.Fatalf("send running turn: %v", err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	provider.markActive("provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "queued behind it", ClientMessageID: "handoff-race-2",
	}); err != nil {
		t.Fatalf("queue second turn: %v", err)
	}

	if err := h.svc.ArmChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionInterrupt); err != nil {
		t.Fatalf("arm reversible handoff fence: %v", err)
	}
	if got := flakyStore.callCount(); got != 0 {
		t.Fatalf("queue cancellation calls during reversible arm = %d, want 0", got)
	}
	if err := h.svc.PrepareChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionInterrupt); err == nil {
		t.Fatal("first handoff preparation error = nil, want injected queue cancellation failure")
	}
	if got := provider.attemptCount(); got != 0 {
		t.Fatalf("provider interrupt attempts = %d before durable queue cancellation, want 0", got)
	}

	// Preparation aborts the reversible fence after a local-store failure. A
	// caller may re-arm and retry; provider I/O begins only after the durable queue
	// is terminal.
	if err := h.svc.ArmChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionInterrupt); err != nil {
		t.Fatalf("re-arm handoff after preparation failure: %v", err)
	}
	if err := h.svc.PrepareChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionInterrupt); err != nil {
		t.Fatalf("prepare armed interrupt handoff: %v", err)
	}
	if got := flakyStore.callCount(); got != 2 {
		t.Fatalf("queue cancellation calls = %d, want one failed arm and one successful retry", got)
	}
	if got := provider.attemptCount(); got != 1 {
		t.Fatalf("provider interrupt attempts = %d, want 1 after queue cancellation", got)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["queued behind it"].Terminal()
	})
	if got := turnStateByText(t, snapshot)["queued behind it"]; got != domain.TurnStateInterrupted {
		t.Fatalf("queued turn = %q, want interrupted before provider cancellation", got)
	}
}

func TestChatHandoffTreatsMissingControllerAsAlreadyQuiescent(t *testing.T) {
	svc := chatsvc.New(chatsvc.Options{})
	if err := svc.PrepareChatHandoff(
		context.Background(), "missing-controller", domain.SessionInterfaceTransitionDrain,
	); err != nil {
		t.Fatalf("prepare missing controller: %v", err)
	}
}

func TestServiceStopRetainsControllerUntilItsEventStreamActuallyEnds(t *testing.T) {
	base := newFakeConversation()
	h := newHarnessWithConversation(t, &stuckConversation{
		fakeConversation: base,
		closeErr:         errors.New("provider close failed"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := h.svc.Stop(ctx, testSession); err == nil {
		t.Fatal("Stop error = nil, want provider close failure or deadline")
	}
	if _, err := h.svc.Controller(testSession); err != nil {
		t.Fatalf("controller was forgotten while its stream was still live: %v", err)
	}
	if !h.svc.HasLiveChatController(testSession) {
		t.Fatal("live-controller guard cleared before the provider stream ended")
	}

	base.closeOnce.Do(func() { close(base.events) })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := h.svc.Controller(testSession); errors.Is(err, chatsvc.ErrNoController) {
			if h.svc.HasLiveChatController(testSession) {
				t.Fatal("live-controller guard remained set after registry release")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("controller registry did not release the stopped stream")
}

func TestServiceStopTerminatesPersistentConversation(t *testing.T) {
	provider := &terminatingConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, provider)
	if err := h.svc.Stop(context.Background(), testSession); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !provider.terminated.Load() {
		t.Fatal("explicit session stop detached persistent conversation instead of terminating it")
	}
}

func TestServiceStopAllOnlyDetachesPersistentConversation(t *testing.T) {
	provider := &terminatingConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, provider)
	turn, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "keep working"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID,
		ProviderConversationID: provider.ProviderConversationID(),
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, candidate := range s.Turns {
			if candidate.ID == turn.ID {
				return candidate.State == domain.TurnStateRunning
			}
		}
		return false
	})
	h.svc.StopAll(context.Background())
	if provider.terminated.Load() {
		t.Fatal("daemon-wide shutdown terminated persistent conversation")
	}
	rec, found, err := h.st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	if rec.Activity.State == domain.ActivityExited {
		t.Fatal("daemon detach projected a false provider exit")
	}
	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range snapshot.Turns {
		if candidate.ID == turn.ID && candidate.State != domain.TurnStateRunning {
			t.Fatalf("in-flight turn settled on daemon detach: %s", candidate.State)
		}
	}
}

func TestServiceLiveReconnectSkipsSettledHistoryBarrier(t *testing.T) {
	st := openStore(t)
	native := &nativeHistoryConversation{fakeConversation: newFakeConversation(), err: ports.ErrChatHistoryUnsettled}
	provider := &liveReconnectedConversation{nativeHistoryConversation: native}
	var prepareCalls atomic.Int32
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Reader: fullSnapshotReader(st), Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: provider}},
		Log: slog.New(slog.DiscardHandler), NewID: func() string { return "live-reconnect-id" },
	})
	if _, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1",
		PrepareControllerEnv: func(context.Context, domain.SessionControllerOwner) (map[string]string, error) {
			prepareCalls.Add(1)
			return map[string]string{"AO_BROWSER_CAPABILITY": "rotated"}, nil
		},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer svc.StopAll(context.Background())
	if reads := native.historyReads(); reads != 0 {
		t.Fatalf("native history reads = %d, live reconnect must not wait for active turn to settle", reads)
	}
	if got := prepareCalls.Load(); got != 0 {
		t.Fatalf("live reconnect rotated launch-only credentials %d times, want 0", got)
	}
}

func TestServiceLiveReconnectKeepsDurableRunningTurnBusy(t *testing.T) {
	st := openStore(t)
	reader := fullSnapshotReader(st)
	var ids atomic.Int32
	newID := func() string { return fmt.Sprintf("reconnect-%d", ids.Add(1)) }
	firstProvider := &terminatingConversation{fakeConversation: newFakeConversation()}
	first := chatsvc.New(chatsvc.Options{
		Store: st, Reader: reader, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: firstProvider}},
		Log:     slog.New(slog.DiscardHandler), NewID: newID,
	})
	firstController, err := first.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	turn, err := firstController.Send(context.Background(), ports.ChatUserMessage{Text: "keep working"})
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	firstProvider.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID,
		ProviderConversationID: firstProvider.ProviderConversationID(),
	})
	h := &harness{st: st, ctrl: firstController}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, candidate := range s.Turns {
			if candidate.ID == turn.ID {
				return candidate.State == domain.TurnStateRunning
			}
		}
		return false
	})
	first.StopAll(context.Background())

	secondProvider := &liveReconnectedConversation{nativeHistoryConversation: &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
	}}
	second := chatsvc.New(chatsvc.Options{
		Store: st, Reader: reader, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: secondProvider}},
		Log:     slog.New(slog.DiscardHandler), NewID: newID,
	})
	t.Cleanup(func() { second.StopAll(context.Background()) })
	secondController, err := second.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: firstProvider.ProviderConversationID(),
	})
	if err != nil {
		t.Fatalf("reconnect Start: %v", err)
	}
	queued, err := secondController.Send(context.Background(), ports.ChatUserMessage{Text: "after restart"})
	if err != nil {
		t.Fatalf("reconnect Send: %v", err)
	}
	if queued.State != domain.TurnStateQueued {
		t.Fatalf("reconnect Send state = %s, want queued behind surviving turn", queued.State)
	}
	if got := secondProvider.sentTexts(); len(got) != 0 {
		t.Fatalf("reconnected provider received concurrent turns: %v", got)
	}
}

func TestStartWaitsForStoppedControllerCleanupBeforeRelaunch(t *testing.T) {
	st := openStore(t)
	first := newFakeConversation()
	second := newFakeConversation()
	driver := &sequenceDriver{conversations: []ports.ChatConversation{first, second}}
	var idMu sync.Mutex
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: driver},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			nextID++
			return fmt.Sprintf("relaunch-id-%d", nextID)
		},
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })

	firstController, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	first.emit(ports.ChatEvent{
		Kind:            ports.ChatEventControllerState,
		ControllerState: ports.ChatControllerStopped,
	})
	deadline := time.Now().Add(time.Second)
	for firstController.State() != ports.ChatControllerStopped && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := firstController.State(); got != ports.ChatControllerStopped {
		t.Fatalf("first controller state = %q, want stopped", got)
	}
	if svc.HasLiveChatController(testSession) {
		t.Fatal("stopped controller reported live")
	}

	replacementWorkspace := t.TempDir()
	waitCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	controller, err := svc.Start(waitCtx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: replacementWorkspace, ProviderConversationID: "thread-1",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start while stopped controller was cleaning up = controller=%p err=%v, want deadline", controller, err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first conversation: %v", err)
	}
	firstController.Wait()
	replacement, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: replacementWorkspace, ProviderConversationID: "thread-1",
	})
	if err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	if replacement == firstController {
		t.Fatal("relaunch returned the stopped controller")
	}
}

func TestConcurrentReconcileAndResumeShareOneCredentialedControllerLaunch(t *testing.T) {
	st := openStore(t)
	conversation := newFakeConversation()
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	var providerOnce sync.Once
	var providerStarts atomic.Int32
	var launchedEnv map[string]string
	driver := fakeDriver{conv: conversation}
	driver.start = func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
		providerStarts.Add(1)
		launchedEnv = maps.Clone(cfg.Env)
		providerOnce.Do(func() { close(providerStarted) })
		<-releaseProvider
		return conversation, nil
	}

	var prepared atomic.Int32
	var ids atomic.Int32
	prepare := func(context.Context, domain.SessionControllerOwner) (map[string]string, error) {
		call := prepared.Add(1)
		return map[string]string{"AO_BROWSER_CAPABILITY": fmt.Sprintf("token-%d", call)}, nil
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st, Drivers: fakeRegistry{driver: driver},
		Log: slog.New(slog.DiscardHandler), NewID: func() string { return fmt.Sprintf("gate-id-%d", ids.Add(1)) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	cfg := chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), Env: map[string]string{"AO_BROWSER_CAPABILITY": "stale"},
		PrepareControllerEnv: prepare,
	}
	type startResult struct {
		controller *chatsvc.Controller
		err        error
	}
	results := make(chan startResult, 2)
	go func() {
		controller, err := svc.Start(context.Background(), cfg)
		results <- startResult{controller, err}
	}()
	<-providerStarted
	secondEntered := make(chan struct{})
	go func() {
		close(secondEntered)
		controller, err := svc.Start(context.Background(), cfg)
		results <- startResult{controller, err}
	}()
	<-secondEntered
	select {
	case result := <-results:
		t.Fatalf("a concurrent Start escaped the controller gate: controller=%p err=%v", result.controller, result.err)
	case <-time.After(25 * time.Millisecond):
	}
	if got := prepared.Load(); got != 1 {
		t.Fatalf("credential preparations while first launch is blocked = %d, want 1", got)
	}
	close(releaseProvider)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil || first.controller != second.controller {
		t.Fatalf("concurrent Starts = (%p, %v), (%p, %v), want the same live controller",
			first.controller, first.err, second.controller, second.err)
	}
	if got := providerStarts.Load(); got != 1 {
		t.Fatalf("provider starts = %d, want 1", got)
	}
	if got := launchedEnv["AO_BROWSER_CAPABILITY"]; got != "token-1" {
		t.Fatalf("provider capability = %q, want token-1", got)
	}
}

// The cancellation belongs to the moment stop was pressed. A message typed after
// that is the user asking for new work, and must not be swept up by it.
func TestMessageTypedAfterStopIsStillDelivered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "c1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "before stop", ClientMessageID: "c2",
	}); err != nil {
		t.Fatalf("Send before stop: %v", err)
	}

	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// The user changes their mind and types again while the interrupt lands.
	h.advance(time.Second)
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "after stop", ClientMessageID: "c3",
	}); err != nil {
		t.Fatalf("Send after stop: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateInterrupted,
	})

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["after stop"] == domain.TurnStateRunning
	})
	states := turnStateByText(t, snapshot)
	if states["before stop"] != domain.TurnStateInterrupted {
		t.Errorf("pre-stop message = %q, want interrupted", states["before stop"])
	}
	if got := h.conv.sentTexts(); len(got) != 2 || got[1] != "after stop" {
		t.Fatalf("provider received %v, want the post-stop message delivered", got)
	}
}

func errorsIs(err, target error) bool {
	for err != nil {
		if errors.Is(err, target) {
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

// The initial prompt is the user's task brief, so it must render as their message
// rather than as a system notice. Origin records who authored a message, not who
// delivered it to the provider.
func TestInitialPromptIsAttributedToTheUser(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.StartChatTurn(ctx, testSession, "Explain the whole design system"); err != nil {
		t.Fatalf("StartChatTurn: %v", err)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Messages) >= 1
	})
	first := snapshot.Messages[0]
	if first.Origin != domain.MessageOriginHuman {
		t.Fatalf("initial prompt origin = %q, want %q — the daemon delivers it but the user wrote it",
			first.Origin, domain.MessageOriginHuman)
	}
	if first.Role != domain.MessageRoleUser {
		t.Errorf("initial prompt role = %q, want user", first.Role)
	}
}

// A relayed message is AO carrying someone else's words: `ao send`, or an
// orchestrator writing to a worker. It must be attributed to automation, not
// passed off as something the user typed here — the timeline distinguishes the
// two structurally, and a reader should never have to infer it from a prefix.
func TestRelayedMessageIsAttributedToAutomation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.RelayChatTurn(ctx, testSession, "orchestrator: rebase onto main"); err != nil {
		t.Fatalf("RelayChatTurn: %v", err)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Messages) >= 1
	})
	msg := snapshot.Messages[0]
	if msg.Origin != domain.MessageOriginAutomation {
		t.Fatalf("relay origin = %q, want %q", msg.Origin, domain.MessageOriginAutomation)
	}
	if msg.Role != domain.MessageRoleUser {
		t.Errorf("relay role = %q; a relay is still an inbound request", msg.Role)
	}
	if got := h.conv.sentTexts(); len(got) != 1 || got[0] != "orchestrator: rebase onto main" {
		t.Fatalf("provider received %v, want the relayed text dispatched", got)
	}
}

// interruptRecorder answers turn/interrupt the way the provider does: it refuses
// any turn it has not been told to consider active.
type interruptRecorder struct {
	*fakeConversation
	activeMu sync.Mutex
	active   map[string]bool
	attempts []string
}

// blockingDispatchConversation exposes the interval in which Send owns sendMu
// but has not yet published pendingTurnID. Stop must wait for dispatch and then
// retain the normal provider-ack grace period.
type blockingDispatchConversation struct {
	*interruptRecorder
	sendStarted chan struct{}
	releaseSend chan struct{}
	attempted   chan struct{}
	startOnce   sync.Once
	attemptOnce sync.Once
}

func newBlockingDispatchConversation() *blockingDispatchConversation {
	return &blockingDispatchConversation{
		interruptRecorder: newInterruptRecorder(),
		sendStarted:       make(chan struct{}),
		releaseSend:       make(chan struct{}),
		attempted:         make(chan struct{}),
	}
}

func (c *blockingDispatchConversation) SendTurn(
	ctx context.Context,
	msg ports.ChatUserMessage,
) (ports.ChatTurnRef, error) {
	c.startOnce.Do(func() { close(c.sendStarted) })
	select {
	case <-c.releaseSend:
		return c.fakeConversation.SendTurn(ctx, msg)
	case <-ctx.Done():
		return ports.ChatTurnRef{}, ctx.Err()
	}
}

func (c *blockingDispatchConversation) Interrupt(ctx context.Context, turn string) error {
	c.attemptOnce.Do(func() { close(c.attempted) })
	return c.interruptRecorder.Interrupt(ctx, turn)
}

// blockingInterruptRefusalConversation holds the provider refusal open so a
// message can arrive after Stop began but before reconciliation runs.
type blockingInterruptRefusalConversation struct {
	*fakeConversation
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newBlockingInterruptRefusalConversation() *blockingInterruptRefusalConversation {
	return &blockingInterruptRefusalConversation{
		fakeConversation: newFakeConversation(),
		started:          make(chan struct{}),
		release:          make(chan struct{}),
	}
}

func (c *blockingInterruptRefusalConversation) Interrupt(ctx context.Context, _ string) error {
	c.startedOnce.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return ports.ErrChatNoActiveTurn
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *blockingInterruptRefusalConversation) unblock() {
	c.releaseOnce.Do(func() { close(c.release) })
}

// completionBeforeRefusalConversation lets the committed completion win the
// controller's send lock before the provider's stale refusal reaches Interrupt.
type completionBeforeRefusalConversation struct {
	*fakeConversation
	emitted     chan struct{}
	release     chan struct{}
	emittedOnce sync.Once
	releaseOnce sync.Once
}

func newCompletionBeforeRefusalConversation() *completionBeforeRefusalConversation {
	return &completionBeforeRefusalConversation{
		fakeConversation: newFakeConversation(),
		emitted:          make(chan struct{}),
		release:          make(chan struct{}),
	}
}

func (c *completionBeforeRefusalConversation) Interrupt(ctx context.Context, turn string) error {
	c.emittedOnce.Do(func() {
		c.emit(ports.ChatEvent{
			Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn,
			TurnState: domain.TurnStateCompleted,
		})
		close(c.emitted)
	})
	select {
	case <-c.release:
		return ports.ErrChatNoActiveTurn
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *completionBeforeRefusalConversation) unblock() {
	c.releaseOnce.Do(func() { close(c.release) })
}

func newInterruptRecorder() *interruptRecorder {
	return &interruptRecorder{fakeConversation: newFakeConversation(), active: map[string]bool{}}
}

func (r *interruptRecorder) markActive(turn string) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	r.active[turn] = true
}

func (r *interruptRecorder) Interrupt(_ context.Context, turn string) error {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	r.attempts = append(r.attempts, turn)
	if !r.active[turn] {
		return ports.ErrChatNoActiveTurn
	}
	return nil
}

func (r *interruptRecorder) attemptCount() int {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	return len(r.attempts)
}

// Stop appears the moment a message is sent, so a user can press it before the
// provider has acknowledged the turn — and a provider refuses to cancel a turn it
// does not yet consider active. Interrupt waits out that gap rather than handing
// back a failure in the exact moment someone realizes they sent the wrong thing.
func TestInterruptWaitsForTheProviderToAcknowledgeTheTurn(t *testing.T) {
	conv := newInterruptRecorder()
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// The acknowledgement lands while Interrupt is already waiting.
	go func() {
		time.Sleep(150 * time.Millisecond)
		conv.markActive("provider-turn-1")
		conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	}()

	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatalf("Interrupt raced the provider's acknowledgement: %v", err)
	}
	if got := conv.attemptCount(); got != 1 {
		t.Errorf("interrupt attempts = %d, want 1", got)
	}
}

// Interrupt can begin while Send still owns the dispatch lock. Once dispatch
// finishes, Stop must still wait for turn-started rather than treating the
// provider's early refusal as proof that the new turn is stale.
func TestInterruptWaitsForAcknowledgementAfterRacingDispatch(t *testing.T) {
	conv := newBlockingDispatchConversation()
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	sendDone := make(chan error, 1)
	go func() {
		_, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
			Text: "go", ClientMessageID: "dispatch-race",
		})
		sendDone <- err
	}()
	select {
	case <-conv.sendStarted:
	case <-time.After(4 * time.Second):
		t.Fatal("Send did not enter provider dispatch")
	}

	interruptDone := make(chan error, 1)
	go func() { interruptDone <- h.svc.Interrupt(ctx, testSession) }()
	close(conv.releaseSend)
	if err := <-sendDone; err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case <-conv.attempted:
		t.Fatal("Interrupt reached the provider before turn-started acknowledgement")
	case <-time.After(100 * time.Millisecond):
	}
	conv.markActive("provider-turn-1")
	conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt after acknowledgement: %v", err)
	}
}

// A provider that refuses because the turn is genuinely gone must not strand the
// user: the durable row still says running — that is what the UI renders — so
// Interrupt reconciles it as interrupted instead of answering "nothing to stop"
// while the Working bar stays up.
func TestProviderRefusalReconcilesTheDurableTurnAsInterrupted(t *testing.T) {
	conv := newInterruptRecorder() // never marks anything active
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})

	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateInterrupted
	})
	if got := snapshot.Turns[0].ErrorMessage; got != "" {
		t.Errorf("interrupted turn error message = %q, want empty", got)
	}
	if !hasActivitySignal(h.activity.snapshot(), domain.ActivityIdle, "chat.interrupt.reconciled") {
		t.Errorf("no idle signal after reconciliation; signals = %v", h.activity.snapshot())
	}
}

// The recovery path must keep the same cutoff as the Stop request. The composer
// remains available while provider cancellation is pending, and a later prompt
// is new user intent rather than part of the queue that Stop was asked to clear.
func TestProviderRefusalPreservesMessageQueuedAfterStop(t *testing.T) {
	conv := newBlockingInterruptRefusalConversation()
	t.Cleanup(conv.unblock)
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "c1",
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "before stop", ClientMessageID: "c2",
	}); err != nil {
		t.Fatalf("Send before stop: %v", err)
	}

	interruptDone := make(chan error, 1)
	go func() { interruptDone <- h.svc.Interrupt(ctx, testSession) }()
	select {
	case <-conv.started:
	case <-time.After(4 * time.Second):
		t.Fatal("provider interrupt did not start")
	}

	h.advance(time.Second)
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "after stop", ClientMessageID: "c3",
	}); err != nil {
		t.Fatalf("Send after stop: %v", err)
	}
	conv.unblock()
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["before stop"] == domain.TurnStateInterrupted &&
			states["after stop"] != domain.TurnStateQueued
	})
	states := turnStateByText(t, snapshot)
	if got := states["after stop"]; got != domain.TurnStateRunning {
		t.Fatalf("post-stop message = %q, want running", got)
	}
	if got := conv.sentTexts(); len(got) != 2 || got[1] != "after stop" {
		t.Fatalf("provider received %v, want the post-stop message delivered", got)
	}
}

// A provider can publish completion just before its interrupt call reports that
// no active turn remains. If that completion commits first, reconciliation must
// not overwrite it or the queue transition it already performed.
func TestProviderRefusalDoesNotOverwriteCommittedCompletion(t *testing.T) {
	conv := newCompletionBeforeRefusalConversation()
	t.Cleanup(conv.unblock)
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "c1",
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "before stop", ClientMessageID: "c2",
	}); err != nil {
		t.Fatalf("Send before stop: %v", err)
	}

	interruptDone := make(chan error, 1)
	go func() { interruptDone <- h.svc.Interrupt(ctx, testSession) }()
	select {
	case <-conv.emitted:
	case <-time.After(4 * time.Second):
		t.Fatal("provider completion was not emitted")
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["running"] == domain.TurnStateCompleted &&
			states["before stop"] == domain.TurnStateInterrupted
	})

	conv.unblock()
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["running"].Terminal()
	})
	if got := turnStateByText(t, snapshot)["running"]; got != domain.TurnStateCompleted {
		t.Fatalf("turn after committed completion = %q, want completed", got)
	}
}

func hasActivitySignal(signals []ports.ActivitySignal, state domain.ActivityState, event string) bool {
	for _, s := range signals {
		if s.State == state && s.Event == event {
			return true
		}
	}
	return false
}

func countActivitySignals(signals []ports.ActivitySignal, state domain.ActivityState, event string) int {
	count := 0
	for _, signal := range signals {
		if signal.State == state && signal.Event == event {
			count++
		}
	}
	return count
}

// The #3749 desync: a turn is 'running' on disk while the in-memory controller
// has no pendingTurnID (a completed projection failed, or the daemon restarted
// mid-turn). The UI reads disk and shows "Working"; Stop must act on what the
// user sees rather than refuse with CHAT_NO_ACTIVE_TURN.
func TestInterruptReconcilesStaleRunningTurnOnDisk(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Create the running turn directly in the store, bypassing the controller so
	// its pendingTurnID stays empty — the desynced state.
	const providerTurnID = "stale-running-turn"
	if err := h.st.AdoptProviderTurn(ctx, h.ctrl.ConversationID(), testSession,
		h.ctrl.Generation(), "stale-turn-id", providerTurnID, h.now()); err != nil {
		t.Fatalf("AdoptProviderTurn: %v", err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})

	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateInterrupted
	})
	if !hasActivitySignal(h.activity.snapshot(), domain.ActivityIdle, "chat.interrupt.reconciled") {
		t.Errorf("no idle signal after reconciliation; signals = %v", h.activity.snapshot())
	}
}

// A root and a nested provider turn can both be durably running. The UI renders
// the oldest visible row first, but clearing only that row would merely reveal a
// second Working bar. Recovery cancels the visible root and settles the full
// visible running set.
func TestInterruptReconcilesAllVisibleRunningTurns(t *testing.T) {
	conv := newInterruptRecorder()
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	if err := h.st.AdoptProviderTurn(ctx, h.ctrl.ConversationID(), testSession,
		h.ctrl.Generation(), "root-turn", "provider-root", h.now()); err != nil {
		t.Fatalf("AdoptProviderTurn root: %v", err)
	}
	h.advance(time.Second)
	if err := h.st.AdoptProviderTurn(ctx, h.ctrl.ConversationID(), testSession,
		h.ctrl.Generation(), "child-turn", "provider-child", h.now()); err != nil {
		t.Fatalf("AdoptProviderTurn child: %v", err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 2 && s.Turns[0].State == domain.TurnStateRunning &&
			s.Turns[1].State == domain.TurnStateRunning
	})

	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 2 && s.Turns[0].State.Terminal() && s.Turns[1].State.Terminal()
	})
	for _, turn := range snapshot.Turns {
		if turn.State != domain.TurnStateInterrupted {
			t.Errorf("turn %s = %q, want interrupted", turn.ProviderTurnID, turn.State)
		}
	}
	conv.activeMu.Lock()
	attempts := append([]string(nil), conv.attempts...)
	conv.activeMu.Unlock()
	if len(attempts) != 1 || attempts[0] != "provider-root" {
		t.Fatalf("provider interrupt attempts = %v, want visible root first", attempts)
	}
}

// In the no-memory recovery path, provider cancellation happens under sendMu.
// A prompt submitted after Stop therefore waits for stale settlement and then
// starts as new work instead of replacing the recovery target.
func TestInterruptDurableFallbackPreservesPostStopSend(t *testing.T) {
	conv := newBlockingInterruptRefusalConversation()
	t.Cleanup(conv.unblock)
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	if err := h.st.AdoptProviderTurn(ctx, h.ctrl.ConversationID(), testSession,
		h.ctrl.Generation(), "stale-turn", "provider-stale", h.now()); err != nil {
		t.Fatalf("AdoptProviderTurn: %v", err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})

	interruptDone := make(chan error, 1)
	go func() { interruptDone <- h.svc.Interrupt(ctx, testSession) }()
	select {
	case <-conv.started:
	case <-time.After(4 * time.Second):
		t.Fatal("durable fallback did not reach provider interrupt")
	}

	h.advance(time.Second)
	sendDone := make(chan error, 1)
	go func() {
		_, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
			Text: "after stop", ClientMessageID: "post-stop-fallback",
		})
		sendDone <- err
	}()
	select {
	case err := <-sendDone:
		t.Fatalf("post-Stop Send crossed reconciliation lock early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	conv.unblock()
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if err := <-sendDone; err != nil {
		t.Fatalf("post-Stop Send: %v", err)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 2 && s.Turns[0].State == domain.TurnStateInterrupted &&
			s.Turns[1].State == domain.TurnStateRunning
	})
	if snapshot.Turns[0].ProviderTurnID != "provider-stale" {
		t.Fatalf("reconciled turn = %q, want provider-stale", snapshot.Turns[0].ProviderTurnID)
	}
	if got := conv.sentTexts(); len(got) != 1 || got[0] != "after stop" {
		t.Fatalf("provider received %v, want only post-Stop work", got)
	}
}

// When memory and disk agree there is nothing running, Interrupt must still
// answer ErrNoActiveTurn — the disk fallback must not invent work to stop.
func TestInterruptReturnsNoActiveTurnWhenNoRunningTurnAnywhere(t *testing.T) {
	h := newHarness(t)

	err := h.svc.Interrupt(context.Background(), testSession)
	if !errorsIs(err, chatsvc.ErrNoActiveTurn) {
		t.Fatalf("err = %v, want ErrNoActiveTurn", err)
	}
}

// Reconciliation is still the user's brake: anything queued behind the stale
// turn is cancelled, not released into the provider.
func TestInterruptReconciliationCancelsQueuedTurns(t *testing.T) {
	conv := newInterruptRecorder() // never marks anything active
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "c1",
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "queued", ClientMessageID: "c2",
	}); err != nil {
		t.Fatalf("Send queued: %v", err)
	}

	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["running"] == domain.TurnStateInterrupted &&
			states["queued"] == domain.TurnStateInterrupted
	})
	states := turnStateByText(t, snapshot)
	if states["queued"] != domain.TurnStateInterrupted {
		t.Errorf("queued turn = %q, want interrupted (cancelled, not dispatched)", states["queued"])
	}
	if got := h.conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v; reconciliation must not release the queue", got)
	}
}

// A daemon that is killed never runs its own cleanup, so whatever the dead
// controller left in flight is still marked live on disk. The next controller to
// come up has to close it out: nothing else ever will, and until then the timeline
// claims a turn is running and a queued message is waiting to be sent behind a
// controller that no longer exists.
func TestStartSettlesWorkLeftByAKilledController(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A turn in flight and a message queued behind it, exactly as a crash leaves them.
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "running", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "queued", ClientMessageID: "c2"}); err != nil {
		t.Fatalf("mid-turn Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventApprovalRequested, ProviderTurnID: "provider-turn-1",
		ProviderItemID: "9", RequestID: "9", ActivityKind: domain.ActivityKindCommand,
		ActivityStatus: domain.ActivityStatusPending, Summary: "Run something",
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Activities) == 1 })

	// A killed daemon leaves the rows mid-flight and takes its service with it, so
	// the next controller comes up in a NEW service over the SAME store. Building
	// it that way rather than reusing this one is the point: nothing in the old
	// process gets a chance to clean up.
	next := chatsvc.New(chatsvc.Options{
		Store:    h.st,
		Sessions: h.st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: newFakeConversation()}},
		Log:      slog.New(slog.DiscardHandler),
		NewID:    func() string { return "next-" + fmt.Sprint(time.Now().UnixNano()) },
		Now:      h.now,
	})
	t.Cleanup(func() { _ = next.Stop(context.Background(), testSession) })

	if _, err := next.Start(ctx, chatsvc.StartConfig{
		SessionID:              testSession,
		ProjectID:              testProject,
		Harness:                domain.HarnessCodex,
		WorkspacePath:          t.TempDir(),
		ProviderConversationID: "thread-1",
	}); err != nil {
		t.Fatalf("Start after crash: %v", err)
	}

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, turn := range s.Turns {
			if !turn.State.Terminal() {
				return false
			}
		}
		return len(s.Turns) == 2
	})
	states := turnStateByText(t, snapshot)
	if states["running"] != domain.TurnStateFailed {
		t.Errorf("turn abandoned mid-flight = %q, want failed", states["running"])
	}
	if states["queued"] != domain.TurnStateFailed {
		t.Errorf("message left queued by a dead controller = %q; nothing would ever send it",
			states["queued"])
	}
	if got := snapshot.Activities[0].Status; got == domain.ActivityStatusPending {
		t.Error("approval left pending by a dead controller; the user can never answer it")
	}
}

// Usage is current state, not history. The provider reports it after every tool
// call, so the projection must overwrite: a row per report is what buried the
// conversation, and the conversation is only ever one amount full.
func TestUsageProjectionKeepsOnlyTheLatest(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventUsage, Usage: &ports.ChatUsage{
			ContextUsed: 18055, ContextWindow: 258400,
			InputTokens: 18050, OutputTokens: 5, CachedTokens: 11008, TotalTokens: 18055,
		}},
		ports.ChatEvent{Kind: ports.ChatEventUsage, Usage: &ports.ChatUsage{
			ContextUsed: 42100, ContextWindow: 258400,
			InputTokens: 41000, OutputTokens: 1100, CachedTokens: 20000, TotalTokens: 60155,
		}},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.Usage != nil && s.Conversation.Usage.ContextUsed == 42100
	})

	usage := snapshot.Conversation.Usage
	if usage.ContextWindow != 258400 {
		t.Errorf("context window = %d, want 258400", usage.ContextWindow)
	}
	if usage.TotalTokens != 60155 {
		t.Errorf("cumulative total = %d, want the later 60155", usage.TotalTokens)
	}
	// The readout is only meaningful as a fraction; without the window it is a
	// number with no scale, which is what the header used to show.
	if got := usage.ContextFraction(); got < 0.16 || got > 0.17 {
		t.Errorf("context fraction = %v, want roughly 0.163", got)
	}

	// Usage must not become a timeline entry, under any kind.
	for _, activity := range snapshot.Activities {
		if activity.Kind == domain.ActivityKindUsage {
			t.Fatalf("usage was projected as an activity: %+v", activity)
		}
	}
}

// ACP reports context fullness and cumulative token totals in separate messages.
// A later totals update must not erase the context window received just before it.
func TestUsageProjectionMergesIndependentProviderUpdates(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventUsage, Usage: &ports.ChatUsage{
			ContextUsed: 74300, ContextWindow: 1_000_000, ContextKnown: true,
		}},
		ports.ChatEvent{Kind: ports.ChatEventUsage, Usage: &ports.ChatUsage{
			InputTokens: 2, OutputTokens: 59, CachedTokens: 74216,
			TotalTokens: 74277, TotalsKnown: true,
		}},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.Usage != nil && s.Conversation.Usage.TotalTokens == 74277
	})
	usage := snapshot.Conversation.Usage
	if usage.ContextUsed != 74300 || usage.ContextWindow != 1_000_000 {
		t.Fatalf("context usage was erased by totals update: %+v", usage)
	}
	if usage.CachedTokens != 74216 {
		t.Fatalf("cumulative totals were not merged: %+v", usage)
	}
}

// A model the provider states no window for still reports its tokens. The meter
// has to say "unknown" rather than draw an empty bar for a conversation that may
// be nearly full.
func TestUsageProjectionWithoutContextWindow(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventUsage, Usage: &ports.ChatUsage{
		ContextUsed: 900, TotalTokens: 900, InputTokens: 800, OutputTokens: 100,
	}})

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.Usage != nil
	})
	if got := snapshot.Conversation.Usage.ContextFraction(); got != -1 {
		t.Errorf("context fraction = %v, want -1 for an unknown window", got)
	}
}

// Rate limits are current state too, and an unreported window must survive a round
// trip through the database as unreported rather than as a reassuring zero.
func TestRateLimitProjectionKeepsOnlyTheLatest(t *testing.T) {
	h := newHarnessForHarness(t, domain.HarnessClaudeCode)

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventRateLimits, RateLimits: &ports.ChatRateLimits{
			PrimaryUsedPercent: 12, SecondaryUsedPercent: -1,
			PrimaryResetsInSeconds: 600, PlanLabel: "pro",
		}},
		ports.ChatEvent{Kind: ports.ChatEventRateLimits, RateLimits: &ports.ChatRateLimits{
			PrimaryUsedPercent: 71, SecondaryUsedPercent: -1,
			PrimaryResetsInSeconds: 490444, PlanLabel: "pro",
		}},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.RateLimits != nil && s.Conversation.RateLimits.PrimaryUsedPercent == 71
	})

	limits := snapshot.Conversation.RateLimits
	if limits.SecondaryUsedPercent >= 0 {
		t.Errorf("secondary = %v; an unreported window must not read as untouched quota",
			limits.SecondaryUsedPercent)
	}
	if limits.PrimaryResetsInSeconds != 490444 {
		t.Errorf("primary resets in %d, want 490444", limits.PrimaryResetsInSeconds)
	}
	if limits.PlanLabel != "pro" {
		t.Errorf("plan = %q, want pro", limits.PlanLabel)
	}
	if got := limits.WorstUsedPercent(); got != 71 {
		t.Errorf("worst window = %v, want 71", got)
	}
}

func TestCodexRateLimitsUpdateActiveAccountCapacityWithoutConversationPersistence(t *testing.T) {
	st := openStore(t)
	conv := newFakeConversation()
	updates := make(chan ports.CodexCapacityObservation, 1)
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conv}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return "bound-capacity" },
		OnCodexCapacityChanged: func(sessionID domain.SessionID, generation string, observation ports.CodexCapacityObservation) {
			if sessionID != testSession || generation != "managed-generation" {
				t.Errorf("capacity attribution = %s/%s", sessionID, generation)
			}
			updates <- observation
		},
	})
	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ControllerGeneration: "managed-generation",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	observation := ports.CodexCapacityObservation{Partial: true, Overall: &domain.CodexCapacityBucket{
		LimitID: "codex", Reached: domain.CodexCapacityNotReached,
		Primary: &domain.CodexCapacityWindow{UsedPercent: 81},
	}}
	conv.emit(ports.ChatEvent{Kind: ports.ChatEventRateLimits, ProviderEventID: "capacity-1", RateLimits: &ports.ChatRateLimits{
		PrimaryUsedPercent: 81, PlanLabel: "pro", CodexCapacity: &observation,
	}})
	select {
	case got := <-updates:
		if got.Overall == nil || got.Overall.Primary == nil || got.Overall.Primary.UsedPercent != 81 {
			t.Fatalf("capacity callback = %#v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for bound profile capacity update")
	}
	snapshot, err := st.LoadConversationSnapshot(context.Background(), ctrl.ConversationID())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Conversation.RateLimits != nil {
		t.Fatalf("bound Codex rate limits persisted: %#v", snapshot.Conversation.RateLimits)
	}
	events, err := st.ProviderEventsSince(context.Background(), ctrl.ConversationID(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var archived map[string]any
	if len(events) != 1 || json.Unmarshal([]byte(events[0].PayloadJson), &archived) != nil || archived["rateLimits"] != nil || strings.Contains(events[0].PayloadJson, "usedPercent") {
		t.Fatalf("bound Codex capacity leaked into provider archive: %#v", events)
	}
}

// Nothing reported yet is distinct from a conversation using nothing: the snapshot
// leaves both nil so a client can withhold the meter rather than draw an empty one.
func TestSnapshotOmitsUsageUntilTheProviderReports(t *testing.T) {
	h := newHarness(t)

	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot.Conversation.Usage != nil {
		t.Errorf("usage = %+v, want nil before the provider reports", snapshot.Conversation.Usage)
	}
	if snapshot.Conversation.RateLimits != nil {
		t.Errorf("rate limits = %+v, want nil before the provider reports",
			snapshot.Conversation.RateLimits)
	}
}

/* ---- compaction --------------------------------------------------------- */

// compactingConversation is a provider that can reclaim context. The plain fake
// deliberately cannot, so the unsupported path stays exercised by every other test.
type compactingConversation struct {
	*fakeConversation

	compactMu sync.Mutex
	calls     int
}

func newCompactingConversation() *compactingConversation {
	return &compactingConversation{fakeConversation: newFakeConversation()}
}

func (c *compactingConversation) Compact(context.Context) (ports.ChatCompactionResult, error) {
	c.compactMu.Lock()
	defer c.compactMu.Unlock()
	c.calls++
	// What is about to be reclaimed, not what was: the real provider accepts the
	// request and does the work as its own turn afterwards.
	return ports.ChatCompactionResult{TokensBefore: 15650}, nil
}

func (c *compactingConversation) compactCalls() int {
	c.compactMu.Lock()
	defer c.compactMu.Unlock()
	return c.calls
}

// A compaction has to survive a restart, which means it has to be a row. Without
// one, a conversation that quietly lost half its history has nothing in the
// timeline to explain the gap, and reads as if the agent simply forgot.
func TestCompactionIsProjectedAsATimelineFact(t *testing.T) {
	conv := newCompactingConversation()
	h := newHarnessWithConversation(t, conv)

	conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventCompacted,
		ProviderTurnID: "compact-turn",
		ProviderItemID: "cc-1",
		Summary:        "Compacted history, freeing 11.0k tokens",
		Detail:         []byte(`{"tokensBefore":15650,"tokensAfter":4632,"tokensReclaimed":11018}`),
	})

	// Both writes, because the row and the conversation flag are two statements and
	// this test asserts on both: waiting only on the row races the flag.
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Activities) == 1 && s.Conversation.CompactedAt != nil
	})
	activity := snapshot.Activities[0]
	if activity.Kind != domain.ActivityKindSystem {
		t.Errorf("kind = %q, want system", activity.Kind)
	}
	if activity.Status != domain.ActivityStatusCompleted {
		t.Errorf("status = %q, want completed", activity.Status)
	}
	if activity.Summary != "Compacted history, freeing 11.0k tokens" {
		t.Errorf("summary = %q", activity.Summary)
	}
	if activity.ProviderItemID != "cc-1" {
		t.Errorf("provider item id = %q, want cc-1 so a replay updates this row", activity.ProviderItemID)
	}
	// Not attached to a turn: the provider ran the compaction in a turn AO never
	// dispatched, so filing the row under it would attribute the entry to work the
	// user never asked for.
	if activity.TurnID != "" {
		t.Errorf("turn id = %q, want none", activity.TurnID)
	}
	var detail struct{ TokensReclaimed int64 }
	if err := json.Unmarshal(activity.Detail, &detail); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.TokensReclaimed != 11018 {
		t.Errorf("reclaimed = %d, want 11018", detail.TokensReclaimed)
	}

	// The conversation itself records that compaction has run, so a client does not
	// have to scan an unbounded timeline to find out.
	if snapshot.Conversation.CompactedAt == nil {
		t.Error("conversation was not marked compacted")
	}
}

// A compaction replayed across a reconnect updates the row it already has. Two
// entries for one compaction would read as two, and the reclaim would look twice
// as large as it was.
func TestCompactionReplayDoesNotDuplicateTheRow(t *testing.T) {
	conv := newCompactingConversation()
	h := newHarnessWithConversation(t, conv)

	for range 2 {
		conv.emit(ports.ChatEvent{
			Kind:           ports.ChatEventCompacted,
			ProviderItemID: "cc-1",
			Summary:        "Compacted the conversation history",
		})
	}
	// A marker after both, so this waits on an event rather than on a timeout.
	conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventActivityCompleted, ProviderItemID: "exec-1",
		ActivityKind: domain.ActivityKindCommand, ActivityStatus: domain.ActivityStatusCompleted,
		Summary: "date -u",
	})

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, activity := range s.Activities {
			if activity.ProviderItemID == "exec-1" {
				return true
			}
		}
		return false
	})
	compactions := 0
	for _, activity := range snapshot.Activities {
		if activity.Kind == domain.ActivityKindSystem {
			compactions++
		}
	}
	if compactions != 1 {
		t.Fatalf("got %d compaction rows for one compaction", compactions)
	}
}

func TestCompactReportsWhatIsAboutToBeReclaimed(t *testing.T) {
	conv := newCompactingConversation()
	h := newHarnessWithConversation(t, conv)

	result, err := h.svc.Compact(context.Background(), testSession)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.TokensBefore != 15650 {
		t.Errorf("tokensBefore = %d, want 15650", result.TokensBefore)
	}
	// Zero means "not yet known", not "reclaimed everything". The settled figures
	// reach the client on the timeline.
	if result.TokensAfter != 0 {
		t.Errorf("tokensAfter = %d, want 0 while the reclaim is in flight", result.TokensAfter)
	}
	if conv.compactCalls() != 1 {
		t.Errorf("provider called %d times, want 1", conv.compactCalls())
	}
}

// A provider with no way to reclaim context gets a typed answer, so the client can
// stop offering the control instead of surfacing an internal failure the user
// cannot act on. The plain fake conversation does not implement ChatCompactor.
func TestCompactOnAProviderThatCannotIsTyped(t *testing.T) {
	h := newHarness(t)

	_, err := h.svc.Compact(context.Background(), testSession)
	if !errors.Is(err, chatsvc.ErrCompactionUnsupported) {
		t.Fatalf("err = %v, want ErrCompactionUnsupported", err)
	}
}

// Measured twice against a live app-server: thread/compact/start mid-turn silently
// interrupts the running turn and reports it as interrupted, then compacts. Losing
// work the user is waiting on as a side effect of housekeeping is not something to
// discover afterwards from the timeline, so AO refuses and makes them stop it.
func TestCompactRefusesWhileATurnIsInFlight(t *testing.T) {
	conv := newCompactingConversation()
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "start something long", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if _, err := h.svc.Compact(ctx, testSession); !errors.Is(err, chatsvc.ErrCompactionWhileBusy) {
		t.Fatalf("err = %v, want ErrCompactionWhileBusy", err)
	}
	if conv.compactCalls() != 0 {
		t.Error("the provider was asked to compact anyway; the running turn would have been discarded")
	}

	// Once the turn settles it is allowed again.
	conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateCompleted,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
	})
	if _, err := h.svc.Compact(ctx, testSession); err != nil {
		t.Fatalf("Compact after the turn settled: %v", err)
	}
}

// A provider can start a turn AO never dispatched: a compaction runs as its own
// turn, and so does work the provider resumes from its own history. Without a row
// for it, every item that turn emits correlates to no turn — the activities arrive
// with an empty turn id and the timeline silently stops grouping them, which reads
// to a user as the conversation falling apart.
func TestProviderStartedTurnIsAdoptedSoItsItemsCorrelate(t *testing.T) {
	h := newHarness(t)

	// No Send: this turn is entirely the provider's doing.
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-owned-1"},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityCompleted, ProviderTurnID: "provider-owned-1",
			ProviderItemID: "exec-1", ActivityKind: domain.ActivityKindCommand,
			ActivityStatus: domain.ActivityStatusCompleted, Summary: "rg --files",
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityCompleted, ProviderTurnID: "provider-owned-1",
			ProviderItemID: "exec-2", ActivityKind: domain.ActivityKindCommand,
			ActivityStatus: domain.ActivityStatusCompleted, Summary: "sed -n 1,40p x",
		},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Activities) == 2
	})

	if len(snapshot.Turns) != 1 {
		t.Fatalf("turns = %d, want the provider's turn adopted", len(snapshot.Turns))
	}
	if got := snapshot.Turns[0].ProviderTurnID; got != "provider-owned-1" {
		t.Errorf("adopted turn provider id = %q", got)
	}
	for _, activity := range snapshot.Activities {
		if activity.TurnID == "" {
			t.Errorf("activity %q has no turn id; the timeline cannot group it", activity.Summary)
		}
	}
}

// failingProjectStore injects a projection failure for one event kind,
// simulating a SettleTurn error or tx.Commit failure without corrupting SQLite.
type failingProjectStore struct {
	chatsvc.Store
	failMethod string
	failed     chan struct{}
	failOnce   sync.Once
}

type recordingCleanupStore struct {
	chatsvc.Store
	called chan struct{}
}

func (s *recordingCleanupStore) CleanupOwnedControllerWork(
	ctx context.Context,
	session domain.SessionID,
	conversationID, generation string,
	now time.Time,
) (bool, error) {
	s.called <- struct{}{}
	return s.Store.CleanupOwnedControllerWork(ctx, session, conversationID, generation, now)
}

func (s *failingProjectStore) ProjectProviderEvent(
	ctx context.Context, conversationID string, session domain.SessionID,
	generation, providerEventID, method, payloadJSON string, now time.Time,
	project func(context.Context) error,
) (bool, error) {
	if method != s.failMethod {
		return s.Store.ProjectProviderEvent(ctx, conversationID, session, generation,
			providerEventID, method, payloadJSON, now, project)
	}
	projected, err := s.Store.ProjectProviderEvent(ctx, conversationID, session, generation,
		providerEventID, method, payloadJSON, now, func(txCtx context.Context) error {
			if err := project(txCtx); err != nil {
				return err
			}
			// Fail after the projection callback so its SQLite writes roll back. This
			// is the exact boundary that used to leak volatile state out of the tx.
			return errors.New("injected projection failure")
		})
	s.failOnce.Do(func() { close(s.failed) })
	return projected, err
}

// The exact #3749 sequence: the turn/completed projection fails (so the durable
// row stays 'running' and the UI keeps showing "Working"), then the user presses
// Stop and the provider refuses because the turn already ended on its side.
// Interrupt must settle the durable row as interrupted, cancel the queue behind
// it, and report idle — not answer CHAT_NO_ACTIVE_TURN and strand the user.
func TestProjectionFailureThenStopStillStopsTheTurn(t *testing.T) {
	conv := newInterruptRecorder() // never marks anything active -> always refuses
	st := openStore(t)

	var counterMu sync.Mutex
	counter := 0
	activity := &recordingActivity{}
	clock := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	failingStore := &failingProjectStore{
		Store: st, failMethod: string(ports.ChatEventTurnCompleted), failed: make(chan struct{}),
	}
	svc := chatsvc.New(chatsvc.Options{
		Store:    failingStore,
		Sessions: st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: conv}},
		Activity: activity,
		Log:      slog.New(slog.DiscardHandler),
		NewID: func() string {
			counterMu.Lock()
			defer counterMu.Unlock()
			counter++
			return fmt.Sprintf("id-%03d", counter)
		},
		Now: func() time.Time { return clock },
	})

	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID:     testSession,
		ProjectID:     testProject,
		Harness:       domain.HarnessCodex,
		WorkspacePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	ctx := context.Background()

	if _, err := svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "running", ClientMessageID: "c1",
	}); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	awaitStoreSnapshot(t, st, ctrl.ConversationID(), func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})

	if _, err := svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "queued", ClientMessageID: "c2",
	}); err != nil {
		t.Fatalf("Send queued: %v", err)
	}

	// The completion's projection fails: the durable row stays 'running' and
	// afterProject never runs, so nothing drains and nothing reports idle.
	conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventTurnCompleted,
		ProviderTurnID: "provider-turn-1",
		TurnState:      domain.TurnStateCompleted,
	})
	select {
	case <-failingStore.failed:
	case <-time.After(4 * time.Second):
		t.Fatal("completion projection did not reach the injected rollback")
	}

	if err := svc.Interrupt(ctx, testSession); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	snapshot := awaitStoreSnapshot(t, st, ctrl.ConversationID(), func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["running"] == domain.TurnStateInterrupted &&
			states["queued"] == domain.TurnStateInterrupted
	})
	states := turnStateByText(t, snapshot)
	if states["running"] != domain.TurnStateInterrupted {
		t.Errorf("running turn = %q, want interrupted", states["running"])
	}
	if states["queued"] != domain.TurnStateInterrupted {
		t.Errorf("queued turn = %q, want interrupted (cancelled, not dispatched)", states["queued"])
	}
	if got := conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider received %v; reconciliation must not release the queue", got)
	}
	if !hasActivitySignal(activity.snapshot(), domain.ActivityIdle, "chat.interrupt.reconciled") {
		t.Errorf("no idle signal after reconciliation; signals = %v", activity.snapshot())
	}
}

// Controller-state notifications perform durable cleanup before they change the
// volatile controller state. If that transaction rolls back, memory must continue
// reporting the last committed state rather than claiming the controller stopped.
func TestControllerStateChangesOnlyAfterProjectionCommits(t *testing.T) {
	var failingStore *failingProjectStore
	h := newHarnessWithConversationAndStore(t, nil, func(st *sqlite.Store) chatsvc.Store {
		failingStore = &failingProjectStore{
			Store: st, failMethod: string(ports.ChatEventControllerState), failed: make(chan struct{}),
		}
		return failingStore
	})
	want := h.ctrl.State()

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerStopped,
	})
	select {
	case <-failingStore.failed:
	case <-time.After(4 * time.Second):
		t.Fatal("controller-state projection did not reach the injected rollback")
	}
	if got := h.ctrl.State(); got != want {
		t.Fatalf("controller state after rolled-back projection = %q, want committed %q", got, want)
	}
}

func TestControllerStoppedEventUsesGenerationOwnedCleanup(t *testing.T) {
	var recordingStore *recordingCleanupStore
	h := newHarnessWithConversationAndStore(t, nil, func(st *sqlite.Store) chatsvc.Store {
		recordingStore = &recordingCleanupStore{Store: st, called: make(chan struct{}, 2)}
		return recordingStore
	})

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerStopped,
	})
	select {
	case <-recordingStore.called:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("explicit controller-stopped event did not use generation-owned cleanup")
	}
}

// awaitStoreSnapshot is awaitSnapshot for tests that build their own service
// rather than using the harness.
func awaitStoreSnapshot(t *testing.T, st *sqlite.Store, conversationID string,
	pred func(store.ConversationSnapshot) bool) store.ConversationSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last store.ConversationSnapshot
	for time.Now().Before(deadline) {
		snapshot, err := st.LoadConversationSnapshot(context.Background(), conversationID)
		if err != nil {
			t.Fatalf("load snapshot: %v", err)
		}
		last = snapshot
		if pred(snapshot) {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("snapshot never satisfied the condition; last had %d messages, %d activities, %d turns",
		len(last.Messages), len(last.Activities), len(last.Turns))
	return last
}
