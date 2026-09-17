package qoder

import (
	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	_ ports.AgentContinuationCapabilityProvider = (*Plugin)(nil)
	_ ports.AgentFreshNativeSessionIDProvider   = (*Plugin)(nil)
)

func (p *Plugin) ContinuationCapabilities() ports.ContinuationCapabilities {
	return ports.ContinuationCapabilities{
		FreshNativeSessionID: ports.FreshNativeSessionIDCallerAssigned,
	}
}

func (p *Plugin) NewNativeSessionID() string {
	return uuid.NewString()
}
