package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ErrNoController reports a command for a session with no live Chat controller.
// It is distinct from "unknown session": the session may exist and be terminated,
// or its controller may have stopped, and the client needs to tell those apart.
var ErrNoController = errors.New("no live chat controller for session")

// ErrNotChatMode reports a Chat command against a session whose committed
// controller is currently TUI. An explicit interface transition may change that
// fact later, but callers must route from the persisted mode they read now.
var ErrNotChatMode = errors.New("session is not in chat mode")

// SessionReader is the session-fact surface the service needs. It reads the
// persisted mode rather than trusting the caller, so a client cannot talk its way
// into the wrong dispatch path.
type SessionReader interface {
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
}

// Service owns the live Chat controllers.
type Service struct {
	store                  Store
	reader                 SnapshotReader
	pageReader             SnapshotPageReader
	sessions               SessionReader
	drivers                ports.ChatDriverRegistry
	activity               ActivityRecorder
	log                    *slog.Logger
	newID                  IDFactory
	now                    Clock
	onAccountChanged       func(domain.SessionID, string, domain.AgentHarness)
	onCodexCapacityChanged func(domain.SessionID, string, ports.CodexCapacityObservation)

	mu           sync.RWMutex
	controllers  map[domain.SessionID]*Controller
	startConfigs map[domain.SessionID]StartConfig
	gateMu       sync.Mutex
	gates        map[domain.SessionID]controllerGate
	probeMu      sync.Mutex
	probed       map[domain.AgentHarness]ports.ChatCapabilities
}

// controllerGate serializes start/stop for one session without making provider
// I/O for that session block lookups or commands for every other Chat session.
type controllerGate chan struct{}

func (g controllerGate) lock(ctx context.Context) error {
	select {
	case g <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g controllerGate) unlock() { <-g }

// Options configures a Service. The id factory and clock are injected so tests
// are deterministic.
type Options struct {
	Store      Store
	Reader     SnapshotReader
	PageReader SnapshotPageReader
	Sessions   SessionReader
	Drivers    ports.ChatDriverRegistry
	// Activity feeds derived session status from turn events. Nil leaves a chat
	// session reading as idle while it works, so production always wires it.
	Activity ActivityRecorder
	Log      *slog.Logger
	NewID    IDFactory
	Now      Clock
	// OnAccountChanged invalidates daemon-owned account readiness for the
	// harness that emitted an account/updated notification.
	OnAccountChanged func(domain.SessionID, string, domain.AgentHarness)
	// OnCodexCapacityChanged attributes a structured provider update to the one
	// globally active AO Codex account. The callback owns profile-independent
	// account state; conversation rows are not the authority for Codex capacity.
	OnCodexCapacityChanged func(domain.SessionID, string, ports.CodexCapacityObservation)
}

// New builds a Chat service.
func New(opts Options) *Service {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store:                  opts.Store,
		reader:                 opts.Reader,
		pageReader:             opts.PageReader,
		sessions:               opts.Sessions,
		drivers:                opts.Drivers,
		activity:               opts.Activity,
		log:                    log,
		newID:                  opts.NewID,
		now:                    now,
		onAccountChanged:       opts.OnAccountChanged,
		onCodexCapacityChanged: opts.OnCodexCapacityChanged,
		controllers:            make(map[domain.SessionID]*Controller),
		startConfigs:           make(map[domain.SessionID]StartConfig),
		gates:                  make(map[domain.SessionID]controllerGate),
		probed:                 make(map[domain.AgentHarness]ports.ChatCapabilities),
	}
}

func (s *Service) controllerGate(id domain.SessionID) controllerGate {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	gate := s.gates[id]
	if gate == nil {
		gate = make(controllerGate, 1)
		s.gates[id] = gate
	}
	return gate
}

// StartConfig opens a controller for a session.
type StartConfig struct {
	SessionID             domain.SessionID
	ProjectID             domain.ProjectID
	Kind                  domain.SessionKind
	Harness               domain.AgentHarness
	DataDir               string
	WorkspacePath         string
	Env                   map[string]string
	Model                 string
	Effort                string
	Permissions           ports.PermissionMode
	SystemPrompt          string
	AdditionalDirectories []string
	MCPServers            []ports.ChatMCPServerConfig
	// ExpectedControllerOwner is the durable controller identity observed before
	// this launch. PrepareControllerEnv uses it as a compare-and-swap fence.
	ExpectedControllerOwner domain.SessionControllerOwner
	// PrepareControllerEnv rotates launch-only credentials inside the per-session
	// controller gate. The returned environment is never retained in startConfigs.
	PrepareControllerEnv func(context.Context, domain.SessionControllerOwner) (map[string]string, error)
	// ProviderConversationID resumes an existing provider conversation when set.
	ProviderConversationID string
	// ProviderScopeID reserves the opaque-id namespace for a provider boundary
	// that ControllerReady will commit. Empty derives the namespace from the
	// active branch, which is the ordinary initial-start and resume path.
	ProviderScopeID string
	// ControllerGeneration is supplied by a durable replacement saga that must
	// fence the target before starting it. Ordinary starts leave it empty.
	ControllerGeneration string
	// RequireNativeHistory makes a missing typed provider replay fatal. Interface
	// handoff sets it because provider context without a visible transcript would
	// make completed Terminal work disappear from Chat.
	RequireNativeHistory bool
	// SkipNativeHistoryImport resumes provider context without projecting its old
	// events before ControllerReady. Agent switching uses this because its atomic
	// provider boundary does not exist until ControllerReady commits; AO already
	// retains the unified timeline and the finalized continuation separately.
	SkipNativeHistoryImport bool
	// ControllerReady commits the controller's durable generation before event
	// consumption starts. A controller that exits immediately must report after
	// the launch has been marked live, so its exited signal cannot be overwritten
	// by a later launch-completion write.
	ControllerReady func(StartResult) (ControllerCommit, error)
}

// ControllerCommit is the conversation state committed by ControllerReady.
// Carrying it back across the callback avoids a fallible database read after an
// irreversible ownership transfer.
type ControllerCommit struct {
	Conversation    domain.ConversationRecord
	ControllerOwner domain.SessionControllerOwner
}

func controllerStartResult(
	controller *Controller,
	providerBoundary *domain.ConversationBranch,
	commitProviderHistory func(context.Context) error,
) StartResult {
	return StartResult{
		ProviderConversationID: controller.ProviderConversationID(),
		ControllerGeneration:   controller.Generation(),
		Conversation:           controller.conversation,
		ProviderBoundary:       providerBoundary,
		CommitProviderHistory:  commitProviderHistory,
	}
}

func notifyControllerReady(
	cfg StartConfig,
	controller *Controller,
	providerBoundary *domain.ConversationBranch,
	commitProviderHistory func(context.Context) error,
) (ControllerCommit, error) {
	if cfg.ControllerReady == nil {
		return ControllerCommit{}, nil
	}
	commit, err := cfg.ControllerReady(controllerStartResult(
		controller, providerBoundary, commitProviderHistory,
	))
	if err != nil {
		return ControllerCommit{}, fmt.Errorf("commit chat controller: %w", err)
	}
	return commit, nil
}

