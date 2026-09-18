// Package fake implements a deterministic, LLM-free agent harness. It exists so
// e2e tests can drive the FULL session lifecycle without a real CLI, a network
// round-trip, or any token spend. The timeline the script walks is:
//
//	spawning -> active -> waiting_input -> active -> blocked -> done (exited)
//
// with a canned PR-push event emitted during the active work phase. This is the
// event set issue #2483's fake-agent prerequisite (#2) calls for: spawning,
// active, waiting_input, blocked, done, and a PR push.
//
// The launch command is a small POSIX shell script that walks that fixed
// timeline: it prints canned marker lines to its pane and calls
// `ao hooks fake <event>` at each phase, exactly the way real agents report
// activity through their native hooks. The daemon pins the session PATH to its
// own binary and sets AO_SESSION_ID/AO_DATA_DIR, so the bare `ao` in the script
// reaches the daemon and its activity reports land against the spawning session.
//
// The terminal state is deterministic: the run ends on a `session-end` event
// that derives ActivityExited, NOT an incidental idle left behind by a trailing
// `stop`. Exited records agent-process death while the runtime remains available;
// it does not set is_terminated. A turn-boundary event (session-end) also clears
// the sticky `blocked` state that precedes it.
//
// Timing is controlled by AO_FAKE_SPEEDUP (a float, default 1): every phase
// sleeps for a base duration divided by the speedup, so tests can compress the
// whole run into well under a second (issue prereq #4, clock control). There is
// no trailing sleep after the terminal event, so a high speedup collapses the
// entire run to a handful of near-zero sleeps plus process spawn overhead.
package fake

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SpeedupEnv is the environment variable that divides every phase duration. A
// value <= 0 or unparseable falls back to 1 (real-time base durations).
const SpeedupEnv = "AO_FAKE_SPEEDUP"

// HarnessEnv gates the fake harness's AuthStatus. Without a truthy value the
// probe reports unauthorized, so the fake stays out of the default-selectable
// catalog on a user's machine — e2e and dev must opt in explicitly by setting
// AO_FAKE_HARNESS=1 (or any of the truthy tokens accepted by isTruthy). See
// AuthStatus for the rationale (#2692 review, @whoisasx).
const HarnessEnv = "AO_FAKE_HARNESS"

// basePhaseSeconds is how long each timeline phase lasts at speedup 1. The
// timeline has six sleeping phases (spawning, active, waiting_input, active,
// pr-push, blocked) — the terminal session-end fires with no trailing sleep —
// for an ~12s run unspeeded, which AO_FAKE_SPEEDUP compresses for tests.
const basePhaseSeconds = 2.0

// Plugin is the fake agent adapter. It holds no state and is safe for
// concurrent use.
type Plugin struct {
	agentbase.Base
}

// New returns a ready-to-register fake adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          "fake",
		Name:        "Fake",
		Description: "Deterministic LLM-free harness for e2e tests.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetLaunchCommand returns `sh -lc <script>`, where the script walks the fixed
// activity timeline. cfg is intentionally ignored except that it is honored via
// ctx cancellation: the fake never consults the prompt, permissions, or
// workspace, so its behavior is fully deterministic.
func (p *Plugin) GetLaunchCommand(ctx context.Context, _ ports.LaunchConfig) (cmd []string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Launch via the RESOLVED shell path, not a bare "sh". Manager.Spawn validates
	// argv[0] with exec.LookPath, so using the same path ResolveBinary reported
	// keeps "catalog says installed" and "spawn can launch it" in lockstep — and a
	// stripped PATH / Windows (no sh) fails here at preflight, not mid-spawn.
	sh, err := p.ResolveBinary(ctx)
	if err != nil {
		return nil, err
	}
	return []string{sh, "-lc", timelineScript(phaseSleep())}, nil
}

// AuthStatus reports authorized ONLY when AO_FAKE_HARNESS is set to a truthy
// value; otherwise it reports unauthorized. The fake needs no credentials, but
// reporting authorized unconditionally would let it drift into the
// default-selectable catalog on a user machine after a catalog refresh — desktop
// defaults can pick any authorized agent when preferred agents are unavailable.
// Gating behind an explicit opt-in keeps the fake usable for e2e/dev without
// making it production-selectable (#2692 review, @whoisasx).
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if !isTruthy(os.Getenv(HarnessEnv)) {
		return ports.AgentAuthStatusUnauthorized, nil
	}
	return ports.AgentAuthStatusAuthorized, nil
}

