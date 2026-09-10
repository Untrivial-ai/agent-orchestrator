// Package lifecycle implements the synchronous reducer that writes durable
// session lifecycle facts. It deliberately keeps the session model small:
// activity_state plus an is_terminated bit are the only persisted status-like
// facts on the session row.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/ownership"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/reqid"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionguard"
)

type sessionStore interface {
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
	UpdateSession(ctx context.Context, rec domain.SessionRecord) error
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
	// UpdateSessionFromActivitySignal is a narrow, owner-generation-fenced
	// write. It returns false when a concurrent lifecycle/agent-switch boundary
	// made the reducer's previously read session stale.
	UpdateSessionFromActivitySignal(ctx context.Context, rec domain.SessionRecord) (bool, error)
	// ListSessions returns every session in a project. The dispatcher reads it
	// to resolve the current orchestrator at delivery time.
	ListSessions(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error)
	// ListPRsBySession returns every PR row tracked for the session. The
	// reducer reads it to apply the multi-PR completion rule (terminate only
	// when no open PR remains and at least one merged) and to suppress
	// merge-conflict nudges on PRs stacked behind an open parent.
	ListPRsBySession(ctx context.Context, id domain.SessionID) ([]domain.PullRequest, error)
	GetPR(ctx context.Context, prURL string) (domain.PullRequest, bool, error)
	// ListPRReviews and ListPRComments return the effective rows committed by
	// the SCM observer, including each item's preserved injection decision.
	ListPRReviews(ctx context.Context, prURL string) ([]domain.PullRequestReview, error)
	ListPRComments(ctx context.Context, prURL string) ([]domain.PullRequestComment, error)
	// GetPRLastNudgeSignature / UpdatePRLastNudgeSignature persist the
	// reaction-dedup map so nudges survive a daemon restart.
	GetPRLastNudgeSignature(ctx context.Context, prURL string) (string, error)
	UpdatePRLastNudgeSignature(ctx context.Context, prURL, payload string) error
}

// controllerEpochStore is the atomic persistence primitive used by
// CommitControllerEpoch. It stays optional on the broad lifecycle store so
// focused reducer fakes do not need controller-transition methods; production
// SQLite implements it.
type controllerEpochStore interface {
	CommitSessionControllerEpoch(
		context.Context,
		domain.SessionID,
		domain.SessionMode,
		domain.SessionMode,
		string,
		time.Time,
	) (bool, error)
}

// chatSpawnStore commits the lifecycle facts and the provider boundary in one
// transaction. A fresh provider must never become the durable session owner
// while the conversation head still names the provider it replaced.
type chatSpawnStore interface {
	CommitChatSpawn(context.Context, domain.SessionRecord, domain.ConversationBranch) error
}

// preparedChatSpawnStore extends the atomic Chat publication boundary with
// native-history projection. The callback runs on the same SQLite transaction
// after the provider boundary and generation are staged, but before the live
// session record is published. It remains optional for focused lifecycle fakes.
type preparedChatSpawnStore interface {
	CommitChatSpawnPrepared(
		context.Context,
		domain.SessionRecord,
		domain.ConversationBranch,
		func(context.Context) error,
	) error
}

// agentSwitchSourceStopStore and agentSwitchTargetActivationStore are the
// atomic persistence primitives used at the two agent-switch ownership
// boundaries. They remain optional so focused lifecycle reducer fakes do not
// need to implement the agent-switch saga; production SQLite implements both.
type agentSwitchSourceStopStore interface {
	ConfirmAgentSwitchSourceStopped(context.Context, domain.AgentSwitchSourceStopConfirmation) (bool, error)
}

type agentSwitchTargetActivationStore interface {
	ActivateAgentSwitchTarget(context.Context, domain.AgentSwitchTargetActivation) (bool, error)
}

type agentSwitchChatTargetActivationStore interface {
	ActivateChatAgentSwitchTarget(context.Context, domain.AgentSwitchChatTargetActivation) (bool, error)
}

// notificationSink is the optional lifecycle-to-notification-producer boundary.
type notificationSink interface {
	Notify(ctx context.Context, intent ports.NotificationIntent) error
	// Resolve closes notifications whose underlying issue went away. It is the
	// only way a notification leaves the unresolved list: there is no manual
	// user-facing resolve action.
	Resolve(ctx context.Context, res ports.NotificationResolution) error
}

// projectConfigLoader resolves a project's config so MarkTerminated can check
// the ContainerReap opt-out before reaping. A load failure must not fall
// through to reaping - see ports.ContainerReaper below.
type projectConfigLoader interface {
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
}

type sessionTerminator interface {
	Kill(ctx context.Context, id domain.SessionID) (bool, error)
}

type sessionUsageFinalizer interface {
	FinalizeSession(
		ctx context.Context,
		id domain.SessionID,
		expectedRuntimeLaunchID string,
		expectedSessionRevision time.Time,
	) error
}

type sessionUsageReactivator interface {
	ReactivateSession(ctx context.Context, id domain.SessionID, expectedRuntimeLaunchID string) error
}

type sessionOperationGate interface {
	SessionMutationInProgress(id domain.SessionID) bool
}

type pendingLaunch struct {
	launchID string
	ready    chan struct{}
}

// Option customizes a Manager.
type Option func(*Manager)

// WithNotificationSink wires lifecycle notification intents to a write-side producer.
func WithNotificationSink(sink notificationSink) Option {
	return func(m *Manager) { m.notifications = sink }
}

// WithTelemetry wires lifecycle activity transitions to the shared telemetry sink.
func WithTelemetry(sink ports.EventSink) Option {
	return func(m *Manager) { m.telemetry = sink }
}

// WithContainerReaper wires the container leg of #2652: MarkTerminated will
// force-remove the terminated session's ao.session-labeled Docker containers,
// unless the project opts out via ProjectConfig.ContainerReap.Disabled.
func WithContainerReaper(reaper ports.ContainerReaper, projects projectConfigLoader) Option {
	return func(m *Manager) {
		m.containers = reaper
		m.projects = projects
	}
}

// WithActiveSteering supplies the adapter-provided active-turn steering
// capability (see ports.ActiveTurnSteerer). Without it the reducer assumes no
// harness can be steered mid-turn.
func WithActiveSteering(pred func(domain.AgentHarness) bool) Option {
	return func(m *Manager) {
		if pred != nil {
			m.steerActive = pred
		}
	}
}

// WithStartupSignalGate supplies the adapter capability predicate used to
// suppress reaction writes until a TUI's first startup-ready hook arrives.
func WithStartupSignalGate(pred func(domain.AgentHarness) bool) Option {
	return func(m *Manager) {
		if pred != nil {
			m.startupSignalGatesInput = pred
		}
	}
}

// WithUrgentNudgeGate supplies the adapter capability predicate that decides
// whether an urgent (merge-conflict) nudge may reach a session at a
// waiting_input prompt: true only for harnesses that report a permission dialog
// as blocked rather than as waiting_input. Without it the reducer treats every
// waiting_input prompt as a possible masked decision and withholds the urgent
// nudge until the session is active or idle.
func WithUrgentNudgeGate(pred func(domain.AgentHarness) bool) Option {
	return func(m *Manager) {
		if pred != nil {
			m.urgentNudgeWaitingInputSafe = pred
		}
	}
}

