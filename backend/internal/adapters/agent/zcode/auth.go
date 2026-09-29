package zcode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
//
// The credential that actually governs model access is zcode's OAuth token in
// ~/.zcode/v2/credentials.json (the apiKey strings in cli/config.json stay
// populated even after the OAuth session expires). The access token is a JWT;
// its exp claim is decoded locally — no network call, no signature
// verification (expiry is a sufficient local signal; the daemon rejects
// stale tokens on first real use anyway).
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return zcodeCredentialsAuthStatus(), nil
}

func zcodeCredentialsAuthStatus() ports.AgentAuthStatus {
	zcodeHome, err := zcodeConfigDir()
	if err != nil || zcodeHome == "" {
		return ports.AgentAuthStatusUnknown
	}
	data, err := os.ReadFile(filepath.Join(zcodeHome, "v2", "credentials.json")) //nolint:gosec // fixed user-credentials path
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnauthorized
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown
	}

	var creds struct {
		OAuthZaiAccessToken string `json:"oauth:zai:access_token"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return ports.AgentAuthStatusUnknown
	}
	token := strings.TrimSpace(creds.OAuthZaiAccessToken)
	if token == "" {
		return ports.AgentAuthStatusUnauthorized
	}
	exp, ok := jwtExpiry(token)
	if !ok {
		return ports.AgentAuthStatusUnknown
	}
	if time.Now().After(time.Unix(exp, 0)) {
		return ports.AgentAuthStatusUnauthorized
	}
	return ports.AgentAuthStatusAuthorized
}

// jwtExpiry decodes the unverified exp claim of a JWT. It returns ok=false
// for malformed tokens rather than guessing.
func jwtExpiry(token string) (int64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return 0, false
	}
	return claims.Exp, true
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
