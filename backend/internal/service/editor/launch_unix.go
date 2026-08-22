//go:build !windows

package editor

import (
	"os/exec"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

func launchCommand(name string, args ...string) *exec.Cmd {
	return aoprocess.Command(name, args...)
}
