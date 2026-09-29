package mimocode

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var authProbeTimeout = 10 * time.Second

var runAuthProbe = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return aoprocess.CommandContext(ctx, binary, args...).CombinedOutput()
}

// AuthStatus reports whether MiMo Code has credentials or its built-in free model.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, authProbeTimeout)
	defer cancel()
	out, _ := runAuthProbe(probeCtx, binary, "auth", "list")
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if probeCtx.Err() != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	if status, ok := providerListStatus(string(out)); ok && status == ports.AgentAuthStatusConfigured {
		return status, nil
	}
	// MiMo Auto is usable without a login, so it is absent from `auth list`.
	// Ask MiMo itself whether the free channel is available before reporting
	// an inconclusive auth state.
	models, err := runAuthProbe(probeCtx, binary, "models", "mimo")
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if probeCtx.Err() != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	if err == nil && freeModelListed(string(models)) {
		return ports.AgentAuthStatusConfigured, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func freeModelListed(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "mimo/mimo-auto" {
			return true
		}
	}
	return false
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
