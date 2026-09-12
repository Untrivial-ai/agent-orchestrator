package lifecycle

import (
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// sig builds an event-tagged activity signal the way the ingress does.
func sig(state domain.ActivityState, event, toolName, toolUseID string) ports.ActivitySignal {
	return ports.ActivitySignal{Valid: true, State: state, Event: event, ToolName: toolName, ToolUseID: toolUseID}
}

// seedSignaled seeds a session that already produced signals (FirstSignalAt
// stamped), so receipt stamping does not interfere with precedence assertions.
func seedSignaled(st *fakeStore, id domain.SessionID, state domain.ActivityState) {
	st.sessions[id] = domain.SessionRecord{
		ID: id, ProjectID: "mer",
		Activity:      domain.Activity{State: state, LastActivityAt: time.Now()},
		FirstSignalAt: time.Now(),
	}
}

// mustApply applies a signal and fails the test on error.
func mustApply(t *testing.T, m *Manager, id domain.SessionID, s ports.ActivitySignal) {
	t.Helper()
	if err := m.ApplyActivitySignal(ctx, id, s); err != nil {
		t.Fatal(err)
	}
}

func stateOf(st *fakeStore, id domain.SessionID) domain.ActivityState {
	return st.sessions[id].Activity.State
}

// blockOnDialog drives a session into blocked through the real signal path:
// the blocking tool's pre-tool-use, then permission-request naming that tool.
func blockOnDialog(t *testing.T, m *Manager, st *fakeStore, id domain.SessionID, toolName, toolUseID string) {
	t.Helper()
	mustApply(t, m, id, sig(domain.ActivityActive, "pre-tool-use", toolName, toolUseID))
	mustApply(t, m, id, sig(domain.ActivityBlocked, "permission-request", toolName, ""))
	if got := stateOf(st, id); got != domain.ActivityBlocked {
		t.Fatalf("setup: state = %q, want blocked", got)
	}
}

// blockOnNotificationCorrelation drives a session into blocked through the
// notification correlation path: the blocking tool's pre-tool-use, then a
// notification(permission_prompt) whose tool_use_id matches the inflight entry.
// The notification's tool_name is lowercased to mirror adapters (e.g. Kimchi)
// whose notification payloads carry the internal tool name while hooks carry
// the external capitalized name.
func blockOnNotificationCorrelation(t *testing.T, m *Manager, st *fakeStore, id domain.SessionID, toolName, toolUseID string) {
	t.Helper()
	mustApply(t, m, id, sig(domain.ActivityActive, "pre-tool-use", toolName, toolUseID))
	mustApply(t, m, id, ports.ActivitySignal{
		Valid: true, State: domain.ActivityBlocked, Event: "notification",
		ToolName: strings.ToLower(toolName), ToolUseID: toolUseID,
	})
	if got := stateOf(st, id); got != domain.ActivityBlocked {
		t.Fatalf("setup: state = %q, want blocked", got)
	}
}

func TestToolPrecedence_ApprovedToolPostClearsBlocked(t *testing.T) {
	// Approving the dialog fires no hook; the approved tool's own post is the
	// earliest observable resolution signal and must clear blocked -> active.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnDialog(t, m, st, "mer-1", "Bash", "toolu_1")

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after approved tool's post = %q, want active", got)
	}
}

func TestToolPrecedence_ApprovedToolFailurePostAlsoClears(t *testing.T) {
	// An approved tool that runs and FAILS still resolved the dialog.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnDialog(t, m, st, "mer-1", "Bash", "toolu_1")

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use-failure", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after approved tool's failure post = %q, want active", got)
	}
}

func TestToolPrecedence_CursorMatchingAfterClearsBlocked(t *testing.T) {
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)

	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "before-shell-execution", "git status", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "after-shell-execution", "git status", ""))

	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after matching Cursor shell completion = %q, want active", got)
	}
}

func TestToolPrecedence_CursorUnrelatedAfterDoesNotClearBlocked(t *testing.T) {
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)

	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "before-shell-execution", "git push", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "after-mcp-execution", "search", ""))

	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after unrelated Cursor completion = %q, want blocked", got)
	}
}

func TestToolPrecedence_CursorConcurrentDialogsRemainBlockedUntilAllComplete(t *testing.T) {
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)

	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "before-shell-execution", "git push", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "before-mcp-execution", "deploy", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "after-shell-execution", "git push", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after only one Cursor dialog completed = %q, want blocked", got)
	}

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "after-mcp-execution", "deploy", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after all Cursor dialogs completed = %q, want active", got)
	}
}

