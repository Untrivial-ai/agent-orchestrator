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

// kiroAuthProbeTimeout bounds `whoami` so a broken install cannot stall the
// callers that gate real work on this answer.
const kiroAuthProbeTimeout = 3 * time.Second

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

func kiroWhoamiAuthStatus(ctx context.Context, binary string) (ports.AgentAuthStatus, error) {
	if binary == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	// Kiro documents `whoami` as its authentication-status command. Keep the
	// probe bounded so catalog refresh cannot hang on a broken CLI install.
	probeCtx, cancel := context.WithTimeout(ctx, kiroAuthProbeTimeout)
	defer cancel()
	out, runErr := authprobe.CmdRunner(probeCtx, binary, "whoami", "--format", "json")
	if ctx.Err() != nil {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if status, ok := kiroStatusFromWhoami(out); ok {
		return status, nil
	}
	if runErr != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	// Unrecognized shape: defer to the shared prose classifier rather than
	// guessing. Unknown is not a denial — callers must not read it as "logged out".
	return authprobe.StatusFromText(string(out)), nil
}

// kiroStatusFromWhoami classifies `kiro-cli whoami --format json`.
//
// The generic keyword classifier cannot read this output: signed out prints
// `{"account":null}` and signed in prints an account object, neither of which
// contains any of the phrases it looks for, so both classified as unknown — a
// probe that could never distinguish the two states. Signed-in output is also
// followed by a plain-text "Profile:" block, so the payload as a whole is not
// valid JSON; decoding only the leading value skips that trailer.
func kiroStatusFromWhoami(out []byte) (ports.AgentAuthStatus, bool) {
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&payload); err != nil {
		return "", false
	}
	if raw, ok := payload["account"]; ok {
		if isJSONNull(raw) {
			return ports.AgentAuthStatusUnauthorized, true
		}
		return ports.AgentAuthStatusAuthorized, true
	}
	// Older/other builds report the identity inline instead of under "account".
	for _, key := range []string{"email", "accountType", "startUrl"} {
		if raw, ok := payload[key]; ok && !isJSONNull(raw) && !isEmptyJSONString(raw) {
			return ports.AgentAuthStatusAuthorized, true
		}
	}
	return "", false
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func isEmptyJSONString(raw json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return strings.TrimSpace(s) == ""
}
