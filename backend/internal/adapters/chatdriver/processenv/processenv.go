// Package processenv builds child-process environments shared by Chat drivers.
package processenv

import (
	"os"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// Merge overlays session-specific values on the daemon environment and returns
// the KEY=VALUE form expected by os/exec. Sorting makes launches deterministic
// enough to inspect and compare in tests and process diagnostics.
func Merge(overlay map[string]string) []string {
	return aoprocess.WorkerEnvironment(os.Environ(), overlay)
}