func TestToolPrecedence_CursorTerminalFailureClearsOnlyMatchingPendingRequest(t *testing.T) {
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)

	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "before-shell-execution", "git push", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "before-mcp-execution", "deploy", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "cursor-shell-terminal-failure", "git push", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after one Cursor denial = %q, want blocked while MCP dialog remains", got)
	}

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "after-mcp-execution", "deploy", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after final Cursor dialog completed = %q, want active", got)
	}
}

func TestToolPrecedence_CursorSameKeyDialogsDecrementOneAtATime(t *testing.T) {
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)

	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "before-shell-execution", "git push", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "before-shell-execution", "git push", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "cursor-shell-terminal-failure", "git push", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after one same-key Cursor dialog completed = %q, want blocked", got)
	}

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "after-shell-execution", "git push", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after both same-key Cursor dialogs completed = %q, want active", got)
	}
}

func TestToolPrecedence_LegacyKimchiFailurePostAlsoClears(t *testing.T) {
	// Old Kimchi hook files used this non-canonical subcommand. Keep accepting
	// it so existing worktrees do not remain blocked after a failed tool call.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnDialog(t, m, st, "mer-1", "Bash", "toolu_1")

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use-fail", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after legacy failure post = %q, want active", got)
	}
}

func TestToolPrecedence_SubagentTrafficDoesNotClearBlocked(t *testing.T) {
	// The failure that reverted the naive mapping in PR #5's review: parallel
	// subagent tool signals (same session id) land while the dialog is still
	// on screen. They must be tracked but never change the state.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnDialog(t, m, st, "mer-1", "Bash", "toolu_1")

	// A different tool starts and finishes while the dialog is pending.
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "pre-tool-use", "Read", "toolu_sub"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after subagent pre = %q, want blocked", got)
	}
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Read", "toolu_sub"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after subagent post = %q, want blocked", got)
	}
	// The approved tool's post still clears afterwards.
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after approved post = %q, want active", got)
	}
}

func TestToolPrecedence_TurnBoundariesClearBlocked(t *testing.T) {
	// A prompt cannot be submitted and a turn cannot end while a dialog holds
	// the composer, so these events reliably mean the dialog is gone.
	cases := []struct {
		name string
		s    ports.ActivitySignal
		want domain.ActivityState
	}{
		{"user-prompt-submit", sig(domain.ActivityActive, "user-prompt-submit", "", ""), domain.ActivityActive},
		{"stop", sig(domain.ActivityIdle, "stop", "", ""), domain.ActivityIdle},
		{"session-end", sig(domain.ActivityExited, "session-end", "", ""), domain.ActivityExited},
		{"chat-controller-stopped", sig(domain.ActivityExited, "chat.controller.stopped", "", ""), domain.ActivityExited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, st, _ := newManager()
			seedSignaled(st, "mer-1", domain.ActivityActive)
			blockOnDialog(t, m, st, "mer-1", "Bash", "toolu_1")
			mustApply(t, m, "mer-1", tc.s)
			if got := stateOf(st, "mer-1"); got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToolPrecedence_NotificationSubtypesDoNotClearBlocked(t *testing.T) {
	// agent_completed (idle) and idle_prompt (waiting_input) arrive with
	// event "notification" and are no evidence the dialog closed — a
	// background `claude agents` run finishing must not unmask a live dialog
	// (the cycle-2 minor finding on PR #5).
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnDialog(t, m, st, "mer-1", "Bash", "toolu_1")

	mustApply(t, m, "mer-1", sig(domain.ActivityIdle, "notification", "", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after notification idle = %q, want blocked", got)
	}
	mustApply(t, m, "mer-1", sig(domain.ActivityWaitingInput, "notification", "", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after notification waiting_input = %q, want blocked", got)
	}
}

func TestToolPrecedence_IdentitylessBlockedDuplicatePreservesCandidate(t *testing.T) {
	// Claude can report the same permission dialog twice: first with its tool
	// identity, then as a bare Notification. The duplicate must not erase the
	// candidate needed to correlate the approved tool's post.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnDialog(t, m, st, "mer-1", "Bash", "toolu_1")

	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "notification", "", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after duplicate notification and approved post = %q, want active", got)
	}
}

func TestToolPrecedence_NoCandidatesFailsSafe(t *testing.T) {
	// A blocking signal that carries no tool identity (codex, a bare
	// Notification, or a daemon restarted mid-dialog) yields no candidates:
	// nothing tool-shaped may clear the block; the turn boundary still does.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	// Blocked lands with NO prior pre and NO tool name (restart-equivalent).
	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "permission-request", "", ""))

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after uncorrelated post = %q, want blocked (fail safe)", got)
	}
	mustApply(t, m, "mer-1", sig(domain.ActivityIdle, "stop", "", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityIdle {
		t.Fatalf("state after stop = %q, want idle", got)
	}
}

func TestToolPrecedence_LegacySignalsKeepLastWriterWins(t *testing.T) {
	// The compatibility pin: a signal WITHOUT an event (old CLIs, the 12
	// adapters that don't tag their signals) keeps today's last-writer-wins
	// semantics even out of blocked — the precedence rule must not change
	// behavior for anyone who doesn't opt in.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnDialog(t, m, st, "mer-1", "Bash", "toolu_1")

	mustApply(t, m, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive})
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("legacy active over blocked = %q, want active (last-writer-wins preserved)", got)
	}
}

func TestToolPrecedence_ToolEventsDoNotDemoteWaitingInput(t *testing.T) {
	// waiting_input marks "the user's turn". Background subagent tool traffic
	// must not clear it; an explicit user signal does.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityWaitingInput)

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "pre-tool-use", "Read", "toolu_bg"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityWaitingInput {
		t.Fatalf("state after background pre = %q, want waiting_input", got)
	}
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Read", "toolu_bg"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityWaitingInput {
		t.Fatalf("state after background post = %q, want waiting_input", got)
	}
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "user-prompt-submit", "", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after user prompt = %q, want active", got)
	}
}