// Manager reduces runtime, activity, spawn, and termination observations into durable session facts.
// It also owns agent nudges caused by PR observations, including merge-conflict, CI-failure, and review-feedback prompts.
type Manager struct {
	store sessionStore
	// guard is the shared pane-write primitive every reaction nudge goes
	// through (see sessionguard). Nil when no messenger was wired: reaction
	// nudges become no-ops but the reducer still runs.
	guard         *sessionguard.Guard
	notifications notificationSink
	// completionTerminator is late-bound because Session Manager itself depends
	// on this lifecycle reducer. It is required before the SCM observer starts.
	completionTerminator sessionTerminator
	// usageFinalizer is late-bound because the usage pipeline is optional. It
	// receives terminal intent before is_terminated makes the session ineligible
	// for normal source discovery.
	usageFinalizer   sessionUsageFinalizer
	usageReactivator sessionUsageReactivator
	containers       ports.ContainerReaper
	projects         projectConfigLoader
	operationGateMu  sync.RWMutex
	operationGate    sessionOperationGate

	mu        sync.Mutex
	window    time.Duration
	clock     func() time.Time
	react     reactionState
	telemetry ports.EventSink
	// flights tracks, per session, the in-flight tool executions and the
	// pending permission dialog's identity (see toolFlight). Guarded by mu.
	flights map[domain.SessionID]*toolFlight
	// pendingLaunches closes the small ordering gap between starting a supervised
	// process and durably recording its generation in MarkSpawned. A hook from
	// that exact generation waits on ready instead of being discarded as stale.
	// This coordination is intentionally memory-only: a daemon crash leaves the
	// durable session exited, so the user can safely retry the resume.
	pendingLaunches map[domain.SessionID]pendingLaunch
	// steerActive reports whether a harness can safely receive a write during an
	// active turn (input steers the run) rather than only while idle. Supplied by
	// the agent adapter via WithActiveSteering; the default answers false, so an
	// unknown harness is only written to while idle.
	steerActive             func(domain.AgentHarness) bool
	startupSignalGatesInput func(domain.AgentHarness) bool
	// urgentNudgeWaitingInputSafe reports whether a harness surfaces a permission
	// dialog AS blocked (rather than as waiting_input), so an urgent merge-conflict
	// nudge is safe to paste at a waiting_input prompt. Supplied by the agent
	// adapter via WithUrgentNudgeGate; the default answers false, so an unknown
	// harness never takes an urgent write while waiting_input.
	urgentNudgeWaitingInputSafe func(domain.AgentHarness) bool
}

// New builds a Lifecycle Manager over the session store it writes and the messenger it uses for agent nudges.
func New(store sessionStore, messenger ports.AgentMessenger, opts ...Option) *Manager {
	// UTC so activity-driven LastActivityAt/UpdatedAt match spawn-stamped
	// timestamps (the session manager clock is UTC too); a local clock here left
	// `ao session get` showing created in UTC but updated in local time. A
	// WithClock option may still override this in tests.
	clock := func() time.Time { return time.Now().UTC() }
	m := &Manager{
		store:                       store,
		window:                      defaultRecentActivityWindow,
		clock:                       clock,
		react:                       newReactionState(),
		flights:                     map[domain.SessionID]*toolFlight{},
		pendingLaunches:             map[domain.SessionID]pendingLaunch{},
		steerActive:                 func(domain.AgentHarness) bool { return false },
		startupSignalGatesInput:     func(domain.AgentHarness) bool { return false },
		urgentNudgeWaitingInputSafe: func(domain.AgentHarness) bool { return false },
	}
	if messenger != nil {
		m.guard = sessionguard.New(store, messenger, nil)
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.guard != nil {
		m.guard.SetStartupSignalGate(m.startupSignalGatesInput)
	}
	return m
}

// SetCompletionTerminator wires merge completion to the same teardown path as
// an explicit user kill.
func (m *Manager) SetCompletionTerminator(terminator sessionTerminator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completionTerminator = terminator
}

// SetUsageFinalizer wires termination and relaunches to usage collection.
// Telemetry failures never block the lifecycle transition.
func (m *Manager) SetUsageFinalizer(finalizer sessionUsageFinalizer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usageFinalizer = finalizer
	m.usageReactivator, _ = finalizer.(sessionUsageReactivator)
}

// SetSessionInputLease late-binds Session Manager's pane-input authority into
// lifecycle's guarded reaction writes. Lifecycle is constructed first during
// daemon boot, so constructor injection would create a dependency cycle.
func (m *Manager) SetSessionInputLease(lease sessionguard.InputLease) {
	if m.guard != nil {
		m.guard.SetInputLease(lease)
	}
}

// SetSessionOperationGate prevents observation-driven terminal facts from
// racing AO's deliberate provider replacement/relaunch operations.
func (m *Manager) SetSessionOperationGate(gate sessionOperationGate) {
	m.operationGateMu.Lock()
	m.operationGate = gate
	m.operationGateMu.Unlock()
}

func (m *Manager) sessionMutationInProgress(id domain.SessionID) bool {
	m.operationGateMu.RLock()
	gate := m.operationGate
	m.operationGateMu.RUnlock()
	return gate != nil && gate.SessionMutationInProgress(id)
}

// PrepareLaunch registers a supervised generation before the runtime starts.
// Hooks from that exact generation wait until MarkSpawned commits the generation
// instead of racing the old durable generation and being discarded as stale.
func (m *Manager) PrepareLaunch(id domain.SessionID, launchID string) error {
	launchID = strings.TrimSpace(launchID)
	if launchID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if pending, ok := m.pendingLaunches[id]; ok {
		if pending.launchID == launchID {
			return nil
		}
		return fmt.Errorf("lifecycle: session %q already has launch %q in progress", id, pending.launchID)
	}
	m.pendingLaunches[id] = pendingLaunch{launchID: launchID, ready: make(chan struct{})}
	return nil
}

// CancelLaunch releases hooks waiting on a generation whose runtime failed to
// start. Once released, normal generation fencing discards those signals.
func (m *Manager) CancelLaunch(id domain.SessionID, launchID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishLaunchLocked(id, strings.TrimSpace(launchID))
}

// ReleaseLaunch publishes that the caller has durably committed the prepared
// generation. Hooks waiting behind PrepareLaunch may now re-read the session
// row and pass normal generation fencing. Unlike MarkSpawned, this does not
// rewrite session ownership; agent switching commits that ownership together
// with its saga state in the AgentSwitchStore transaction.
func (m *Manager) ReleaseLaunch(id domain.SessionID, launchID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishLaunchLocked(id, strings.TrimSpace(launchID))
}

func (m *Manager) finishLaunchLocked(id domain.SessionID, launchID string) {
	if launchID == "" {
		return
	}
	pending, ok := m.pendingLaunches[id]
	if !ok || pending.launchID != launchID {
		return
	}
	delete(m.pendingLaunches, id)
	close(pending.ready)
}

func (m *Manager) mutate(ctx context.Context, id domain.SessionID, fn func(domain.SessionRecord, time.Time) (domain.SessionRecord, bool)) error {
	m.mu.Lock()
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil || !ok {
		m.mu.Unlock()
		return err
	}
	now := m.clock()
	next, changed := fn(rec, now)
	if !changed {
		m.mu.Unlock()
		return nil
	}
	next.UpdatedAt = now
	if err := m.store.UpdateSession(ctx, next); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	// Notification side effects run outside the reducer lock, like the activity
	// path does: a slow notification store must never stall lifecycle writes.
	m.resolveNotifications(ctx, needsInputResolutions(rec, next, now)...)
	return nil
}

// needsInputResolutions reports the needs-input notification a session write
// just made stale. A session that stopped waiting on the user — because the
// input arrived, or because the session ended — has nothing left to resolve.
func needsInputResolutions(prev, next domain.SessionRecord, now time.Time) []ports.NotificationResolution {
	if !prev.Activity.State.NeedsInput() {
		return nil
	}
	if next.Activity.State.NeedsInput() && !next.IsTerminated {
		return nil
	}
	return []ports.NotificationResolution{{
		Type:       domain.NotificationNeedsInput,
		SessionID:  next.ID,
		ResolvedAt: now,
	}}
}

// ApplyRuntimeObservation only writes when runtime liveness is unambiguous. A
// failed probe or liveness disagreement is ignored. Runtime death keeps the
// existing recent-activity guard; supervised workload death is independently
// fenced by the launch generation and never terminates the runtime.
func (m *Manager) ApplyRuntimeObservation(ctx context.Context, id domain.SessionID, f ports.RuntimeFacts) error {
	matchesLaunch := func(cur domain.SessionRecord) bool {
		currentLaunch := cur.Metadata.RuntimeLaunchID
		return currentLaunch == "" || f.LaunchID == currentLaunch
	}
	var (
		finalizer           sessionUsageFinalizer
		terminationLaunch   string
		terminationRevision time.Time
		shouldTerminate     bool
	)
	if err := m.mutate(ctx, id, func(cur domain.SessionRecord, now time.Time) (domain.SessionRecord, bool) {
		if cur.IsTerminated || !matchesLaunch(cur) {
			return cur, false
		}
		currentLaunch := cur.Metadata.RuntimeLaunchID
		if currentLaunch != "" && f.Runtime == ports.ProbeAlive && f.Workload == ports.ProbeDead {
			if cur.Activity.State == domain.ActivityExited {
				return cur, false
			}
			next := cur
			next.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: timeOr(f.ObservedAt, now)}
			delete(m.flights, id)
			return next, true
		}
		if !runtimeClearlyDead(f, cur.Activity, now, m.window) {
			return cur, false
		}
		if m.sessionMutationInProgress(id) {
			return cur, false
		}
		finalizer = m.usageFinalizer
		terminationLaunch = currentLaunch
		terminationRevision = cur.UpdatedAt
		shouldTerminate = true
		return cur, false
	}); err != nil || !shouldTerminate {
		return err
	}

	finalizeSessionUsage(ctx, id, terminationLaunch, terminationRevision, finalizer)

	terminated := false
	err := m.mutate(ctx, id, func(cur domain.SessionRecord, now time.Time) (domain.SessionRecord, bool) {
		if cur.IsTerminated || !cur.UpdatedAt.Equal(terminationRevision) ||
			cur.Metadata.RuntimeLaunchID != terminationLaunch || !matchesLaunch(cur) ||
			!runtimeClearlyDead(f, cur.Activity, now, m.window) || m.sessionMutationInProgress(id) {
			return cur, false
		}
		next := cur
		next.IsTerminated = true
		next.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: timeOr(f.ObservedAt, now)}
		// Reaper-driven death (crash/SIGKILL) never fires a session-end hook,
		// so this is the last chance to release the session's tool-flight
		// state; a leaked entry would otherwise persist for the daemon's life
		// (later observations return early on cur.IsTerminated). Runs under
		// m.mu — mutate holds it across this callback.
		delete(m.flights, id)
		terminated = true
		return next, true
	})
	if err != nil {
		return err
	}
	if terminated {
		// Route reaper-observed death through the same container-reap hook as
		// every other terminal path (#2652): a crash/SIGKILL detected by the
		// runtime reaper must not leave the session's Docker containers behind
		// just because it never called MarkTerminated directly.
		m.reapSessionContainers(ctx, id)
	}
	return nil
}

