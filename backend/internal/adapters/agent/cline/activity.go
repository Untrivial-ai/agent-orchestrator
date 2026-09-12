package cline

import (
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var clineTerminalEscape = regexp.MustCompile(`\x1b(?:\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\][^\x07]*(?:\x07|\x1b\\))`)

// ContinuouslyDetectTerminalActivity opts Cline into terminal reconciliation
// on every observer tick. Completion hooks provide the primary transition, but
// the idle composer is the fallback when Cline omits or loses a callback.
func (p *Plugin) ContinuouslyDetectTerminalActivity() bool { return true }

// DetectTerminalActivity recognizes authoritative current-state markers in
// Cline's TUI. The newest marker wins so retained transcript text cannot
// override the current composer or generation indicator.
func (p *Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	lines := clineTerminalLines(output)
	if len(lines) == 0 {
		return "", false
	}
	start := len(lines) - 30
	if start < 0 {
		start = 0
	}
	recent := lines[start:]

	for i := len(recent) - 1; i >= 0; i-- {
		line := strings.ToLower(recent[i])
		if strings.Contains(line, "thinking... (esc to cancel)") {
			return domain.ActivityActive, true
		}
		if clineEmptyComposer(recent[i]) && clineModeFooterAfter(recent, i) {
			return domain.ActivityIdle, true
		}
	}
	return "", false
}

func clineEmptyComposer(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "❯") {
		return false
	}
	prompt := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "❯")))
	return prompt == "ask anything..." || prompt == "plan something..."
}

func clineModeFooterAfter(lines []string, composer int) bool {
	for _, line := range lines[composer+1:] {
		line = strings.ToLower(line)
		if strings.Contains(line, "plan") && strings.Contains(line, "act") && strings.Contains(line, "(tab)") {
			return true
		}
	}
	return false
}

func clineTerminalLines(output string) []string {
	plain := clineTerminalEscape.ReplaceAllString(strings.ReplaceAll(output, "\r", "\n"), "")
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
