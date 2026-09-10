package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/runtimeselect"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	telemetryadapter "github.com/aoagents/agent-orchestrator/backend/internal/adapters/telemetry"
	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type wiringReadinessProvider struct {
	snapshot domain.AgentReadinessSnapshot
	err      error
	agentID  string
	purpose  domain.AgentReadinessPurpose
}

func (p *wiringReadinessProvider) EnsureAgentReadiness(_ context.Context, agentID string, purpose domain.AgentReadinessPurpose) (domain.AgentReadinessSnapshot, error) {
	p.agentID = agentID
	p.purpose = purpose
	return p.snapshot, p.err
}
func (*wiringReadinessProvider) InvalidateAgentInstallation(string)   {}
func (*wiringReadinessProvider) InvalidateAgentAuthentication(string) {}
func (*wiringReadinessProvider) RecheckAgent(string)                  {}

func TestReviewerAgentAuthUsesLaunchReadinessAndPreservesStrictStates(t *testing.T) {
	for _, test := range []struct {
		name  string
		state domain.AgentAuthenticationState
		want  ports.AgentAuthStatus
	}{
		{name: "authorized", state: domain.AgentAuthenticationAuthorized, want: ports.AgentAuthStatusAuthorized},
		{name: "not applicable", state: domain.AgentAuthenticationNotApplicable, want: ports.AgentAuthStatusAuthorized},
		{name: "unauthorized", state: domain.AgentAuthenticationUnauthorized, want: ports.AgentAuthStatusUnauthorized},
		{name: "unknown", state: domain.AgentAuthenticationUnknown, want: ports.AgentAuthStatusUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &wiringReadinessProvider{snapshot: domain.AgentReadinessSnapshot{
				Authentication: domain.AgentAuthenticationObservation{State: test.state},
			}}
			got, supported, err := (reviewerAgentAuth{readiness: provider}).AuthStatus(context.Background(), domain.ReviewerCodex)
			if err != nil || !supported || got != test.want {
				t.Fatalf("AuthStatus() = (%q, %v, %v), want (%q, true, nil)", got, supported, err, test.want)
			}
			if provider.agentID != "codex" || provider.purpose != domain.AgentReadinessPurposeLaunch {
				t.Fatalf("readiness request = (%q, %q)", provider.agentID, provider.purpose)
			}
		})
	}
}

func TestInstalledAgentHarnessMapsManagedHarnessInstalls(t *testing.T) {
	for _, test := range []struct {
		target  systeminstall.Target
		harness string
		ok      bool
	}{
		{target: systeminstall.TargetClaude, harness: "claude-code", ok: true},
		{target: systeminstall.TargetClaudeCode, harness: "claude-code", ok: true},
		{target: systeminstall.TargetCodex, harness: "codex", ok: true},
		{target: systeminstall.TargetOpencode, harness: "opencode", ok: true},
		{target: systeminstall.TargetCopilot, harness: "copilot", ok: true},
		{target: systeminstall.TargetKiro, harness: "kiro", ok: true},
		{target: systeminstall.TargetPi, harness: "pi", ok: true},
		{target: systeminstall.TargetVibe, harness: "vibe", ok: true},
		{target: systeminstall.TargetTmux},
		{target: systeminstall.TargetGH},
	} {
		got, ok := installedAgentHarness(test.target)
		if got != test.harness || ok != test.ok {
			t.Errorf("installedAgentHarness(%q) = (%q, %v), want (%q, %v)", test.target, got, ok, test.harness, test.ok)
		}
	}
}

// TestWiring_WriteFlowsToBroadcaster exercises the real boot path end to end:
// a lifecycle write -> sqlite -> DB trigger -> change_log -> CDC poller ->
// broadcaster, through the same cdc.Source implementation the daemon uses.
func TestWiring_WriteFlowsToBroadcaster(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	lcm := lifecycle.New(store, nil)

	bcast := cdc.NewBroadcaster()
	poller := cdc.NewPoller(store, bcast, cdc.PollerConfig{})
	if err := poller.SeekToHead(ctx); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []cdc.Event
	bcast.Subscribe(func(e cdc.Event) { mu.Lock(); got = append(got, e); mu.Unlock() })

	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "mer", Path: "/repo/mer"}); err != nil {
		t.Fatal(err)
	}
	rec, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "mer", Kind: domain.KindWorker,
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A real transition through the engine, which writes the row and fires the
	// activity_state/is_terminated CDC trigger.
	if err := lcm.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := poller.Poll(ctx); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	var sawSession bool
	for _, e := range got {
		if e.SessionID == string(rec.ID) {
			sawSession = true
		}
	}
	if !sawSession {
		t.Fatalf("expected a change_log event for %s to reach the broadcaster, got %d events", rec.ID, len(got))
	}
}

