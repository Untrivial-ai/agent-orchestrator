package fx

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const defaultStatusTimeout = 3 * time.Second

type commandRunner func(context.Context, string, ...string) ([]byte, error)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus runs fx's documented local status probe. Its output establishes
// missing or expired credentials, but a named credential source alone remains
// unknown until a provider request proves authorization.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	binary, err := p.fxBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}

	timeout := p.statusTimeout
	if timeout <= 0 {
		timeout = defaultStatusTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runner := p.statusRunner
	if runner == nil {
		runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return aoprocess.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	output, _ := runner(probeCtx, binary, "status", "--json")
	if probeCtx.Err() != nil {
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}
	return authStatusFromJSON(output), nil
}

func authStatusFromJSON(output []byte) ports.AgentAuthStatus {
	var status struct {
		Auth        string `json:"auth"`
		AuthExpired bool   `json:"auth_expired"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return ports.AgentAuthStatusUnknown
	}
	if status.AuthExpired || strings.EqualFold(strings.TrimSpace(status.Auth), "missing") {
		return ports.AgentAuthStatusUnauthorized
	}
	// A non-empty auth name proves only that credentials are configured. The
	// generic port has no "configured" state, and AO must not promote local
	// evidence to authorized without a provider round-trip.
	return ports.AgentAuthStatusUnknown
}