// ApplyActivitySignal records an authoritative agent activity signal and any
// native agent session id carried alongside it. Metadata-only hooks leave the
// existing activity and first-signal facts untouched.
func (m *Manager) ApplyActivitySignal(ctx context.Context, id domain.SessionID, s ports.ActivitySignal) error {
	s.AgentSessionID = strings.TrimSpace(s.AgentSessionID)
	s.LatestUserPrompt = strings.TrimSpace(s.LatestUserPrompt)
	s.LatestAssistantUpdate = strings.TrimSpace(s.LatestAssistantUpdate)
	s.TranscriptPath = strings.TrimSpace(s.TranscriptPath)
	s.LaunchID = strings.TrimSpace(s.LaunchID)
	s.ControllerGeneration = strings.TrimSpace(s.ControllerGeneration)
	// A response or Stop hook produced by AO's optional source handoff request
	// may contain last_assistant_message without echoing the internal prompt.
	// From collection through source teardown, do not let that coordination
	// response replace the latest user-facing assistant update used by the
	// target continuation. The semantic outcome may already be received,
	// rejected, timed out, or failed before the provider emits its final Stop
	// hook. not_attempted/unavailable do not prove an internal request landed,
	// so a legitimate source update remains user-facing in those states.
	if s.LatestAssistantUpdate != "" {
		if switchStore, ok := m.store.(ports.AgentSwitchStore); ok {
			if active, found, err := switchStore.GetActiveAgentSwitch(ctx, id); err == nil && found {
				internalRequestMayHaveLanded := false
				switch active.AgentHandoffStatus {
				case domain.AgentHandoffRequested, domain.AgentHandoffReceived, domain.AgentHandoffTimedOut,
					domain.AgentHandoffFailed, domain.AgentHandoffRejected:
					internalRequestMayHaveLanded = true
				}
				if internalRequestMayHaveLanded {
					switch active.State {
					case domain.AgentSwitchPreparingHandoff, domain.AgentSwitchStoppingSource:
						s.LatestAssistantUpdate = ""
					}
				}
			}
		}
	}
	if !s.Valid && s.AgentSessionID == "" && s.LatestUserPrompt == "" && s.LatestAssistantUpdate == "" && s.TranscriptPath == "" {
		return nil
	}
	if s.LaunchID != "" {
		if err := m.stagePendingAgentSwitchNativeMetadata(ctx, id, s); err != nil {
			return err
		}
	}
	var intent *ports.NotificationIntent
	m.mu.Lock()
	for {
		pending, ok := m.pendingLaunches[id]
		if !ok || s.LaunchID == "" || pending.launchID != s.LaunchID {
			break
		}
		ready := pending.ready
		m.mu.Unlock()
		select {
		case <-ready:
			m.mu.Lock()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ports.ErrSessionNotFound, id)
	}
	now := m.clock()
	if rec.IsTerminated {
		delete(m.flights, id)
		m.mu.Unlock()
		return nil
	}
	mode := domain.NormalizeSessionMode(rec.Mode)
	currentChatController := mode == domain.SessionModeChat &&
		s.ControllerGeneration != "" &&
		s.ControllerGeneration == rec.Metadata.ControllerGeneration
	// Controller ownership is mode-specific. A Chat controller has no terminal
	// launch id, so stale TUI metadata must not veto its current generation.
	// Provider hooks inherited from a shell never receive that internal
	// credential and therefore cannot mutate structured Chat lifecycle facts.
	if mode != domain.SessionModeChat && rec.Metadata.RuntimeLaunchID != "" &&
		s.LaunchID != rec.Metadata.RuntimeLaunchID {
		m.mu.Unlock()
		return nil
	}
	if (mode == domain.SessionModeChat && !currentChatController) ||
		(mode != domain.SessionModeChat && s.ControllerGeneration != "") {
		m.mu.Unlock()
		return nil
	}
	if !s.ExpectedUpdatedAt.IsZero() &&
		!rec.UpdatedAt.Equal(s.ExpectedUpdatedAt) {
		m.mu.Unlock()
		return nil
	}
	// An explicit prompt submission is proof that an agent was relaunched in the
	// preserved shell. Other same-generation callbacks may have been delayed
	// behind the process-exit report and cannot resurrect an exited workload.
	if rec.Activity.State == domain.ActivityExited && s.Valid && s.State != domain.ActivityExited &&
		(s.State != domain.ActivityActive || s.Event != "user-prompt-submit") {
		m.mu.Unlock()
		return nil
	}
	// Event-tagged signals fold through the session's tool-flight state first:
	// they may be suppressed (state write skipped) by the blocked-precedence
	// rule, while their tracking side effects still land. Untagged signals
	// (old CLIs, adapters without tool identity) pass through untouched —
	// last-writer-wins, exactly as before.
	promptAt := timeOr(s.Timestamp, now)
	metadataChanged := (s.AgentSessionID != "" && rec.Metadata.AgentSessionID != s.AgentSessionID) ||
		(s.AgentSessionID != "" && rec.Metadata.AgentSessionIDLaunchID != s.LaunchID) ||
		(s.LatestUserPrompt != "" && (rec.Metadata.LatestUserPrompt != s.LatestUserPrompt ||
			(s.Event == "user-prompt-submit" && promptAt.After(rec.Metadata.LatestUserPromptAt)))) ||
		(s.LatestAssistantUpdate != "" && rec.Metadata.LatestAssistantUpdate != s.LatestAssistantUpdate) ||
		(s.TranscriptPath != "" && rec.Metadata.NativeTranscriptPath != s.TranscriptPath)
	if s.Valid {
		s = m.applyToolPrecedenceLocked(id, rec.Activity.State, s)
	}
	if !s.Valid && !metadataChanged {
		m.mu.Unlock()
		return nil
	}
	if !s.Valid {
		applyActivityMetadata(&rec.Metadata, s, now)
		rec.UpdatedAt = now
		_, err := m.store.UpdateSessionFromActivitySignal(ctx, rec)
		m.mu.Unlock()
		return err
	}
	if metadataChanged {
		// Fold metadata into rec before copying it into next below, so the
		// activity and resume handle land in one store update.
		applyActivityMetadata(&rec.Metadata, s, now)
	}
	prevState := rec.Activity.State
	prevAt := rec.Activity.LastActivityAt
	act := domain.Activity{State: s.State, LastActivityAt: timeOr(s.Timestamp, now)}
	sameState := sameActivity(rec.Activity, act)
	// A same-state repeat is still a write when it is the FIRST signal for
	// this spawn: the receipt itself is a durable fact (it clears the
	// no_signal display status). Hook deliveries are best-effort, so the
	// first to ARRIVE may match the seeded state — e.g. a turn's "active"
	// POST is lost and its Stop hook lands idle on the idle-seeded row.
	if sameState && !rec.FirstSignalAt.IsZero() {
		if metadataChanged || s.Event == "user-prompt-submit" {
			rec.UpdatedAt = now
			applied, err := m.store.UpdateSessionFromActivitySignal(ctx, rec)
			m.mu.Unlock()
			if err != nil {
				return err
			}
			if !applied {
				return nil
			}
			return m.acknowledgeAgentSwitchTarget(ctx, id, s, now)
		}
		m.mu.Unlock()
		return nil
	}
	next := rec
	next.Activity = act
	if next.FirstSignalAt.IsZero() {
		next.FirstSignalAt = timeOr(s.Timestamp, now)
	}
	if s.State == domain.ActivityExited {
		// The agent process can exit while the managed tmux session remains
		// alive and inspectable. Do not infer session termination from this
		// hook; a runtime observation or explicit lifecycle action owns that
		// fact. No tool/permission correlation survives an agent process exit.
		delete(m.flights, id)
	}
	next.UpdatedAt = now
	applied, err := m.store.UpdateSessionFromActivitySignal(ctx, next)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if !applied {
		m.mu.Unlock()
		return nil
	}
	// Transition into the needs-input family (waiting_input or blocked) pings
	// the user; an in-family escalation (waiting_input -> blocked) does not
	// re-notify — the user was already pinged once for this pause.
	if !rec.Activity.State.NeedsInput() && next.Activity.State.NeedsInput() && !next.IsTerminated {
		intent = &ports.NotificationIntent{
			Type:               domain.NotificationNeedsInput,
			SessionID:          next.ID,
			ProjectID:          next.ProjectID,
			CreatedAt:          next.Activity.LastActivityAt,
			SessionDisplayName: next.DisplayName,
		}
	}
	// Leaving the needs-input family is the user answering: the notification
	// that pinged them has nothing left to resolve.
	resolutions := needsInputResolutions(rec, next, now)
	waitingEvents := m.waitingInputEvents(next, prevState, prevAt, now)
	m.mu.Unlock()
	if err := m.acknowledgeAgentSwitchTarget(ctx, id, s, now); err != nil {
		return err
	}
	for _, ev := range waitingEvents {
		m.emitTelemetry(ctx, ev)
	}
	m.emitNotification(ctx, intent)
	m.resolveNotifications(ctx, resolutions...)
	return nil
}