// isTruthy accepts the common opt-in tokens for an env-var gate. Matched
// case-insensitively; whitespace is trimmed. Empty / unset / any other value
// (including "0", "false", "no") is treated as off.
func isTruthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ResolveBinary satisfies ports.AgentBinaryResolver. The fake harness launches
// via the system shell (`sh -lc`), so it resolves to sh's actual path on PATH.
// When no runnable sh exists (Windows, a stripped-down PATH), it returns an
// error so the agent catalog reports `fake` as NOT installed and CLI preflight
// fails cleanly — rather than reporting installed via a hard-coded /bin/sh and
// then failing later in Manager.Spawn's argv[0] lookup.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	if path, shared, err := p.DiscoveredBinary(ctx, p.Manifest().ID, ports.BinaryResolveLaunch); shared {
		return path, err
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := exec.LookPath("sh")
	if err != nil {
		return "", fmt.Errorf("fake harness requires a POSIX shell (sh) on PATH: %w", err)
	}
	return path, nil
}

// DeriveActivityState maps a fake hook sub-command name onto an AO activity
// state. The bool is false when the event carries no activity signal. It is the
// deriver registered for the "fake" token in activitydispatch, and it mirrors
// the events timelineScript emits, chosen so the fake can exercise every
// activity state a real agent reports:
//
//   - session-start / user-prompt-submit / pr-push → active
//   - agent-needs-input                            → waiting_input
//   - permission-request                           → blocked
//   - stop                                         → idle
//   - session-end                                  → exited (terminal)
//
// The waiting_input / blocked split mirrors the canonical claude-code deriver:
// agent-needs-input is a request for the next INSTRUCTION (safe to nudge), while
// permission-request is a pending DECISION dialog (blocked — a stray keystroke
// could answer it). A fake session can safely map permission-request to blocked
// even without the pre/post-tool-use trio because it always follows the dialog
// with a turn-boundary event (session-end), which lifecycle uses to clear the
// sticky block. stop is retained for completeness (a mid-turn idle) though the
// timeline itself ends on session-end so the terminal state is a durable exit,
// not an ageable idle.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "user-prompt-submit", "pr-push":
		return domain.ActivityActive, true
	case "agent-needs-input":
		return domain.ActivityWaitingInput, true
	case "permission-request":
		return domain.ActivityBlocked, true
	case "stop":
		return domain.ActivityIdle, true
	case "session-end":
		return domain.ActivityExited, true
	default:
		return "", false
	}
}

// phaseSleep resolves the per-phase sleep duration in seconds from
// AO_FAKE_SPEEDUP, defaulting to basePhaseSeconds at speedup 1. It reads the
// process environment directly; tests control it with t.Setenv, which restores
// it automatically — there is no mutable package-level seam to leak across tests.
func phaseSleep() float64 {
	speedup := 1.0
	if raw := strings.TrimSpace(os.Getenv(SpeedupEnv)); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed > 0 {
			speedup = parsed
		}
	}
	return basePhaseSeconds / speedup
}

// timelineScript builds the POSIX shell script the fake runs. Each `ao hooks`
// call takes its stdin from /dev/null (the hook reads a payload from stdin) and
// swallows output, and is guarded with `|| true` so a missing/unreachable
// daemon never fails the fake mid-timeline. The phases, in order, and the
// activity state each drives (see DeriveActivityState):
//
//	(launch) spawning -> active -> waiting_input -> active[+pr-push] -> blocked -> done
//
// spawning is the pre-hook window (a sleep with no hook) so the session can be
// observed in its spawn state before the first hook flips it to active. The
// pr-push event fires during the active work phase — the canned "pushed PR"
// marker plus a `pr-push` hook (which derives active, so it refreshes rather
// than changes state). The run ENDS on session-end (derives exited), a
// turn-boundary event that clears the sticky `blocked` without terminating the
// runtime. No trailing sleep follows the terminal event, so a high
// AO_FAKE_SPEEDUP collapses the whole run.
func timelineScript(sleepSeconds float64) string {
	d := formatSeconds(sleepSeconds)
	var b strings.Builder
	// phase prints a canned marker, optionally fires a hook, and (unless it is
	// the terminal phase) sleeps to keep the derived state observable.
	phase := func(marker, event string, sleep bool) {
		fmt.Fprintf(&b, "printf '%%s\\n' 'fake-agent: %s'\n", marker)
		if event != "" {
			fmt.Fprintf(&b, "ao hooks fake %s </dev/null >/dev/null 2>&1 || true\n", event)
		}
		if sleep {
			fmt.Fprintf(&b, "sleep %s\n", d)
		}
	}
	phase("spawning", "", true)
	phase("active", "session-start", true)
	phase("waiting for input", "agent-needs-input", true)
	phase("active", "user-prompt-submit", true)
	// Print a realistic PR URL on the push line: real agents surface a link here,
	// and the desktop watches terminal output for URLs to glow the Browser tab.
	phase("pushed PR https://github.com/aoagents/agent-orchestrator/pull/2483", "pr-push", true)
	phase("blocked", "permission-request", true)
	phase("done", "session-end", false)
	return b.String()
}

// formatSeconds renders a duration for the shell `sleep` builtin: a compact
// decimal with no trailing zero noise (GNU/BSD sleep both accept fractions).
func formatSeconds(s float64) string {
	return strconv.FormatFloat(s, 'f', -1, 64)
}
