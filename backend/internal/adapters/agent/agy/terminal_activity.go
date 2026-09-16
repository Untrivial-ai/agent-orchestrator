package agy

import (
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// DetectTerminalActivity parses the Agy CLI terminal output to determine if the
// agent is currently idle, allowing the observer to reconcile sessions whose
// turn was aborted (e.g. via Escape) without emitting a stop hook.
func (p *Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	lines := strings.Split(strings.TrimRight(output, "\r\n "), "\n")
	if len(lines) == 0 {
		return "", false
	}

	// We only look at the bottom-most lines (the newest frame) to prevent old
	// transcript lines or user input like "> explain why it says thinking..."
	// from spoofing the detection.
	searchDepth := 10
	if len(lines) < searchDepth {
		searchDepth = len(lines)
	}

	for i := len(lines) - 1; i >= len(lines)-searchDepth; i-- {
		line := strings.TrimSpace(lines[i])

		// If we encounter an active execution marker at the bottom before finding
		// a footer, the agent is actively working. We fail closed (return no signal)
		// rather than returning Active, to respect the hook-driven staleAfter grace period.
		if strings.Contains(line, "(esc to interrupt)") ||
			strings.Contains(line, "(ctrl+c to cancel)") ||
			strings.Contains(line, "thinking...") {
			return "", false
		}

		// If we find the footer, check if this frame has a prompt.
		if strings.Contains(line, "? for shortcuts") {
			for j := i; j >= len(lines)-searchDepth && j >= 0; j-- {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), ">") {
					return domain.ActivityIdle, true
				}
			}
			// Found footer but no prompt within depth; incomplete frame.
			return "", false
		}
	}

	return "", false
}