func cloneStartConfig(cfg StartConfig) StartConfig {
	cloned := cfg
	cloned.Env = make(map[string]string, len(cfg.Env))
	for key, value := range cfg.Env {
		cloned.Env[key] = value
	}
	cloned.AdditionalDirectories = append([]string(nil), cfg.AdditionalDirectories...)
	cloned.MCPServers = make([]ports.ChatMCPServerConfig, len(cfg.MCPServers))
	for index, server := range cfg.MCPServers {
		server.Args = append([]string(nil), server.Args...)
		server.Env = make(map[string]string, len(cfg.MCPServers[index].Env))
		for key, value := range cfg.MCPServers[index].Env {
			server.Env[key] = value
		}
		server.Headers = make(map[string]string, len(cfg.MCPServers[index].Headers))
		for key, value := range cfg.MCPServers[index].Headers {
			server.Headers[key] = value
		}
		cloned.MCPServers[index] = server
	}
	return cloned
}

// settleOrphanedWork closes out anything a previous controller left behind.
//
// Best-effort by design: a failure here must not stop a session from coming back,
// because a session the user cannot reopen is worse than a stale row. Both
// failures are logged rather than swallowed.
func (s *Service) settleOrphanedWork(ctx context.Context, session domain.SessionID, conversationID string) {
	now := s.now()
	if err := s.store.SettleOrphanedTurns(ctx, session, now); err != nil {
		s.log.Error("chat start: settle orphaned turns", "session", session, "error", err)
	}
	// An approval left pending can never be answered: the provider call it was
	// blocking died with the process that was holding it.
	if err := s.store.FailPendingApprovals(ctx, conversationID, now); err != nil {
		s.log.Error("chat start: close pending approvals", "session", session, "error", err)
	}
	if err := s.store.FailPendingInputs(ctx, conversationID, now); err != nil {
		s.log.Error("chat start: close pending input requests", "session", session, "error", err)
	}
}

