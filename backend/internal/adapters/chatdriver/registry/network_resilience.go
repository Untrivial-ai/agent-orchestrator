package registry

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// NetworkResilienceStatus records how a shipped chat harness behaves when a
// provider connection drops mid-turn (issue #4613). It exists so support is
// stated rather than inferred: being registered in Build means an agent can run
// in chat, not that it surfaces retry or connection state. The guard test in this
// package fails if a registered driver has no entry here, or an entry names a
// harness that is not registered, so a new agent cannot be silently assumed to
// handle outages.
type NetworkResilienceStatus string

const (
	// NetworkResilienceHandled means AO surfaces retry, connection, or stall state
	// for this harness, verified against captured provider behavior. No live network
	// call is needed to reproduce it.
	NetworkResilienceHandled NetworkResilienceStatus = "handled"
	// NetworkResilienceUpstreamBlocked means AO surfaces the terminal failure
	// faithfully, but the provider emits no retry or backoff telemetry for AO to
	// display. Closing the gap requires an upstream change, not an AO one; AO must
	// not invent counts the provider never sent.
	NetworkResilienceUpstreamBlocked NetworkResilienceStatus = "upstream-blocked"
	// NetworkResilienceUnverified means the harness was not exercised locally because
	// the agent or its required bridge was not installed. It is not claimed as
	// supported until it is.
	NetworkResilienceUnverified NetworkResilienceStatus = "unverified"
)

// NetworkResilience is one harness's outage behavior plus a short human note on
// what was observed and who owns any remaining gap.
type NetworkResilience struct {
	Status NetworkResilienceStatus
	Note   string
}

// networkResilienceByHarness must stay in lockstep with Build. See #4613.
var networkResilienceByHarness = map[domain.AgentHarness]NetworkResilience{
	domain.HarnessCodex: {
		NetworkResilienceHandled,
		"willRetry app-server errors collapse onto one running provider-status activity per turn; willRetry:false stays terminal.",
	},
	domain.HarnessClaudeCode: {
		NetworkResilienceHandled,
		"claude-agent-acp reports retry and terminal failures via the jetbrains/air sessionFailure extension, rendered as a collapsing provider-failure row.",
	},
	domain.HarnessOpenCode: {
		NetworkResilienceHandled,
		"emits no message after a network cut, so the shared ACP idle watchdog surfaces a non-terminal waiting row instead of an indefinite Working turn.",
	},
	domain.HarnessCursor: {
		NetworkResilienceUpstreamBlocked,
		"returns a terminal RetriableError with no retry count or backoff; AO surfaces the terminal failure but cannot show attempts the agent never reports.",
	},
	domain.HarnessDroid: {
		NetworkResilienceUpstreamBlocked,
		"returns a generic Connection error plus an ACP internal error with no retry progress; AO surfaces the terminal failure only.",
	},
	domain.HarnessPi: {
		NetworkResilienceUnverified,
		"requires the separate pi-acp bridge, which was not installed during outage testing.",
	},
	domain.HarnessKimi: {
		NetworkResilienceUnverified,
		"not installed during outage testing; behavior not locally reproduced.",
	},
	domain.HarnessKimchi: {
		NetworkResilienceUnverified,
		"not installed during outage testing; behavior not locally reproduced.",
	},
	domain.HarnessOMP: {
		NetworkResilienceUnverified,
		"not installed during outage testing; behavior not locally reproduced.",
	},
}

// NetworkResilienceFor reports the recorded outage behavior for a harness.
func NetworkResilienceFor(harness domain.AgentHarness) (NetworkResilience, bool) {
	entry, ok := networkResilienceByHarness[harness]
	return entry, ok
}