// TestWiring_AgentResolverResolvesRealAdapters asserts buildAgentResolver wires a
// real registry-backed per-session resolver: each harness resolves to the
// matching registered adapter, while empty and unknown harnesses miss.
func TestWiring_AgentResolverResolvesRealAdapters(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver, err := buildAgentResolver("", log) // empty default → claude-code
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		harness domain.AgentHarness
		wantID  string
	}{
		{domain.HarnessClaudeCode, "claude-code"},
		{domain.HarnessCodex, "codex"},
		{domain.HarnessOpenCode, "opencode"},
		{domain.HarnessGrok, "grok"},
		{domain.HarnessCursor, "cursor"},
		{domain.HarnessQwen, "qwen"},
		{domain.HarnessCopilot, "copilot"},
		{domain.HarnessKimi, "kimi"},
		{domain.HarnessMuse, "muse"},
		{domain.HarnessDroid, "droid"},
		{domain.HarnessAmp, "amp"},
		{domain.HarnessAgy, "agy"},
		{domain.HarnessCrush, "crush"},
		{domain.HarnessAider, "aider"},
		{domain.HarnessGoose, "goose"},
		{domain.HarnessAuggie, "auggie"},
		{domain.HarnessContinue, "continue"},
		{domain.HarnessDevin, "devin"},
		{domain.HarnessCline, "cline"},
		{domain.HarnessKiro, "kiro"},
		{domain.HarnessKilocode, "kilocode"},
		{domain.HarnessVibe, "vibe"},
		{domain.HarnessPi, "pi"},
		{domain.HarnessPrimeAgent, "prime-agent"},
		{domain.HarnessAutohand, "autohand"},
	} {
		agent, ok := resolver.Agent(tc.harness)
		if !ok {
			t.Fatalf("resolver has no agent for harness %q", tc.harness)
		}
		described, ok := agent.(adapters.Adapter)
		if !ok {
			t.Fatalf("agent for harness %q is %T, not a registered adapters.Adapter", tc.harness, agent)
		}
		if got := described.Manifest().ID; got != tc.wantID {
			t.Fatalf("harness %q resolved to adapter %q, want %q", tc.harness, got, tc.wantID)
		}
	}
	if _, ok := resolver.Agent("definitely-not-an-agent"); ok {
		t.Fatal("unknown harness resolved to an agent; want a miss")
	}
	if _, ok := resolver.Agent(""); ok {
		t.Fatal("empty harness resolved to an agent; want a miss")
	}
}

// TestWiring_ActiveTurnSteeringComesFromAdapters asserts the active-turn
// steering policy lifecycle consumes is resolved from the agent adapters
// (ports.ActiveTurnSteerer) rather than from any harness knowledge baked into
// the shared domain package. A harness whose adapter does not declare the
// capability — and an unresolvable one — must answer false, so an unknown
// harness is never written to mid-turn.
func TestWiring_ActiveTurnSteeringComesFromAdapters(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agents, err := buildAgentResolver(config.DefaultAgent, log)
	if err != nil {
		t.Fatal(err)
	}
	steers := activeTurnSteering(agents)

	if !steers(domain.HarnessCodex) {
		t.Error("codex declares SteersActiveTurn; want true from the adapter-backed policy")
	}
	if !steers(domain.HarnessPrimeAgent) {
		t.Error("prime-agent declares SteersActiveTurn; want true from the adapter-backed policy")
	}
	for _, harness := range []domain.AgentHarness{domain.HarnessClaudeCode, domain.HarnessAider, "definitely-not-an-agent", ""} {
		if steers(harness) {
			t.Errorf("harness %q must not be steerable mid-turn", harness)
		}
	}
	if activeTurnSteering(nil)(domain.HarnessCodex) {
		t.Error("a nil resolver must answer false, not steer")
	}
}

func TestWiring_StartupSignalGateComesFromAdapters(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agents, err := buildAgentResolver(config.DefaultAgent, log)
	if err != nil {
		t.Fatal(err)
	}
	gates := startupSignalGatesInput(agents)

	if !gates(domain.HarnessCursor) {
		t.Error("cursor declares startup-ready signaling; want its TUI input gated")
	}
	for _, harness := range []domain.AgentHarness{domain.HarnessAider, domain.HarnessOMP, "definitely-not-an-agent", ""} {
		if gates(harness) {
			t.Errorf("harness %q must not require a startup signal", harness)
		}
	}
	if startupSignalGatesInput(nil)(domain.HarnessCursor) {
		t.Error("a nil resolver must leave startup input ungated")
	}
}