// Start launches or resumes the Chat controller for a session.
//
// A resume that fails is reported as a failure rather than quietly becoming a new
// conversation: presenting unrelated history as continuous is worse than an error
// the user can act on.
func (s *Service) Start(ctx context.Context, cfg StartConfig) (*Controller, error) {
	gate := s.controllerGate(cfg.SessionID)
	if err := gate.lock(ctx); err != nil {
		return nil, err
	}
	defer gate.unlock()

	replayCheckpoint := nativeHistoryCheckpoint{}
	if cfg.RequireNativeHistory {
		if s.sessions == nil {
			return nil, errors.New("native history replay requires a session reader")
		}
		rec, found, err := s.sessions.GetSession(ctx, cfg.SessionID)
		if err != nil {
			return nil, fmt.Errorf("read native history checkpoint: %w", err)
		}
		if !found {
			return nil, ports.ErrSessionNotFound
		}
		replayCheckpoint.latestUserPrompt = strings.TrimSpace(rec.Metadata.LatestUserPrompt)
		replayCheckpoint.latestAssistantUpdate = strings.TrimSpace(rec.Metadata.LatestAssistantUpdate)
	}

	s.mu.RLock()
	existing := s.controllers[cfg.SessionID]
	s.mu.RUnlock()
	if existing != nil {
		if existing.State() != ports.ChatControllerStopped {
			return existing, nil
		}
		// A stopped event can reach the UI before the projector finishes its final
		// durable cleanup and the registry goroutine releases the entry. Never hand
		// that dead controller back as a successful resume. Wait for its stream to
		// finish, then remove only that generation before opening the replacement.
		select {
		case <-existing.stopped:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
		if current := s.controllers[cfg.SessionID]; current == existing {
			delete(s.controllers, cfg.SessionID)
		}
		s.mu.Unlock()
	}

	driver, err := s.drivers.Driver(cfg.Harness)
	if err != nil {
		return nil, fmt.Errorf("chat driver for %s: %w", cfg.Harness, err)
	}

	caps, err := s.driverCapabilities(ctx, cfg.Harness, driver)
	if err != nil {
		return nil, err
	}
	if cfg.ProviderConversationID == "" {
		if err := capabilityAdmissionError(cfg.Harness, caps, cfg.Permissions); err != nil {
			return nil, err
		}
	}

	scope := domain.ConversationScopeSession
	if cfg.Kind == domain.KindOrchestrator {
		scope = domain.ConversationScopeProject
	}
	now := s.now()
	var resetBoundary domain.ConversationActivity
	freshProjectContext := scope == domain.ConversationScopeProject && cfg.ProviderConversationID == ""
	if freshProjectContext {
		detail, marshalErr := json.Marshal(map[string]string{
			"event":  "context.reset",
			"reason": "native conversation was unavailable",
		})
		if marshalErr != nil {
			return nil, fmt.Errorf("encode fresh context boundary: %w", marshalErr)
		}
		resetBoundary = domain.ConversationActivity{
			ID:             s.newID(),
			Kind:           domain.ActivityKindSystem,
			Status:         domain.ActivityStatusCompleted,
			Summary:        "Agent context reset.",
			Detail:         detail,
			ProviderItemID: domain.ConversationContextResetProviderItemID(cfg.SessionID),
		}
	}
	conversationID := s.newID()
	var conversation domain.ConversationRecord
	if freshProjectContext {
		conversation, err = s.store.CreateProjectConversationWithContextReset(
			ctx, conversationID, cfg.ProjectID, cfg.SessionID, resetBoundary, now)
	} else if scope == domain.ConversationScopeProject &&
		cfg.ProviderConversationID != "" && cfg.ProviderScopeID != "" {
		// A coordinator-reserved provider boundary is an ownership transfer, not
		// permission to steal a project narrative from a newer orchestrator.
		// Require the durable current_session_id proof the coordinator observed;
		// ControllerReady/CommitChatSpawn rechecks it after provider I/O.
		conversation, err = s.store.ConversationForSession(ctx, cfg.SessionID)
	} else {
		conversation, err = s.store.CreateConversation(
			ctx, conversationID, scope, cfg.ProjectID, cfg.SessionID, now)
	}
	if err != nil {
		return nil, fmt.Errorf("open conversation: %w", err)
	}
	repairedBranch, restoredProviderOwner, err := s.store.RepairIncompleteConversationEdit(
		ctx, cfg.SessionID, conversation.ID, s.now())
	if err != nil {
		return nil, fmt.Errorf("repair incomplete conversation edit: %w", err)
	}
	if repairedBranch.ID != "" {
		conversation.ActiveBranchID = repairedBranch.ID
		// An ordinary restart was handed the abandoned child's provider handle.
		// A coordinator-supplied scope belongs to a pending provider boundary (for
		// example, agent switching) and must keep its independently reserved handle.
		if restoredProviderOwner && cfg.ProviderScopeID == "" {
			cfg.ProviderConversationID = repairedBranch.ProviderConversationID
		}
	}
	activeBranch, err := s.store.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil {
		return nil, fmt.Errorf("load active conversation branch: %w", err)
	}
	providerScopeID := activeBranch.ProviderScopeID
	providerHandleOwnedByActiveBranch := true
	if cfg.ProviderScopeID != "" {
		providerScopeID = cfg.ProviderScopeID
		providerHandleOwnedByActiveBranch = providerScopeID == activeBranch.ProviderScopeID
	}
	providerBoundaryID := ""
	if !providerHandleOwnedByActiveBranch {
		providerBoundaryID = providerScopeID
	} else if cfg.ProviderConversationID == "" && cfg.ProviderScopeID == "" &&
		(conversation.LatestSequence > 0 || activeBranch.ProviderConversationID != "") {
		// This conversation already owns provider history, but the caller proved it
		// cannot resume that provider thread. Reserve the next provider boundary
		// before connect so every opaque id emitted by the fresh process is born in
		// the same namespace that ControllerReady will durably publish.
		providerBoundaryID = s.newID()
		providerScopeID = providerBoundaryID
		providerHandleOwnedByActiveBranch = false
	}
	if cfg.ProviderConversationID != "" && providerHandleOwnedByActiveBranch {
		if activeBranch.ProviderConversationID != "" &&
			activeBranch.ProviderConversationID != cfg.ProviderConversationID {
			return nil, fmt.Errorf("active conversation branch provider handle %q does not match session handle %q",
				activeBranch.ProviderConversationID, cfg.ProviderConversationID)
		}
	}
	// Conversation settings are durable user choices. Restore the selected
	// model when rebuilding a controller after a daemon restart; the caller's
	// session default must not silently replace it.
	if cfg.ProviderConversationID != "" && conversation.Settings.Model != "" {
		cfg.Model = conversation.Settings.Model
	}
	if cfg.ProviderConversationID != "" && conversation.Settings.ReasoningEffort != "" {
		cfg.Effort = conversation.Settings.ReasoningEffort
	}
	if cfg.ProviderConversationID != "" && conversation.Settings.ApprovalMode != "" {
		cfg.Permissions = conversation.Settings.ApprovalMode
	}
	if err := capabilityAdmissionError(cfg.Harness, caps, cfg.Permissions); err != nil {
		return nil, err
	}
	if cfg.ProviderConversationID == "" {
		conversation.Settings.Model = cfg.Model
		conversation.Settings.ReasoningEffort = cfg.Effort
		conversation.Settings.ApprovalMode = cfg.Permissions
		if err := s.store.SetConversationSettings(ctx, conversation.ID, conversation.Settings, s.now()); err != nil {
			return nil, fmt.Errorf("record initial conversation settings: %w", err)
		}
	}

	launchEnv := cfg.Env
	if cfg.PrepareControllerEnv != nil {
		launchEnv, err = cfg.PrepareControllerEnv(ctx, cfg.ExpectedControllerOwner)
		if err != nil {
			return nil, fmt.Errorf("prepare chat controller environment: %w", err)
		}
	}

	var conv ports.ChatConversation
	if cfg.ProviderConversationID != "" {
		conv, err = driver.Resume(ctx, ports.ChatResumeConfig{
			SessionID:              cfg.SessionID,
			ProviderConversationID: cfg.ProviderConversationID,
			DataDir:                cfg.DataDir,
			WorkspacePath:          cfg.WorkspacePath,
			Env:                    launchEnv,
			Model:                  cfg.Model,
			Effort:                 cfg.Effort,
			Permissions:            cfg.Permissions,
			SystemPrompt:           cfg.SystemPrompt,
			ProviderScopeID:        providerScopeID,
			AdditionalDirectories:  cfg.AdditionalDirectories,
			MCPServers:             cfg.MCPServers,
		})
	} else {
		conv, err = driver.Start(ctx, ports.ChatStartConfig{
			SessionID:             cfg.SessionID,
			DataDir:               cfg.DataDir,
			WorkspacePath:         cfg.WorkspacePath,
			Env:                   launchEnv,
			Model:                 cfg.Model,
			Permissions:           cfg.Permissions,
			SystemPrompt:          cfg.SystemPrompt,
			ProviderScopeID:       providerScopeID,
			AdditionalDirectories: cfg.AdditionalDirectories,
			MCPServers:            cfg.MCPServers,
		})
	}
	if err != nil {
		return nil, err
	}
	if cfg.ProviderConversationID != "" &&
		conv.ProviderConversationID() != cfg.ProviderConversationID {
		returned := conv.ProviderConversationID()
		cleanupUnpublishedConversation(conv, false)
		return nil, fmt.Errorf(
			"resumed provider conversation handle %q does not match requested handle %q",
			returned, cfg.ProviderConversationID,
		)
	}
	liveReconnect := false
	if reconnected, ok := conv.(ports.ChatLiveReconnector); ok {
		liveReconnect = reconnected.ReconnectedLive()
	}
	var liveRows ConversationRows
	if liveReconnect {
		if s.reader == nil {
			cleanupUnpublishedConversation(conv, false)
			return nil, fmt.Errorf("%w: durable conversation snapshot is unavailable", ports.ErrChatRecoveryInconclusive)
		}
		liveRows, err = s.reader.LoadConversationSnapshot(ctx, conversation.ID)
		if err != nil {
			cleanupUnpublishedConversation(conv, false)
			return nil, fmt.Errorf("%w: load live conversation before reconnect: %w", ports.ErrChatRecoveryInconclusive, err)
		}
	}

	// Claim the durable fence before the controller starts consuming events. A
	// pending provider boundary claims it in ControllerReady's atomic ownership
	// commit instead, so a failed provider connect or callback cannot split the
	// session owner from the conversation head.
	generation := strings.TrimSpace(cfg.ControllerGeneration)
	if generation == "" {
		generation = s.newID()
	}
	if providerBoundaryID == "" {
		if err := s.store.ClaimChatControllerGeneration(ctx, cfg.SessionID, generation, s.now()); err != nil {
			cleanupUnpublishedConversation(conv, cfg.ProviderConversationID == "")
			return nil, fmt.Errorf("claim chat controller: %w", err)
		}
	}
	providerBoundary := (*domain.ConversationBranch)(nil)
	if providerBoundaryID != "" {
		providerBoundary = &domain.ConversationBranch{
			ID: providerBoundaryID, ConversationID: conversation.ID, SessionID: cfg.SessionID,
			ProviderConversationID: conv.ProviderConversationID(), ParentBranchID: activeBranch.ID,
			ForkAfterSequence: conversation.LatestSequence, ProviderScopeID: providerScopeID,
			CreatedAt: s.now(),
		}
	}
	// Whatever the previous controller left in flight is not this controller's, and
	// it is not evidence that any work finished.
	//
	// The graceful path settles this when the event stream ends, but a daemon that
	// was killed never got there — so on a crash the timeline was left claiming a
	// turn was still running and a queued message was still waiting to be sent,
	// behind a controller that no longer existed. Nothing would ever have corrected
	// it. Settling here covers every way a controller can come up, and is a no-op
	// for a session that has none of it.
	if !liveReconnect {
		s.settleOrphanedWork(ctx, cfg.SessionID, conversation.ID)
	}
	// A fresh generation per launch, so events from the controller this one
	// replaced can be told apart from the current one's.
	controller := newController(
		cfg.SessionID, conversation, generation, cfg.Harness, conv, s.store, s.activity, s.log, s.newID, s.now, s.onAccountChanged, s.onCodexCapacityChanged)
	var commitProviderHistory func(context.Context) error
	if liveReconnect {
		controller.restoreLiveTurnOwnership(liveRows.Turns)
	}
	if cfg.ProviderConversationID != "" && !cfg.SkipNativeHistoryImport && !liveReconnect {
		// The provider's native thread is the continuity authority across TUI and
		// Chat. Import it before the live projector starts so the first notification
		// cannot appear ahead of the older prompt, tool work, and answer it follows.
		//
		// Read AO's existing projection too. ACP message/turn ids are opaque, and an
		// agent may assign a different persisted user id from the id AO supplied at
		// prompt time. Reconciliation must therefore happen before projection; doing
		// it in a Claude binding would leave every other ACP harness with the same
		// restart duplication race.
		var existing ConversationRows
		if s.reader != nil {
			existing, err = s.reader.LoadConversationSnapshot(ctx, conversation.ID)
			if err != nil {
				cleanupUnpublishedConversation(conv, false)
				return nil, fmt.Errorf("load conversation before native history import: %w", err)
			}
		}
		if cfg.RequireNativeHistory {
			replayCheckpoint.captureAOHighWater(
				cfg.SessionID, existing.Turns, existing.Messages, existing.Activities,
			)
		}
		events, historyErr := controller.readNativeHistory(
			ctx, existing.Turns, existing.Messages, existing.Activities,
			cfg.RequireNativeHistory, replayCheckpoint,
		)
		if historyErr != nil {
			cleanupUnpublishedConversation(conv, false)
			return nil, historyErr
		}
		if providerBoundary != nil {
			// The pending branch does not exist yet. Carry its stable replay into
			// ControllerReady so SQLite can insert/activate the boundary, stage the
			// generation, project every event onto that branch, and publish the
			// live session as one transaction. Until then the terminated target is
			// not exposed to input and no event can be attributed to the old root.
			commitProviderHistory = func(commitCtx context.Context) error {
				return controller.projectNativeHistory(commitCtx, events)
			}
		} else if err := controller.projectNativeHistory(ctx, events); err != nil {
			cleanupUnpublishedConversation(conv, false)
			return nil, err
		}
	}
	commit, err := notifyControllerReady(
		cfg, controller, providerBoundary, commitProviderHistory,
	)
	if err != nil {
		cleanupUnpublishedConversation(conv, cfg.ProviderConversationID == "")
		return nil, err
	}
	if providerBoundary != nil {
		if cfg.ControllerReady == nil {
			if commitProviderHistory != nil {
				_ = conv.Close()
				return nil, errors.New("commit native history: atomic provider-boundary lifecycle is unavailable")
			}
			if err := s.store.CreateAndActivateConversationBranch(
				ctx, cfg.SessionID, *providerBoundary, generation, s.now(),
			); err != nil {
				_ = conv.Close()
				return nil, fmt.Errorf("commit fresh provider boundary: %w", err)
			}
			commit.Conversation = controller.conversation
			commit.Conversation.ActiveBranchID = providerBoundary.ID
			commit.Conversation.UpdatedAt = s.now()
		} else if commit.Conversation.ActiveBranchID != providerBoundary.ID {
			_ = conv.Close()
			return nil, fmt.Errorf("commit chat controller: provider boundary %s was not activated", providerBoundary.ID)
		}
	}
	if commit.Conversation.ID != "" {
		// The controller has not been published and its projector has not started,
		// so this is the one safe point where the activation-mutated cache fields
		// move together. Preserve immutable identity from the controller itself:
		// after ControllerReady commits ownership, synchronization must be an
		// infallible assignment rather than another operation that can strand it.
		controller.conversation.ActiveBranchID = commit.Conversation.ActiveBranchID
		controller.conversation.Settings = commit.Conversation.Settings
		controller.conversation.UpdatedAt = commit.Conversation.UpdatedAt
		controller.settings = commit.Conversation.Settings
	}
	if commit.ControllerOwner != (domain.SessionControllerOwner{}) {
		cfg.ExpectedControllerOwner = commit.ControllerOwner
	} else {
		cfg.ExpectedControllerOwner.Harness = cfg.Harness
		cfg.ExpectedControllerOwner.Mode = domain.SessionModeChat
		cfg.ExpectedControllerOwner.IsTerminated = false
		cfg.ExpectedControllerOwner.RuntimeLaunchID = ""
		cfg.ExpectedControllerOwner.ProviderConversationID = controller.ProviderConversationID()
		cfg.ExpectedControllerOwner.ControllerGeneration = controller.Generation()
	}
	s.mu.Lock()
	s.controllers[cfg.SessionID] = controller
	s.startConfigs[cfg.SessionID] = cloneStartConfig(cfg)
	controller.start()
	s.mu.Unlock()

	// Drop the registry entry when the provider stream ends, so a later command
	// reports ErrNoController instead of writing into a dead controller.
	go func() {
		controller.Wait()
		controller.waitForBranchHandoff()
		s.mu.Lock()
		if current, ok := s.controllers[cfg.SessionID]; ok && current == controller {
			delete(s.controllers, cfg.SessionID)
		}
		s.mu.Unlock()
	}()

	return controller, nil
}

// cleanupUnpublishedConversation rolls back a provider opened before its AO
// controller was published. A fresh thread has no durable resume handle and must
// be destroyed; a resumed thread is detached so a transient AO persistence or
// import failure cannot interrupt provider work that was already in flight.
func cleanupUnpublishedConversation(conv ports.ChatConversation, fresh bool) {
	if fresh {
		if terminator, ok := conv.(ports.ChatProviderTerminator); ok {
			_ = terminator.Terminate()
			return
		}
	}
	_ = conv.Close()
}

// Controller returns a session's live controller.
func (s *Service) Controller(sessionID domain.SessionID) (*Controller, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	controller, ok := s.controllers[sessionID]
	if !ok {
		return nil, ErrNoController
	}
	return controller, nil
}

// HasLiveChatController reports whether the service owns a controller that can
// still process provider events. A stopped controller can remain in the registry
// briefly while its final cleanup lands; Start waits for that cleanup before
// replacing it rather than treating the dead entry as a successful resume.
func (s *Service) HasLiveChatController(sessionID domain.SessionID) bool {
	s.mu.RLock()
	controller := s.controllers[sessionID]
	s.mu.RUnlock()
	return controller != nil && controller.State() != ports.ChatControllerStopped
}

// requireChatSession reads the persisted mode and refuses anything that is not a
// Chat session. Dispatch is decided by durable state, never by the caller.
func (s *Service) requireChatSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, error) {
	record, found, err := s.sessions.GetSession(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, fmt.Errorf("read session %s: %w", id, err)
	}
	if !found {
		return domain.SessionRecord{}, ports.ErrSessionNotFound
	}
	if domain.NormalizeSessionMode(record.Mode) != domain.SessionModeChat {
		return domain.SessionRecord{}, ErrNotChatMode
	}
	return record, nil
}

