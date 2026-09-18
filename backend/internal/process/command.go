package process

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
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
	ApplyCommandEnvironment(ctx, cmd)
	return cmd
}

// ApplyCommandEnvironment copies operation-scoped overrides into a prepared child.
func ApplyCommandEnvironment(ctx context.Context, cmd *exec.Cmd) {
	if env, ok := ctx.Value(commandEnvironmentKey{}).(map[string]string); ok {
		values := cmd.Environ()
		for key, value := range env {
			next := values[:0]
			for _, entry := range values {
				name, _, _ := strings.Cut(entry, "=")
				if name != key && (runtime.GOOS != "windows" || !strings.EqualFold(name, key)) {
					next = append(next, entry)
				}
			}
			next = append(next, key+"="+value)
			values = next
		}
		cmd.Env = values
	}
}

// WithCommandEnvironment scopes child environment overrides to one operation.
// CommandContext copies these into the child; the daemon environment is unchanged.
func WithCommandEnvironment(ctx context.Context, env map[string]string) context.Context {
	copyEnv := make(map[string]string, len(env))
	for key, value := range env {
		copyEnv[key] = value
	}
	return context.WithValue(ctx, commandEnvironmentKey{}, copyEnv)
}

type commandEnvironmentKey struct{}
