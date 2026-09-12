package mobilebridge

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// tailscaleTimeout bounds every CLI call. The Connect Mobile status endpoint is
// polled by the modal, so a wedged tailscaled must never stall it.
const tailscaleTimeout = 3 * time.Second

// TailscaleRunner runs the tailscale CLI and returns its stdout. Injected so
// tests never invoke the real binary.
type TailscaleRunner func(ctx context.Context, args ...string) ([]byte, error)

// execTailscale is the production TailscaleRunner.
func execTailscale(ctx context.Context, args ...string) ([]byte, error) {
	return aoprocess.CommandContext(ctx, "tailscale", args...).Output()
}

// TailscaleInfo is what the local daemon can tell us about this node.
type TailscaleInfo struct {
	// Name is the MagicDNS name with no trailing dot, or "" when unavailable.
	Name string
	// CertsEnabled reports whether the tailnet can issue HTTPS certificates,
	// which `tailscale serve --https` requires.
	CertsEnabled bool
}

// QueryTailscale reports this node's MagicDNS name and certificate
// availability. Every failure — missing binary, non-zero exit, malformed
// output — yields the zero value; callers treat an empty Name as
// "secure pairing unavailable" rather than as an error to surface.
func QueryTailscale(ctx context.Context) TailscaleInfo {
	return queryTailscale(ctx, execTailscale)
}

func queryTailscale(ctx context.Context, run TailscaleRunner) TailscaleInfo {
	ctx, cancel := context.WithTimeout(ctx, tailscaleTimeout)
	defer cancel()
	out, err := run(ctx, "status", "--json")
	if err != nil {
		return TailscaleInfo{}
	}
	var parsed struct {
		Self        *struct{ DNSName string } `json:"Self"`
		CertDomains []string                  `json:"CertDomains"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil || parsed.Self == nil {
		return TailscaleInfo{}
	}
	name := strings.TrimSuffix(parsed.Self.DNSName, ".")
	if name == "" {
		return TailscaleInfo{}
	}
	return TailscaleInfo{Name: name, CertsEnabled: len(parsed.CertDomains) > 0}
}
