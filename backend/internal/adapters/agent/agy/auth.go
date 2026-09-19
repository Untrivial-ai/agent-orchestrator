package agy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor checks only the credential sources used by this invocation.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return agyAuthStatus(ctx, check, authutil.Dependencies{})
}

func agyAuthStatus(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	lookup := func(name string) string {
		if value, ok := check.Env[name]; ok {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(getenv(name))
	}

	home := lookup("HOME")
	goos := d.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		home = lookup("USERPROFILE")
	}
	if filepath.IsAbs(home) {
		var settings struct {
			ModelProvider string `json:"modelProvider"`
		}
		if authutil.ReadJSON(ctx, d, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), &settings) == nil &&
			strings.EqualFold(strings.TrimSpace(settings.ModelProvider), "gemini") {
			if lookup("GEMINI_API_KEY") != "" {
				return ports.AgentAuthStatusConfigured, nil
			}
			return ports.AgentAuthStatusUnknown, nil
		}
	}
	secret, _ := authutil.GenericPassword(ctx, d, "gemini", "antigravity")
	if len(strings.TrimSpace(string(secret))) > 0 {
		return ports.AgentAuthStatusConfigured, nil
	}
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return ports.AgentAuthStatusUnknown, nil
}
