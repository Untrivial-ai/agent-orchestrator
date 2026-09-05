package process

import (
	"context"
	"os/exec"
)

// Command creates a non-interactive child process. On Windows it suppresses
// transient console windows for CLI tools launched by the desktop daemon.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	configureHidden(cmd)
	return cmd
}

// CommandContext is Command with cancellation support.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configureHidden(cmd)
	return cmd
}

// CommandContextForPath is CommandContext for an already-resolved executable
// path. Windows command shims are routed through cmd.exe so .cmd and .bat
// files can be probed with the same argv semantics as native executables.
func CommandContextForPath(ctx context.Context, path string, args ...string) *exec.Cmd {
	return commandContextForPath(ctx, path, args...)
}
