package amp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := ampLocalAuthStatus(ctx); err != nil || ok {
		return status, err
	}
	return ampUsageAuthStatus(ctx, binary)
}

func ampLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(os.Getenv("AMP_API_KEY")) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	if status, ok, err := ampSecretsAuthStatus(ampSecretsPath()); err != nil || ok {
		return status, ok, err
	}
	status, ok, err := ampSettingsAuthStatus(ampSettingsPath())
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	return status, ok, nil
}

// Amp documents its persisted CLI credentials in ~/.local/share/amp/secrets.json.
// The schema has changed across CLI releases, so only credential-shaped fields
// are considered; arbitrary settings in the file must not imply authentication.
func ampSecretsPath() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "amp", "secrets.json")
	}
	return ""
}

func ampSecretsAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	if path == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for key, value := range values {
		lower := strings.ToLower(key)
		if !strings.Contains(lower, "token") && !strings.Contains(lower, "secret") && !strings.Contains(lower, "key") {
			continue
		}
		if strings.TrimSpace(stringValue(value)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, true, nil
}

func ampSettingsPath() string {
	if path := strings.TrimSpace(os.Getenv("AMP_SETTINGS_FILE")); path != "" {
		return expandHome(path)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "amp", "settings.json")
	}
	return ""
}

func ampSettingsAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	if path == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.AgentAuthStatusUnknown, false, nil
		}
		return ports.AgentAuthStatusUnknown, false, err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, key := range []string{"amp.apiKey", "amp.api_key", "apiKey", "api_key"} {
		if value, ok := settings[key]; ok {
			if strings.TrimSpace(stringValue(value)) != "" {
				return ports.AgentAuthStatusAuthorized, true, nil
			}
			return ports.AgentAuthStatusUnknown, false, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

// ampUsageAuthStatus recognizes Amp's own signed-in account output. It is
// deliberately authorization-only: a failed or unfamiliar usage command does
// not prove that an interactive launch cannot authenticate.
func ampUsageAuthStatus(ctx context.Context, binary string) (ports.AgentAuthStatus, error) {
	if binary == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := authprobe.CmdRunner(probeCtx, binary, "usage", "--no-color")
	if probeCtx.Err() != nil {
		if probeCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}
	if err == nil && strings.Contains(strings.ToLower(string(out)), "signed in as") {
		return ports.AgentAuthStatusAuthorized, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
