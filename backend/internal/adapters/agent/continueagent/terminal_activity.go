package continueagent

import (
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var continueTerminalEscape = regexp.MustCompile(`\x1b(?:\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\][^\x07]*(?:\x07|\x1b\\))`)

// ContinuouslyDetectTerminalActivity opts Continue into terminal reconciliation
// because its structured question picker can appear without an activity hook.
func (p *Plugin) ContinuouslyDetectTerminalActivity() bool { return true }

// ComposerIsEmpty proves that Continue's current composer is visible without
// draft text. Message delivery uses this narrower terminal fact before writing
// unsolicited coordination into the TUI.
func (p *Plugin) ComposerIsEmpty(output string) bool {
	lines := continueTerminalLines(output)
	for i := len(lines) - 1; i >= 0; i-- {
		if continueIdleComposerAt(lines[i]) {
			return true
		}
		if strings.Contains(lines[i], "❯") {
			return false
		}
	}
	return false
}

// DetectTerminalActivity recognizes authoritative markers in Continue's TUI.
// The newest marker wins so an old picker retained in scrollback cannot keep a
// session waiting once Continue returns to active work.
func (p *Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	lines := continueTerminalLines(output)
	if len(lines) == 0 {
		return "", false
	}
	start := len(lines) - 40
	if start < 0 {
		start = 0
	}
	recent := lines[start:]

	for i := len(recent) - 1; i >= 0; i-- {
		line := strings.ToLower(recent[i])
		if continueIdleComposerAt(recent[i]) {
			return domain.ActivityIdle, true
		}
		if continueQuestionPickerAt(recent, i) {
			return domain.ActivityWaitingInput, true
		}
		if strings.Contains(line, "esc to interrupt") {
			return domain.ActivityActive, true
		}
	}
	return "", false
}

func continueQuestionPickerAt(lines []string, idx int) bool {
	if !strings.Contains(strings.ToLower(lines[idx]), "enter select") {
		return false
	}
	// A completed picker remains in Continue's transcript. An empty composer
	// after this action row proves that the picker is historical, not visible.
	for i := idx + 1; i < len(lines); i++ {
		if continueIdleComposerAt(lines[i]) {
			return false
		}
	}
	start := idx - 8
	if start < 0 {
		start = 0
	}
	for i := start; i <= idx; i++ {
		nearby := strings.ToLower(lines[i])
		if strings.Contains(nearby, "ask question(") || strings.Contains(nearby, "? ") {
			return true
		}
	}
	return false
}

func continueIdleComposerAt(line string) bool {
	return strings.Trim(line, "│ ") == "❯"
}

func continueTerminalLines(output string) []string {
	plain := continueTerminalEscape.ReplaceAllString(strings.ReplaceAll(output, "\r", "\n"), "")
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