// Send delivers a message to a session's agent.
func (s *Service) Send(
	ctx context.Context,
	id domain.SessionID,
	msg ports.ChatUserMessage,
) (domain.ConversationTurn, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return domain.ConversationTurn{}, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return domain.ConversationTurn{}, err
	}
	return controller.Send(ctx, msg)
}

// Resolve answers a pending approval.
func (s *Service) Resolve(
	ctx context.Context,
	id domain.SessionID,
	requestID string,
	decision ports.ChatDecision,
) error {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return err
	}
	return controller.Resolve(ctx, requestID, decision)
}

// ResolveInput answers a structured user-input request. It remains a separate
// command from approval resolution because the response carries typed form data
// (or URL consent), not a provider-offered permission id.
func (s *Service) ResolveInput(
	ctx context.Context,
	id domain.SessionID,
	requestID string,
	response ports.ChatInputResponse,
) error {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return err
	}
	return controller.ResolveInput(ctx, requestID, response)
}

// Interrupt cancels a session's in-flight turn.
func (s *Service) Interrupt(ctx context.Context, id domain.SessionID) error {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return err
	}
	return controller.Interrupt(ctx)
}

// ArmChatHandoff closes source intake and queue dispatch at
// interface-transition acceptance time. It is a reversible fence; durable queue
// settlement waits until target preflight succeeds.
func (s *Service) ArmChatHandoff(
	ctx context.Context,
	id domain.SessionID,
	policy domain.SessionInterfaceTransitionPolicy,
) error {
	controller, err := s.Controller(id)
	if errors.Is(err, ErrNoController) {
		return nil
	}
	if err != nil {
		return err
	}
	return controller.ArmHandoff(ctx, policy)
}