// stagePendingAgentSwitchNativeMetadata persists provider-assigned startup
// identity while a target hook is waiting behind PrepareLaunch. It deliberately
// updates only the switch's retained native-session row, never the source-owned
// session row. Once ownership transfers, ReleaseLaunch lets the same hook apply
// its normal activity/metadata update. If the daemon crashes first, recovery
// can still prove the target conversation is resumable.
func (m *Manager) stagePendingAgentSwitchNativeMetadata(ctx context.Context, id domain.SessionID, s ports.ActivitySignal) error {
	if s.AgentSessionID == "" && s.TranscriptPath == "" {
		return nil
	}
	store, ok := m.store.(ports.AgentSwitchStore)
	if !ok {
		return nil
	}
	sw, found, err := store.GetActiveAgentSwitch(ctx, id)
	if err != nil {
		return err
	}
	if !found || sw.State != domain.AgentSwitchStartingTarget || string(sw.TargetGenerationID) != s.LaunchID || sw.TargetNativeSessionRef == nil {
		return nil
	}
	native, found, err := store.GetAgentNativeSession(ctx, *sw.TargetNativeSessionRef)
	if err != nil {
		return err
	}
	if !found || native.AOSessionID != id || native.Harness != sw.TargetHarness || native.LastGenerationID != sw.TargetGenerationID {
		return nil
	}
	changed := false
	if s.AgentSessionID != "" && native.NativeSessionID != s.AgentSessionID {
		if native.NativeSessionID != "" {
			return fmt.Errorf("lifecycle: target native session identity changed from %q to %q", native.NativeSessionID, s.AgentSessionID)
		}
		native.NativeSessionID = s.AgentSessionID
		changed = true
	}
	if s.TranscriptPath != "" && native.TranscriptPath != s.TranscriptPath {
		native.TranscriptPath = s.TranscriptPath
		changed = true
	}
	if !changed {
		return nil
	}
	updated, err := store.UpdateAgentNativeSession(ctx, native, sw.TargetGenerationID)
	if err != nil {
		return err
	}
	if !updated {
		return errors.New("lifecycle: target native session metadata changed concurrently")
	}
	return nil
}

