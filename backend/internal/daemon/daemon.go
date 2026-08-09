// Package daemon owns the Agent Orchestrator backend process: config loading,
// loopback HTTP serving, durable storage, CDC fan-out, lifecycle wiring, and
// graceful shutdown.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/modelcatalog"
	chatdriverregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/runtimeselect"
	"github.com/aoagents/agent-orchestrator/backend/internal/browserruntime"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/daemon/supervisor"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/notify"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/reconciler"
	usagepipeline "github.com/aoagents/agent-orchestrator/backend/internal/observe/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/preview"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/push"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	browsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	devimportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/devimport"
	importsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/importer"
	notificationsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/notification"
	prsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/pr"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	settingssvc "github.com/aoagents/agent-orchestrator/backend/internal/service/settings"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/skillassets"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/terminal"
)

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

	// Refresh the embedded using-ao skill into the data dir so worker sessions
	// in any project can read the ao CLI catalog from a stable absolute path.
	// Non-fatal: the skill is an enhancement over `ao --help`, not required.
	if err := skillassets.Install(cfg.DataDir); err != nil {
		log.Warn("install using-ao skill", "err", err)
	}

	telemetrySink := newTelemetrySink(cfg, store, log)
	defer func() { _ = telemetrySink.Close(context.Background()) }()
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

	cdcPipe, err := startCDC(ctx, store, log)
	if err != nil {
		return err
	}

	// Terminal streaming: the selected runtime (tmux on macOS/Linux, conpty on Windows) supplies the
	// attach Stream and liveness; the CDC broadcaster feeds the session-state channel. The manager
	// is handed to httpd, which mounts it at /mux. Raw PTY bytes never flow
	// through the CDC change_log -- only session-state events do.
	runtimeAdapter := runtimeselect.New(log)
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
	// service's, adapted here so neither package imports the other.
	settingsSvc := settingssvc.New(
		settingsStore{store: store},
		chatDrivers,
		func() time.Time { return time.Now().UTC() },
	)

	// Chat service. The driver registry is the capability gate: a harness with no
	// registered driver cannot start in chat mode, so an unsupported request fails
	// loudly instead of silently becoming a TUI session.
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
				Conversation: rows.Conversation,
				Turns:        rows.Turns,
				Messages:     rows.Messages,
				Activities:   rows.Activities,
			}, nil
		}),
		PageReader: chatsvc.SnapshotPageReaderFunc(func(ctx context.Context, conversationID string, beforeSequence, limit int64) (chatsvc.ConversationRows, error) {
			rows, err := store.LoadConversationSnapshotPage(ctx, conversationID, beforeSequence, limit)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation:   rows.Conversation,
				Turns:          rows.Turns,
				Messages:       rows.Messages,
				Activities:     rows.Activities,
				OldestSequence: rows.OldestSequence,
				HasMoreBefore:  rows.HasMoreBefore,
			}, nil
		}),
		Drivers: chatDrivers,
		// The LCM satisfies ActivityRecorder directly: a chat turn is a pure
		// lifecycle reduction, same as a hook signal from a terminal session.
		Activity: lcStack.LCM,
		Log:      log,
		NewID:    uuid.NewString,
	})

	sessionSvc, reviewSvc, sessMgr, err := startSession(ctx, cfg, runtimeAdapter, store, lcStack.LCM, messenger, telemetrySink, agents, managedPreview, browserBroker, browserAuthority, chatLauncher{svc: chatSvc}, settingsSvc, log)
	if err != nil {
		stop()
		lcStack.Stop()
		if cdcErr := cdcPipe.Stop(); cdcErr != nil {
			log.Error("cdc pipeline shutdown", "err", cdcErr)
		}
		return fmt.Errorf("wire session service: %w", err)
	}
	sessMgr.SetTerminalInputGate(termMgr)
	lifecycleMessenger.Bind(sessionLifecycleMessenger{sessMgr})
	lcStack.LCM.SetCompletionTerminator(sessMgr)
	projectSvc := projectsvc.NewWithDeps(projectsvc.Deps{Store: store, Sessions: sessionSvc, DefaultHarness: domain.AgentHarness(cfg.Agent), Telemetry: telemetrySink})
	if err := seedScratchProjectOnBoot(ctx, cfg, projectSvc); err != nil {
		stop()
		lcStack.Stop()
		if cdcErr := cdcPipe.Stop(); cdcErr != nil {
			log.Error("cdc pipeline shutdown", "err", cdcErr)
		}
		return err
	}
	lcStack.trackerDone = startTrackerIntake(ctx, store, sessionSvc, log)

	agentSvc := agentsvc.NewWithDeps(agentsvc.Deps{Cache: store, Discoverer: modelcatalog.Discoverer{}, Projects: store})
	go func() {
		if _, err := agentSvc.Refresh(ctx); err != nil {
			log.Warn("initial agent catalog refresh failed", "err", err)
		}
	}()

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
	mc := &controllers.MobileController{Bridge: bs}
	browserService := browsersvc.New(sessionSvc, browserBroker, browserAuthority)

	// Standalone shell terminals: user-opened shells with no agent session
	// behind them. They reuse the same runtime adapter (and therefore the same
	// terminal mux) as session panes, but keep their own ids, storage, and
	// lifetime — see internal/service/shellterm.
	shellTermSvc := startShellTerminals(ctx, cfg, runtimeAdapter, store, projectSvc, sessionSvc, log)
	// Late-bound so Kill/Cleanup close a session's scoped shells before its
	// worktree is torn down (shellTermSvc cannot exist before sessMgr does; see
	// SetShellTerminalCloser).
	sessMgr.SetShellTerminalCloser(shellTermSvc)
	var (
		usageCollector *usagesvc.Collector
		usagePipeline  *usagepipeline.Pipeline
	)
	if roots, rootsErr := usagesvc.DefaultSourceRoots(ctx); rootsErr != nil {
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
		ingestor := usagepipeline.NewIngestor(store, usagepipeline.IngestorConfig{})
		usagePipeline = usagepipeline.NewPipeline(store, ingestor, []string{
			roots.ClaudeProjects,
			roots.CodexSessions,
			roots.CodexArchived,
		}, usagepipeline.CoordinatorConfig{
			Logger:     log,
			Initialize: usageCollector.BackfillActive,
			Reconcile: func(reconcileCtx context.Context) error {
				return usageCollector.ReconcileSources(reconcileCtx, 0)
			},
			ReconcilePath: usageCollector.ReconcilePath,
		})
		lcStack.LCM.SetUsageFinalizer(usageCollector)
	}
	lcStack.scmDone = startSCMObserver(ctx, store, lcStack.LCM, log)
	var prActions prsvc.ActionManager
	if mergeProvider, mergeErr := newGitHubSCMProvider(log); mergeErr != nil {
		logSCMProviderDisabled(log, mergeErr)
	} else {
		prActions = prsvc.NewActionService(prsvc.ActionDeps{Store: store, Merger: mergeProvider, Reader: mergeProvider})
	}
	// Push-device registry: persisted phones that receive OS push notifications.
	// A load failure must not block boot — degrade to no push rather than refusing
	// to start the daemon. pushRegistry (interface) is assigned only when load
	// succeeds so a failure leaves a true nil interface (not a non-nil interface
	// wrapping a nil pointer), which the controller's nil guard relies on to
	// return 501. pushDevices keeps the concrete registry for the dispatcher.
	var (
		pushRegistry controllers.PushRegistry
		pushDevices  *mobilebridge.DeviceRegistry
	)
	if reg, regErr := mobilebridge.LoadRegistry(mobilebridge.PushDevicesPath(cfg.DataDir)); regErr != nil {
		log.Warn("load push device registry failed; push notifications disabled", "err", regErr)
	} else {
		pushRegistry = reg
		pushDevices = reg
	}

	// Push dispatcher: an additive notification-hub subscriber that relays each
	// new notification to every registered device via the Expo Push Service. Runs
	// for the daemon's lifetime and stops when ctx is cancelled. EXPO_ACCESS_TOKEN
	// (optional) enables Expo's enforced push security when set.
	if pushDevices != nil {
		dispatcher := push.NewDispatcher(notificationHub, pushDevices, push.NewExpoClient(os.Getenv("EXPO_ACCESS_TOKEN")), log)
		go dispatcher.Run(ctx)
	}

	srv, err := httpd.NewWithDeps(cfg, log, termMgr, httpd.APIDeps{
		Projects:           projectSvc,
		Agents:             agentSvc,
		Sessions:           sessionSvc,
		PRs:                prActions,
		Reviews:            reviewSvc,
		Notifications:      notifier,
		NotificationStream: notificationHub,
		Push:               pushRegistry,
		Import:             importsvc.New(importsvc.Deps{Store: store}),
		ShellTerminals:     shellTermSvc,
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
	// hash so an already-paired phone keeps working with no new password.
	// Best-effort: never blocks boot.
	if err := restoreMobileOnBoot(mobilebridge.Path(cfg.DataDir), lan); err != nil {
		log.Warn("restore mobile bridge on boot failed", "err", err)
	}

	// Reconcile sessions on boot: adopt crash-surviving runtimes, capture and
	// terminate dead ones, then restore shutdown-saved sessions. Run to
	// completion (synchronous, as before) so restore-pending sessions are settled
	// before the terminal-resource reconciler starts and before the server
	// serves. Best-effort: a failure is logged but never blocks boot.
	if reconcileErr := sessMgr.Reconcile(ctx); reconcileErr != nil {
		log.Error("reconcile sessions on boot failed", "err", reconcileErr)
	}
	if reconcileErr := lcStack.ReconcileRuntime(ctx); reconcileErr != nil {
		log.Error("reconcile agent processes on boot failed", "err", reconcileErr)
	}
	if usagePipeline != nil {
		usageDone = usagePipeline.Start(ctx)
	}

	// Start the terminal-resource reconciler AFTER Reconcile/RestoreAll has
	// settled: it subscribes to the CDC session_updated wake, then drains the
	// sessions-driven candidate backlog (the pre-existing leaked worktrees #2811
	// is about) onto its async worker, and retries capped-backoff pending
	// cleanups on a sweep. Subscribing before its own boot snapshot closes the
	// boot-window race; draining is async so a large backlog never delays serving.
	// Its done-channel joins lcStack.Stop so it unsubscribes before cdcPipe.Stop.
	termReconciler := reconciler.New(sessMgr, store, cdcPipe.Broadcaster, reconciler.Config{Logger: log})
	lcStack.reconcilerDone = termReconciler.Start(ctx)

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

	runErr := srv.Run(ctx)

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
	managedPreview.Close()
	<-previewDone
	// Close chat controllers before the lifecycle stack: each owns an app-server
	// child process, and closing them also settles any turn left in flight so a
	// restart does not read a half-finished turn as still working.
	chatStopCtx, chatCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	chatSvc.StopAll(chatStopCtx)
	chatCancel()
	if usageDone != nil {
		<-usageDone
	}
	lcStack.Stop()
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
