package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

const (
	chatSwitchIntegrationProject          = domain.ProjectID("chat-switch-integration")
	chatSwitchIntegrationSourceProvider   = "source-provider"
	chatSwitchIntegrationSourceGeneration = "source-generation"
	chatSwitchIntegrationTargetProvider   = "target-provider"
	chatSwitchIntegrationTargetGeneration = "target-generation"
)

// integrationChatLauncher is the test-side equivalent of daemon.chatLauncher:
// it translates Session Manager's consumer-owned types while keeping the real
// Chat service on every controller path exercised by this fixture.
type integrationChatLauncher struct{ service *chatsvc.Service }

func (l integrationChatLauncher) SupportsChat(harness domain.AgentHarness) bool {
	return l.service.SupportsChat(harness)
}

func (l integrationChatLauncher) PreflightChat(
	ctx context.Context,
	harness domain.AgentHarness,
	permissions ports.PermissionMode,
) error {
	return l.service.PreflightChat(ctx, harness, permissions)
}

func (l integrationChatLauncher) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	result, err := l.service.StartChat(ctx, chatsvc.StartRequest{
		SessionID:               cfg.SessionID,
		ProjectID:               cfg.ProjectID,
		Kind:                    cfg.Kind,
		Harness:                 cfg.Harness,
		DataDir:                 cfg.DataDir,
		WorkspacePath:           cfg.WorkspacePath,
		Env:                     cfg.Env,
		Model:                   cfg.Model,
		Permissions:             cfg.Permissions,
		SystemPrompt:            cfg.SystemPrompt,
		AdditionalDirectories:   cfg.AdditionalDirectories,
		ProviderConversationID:  cfg.ProviderConversationID,
		ProviderScopeID:         cfg.ProviderScopeID,
		ControllerGeneration:    cfg.ControllerGeneration,
		RequireNativeHistory:    cfg.RequireNativeHistory,
		SkipNativeHistoryImport: cfg.SkipNativeHistoryImport,
		ControllerReady: func(result chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			if cfg.ControllerReady == nil {
				return chatsvc.ControllerCommit{}, nil
			}
			commit, readyErr := cfg.ControllerReady(ChatStarted{
				ProviderConversationID: result.ProviderConversationID,
				ControllerGeneration:   result.ControllerGeneration,
				Conversation:           result.Conversation,
				ProviderBoundary:       result.ProviderBoundary,
			})
			return chatsvc.ControllerCommit{Conversation: commit.Conversation}, readyErr
		},
	})
	if err != nil {
		return ChatStarted{}, err
	}
	return ChatStarted{
		ProviderConversationID: result.ProviderConversationID,
		ControllerGeneration:   result.ControllerGeneration,
	}, nil
}

func (l integrationChatLauncher) StartChatTurn(ctx context.Context, id domain.SessionID, text string) (string, error) {
	return l.service.StartChatTurn(ctx, id, text)
}

func (l integrationChatLauncher) RelayChatTurn(ctx context.Context, id domain.SessionID, text string) (string, error) {
	return l.service.RelayChatTurn(ctx, id, text)
}

func (l integrationChatLauncher) RelayChatTurnWithID(
	ctx context.Context,
	id domain.SessionID,
	text, clientMessageID string,
) (string, error) {
	return l.service.RelayChatTurnWithID(ctx, id, text, clientMessageID)
}

func (l integrationChatLauncher) HasLiveChatController(id domain.SessionID) bool {
	return l.service.HasLiveChatController(id)
}

func (l integrationChatLauncher) ArmChatHandoff(
	ctx context.Context,
	id domain.SessionID,
	policy domain.SessionInterfaceTransitionPolicy,
) error {
	return l.service.ArmChatHandoff(ctx, id, policy)
}

func (l integrationChatLauncher) PrepareChatHandoff(
	ctx context.Context,
	id domain.SessionID,
	policy domain.SessionInterfaceTransitionPolicy,
) error {
	return l.service.PrepareChatHandoff(ctx, id, policy)
}

