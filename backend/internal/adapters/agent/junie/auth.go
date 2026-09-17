package junie

import (
	"context"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"os"
	"strings"
)

var junieAPIKeys = []string{"JUNIE_API_KEY", "JUNIE_ANTHROPIC_API_KEY", "JUNIE_OPENAI_API_KEY", "JUNIE_GOOGLE_API_KEY", "JUNIE_GROK_API_KEY", "JUNIE_META_API_KEY", "JUNIE_OPENROUTER_API_KEY", "JUNIE_LITELLM_API_KEY"}

func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	for _, name := range junieAPIKeys {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusUnknown, nil
		}
	}
	return ports.AgentAuthStatusUnknown, nil
}

var _ ports.AgentAuthChecker = (*Plugin)(nil)
