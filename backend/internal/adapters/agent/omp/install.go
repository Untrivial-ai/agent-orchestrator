package omp

import "context"

// ResolveBinary resolves the executable path for the plugin.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.ompBinary(ctx)
}