// PrepareChatHandoff closes source intake and makes the controller quiescent.
// Session Manager remains responsible for stopping it and starting the target;
// keeping that sequencing outside this package preserves the one-writer rule.
func (s *Service) PrepareChatHandoff(
	ctx context.Context,
	id domain.SessionID,
	policy domain.SessionInterfaceTransitionPolicy,
) error {
	controller, err := s.Controller(id)
	if errors.Is(err, ErrNoController) {
		// A dead/missing source controller is already quiescent. Session Manager
		// can safely stop (a no-op) and resume the native conversation in TUI,
		// which is also the useful recovery path for a broken Chat process.
		return nil
	}
	if err != nil {
		return err
	}
	return controller.BeginHandoff(ctx, policy)
}

// AbortChatHandoff reopens the existing controller when the user cancels before
// Session Manager has stopped it.
func (s *Service) AbortChatHandoff(id domain.SessionID) {
	controller, err := s.Controller(id)
	if err == nil {
		controller.AbortHandoff()
	}
}

// Stop closes a session's controller. Safe to call for a session that has none.
func (s *Service) Stop(ctx context.Context, id domain.SessionID) error {
	gate := s.controllerGate(id)
	if err := gate.lock(ctx); err != nil {
		return err
	}
	defer gate.unlock()

	s.mu.RLock()
	controller, ok := s.controllers[id]
	s.mu.RUnlock()
	if !ok {
		s.mu.Lock()
		delete(s.startConfigs, id)
		s.mu.Unlock()
		return nil
	}
	err := controller.Terminate(ctx)

	// Keep the only handle to a controller whose event stream has not ended. A
	// caller can then retry the idempotent close rather than assuming a timeout
	// killed it and launching a second writer. Once stopped, remove only this
	// generation so a concurrent replacement can never be deleted accidentally.
	select {
	case <-controller.stopped:
		s.mu.Lock()
		if current, found := s.controllers[id]; found && current == controller {
			delete(s.controllers, id)
		}
		delete(s.startConfigs, id)
		s.mu.Unlock()
	default:
	}
	return err
}

// StopAll closes every controller, for daemon shutdown.
func (s *Service) StopAll(ctx context.Context) {
	s.mu.Lock()
	controllers := make([]*Controller, 0, len(s.controllers))
	for id, controller := range s.controllers {
		controllers = append(controllers, controller)
		delete(s.controllers, id)
		delete(s.startConfigs, id)
	}
	s.mu.Unlock()

	for _, controller := range controllers {
		if err := controller.Close(ctx); err != nil {
			s.log.Error("failed to close chat controller", "error", err)
		}
	}
}

// Snapshot is the durable read model a client bootstraps from.
type Snapshot struct {
	Conversation                     domain.ConversationRecord
	ActiveBranch                     domain.ConversationBranch
	EditFloorSequence                int64
	NativeForkAvailableAfterSequence int64
	SessionID                        domain.SessionID
	Harness                          domain.AgentHarness
	Mode                             domain.SessionMode
	Controller                       ports.ChatControllerState
	Turns                            []domain.ConversationTurn
	Messages                         []domain.ConversationMessage
	Activities                       []domain.ConversationActivity
	BranchPoints                     []domain.ConversationBranchPoint
	BranchedFromEarlierMessage       bool
	OldestSequence                   int64
	HasMoreBefore                    bool
	// Usage and RateLimits are current state carried on the snapshot the client
	// already polls, rather than timeline entries or a second request. Both are nil
	// until the provider has reported, so a client can tell "not known yet" from a
	// real zero.
	Usage      *domain.ConversationUsage
	RateLimits *domain.ConversationRateLimits
	// Capabilities is what this session's provider can actually do, so a client can
	// decide what to offer BEFORE offering it. Without this the only way to find out
	// is to try: a session whose harness cannot steer would still draw "Steer this
	// turn", take the press, and withdraw the control on the refusal — which reads
	// as a bug rather than as a harness difference. That matters as soon as a second
	// driver lands, and the abilities already differ per install today. Nil when no
	// controller is live, because an unstarted session's abilities are not yet known
	// and guessing them is how a control appears and then vanishes.
	Capabilities ports.ChatCapabilities
}

// SnapshotReader is the durable read the service serves snapshots from. Kept
// separate from Store so the write path and the read path can be satisfied
// independently.
type SnapshotReader interface {
	LoadConversationSnapshot(ctx context.Context, conversationID string) (ConversationRows, error)
}

// SnapshotPageReader is the production bounded-history path. It is separate from
// SnapshotReader so existing tests and recovery tools can still request a full
// snapshot explicitly.
type SnapshotPageReader interface {
	LoadConversationSnapshotPage(ctx context.Context, conversationID string, beforeSequence, limit int64) (ConversationRows, error)
}

