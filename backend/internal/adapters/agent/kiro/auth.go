package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.kiroBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if strings.TrimSpace(os.Getenv("KIRO_API_KEY")) != "" {
		return ports.AgentAuthStatusAuthorized, nil
	}
	return kiroWhoamiAuthStatus(ctx, binary)
}

type kiroWhoamiIdentity struct {
	AccountType string `json:"accountType"`
	Email       string `json:"email"`
	StartURL    string `json:"startUrl"`
}

func kiroWhoamiAuthStatus(ctx context.Context, binary string) (ports.AgentAuthStatus, error) {
	if binary == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	// Kiro documents `whoami` as its authentication-status command. Keep the
	// probe bounded so catalog refresh cannot hang on a broken CLI install.
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := authprobe.CmdRunner(probeCtx, binary, "whoami", "--format", "json")
	if probeCtx.Err() != nil {
		if probeCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}

	// Keyword-based classification first: it recognizes kiro-cli's plain-text
	// "not logged in" message regardless of the JSON path below.
	if status := authprobe.StatusFromText(string(out)); status != ports.AgentAuthStatusUnknown {
		return status, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, nil
	}

	// kiro-cli appends a plain-text "Profile:" trailer after the JSON object.
	var identity kiroWhoamiIdentity
	if decErr := json.NewDecoder(bytes.NewReader(out)).Decode(&identity); decErr == nil {
		if identity.AccountType != "" || identity.Email != "" || identity.StartURL != "" {
			return ports.AgentAuthStatusAuthorized, nil
		}
	}

	return ports.AgentAuthStatusUnknown, nil
}