func (l integrationChatLauncher) AbortChatHandoff(id domain.SessionID) {
	l.service.AbortChatHandoff(id)
}

func (l integrationChatLauncher) StopChat(ctx context.Context, id domain.SessionID) error {
	return l.service.StopChat(ctx, id)
}

type integrationChatConversation struct {
	providerID string
	events     chan ports.ChatEvent
	closeOnce  sync.Once

	mu   sync.Mutex
	sent []ports.ChatUserMessage
}

func newIntegrationChatConversation(providerID string) *integrationChatConversation {
	return &integrationChatConversation{
		providerID: providerID,
		events:     make(chan ports.ChatEvent),
	}
}

func (c *integrationChatConversation) ProviderConversationID() string { return c.providerID }

func (c *integrationChatConversation) Capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{
		ports.ChatCapabilityStreaming: true,
		ports.ChatCapabilityApprovals: true,
		ports.ChatCapabilityInterrupt: true,
		ports.ChatCapabilityResume:    true,
	}
}

func (c *integrationChatConversation) SendTurn(
	_ context.Context,
	message ports.ChatUserMessage,
) (ports.ChatTurnRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, message)
	return ports.ChatTurnRef{ProviderTurnID: fmt.Sprintf("provider-turn-%d", len(c.sent))}, nil
}

func (*integrationChatConversation) Interrupt(context.Context, string) error { return nil }

func (*integrationChatConversation) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return nil
}

func (c *integrationChatConversation) Events() <-chan ports.ChatEvent { return c.events }

func (c *integrationChatConversation) Close() error {
	c.closeOnce.Do(func() { close(c.events) })
	return nil
}

type integrationChatDriver struct {
	harness domain.AgentHarness
	start   func() ports.ChatConversation
	resume  func() ports.ChatConversation
}

func (d integrationChatDriver) Harness() domain.AgentHarness { return d.harness }

func (integrationChatDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return ports.ChatCapabilities{
		ports.ChatCapabilityStreaming: true,
		ports.ChatCapabilityApprovals: true,
		ports.ChatCapabilityInterrupt: true,
		ports.ChatCapabilityResume:    true,
	}, nil
}

func (d integrationChatDriver) Start(context.Context, ports.ChatStartConfig) (ports.ChatConversation, error) {
	return d.start(), nil
}

func (d integrationChatDriver) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	return d.resume(), nil
}

type integrationChatRegistry map[domain.AgentHarness]ports.ChatDriver

func (r integrationChatRegistry) Driver(harness domain.AgentHarness) (ports.ChatDriver, error) {
	driver, ok := r[harness]
	if !ok {
		return nil, ports.ErrChatUnsupported
	}
	return driver, nil
}

func (r integrationChatRegistry) SupportsChat(harness domain.AgentHarness) bool {
	_, ok := r[harness]
	return ok
}

type integrationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *integrationClock) Next() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Second)
	return c.now
}

type chatActivationDurableSnapshot struct {
	session      domain.SessionRecord
	conversation domain.ConversationRecord
	activeBranch domain.ConversationBranch
	switchRecord domain.AgentSwitch
}

// observingActivationLifecycle delegates the actual CAS to lifecycle.Manager.
// Its hook is exactly the Manager ControllerReady boundary because that callback
// is the sole caller of ActivateChatAgentSwitchTarget during a Chat switch.
type observingActivationLifecycle struct {
	*lifecycle.Manager
	store *sqlite.Store
	stale bool

	mu                    sync.Mutex
	calls                 int
	activation            domain.AgentSwitchChatTargetActivation
	controllerReadySource domain.SessionRecord
	beforeCAS             chatActivationDurableSnapshot
	afterCAS              chatActivationDurableSnapshot
	changed               bool
	activationErr         error
	observationErr        error
}

