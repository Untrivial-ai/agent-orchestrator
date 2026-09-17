package qoder

import (
	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	_ ports.AgentContinuationCapabilityProvider = (*Plugin)(nil)
	_ ports.AgentFreshNativeSessionIDProvider   = (*Plugin)(nil)
)

// ContinuationCapabilities reports Qoder's continuation behavior: --session-id
// lets AO assign the id for a fresh provider conversation, and --resume takes
// that same id back, so one stable AO session can own several Qoder
// conversations.
func (p *Plugin) ContinuationCapabilities() ports.ContinuationCapabilities {
	return ports.ContinuationCapabilities{
		FreshNativeSessionID: ports.FreshNativeSessionIDCallerAssigned,
	}
}

// NewNativeSessionID returns a provider-valid id for a distinct fresh Qoder
// conversation owned by an existing AO session. Qoder accepts any UUID.
func (p *Plugin) NewNativeSessionID() string {
	return uuid.NewString()
}
