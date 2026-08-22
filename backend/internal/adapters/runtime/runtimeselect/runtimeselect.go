// Package runtimeselect picks the correct runtime backend by platform:
// tmux on Darwin/Linux, conpty (ConPTY) on Windows.
package runtimeselect

import (
	"context"
	"log/slog"
	"runtime"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Runtime is the union interface that both tmux and conpty satisfy.
// It extends ports.Runtime (Create/Destroy/IsAlive) with the additional methods
// the daemon wires directly, including ports.Attacher (Attach) so the terminal
// layer can open a Stream against the selected runtime.
type Runtime interface {
	ports.Runtime // Create, Destroy, IsAlive
	ports.Attacher
	Interrupt(ctx context.Context, handle ports.RuntimeHandle) error
	SendInput(ctx context.Context, handle ports.RuntimeHandle, input string) error
	SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error
	GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error)
}

// Options configures the selected runtime backend. The zero value preserves
// the historical runtime behavior.
type Options struct {
	// ProcessContainment is forwarded to the tmux backend on Unix. Windows uses
	// ConPTY and ignores this option.
	ProcessContainment string
}

// Compile-time assertions: both adapters must implement the union interface.
var _ Runtime = (*tmux.Runtime)(nil)
var _ Runtime = (*conpty.Runtime)(nil)

// New returns the per-platform runtime: tmux on Darwin/Linux, conpty on
// Windows. log is accepted for signature stability with callers but is
// currently unused. runFilePath is this daemon instance's running.json path
// (config.Config.RunFilePath); on Windows it scopes the conpty pty-host
// registry to the same instance, so two AO daemons on one machine with
// different AO_RUN_FILE/AO_DATA_DIR overrides never share one registry — see
// ptyregistry.SetRunFilePath.
func New(_ *slog.Logger, runFilePath string, options ...Options) Runtime {
	if runtime.GOOS != "windows" {
		opts := tmux.Options{}
		if len(options) > 0 {
			opts.ProcessContainment = options[0].ProcessContainment
		}
		return tmux.New(opts)
	}
	return conpty.New(conpty.Options{RunFilePath: runFilePath})
}