// TestWiring_UrgentNudgeGateComesFromAdapters asserts the urgent merge-conflict
// nudge is fail-closed at a waiting_input prompt: it is only safe on a harness
// that reports a permission dialog AS blocked (ports.BlockedActivitySignaler),
// so a waiting_input prompt there is a genuine idle composer rather than a
// masked permission decision. Codex, Droid, and the shared-hook harnesses all
// fold permission prompts into waiting_input and must stay suppressed; only
// blocked-signalling adapters (claude-code, kimchi) open the boundary.
func TestWiring_UrgentNudgeGateComesFromAdapters(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agents, err := buildAgentResolver(config.DefaultAgent, log)
	if err != nil {
		t.Fatal(err)
	}
	safe := urgentNudgeWaitingInputSafe(agents)

	for _, harness := range []domain.AgentHarness{domain.HarnessClaudeCode, domain.HarnessKimchi} {
		if !safe(harness) {
			t.Errorf("harness %q reports permission dialogs as blocked; urgent nudge must be allowed at waiting_input", harness)
		}
	}
	// Codex maps permission-request to waiting_input; Droid folds both permission
	// decisions and idle notifications into waiting_input; Goose/Devin ride the
	// shared name-only StandardDeriveActivityState with no blocked signal. All
	// must keep urgent delivery suppressed at a waiting_input prompt.
	for _, harness := range []domain.AgentHarness{
		domain.HarnessCodex, domain.HarnessDroid, domain.HarnessGoose, domain.HarnessDevin,
		"definitely-not-an-agent", "",
	} {
		if safe(harness) {
			t.Errorf("harness %q cannot distinguish a masked permission prompt from an idle composer; urgent nudge must stay fail-closed", harness)
		}
	}
	if urgentNudgeWaitingInputSafe(nil)(domain.HarnessClaudeCode) {
		t.Error("a nil resolver must fail closed, not open the waiting_input boundary")
	}
}

// TestWiring_StartSessionBuildsSessionService asserts the daemon's startSession
// constructs a real controller-facing session service end to end (resolver +
// gitworktree workspace + session manager over the shared store/LCM), which is
// what gets mounted at httpd APIDeps.Sessions.
func TestWiring_StartSessionBuildsSessionService(t *testing.T) {
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lcm := lifecycle.New(store, nil)
	cfg := config.Config{DataDir: t.TempDir()}

	rt := runtimeselect.New(nil, cfg.RunFilePath)
	messenger := newSessionMessenger(store, rt, log)
	agents, err := buildAgentResolver(config.DefaultAgent, log)
	if err != nil {
		t.Fatalf("buildAgentResolver: %v", err)
	}
	svc, reviewSvc, lc, err := startSession(context.Background(), cfg, rt, store, lcm, messenger, telemetryadapter.NoopSink{}, agents, nil, nil, nil, nil, nil, nil, nil, nil, nil, log)
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}
	if svc == nil {
		t.Fatal("startSession returned nil session service")
	}
	if reviewSvc == nil {
		t.Fatal("startSession returned nil review service")
	}
	if lc == nil {
		t.Fatal("startSession returned nil session lifecycle")
	}
}

func TestWiring_StartSessionSpawnsScratchWithoutGitRepo(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	binDir := t.TempDir()
	writeFakeExecutable(t, filepath.Join(binDir, "claude"))
	writeFakeExecutable(t, filepath.Join(binDir, "claude.cmd"))
	writeFakeExecutable(t, filepath.Join(binDir, "tmux"))
	writeFakeExecutable(t, filepath.Join(binDir, "tmux.cmd"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	dataDir := t.TempDir()
	scratchPath := filepath.Join(dataDir, "scratch", "default")
	if err := os.MkdirAll(scratchPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:           "scratch",
		Path:         scratchPath,
		Kind:         domain.ProjectKindScratch,
		RegisteredAt: time.Now(),
		Config: domain.ProjectConfig{
			Worker:       domain.RoleOverride{Harness: domain.HarnessClaudeCode},
			Orchestrator: domain.RoleOverride{Harness: domain.HarnessClaudeCode},
		},
	}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lcm := lifecycle.New(store, nil)
	runtime := &selectableRuntime{}
	cfg := config.Config{DataDir: dataDir, Agent: string(domain.HarnessClaudeCode)}
	messenger := newSessionMessenger(store, runtime, log)
	agents, err := buildAgentResolver(string(domain.HarnessClaudeCode), log)
	if err != nil {
		t.Fatalf("buildAgentResolver: %v", err)
	}
	svc, _, _, err := startSession(context.Background(), cfg, runtime, store, lcm, messenger, telemetryadapter.NoopSink{}, agents, nil, nil, nil, nil, nil, nil, nil, nil, nil, log)
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}

	session, _, _, err := svc.Spawn(ctx, ports.SpawnConfig{ProjectID: "scratch", Kind: domain.KindWorker, Prompt: "try scratch"})
	if err != nil {
		t.Fatalf("Spawn scratch: %v", err)
	}
	if session.Metadata.Branch != "" {
		t.Fatalf("scratch branch = %q, want empty", session.Metadata.Branch)
	}
	wantWorkspace := filepath.Join(dataDir, "worktrees", "scratch", "workers", string(session.ID))
	physicalWorkspace, err := filepath.EvalSymlinks(wantWorkspace)
	if err != nil {
		t.Fatalf("resolve scratch workspace %q: %v", wantWorkspace, err)
	}
	if runtime.lastCfg.WorkspacePath != physicalWorkspace {
		t.Fatalf("runtime workspace = %q, want %q", runtime.lastCfg.WorkspacePath, physicalWorkspace)
	}
	if _, err := os.Stat(wantWorkspace); err != nil {
		t.Fatalf("scratch workspace not created at %q: %v", wantWorkspace, err)
	}
}

// TestStartSession_SpawnDoesNotPanicWhenNoTrackerToken is a regression test for
// issue #2685: when no tracker credentials are configured, the session service
// must tolerate a true-nil ports.Tracker so Spawn's issue-context guard fires
// instead of dereferencing a typed-nil *github.Tracker. The pre-fix wiring
// assigned the typed-nil return of newGitHubTracker directly, and
// `ao spawn --issue` panicked on the first lookup. The multi-tracker is now
// built once in Run and passed in (see newMultiTracker); this test exercises
// the service-side nil-guard directly.
func TestStartSession_SpawnDoesNotPanicWhenNoTrackerToken(t *testing.T) {
	t.Setenv("AO_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "mer", Path: "/repo/mer", RepoOriginURL: "https://github.com/acme/repo", RegisteredAt: time.Now()}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lcm := lifecycle.New(store, nil)
	cfg := config.Config{DataDir: t.TempDir()}
	rt := runtimeselect.New(nil, cfg.RunFilePath)
	messenger := newSessionMessenger(store, rt, log)
	agents, agentsErr := buildAgentResolver(config.DefaultAgent, log)
	if agentsErr != nil {
		t.Fatalf("buildAgentResolver: %v", agentsErr)
	}
	svc, _, _, err := startSession(context.Background(), cfg, rt, store, lcm, messenger, telemetryadapter.NoopSink{}, agents, nil, nil, nil, nil, nil, nil, nil, nil, nil, log)
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}

	// Spawn reaches withIssueContext (and the tracker guard) before the manager
	// tries to materialize a workspace. The manager may return an error from the
	// no-op runtime, but it must not panic — that is the regression.
	_, _, _, _ = svc.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, IssueID: "107"})
}

