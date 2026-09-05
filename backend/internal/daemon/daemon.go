// Package daemon owns the Agent Orchestrator backend process: config loading,
// loopback HTTP serving, durable storage, CDC fan-out, lifecycle wiring, and
// graceful shutdown.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	codexagent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/modelcatalog"
	chatdriveracp "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/claudeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver"
	chatdriverregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/runtimeselect"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/systemexec"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/telemetry/policyauthority"
	"github.com/aoagents/agent-orchestrator/backend/internal/autoreview"
	"github.com/aoagents/agent-orchestrator/backend/internal/browserruntime"
	"github.com/aoagents/agent-orchestrator/backend/internal/codexops"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/daemon/supervisor"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/notify"
	agentswitchobs "github.com/aoagents/agent-orchestrator/backend/internal/observe/agentswitch"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/sentryobs"
	usagepipeline "github.com/aoagents/agent-orchestrator/backend/internal/observe/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/presence"
	"github.com/aoagents/agent-orchestrator/backend/internal/preview"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/push"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentauth"
	browsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	devimportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/devimport"
	importsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/importer"
	notificationsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/notification"
	prsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/pr"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	settingssvc "github.com/aoagents/agent-orchestrator/backend/internal/service/settings"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systemcheck"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/skillassets"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/terminal"
)

// sentryEnvironment maps the daemon's app version to a Sentry environment so a
// nightly/edge build's issues do not mix with stable release health.
func sentryEnvironment(version string) string {
	v := strings.ToLower(strings.TrimSpace(version))
	switch {
	case v == "":
		return "unknown"
	case strings.Contains(v, "nightly"):
		return "nightly"
	case strings.Contains(v, "edge") || strings.Contains(v, "pr"):
		return "development"
	default:
		return "stable"
	}
}

func agentSwitchEventMetadata(cfg config.Config) domain.AgentSwitchEventMetadata {
	environment := domain.AgentSwitchEnvironmentStable
	channel := domain.AgentSwitchChannelStable
	version := strings.ToLower(strings.TrimSpace(cfg.Telemetry.AppVersion))
	switch {
	case strings.Contains(version, "nightly"):
		environment, channel = domain.AgentSwitchEnvironmentNightly, domain.AgentSwitchChannelNightly
	case strings.Contains(version, "edge"), strings.Contains(version, "-pr"):
		environment, channel = domain.AgentSwitchEnvironmentDevelopment, domain.AgentSwitchChannelPreview
	}
	osName := domain.AgentSwitchOS(runtime.GOOS)
	return domain.AgentSwitchEventMetadata{
		Release: cfg.Telemetry.AppVersion, Environment: environment, Channel: channel,
		Platform: domain.AgentSwitchPlatformDaemon, OS: osName,
		ElapsedTimeBucket: domain.AgentSwitchElapsedNotApplicable,
	}
}

func agentSwitchFailureStreamDisabled(disabled []string) bool {
	for _, name := range disabled {
		if name == "ao.agent_switch.failure" || name == "ao.agent_switch.*" || name == "*" {
			return true
		}
	}
	return false
}

type agentSwitchDaemonFaultEnqueuer interface {
	EnqueueAgentSwitchDaemonFault(context.Context, ports.AgentSwitchDaemonFault) (ports.AgentSwitchMutationResult, error)
}

