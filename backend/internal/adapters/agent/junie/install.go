package junie

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
)

var junieBinarySpec = binaryutil.BinarySpec{
	Label:         "junie",
	Names:         []string{"junie"},
	WinNames:      []string{"junie.bat", "junie.exe", "junie.cmd", "junie"},
	UnixPaths:     []string{"/usr/local/bin/junie", "/opt/homebrew/bin/junie"},
	UnixHomePaths: [][]string{{".local", "bin", "junie"}},
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinHome, Parts: []string{".local", "bin", "junie.bat"}},
		{Base: binaryutil.WinHome, Parts: []string{".local", "bin", "junie.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".local", "bin", "junie.cmd"}},
	},
}

// ResolveJunieBinary locates Junie on PATH or in an official user install
// location.
func ResolveJunieBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, junieBinarySpec)
}

// ResolveBinary resolves the executable path for this plugin.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.junieBinary(ctx)
}

func (p *Plugin) junieBinary(ctx context.Context) (string, error) {
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
	binary, err := ResolveJunieBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
