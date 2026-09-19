package muse

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

// AuthStatus reports local Meta provider credentials without remote validation.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

func (p *Plugin) AuthStatusFor(ctx context.Context, scope ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return museAuthStatus(ctx, scope, authutil.Dependencies{})
}

const museAPIKeyEnvVar = "META_API_KEY" //nolint:gosec // environment variable name, not a credential

func museLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status, err := museAuthStatus(ctx, ports.AgentAuthCheck{}, authutil.Dependencies{})
	return status, status != ports.AgentAuthStatusUnknown, err
}

func museAuthStatus(ctx context.Context, scope ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
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
	if strings.TrimSpace(env(museAPIKeyEnvVar)) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	// MUSE_AUTH_PATH wins, then the XDG config root, then the user home.
	path := strings.TrimSpace(env("MUSE_AUTH_PATH"))
	if path == "" {
		root := strings.TrimSpace(env("XDG_CONFIG_HOME"))
		if root == "" {
			goos := d.GOOS
			if goos == "" {
				goos = runtime.GOOS
			}
			homeKey := "HOME"
			if goos == "windows" {
				homeKey = "USERPROFILE"
			}
			if home := strings.TrimSpace(env(homeKey)); home != "" {
				root = filepath.Join(home, ".config")
			}
		}
		if root == "" {
			return ports.AgentAuthStatusUnknown, nil
		}
		path = filepath.Join(root, "muse", "auth.json")
	}
	if !filepath.IsAbs(path) && scope.WorkingDir != "" {
		path = filepath.Join(scope.WorkingDir, path)
	}
	status, _, _ := museAuthJSONStatusWith(ctx, path, d)
	return status, ctx.Err()
}

func museAuthJSONStatus(path string) (ports.AgentAuthStatus, bool, error) {
	return museAuthJSONStatusWith(context.Background(), path, authutil.Dependencies{})
}

func museAuthJSONStatusWith(ctx context.Context, path string, d authutil.Dependencies) (ports.AgentAuthStatus, bool, error) {
	data, err := authutil.ReadFile(ctx, d, path)
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	var auth struct {
		Storage   string `json:"storage"`
		Providers struct {
			Meta struct {
				Storage      string          `json:"storage"`
				Mechanism    string          `json:"mechanism"`
				AccessToken  string          `json:"access_token"`
				RefreshToken string          `json:"refresh_token"`
				ExpiresAt    json.RawMessage `json:"expires_at"`
				APIKey       string          `json:"api_key"`
			} `json:"meta"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return ports.AgentAuthStatusUnknown, false, errors.New("invalid credential JSON")
	}
	meta := auth.Providers.Meta
	for _, storage := range []string{auth.Storage, meta.Storage} {
		if storage = strings.ToLower(strings.TrimSpace(storage)); storage != "" && storage != "file" {
			// Keychain metadata is not a secret. Until Muse's fixed service and
			// account selectors are source-confirmed, leave it unknown rather
			// than reading an arbitrary item or accepting stale file fields.
			return ports.AgentAuthStatusUnknown, false, nil
		}
	}
	switch strings.ToLower(strings.TrimSpace(meta.Mechanism)) {
	case "oauth":
		if strings.TrimSpace(meta.AccessToken) != "" {
			if len(meta.ExpiresAt) > 0 {
				// The native auth.json field is an integer Unix timestamp.
				var seconds int64
				if json.Unmarshal(meta.ExpiresAt, &seconds) != nil {
					return ports.AgentAuthStatusUnknown, false, nil
				}
				expires, ok := authutil.ParseExpiry(seconds)
				if !ok {
					return ports.AgentAuthStatusUnknown, false, nil
				}
				now := time.Now()
				if d.Now != nil {
					now = d.Now()
				}
				evidence := authutil.ExpiryEvidence(expires, strings.TrimSpace(meta.RefreshToken) != "", now)
				return evidence.Status, true, nil
			}
			return ports.AgentAuthStatusConfigured, true, nil
		}
	case "api_key", "api-key", "apikey":
		if strings.TrimSpace(meta.APIKey) != "" {
			return ports.AgentAuthStatusConfigured, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}
