package copilot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus reports local configuration, without claiming provider validation.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

func (p *Plugin) AuthStatusFor(ctx context.Context, scope ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return copilotAuthStatus(ctx, scope, authutil.Dependencies{})
}

var copilotTokenEnvVars = []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}

func copilotLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status, err := copilotAuthStatus(ctx, ports.AgentAuthCheck{}, authutil.Dependencies{})
	return status, status != ports.AgentAuthStatusUnknown, err
}

func copilotAuthStatus(ctx context.Context, scope ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	baseEnv := d.Getenv
	if baseEnv == nil {
		baseEnv = os.Getenv
	}
	env := func(key string) string {
		if value, ok := scope.Env[key]; ok {
			return value
		}
		return baseEnv(key)
	}
	for _, key := range copilotTokenEnvVars {
		if token := strings.TrimSpace(env(key)); token != "" {
			// The first set variable selects the token, including unsupported
			// classic PATs. A lower-priority credential cannot override it.
			if copilotUsableToken(token) {
				return ports.AgentAuthStatusConfigured, nil
			}
			return ports.AgentAuthStatusUnknown, nil
		}
	}
	// BYOK endpoints can be keyless. Presence identifies configuration only;
	// it does not validate the endpoint or establish a no-auth provider.
	if strings.TrimSpace(env("COPILOT_PROVIDER_BASE_URL")) != "" && strings.TrimSpace(env("COPILOT_MODEL")) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}

	goos := d.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "darwin" {
		// Copilot supports multiple accounts under this documented service;
		// there is no fixed account selector. A service-only read establishes
		// local configuration, never validation of an active account.
		out, err := authutil.RunCommand(ctx, d, "/usr/bin/security", "find-generic-password", "-s", "copilot-cli", "-w")
		if ctx.Err() != nil {
			return ports.AgentAuthStatusUnknown, ctx.Err()
		}
		if err == nil && copilotUsableToken(string(out)) {
			return ports.AgentAuthStatusConfigured, nil
		}
	}
	// The bounded command runner inherits the daemon environment. If the
	// invocation changes gh's credential selection, do not mistake the
	// daemon's gh token for the scoped credential; try the scoped file next.
	ghEnvMatches := true
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_HOST", "GH_CONFIG_DIR", "HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		if value, ok := scope.Env[key]; ok && value != baseEnv(key) {
			ghEnvMatches = false
			break
		}
	}
	if ghEnvMatches {
		out, err := authutil.RunCommand(ctx, d, "gh", "auth", "token")
		if ctx.Err() != nil {
			return ports.AgentAuthStatusUnknown, ctx.Err()
		}
		if err == nil && copilotUsableToken(string(out)) {
			return ports.AgentAuthStatusConfigured, nil
		}
	}
	dir := strings.TrimSpace(env("COPILOT_HOME"))
	if dir == "" {
		homeKey := "HOME"
		if goos == "windows" {
			homeKey = "USERPROFILE"
		}
		if home := strings.TrimSpace(env(homeKey)); home != "" {
			dir = filepath.Join(home, ".copilot")
		}
	}
	if dir == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	if !filepath.IsAbs(dir) && scope.WorkingDir != "" {
		dir = filepath.Join(scope.WorkingDir, dir)
	}
	status, _, _ := copilotConfigAuthStatusWith(ctx, filepath.Join(dir, "config.json"), d)
	return status, ctx.Err()
}

func copilotConfigAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	return copilotConfigAuthStatusWith(context.Background(), path, authutil.Dependencies{})
}

func copilotConfigAuthStatusWith(ctx context.Context, path string, d authutil.Dependencies) (ports.AgentAuthStatus, bool, error) {
	data, err := authutil.ReadFile(ctx, d, path)
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	for _, key := range []string{"authToken", "accessToken", "token"} {
		var token string
		if err := json.Unmarshal(config[key], &token); err == nil && copilotUsableToken(token) {
			return ports.AgentAuthStatusConfigured, true, nil
		}
	}
	// Native Copilot stores plaintext fallbacks by host:login under this
	// field. Read only string tokens, never account metadata objects.
	var tokens map[string]json.RawMessage
	if json.Unmarshal(config["copilot_tokens"], &tokens) == nil {
		for _, value := range tokens {
			var token string
			if json.Unmarshal(value, &token) == nil && copilotUsableToken(token) {
				return ports.AgentAuthStatusConfigured, true, nil
			}
		}
	}
	// loggedInUsers is account metadata, and session events are historical
	// activity. Neither establishes the presence of an effective credential.
	return ports.AgentAuthStatusUnknown, false, nil
}

func copilotUsableToken(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, "ghp_")
}
