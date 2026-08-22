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
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/amp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/auggie"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/droid"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/fake"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kimchi"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/muse"
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
	"codex":       codex.DeriveActivityState,
	"droid":       droid.DeriveActivityState,
	"agy":         agy.DeriveActivityState,
	"kimchi":      kimchi.DeriveActivityState,
	"opencode":    opencode.DeriveActivityState,
	"prime-agent": primeagent.DeriveActivityState,
	"amp":         amp.DeriveActivityState,
	"pi":          pi.DeriveActivityState,
	"auggie":      auggie.DeriveActivityState,
	"goose":       activitystate.StandardDeriveActivityState,
	"devin":       activitystate.StandardDeriveActivityState,
	"cursor":      activitystate.StandardDeriveActivityState,
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

// SupportsHarness reports whether a harness has an activity pipeline at all:
// a registered deriver here means its adapter installs `ao hooks <harness>`
// callbacks that can reach the daemon. Status derivation uses this to decide
// whether prolonged silence is suspicious (no_signal) or simply all a hook-less
// harness can ever report (idle). Harness names and `ao hooks` agent tokens are
// the same strings by convention.
func SupportsHarness(h domain.AgentHarness) bool {
	_, ok := Derivers[string(h)]
	return ok
}
