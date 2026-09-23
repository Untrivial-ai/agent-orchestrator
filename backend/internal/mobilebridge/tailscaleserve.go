package mobilebridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Serve drives the tailnet-only HTTPS proxy that fronts the Connect Mobile
// bridge, so phones reach it over TLS with Tailscale's certificate instead of
// plaintext HTTP. Tailnet-only: this is `serve`, never `funnel`, so nothing is
// exposed publicly.
type Serve struct {
	// Run is the CLI runner; nil means the real tailscale binary.
	Run TailscaleRunner
}

// NewServe returns a Serve backed by the real tailscale CLI.
func NewServe() *Serve { return &Serve{} }

func (s *Serve) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, tailscaleTimeout)
	defer cancel()
	if s.Run != nil {
		return s.Run(ctx, args...)
	}
	return execTailscale(ctx, args...)
}

// Apply points the tailnet's HTTPS :443 proxy at the given local bridge port,
// replacing any previous target. Idempotent, and safe to call on every listener
// start — which is exactly how the caller avoids a proxy left on a dead port.
func (s *Serve) Apply(ctx context.Context, bridgePort int) error {
	target := fmt.Sprintf("http://127.0.0.1:%d", bridgePort)
	if _, err := s.run(ctx, "serve", "--bg", "--https=443", target); err != nil {
		return fmt.Errorf("tailscale serve --https=443 %s: %w", target, err)
	}
	return nil
}

// Clear removes the :443 proxy. Idempotent: tailscale treats turning off an
// unset proxy as success.
func (s *Serve) Clear(ctx context.Context) error {
	if _, err := s.run(ctx, "serve", "--https=443", "off"); err != nil {
		return fmt.Errorf("tailscale serve --https=443 off: %w", err)
	}
	return nil
}

// Target reports the local port :443 currently proxies to, or 0 when unset or
// undeterminable. Callers compare it against the live bridge port to detect a
// stale config left by a crash or an out-of-band edit.
func (s *Serve) Target(ctx context.Context) int {
	out, err := s.run(ctx, "serve", "status", "--json")
	if err != nil {
		return 0
	}
	var parsed struct {
		Web map[string]struct {
			Handlers map[string]struct{ Proxy string } `json:"Handlers"`
		} `json:"Web"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return 0
	}
	// parsed.Web is keyed "hostname:port". A user may also run `tailscale serve`
	// for something unrelated on another port (e.g. --https=8443); skip any site
	// not on :443 so its handler is never mistaken for AO's proxy target — Go
	// map iteration order would otherwise make that pick random.
	for key, site := range parsed.Web {
		if !strings.HasSuffix(key, ":443") {
			continue
		}
		for _, h := range site.Handlers {
			u, err := url.Parse(h.Proxy)
			if err != nil {
				continue
			}
			if port, err := strconv.Atoi(u.Port()); err == nil {
				return port
			}
		}
	}
	return 0
}