func (m *Manager) acknowledgeAgentSwitchTarget(ctx context.Context, id domain.SessionID, signal ports.ActivitySignal, at time.Time) error {
	if !signal.Valid || signal.State != domain.ActivityActive || signal.Event != "user-prompt-submit" || signal.LaunchID == "" {
		return nil
	}
	store, ok := m.store.(ports.AgentSwitchStore)
	if !ok {
		return nil
	}
	sw, found, err := store.GetActiveAgentSwitch(ctx, id)
	if err != nil {
		return fmt.Errorf("lifecycle: read active agent switch acknowledgement for %s: %w", id, err)
	}
	if !found || sw.State != domain.AgentSwitchDelivering {
		return nil
	}
	changed, ackErr := store.AcknowledgeAgentSwitchTarget(ctx, sw.ID, id, domain.AgentGenerationID(signal.LaunchID), at)
	if changed && ackErr == nil {
		return nil
	}
	current, found, readErr := store.GetAgentSwitch(ctx, sw.ID)
	if readErr != nil {
		return ownership.Own(fmt.Errorf("lifecycle: read back agent switch %s acknowledgement: %w", sw.ID, readErr), ownership.OwnerAgentSwitchSaga)
	}
	if !found || current.State.Terminal() || current.State != domain.AgentSwitchDelivering ||
		current.TargetGenerationID != domain.AgentGenerationID(signal.LaunchID) || current.TargetAcknowledgedAt != nil {
		return nil
	}
	if ackErr != nil {
		return ownership.Own(fmt.Errorf("lifecycle: acknowledge agent switch %s target: %w", sw.ID, ackErr), ownership.OwnerAgentSwitchSaga)
	}
	if changed {
		return ownership.Own(fmt.Errorf("lifecycle: acknowledge agent switch %s target: commit was not observable", sw.ID), ownership.OwnerAgentSwitchSaga)
	}
	return ownership.Own(fmt.Errorf("lifecycle: acknowledge agent switch %s target: changed=false with unchanged durable predicate", sw.ID), ownership.OwnerAgentSwitchSaga)
}

// toolFlight tracks one session's in-flight tool executions and the pending
// permission dialog's identity, so a sticky `blocked` is cleared by the post
// of the exact approved tool — and by nothing else tool-shaped. Answering a
// permission dialog fires no hook of its own, so the approved tool's
// post-tool-use is the earliest observable "the decision was resolved"
// signal; but tool hooks also fire for parallel subagents on the same
// session, whose traffic must never clear a dialog that is still on screen.
// In-memory only: a daemon restart loses it and the session degrades to
// clearing at the next turn boundary — fail-safe staleness, never a spurious
// clear.
type toolFlight struct {
	// inflight maps toolUseID -> toolName for pre-tool-use signals whose post
	// has not arrived yet.
	inflight map[string]string
	// blockedCandidate is the tool-use id of the UNIQUE in-flight tool bearing
	// the dialog's tool_name when it appeared — the tool whose post proves the
	// dialog was answered. Empty when no in-flight tool matched, or when the
	// match was ambiguous (two same-name tools in flight: the permission
	// payload carries no tool_use_id to disambiguate, so a sibling's post must
	// NOT be mistaken for the approval). Either way, empty means nothing
	// tool-shaped may clear the block and it lifts only at a turn boundary.
	blockedCandidate string
	// cursorPending counts native Cursor permission dialogs by execution family
	// and bounded tool name. Cursor does not provide tool-use ids, so a matching
	// after-execution event can clear only its own key, and the session remains
	// blocked until every observed dialog has completed.
	cursorPending map[string]int
}

// maxInflightTools caps a session's in-flight map so lost posts cannot grow
// it without bound; hitting the cap resets the map, degrading that session to
// turn-boundary clearing (fail-safe).
const maxInflightTools = 128

// isToolUseEvent reports whether the AO hook event is one of the tool-use
// trio whose signals must not demote a sticky state on their own.
func isToolUseEvent(event string) bool {
	return event == "pre-tool-use" || isPostToolUseEvent(event)
}

func isPostToolUseEvent(event string) bool {
	// post-tool-use-fail is retained for Kimchi hook files installed before the
	// adapter switched to AO's canonical failure event name.
	return event == "post-tool-use" || event == "post-tool-use-failure" || event == "post-tool-use-fail"
}

func cursorBeforeExecutionKey(s ports.ActivitySignal) (string, bool) {
	if s.ToolName == "" {
		return "", false
	}
	switch s.Event {
	case "before-shell-execution":
		return "shell\x00" + s.ToolName, true
	case "before-mcp-execution":
		return "mcp\x00" + s.ToolName, true
	default:
		return "", false
	}
}

func cursorResolvedExecutionKey(s ports.ActivitySignal) (string, bool) {
	if s.ToolName == "" {
		return "", false
	}
	switch s.Event {
	case "after-shell-execution", "cursor-shell-terminal-failure":
		return "shell\x00" + s.ToolName, true
	case "after-mcp-execution", "cursor-mcp-terminal-failure":
		return "mcp\x00" + s.ToolName, true
	default:
		return "", false
	}
}

// isTurnBoundaryEvent reports the events that reliably mean the pending
// dialog is gone: a prompt cannot be submitted while a dialog holds the
// composer, and a turn cannot end (or the session exit) with one on screen.
func isTurnBoundaryEvent(event string) bool {
	return event == "user-prompt-submit" || event == "stop" || event == "session-end" ||
		event == "process-exited" || event == "chat.controller.stopped"
}

