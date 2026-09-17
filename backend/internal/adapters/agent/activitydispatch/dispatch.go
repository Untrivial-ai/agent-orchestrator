// Package activitydispatch is the single source of truth mapping the agent
// token in `ao hooks <agent> <event>` onto the function that interprets that
// agent's hook callbacks as an AO activity state.
//
// The hidden `ao hooks` CLI command dispatches a live callback through it. Every
// adapter that installs `ao hooks <tok>` callbacks must have a deriver
// registered here — otherwise the adapter writes callbacks that nothing on the
// receiving side understands, so its activity is silently never reported.
package activitydispatch

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/activitystate"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agy"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/aider"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/amp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/auggie"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/continueagent"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/cursor"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/droid"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/fake"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kimchi"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/muse"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/omp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/pi"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/primeagent"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/vibe"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// DeriveFunc maps a native agent hook event and its raw stdin payload onto an AO
// activity state. ok=false means the event carries no activity signal.
type DeriveFunc func(event string, payload []byte) (domain.ActivityState, bool)

// Derivers maps the agent token in `ao hooks <agent> <event>` to its deriver.
// Per-adapter PRs add their tokens here as they land.
var Derivers = map[string]DeriveFunc{
	// Adapters that parse hook payloads for finer-grained state keep their own
	// deriver; the rest share the name-only StandardDeriveActivityState.
	"claude-code": claudecode.DeriveActivityState,
	"grok":        claudecode.DeriveActivityState,
	"muse":        muse.DeriveActivityState,
	"omp":         omp.DeriveActivityState,
	"codex":       codex.DeriveActivityState,
	"continue":    continueagent.DeriveActivityState,
	"droid":       droid.DeriveActivityState,
	"agy":         agy.DeriveActivityState,
	"aider":       aider.DeriveActivityState,
	"kimchi":      kimchi.DeriveActivityState,
	"opencode":    opencode.DeriveActivityState,
	"prime-agent": primeagent.DeriveActivityState,
	"amp":         amp.DeriveActivityState,
	"pi":          pi.DeriveActivityState,
	"auggie":      auggie.DeriveActivityState,
	"goose":       activitystate.StandardDeriveActivityState,
	"devin":       activitystate.StandardDeriveActivityState,
	"cursor":      cursor.DeriveActivityState,
	"qwen":        activitystate.StandardDeriveActivityState,
	"copilot":     activitystate.StandardDeriveActivityState,
	"kimi":        activitystate.StandardDeriveActivityState,
	"cline":       activitystate.StandardDeriveActivityState,
	"kiro":        activitystate.StandardDeriveActivityState,
	"kilocode":    activitystate.StandardDeriveActivityState,
	"autohand":    activitystate.StandardDeriveActivityState,
	"vibe":        vibe.DeriveActivityState,
	"fake":        fake.DeriveActivityState,
}

// SignalCoverage describes how much of a harness lifecycle AO can observe.
// Partial coverage can report useful transitions but cannot prove that silence
// means a broken pipeline. Complete coverage is eligible for the no_signal
// watchdog because the harness is expected to report promptly after launch or
// prompt submission.
type SignalCoverage uint8

const (
	// SignalCoverageNone means the harness has no activity callback pipeline.
	SignalCoverageNone SignalCoverage = iota
	// SignalCoveragePartial means the harness emits only a subset of lifecycle
	// transitions, such as Aider's response-ready notification.
	SignalCoveragePartial
	// SignalCoverageComplete means the harness emits enough lifecycle callbacks
	// for prolonged initial silence to indicate a broken pipeline.
	SignalCoverageComplete
)

// signalCoverageOverrides records harnesses whose callback coverage cannot be
// inferred from a same-named Derivers entry. Aider has only a completion
// callback. Continue's Claude-compatible hooks vary by installed CLI version,
// so its terminal fallback is useful without treating hook silence as broken.
var signalCoverageOverrides = map[domain.AgentHarness]SignalCoverage{
	domain.HarnessAider:    SignalCoveragePartial,
	domain.HarnessContinue: SignalCoveragePartial,
}

// CoverageForHarness returns the activity-signal coverage for a selectable
// harness. Same-named callback pipelines are complete by default; exceptional
// aliases and partial pipelines are declared above.
func CoverageForHarness(h domain.AgentHarness) SignalCoverage {
	if coverage, ok := signalCoverageOverrides[h]; ok {
		return coverage
	}
	if _, ok := Derivers[string(h)]; ok {
		return SignalCoverageComplete
	}
	return SignalCoverageNone
}

// Derive looks up the deriver for an agent token and applies it. ok=false when
// the token has no registered deriver or the event carries no activity signal —
// the caller reports nothing in either case.
func Derive(agent, event string, payload []byte) (domain.ActivityState, bool) {
	derive, found := Derivers[agent]
	if !found {
		return "", false
	}
	return derive(event, payload)
}

// SupportsHarness reports whether a harness has any activity callback pipeline,
// including partial coverage and compatibility aliases.
func SupportsHarness(h domain.AgentHarness) bool {
	return CoverageForHarness(h) != SignalCoverageNone
}

// FullySupportsHarness reports whether a harness has complete activity signal
// coverage. Status derivation uses this narrower predicate for the no_signal
// watchdog so a partial pipeline is not penalized for callbacks it cannot emit.
func FullySupportsHarness(h domain.AgentHarness) bool {
	return CoverageForHarness(h) == SignalCoverageComplete
}
