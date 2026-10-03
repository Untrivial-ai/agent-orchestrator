package opencode

import "context"

// ResolveBinary resolves the executable path for native Chat bindings and
// requires the OpenCode 1 command contract before they launch it.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return ResolveBinaryForMajor(ctx, 1)
}