func agentSwitchWorkerWaitTimedOut(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

func enqueueAgentSwitchWorkerShutdownTimeout(
	ctx context.Context,
	store agentSwitchDaemonFaultEnqueuer,
	policy ports.AgentSwitchReportingPolicy,
	daemonRunID string,
	at time.Time,
) error {
	if store == nil || policy == nil || strings.TrimSpace(daemonRunID) == "" {
		return nil
	}
	fault := domain.AgentSwitchFault{
		ReportKind:           domain.AgentSwitchReportDaemonLifecycleFailure,
		FailurePoint:         domain.AgentSwitchFailureShutdownWorkerTimeout,
		ClassifierCallsite:   domain.AgentSwitchClassifierDaemonShutdown,
		Phase:                domain.AgentSwitchStateNotApplicable,
		ErrorCode:            domain.AgentSwitchErrorNotApplicable,
		FaultCode:            domain.AgentSwitchFaultShutdownWorkersTimedOut,
		Execution:            domain.AgentSwitchExecutionDaemonShutdown,
		Mode:                 domain.SessionModeNotApplicable,
		FromHarness:          domain.HarnessNotApplicable,
		TargetHarness:        domain.HarnessNotApplicable,
		TargetStartMode:      domain.AgentSwitchTargetStartNotApplicable,
		RuntimeBackend:       domain.AgentSwitchRuntimeNotApplicable,
		CallOutcome:          domain.AgentSwitchCallTimedOut,
		Ownership:            domain.AgentSwitchOwnershipNotApplicable,
		Compensation:         domain.AgentSwitchCompensationNotApplicable,
		UserImpact:           domain.AgentSwitchUserImpactNotApplicable,
		SourceStopConfirmed:  domain.AgentSwitchTriNotApplicable,
		TargetOwnerCommitted: domain.AgentSwitchTriNotApplicable,
		GateRetained:         domain.AgentSwitchTriNotApplicable,
		OccurredAt:           at,
		Frames: []domain.AgentSwitchStackFrame{{
			Package: "daemon", Function: "Run",
			Filename: "backend/internal/daemon/daemon.go", Line: 1,
		}},
	}
	_, err := store.EnqueueAgentSwitchDaemonFault(ctx, ports.AgentSwitchDaemonFault{
		DaemonRunID:   daemonRunID,
		Fault:         fault,
		Authorization: policy.Authorization(),
	})
	return err
}

// Run starts the daemon and blocks until it exits. SIGINT/SIGTERM drive
// graceful shutdown through the HTTP server and background workers.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cwd, err := os.Getwd(); err == nil {
		cfg.StartupWorkingDirectory = cwd
	}
	if err := stabilizeWorkingDirectory(cfg.DataDir); err != nil {
		return err
	}
	ignoreBrokenPipeSignal()

	log := newLogger()
	var browserRuntimeToken string
	if os.Getenv(browserruntime.RuntimeTokenStdinEnv) == "1" {
		browserRuntimeToken, err = browserruntime.ReadRuntimeToken(os.Stdin)
		if err != nil {
			return err
		}
	}
	if browserRuntimeToken == "" {
		browserRuntimeToken, err = browserruntime.NewToken()
		if err != nil {
			return err
		}
	}
	browserAuthority := browsersvc.NewAuthority()
	browserBroker := browserruntime.New(log, browserRuntimeToken)

	// Fail fast only if a daemon is genuinely still serving the recorded port.
	// CheckStale confirms the run-file's PID is alive, but that alone is not
	// proof a predecessor owns the port: the file leaks when the daemon is hard
	// killed without a graceful shutdown (the norm on Windows, where the desktop
	// supervisor can only TerminateProcess it), and Windows reuses the recorded
	// PID for unrelated processes. So a "live" PID is verified against an actual
	// /healthz probe; a run-file left by a crashed/hard-killed/reused-PID
	// predecessor is treated as stale and overwritten when the new server starts.
	if live, err := runfile.CheckStale(cfg.RunFilePath); err != nil {
		return fmt.Errorf("inspect run-file: %w", err)
	} else if live != nil && runFileOwnerServing(&http.Client{Timeout: staleProbeTimeout}, config.LoopbackHost, live) {
		return fmt.Errorf("daemon already running (pid %d, port %d); refusing to start", live.PID, live.Port)
	}

	// Open the durable store and bring up the CDC substrate: DB triggers capture
	// changes into change_log, the poller tails it, and the broadcaster fans
	// events out to live transports.
	store, err := sqlite.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.ConfigureAgentSwitchFailureEventEncoder(context.Background(), sentryobs.AgentSwitchEventEncoder{}); err != nil {
		return fmt.Errorf("configure agent switch failure event encoder: %w", err)
	}

	// Consent and event metadata are established before any reporting surface or
	// recovery enrollment exists. SQLite is forced off first on every boot so a
	// stale enabled mirror can never authorize work after a restart.
	destination, destinationErr := sentryobs.ParseAgentSwitchDSN(cfg.Telemetry.SentryDSN, true)
	if destinationErr != nil && cfg.Telemetry.SentryDSN != "" {
		log.Warn("agent switch failure sender disabled", "error", destinationErr)
	}
	policyCoordinator := agentswitchobs.NewPolicyCoordinator(store, agentswitchobs.PolicyOptions{
		AuthorityReader:         policyauthority.New(filepath.Join(cfg.DataDir, agentswitchobs.PolicyFileName)),
		TelemetryEvents:         cfg.Telemetry.Events,
		TelemetryEventsExplicit: cfg.Telemetry.EventsExplicit,
		DestinationFingerprint:  destination.Fingerprint,
		StreamKillSwitched:      agentSwitchFailureStreamDisabled(cfg.Telemetry.DisabledEvents),
		Metadata:                agentSwitchEventMetadata(cfg),
		OnEventsChanged:         sentryobs.SetPolicyEnabled,
		ProviderDrain:           sentryobs.Drain,
	})
	if err := policyCoordinator.ForceDisabled(context.Background()); err != nil {
		return fmt.Errorf("force agent switch reporting disabled: %w", err)
	}
	if err := policyCoordinator.Synchronize(context.Background()); err != nil && !errors.Is(err, agentswitchobs.ErrPolicyUnavailable) {
		return fmt.Errorf("synchronize agent switch reporting policy: %w", err)
	}

	// Refresh the embedded using-ao skill into the data dir so worker sessions
	// in any project can read the ao CLI catalog from a stable absolute path.
	// Non-fatal: the skill is an enhancement over `ao --help`, not required.
	if err := skillassets.Install(cfg.DataDir); err != nil {
		log.Warn("install using-ao skill", "err", err)
	}

	telemetryCfg := cfg
	telemetryCfg.Telemetry.Events = policyCoordinator.EventsEnabled()
	telemetrySink := newTelemetrySink(telemetryCfg, store, log)
	defer func() { _ = telemetrySink.Close(context.Background()) }()
	// Daemon Sentry: captures genuine 5xx/panics with their Go stack. Gated on
	// Initialize the transport once so a later policy opt-in works without a
	// daemon restart. The policy gate remains fail-closed and is checked before
	// every capture; a blank DSN still leaves this as a no-op.
	if err := sentryobs.Init(sentryobs.Config{
		DSN:         cfg.Telemetry.SentryDSN,
		Release:     cfg.Telemetry.AppVersion,
		Environment: sentryEnvironment(cfg.Telemetry.AppVersion),
	}); err != nil {
		log.Warn("daemon sentry disabled", "err", err)
	}
	defer sentryobs.Flush(2 * time.Second)
	telemetrySink.Emit(context.Background(), ports.TelemetryEvent{
		Name:       "ao.daemon.started",
		Source:     "daemon",
		OccurredAt: time.Now().UTC(),
		Level:      ports.TelemetryLevelInfo,
		Payload: map[string]any{
			"port":  cfg.Port,
			"agent": cfg.Agent,
		},
	})

	// signal.NotifyContext cancels ctx on SIGINT/SIGTERM, which drives the
	// graceful shutdown inside Server.Run and stops the background goroutines.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	policyCoordinator.StartWatcher(ctx)
	defer func() { _ = policyCoordinator.CloseAndDrain(context.Background()) }()
	// Constructing the synchronous sender performs no I/O. The hard production
	// gate keeps this dormant until the separate privacy and destination release
	// gates are approved; each call remains guarded by durable consent below.
	var agentSwitchObserver ports.AgentSwitchFailureObserver
	if domain.AgentSwitchFailureProductionEnabled && destinationErr == nil {
		agentSwitchObserver = sentryobs.NewAgentSwitchFailureSender(destination, nil)
	}
	agentSwitchDispatcher, err := newAgentSwitchFailureDispatcher(store, policyCoordinator, agentSwitchObserver, log)
	if err != nil {
		return fmt.Errorf("wire agent switch failure dispatcher: %w", err)
	}
	if agentSwitchDispatcher != nil {
		agentSwitchDispatcher.Start(ctx)
		defer func() {
			stopContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			if stopErr := agentSwitchDispatcher.Stop(stopContext); stopErr != nil {
				log.Error("agent switch failure dispatcher shutdown", "error", stopErr)
			}
		}()
	}

	cdcPipe, err := startCDC(ctx, store, log)
	if err != nil {
		return err
	}

	// Terminal streaming: the selected platform runtime supplies the
	// attach Stream and liveness; the CDC broadcaster feeds the session-state channel. The manager
	// is handed to httpd, which mounts it at /mux. Raw PTY bytes never flow
	// through the CDC change_log -- only session-state events do.
	runtimeAdapter := runtimeselect.New(log, cfg.RunFilePath)
	managedPreview := previewserver.New(log, cfg.DataDir)
	termMgr := terminal.NewManager(runtimeAdapter, cdcPipe.Broadcaster, log)
	defer termMgr.Close()

	// The agent messenger sends validated user input to the session's live
	// runtime pane. Keep this path small until durable inbox semantics are needed.
	// Built before the Lifecycle Manager so the LCM can use it for SCM-driven
	// agent nudges (CI failure, review feedback, merge conflict).
	messenger := newSessionMessenger(store, runtimeAdapter, log)
	lifecycleMessenger := newModeAwareMessenger()
	notificationHub := notify.NewHub()
	notifier := notificationsvc.New(notificationsvc.Deps{Store: store})
	notificationWriter := notify.New(notify.Deps{Store: store, Publisher: notificationHub})
	// Resolution transitions that happened while the daemon was down never
	// reached lifecycle, so re-check open notifications against the durable
	// session/PR facts before serving. Best-effort: a failure here only leaves
	// stale rows in the unresolved list, never blocks startup.
	if err := notificationWriter.Reconcile(ctx); err != nil {
		log.Warn("notification resolution reconcile failed", "err", err)
	}

	// Bring up the Lifecycle Manager and the reaper first: it makes the session
	// lifecycle write path live (reducer write -> store -> DB trigger ->
	// change_log -> poller -> broadcaster) and gives startSession the shared LCM.
	// The agent resolver is built before the LCM so lifecycle can consume the
	// adapter-declared active-turn steering capability; startSession reuses it.
	defaultAgent := cfg.Agent
	if defaultAgent == "" {
		defaultAgent = config.DefaultAgent
	}
	agents, err := buildAgentResolver(defaultAgent, log)
	if err != nil {
		stop()
		if cdcErr := cdcPipe.Stop(); cdcErr != nil {
			log.Error("cdc pipeline shutdown", "err", cdcErr)
		}
		return fmt.Errorf("wire agent resolver: %w", err)
	}

	lcStack := startLifecycle(ctx, store, runtimeAdapter, lifecycleMessenger, notificationWriter, telemetrySink, agents, log)

	// Wire the controller-facing session service over the same store + LCM, the
	// selected runtime, routed git/scratch workspaces, the per-session agent
	// resolver (AO_AGENT validated here for compatibility), and the agent
	// messenger, then mount it on the API.
	chatDrivers := chatdriverregistry.Build(log)

	// Daemon-owned preferences. The store's type is field-compatible with the
	// service's, adapted here so neither package imports the other. Offering
	// gates ride along: they are boot-time config, not stored preferences.
	settingsSvc := settingssvc.New(
		settingsStore{store: store},
		chatDrivers,
		settingssvc.OfferingFromConfig(cfg),
		func() time.Time { return time.Now().UTC() },
	)

	// Chat service. The driver registry is the capability gate: a harness with no
	// registered driver cannot start in chat mode, so an unsupported request fails
	// loudly instead of silently becoming a TUI session.
	var agentSvc *agentsvc.Service
	var sessMgr sessionLifecycle
	chatSvc := chatsvc.New(chatsvc.Options{
		Store:    store,
		Sessions: store,
		// Adapts the store's own snapshot type, so the chat service never has to
		// import the storage layer.
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			rows, err := store.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation:                     rows.Conversation,
				ActiveBranch:                     rows.ActiveBranch,
				EditFloorSequence:                rows.EditFloorSequence,
				NativeForkAvailableAfterSequence: rows.NativeForkAvailableAfterSequence,
				Turns:                            rows.Turns,
				Messages:                         rows.Messages,
				Activities:                       rows.Activities,
				BranchPoints:                     rows.BranchPoints,
				BranchedFromEarlierMessage:       rows.BranchedFromEarlierMessage,
			}, nil
		}),
		PageReader: chatsvc.SnapshotPageReaderFunc(func(ctx context.Context, conversationID string, beforeSequence, limit int64) (chatsvc.ConversationRows, error) {
			rows, err := store.LoadConversationSnapshotPage(ctx, conversationID, beforeSequence, limit)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation:                     rows.Conversation,
				ActiveBranch:                     rows.ActiveBranch,
				EditFloorSequence:                rows.EditFloorSequence,
				NativeForkAvailableAfterSequence: rows.NativeForkAvailableAfterSequence,
				Turns:                            rows.Turns,
				Messages:                         rows.Messages,
				Activities:                       rows.Activities,
				BranchPoints:                     rows.BranchPoints,
				BranchedFromEarlierMessage:       rows.BranchedFromEarlierMessage,
				OldestSequence:                   rows.OldestSequence,
				HasMoreBefore:                    rows.HasMoreBefore,
			}, nil
		}),
		Drivers: chatDrivers,
		// The LCM satisfies ActivityRecorder directly: a chat turn is a pure
		// lifecycle reduction, same as a hook signal from a terminal session.
		Activity: lcStack.LCM,
		Log:      log,
		NewID:    uuid.NewString,
		OnAccountChanged: func(sessionID domain.SessionID, generation string, harness domain.AgentHarness) {
			if harness != domain.HarnessCodex || agentSvc == nil || sessMgr == nil || sessMgr.CodexAccountSwitchInProgress() {
				return
			}
			rec, ok, readErr := store.GetSession(ctx, sessionID)
			if readErr == nil && ok && rec.Harness == domain.HarnessCodex && rec.Metadata.ControllerGeneration == generation {
				agentSvc.InvalidateCodexAccountAuthentication()
			}
		},
		OnCodexCapacityChanged: func(sessionID domain.SessionID, generation string, observation ports.CodexCapacityObservation) {
			if agentSvc == nil || sessMgr == nil || sessMgr.CodexAccountSwitchInProgress() {
				return
			}
			rec, ok, readErr := store.GetSession(ctx, sessionID)
			if readErr != nil || !ok || rec.Harness != domain.HarnessCodex || rec.Metadata.ControllerGeneration != generation {
				return
			}
			agentSvc.ObserveActiveCodexAccountCapacity(observation)
		},
	})

	codexModelDriver := codexappserver.New(codexagent.New(), log)
	modelDiscoverer := modelcatalog.Discoverer{
		ClaudeOptions: func(listCtx context.Context, request ports.AgentModelDiscoveryRequest) ([]ports.ChatConfigOption, error) {
			return claudeacp.DiscoverConfigOptions(listCtx, request.Binary, request.WorkingDir, request.Env, log)
		},
		CodexModels: func(listCtx context.Context, request ports.AgentModelDiscoveryRequest) ([]ports.ChatModel, error) {
			return codexModelDriver.DiscoverModels(listCtx, request.WorkingDir, request.Env)
		},
		ClineOptions: func(listCtx context.Context, request ports.AgentModelDiscoveryRequest) ([]ports.ChatConfigOption, error) {
			return chatdriveracp.DiscoverConfigOptions(listCtx, chatdriveracp.Launch{
				Command: request.Binary,
				Args:    []string{"--acp"},
				Env:     request.Env,
			}, request.WorkingDir, log)
		},
	}
	// Build the multi-tracker dispatching to both GitHub and GitLab once,
	// shared between the session service and the intake observer below.
	// Env-configured tokens are validated eagerly here; CLI credential probing
	// (`gh auth token`) stays lazy inside the multi-tracker so boot is not
	// blocked. May be nil (no usable credentials) — the session service's
	// nil-guard and the intake resolver's backoff both tolerate that
	// (issue #2685).
	tracker := newMultiTracker(cfg.GitLab, log)
	codexPlugin := codexagent.New()
	codexHome, err := codexPlugin.NativeSessionConfigDir(ctx, nil)
	if err != nil {
		stop()
		lcStack.Stop()
		if cdcErr := cdcPipe.Stop(); cdcErr != nil {
			log.Error("cdc pipeline shutdown", "err", cdcErr)
		}
		return fmt.Errorf("resolve device-global Codex home: %w", err)
	}
	codexOperationGate := codexops.NewGate()
	agentDeps := agentsvc.Deps{
		Cache: store, Discoverer: modelDiscoverer, Projects: store, Sessions: store, Context: ctx, Logger: log,
		CodexAccountRoot:       filepath.Join(cfg.StateDir, "harnesses", "codex", "accounts"),
		CodexPendingRoot:       filepath.Join(cfg.StateDir, "harnesses", "codex", "pending-accounts"),
		CodexSwitchStagingRoot: filepath.Join(cfg.StateDir, "harnesses", "codex", "switch-staging"),
		CodexGlobalHome:        codexHome, CodexAccountState: store,
		CodexAccounts: codexappserver.NewAccountFactoryWithResolver(func(resolveCtx context.Context) (string, error) {
			return codexagent.New().ResolveBinary(resolveCtx)
		}, log),
		CodexOperationGate: codexOperationGate,
	}
	agentSvc = agentsvc.NewWithDeps(agentDeps)
	agentSvc.WarmModelCatalogs(ctx)

	sessionSvc, reviewSvc, wiredSessMgr, err := startSession(ctx, cfg, runtimeAdapter, store, lcStack.LCM, messenger, telemetrySink, agents, agentSvc, managedPreview, browserBroker, browserAuthority, chatLauncher{svc: chatSvc}, settingsSvc, policyCoordinator, tracker, codexOperationGate, log)
	if err != nil {
		stop()
		lcStack.Stop()
		if cdcErr := cdcPipe.Stop(); cdcErr != nil {
			log.Error("cdc pipeline shutdown", "err", cdcErr)
		}
		return fmt.Errorf("wire session service: %w", err)
	}
	sessMgr = wiredSessMgr

	// servers isn't clobbered. See preview_wiring.go (issue #4500).
	wireManagedPreviewExit(managedPreview, sessionSvc, log)
	sessMgr.SetTerminalInputGate(termMgr)
	agentSvc.SetCodexAccountSwitchCoordinator(sessMgr)
	sessMgr.SetCodexAccountSwitchObserver(agentSvc.PublishCodexAccounts)
	lifecycleMessenger.Bind(sessionLifecycleMessenger{sessMgr})
	lcStack.LCM.SetCompletionTerminator(sessMgr)
	lcStack.LCM.SetSessionInputLease(sessMgr)
	lcStack.LCM.SetSessionOperationGate(sessMgr)
	termMgr.SetSessionInputLease(sessMgr)
	projectSvc := projectsvc.NewWithDeps(projectsvc.Deps{Store: store, Sessions: sessionSvc, DefaultHarness: domain.AgentHarness(cfg.Agent), Telemetry: telemetrySink, Logger: log})
	if err := seedScratchProjectOnBoot(ctx, cfg, projectSvc); err != nil {
		stop()
		lcStack.Stop()
		if cdcErr := cdcPipe.Stop(); cdcErr != nil {
			log.Error("cdc pipeline shutdown", "err", cdcErr)
		}
		return err
	}
	lcStack.trackerDone = startTrackerIntake(ctx, store, sessionSvc, tracker, log)

	hostCommands := systemexec.New(cfg.DataDir)
	systemChecks := systemcheck.New(agentSvc, hostCommands)
	systemInstall := systeminstall.NewWithDeps(hostCommands, hostCommands, systeminstall.Deps{
		JobStore: store,
		Verifier: systeminstall.NewVerifier(agents, hostCommands),
		Sessions: store,
	})
	if err := systemInstall.Recover(ctx); err != nil {
		stop()
		lcStack.Stop()
		if cdcErr := cdcPipe.Stop(); cdcErr != nil {
			log.Error("cdc pipeline shutdown", "err", cdcErr)
		}
		return fmt.Errorf("recover harness install jobs: %w", err)
	}
	sessMgr.SetHarnessUseGate(systemInstall)
	systemInstall.SetOnSucceeded(func(target systeminstall.Target) {
		harness, ok := installedAgentHarness(target)
		if !ok {
			return
		}
		agentSvc.InvalidateAgentInstallation(harness)
		agentSvc.RecheckAgent(harness)
	})
	agentSvc.WarmReadiness()

	// Connect Mobile: the bridge service needs the LAN listener, but the LAN
	// listener needs the built router's handler, which only exists once srv is
	// constructed — and srv's router mounts the mobile controller, which needs
	// the bridge service. Break the cycle with late binding: build bs with LAN
	// left nil, hand its controller into NewWithDeps, then once srv exists,
	// build the LAN listener over srv.Handler() and assign it onto bs.LAN.
	bs := &controllers.BridgeService{
		ConfigPath:  mobilebridge.Path(cfg.DataDir),
		DefaultPort: mobilebridge.DefaultPort,
	}
	// HostID is assigned below, once the identity file has been read.
	mc := &controllers.MobileController{Bridge: bs}
	browserService := browsersvc.New(sessionSvc, browserBroker, browserAuthority)

	// Standalone shell terminals: user-opened shells with no agent session
	// behind them. They reuse the same runtime adapter (and therefore the same
	// terminal mux) as session panes, but keep their own ids, storage, and
	// lifetime — see internal/service/shellterm.
	shellTermSvc := startShellTerminals(ctx, cfg, runtimeAdapter, store, projectSvc, sessionSvc, log)
	agentAuthSvc := agentauth.NewWithAgentResolver(hostCommands, agentSvc, shellTermSvc)
	agentSvc.SetCodexAccountLoginTerminalOpener(shellTermSvc)
	// Late-bound so Kill/Cleanup close a session's scoped shells before its
	// worktree is torn down (shellTermSvc cannot exist before sessMgr does; see
	// SetShellTerminalCloser).
	sessMgr.SetShellTerminalCloser(shellTermSvc)
	var (
		usageCollector *usagesvc.Collector
		usagePipeline  *usagepipeline.Pipeline
		usagePricing   *usagePricingRuntime
	)
	if pricingRuntime, pricingErr := newUsagePricingRuntime(usagePricingRuntimeConfig{
		DataDir: cfg.DataDir,
		Store:   store,
		Logger:  log,
	}); pricingErr != nil {
		log.Warn("usage pricing disabled", "err", pricingErr)
	} else {
		usagePricing = pricingRuntime
		if pricingErr := pricingRuntime.Start(ctx); pricingErr != nil {
			log.Warn("usage pricing startup failed; continuing without catalog enrichment", "err", pricingErr)
		}
		defer func() {
			stop()
			usagePricing.Wait()
		}()
	}
	if roots, rootsErr := usagesvc.DefaultSourceRoots(ctx, cfg.DataDir); rootsErr != nil {
		log.Warn("usage collection disabled", "err", rootsErr)
	} else {
		usageCollector = usagesvc.NewCollector(store, roots, func(reconcile bool) {
			if usagePipeline == nil {
				return
			}
			if reconcile {
				usagePipeline.NotifySourcesChanged()
			} else {
				usagePipeline.NotifyInventoryChanged()
			}
		})
		if usagePricing != nil {
			usageCollector.OnRouteResolved(usagePricing.RepairLegacyAttribution)
		}
		ingestorConfig := usagepipeline.IngestorConfig{}
		if usagePricing != nil {
			ingestorConfig.Pricing = usagePricing.Manager()
			ingestorConfig.OnPricingError = func(err error) {
				log.Warn("usage event pricing failed", "err", err)
			}
			ingestorConfig.RequestAttributionRepair = usagePricing.RepairLegacyAttribution
		}
		ingestor := usagepipeline.NewIngestor(store, ingestorConfig)
		usagePipeline = usagepipeline.NewPipeline(store, ingestor, usagePipelineWatchRoots(roots), usagepipeline.CoordinatorConfig{
			Logger:     log,
			Initialize: usageCollector.BackfillActive,
			Reconcile: func(reconcileCtx context.Context) error {
				return usageCollector.ReconcileSources(reconcileCtx, 0)
			},
			ReconcilePath: usageCollector.ReconcilePath,
		})
		lcStack.LCM.SetUsageFinalizer(usageCollector)
	}
	lcStack.scmDone = startSCMObserver(ctx, store, lcStack.LCM, cfg.GitLab, log)
	var prActions prsvc.ActionManager
	prReader := newMultiSCMProvider(cfg.GitLab, log)
	prMerger := newMultiSCMMerger(cfg.GitLab, log)
	if prReader != nil && prMerger != nil {
		prActions = prsvc.NewActionService(prsvc.ActionDeps{Store: store, Merger: prMerger, Reader: prReader})
	} else {
		log.Warn("pr action service disabled: no usable SCM provider")
	}

	// Durable agent-switch and interface-transition recovery is the startup
	// safety boundary. The in-memory input fence disappeared with the previous
	// daemon; if AO cannot prove and close every active saga, do not bind a
	// usable API with user input accidentally reopened. Runtime/worktree
	// restoration follows in the background after the listener is live.
	if reconcileErr := sessMgr.ReconcileStartupSafety(ctx); reconcileErr != nil {
		stop()
		managedPreview.Close()
		lcStack.Stop()
		if cdcErr := cdcPipe.Stop(); cdcErr != nil {
			log.Error("cdc pipeline shutdown", "err", cdcErr)
		}
		return fmt.Errorf("reconcile sessions on boot: %w", reconcileErr)
	}
	agentSvc.WarmCodexAccounts()
	autoReview := autoreview.New(store, reviewSvc, autoreview.Config{Logger: log})
	lcStack.autoReviewDone = autoReview.Start(ctx)
	// Push-device registry: persisted phones that receive OS push notifications.
	// A load failure must not block boot — degrade to no push rather than refusing
	// to start the daemon. pushRegistry (interface) is assigned only when load
	// succeeds so a failure leaves a true nil interface (not a non-nil interface
	// wrapping a nil pointer), which the controller's nil guard relies on to
	// return 501. pushDevices keeps the concrete registry for the dispatcher.
	// deviceRoster (interface) mirrors the same nil-guard as pushRegistry: it is
	// assigned only when load succeeds, so a failed load leaves a true nil
	// interface rather than a non-nil interface wrapping a nil *DeviceRegistry
	// (which would panic on first method call). The roster controller answers
	// 503 DEVICE_REGISTRY_UNAVAILABLE in that state instead of crashing or
	// silently no-oping.
	var (
		pushRegistry controllers.PushRegistry
		pushDevices  *mobilebridge.DeviceRegistry
		deviceRoster controllers.DeviceRoster
	)
	if reg, regErr := mobilebridge.LoadRegistry(mobilebridge.PushDevicesPath(cfg.DataDir)); regErr != nil {
		log.Warn("load push device registry failed; push notifications disabled", "err", regErr)
	} else {
		pushRegistry = reg
		pushDevices = reg
		deviceRoster = reg
	}

	// One presence tracker instance shared by APIDeps.Presence (the
	// heartbeat middleware that touches it) and APIDeps.DeviceLive (the roster
	// controller that reads it) — must be the same instance or every device
	// would silently report offline.
	presenceTracker := presence.NewTracker()

	// Push dispatcher: an additive notification-hub subscriber that relays each
	// new notification to every registered device via the Expo Push Service. Runs
	// for the daemon's lifetime and stops when ctx is cancelled. EXPO_ACCESS_TOKEN
	// (optional) enables Expo's enforced push security when set.
	if pushDevices != nil {
		dispatcher := push.NewDispatcher(notificationHub, pushDevices, push.NewExpoClient(os.Getenv("EXPO_ACCESS_TOKEN")), log)
		go dispatcher.Run(ctx)
	}

	// Managed remote-access connector. Reap first: a daemon that died without
	// stopping its connector leaves a public tunnel to this machine running
	// with nobody watching it.
	tunnelPID := mobilebridge.TunnelPIDPath(cfg.DataDir)
	if reapErr := mobilebridge.ReapStaleTunnel(tunnelPID, mobilebridge.IsLiveCloudflared, mobilebridge.KillProcess); reapErr != nil {
		log.Warn("could not reap a stale mobile tunnel", "error", reapErr)
	}
	// Looked up again whenever the bridge is enabled, so a cloudflared the user
	// installs from Connect Mobile is picked up without restarting AO.
	bs.ResolveTunnel = func() controllers.TunnelController {
		res := mobilebridge.ResolveCloudflared(mobilebridge.LocalCloudflaredLookup(cfg.DataDir))
		if res.NeedsInstall {
			return nil
		}
		log.Info("mobile remote access available", "cloudflared", res.Path, "source", res.Source)
		return mobilebridge.NewManagedTunnel(mobilebridge.ManagedTunnelDeps{
			Binary: res.Path, PIDPath: tunnelPID, Log: log,
		})
	}
	if res := mobilebridge.ResolveCloudflared(mobilebridge.LocalCloudflaredLookup(cfg.DataDir)); !res.NeedsInstall {
		log.Info("mobile remote access available", "cloudflared", res.Path, "source", res.Source)
		bs.Tunnel = mobilebridge.NewManagedTunnel(mobilebridge.ManagedTunnelDeps{
			Binary: res.Path, PIDPath: tunnelPID, Log: log,
		})
	} else {
		// Not fatal: the LAN and Tailscale endpoints still work, and Connect
		// Mobile behaves exactly as it did before remote access existed.
		log.Info("mobile remote access unavailable; cloudflared not installed",
			"rejectedSystemPath", res.SystemPath)
	}

	// Stable, machine-bound host identity, served by the unauthenticated
	// GET /api/v1/identity probe. A failure here is not fatal: the probe then
	// answers 501 and the phone falls back to pairing without identity
	// verification, which is how it behaved before the probe existed.
	hostIdentity, identityErr := mobilebridge.EnsureLocalIdentity(cfg.DataDir)
	if identityErr != nil {
		log.Warn("could not establish host identity; /api/v1/identity will report unimplemented", "error", identityErr)
	}

	bs.HostID = hostIdentity.HostID

	srv, err := httpd.NewWithDeps(cfg, log, termMgr, httpd.APIDeps{
		Projects:           projectSvc,
		HostID:             hostIdentity.HostID,
		Endpoints:          bs,
		Agents:             agentSvc,
		CodexAccounts:      agentSvc,
		SystemChecks:       systemChecks,
		Installer:          systemInstall,
		Sessions:           sessionSvc,
		DesktopWorkspaces:  sessionSvc,
		PRs:                prActions,
		Reviews:            reviewSvc,
		Notifications:      notifier,
		NotificationStream: notificationHub,
		Push:               pushRegistry,
		Presence:           presenceTracker,
		DeviceRoster:       deviceRoster,
		DeviceLive:         presenceTracker,
		Import:             importsvc.New(importsvc.Deps{Store: store}),
		ShellTerminals:     shellTermSvc,
		AgentAuth:          agentAuthSvc,
		Conversations:      chatSvc,
		Settings:           settingsSvc,
		CDC:                store,
		Events:             cdcPipe.Broadcaster,
		Activity:           lcStack.LCM,
		UsageHooks:         usageCollector,
		UsageSummary:       usagesvc.NewSummaryReader(store),
		Telemetry:          telemetrySink,
		Mobile:             mc,
		DevImport: devimportsvc.New(devimportsvc.Deps{
			Store:         store,
			TargetDataDir: cfg.DataDir,
			OpenSource: func(ctx context.Context, dataDir string) (devimportsvc.SourceStore, error) {
				return sqlite.OpenReadOnly(ctx, dataDir)
			},
		}),
		Browser:             browserService,
		PreviewServer:       managedPreview,
		SessionCapabilities: browserAuthority,
		AgentSwitchPolicy:   policyCoordinator,
	})
	if err != nil {
		stop()
		lcStack.Stop()
		if cdcErr := cdcPipe.Stop(); cdcErr != nil {
			log.Error("cdc pipeline shutdown", "err", cdcErr)
		}
		return err
	}
	previewDone := preview.NewPoller(store, sessionSvc, "http://"+srv.Addr().String(), preview.PollerConfig{Logger: log}).Start(ctx)
	_ = os.Unsetenv(browserruntime.RuntimeAddressEnv)
	if ln, addr, err := browserruntime.Listen(cfg.RunFilePath); err != nil {
		log.Warn("browser runtime: listener unavailable; agent browser control disabled", "err", err)
	} else {
		if err := os.Setenv(browserruntime.RuntimeAddressEnv, addr); err != nil {
			_ = ln.Close()
			return fmt.Errorf("publish browser runtime address: %w", err)
		}
		log.Info("browser runtime: listening", "addr", addr)
		go func() {
			if err := browserBroker.Serve(ctx, ln); err != nil {
				log.Warn("browser runtime: serve stopped with error", "err", err)
			}
		}()
	}
	var usageDone <-chan struct{}

	// Late-bind: the LAN listener shares the exact loopback router instance so
	// the LAN surface and loopback surface never drift apart.
	lan := httpd.NewMobileLAN(srv.Handler(), mobilebridge.DefaultPort, log, telemetrySink)
	bs.LAN = lan

	// Restore Connect Mobile across a daemon restart: if the bridge was left
	// enabled, re-arm the listener on its last port with the same password
	// hash so an already-paired phone keeps working with no new password, and
	// (via bs.RestoreOnBoot) re-apply the secure-pairing proxy against the
	// port Start actually bound. Routed through bs, not lan directly, so the
	// proxy never gets pinned to a dead port after an ephemeral fallback.
	// Best-effort: never blocks boot.
	if err := restoreMobileOnBoot(mobilebridge.Path(cfg.DataDir), bs); err != nil {
		log.Warn("restore mobile bridge on boot failed", "err", err)
	}

	if usagePipeline != nil {
		usageDone = usagePipeline.Start(ctx)
	}
	// ponytail: 5s tolerates a brief frontend restart; tune if dev hot-reload trips it.
	const supervisorGrace = 5 * time.Second

	if ln, addr, err := supervisor.Listen(cfg.RunFilePath); err != nil {
		// Non-fatal: without the link the daemon still works (e.g. headless "ao start"),
		// it just will not auto-stop when a frontend dies. Do not block startup on it.
		log.Warn("supervisor: listener unavailable; frontend-death auto-stop disabled", "err", err)
	} else {
		log.Info("supervisor: listening", "addr", addr)
		sup := supervisor.New(supervisorGrace, srv.RequestShutdown, log)
		go func() {
			if err := sup.Serve(ctx, ln); err != nil {
				log.Warn("supervisor: serve stopped with error", "err", err)
			}
		}()
	}

	var startupReconcileDone <-chan struct{}
	runErr := srv.RunWithReady(ctx, func() {
		done := make(chan struct{})
		startupReconcileDone = done
		go func() {
			defer close(done)
			if reconcileErr := reconcilePersistentChatHosts(ctx, cfg.DataDir, store); reconcileErr != nil {
				log.Error("persistent chat host reconciliation on boot failed", "err", reconcileErr)
			}
			if reconcileErr := sessMgr.ReconcileBackground(ctx); reconcileErr != nil {
				log.Error("background session reconciliation on boot failed", "err", reconcileErr)
			}
			if reconcileErr := lcStack.ReconcileRuntime(ctx); reconcileErr != nil {
				log.Error("background agent-process reconciliation on boot failed", "err", reconcileErr)
			}
		}()
	})

	// Both graceful shutdown paths (SIGTERM and POST /shutdown) funnel through
	// srv.Run returning. We deliberately do NOT tear down sessions here: they
	// survive the daemon exit and the next boot's Reconcile adopts them,
	// preserving session IDs. The narrowed sessionLifecycle interface makes
	// teardown-on-shutdown a compile error.

	// Shut the background goroutines down in order: cancel the context FIRST so
	// their loops exit, then wait for them to drain. Doing this explicitly (not
	// via defer) avoids the LIFO trap where a Stop() that blocks on ctx-cancel
	// runs before the cancel: a non-signal exit path would hang otherwise.
	stop()
	if agentSwitchDispatcher != nil {
		dispatcherStopContext, dispatcherStopCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		if err := agentSwitchDispatcher.Stop(dispatcherStopContext); err != nil {
			log.Error("agent switch failure dispatcher shutdown", "error", err)
		}
		dispatcherStopCancel()
	}
	installStopCtx, installStopCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	if err := systemInstall.Close(installStopCtx); err != nil {
		log.Error("harness installer shutdown", "err", err)
	}
	installStopCancel()
	if startupReconcileDone != nil {
		<-startupReconcileDone
	}
	switchStopCtx, switchCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	if err := sessMgr.WaitAgentSwitchWorkers(switchStopCtx); err != nil {
		if agentSwitchWorkerWaitTimedOut(err) {
			enqueueCtx, enqueueCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if enqueueErr := enqueueAgentSwitchWorkerShutdownTimeout(enqueueCtx, store, policyCoordinator, cfg.AppRunID, time.Now().UTC()); enqueueErr != nil {
				log.Error("agent switch worker shutdown observability enqueue", "error", enqueueErr)
			}
			enqueueCancel()
		}
		log.Error("agent switch worker shutdown", "err", err)
	}
	switchCancel()
	managedPreview.Close()
	<-previewDone
	// Detach chat controllers before stopping the lifecycle stack. Persistent
	// provider hosts deliberately survive this daemon and preserve in-flight
	// turns; the replacement daemon reconnects to the same initialized stream and
	// consumes host-replayed output. Explicit session termination, not daemon
	// shutdown, destroys them.
	chatStopCtx, chatCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	chatSvc.StopAll(chatStopCtx)
	chatCancel()
	if usageDone != nil {
		<-usageDone
	}
	if usagePricing != nil {
		usagePricing.Wait()
	}
	lcStack.Stop()
	// Tear the tailnet proxy down before the listener it fronts. `tailscale
	// serve --bg` state lives in tailscaled and outlives this process, so
	// leaving it would keep publishing a local port that no longer has the
	// authenticated LAN listener behind it. Best-effort and never blocking:
	// boot restore re-applies it against the next bound port.
	bs.ShutdownServe()
	// And the connector before that again, for the same reason: cloudflared is
	// a separate process that outlives this one, so leaving it would keep a
	// public hostname resolving to a port that is about to close. Stopping it
	// does not disable the bridge — boot restore starts a new one.
	bs.ShutdownTunnel()
	lanStopCtx, lanCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer lanCancel()
	if err := lan.Stop(lanStopCtx); err != nil {
		log.Error("mobile LAN listener shutdown", "err", err)
	}
	if err := cdcPipe.Stop(); err != nil {
		log.Error("cdc pipeline shutdown", "err", err)
	}
	return runErr
}