func TestWiring_SeedScratchProjectOnBootUsesDataDir(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Config{DataDir: t.TempDir(), Agent: string(domain.HarnessCodex)}
	projects := projectsvc.NewWithDeps(projectsvc.Deps{Store: store, DefaultHarness: domain.HarnessCodex})
	if err := seedScratchProjectOnBoot(ctx, cfg, projects); err != nil {
		t.Fatalf("seedScratchProjectOnBoot: %v", err)
	}

	got, ok, err := store.GetProject(ctx, "scratch")
	if err != nil || !ok {
		t.Fatalf("GetProject(scratch): ok=%v err=%v", ok, err)
	}
	if got.Kind != domain.ProjectKindScratch {
		t.Fatalf("kind = %q, want scratch", got.Kind)
	}
	if want := filepath.Join(cfg.DataDir, "scratch", "default"); got.Path != want {
		t.Fatalf("path = %q, want %q", got.Path, want)
	}
}

// TestStartTrackerIntake_RunsEvenWithoutEnabledProjects is a regression test:
// startTrackerIntake used to scan projects once at call time and skip starting
// the observer loop entirely when none had intake enabled yet. Poll() itself
// already re-reads project config on every tick, so a project enabling
// intake after daemon boot was silently never picked up until a restart. The
// loop must always start; Poll is what decides whether there's work to do.
func TestStartTrackerIntake_RunsEvenWithoutEnabledProjects(t *testing.T) {
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lcm := lifecycle.New(store, nil)
	cfg := config.Config{DataDir: t.TempDir()}
	rt := runtimeselect.New(nil, cfg.RunFilePath)
	messenger := newSessionMessenger(store, rt, log)
	agents, agentsErr := buildAgentResolver(config.DefaultAgent, log)
	if agentsErr != nil {
		t.Fatalf("buildAgentResolver: %v", agentsErr)
	}
	svc, _, _, err := startSession(context.Background(), cfg, rt, store, lcm, messenger, telemetryadapter.NoopSink{}, agents, nil, nil, nil, nil, nil, nil, nil, nil, nil, log)
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := startTrackerIntake(ctx, store, svc, newMultiTracker(config.GitLabConfig{}, log), log)

	select {
	case <-done:
		t.Fatal("startTrackerIntake returned an already-closed channel; observer loop did not start")
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("observer did not stop after context cancellation")
	}
}

func TestGhTokenSourcePrefersAOGitHubToken(t *testing.T) {
	t.Setenv("AO_GITHUB_TOKEN", "ao-token")
	t.Setenv("GITHUB_TOKEN", "github-token")
	token, err := (&ghTokenSource{}).Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "ao-token" {
		t.Fatalf("token = %q, want AO_GITHUB_TOKEN", token)
	}
}

