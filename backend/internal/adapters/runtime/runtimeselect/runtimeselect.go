// Package runtimeselect picks the correct runtime backend by platform. Windows
// uses ConPTY, while macOS and Linux route legacy handles to tmux while creating
// new sessions on a detached native PTY host. Other platforms default to tmux.
package runtimeselect

import (
	"context"
	"log/slog"
	"runtime"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Runtime is the union interface that every selected runtime satisfies.
// It extends ports.Runtime (Create/Destroy/IsAlive) with the additional methods
// the daemon wires directly, including ports.Attacher (Attach) so the terminal
// layer can open a Stream against the selected runtime.
type Runtime interface {
	ports.Runtime // Create, Destroy, IsAlive
	ports.FencedRuntimeProber
	ports.Attacher
	Interrupt(ctx context.Context, handle ports.RuntimeHandle) error
	SendInput(ctx context.Context, handle ports.RuntimeHandle, input string) error
	SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error
	GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error)
}

// Compile-time assertions: both concrete adapters must implement the union
// interface.
var _ Runtime = (*tmux.Runtime)(nil)
var _ Runtime = (*conpty.Runtime)(nil)

// New returns the platform runtime. runFilePath is this daemon instance's
// running.json path and scopes detached-host recovery to that AO instance.
func New(log *slog.Logger, runFilePath string) Runtime {
	switch runtime.GOOS {
	case "windows":
		return conpty.New(conpty.Options{RunFilePath: runFilePath})
	case "darwin":
		return newHybridRuntime(
			tmux.New(tmux.Options{}),
			conpty.New(conpty.Options{RunFilePath: runFilePath}),
			log,
			"macOS",
		)
	case "linux":
		return newHybridRuntime(
			tmux.New(tmux.Options{}),
			conpty.New(conpty.Options{RunFilePath: runFilePath}),
			log,
			"Linux",
		)
	default:
		return tmux.New(tmux.Options{})
	}
}
