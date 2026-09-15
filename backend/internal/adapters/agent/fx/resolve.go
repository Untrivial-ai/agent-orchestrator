package fx

import (
	"context"
	"fmt"
	"runtime"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var fxBinarySpec = binaryutil.BinarySpec{
	Label:         "fx",
	Names:         []string{"fx"},
	UnixHomePaths: [][]string{{".local", "bin", "fx"}},
}

// ResolveFXBinary finds fx on PATH or at ~/.local/bin/fx.
func ResolveFXBinary(ctx context.Context) (string, error) {
	return resolveFXBinary(ctx, runtime.GOOS)
}

func resolveFXBinary(ctx context.Context, goos string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if goos == "windows" {
		return "", fmt.Errorf("fx: native Windows is unsupported; use fx through WSL or follow the manual setup guidance: %w", ports.ErrAgentBinaryNotFound)
	}
	return binaryutil.ResolveBinary(ctx, fxBinarySpec)
}

// ResolveBinary resolves the executable path for readiness checks.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.fxBinary(ctx)
}

func (p *Plugin) fxBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	binary, err := ResolveFXBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
