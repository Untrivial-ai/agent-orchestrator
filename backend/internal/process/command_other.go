//go:build !windows

package process

import (
	"context"
	"os/exec"
)

func configureHidden(_ *exec.Cmd) {}

func commandContextForPath(ctx context.Context, path string, args ...string) *exec.Cmd {
	return CommandContext(ctx, path, args...)
}