// ConversationRows is the raw durable read.
type ConversationRows struct {
	Conversation                     domain.ConversationRecord
	ActiveBranch                     domain.ConversationBranch
	EditFloorSequence                int64
	NativeForkAvailableAfterSequence int64
	Turns                            []domain.ConversationTurn
	Messages                         []domain.ConversationMessage
	Activities                       []domain.ConversationActivity
	BranchPoints                     []domain.ConversationBranchPoint
	BranchedFromEarlierMessage       bool
	OldestSequence                   int64
	HasMoreBefore                    bool
}

// preConversationControllerState answers what a session that has never had a
// conversation should report. Such a session has never had a controller either,
// so "stopped" claims an ending that never happened — and the chat surface paints
// that claim as a crash banner offering to resume an agent that has not yet run.
//
// The reading is taken from durable state alone, never from the row's age: a
// terminated session, and one whose agent process exited, keep reporting stopped
// from the first poll, so a real failure is never masked by a startup grace
// period.
func preConversationControllerState(record domain.SessionRecord) ports.ChatControllerState {
	if record.IsTerminated || record.Activity.State == domain.ActivityExited {
		return ports.ChatControllerStopped
	}
	return ports.ChatControllerConnecting
}

// Snapshot reads a session's conversation.
//
// It does not require a live controller: history must remain readable after the
// agent process is gone, which is the whole point of persisting it. The
// controller state is reported separately so the client can distinguish "no
// history" from "agent not running".
func (s *Service) Snapshot(ctx context.Context, id domain.SessionID) (Snapshot, error) {
	record, err := s.requireChatSession(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}

	conversation, err := s.store.ConversationForSession(ctx, id)
	if errors.Is(err, domain.ErrNoConversation) {
		// A chat session has no conversation until its controller first starts.
		// That is an empty conversation, not a failure — returning an error here
		// would make a brand-new session look broken.
		return Snapshot{
			SessionID:  id,
			Harness:    record.Harness,
			Mode:       domain.NormalizeSessionMode(record.Mode),
			Controller: preConversationControllerState(record),
		}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("conversation for %s: %w", id, err)
	}

	rows, err := s.reader.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load conversation %s: %w", conversation.ID, err)
	}

	state := ports.ChatControllerStopped
	var caps ports.ChatCapabilities
	if controller, err := s.Controller(id); err == nil {
		state = controller.State()
		caps = controller.Capabilities()
	}

	return Snapshot{
		Conversation:                     rows.Conversation,
		ActiveBranch:                     rows.ActiveBranch,
		EditFloorSequence:                rows.EditFloorSequence,
		NativeForkAvailableAfterSequence: rows.NativeForkAvailableAfterSequence,
		SessionID:                        id,
		Harness:                          record.Harness,
		Mode:                             domain.NormalizeSessionMode(record.Mode),
		Controller:                       state,
		Turns:                            rows.Turns,
		Messages:                         rows.Messages,
		Activities:                       rows.Activities,
		BranchPoints:                     rows.BranchPoints,
		BranchedFromEarlierMessage:       rows.BranchedFromEarlierMessage,
		Capabilities:                     caps,
		Usage:                            rows.Conversation.Usage,
		RateLimits:                       rows.Conversation.RateLimits,
	}, nil
}

// SnapshotPage reads one bounded timeline page. The live conversation metadata
// remains current on every page; only turns/messages/activities are windowed.
func (s *Service) SnapshotPage(ctx context.Context, id domain.SessionID, beforeSequence, limit int64) (Snapshot, error) {
	record, err := s.requireChatSession(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	conversation, err := s.store.ConversationForSession(ctx, id)
	if errors.Is(err, domain.ErrNoConversation) {
		return Snapshot{
			SessionID:  id,
			Harness:    record.Harness,
			Mode:       domain.NormalizeSessionMode(record.Mode),
			Controller: preConversationControllerState(record),
		}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("conversation for %s: %w", id, err)
	}
	if s.pageReader == nil {
		// A non-production reader (normally a focused test fake) can retain the
		// old full read. Production always wires PageReader.
		return s.Snapshot(ctx, id)
	}
	rows, err := s.pageReader.LoadConversationSnapshotPage(ctx, conversation.ID, beforeSequence, limit)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load conversation page %s: %w", conversation.ID, err)
	}
	state := ports.ChatControllerStopped
	var caps ports.ChatCapabilities
	if controller, err := s.Controller(id); err == nil {
		state = controller.State()
		caps = controller.Capabilities()
	}
	return Snapshot{
		Conversation:                     rows.Conversation,
		ActiveBranch:                     rows.ActiveBranch,
		EditFloorSequence:                rows.EditFloorSequence,
		NativeForkAvailableAfterSequence: rows.NativeForkAvailableAfterSequence,
		SessionID:                        id,
		Harness:                          record.Harness,
		Mode:                             domain.NormalizeSessionMode(record.Mode),
		Controller:                       state,
		Turns:                            rows.Turns,
		Messages:                         rows.Messages,
		Activities:                       rows.Activities,
		BranchPoints:                     rows.BranchPoints,
		BranchedFromEarlierMessage:       rows.BranchedFromEarlierMessage,
		OldestSequence:                   rows.OldestSequence,
		HasMoreBefore:                    rows.HasMoreBefore,
		Capabilities:                     caps,
		Usage:                            rows.Conversation.Usage,
		RateLimits:                       rows.Conversation.RateLimits,
	}, nil
}

// SnapshotReaderFunc adapts a plain function to SnapshotReader. The daemon wiring
// uses it to convert the store's own snapshot type, so this package never has to
// import the storage layer.
type SnapshotReaderFunc func(ctx context.Context, conversationID string) (ConversationRows, error)

// LoadConversationSnapshot satisfies SnapshotReader.
func (f SnapshotReaderFunc) LoadConversationSnapshot(
	ctx context.Context,
	conversationID string,
) (ConversationRows, error) {
	return f(ctx, conversationID)
}

// SnapshotPageReaderFunc adapts a function to SnapshotPageReader.
type SnapshotPageReaderFunc func(ctx context.Context, conversationID string, beforeSequence, limit int64) (ConversationRows, error)

// LoadConversationSnapshotPage satisfies SnapshotPageReader.
func (f SnapshotPageReaderFunc) LoadConversationSnapshotPage(
	ctx context.Context,
	conversationID string,
	beforeSequence, limit int64,
) (ConversationRows, error) {
	return f(ctx, conversationID, beforeSequence, limit)
}

/* ---- session_manager.ChatLauncher ---------------------------------------- */

// The methods below let the session manager launch a chat controller during spawn
// without importing this package's config types. They are deliberately narrow:
// the manager decides when, this package decides how.

// SupportsChat reports whether a harness has a Chat driver at all, without
// probing the local install. Use it to decide whether Chat is even offerable.
func (s *Service) SupportsChat(harness domain.AgentHarness) bool {
	return s.drivers.SupportsChat(harness)
}

