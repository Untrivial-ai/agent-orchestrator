package zcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status. ZCode stores
// provider credentials (apiKey) in the user-global ~/.zcode/cli/config.json;
// only the structure is inspected — that at least one provider entry carries
// a non-empty options.apiKey — never the key values themselves.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := zcodeLocalAuthStatus(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func zcodeLocalAuthStatus() (ports.AgentAuthStatus, bool, error) {
	zcodeHome, err := zcodeConfigDir()
	if err != nil || zcodeHome == "" {
		return ports.AgentAuthStatusUnknown, false, err
	}
	path := filepath.Join(zcodeHome, "cli", "config.json")
	data, err := os.ReadFile(path) //nolint:gosec // fixed user-config path
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}

	var config struct {
		Provider map[string]struct {
			Options struct {
				APIKey string `json:"apiKey"`
			} `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	// This is a structural inspection only: a persisted apiKey string proves
	// the credential exists on disk, not that it is valid. zcode's OAuth
	// tokens can expire while remaining in config.json (observed live), and
	// reporting "authorized" on a stale key misleads spawn decisions. Report
	// unknown; a bounded native auth probe can upgrade this when zcode
	// grows one.
	return ports.AgentAuthStatusUnknown, false, nil
}

func zcodeConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", nil
	}
	return filepath.Join(home, ".zcode"), nil
}
