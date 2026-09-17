package qoder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// AuthStatus reports whether Qoder is authenticated. Qoder offers no offline
// credential-validation command, and a personal access token or on-disk
// credential file only proves configuration, not that the remote account still
// authorizes requests. Every path therefore returns unknown; an absent binary
// is reported as unknown rather than an error.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if _, err := p.ResolveBinary(ctx); err != nil {
		if errors.Is(err, ports.ErrAgentBinaryNotFound) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}
	// Local credential presence is configuration evidence, not proof that the
	// remote account currently authorizes requests. AO's present vocabulary has
	// no "configured" state, so conservatively report unknown.
	if strings.TrimSpace(os.Getenv("QODER_PERSONAL_ACCESS_TOKEN")) != "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	root := strings.TrimSpace(os.Getenv("QODER_CONFIG_DIR"))
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".qoder")
		}
	}
	if root != "" {
		for _, name := range []string{"credentials.json", "auth.json"} {
			if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				return ports.AgentAuthStatusUnknown, nil
			}
		}
	}
	return ports.AgentAuthStatusUnknown, nil
}