func (l *observingActivationLifecycle) ActivateChatAgentSwitchTarget(
	ctx context.Context,
	activation domain.AgentSwitchChatTargetActivation,
) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.activation = activation

	source, found, err := l.store.GetSession(ctx, activation.SessionID)
	if err != nil || !found {
		l.observationErr = fmt.Errorf("read ControllerReady source: found=%v err=%w", found, err)
		return false, l.observationErr
	}
	l.controllerReadySource = source
	if l.stale {
		source.Metadata.ControllerGeneration = "concurrent-source-generation"
		if err := l.store.UpdateSession(ctx, source); err != nil {
			l.observationErr = fmt.Errorf("inject stale source generation: %w", err)
			return false, l.observationErr
		}
	}
	l.beforeCAS, err = captureChatActivationDurableSnapshot(ctx, l.store, activation)
	if err != nil {
		l.observationErr = err
		return false, err
	}
	l.changed, l.activationErr = l.Manager.ActivateChatAgentSwitchTarget(ctx, activation)
	l.afterCAS, err = captureChatActivationDurableSnapshot(ctx, l.store, activation)
	if err != nil {
		l.observationErr = err
		return l.changed, errors.Join(l.activationErr, err)
	}
	return l.changed, l.activationErr
}

func captureChatActivationDurableSnapshot(
	ctx context.Context,
	store *sqlite.Store,
	activation domain.AgentSwitchChatTargetActivation,
) (chatActivationDurableSnapshot, error) {
	session, found, err := store.GetSession(ctx, activation.SessionID)
	if err != nil || !found {
		return chatActivationDurableSnapshot{}, fmt.Errorf("read activation session: found=%v err=%w", found, err)
	}
	conversation, err := store.ConversationForSession(ctx, activation.SessionID)
	if err != nil {
		return chatActivationDurableSnapshot{}, fmt.Errorf("read activation conversation: %w", err)
	}
	branch, err := store.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil {
		return chatActivationDurableSnapshot{}, fmt.Errorf("read activation branch: %w", err)
	}
	switchRecord, found, err := store.GetAgentSwitch(ctx, activation.SwitchID)
	if err != nil || !found {
		return chatActivationDurableSnapshot{}, fmt.Errorf("read activation switch: found=%v err=%w", found, err)
	}
	return chatActivationDurableSnapshot{
		session: session, conversation: conversation, activeBranch: branch, switchRecord: switchRecord,
	}, nil
}

type chatSwitchIntegrationFixture struct {
	store      *sqlite.Store
	service    *chatsvc.Service
	manager    *Manager
	lifecycle  *observingActivationLifecycle
	session    domain.SessionRecord
	targetChat *integrationChatConversation
}