func installedAgentHarness(target systeminstall.Target) (string, bool) {
	if target == systeminstall.TargetClaude {
		return "claude-code", true
	}
	if systeminstall.IsAgentTarget(target) {
		return string(target), true
	}
	return "", false
}

func usagePipelineWatchRoots(roots usagesvc.SourceRoots) []string {
	return []string{
		roots.ClaudeProjects,
		roots.CodexSessions,
		roots.CodexArchived,
		roots.KimiHome,
	}
}

func seedScratchProjectOnBoot(ctx context.Context, cfg config.Config, projects *projectsvc.Service) error {
	if projects == nil {
		return nil
	}
	if _, err := projects.EnsureDefaultScratchProject(ctx, filepath.Join(cfg.DataDir, "scratch", "default")); err != nil {
		return fmt.Errorf("seed scratch project: %w", err)
	}
	return nil
}

// newLogger returns the daemon's slog logger. It writes to stderr so supervisors
// can capture it separately from any structured stdout protocol added later.
func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func stabilizeWorkingDirectory(dataDir string) error {
	if dataDir == "" {
		return fmt.Errorf("daemon working directory: data dir is required")
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("daemon working directory: create %s: %w", dataDir, err)
	}
	if err := os.Chdir(dataDir); err != nil {
		return fmt.Errorf("daemon working directory: chdir %s: %w", dataDir, err)
	}
	return nil
}
