package claudecode

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// DetectTerminalActivity reports idle only when Claude's rendered surface
// positively shows an idle composer: no active spinner/interrupt chrome and no
// pending provider decision frame. Anything else is not authoritative.
//
// Claude Code's durable activity is hook-driven, and a turn that dies without
// emitting its Stop hook (auth expiry, CLI crash, network loss) leaves the
// session stuck active forever. This capability is what opts the adapter into
// the activity observer's stale-active reconciliation, which screen-proves
// idle after the hook state has been quiet past its staleness limit. Claude
// intentionally does not implement ContinuousTerminalActivityDetector: hooks
// stay authoritative while they flow, and the composer draft state must never
// promote or demote sticky states on its own.
func (p *Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	if p.InspectTerminalSurface(output).Work == ports.TerminalSurfaceWorkIdle {
		return domain.ActivityIdle, true
	}
	return "", false
}