// applyToolPrecedenceLocked folds an event-tagged activity signal through the
// session's tool-flight state and decides whether its state write may
// proceed. Returned signal with Valid=false means "suppressed": the tracking
// side effects have landed but the state must not change. Signals without an
// Event pass through untouched — the compatibility contract for old CLIs and
// for adapters that don't tag their signals (their last-writer-wins semantics
// are pinned by tests). Caller must hold m.mu.
func (m *Manager) applyToolPrecedenceLocked(id domain.SessionID, cur domain.ActivityState, s ports.ActivitySignal) ports.ActivitySignal {
	if s.Event == "" {
		return s
	}
	suppressed := s
	suppressed.Valid = false

	fl := m.flights[id]
	ensure := func() *toolFlight {
		if fl == nil {
			fl = &toolFlight{inflight: map[string]string{}}
			m.flights[id] = fl
		}
		return fl
	}

	// Tracking side effects happen regardless of what the state decision is.
	switch s.Event {
	case "pre-tool-use":
		if s.ToolUseID != "" {
			f := ensure()
			if len(f.inflight) >= maxInflightTools {
				f.inflight = map[string]string{}
			}
			f.inflight[s.ToolUseID] = s.ToolName
		}
	case "post-tool-use", "post-tool-use-failure", "post-tool-use-fail":
		if fl != nil {
			delete(fl.inflight, s.ToolUseID)
		}
	}

	switch {
	case s.State == domain.ActivityBlocked:
		if key, ok := cursorBeforeExecutionKey(s); ok {
			f := ensure()
			f.blockedCandidate = ""
			if f.cursorPending == nil {
				f.cursorPending = map[string]int{}
			}
			f.cursorPending[key]++
			return s
		}
		// Entering (or re-asserting) blocked: snapshot the dialog's identity.
		// permission-request carries the blocking tool_name; the Notification
		// duplicate does not and must not wipe an existing snapshot.
		//
		// The permission hook payload does not carry the blocking tool's
		// tool_use_id (only its name), so we can only identify the blocking
		// tool unambiguously when EXACTLY ONE in-flight tool bears that name.
		// With two same-name tools in flight (a batch of Bash calls, one of
		// them the one at the dialog), a sibling's post could otherwise clear
		// a still-open dialog — the exact permission-bypass this change exists
		// to prevent. So we correlate only in the unique case; otherwise no
		// candidate is recorded and the block clears only at a turn boundary
		// (fail-closed).
		f := ensure()
		f.cursorPending = nil
		// Recompute only when this signal identifies a dialog. Claude can emit an
		// identity-less Notification duplicate after permission-request; that
		// duplicate must not erase the candidate captured by the first signal.
		if s.ToolUseID != "" || s.ToolName != "" {
			f.blockedCandidate = ""
		}
		if s.ToolUseID != "" {
			// If the blocking signal carries a tool_use_id that is in the
			// inflight map, use it directly — this is more precise than a
			// name match and handles adapters whose notification payloads
			// use a different tool_name casing than their PreToolUse/PostToolUse
			// payloads (e.g. Kimchi: "bash" in notification vs "Bash" in hooks).
			if _, ok := f.inflight[s.ToolUseID]; ok {
				f.blockedCandidate = s.ToolUseID
			}
		}
		if f.blockedCandidate == "" && s.ToolName != "" {
			for useID, name := range f.inflight {
				if name != s.ToolName {
					continue
				}
				if f.blockedCandidate != "" {
					// A second same-name tool: ambiguous, fail closed by
					// leaving no candidate (only a turn boundary clears).
					f.blockedCandidate = ""
					break
				}
				f.blockedCandidate = useID
			}
		}
		return s

	case cur == domain.ActivityBlocked:
		// Paused on a decision: only a turn boundary or the correlated post
		// may change the state.
		switch {
		case isTurnBoundaryEvent(s.Event):
			delete(m.flights, id)
			return s
		default:
			if fl != nil {
				if key, ok := cursorResolvedExecutionKey(s); ok && fl.cursorPending[key] > 0 {
					if fl.cursorPending[key] == 1 {
						delete(fl.cursorPending, key)
					} else {
						fl.cursorPending[key]--
					}
					if len(fl.cursorPending) == 0 {
						delete(m.flights, id)
						return s
					}
					return suppressed
				}
				if isPostToolUseEvent(s.Event) && fl.blockedCandidate != "" && s.ToolUseID == fl.blockedCandidate {
					// The single unambiguous blocking tool finished: the dialog was
					// answered. Clear the candidate so a later dialog in the same turn
					// starts from a clean slate.
					fl.blockedCandidate = ""
					return s
				}
			}
			// Subagent/sibling tool traffic (including a same-name sibling when
			// the block was ambiguous), notification sub-types (idle_prompt,
			// agent_completed), and anything else that is not proof the dialog
			// closed.
			return suppressed
		}

	case cur.IsSticky() && isToolUseEvent(s.Event):
		// waiting_input: background tool traffic must not clear the "waiting
		// on the user" marker; only an explicit user/turn signal does.
		return suppressed

	default:
		if isTurnBoundaryEvent(s.Event) {
			delete(m.flights, id)
		}
		return s
	}
}

func (m *Manager) waitingInputEvents(next domain.SessionRecord, prevState domain.ActivityState, prevAt, now time.Time) []ports.TelemetryEvent {
	if m.telemetry == nil {
		return nil
	}
	projectID := next.ProjectID
	sessionID := next.ID
	var events []ports.TelemetryEvent
	// Entry/exit is measured on the needs-input family boundary (waiting_input
	// or blocked): the event names stay waiting_input_* for dashboard
	// continuity, the payload state distinguishes the two, and an in-family
	// transition emits neither event so dwell covers the whole pause.
	if !prevState.NeedsInput() && next.Activity.State.NeedsInput() && !next.IsTerminated {
		events = append(events, ports.TelemetryEvent{
			Name:       "ao.session.waiting_input_entered",
			Source:     "lifecycle",
			OccurredAt: now.UTC(),
			Level:      ports.TelemetryLevelInfo,
			ProjectID:  &projectID,
			SessionID:  &sessionID,
			Payload: map[string]any{
				"state": string(next.Activity.State),
			},
		})
	}
	if prevState.NeedsInput() && !next.Activity.State.NeedsInput() {
		payload := map[string]any{
			"state":     string(next.Activity.State),
			"dwell_ms":  now.Sub(prevAt).Milliseconds(),
			"exited_to": string(next.Activity.State),
		}
		events = append(events, ports.TelemetryEvent{
			Name:       "ao.session.waiting_input_exited",
			Source:     "lifecycle",
			OccurredAt: now.UTC(),
			Level:      ports.TelemetryLevelInfo,
			ProjectID:  &projectID,
			SessionID:  &sessionID,
			Payload:    payload,
		})
	}
	return events
}

func (m *Manager) emitTelemetry(ctx context.Context, ev ports.TelemetryEvent) {
	if m.telemetry == nil {
		return
	}
	if ev.RequestID == "" {
		ev.RequestID = reqid.FromContext(ctx)
	}
	m.telemetry.Emit(ctx, ev)
}

func (m *Manager) emitNotification(ctx context.Context, intent *ports.NotificationIntent) {
	if intent == nil || m.notifications == nil {
		return
	}
	if err := m.notifications.Notify(ctx, *intent); err != nil {
		slog.Default().Warn("lifecycle: notification failed", "session", intent.SessionID, "type", intent.Type, "err", err)
	}
}

// resolveNotifications closes notifications the just-written facts made stale.
// Best-effort like emitNotification: a failed resolve must never fail the
// lifecycle write that produced it.
func (m *Manager) resolveNotifications(ctx context.Context, resolutions ...ports.NotificationResolution) {
	if m.notifications == nil {
		return
	}
	for _, res := range resolutions {
		if err := m.notifications.Resolve(ctx, res); err != nil {
			slog.Default().Warn(
				"lifecycle: notification resolve failed",
				"session", res.SessionID, "pr", res.PRURL, "type", res.Type, "err", err,
			)
		}
	}
}

// MarkSpawned marks a newly spawned or restored session live and stores runtime/workspace handles.
func (m *Manager) MarkSpawned(ctx context.Context, id domain.SessionID, metadata domain.SessionMetadata) error {
	return m.markSpawned(ctx, id, metadata, nil, nil)
}

// MarkChatSpawned atomically marks a Chat controller live and publishes the
// fresh provider boundary reserved before the provider process connected.
func (m *Manager) MarkChatSpawned(
	ctx context.Context,
	id domain.SessionID,
	metadata domain.SessionMetadata,
	boundary domain.ConversationBranch,
) error {
	if boundary.ID == "" || boundary.ConversationID == "" || boundary.SessionID != id ||
		boundary.ProviderConversationID == "" || boundary.ProviderScopeID != boundary.ID ||
		metadata.ProviderConversationID != boundary.ProviderConversationID ||
		strings.TrimSpace(metadata.ControllerGeneration) == "" {
		return fmt.Errorf("lifecycle: Chat provider boundary for %q has incomplete or mismatched ownership", id)
	}
	return m.markSpawned(ctx, id, metadata, &boundary, nil)
}

// MarkChatSpawnedPrepared publishes native history together with its reserved
// provider boundary and lifecycle owner. The preparation callback is executed
// only by the storage transaction; no controller is registered and the session
// remains externally unavailable until the complete unit commits.
func (m *Manager) MarkChatSpawnedPrepared(
	ctx context.Context,
	id domain.SessionID,
	metadata domain.SessionMetadata,
	boundary domain.ConversationBranch,
	prepare func(context.Context) error,
) error {
	if prepare == nil {
		return errors.New("lifecycle: Chat provider-history preparation is missing")
	}
	if boundary.ID == "" || boundary.ConversationID == "" || boundary.SessionID != id ||
		boundary.ProviderConversationID == "" || boundary.ProviderScopeID != boundary.ID ||
		metadata.ProviderConversationID != boundary.ProviderConversationID ||
		strings.TrimSpace(metadata.ControllerGeneration) == "" {
		return fmt.Errorf("lifecycle: Chat provider boundary for %q has incomplete or mismatched ownership", id)
	}
	return m.markSpawned(ctx, id, metadata, &boundary, prepare)
}

