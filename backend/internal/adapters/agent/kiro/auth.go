package kiro

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	// Device readiness cannot assume a headless invocation.
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{Interactive: true})
}

// AuthStatusFor preserves Kiro's browser-session-before-API-key precedence.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	binary, err := p.kiroBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	status, err := kiroWhoamiAuthStatus(ctx, binary)
	if err != nil || status == ports.AgentAuthStatusConfigured || status == ports.AgentAuthStatusAuthorized {
		return status, err
	}
	key, overridden := check.Env["KIRO_API_KEY"]
	if !overridden {
		key = os.Getenv("KIRO_API_KEY")
	}
	if !check.Interactive && strings.TrimSpace(key) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	return status, nil
}

func kiroWhoamiAuthStatus(ctx context.Context, binary string) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if binary == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := authprobe.CmdRunner(probeCtx, binary, "whoami", "--format", "json")
	if ctx.Err() != nil {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if probeCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ports.AgentAuthStatusUnknown, nil
	}
	var result kiroWhoamiResponse
	if json.Unmarshal(out, &result) != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	// The native signed-out response is {"account":null}, including on exit 1.
	// A missing field, or a contradictory accountType, is not signed-out proof.
	if result.Account != nil {
		if string(result.Account) == "null" && result.AccountType == "" && result.StartURL == nil && result.Region == nil {
			return ports.AgentAuthStatusUnauthorized, nil
		}
		return ports.AgentAuthStatusUnknown, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	// Upstream WhoamiArgs loads (and may refresh) the stored token. Unexpired
	// tokens can skip a provider request, so the account alone is configured.
	switch result.AccountType {
	case "BuilderId":
		return ports.AgentAuthStatusConfigured, nil
	case "IamIdentityCenter":
		if result.StartURL != nil && strings.TrimSpace(*result.StartURL) != "" && result.Region != nil && strings.TrimSpace(*result.Region) != "" {
			return ports.AgentAuthStatusConfigured, nil
		}
	}
	return ports.AgentAuthStatusUnknown, nil
}

type kiroWhoamiResponse struct {
	Account     json.RawMessage `json:"account"`
	AccountType string          `json:"accountType"`
	StartURL    *string         `json:"startUrl"`
	Region      *string         `json:"region"`
}
