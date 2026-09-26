package mimocode

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var authProbeTimeout = 3 * time.Second

var runAuthProbe = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return aoprocess.CommandContext(ctx, binary, args...).CombinedOutput()
}

// AuthStatus reports whether MiMo Code has locally configured provider credentials.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, authProbeTimeout)
	defer cancel()
	out, _ := runAuthProbe(probeCtx, binary, "providers", "list")
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if probeCtx.Err() != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	if status, ok := providerListStatus(string(out)); ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

var providerCountRE = regexp.MustCompile(`(?m)\b([1-9][0-9]*)\s+(credentials?|environment variables?)\b`)

func providerListStatus(output string) (ports.AgentAuthStatus, bool) {
	text := strings.ToLower(output)
	if providerCountRE.MatchString(text) {
		return ports.AgentAuthStatusConfigured, true
	}
	if strings.Contains(text, "0 credentials") && strings.Contains(text, "0 environment variable") {
		return ports.AgentAuthStatusUnknown, true
	}
	return ports.AgentAuthStatusUnknown, false
}