func newChatSwitchIntegrationFixture(t *testing.T, stale bool) *chatSwitchIntegrationFixture {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	workspacePath := filepath.Join(dataDir, "workspace")
	store := sqlitetest.MustOpenAt(t, dataDir)
	clock := &integrationClock{now: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)}

	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:           string(chatSwitchIntegrationProject),
		Path:         dataDir,
		RegisteredAt: clock.Next(),
	}); err != nil {
		t.Fatalf("seed integration project: %v", err)
	}
	session, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: chatSwitchIntegrationProject,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessClaudeCode,
		Mode:      domain.SessionModeChat,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: clock.Next()},
		Metadata: domain.SessionMetadata{
			WorkspacePath:          workspacePath,
			Prompt:                 "implement the feature",
			LatestUserPrompt:       "keep the API small",
			LatestAssistantUpdate:  "implementation is in progress",
			ProviderConversationID: chatSwitchIntegrationSourceProvider,
			ControllerGeneration:   chatSwitchIntegrationSourceGeneration,
		},
		CreatedAt: clock.Next(),
		UpdatedAt: clock.Next(),
	})
	if err != nil {
		t.Fatalf("seed integration session: %v", err)
	}
	if _, err := store.CreateConversation(
		ctx,
		"chat-switch-integration-conversation",
		domain.ConversationScopeSession,
		chatSwitchIntegrationProject,
		session.ID,
		clock.Next(),
	); err != nil {
		t.Fatalf("seed integration conversation: %v", err)
	}

	sourceDriver := integrationChatDriver{
		harness: domain.HarnessClaudeCode,
		start: func() ports.ChatConversation {
			return newIntegrationChatConversation(chatSwitchIntegrationSourceProvider)
		},
		resume: func() ports.ChatConversation {
			return newIntegrationChatConversation(chatSwitchIntegrationSourceProvider)
		},
	}
	targetChat := newIntegrationChatConversation(chatSwitchIntegrationTargetProvider)
	targetDriver := integrationChatDriver{
		harness: domain.HarnessCodex,
		start:   func() ports.ChatConversation { return targetChat },
		resume:  func() ports.ChatConversation { return targetChat },
	}
	nextChatID := 0
	service := chatsvc.New(chatsvc.Options{
		Store: store, Sessions: store,
		Drivers: integrationChatRegistry{
			domain.HarnessClaudeCode: sourceDriver,
			domain.HarnessCodex:      targetDriver,
		},
		Log: slog.New(slog.DiscardHandler),
		NewID: func() string {
			nextChatID++
			return fmt.Sprintf("chat-switch-integration-%d", nextChatID)
		},
		Now: clock.Next,
	})
	if _, err := service.Start(ctx, chatsvc.StartConfig{
		SessionID: session.ID, ProjectID: session.ProjectID, Kind: session.Kind,
		Harness: domain.HarnessClaudeCode, DataDir: dataDir, WorkspacePath: workspacePath,
		ProviderConversationID:  chatSwitchIntegrationSourceProvider,
		ControllerGeneration:    chatSwitchIntegrationSourceGeneration,
		SkipNativeHistoryImport: true,
	}); err != nil {
		t.Fatalf("start real source Chat controller: %v", err)
	}
	t.Cleanup(func() { service.StopAll(context.Background()) })

	realLifecycle := lifecycle.New(store, nil)
	observedLifecycle := &observingActivationLifecycle{
		Manager: realLifecycle,
		store:   store,
		stale:   stale,
	}
	sourceAgent := &switchTestAgent{
		configDir: filepath.Join(dataDir, "claude"),
		available: map[string]ports.NativeSessionAvailability{
			chatSwitchIntegrationSourceProvider: ports.NativeSessionAvailabilityAvailable,
		},
	}
	targetAgent := &switchTestAgent{
		configDir: filepath.Join(dataDir, "codex"),
		available: map[string]ports.NativeSessionAvailability{},
	}
	launcher := integrationChatLauncher{service: service}
	manager := New(Deps{
		Runtime: &fakeRestartRuntime{fakeRuntime: &fakeRuntime{}},
		Agents: switchTestAgents{
			domain.HarnessClaudeCode: sourceAgent,
			domain.HarnessCodex:      targetAgent,
		},
		Workspace:  switchTestWorkspace{fakeWorkspace: &fakeWorkspace{path: workspacePath}},
		Store:      store,
		Messenger:  &fakeMessenger{},
		Chat:       launcher,
		Lifecycle:  observedLifecycle,
		DataDir:    dataDir,
		Clock:      clock.Next,
		LookPath:   func(string) (string, error) { return "/bin/agent", nil },
		Executable: func() (string, error) { return filepath.Join(dataDir, "bin", "ao"), nil },
		NewLaunchID: func() string {
			return chatSwitchIntegrationTargetGeneration
		},
		Logger: slog.New(slog.DiscardHandler),
	})
	manager.handoffWait = time.Millisecond
	manager.switchPostStopWait = time.Second

	return &chatSwitchIntegrationFixture{
		store: store, service: service, manager: manager, lifecycle: observedLifecycle,
		session: session, targetChat: targetChat,
	}
}

