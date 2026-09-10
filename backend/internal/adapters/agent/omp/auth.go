package omp

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // register sqlite driver for OMP auth database probes

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns OMP's local authentication status from its credential
// stores. OMP has no non-interactive `auth status` command: unknown positional
// arguments launch its interactive agent and must never be used as probes.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	_, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := ompLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func ompLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	configDir, ok := ompConfigDir()
	if !ok {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if status, ok, err := ompAgentDBAuthStatus(filepath.Join(configDir, "agent.db")); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	} else if ok {
		return status, true, nil
	}
	return ompAuthJSONStatus(filepath.Join(configDir, "auth.json"))
}

func ompConfigDir() (string, bool) {
	if configDir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); configDir != "" {
		return configDir, true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".omp", "agent"), true
}

type ompAuthEntry struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

func ompAuthJSONStatus(path string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}

	var entries map[string]ompAuthEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if len(entries) == 0 {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	for provider, entry := range entries {
		if strings.TrimSpace(provider) == "" {
			continue
		}
		if strings.TrimSpace(entry.Key) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func ompAgentDBAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	if strings.TrimSpace(path) == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	} else if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}

	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	defer func() {
		_ = db.Close()
	}()

	var total int
	var enabled int
	err = db.QueryRow(`
		SELECT
			COUNT(*),
			COUNT(CASE WHEN trim(coalesce(data, '')) <> '' AND disabled_cause IS NULL THEN 1 END)
		FROM auth_credentials
	`).Scan(&total, &enabled)
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if enabled > 0 {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	if total > 0 {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}