// PreflightChat reports whether a harness can start in chat mode right now.
//
// Called before any durable state exists, so an unsupported request costs nothing
// — no terminated orphan row, no wasted worktree. It never downgrades to TUI:
// that would put the user in a terminal they did not ask for.
func (s *Service) PreflightChat(
	ctx context.Context,
	harness domain.AgentHarness,
	permissions ports.PermissionMode,
) error {
	driver, err := s.drivers.Driver(harness)
	if err != nil {
		return fmt.Errorf("%w: %s has no chat driver", ports.ErrChatUnsupported, harness)
	}
	caps, err := s.driverCapabilities(ctx, harness, driver)
	if err != nil {
		return err
	}
	return capabilityAdmissionError(harness, caps, permissions)
}

func capabilityAdmissionError(
	harness domain.AgentHarness,
	caps ports.ChatCapabilities,
	permissions ports.PermissionMode,
) error {
	missing := ports.MissingCapabilitiesForPermissions(caps, permissions)
	if len(missing) == 0 {
		return nil
	}
	var allowed []ports.PermissionMode
	if ports.NormalizePermissionMode(permissions) != ports.PermissionModeBypassPermissions &&
		len(ports.MissingCapabilitiesForPermissions(caps, ports.PermissionModeBypassPermissions)) == 0 {
		allowed = []ports.PermissionMode{ports.PermissionModeBypassPermissions}
	}
	return &ports.ChatCapabilityError{
		Harness:                harness,
		Missing:                append([]ports.ChatCapability(nil), missing...),
		AllowedPermissionModes: allowed,
	}
}

// driverCapabilities performs the provider capability probe once per harness for
// the lifetime of this service. Reconciliation can resume many sessions using
// the same provider; launching a throwaway provider process for every one makes
// startup scale with twice the number of sessions. Only successful probes are
// cached, so a repaired install can be retried without a daemon restart. The raw
// capabilities are cached because admission depends on each session's requested
// permission mode.
func (s *Service) driverCapabilities(
	ctx context.Context,
	harness domain.AgentHarness,
	driver ports.ChatDriver,
) (ports.ChatCapabilities, error) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if caps, ok := s.probed[harness]; ok {
		return caps, nil
	}
	caps, err := driver.Probe(ctx)
	if err != nil {
		return nil, err
	}
	s.probed[harness] = caps
	return caps, nil
}

// StartChat launches the controller for a freshly created session.
func (s *Service) StartChat(ctx context.Context, cfg StartRequest) (StartResult, error) {
	controller, err := s.Start(ctx, StartConfig(cfg))
	if err != nil {
		return StartResult{}, err
	}
	return controllerStartResult(controller, nil, nil), nil
}

// StartRequest mirrors session_manager.ChatStart. Duplicated rather than
// imported so the manager and this service do not depend on each other's types.
type StartRequest struct {
	SessionID               domain.SessionID
	ProjectID               domain.ProjectID
	Kind                    domain.SessionKind
	Harness                 domain.AgentHarness
	DataDir                 string
	WorkspacePath           string
	Env                     map[string]string
	Model                   string
	Effort                  string
	Permissions             ports.PermissionMode
	SystemPrompt            string
	AdditionalDirectories   []string
	MCPServers              []ports.ChatMCPServerConfig
	ExpectedControllerOwner domain.SessionControllerOwner
	PrepareControllerEnv    func(context.Context, domain.SessionControllerOwner) (map[string]string, error)
	// ProviderConversationID resumes a stored conversation. Empty starts fresh.
	ProviderConversationID  string
	ProviderScopeID         string
	ControllerGeneration    string
	RequireNativeHistory    bool
	SkipNativeHistoryImport bool
	// ControllerReady runs after the provider and generation exist but before
	// live event projection starts.
	ControllerReady func(StartResult) (ControllerCommit, error)
}

// StartResult is the durable outcome of a launch.
type StartResult struct {
	ProviderConversationID string
	ControllerGeneration   string
	Conversation           domain.ConversationRecord
	// ProviderBoundary is non-nil when this launch owns a provider namespace
	// that is not active yet. ControllerReady must commit it atomically with the
	// session's provider handle and controller generation.
	ProviderBoundary *domain.ConversationBranch
	// CommitProviderHistory projects a reconciled native replay inside the
	// provider-boundary lifecycle transaction. It must never be invoked outside
	// that transaction: the pending branch and generation do not exist yet.
	CommitProviderHistory func(context.Context) error
}

// StartChatTurn delivers the initial prompt as a normal turn.
//
// It goes through the controller rather than the mode-gated Send, because the
// session row is still being written when spawn calls this: reading the persisted
// mode here would race the write that sets it.
func (s *Service) StartChatTurn(ctx context.Context, id domain.SessionID, text string) (string, error) {
	controller, err := s.Controller(id)
	if err != nil {
		return "", err
	}
	turn, err := controller.Send(ctx, ports.ChatUserMessage{
		Text: text,
		// The initial prompt is the user's task brief. Origin records who AUTHORED
		// a message, not who delivered it — the daemon carrying it to the provider
		// no more makes it the daemon's message than the network makes it the
		// network's. Attributing it to the daemon rendered the user's own request
		// as a system notice.
		Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		return "", err
	}
	return turn.ID, nil
}

// ErrModelsUnsupported reports a driver whose provider cannot enumerate models.
// Distinct from an empty list: "this agent does not offer a choice" is a different
// answer for a client to render than "you have no models available".
var ErrModelsUnsupported = errors.New("chat driver cannot list models")

// ErrConfigOptionsUnsupported reports a conversation whose provider does not
// advertise live session controls. This is an ordinary capability answer: native
// drivers can continue using AO's model/settings surface.
var ErrConfigOptionsUnsupported = errors.New("chat driver has no session config options")

// Models reports what the provider offers for this session, plus what is selected.
//
// Read from the live conversation rather than a table in AO: models are added,
// renamed, hidden per account and gated by entitlement the provider knows about.
func (s *Service) Models(ctx context.Context, id domain.SessionID) ([]ports.ChatModel, domain.ConversationSettings, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return nil, domain.ConversationSettings{}, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return nil, domain.ConversationSettings{}, err
	}
	lister, ok := controller.conv.(ports.ChatModelLister)
	if !ok {
		return nil, controller.Settings(), ErrModelsUnsupported
	}
	models, err := lister.ListModels(ctx)
	if err != nil {
		return nil, controller.Settings(), err
	}
	return models, controller.Settings(), nil
}

// ConfigOptions reports the provider's live session controls. Unlike AO's
// durable turn settings, these are provider-owned session state and are read from
// the connected conversation so model entitlements and model-dependent choices
// cannot go stale in an AO table.
func (s *Service) ConfigOptions(ctx context.Context, id domain.SessionID) ([]ports.ChatConfigOption, error) {
	record, err := s.requireChatSession(ctx, id)
	if err != nil {
		return nil, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return nil, err
	}
	configurer, ok := controller.conv.(ports.ChatConfigOptionController)
	if !ok {
		return nil, ErrConfigOptionsUnsupported
	}
	options, err := configurer.ListConfigOptions(ctx)
	return permissionConfigOptions(record.Harness, options), err
}

