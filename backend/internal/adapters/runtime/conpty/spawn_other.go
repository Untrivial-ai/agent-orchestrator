//go:build !windows && !darwin && !linux

// spawn_other.go - stub for platforms without a detached PTY host.
package conpty

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// defaultSpawnHost is a stub on unsupported platforms. Tests inject their own
// spawner; this keeps the package buildable on Linux.
func defaultSpawnHost(_ context.Context, _, _ string, _ []string, _ map[string]string, _ ports.TerminalSize) (string, int, error) {
	return "", 0, errors.New("conpty spawn: unsupported on this OS")
}
