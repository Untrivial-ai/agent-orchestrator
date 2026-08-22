// Package runtimeselect picks the correct runtime backend by platform:
// tmux on Darwin/Linux, conpty (ConPTY) on Windows.
package runtimeselect

import (
	"context"
	"log/slog"
	"runtime"

	cloudruntime "github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/cloud"
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
func New(_ *slog.Logger, runFilePath string) (Runtime, error) {
	if remote, configured, err := cloudruntime.FromEnvironment(); configured {
		if err != nil {
			return nil, err
		}
		return remote, nil
	}
	if runtime.GOOS != "windows" {
		return tmux.New(tmux.Options{}), nil
	}
	return conpty.New(conpty.Options{RunFilePath: runFilePath}), nil
}