func (m *Manager) markSpawned(
	ctx context.Context,
	id domain.SessionID,
	metadata domain.SessionMetadata,
	boundary *domain.ConversationBranch,
	prepare func(context.Context) error,
) error {
	launchID := strings.TrimSpace(metadata.RuntimeLaunchID)
	reactivator, err := func() (sessionUsageReactivator, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		defer m.finishLaunchLocked(id, launchID)
		rec, ok, err := m.store.GetSession(ctx, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("lifecycle: MarkSpawned for unknown session %q", id)
		}
		now := m.clock()
		rec.IsTerminated = false
		rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
		// Each spawn/restore must re-prove its hook pipeline: clear the receipt so
		// a relaunch with broken hooks degrades to no_signal instead of inheriting
		// a stale "signals worked once" fact.
		rec.FirstSignalAt = time.Time{}
		rec.Metadata = mergeMetadata(rec.Metadata, metadata)
		if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat &&
			strings.TrimSpace(metadata.ControllerGeneration) != "" {
			// A committed Chat controller is the sole session owner. Clear any
			// terminal identity retained by an older handoff/recovery build so it
			// cannot confuse runtime observation or a later daemon restart.
			rec.Metadata.RuntimeHandleID = ""
			rec.Metadata.RuntimeLaunchID = ""
		}
		rec.UpdatedAt = now
		if boundary == nil {
			if err := m.store.UpdateSession(ctx, rec); err != nil {
				return nil, err
			}
		} else if prepare == nil {
			writer, ok := m.store.(chatSpawnStore)
			if !ok {
				return nil, errors.New("lifecycle: atomic Chat spawn persistence is unavailable")
			}
			if err := writer.CommitChatSpawn(ctx, rec, *boundary); err != nil {
				return nil, err
			}
		} else {
			writer, ok := m.store.(preparedChatSpawnStore)
			if !ok {
				return nil, errors.New("lifecycle: atomic Chat provider-history persistence is unavailable")
			}
			if err := writer.CommitChatSpawnPrepared(ctx, rec, *boundary, prepare); err != nil {
				return nil, err
			}
		}
		return m.usageReactivator, nil
	}()
	if err != nil {
		return err
	}
	reactivateSessionUsage(ctx, id, launchID, reactivator)
	return nil
}

// CommitControllerEpoch atomically changes which controller owns a live
// session. Session Manager coordinates the external-process saga, but only
// Lifecycle Manager is allowed to write the durable controller/activity facts.
// A false result means the expected source controller no longer owns the row.
// startFresh is accepted only with an empty native id; Session Manager sets it
// after an adapter proved the reserved id has no persisted conversation.
func (m *Manager) CommitControllerEpoch(
	ctx context.Context,
	id domain.SessionID,
	source, target domain.SessionMode,
	nativeConversationID string,
	startFresh bool,
) (bool, error) {
	if !source.Valid() || !target.Valid() || source == target {
		return false, fmt.Errorf("lifecycle: invalid controller epoch %q -> %q", source, target)
	}
	nativeConversationID = strings.TrimSpace(nativeConversationID)
	if nativeConversationID == "" && !startFresh {
		return false, fmt.Errorf("lifecycle: controller epoch for %q has no native conversation id", id)
	}
	if nativeConversationID != "" && startFresh {
		return false, fmt.Errorf("lifecycle: fresh controller epoch for %q also supplied a native conversation id", id)
	}
	writer, ok := m.store.(controllerEpochStore)
	if !ok {
		return false, fmt.Errorf("lifecycle: controller epoch persistence is unavailable")
	}

	m.mu.Lock()
	previous, found, err := m.store.GetSession(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return false, err
	}
	if !found {
		m.mu.Unlock()
		return false, fmt.Errorf("%w: %s", ports.ErrSessionNotFound, id)
	}
	if previous.IsTerminated || domain.NormalizeSessionMode(previous.Mode) != source {
		m.mu.Unlock()
		return false, nil
	}
	now := m.clock()
	changed, err := writer.CommitSessionControllerEpoch(
		ctx, id, source, target, nativeConversationID, now,
	)
	if err != nil || !changed {
		m.mu.Unlock()
		return changed, err
	}

	// Mirror the atomic store write for lifecycle side effects. MarkSpawned will
	// clear FirstSignalAt and attach the target's process generation once the new
	// controller is actually live.
	next := previous
	next.Mode = target
	next.Metadata.RuntimeHandleID = ""
	next.Metadata.RuntimeLaunchID = ""
	next.Metadata.AgentSessionID = nativeConversationID
	next.Metadata.AgentSessionIDLaunchID = ""
	next.Metadata.ProviderConversationID = nativeConversationID
	next.Metadata.ControllerGeneration = ""
	next.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
	next.UpdatedAt = now
	delete(m.flights, id)
	resolutions := needsInputResolutions(previous, next, now)
	waitingEvents := m.waitingInputEvents(
		next, previous.Activity.State, previous.Activity.LastActivityAt, now,
	)
	m.mu.Unlock()

	for _, ev := range waitingEvents {
		m.emitTelemetry(ctx, ev)
	}
	m.resolveNotifications(ctx, resolutions...)
	return true, nil
}