func TestToolPrecedence_AmbiguousSameNameBlockFailsClosed(t *testing.T) {
	// Two same-name tools in flight when the dialog appears: the permission
	// payload has no tool_use_id to say WHICH one is at the dialog, so neither
	// sibling's post may clear the block — that would let a paste answer a
	// still-open dialog. Fail closed: the block lifts only at a turn boundary.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "pre-tool-use", "Bash", "toolu_1"))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "pre-tool-use", "Bash", "toolu_2"))
	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "permission-request", "Bash", ""))

	// Either sibling's post must NOT clear the live dialog.
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_2"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after ambiguous sibling post = %q, want blocked (fail closed)", got)
	}
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("state after second ambiguous post = %q, want blocked (fail closed)", got)
	}
	// A turn boundary still lifts it.
	mustApply(t, m, "mer-1", sig(domain.ActivityIdle, "stop", "", ""))
	if got := stateOf(st, "mer-1"); got != domain.ActivityIdle {
		t.Fatalf("state after stop = %q, want idle", got)
	}
}

func TestToolPrecedence_SequentialDialogsResetAmbiguity(t *testing.T) {
	// Two dialogs in one turn without a turn boundary between them (the block
	// re-asserts via a fresh permission-request). A prior ambiguous block must
	// not leak into a later unique one: the re-snapshot recomputes from
	// scratch, so the unique dialog's tool can still clear it.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	// Dialog 1: two same-name Bash tools -> ambiguous, cannot clear on a post.
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "pre-tool-use", "Bash", "toolu_1"))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "pre-tool-use", "Bash", "toolu_2"))
	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "permission-request", "Bash", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_1"))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_2"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("dialog 1 ambiguous: state = %q, want blocked", got)
	}
	// Both dialog-1 tools have posted, so their ids are drained from inflight.
	// Dialog 2: a single new Bash -> unique, must be clearable despite the
	// stale ambiguousBlock from dialog 1.
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "pre-tool-use", "Bash", "toolu_3"))
	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "permission-request", "Bash", ""))
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_3"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("dialog 2 unique: state = %q, want active (ambiguity must reset)", got)
	}
}

func TestToolPrecedence_UniqueSameNameToolClears(t *testing.T) {
	// The unambiguous case still works: exactly one in-flight Bash when the
	// dialog appears, so its post is a valid approval signal.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "pre-tool-use", "Bash", "toolu_1"))
	mustApply(t, m, "mer-1", sig(domain.ActivityBlocked, "permission-request", "Bash", ""))

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("state after unique tool's post = %q, want active", got)
	}
}

