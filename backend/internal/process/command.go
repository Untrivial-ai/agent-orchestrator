package process

import (
	"context"
	"os/exec"
)

// Command creates a non-interactive child process. On Windows it suppresses
// transient console windows for CLI tools launched by the desktop daemon.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(resolveExecutable(name), args...)
	configureHidden(cmd)
	return cmd
}

// CommandContext is Command with cancellation support.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, resolveExecutable(name), args...)
	configureHidden(cmd)
	return cmd
}
