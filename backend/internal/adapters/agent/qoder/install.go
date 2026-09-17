package qoder

import "context"

func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.resolveBinary(ctx)
}

func (p *Plugin) resolveBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()
	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	bin, err := ResolveQoderBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = bin
	return bin, nil
}