func TestToolPrecedence_ReaperTerminationReleasesFlight(t *testing.T) {
	// A crash/SIGKILL is reaped via ApplyRuntimeObservation, which fires no
	// session-end hook — so it is the last chance to release the session's
	// tool-flight state. A leaked entry would otherwise persist for the
	// daemon's life (later observations return early on cur.IsTerminated).
	m, st, _ := newManager()
	// A live session with an in-flight tool tracked in the flights map.
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer",
		Activity:      domain.Activity{State: domain.ActivityActive, LastActivityAt: time.Now().Add(-2 * time.Minute)},
		FirstSignalAt: time.Now(),
	}
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "pre-tool-use", "Bash", "toolu_1"))
	if _, ok := m.flights["mer-1"]; !ok {
		t.Fatal("setup: expected a flights entry after pre-tool-use")
	}
	if err := m.ApplyRuntimeObservation(ctx, "mer-1", ports.RuntimeFacts{Runtime: ports.ProbeDead, Workload: ports.ProbeFailed}); err != nil {
		t.Fatal(err)
	}
	if !st.sessions["mer-1"].IsTerminated {
		t.Fatal("reaper did not terminate the session")
	}
	if _, ok := m.flights["mer-1"]; ok {
		t.Fatal("flights entry leaked after reaper termination")
	}
}

func TestToolPrecedence_SuppressedSignalEmitsNoNotification(t *testing.T) {
	// A suppressed clear must not fan out: the session never left blocked, so
	// no needs-input exit/entry telemetry or notification may fire.
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnDialog(t, m, st, "mer-1", "Bash", "toolu_1")
	entered := len(sink.intents) // the blocked entry notification

	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Read", "toolu_sub"))
	if len(sink.intents) != entered {
		t.Fatalf("suppressed signal emitted a notification: %d -> %d", entered, len(sink.intents))
	}
}

func TestToolPrecedence_ToolUseIDMatchBridgesCasingMismatch(t *testing.T) {
	// Kimchi's PreToolUse/PostToolUse payloads capitalize tool_name ("Bash")
	// via externalToolName, but the permission_prompt notification carries the
	// raw internal name ("bash"). The name-based candidate lookup fails, but
	// the notification also carries tool_use_id — matching it directly against
	// the inflight map bridges the casing gap and lets the block clear on the
	// correlated post-tool-use.
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnNotificationCorrelation(t, m, st, "mer-1", "Bash", "toolu_1")

	// User accepts. post-tool-use: tool_name="Bash", tool_use_id="toolu_1".
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("after post-tool-use: state = %q, want active", got)
	}
}

// TestToolPrecedence_NotificationCorrelationFailurePostClearsBlocked is the
// moved Kimchi lifecycle integration test. Kimchi enters blocked via a
// notification(permission_prompt) whose tool_use_id matches an inflight
// pre-tool-use entry, bridging a tool_name casing mismatch. The approved
// tool may run and fail — a post-tool-use-failure signal must still clear
// the correlated blocked state, because the permission dialog was resolved
// regardless of the tool's exit status.
func TestToolPrecedence_NotificationCorrelationFailurePostClearsBlocked(t *testing.T) {
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnNotificationCorrelation(t, m, st, "mer-1", "Bash", "toolu_1")

	// post-tool-use-failure: the approved tool ran and failed — blocked must clear.
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use-failure", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("after post-tool-use-failure: state = %q, want active (blocked cleared)", got)
	}
}

// TestToolPrecedence_NotificationCorrelationSiblingDoesNotClear verifies that a
// different tool's post does not clear a blocked state entered via the
// notification correlation path — only the approved tool's post (or failure
// post) clears it.
func TestToolPrecedence_NotificationCorrelationSiblingDoesNotClear(t *testing.T) {
	m, st, _ := newManager()
	seedSignaled(st, "mer-1", domain.ActivityActive)
	blockOnNotificationCorrelation(t, m, st, "mer-1", "Bash", "toolu_1")

	// A sibling tool's post must NOT clear blocked.
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use", "Read", "toolu_sibling"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityBlocked {
		t.Fatalf("after sibling post: state = %q, want blocked (sibling must not clear)", got)
	}

	// The approved tool's failure post still clears afterwards.
	mustApply(t, m, "mer-1", sig(domain.ActivityActive, "post-tool-use-failure", "Bash", "toolu_1"))
	if got := stateOf(st, "mer-1"); got != domain.ActivityActive {
		t.Fatalf("after approved failure post: state = %q, want active", got)
	}
}
