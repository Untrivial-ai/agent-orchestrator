package userconfig

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// SetUserConfigInput is the body shape for PUT /api/v1/user-config. AgentConfig
// replaces the stored user-scope config wholesale; a zero-value config clears it
// (stores SQL NULL). See ADR 0002: the write is wholesale, resolution is
// field-by-field.
type SetUserConfigInput struct {
	AgentConfig domain.AgentConfig `json:"agentConfig"`
}
