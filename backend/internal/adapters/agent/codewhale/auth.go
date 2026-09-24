package codewhale

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var activeProviderPattern = regexp.MustCompile(`(?mi)^active provider:\s*([^\s(]+)`)
var activeSourcePattern = regexp.MustCompile(`(?mi)^active source:\s*([^\s(]+)`)

// AuthStatus asks Codewhale which provider is active, then inspects that
// provider's local credential source. This is presence evidence only, so it
// reports configured rather than authorized.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		if errors.Is(err, ports.ErrAgentBinaryNotFound) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}
	out, err := p.execute(ctx, "", nil, "auth", "status")
	if err != nil {
		return ports.AgentAuthStatusUnknown, fmt.Errorf("codewhale auth status: %w", err)
	}
	match := activeProviderPattern.FindSubmatch(out)
	if match == nil || strings.TrimSpace(string(match[1])) == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	provider := strings.TrimSpace(string(match[1]))
	out, err = p.execute(ctx, "", nil, "auth", "status", "--provider", provider)
	if err != nil {
		return ports.AgentAuthStatusUnknown, fmt.Errorf("codewhale auth status for provider %q: %w", provider, err)
	}
	match = activeSourcePattern.FindSubmatch(out)
	if match == nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	source := strings.ToLower(strings.TrimSpace(string(match[1])))
	if source == "" || source == "missing" || source == "unset" || source == "none" {
		return ports.AgentAuthStatusUnknown, nil
	}
	return ports.AgentAuthStatusConfigured, nil
}
