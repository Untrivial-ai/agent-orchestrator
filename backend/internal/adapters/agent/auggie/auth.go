package auggie

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus checks device-wide session evidence using the scoped resolver.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor resolves the session supplied to this Auggie invocation.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return auggieAuthStatus(ctx, check, authutil.Dependencies{})
}

func auggieAuthStatus(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	d.Getenv = func(key string) string {
		if value, exists := check.Env[key]; exists {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(getenv(key))
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	// Native precedence is flag, environment, then the stored session. Invalid
	// JSON/shape falls through, but a usable expired selected token does not.
	input := ""
	for i := 0; i < len(check.Args); i++ {
		if check.Args[i] == "--" {
			break
		}
		if strings.HasPrefix(check.Args[i], "--augment-session-json=") {
			input = strings.TrimPrefix(check.Args[i], "--augment-session-json=")
		}
		if check.Args[i] == "--augment-session-json" && i+1 < len(check.Args) {
			i++
			input = check.Args[i]
		}
	}
	input = strings.TrimSpace(input)
	if input != "" && !strings.HasPrefix(input, "{") {
		path := input
		if !filepath.IsAbs(path) {
			path = ""
			if filepath.IsAbs(check.WorkingDir) {
				path = filepath.Join(check.WorkingDir, input)
			}
		}
		input = ""
		if path != "" {
			if data, err := authutil.ReadFile(ctx, d, path); err == nil {
				input = string(data)
			}
		}
	}
	for _, data := range []string{input, d.Getenv("AUGMENT_SESSION_AUTH")} {
		if status := auggieSessionStatus([]byte(data), now()); status != ports.AgentAuthStatusUnknown {
			return status, nil
		}
	}
	home := d.Getenv("HOME")
	if d.GOOS == "windows" || (d.GOOS == "" && runtime.GOOS == "windows") {
		home = d.Getenv("USERPROFILE")
	}
	if filepath.IsAbs(home) {
		if data, err := authutil.ReadFile(ctx, d, filepath.Join(home, ".augment", "session.json")); err == nil {
			return auggieSessionStatus(data, now()), nil
		}
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

// The official session schema is shared by browser login and service accounts.
// Scopes are a string array, not an OAuth space-delimited string. Browser
// sessions use ["email"]; service-account downloads use ["read", "write"].
func auggieSessionStatus(data []byte, now time.Time) ports.AgentAuthStatus {
	if len(data) == 0 || len(data) > authutil.MaxFileSize {
		return ports.AgentAuthStatusUnknown
	}
	var session struct {
		AccessToken string   `json:"accessToken"`
		TenantURL   string   `json:"tenantURL"`
		Scopes      []string `json:"scopes"`
	}
	if json.Unmarshal(data, &session) != nil || strings.TrimSpace(session.AccessToken) == "" || session.Scopes == nil {
		return ports.AgentAuthStatusUnknown
	}
	endpoint, err := url.Parse(session.TenantURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil {
		return ports.AgentAuthStatusUnknown
	}
	// Opaque service-account tokens have no expiry. If the access token is a
	// JWT, its numeric exp can disprove usability, never establish authorization.
	parts := strings.Split(session.AccessToken, ".")
	if len(parts) == 3 {
		header, err := base64.RawURLEncoding.DecodeString(parts[0])
		var metadata struct {
			Algorithm string `json:"alg"`
		}
		if err != nil || json.Unmarshal(header, &metadata) != nil || metadata.Algorithm == "" || metadata.Algorithm == "none" {
			return ports.AgentAuthStatusUnknown
		}
		if signature, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil || len(signature) == 0 {
			return ports.AgentAuthStatusUnknown
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return ports.AgentAuthStatusUnknown
		}
		var claims struct {
			Exp json.RawMessage `json:"exp"`
		}
		if json.Unmarshal(payload, &claims) != nil {
			return ports.AgentAuthStatusUnknown
		}
		if len(claims.Exp) > 0 {
			var seconds *float64
			if json.Unmarshal(claims.Exp, &seconds) != nil || seconds == nil {
				return ports.AgentAuthStatusUnknown
			}
			// Auggie's session store has no refreshToken field/refresh path.
			// MCP OAuth refresh metadata is a separate credential schema.
			if *seconds <= float64(now.Unix())+float64(now.Nanosecond())/1e9 {
				return ports.AgentAuthStatusUnauthorized
			}
			return ports.AgentAuthStatusConfigured
		}
	}
	return ports.AgentAuthStatusConfigured
}
