//go:build !windows && !darwin && !linux

package conpty

import (
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// newConPTY is a stub on platforms without a detached PTY host. The serve
// engine and tests use a fake ptyConn; this keeps the package buildable on
// Linux.
func newConPTY(cwd, shellCmd string, shellArgs []string, size ports.TerminalSize) (ptyConn, error) {
	return nil, errors.New("conpty: unsupported on this OS")
}
