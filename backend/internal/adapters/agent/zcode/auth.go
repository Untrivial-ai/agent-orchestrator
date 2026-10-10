package zcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus observes native configuration only. Current ZCode encrypts values
// in the shared credential store; AO never decrypts them or treats an unsigned
// JWT expiry as proof of authorization. Native login/request code validates them.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return zcodeCredentialsAuthStatus(), nil
}
func zcodeCredentialsAuthStatus() ports.AgentAuthStatus {
	dir, err := zcodeConfigDir()
	if err != nil || dir == "" {
		return ports.AgentAuthStatusUnknown
	}
	data, err := os.ReadFile(filepath.Join(dir, "v2", "credentials.json")) //nolint:gosec // native configured credential path; values are never logged
	if err != nil {
		return ports.AgentAuthStatusUnknown
	}
	var credentials map[string]string
	if err := json.Unmarshal(data, &credentials); err != nil {
		return ports.AgentAuthStatusUnknown
	}
	for _, key := range []string{"oauth:zai:access_token", "oauth:bigmodel:access_token"} {
		if strings.TrimSpace(credentials[key]) != "" {
			return ports.AgentAuthStatusConfigured
		}
	}
	return ports.AgentAuthStatusUnknown
}
func zcodeConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(os.Getenv("ZCODE_DATA_BASE_DIR"))
	if base == "" {
		base = home
	}
	if base == "~" {
		base = home
	} else if strings.HasPrefix(base, "~/") || strings.HasPrefix(base, `~\`) {
		base = filepath.Join(home, base[2:])
	}
	absolute, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	return filepath.Join(absolute, ".zcode"), nil
}