func (f *chatSwitchIntegrationFixture) runSwitch(t *testing.T, key string) domain.AgentSwitch {
	t.Helper()
	admitted, err := f.manager.SwitchAgent(context.Background(), f.session.ID, SwitchAgentConfig{
		TargetHarness:  domain.HarnessCodex,
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("SwitchAgent admission: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.manager.WaitAgentSwitchWorkers(waitCtx); err != nil {
		t.Fatalf("wait for SwitchAgent worker: %v", err)
	}
	return admitted
}

func TestSwitchAgentRealChatServiceSQLiteActivationCAS(t *testing.T) {
	t.Run("commits complete target ownership tuple", func(t *testing.T) {
		fixture := newChatSwitchIntegrationFixture(t, false)
		admitted := fixture.runSwitch(t, "real-chat-success")
		observation := fixture.lifecycle

		if observation.observationErr != nil {
			t.Fatalf("activation observation: %v", observation.observationErr)
		}
		if observation.calls != 1 {
			t.Fatalf("activation calls = %d, want 1", observation.calls)
		}
		if observation.controllerReadySource.Harness != domain.HarnessClaudeCode ||
			observation.controllerReadySource.Activity.State != domain.ActivityExited ||
			observation.controllerReadySource.Metadata.ControllerGeneration != chatSwitchIntegrationSourceGeneration {
			t.Fatalf("ControllerReady durable source = %+v, want stopped claude-code/%s owner",
				observation.controllerReadySource, chatSwitchIntegrationSourceGeneration)
		}
		if !observation.changed || observation.activationErr != nil {
			t.Fatalf("activation result = changed %v err %v, want changed with no error",
				observation.changed, observation.activationErr)
		}

		activation := observation.activation
		if activation.ExpectedSourceControllerGeneration != chatSwitchIntegrationSourceGeneration ||
			activation.ControllerGeneration != chatSwitchIntegrationTargetGeneration ||
			activation.ProviderConversationID != chatSwitchIntegrationTargetProvider {
			t.Fatalf("activation identity = %+v", activation)
		}
		before := observation.beforeCAS
		if before.session.Harness != domain.HarnessClaudeCode ||
			before.session.Activity.State != domain.ActivityExited ||
			before.session.Metadata.ControllerGeneration != chatSwitchIntegrationSourceGeneration {
			t.Fatalf("pre-CAS session = %+v, want stopped source tuple", before.session)
		}
		after := observation.afterCAS
		if after.session.Harness != domain.HarnessCodex ||
			after.session.Mode != domain.SessionModeChat ||
			after.session.Activity.State != domain.ActivityIdle ||
			after.session.Metadata.ProviderConversationID != chatSwitchIntegrationTargetProvider ||
			after.session.Metadata.ControllerGeneration != chatSwitchIntegrationTargetGeneration ||
			after.session.Metadata.AgentSessionID != chatSwitchIntegrationTargetProvider ||
			after.session.Metadata.RuntimeHandleID != "" || after.session.Metadata.RuntimeLaunchID != "" {
			t.Fatalf("post-CAS target session tuple = %+v", after.session)
		}
		targetNative, found, err := fixture.store.GetAgentNativeSession(context.Background(), activation.TargetNativeSessionRef)
		if err != nil || !found {
			t.Fatalf("target native session: found=%v err=%v", found, err)
		}
		if targetNative.AOSessionID != fixture.session.ID ||
			targetNative.Harness != domain.HarnessCodex ||
			targetNative.NativeSessionID != chatSwitchIntegrationTargetProvider ||
			targetNative.LastGenerationID != domain.AgentGenerationID(chatSwitchIntegrationTargetGeneration) {
			t.Fatalf("target native tuple = %+v", targetNative)
		}
		boundaryID := string(admitted.ID) + ":provider"
		if after.conversation.ActiveBranchID != boundaryID ||
			after.activeBranch.ID != boundaryID || !after.activeBranch.Active ||
			after.activeBranch.ParentBranchID != before.activeBranch.ID ||
			after.activeBranch.ProviderConversationID != chatSwitchIntegrationTargetProvider ||
			after.activeBranch.ProviderScopeID != boundaryID {
			t.Fatalf("post-CAS conversation tuple = conversation %+v branch %+v; source branch %+v",
				after.conversation, after.activeBranch, before.activeBranch)
		}
		if after.switchRecord.State != domain.AgentSwitchTargetReady ||
			after.switchRecord.FromHarness != domain.HarnessClaudeCode ||
			after.switchRecord.TargetHarness != domain.HarnessCodex ||
			after.switchRecord.SourceGenerationID != domain.AgentGenerationID(chatSwitchIntegrationSourceGeneration) ||
			after.switchRecord.TargetGenerationID != domain.AgentGenerationID(chatSwitchIntegrationTargetGeneration) ||
			after.switchRecord.TargetNativeSessionRef == nil ||
			*after.switchRecord.TargetNativeSessionRef != activation.TargetNativeSessionRef {
			t.Fatalf("post-CAS switch tuple = %+v", after.switchRecord)
		}
		completed, found, err := fixture.store.GetAgentSwitch(context.Background(), admitted.ID)
		if err != nil || !found {
			t.Fatalf("completed switch: found=%v err=%v", found, err)
		}
		if completed.State != domain.AgentSwitchCompleted || completed.TargetAcknowledgedAt == nil {
			t.Fatalf("completed switch tuple = %+v", completed)
		}
		fixture.targetChat.mu.Lock()
		sent := append([]ports.ChatUserMessage(nil), fixture.targetChat.sent...)
		fixture.targetChat.mu.Unlock()
		if len(sent) != 1 || sent[0].ClientMessageID != chatSwitchActivationMessageID(admitted.ID) {
			t.Fatalf("target activation turns = %+v, want one idempotent continuation", sent)
		}
	})

	t.Run("stale source generation loses without partial CAS mutation", func(t *testing.T) {
		fixture := newChatSwitchIntegrationFixture(t, true)
		admitted := fixture.runSwitch(t, "real-chat-stale-source")
		observation := fixture.lifecycle

		if observation.observationErr != nil {
			t.Fatalf("activation observation: %v", observation.observationErr)
		}
		if observation.calls != 1 {
			t.Fatalf("activation calls = %d, want 1", observation.calls)
		}
		if observation.controllerReadySource.Harness != domain.HarnessClaudeCode ||
			observation.controllerReadySource.Activity.State != domain.ActivityExited ||
			observation.controllerReadySource.Metadata.ControllerGeneration != chatSwitchIntegrationSourceGeneration {
			t.Fatalf("ControllerReady durable source = %+v, want admitted stopped source generation",
				observation.controllerReadySource)
		}
		if observation.changed || observation.activationErr != nil {
			t.Fatalf("stale activation result = changed %v err %v, want losing CAS",
				observation.changed, observation.activationErr)
		}
		if observation.beforeCAS.session.Metadata.ControllerGeneration != "concurrent-source-generation" {
			t.Fatalf("pre-CAS raced generation = %q, want concurrent-source-generation",
				observation.beforeCAS.session.Metadata.ControllerGeneration)
		}
		if !reflect.DeepEqual(observation.afterCAS, observation.beforeCAS) {
			t.Fatalf("losing CAS changed durable tuple:\n before: %+v\n  after: %+v",
				observation.beforeCAS, observation.afterCAS)
		}
		finalSnapshot, err := captureChatActivationDurableSnapshot(
			context.Background(), fixture.store, observation.activation)
		if err != nil {
			t.Fatalf("read final stale snapshot: %v", err)
		}
		if !reflect.DeepEqual(finalSnapshot, observation.beforeCAS) {
			t.Fatalf("stale switch worker changed pre-CAS tuple:\npre-CAS: %+v\n  final: %+v",
				observation.beforeCAS, finalSnapshot)
		}
		if finalSnapshot.switchRecord.ID != admitted.ID ||
			finalSnapshot.switchRecord.State != domain.AgentSwitchStartingTarget ||
			finalSnapshot.switchRecord.TargetNativeSessionRef == nil {
			t.Fatalf("stale switch snapshot = %+v", finalSnapshot.switchRecord)
		}
		boundaryID := string(admitted.ID) + ":provider"
		if _, err := fixture.store.ConversationBranch(
			context.Background(), finalSnapshot.conversation.ID, boundaryID,
		); !errors.Is(err, domain.ErrNoConversationBranch) {
			t.Fatalf("stale target boundary lookup error = %v, want ErrNoConversationBranch", err)
		}
	})
}
