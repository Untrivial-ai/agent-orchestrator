package primeagent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	extensionDirName            = "agent-runtime"
	extensionFileName           = "ao-activity.ts"
	primeAgentCodingAgentDirEnv = "PRIME_AGENT_CODING_AGENT_DIR"
	sessionDirName              = "sessions"
)

//go:embed assets/ao-activity.ts
var primeAgentExtensionSource string

// GetAgentHooks installs the AO-managed extension at a stable path under
// AO_DATA_DIR. Prime receives this exact path through --extension. The install
// is atomic and skips rewriting identical content.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := extensionPath(cfg.DataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("prime-agent.GetAgentHooks: create extension directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	existing, err := os.ReadFile(path) //nolint:gosec // stable AO-owned path under DataDir
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	switch {
	case err == nil && string(existing) == primeAgentExtensionSource:
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("prime-agent.GetAgentHooks: set extension mode: %w", err)
		}
		return ctx.Err()
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("prime-agent.GetAgentHooks: read extension: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := hookutil.AtomicWriteFile(path, []byte(primeAgentExtensionSource), 0o600); err != nil {
		return fmt.Errorf("prime-agent.GetAgentHooks: write extension: %w", err)
	}
	return ctx.Err()
}

func extensionPath(dataDir string) (string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return "", errors.New("prime-agent: DataDir is required for the managed extension")
	}
	return filepath.Join(dataDir, extensionDirName, adapterID, extensionFileName), nil
}

func primeDataDir(dataDir string) (string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return "", errors.New("prime-agent: DataDir is required for Prime state")
	}
	return filepath.Join(dataDir, extensionDirName, adapterID), nil
}

func sessionDir(dataDir string) (string, error) {
	dir, err := primeDataDir(dataDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionDirName), nil
}