// SetConfigOption applies one provider-advertised value and returns the complete
// post-change catalog. Callers replace their list because changing the model can
// add or remove effort, fast-mode, and other dependent controls.
func (s *Service) SetConfigOption(
	ctx context.Context,
	id domain.SessionID,
	configID string,
	value ports.ChatConfigOptionValue,
) ([]ports.ChatConfigOption, error) {
	record, err := s.requireChatSession(ctx, id)
	if err != nil {
		return nil, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return nil, err
	}
	configurer, ok := controller.conv.(ports.ChatConfigOptionController)
	if !ok {
		return nil, ErrConfigOptionsUnsupported
	}
	controller.configMu.Lock()
	defer controller.configMu.Unlock()
	options, err := configurer.SetConfigOption(ctx, configID, value)
	if err != nil {
		return nil, err
	}
	options = permissionConfigOptions(record.Harness, options)
	if settings, changed := settingsFromConfigOptions(controller.Settings(), options); changed {
		if err := controller.SetSettings(ctx, settings); err != nil {
			return nil, err
		}
	}
	return options, nil
}

func settingsFromConfigOptions(
	settings domain.ConversationSettings,
	options []ports.ChatConfigOption,
) (domain.ConversationSettings, bool) {
	next := settings
	for _, option := range options {
		for _, choice := range option.Choices {
			if choice.Value == option.Current.Select && choice.PermissionMode != "" {
				next.ApprovalMode = choice.PermissionMode
			}
		}
		switch {
		case option.ID == "model" || option.Category == "model":
			if option.Current.Select != "" {
				next.Model = option.Current.Select
			}
		case option.ID == "effort" || option.Category == "thought_level":
			if option.Current.Select != "" {
				next.ReasoningEffort = option.Current.Select
			}
		}
	}
	return next, next != settings
}

// Compact asks the provider to summarize earlier history and reclaim context.
//
// Why this exists at all: every turn re-sends the whole conversation, so context
// fills on its own and a long conversation eventually cannot accept another turn.
// Compaction is the difference between a session that works for an hour and one
// that works for a day.
//
// The result reports what is about to be reclaimed, not what was. The provider
// accepts the request immediately and does the work as its own turn over the next
// ten seconds or so; the settled figures arrive on the timeline as a compaction
// entry.
func (s *Service) Compact(ctx context.Context, id domain.SessionID) (ports.ChatCompactionResult, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return ports.ChatCompactionResult{}, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return ports.ChatCompactionResult{}, err
	}
	return controller.Compact(ctx)
}

// ReloadMCPServers restarts the provider's tool servers for this session.
//
// The failure it addresses is not the agent's. An MCP server that fails to start
// stays failed for the life of the provider process, so a session loses a tool for
// good and the only other way back is to throw the conversation away. Same for a
// server whose config changed on disk, or one whose auth expired: the agent has no
// way to notice and nothing it can do about it.
//
// It returns the servers' state so a caller sees the outcome without polling,
// though the provider's own startup notifications remain the authoritative report
// and land on the conversation regardless of who asked.
func (s *Service) ReloadMCPServers(
	ctx context.Context,
	id domain.SessionID,
) ([]domain.ConversationMCPServer, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return nil, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return nil, err
	}
	return controller.ReloadMCPServers(ctx)
}

// RetryTurn re-dispatches a failed turn's durable prompt as a new turn.
// The content is loaded from AO's own rows, never from the caller, so the daemon
// owns what gets sent again. The current next-turn settings apply.
func (s *Service) RetryTurn(
	ctx context.Context,
	id domain.SessionID,
	turnID string,
) (domain.ConversationTurn, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return domain.ConversationTurn{}, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return domain.ConversationTurn{}, err
	}
	result, err := controller.RetryTurn(ctx, turnID)
	if err != nil {
		return domain.ConversationTurn{}, err
	}
	return result, nil
}

// SetTurnSettings records the provider choices for this session's next turn.
//
// Applied per turn, so nothing restarts: the running turn keeps whatever it was
// dispatched with, and the choice takes effect on the next one.
func (s *Service) SetTurnSettings(
	ctx context.Context,
	id domain.SessionID,
	settings domain.ConversationSettings,
) (domain.ConversationSettings, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return domain.ConversationSettings{}, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return domain.ConversationSettings{}, err
	}
	if err := controller.SetSettings(ctx, settings); err != nil {
		return domain.ConversationSettings{}, err
	}
	return controller.Settings(), nil
}

// RelayChatTurn delivers a message AO is carrying for someone else.
//
// Origin is automation, not human: `ao send` and an orchestrator writing to a
// worker are AO acting on the user's instructions, and the timeline attributes
// them so rather than passing them off as something the user typed here. The
// distinction is durable and structural — a reader must not have to infer it
// from a text prefix.
//
// Delivery follows the same rules as any other send: a message arriving mid-turn
// queues instead of racing the running turn.
func (s *Service) RelayChatTurn(ctx context.Context, id domain.SessionID, text string) (string, error) {
	return s.RelayChatTurnWithID(ctx, id, text, "")
}

// RelayChatTurnWithID is RelayChatTurn with a durable caller-supplied
// idempotency key. Interface-transition outbox retries use it so a crash after
// provider acceptance but before the outbox acknowledgement cannot create a
// duplicate Chat turn.
func (s *Service) RelayChatTurnWithID(
	ctx context.Context,
	id domain.SessionID,
	text, clientMessageID string,
) (string, error) {
	controller, err := s.Controller(id)
	if err != nil {
		return "", err
	}
	turn, err := controller.Send(ctx, ports.ChatUserMessage{
		Text:            text,
		ClientMessageID: clientMessageID,
		Origin:          domain.MessageOriginAutomation,
	})
	if err != nil {
		return "", err
	}
	return turn.ID, nil
}

// StopChat releases a session's controller.
func (s *Service) StopChat(ctx context.Context, id domain.SessionID) error {
	return s.Stop(ctx, id)
}

// permissionConfigOptions annotates only provider controls whose semantics are
// known. In particular, plan and dontAsk are not AO approval policies.
func permissionConfigOptions(harness domain.AgentHarness, options []ports.ChatConfigOption) []ports.ChatConfigOption {
	out := append([]ports.ChatConfigOption(nil), options...)
	for i := range out {
		out[i].Choices = append([]ports.ChatConfigOptionChoice(nil), out[i].Choices...)
		for j := range out[i].Choices {
			out[i].Choices[j].PermissionMode = ""
			if harness != domain.HarnessClaudeCode || out[i].ID != "mode" {
				continue
			}
			switch out[i].Choices[j].Value {
			case "manual", "default":
				out[i].Choices[j].PermissionMode = domain.PermissionModeDefault
			case "acceptEdits":
				out[i].Choices[j].PermissionMode = domain.PermissionModeAcceptEdits
			case "auto":
				out[i].Choices[j].PermissionMode = domain.PermissionModeAuto
			case "bypassPermissions":
				out[i].Choices[j].PermissionMode = domain.PermissionModeBypassPermissions
			}
		}
	}
	return out
}