// ConfirmAgentSwitchSourceStopped records that the source process is gone and
// moves the switch saga across the source-stop boundary in the same store
// transaction. Session Manager coordinates the process; Lifecycle Manager owns
// the durable activity-state write.
func (m *Manager) ConfirmAgentSwitchSourceStopped(
	ctx context.Context,
	confirmation domain.AgentSwitchSourceStopConfirmation,
) (bool, error) {
	writer, ok := m.store.(agentSwitchSourceStopStore)
	if !ok {
		return false, fmt.Errorf("lifecycle: agent-switch source-stop persistence is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return writer.ConfirmAgentSwitchSourceStopped(ctx, confirmation)
}

// ActivateAgentSwitchTarget atomically transfers the session owner to the
// target process and advances the switch saga. Keeping this command on
// Lifecycle Manager preserves the canonical write boundary without splitting
// the store's all-or-nothing transaction.
func (m *Manager) ActivateAgentSwitchTarget(
	ctx context.Context,
	activation domain.AgentSwitchTargetActivation,
) (bool, error) {
	writer, ok := m.store.(agentSwitchTargetActivationStore)
	if !ok {
		return false, fmt.Errorf("lifecycle: agent-switch target activation persistence is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return writer.ActivateAgentSwitchTarget(ctx, activation)
}

// ActivateChatAgentSwitchTarget atomically transfers a stopped Chat session
// from the fenced source generation to the structured target controller.
func (m *Manager) ActivateChatAgentSwitchTarget(
	ctx context.Context,
	activation domain.AgentSwitchChatTargetActivation,
) (bool, error) {
	writer, ok := m.store.(agentSwitchChatTargetActivationStore)
	if !ok {
		return false, fmt.Errorf("lifecycle: Chat agent-switch target activation persistence is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return writer.ActivateChatAgentSwitchTarget(ctx, activation)
}

// MarkTerminated marks a session terminated. Runtime/workspace teardown is the
// caller's responsibility (see session_manager.Manager.Kill); this also reaps the
// session's Docker containers via the optional ContainerReaper (#2652) as its one
// built-in external side effect.
func (m *Manager) MarkTerminated(ctx context.Context, id domain.SessionID) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rec, ok, err := m.store.GetSession(ctx, id)
		if err != nil || !ok {
			return err
		}
		if rec.IsTerminated {
			m.reapSessionContainers(ctx, id)
			return nil
		}

		launchID := rec.Metadata.RuntimeLaunchID
		sessionRevision := rec.UpdatedAt
		m.mu.Lock()
		finalizer := m.usageFinalizer
		m.mu.Unlock()
		finalizeSessionUsage(ctx, id, launchID, sessionRevision, finalizer)

		const (
			terminationChanged = iota
			terminationApplied
			terminationAlreadyApplied
			terminationLaunchChanged
		)
		outcome := terminationChanged
		err = m.mutate(ctx, id, func(cur domain.SessionRecord, now time.Time) (domain.SessionRecord, bool) {
			switch {
			case cur.IsTerminated:
				outcome = terminationAlreadyApplied
				return cur, false
			case cur.Metadata.RuntimeLaunchID != launchID:
				outcome = terminationLaunchChanged
				return cur, false
			case !cur.UpdatedAt.Equal(sessionRevision):
				return cur, false
			default:
				cur.IsTerminated = true
				cur.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: now}
				delete(m.flights, id) // runs under m.mu (mutate holds it)
				outcome = terminationApplied
				return cur, true
			}
		})
		if err != nil {
			return err
		}
		switch outcome {
		case terminationApplied, terminationAlreadyApplied:
			m.reapSessionContainers(ctx, id)
			return nil
		case terminationLaunchChanged:
			return fmt.Errorf("lifecycle: runtime launch changed while terminating session %q", id)
		default:
			// A same-launch activity transition changed UpdatedAt after usage was
			// finalized. Retry from a fresh snapshot so termination and usage
			// finalization commit against the same durable revision.
			continue
		}
	}
}

// reapSessionContainers is the container leg of #2652 (the container-owning
// counterpart to session_manager.Manager's cleanupAgentWorkspace): every
// MarkTerminated call - Kill, daemon-shutdown teardown, Cleanup,
// RetireForReplacement, and tracker-driven termination - funnels through
// here, so this single hook covers every terminal-state path rather than
// only explicit ao session kill. Best-effort: logged on failure, never
// returned, matching the rest of AO's terminal-state teardown. A project-load
// error skips reaping rather than guessing - the package's stated bias is to
// spare on ambiguity, not to reap on it.
func (m *Manager) reapSessionContainers(ctx context.Context, id domain.SessionID) {
	if m.containers == nil {
		return
	}
	if m.projects != nil {
		rec, ok, err := m.store.GetSession(ctx, id)
		if err != nil || !ok {
			slog.Default().Warn("lifecycle: container reap: session lookup failed, skipping", "session", id, "err", err)
			return
		}
		project, ok, err := m.projects.GetProject(ctx, string(rec.ProjectID))
		if err != nil || !ok {
			slog.Default().Warn("lifecycle: container reap: project lookup failed or missing, skipping rather than guessing", "session", id, "project", rec.ProjectID, "err", err)
			return
		}
		if project.Config.ContainerReap.Disabled {
			return
		}
	}
	removed, err := m.containers.ReapSessionContainers(ctx, id)
	if err != nil {
		slog.Default().Warn("lifecycle: container reap failed", "session", id, "err", err)
		return
	}
	if removed > 0 {
		slog.Default().Info("lifecycle: reaped session containers", "session", id, "removed", removed)
	}
}

func finalizeSessionUsage(
	ctx context.Context,
	id domain.SessionID,
	expectedRuntimeLaunchID string,
	expectedSessionRevision time.Time,
	finalizer sessionUsageFinalizer,
) {
	if finalizer == nil {
		return
	}
	if err := finalizer.FinalizeSession(ctx, id, expectedRuntimeLaunchID, expectedSessionRevision); err != nil {
		slog.Default().Warn("lifecycle: finalize session usage before termination", "session", id, "err", err)
	}
}

func reactivateSessionUsage(
	ctx context.Context,
	id domain.SessionID,
	expectedRuntimeLaunchID string,
	reactivator sessionUsageReactivator,
) {
	if reactivator == nil {
		return
	}
	if err := reactivator.ReactivateSession(ctx, id, expectedRuntimeLaunchID); err != nil {
		slog.Default().Warn("lifecycle: reactivate session usage after launch", "session", id, "err", err)
	}
}

// sameActivity reports whether two activity signals describe the same state.
// LastActivityAt is intentionally ignored: same-state repeats (e.g. a stream
// of idle notifications) must not rewrite UpdatedAt or fan out a CDC event.
// LastActivityAt now marks when this state was first entered since the last
// transition, which is the timestamp a UI actually wants.
func sameActivity(a, b domain.Activity) bool {
	return a.State == b.State
}

func mergeMetadata(base, in domain.SessionMetadata) domain.SessionMetadata {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&base.Branch, in.Branch)
	set(&base.WorkspacePath, in.WorkspacePath)
	set(&base.WorkspaceRepoPath, in.WorkspaceRepoPath)
	set(&base.DiffBaseSHA, in.DiffBaseSHA)
	set(&base.DiffBaseRef, in.DiffBaseRef)
	set(&base.RuntimeHandleID, in.RuntimeHandleID)
	base.RuntimeLaunchID = in.RuntimeLaunchID
	set(&base.AgentSessionID, in.AgentSessionID)
	set(&base.AgentSessionIDLaunchID, in.AgentSessionIDLaunchID)
	set(&base.Prompt, in.Prompt)
	set(&base.LatestUserPrompt, in.LatestUserPrompt)
	if !in.LatestUserPromptAt.IsZero() {
		base.LatestUserPromptAt = in.LatestUserPromptAt
	}
	set(&base.LatestAssistantUpdate, in.LatestAssistantUpdate)
	set(&base.NativeTranscriptPath, in.NativeTranscriptPath)
	set(&base.Model, in.Model)
	set(&base.BrowserCapabilityVerifier, in.BrowserCapabilityVerifier)
	// The chat controller's resume handle. Without this a restart has no thread to
	// resume and the conversation is stranded — the provider still holds it, but
	// AO no longer knows its id.
	set(&base.ProviderConversationID, in.ProviderConversationID)
	// Assigned rather than set: a relaunch rotates the generation, and the whole
	// point is that the new value replaces the old one so events from the
	// controller this one superseded can be told apart.
	base.ControllerGeneration = in.ControllerGeneration
	return base
}

func applyActivityMetadata(meta *domain.SessionMetadata, signal ports.ActivitySignal, receivedAt time.Time) {
	if signal.AgentSessionID != "" {
		meta.AgentSessionID = signal.AgentSessionID
		meta.AgentSessionIDLaunchID = signal.LaunchID
	}
	if signal.LatestUserPrompt != "" {
		meta.LatestUserPrompt = signal.LatestUserPrompt
		meta.LatestUserPromptAt = timeOr(signal.Timestamp, receivedAt)
	}
	if signal.LatestAssistantUpdate != "" {
		meta.LatestAssistantUpdate = signal.LatestAssistantUpdate
	}
	if signal.TranscriptPath != "" {
		meta.NativeTranscriptPath = signal.TranscriptPath
	}
}
