package aider

import (
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var (
	aiderTerminalEscape = regexp.MustCompile(`\x1b(?:\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\][^\x07]*(?:\x07|\x1b\\))`)
	aiderPromptLine     = regexp.MustCompile(`^(?:(?:[a-z][a-z0-9-]*)(?: multi)?|multi)?>(?:[ \t](.*))?$`)
)

// ContinuouslyDetectTerminalActivity opts Aider into terminal reconciliation.
// Aider has a completion notification command but no prompt-submit hook, so AO
// observes its submitted prompt while the non-streaming response is in flight.
func (p *Plugin) ContinuouslyDetectTerminalActivity() bool { return true }

// ComposerIsEmpty proves that Aider's newest prompt is visible without a
// submitted message. Activity reconciliation leaves this state to the
// completion hook, while message delivery uses it as an input-readiness guard.
func (p *Plugin) ComposerIsEmpty(output string) bool {
	lines := aiderTerminalLines(output)
	for i := len(lines) - 1; i >= 0; i-- {
		match := aiderPromptLine.FindStringSubmatch(lines[i])
		if match != nil {
			return len(match) < 2 || strings.TrimSpace(match[1]) == ""
		}
	}
	return false
}

// EmptyComposerProvesWaitingInputReady opts Aider into delivery from its
// completion-notification state. Aider emits waiting_input after completion,
// and its empty prompt proves that no native decision boundary is active.
func (p *Plugin) EmptyComposerProvesWaitingInputReady() bool { return true }

// DetectTerminalActivity reports active when Aider's newest prompt contains a
// submitted message. A bare prompt emits no signal: startup must remain idle,
// while the notification hook owns the completed-turn waiting-input state.
func (p *Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	lines := aiderTerminalLines(output)
	start := len(lines) - 40
	if start < 0 {
		start = 0
	}
	for i := len(lines) - 1; i >= start; i-- {
		match := aiderPromptLine.FindStringSubmatch(lines[i])
		if match == nil {
			continue
		}
		if len(match) < 2 || strings.TrimSpace(match[1]) == "" {
			return "", false
		}
		return domain.ActivityActive, true
	}
	return "", false
}

func aiderTerminalLines(output string) []string {
	plain := aiderTerminalEscape.ReplaceAllString(strings.ReplaceAll(output, "\r", "\n"), "")
	raw := strings.Split(plain, "\n")
	lines := raw[:0]
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
