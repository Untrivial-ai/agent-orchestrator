package fx

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/fx/herdr"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentRuntimeLaunchEnv = (*Plugin)(nil)

// AugmentRuntimeLaunchEnv supplies Herdr's endpoint and generation-fenced pane
// identity. fx's reporter tolerates an unavailable endpoint without failing its
// agent process; environment preparation does not require a running listener.
func (p *Plugin) AugmentRuntimeLaunchEnv(env map[string]string, dataDir string, sessionID domain.SessionID, launchID string) {
	env["HERDR_SOCKET_PATH"] = herdr.SocketPath(dataDir)
	env["HERDR_PANE_ID"] = herdr.PaneID(sessionID, launchID)
}
