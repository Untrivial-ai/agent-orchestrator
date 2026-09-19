package agy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
		settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
		lstat := d.Lstat
		if lstat == nil {
			lstat = os.Lstat
		}
		info, err := lstat(settingsPath)
		if err == nil {
			if !info.Mode().IsRegular() {
				return ports.AgentAuthStatusUnknown, nil
			}
			data, err := authutil.ReadFile(ctx, d, settingsPath)
			if err != nil {
				return ports.AgentAuthStatusUnknown, ctx.Err()
			}
			var settings struct {
				ModelProvider json.RawMessage `json:"modelProvider"`
			}
			if json.Unmarshal(data, &settings) != nil {
				return ports.AgentAuthStatusUnknown, nil
			}
			if len(settings.ModelProvider) > 0 {
				var provider string
				if json.Unmarshal(settings.ModelProvider, &provider) != nil || provider != "gemini" {
					return ports.AgentAuthStatusUnknown, nil
				}
				if lookup("GEMINI_API_KEY") != "" {
					return ports.AgentAuthStatusConfigured, nil
				}
				return ports.AgentAuthStatusUnknown, nil
			}
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ports.AgentAuthStatusUnknown, nil
		}
	}

	secret, _ := authutil.GenericPassword(ctx, d, "gemini", "antigravity")
	if agyStoredTokenConfigured(secret, d) {
		return ports.AgentAuthStatusConfigured, nil
	}
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return ports.AgentAuthStatusUnknown, nil
}

func agyStoredTokenConfigured(secret []byte, d authutil.Dependencies) bool {
	var stored struct {
		Token struct {
			AccessToken  string    `json:"access_token"`
			RefreshToken string    `json:"refresh_token"`
			Expiry       time.Time `json:"expiry"`
		} `json:"token"`
	}
	if json.Unmarshal(secret, &stored) != nil {
		return false
	}
	if strings.TrimSpace(stored.Token.RefreshToken) != "" {
		return true
	}
	if strings.TrimSpace(stored.Token.AccessToken) == "" {
		return false
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	return stored.Token.Expiry.IsZero() || stored.Token.Expiry.After(now())
}
