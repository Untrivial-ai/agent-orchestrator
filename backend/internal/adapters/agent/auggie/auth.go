package auggie

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	_, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := auggieLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func auggieLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(os.Getenv("AUGMENT_SESSION_AUTH")) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ports.AgentAuthStatusUnknown, false, err
	}
	return auggieSessionAuthStatus(filepath.Join(home, ".augment", "session.json"))
}

// auggieSessionAuthStatus checks Auggie's documented login session file
// without exposing its contents. The session is JSON and must contain a
// non-empty token value to count as local authorization evidence.
func auggieSessionAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	var session any
	if err := json.Unmarshal(data, &session); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if auggieSessionHasToken(session) {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func auggieSessionHasToken(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if (strings.EqualFold(key, "token") || strings.EqualFold(key, "accessToken") || strings.EqualFold(key, "access_token")) && stringSetting(child) != "" {
				return true
			}
			if auggieSessionHasToken(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if auggieSessionHasToken(child) {
				return true
			}
		}
	}
	return false
}

func stringSetting(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
