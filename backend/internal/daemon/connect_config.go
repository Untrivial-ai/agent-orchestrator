package daemon

import (
	"os"
	"strings"
)

// connectConfig is the deployment-selected configuration for the web UI
// bridge (see docs/adr/0005-web-ui-on-the-lan-listener.md): the LAN listener's bind host and strict-port
// behavior, and whether trusted-tailnet-identity auth is enabled. Every field
// defaults to preserving today's upstream behavior — this is opt-in, off by
// default.
type connectConfig struct {
	// BindHost defaults to "0.0.0.0" (upstream behavior unchanged). A
	// deployment behind a TLS proxy (Caddy, nginx, tailscale serve, a tunnel)
	// sets AO_CONNECT_BIND_HOST=127.0.0.1.
	BindHost string
	// StrictPort defaults to false (upstream ephemeral-port fallback
	// unchanged). AO_CONNECT_STRICT_PORT=1 fails loudly on a port conflict
	// instead of silently drifting to an ephemeral port.
	StrictPort bool
	// TrustTailscaleIdentity defaults to false. AO_CONNECT_TRUST_TAILSCALE_IDENTITY=1
	// enables the Tailscale-User-Login credential — refused at startup unless
	// BindHost is also loopback (the hard invariant in ADR-0005 R7).
	TrustTailscaleIdentity bool
	// AllowedLogins is the identity allowlist, parsed from the comma-separated
	// AO_CONNECT_ALLOWED_LOGINS. Empty means deny everyone — this must never
	// default to "any tailnet user is trusted".
	AllowedLogins map[string]bool
}

// loadConnectConfig reads the AO_CONNECT_* environment variables. Called once
// at daemon boot, mirroring how config.Load reads AO_* variables elsewhere.
func loadConnectConfig() connectConfig {
	cfg := connectConfig{
		BindHost:               strings.TrimSpace(os.Getenv("AO_CONNECT_BIND_HOST")),
		StrictPort:             isTruthyEnv("AO_CONNECT_STRICT_PORT"),
		TrustTailscaleIdentity: isTruthyEnv("AO_CONNECT_TRUST_TAILSCALE_IDENTITY"),
		AllowedLogins:          map[string]bool{},
	}
	if cfg.BindHost == "" {
		cfg.BindHost = "0.0.0.0"
	}
	for _, login := range strings.Split(os.Getenv("AO_CONNECT_ALLOWED_LOGINS"), ",") {
		login = strings.TrimSpace(login)
		if login != "" {
			cfg.AllowedLogins[login] = true
		}
	}
	return cfg
}

// isTruthyEnv reports whether the named environment variable is set to a
// truthy value ("1", "true", "yes", case-insensitive). Matches the informal
// convention other AO_* boolean env vars in this codebase use.
func isTruthyEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
