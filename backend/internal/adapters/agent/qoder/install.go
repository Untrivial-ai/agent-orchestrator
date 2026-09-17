package qoder

import "context"

// ResolveBinary returns the path to the qoder executable for this plugin,
// resolving it once and reusing the cached path on later calls. It returns a
// wrapped ports.ErrAgentBinaryNotFound when qoder is not installed.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.resolveBinary(ctx)
}

func (p *Plugin) resolveBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()
	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	resolve := p.resolveBinaryPath
	if resolve == nil {
		resolve = ResolveQoderBinary
	}
	bin, err := resolve(ctx)
	if err != nil {
		return "", err
	}
	probe := p.probeMinimumVersion
	if probe == nil {
		probe = ProbeMinimumVersion
	}
	if err := probe(ctx, bin); err != nil {
		return "", err
	}
	p.resolvedBinary = bin
	return bin, nil
}
