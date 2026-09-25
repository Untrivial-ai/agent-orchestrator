package openhands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus reports whether OpenHands has a usable LLM configuration.
//
// Without agent_settings.json the TUI opens its first-run settings wizard
// instead of working, so a missing or model-less file is unauthorized. A
// configured model is authorized: local models legitimately have no API key,
// and AO does not launch with --override-with-envs, so LLM_* environment
// variables are not consulted.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return settingsAuthStatus(settingsPath())
}

// settingsPath mirrors OpenHands' persistence dir: OPENHANDS_PERSISTENCE_DIR,
// else ~/.openhands.
func settingsPath() string {
	dir := strings.TrimSpace(os.Getenv("OPENHANDS_PERSISTENCE_DIR"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		dir = filepath.Join(home, ".openhands")
	}
	return filepath.Join(dir, "agent_settings.json")
}

func settingsAuthStatus(path string) (ports.AgentAuthStatus, error) {
	if path == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // fixed OpenHands settings location
	if errors.Is(err, os.ErrNotExist) {
		return ports.AgentAuthStatusUnauthorized, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	var settings struct {
		LLM struct {
			Model string `json:"model"`
		} `json:"llm"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		// OpenHands treats a corrupt file as needing setup too, but the user
		// may be mid-edit; do not claim a definite state.
		return ports.AgentAuthStatusUnknown, nil //nolint:nilerr // malformed settings → can't determine auth status
	}
	if strings.TrimSpace(settings.LLM.Model) == "" {
		return ports.AgentAuthStatusUnauthorized, nil
	}
	return ports.AgentAuthStatusAuthorized, nil
}