func TestGhTokenSourceFallsBackToGITHUBToken(t *testing.T) {
	t.Setenv("AO_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "github-token")
	token, err := (&ghTokenSource{}).Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "github-token" {
		t.Fatalf("token = %q, want GITHUB_TOKEN", token)
	}
}

type captureRuntimeSender struct {
	handle  ports.RuntimeHandle
	message string
}

func (c *captureRuntimeSender) SendMessage(_ context.Context, handle ports.RuntimeHandle, message string) error {
	c.handle = handle
	c.message = message
	return nil
}

// TestWiring_SessionMessengerSendsToRuntimePane asserts the daemon wires ao
// send to the live runtime pane and resolves the handle from the shared store.
func TestWiring_SessionMessengerSendsToRuntimePane(t *testing.T) {
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	runtime := &captureRuntimeSender{}
	messenger := newSessionMessenger(store, runtime, nil)

	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p", Path: "/repo/p", RegisteredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "p", Kind: domain.KindWorker,
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
		Metadata: domain.SessionMetadata{RuntimeHandleID: "ao-1/terminal_0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := messenger.Send(ctx, rec.ID, "hello agent"); err != nil {
		t.Fatalf("messenger.Send: %v", err)
	}
	if runtime.handle.ID != "ao-1/terminal_0" {
		t.Fatalf("handle = %q, want ao-1/terminal_0", runtime.handle.ID)
	}
	if runtime.message != "hello agent" {
		t.Fatalf("message = %q, want hello agent", runtime.message)
	}
}

func TestWiring_SessionMessengerWrapsLookupErrors(t *testing.T) {
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	messenger := newSessionMessenger(store, &captureRuntimeSender{}, nil)
	err = messenger.Send(context.Background(), "missing", "hello")
	if !errors.Is(err, sessionmanager.ErrNotFound) {
		t.Fatalf("missing session should wrap ErrNotFound, got %v", err)
	}
}

func TestWiring_SessionMessengerRequiresRuntimeHandle(t *testing.T) {
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p", Path: "/repo/p", RegisteredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "p", Kind: domain.KindWorker,
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	messenger := newSessionMessenger(store, &captureRuntimeSender{}, nil)
	err = messenger.Send(ctx, rec.ID, "hello")
	if !errors.Is(err, sessionmanager.ErrIncompleteHandle) {
		t.Fatalf("missing runtime handle should wrap ErrIncompleteHandle, got %v", err)
	}
}

func TestWiring_SessionMessengerRejectsTerminatedSession(t *testing.T) {
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p", Path: "/repo/p", RegisteredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "p", Kind: domain.KindWorker,
		IsTerminated: true,
		Activity:     domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
		Metadata:     domain.SessionMetadata{RuntimeHandleID: "ao-1/terminal_0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &captureRuntimeSender{}
	messenger := newSessionMessenger(store, runtime, nil)
	err = messenger.Send(ctx, rec.ID, "hello")
	if !errors.Is(err, sessionmanager.ErrTerminated) {
		t.Fatalf("terminated session should wrap ErrTerminated, got %v", err)
	}
	if runtime.handle.ID != "" || runtime.message != "" {
		t.Fatalf("runtime should not be called for terminated sessions, got handle=%q message=%q", runtime.handle.ID, runtime.message)
	}
}

type captureMessenger struct {
	msgs []capturedMessage
}

type capturedMessage struct {
	id  domain.SessionID
	msg string
}

func (c *captureMessenger) Send(_ context.Context, id domain.SessionID, msg string) error {
	c.msgs = append(c.msgs, capturedMessage{id: id, msg: msg})
	return nil
}

// TestWiring_StartLifecycleThreadsMessengerIntoLCM asserts startLifecycle
// constructs the LCM with a real messenger by driving an SCM observation
// through the wired stack and checking the messenger receives the CI-failure
// nudge — a nil messenger here would silently drop the send inside sendOnce.
func TestWiring_StartLifecycleThreadsMessengerIntoLCM(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel must run BEFORE Stop so the reaper goroutine's ctx.Done() fires;
	// Stop is a no-op otherwise. Cleanup is LIFO, so register Stop first.
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p", Path: "/repo/p", RegisteredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID:    "p",
		Kind:         domain.KindWorker,
		Activity:     domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
		AutoInjectCI: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	messenger := &captureMessenger{}
	stack := startLifecycle(ctx, store, tmux.New(tmux.Options{}), messenger, nil, nil, nil, log)
	t.Cleanup(stack.Stop)
	t.Cleanup(cancel)

	obs := ports.SCMObservation{
		Fetched: true,
		PR:      ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", Number: 1, HeadSHA: "c1"},
		CI: ports.SCMCIObservation{
			Summary:      string(domain.CIFailing),
			HeadSHA:      "c1",
			FailedChecks: []ports.SCMCheckObservation{{Name: "build", Status: string(domain.PRCheckFailed), LogTail: "boom"}},
		},
	}
	if err := store.WriteSCMObservation(ctx, domain.PullRequest{
		URL:       obs.PR.URL,
		SessionID: rec.ID,
		Number:    obs.PR.Number,
		HeadSHA:   obs.PR.HeadSHA,
		UpdatedAt: time.Now(),
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatalf("persist PR before lifecycle: %v", err)
	}
	if err := stack.LCM.ApplySCMObservation(ctx, rec.ID, obs); err != nil {
		t.Fatalf("ApplySCMObservation: %v", err)
	}
	if len(messenger.msgs) != 1 {
		t.Fatalf("want one nudge to flow through the wired messenger, got %d", len(messenger.msgs))
	}
	if messenger.msgs[0].id != rec.ID {
		t.Fatalf("nudge sent to %q, want %q", messenger.msgs[0].id, rec.ID)
	}
}

// TestWiring_MergeConflictNudgeReArmsAfterConflictClears is the end-to-end
// counterpart to the lifecycle unit tests for #4528, over the real sqlite store
// the daemon runs on: it drives the SCM observer's entrypoint
// (ApplySCMObservation) through the full mergeable → conflicting → mergeable →
// conflicting cycle and asserts the second conflict notifies again. The
// dedup signature is persisted in pr.last_nudge_signature, so a fake store
// cannot prove the round trip actually survives the real column.
func TestWiring_MergeConflictNudgeReArmsAfterConflictClears(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p", Path: "/repo/p", RegisteredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "p",
		Kind:      domain.KindWorker,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}

	const prURL = "https://github.com/o/r/pull/1"
	if err := store.WriteSCMObservation(ctx, domain.PullRequest{
		URL:       prURL,
		SessionID: rec.ID,
		Number:    1,
		UpdatedAt: time.Now(),
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatalf("persist PR before lifecycle: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	messenger := &captureMessenger{}
	stack := startLifecycle(ctx, store, tmux.New(tmux.Options{}), messenger, nil, nil, nil, log)
	t.Cleanup(stack.Stop)
	t.Cleanup(cancel)

	observe := func(state domain.Mergeability) {
		t.Helper()
		if err := stack.LCM.ApplySCMObservation(ctx, rec.ID, ports.SCMObservation{
			Fetched:      true,
			PR:           ports.SCMPRObservation{URL: prURL, Number: 1},
			Mergeability: ports.SCMMergeabilityObservation{State: string(state), Conflict: state == domain.MergeConflicting},
		}); err != nil {
			t.Fatalf("ApplySCMObservation(%s): %v", state, err)
		}
	}

	observe(domain.MergeMergeable)
	observe(domain.MergeConflicting)
	if len(messenger.msgs) != 1 {
		t.Fatalf("first conflict should nudge once, got %d: %v", len(messenger.msgs), messenger.msgs)
	}
	observe(domain.MergeConflicting)
	if len(messenger.msgs) != 1 {
		t.Fatalf("an unchanged conflict must stay deduplicated, got %d: %v", len(messenger.msgs), messenger.msgs)
	}
	observe(domain.MergeMergeable)
	observe(domain.MergeConflicting)
	if len(messenger.msgs) != 2 {
		t.Fatalf("a conflict returning after the PR went mergeable should nudge again, got %d: %v", len(messenger.msgs), messenger.msgs)
	}
	if messenger.msgs[1].id != rec.ID || !strings.Contains(messenger.msgs[1].msg, "merge conflicts") {
		t.Fatalf("second nudge is not the merge-conflict nudge for this session: %+v", messenger.msgs[1])
	}

	// A daemon restart rebuilds the lifecycle manager with empty in-memory dedup
	// maps, so only pr.last_nudge_signature carries the state forward. A bare
	// lifecycle.New over the same store is that rebuild without the background
	// observers a second startLifecycle would leave running. The still-unresolved
	// conflict must stay quiet across that boundary.
	restarted := &captureMessenger{}
	restartedLCM := lifecycle.New(store, restarted)
	if err := restartedLCM.ApplySCMObservation(ctx, rec.ID, ports.SCMObservation{
		Fetched:      true,
		PR:           ports.SCMPRObservation{URL: prURL, Number: 1},
		Mergeability: ports.SCMMergeabilityObservation{State: string(domain.MergeConflicting), Conflict: true},
	}); err != nil {
		t.Fatalf("ApplySCMObservation after restart: %v", err)
	}
	if len(restarted.msgs) != 0 {
		t.Fatalf("restart replayed an already-delivered conflict nudge: %v", restarted.msgs)
	}
}

// TestProjectRepoResolver_ResolvesRegisteredProject asserts the DB-backed repo
// resolver turns a registered project into its on-disk repo path (so spawns
// materialise a worktree), and fails loudly for an unregistered project.
func TestProjectRepoResolver_ResolvesRegisteredProject(t *testing.T) {
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "mer", Path: "/repo/mer", RegisteredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	r := projectRepoResolver{store: store}
	got, err := r.RepoPath("mer")
	if err != nil {
		t.Fatalf("RepoPath(mer): %v", err)
	}
	if got != "/repo/mer" {
		t.Fatalf("RepoPath(mer) = %q, want /repo/mer", got)
	}
	_, err = r.RepoPath("nope")
	if err == nil {
		t.Fatal("expected an error for an unregistered project")
	}
	// Guard the sentinel wrapping so the HTTP 400 mapping can't silently regress.
	if !errors.Is(err, sessionmanager.ErrProjectNotResolvable) {
		t.Fatalf("unregistered-project error should wrap ErrProjectNotResolvable, got %v", err)
	}
}

// fakeSessionLifecycle records calls to Reconcile and RestoreAll so tests can
// assert the daemon wiring invokes the correct methods without needing a real
// runtime or worktree.
type fakeSessionLifecycle struct {
	reconcileCalled           bool
	reconcileSafetyCalled     bool
	reconcileBackgroundCalled bool
	restoreAllCalled          bool
	reconcileErr              error
	restoreErr                error
}

type recordingAgentSwitchDaemonFaultStore struct {
	inputs []ports.AgentSwitchDaemonFault
}

func (s *recordingAgentSwitchDaemonFaultStore) EnqueueAgentSwitchDaemonFault(_ context.Context, input ports.AgentSwitchDaemonFault) (ports.AgentSwitchMutationResult, error) {
	s.inputs = append(s.inputs, input)
	return ports.AgentSwitchMutationResult{Enrollment: domain.AgentSwitchEnrollmentEnrolled}, nil
}

type fixedAgentSwitchReportingPolicy struct {
	authorization domain.AgentSwitchReportingAuthorization
}

func (p fixedAgentSwitchReportingPolicy) Authorization() domain.AgentSwitchReportingAuthorization {
	return p.authorization
}

func TestEnqueueAgentSwitchWorkerShutdownTimeoutCreatesOneDaemonAggregate(t *testing.T) {
	store := &recordingAgentSwitchDaemonFaultStore{}
	authorization := domain.AgentSwitchReportingAuthorization{
		Enabled: true, ConsentGeneration: "consent-generation", DestinationFingerprint: "destination-fingerprint",
	}
	at := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	if err := enqueueAgentSwitchWorkerShutdownTimeout(context.Background(), store, fixedAgentSwitchReportingPolicy{authorization}, "daemon-run-1", at); err != nil {
		t.Fatalf("enqueue shutdown timeout: %v", err)
	}
	if len(store.inputs) != 1 {
		t.Fatalf("daemon fault inputs = %d, want 1", len(store.inputs))
	}
	got := store.inputs[0]
	if got.DaemonRunID != "daemon-run-1" || got.Authorization != authorization {
		t.Fatalf("daemon fault scope = %+v", got)
	}
	if got.Fault.ReportKind != domain.AgentSwitchReportDaemonLifecycleFailure ||
		got.Fault.FailurePoint != domain.AgentSwitchFailureShutdownWorkerTimeout ||
		got.Fault.FaultCode != domain.AgentSwitchFaultShutdownWorkersTimedOut ||
		got.Fault.Execution != domain.AgentSwitchExecutionDaemonShutdown ||
		got.Fault.CallOutcome != domain.AgentSwitchCallTimedOut {
		t.Fatalf("daemon fault = %+v", got.Fault)
	}
}

func TestAgentSwitchWorkerWaitCancellationIsNotReportable(t *testing.T) {
	if agentSwitchWorkerWaitTimedOut(context.Canceled) {
		t.Fatal("ordinary shutdown cancellation was classified as a timeout")
	}
	if !agentSwitchWorkerWaitTimedOut(context.DeadlineExceeded) {
		t.Fatal("worker deadline was not classified as a timeout")
	}
}

func (f *fakeSessionLifecycle) Send(context.Context, domain.SessionID, string, *ports.SpawnAttachment) error {
	return nil
}

func (f *fakeSessionLifecycle) Kill(_ context.Context, _ domain.SessionID) (bool, error) {
	return false, nil
}

func (f *fakeSessionLifecycle) Reconcile(_ context.Context) error {
	f.reconcileCalled = true
	return f.reconcileErr
}

func (f *fakeSessionLifecycle) ReconcileStartupSafety(_ context.Context) error {
	f.reconcileSafetyCalled = true
	return f.reconcileErr
}

func (f *fakeSessionLifecycle) ReconcileBackground(_ context.Context) error {
	f.reconcileBackgroundCalled = true
	return f.reconcileErr
}

func (f *fakeSessionLifecycle) RestoreAll(_ context.Context) error {
	f.restoreAllCalled = true
	return f.restoreErr
}

func (*fakeSessionLifecycle) WaitAgentSwitchWorkers(context.Context) error { return nil }

func (f *fakeSessionLifecycle) SetShellTerminalCloser(sessionmanager.ShellTerminalCloser) {}
func (f *fakeSessionLifecycle) SetTerminalInputGate(sessionmanager.TerminalInputGate)     {}
func (f *fakeSessionLifecycle) AcquireSessionInput(domain.SessionID) (func(), bool) {
	return func() {}, true
}

func (f *fakeSessionLifecycle) SessionMutationInProgress(domain.SessionID) bool         { return false }
func (f *fakeSessionLifecycle) SetReviewerTerminator(sessionmanager.ReviewerTerminator) {}
func (f *fakeSessionLifecycle) SetHarnessUseGate(sessionmanager.HarnessUseGate)         {}
func (f *fakeSessionLifecycle) CodexAccountSwitchInProgress() bool                      { return false }
func (f *fakeSessionLifecycle) StartCodexAccountSwitch(context.Context, ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error) {
	return domain.CodexAccountSwitch{}, nil
}
func (f *fakeSessionLifecycle) RecoverCodexAccountSwitch(context.Context, string) (domain.CodexAccountSwitch, error) {
	return domain.CodexAccountSwitch{}, nil
}
func (f *fakeSessionLifecycle) GetActiveCodexAccountSwitch(context.Context) (domain.CodexAccountSwitch, bool, error) {
	return domain.CodexAccountSwitch{}, false, nil
}
func (f *fakeSessionLifecycle) SetCodexAccountSwitchObserver(func()) {}

// TestWiring_SessionLifecycleInterfaceInvokedByDaemon asserts the
// sessionLifecycle interface is satisfied by *sessionmanager.Manager (compile
// check) and that Reconcile and RestoreAll dispatch correctly through the
// interface, matching what daemon.go wires at boot.
func TestWiring_SessionLifecycleInterfaceInvokedByDaemon(t *testing.T) {
	// Verify *sessionmanager.Manager satisfies the interface at compile time.
	var _ sessionLifecycle = (*sessionmanager.Manager)(nil)

	fake := &fakeSessionLifecycle{}
	ctx := context.Background()

	// Dispatch through the interface variable to exercise the real dispatch
	// path, not just direct struct method calls.
	var sl sessionLifecycle = fake

	if err := sl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !fake.reconcileCalled {
		t.Fatal("Reconcile was not called through the interface")
	}

	if err := sl.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if !fake.restoreAllCalled {
		t.Fatal("RestoreAll was not called through the interface")
	}
}

type selectableRuntime struct {
	lastCfg ports.RuntimeConfig
}

func (r *selectableRuntime) Create(_ context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	r.lastCfg = cfg
	return ports.RuntimeHandle{ID: "runtime-1"}, nil
}

func (r *selectableRuntime) Destroy(context.Context, ports.RuntimeHandle) error { return nil }

func (r *selectableRuntime) GetOutput(context.Context, ports.RuntimeHandle, int) (string, error) {
	return "", nil
}

func (r *selectableRuntime) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}

func (r *selectableRuntime) ProbeFencedRuntime(context.Context, ports.FencedRuntimeRef) ports.FencedProbeResult {
	return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonProbeFailed}
}

func (r *selectableRuntime) Attach(context.Context, ports.RuntimeHandle, uint16, uint16) (ports.Stream, error) {
	return nil, nil
}

func (r *selectableRuntime) Interrupt(context.Context, ports.RuntimeHandle) error { return nil }
func (r *selectableRuntime) SendInput(context.Context, ports.RuntimeHandle, string) error {
	return nil
}

func (r *selectableRuntime) SendMessage(context.Context, ports.RuntimeHandle, string) error {
	return nil
}

// TestWiring_NewMultiTracker_NeverTypedNilWhenNoGitHubToken verifies the
// typed-nil guard from issue #2685 at the multi-tracker wiring level. With no
// GitHub token the GitHub slot is lazily constructed, so newMultiTracker must
// return a truly non-nil, usable ports.Tracker — never a typed-nil (non-nil
// interface wrapping a nil pointer), which would bypass the session service's
// `tracker == nil` guard and panic on first call.
func TestWiring_NewMultiTracker_NeverTypedNilWhenNoGitHubToken(t *testing.T) {
	t.Setenv("AO_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := newMultiTracker(config.GitLabConfig{}, log)
	if tracker == nil {
		t.Fatal("newMultiTracker = nil, want non-nil: the GitHub slot is lazily constructed and always present")
	}
	// With no credentials the lazy GitHub adapter fails to resolve, so an
	// error from Preflight is fine — it just must not panic.
	_ = tracker.Preflight(context.Background())
}

// TestWiring_NewMultiTracker_ReturnsNonNilWhenGitHubHasToken verifies the
// degrade-gracefully pattern: when only the GitHub tracker can construct
// (GitLab token missing), the multi-tracker still returns a non-nil
// ports.Tracker that serves GitHub issue lookups.
func TestWiring_NewMultiTracker_ReturnsNonNilWhenGitHubHasToken(t *testing.T) {
	t.Setenv("AO_GITHUB_TOKEN", "gh-test-token")
	t.Setenv("AO_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := newMultiTracker(config.GitLabConfig{}, log)
	if tracker == nil {
		t.Fatal("newMultiTracker = nil, want non-nil when GitHub token is available")
	}
}

func writeFakeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake executable %s: %v", path, err)
	}
}
