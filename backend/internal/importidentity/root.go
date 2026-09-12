// Package importidentity compares provider state roots without requiring the
// original transcript or its date/project subdirectories to still exist.
package importidentity

import (
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Matches compares provider roots, resolving aliases without requiring the transcript.
func Matches(provider domain.AgentHarness, root, transcript string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(transcript) {
		return false
	}
	var depth int
	switch provider {
	case domain.HarnessClaudeCode:
		depth = 3
	case domain.HarnessCodex:
		depth = 5
	default:
		return false
	}
	source := filepath.Clean(transcript)
	container := ""
	for n := 0; n < depth; n++ {
		container = filepath.Base(source)
		parent := filepath.Dir(source)
		if parent == source {
			return false
		}
		source = parent
	}
	if provider == domain.HarnessClaudeCode && container != "projects" {
		return false
	}
	if provider == domain.HarnessCodex && container != "sessions" && container != "archived_sessions" {
		return false
	}
	// Resolve the root only: history files and intermediate folders can disappear
	// while the provider root alias still proves the durable record's identity.
	if resolved, err := filepath.EvalSymlinks(source); err == nil {
		source = resolved
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Clean(source) == filepath.Clean(root)
}
