package crush

import (
	"regexp"
	"slices"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var crushTerminalEscape = regexp.MustCompile(`\x1b(?:\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\][^\x07]*(?:\x07|\x1b\\))`)

// Real Crush v0.80.0 captures from tmux show the active composer as:
//
//	> Ready...  or  > Ready?
//	:::
//	:::
//
// and an active turn as:
//
//	> Working!
//	:::
//	:::
//
// Permission prompts are backed by Crush's permissions dialog, whose title is
// "Permission Required" with Allow / Allow for Session / Deny actions.
var crushReadyPlaceholders = []string{
	"ready!",
	"ready...",
	"ready?",
	"ready for instructions",
	"yolo mode!",
}

var crushWorkingPlaceholders = []string{
	"working!",
	"working...",
	"brrrrr...",
	"prrrrrrrr...",
	"processing...",
	"thinking...",
}

// DeriveActivityState maps a Crush hook event onto an AO activity state.
// Currently a no-op since Crush doesn't have full hooks support like Claude Code and Codex.
// The bool is false to indicate no activity signal is available.
//
// TODO(crush): Implement activity state mapping once Crush has native hook support.
// Until then, runtime exit falls back to the reaper.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	// No-op for now since Crush doesn't have full hooks support
	return "", false
}

// ContinuouslyDetectTerminalActivity opts Crush into terminal reconciliation on
// every observer tick. Crush does not emit AO-compatible lifecycle hooks, so its
// current TUI is the only source for idle, active, and waiting-input edges.
func (p *Plugin) ContinuouslyDetectTerminalActivity() bool { return true }

// DetectTerminalActivity recognizes authoritative states in Crush's TUI. The
// newest marker wins so stale prompt or permission text in scrollback cannot
// override the current terminal state.
func (p *Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	lines := crushTerminalLines(output)
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
		switch {
		case crushLineLooksWaitingInput(recent, i):
			return domain.ActivityWaitingInput, true
		case crushLineLooksActive(line):
			return domain.ActivityActive, true
		case crushLineLooksIdle(line):
			return domain.ActivityIdle, true
		}
	}
	return "", false
}

func crushLineLooksWaitingInput(lines []string, index int) bool {
	line := strings.ToLower(lines[index])
	if strings.HasPrefix(strings.TrimSpace(line), "request user input") ||
		strings.HasPrefix(strings.TrimSpace(line), "◆ request user input") ||
		strings.HasPrefix(strings.TrimSpace(line), "◇ request user input") {
		return true
	}
	start := index - 3
	if start < 0 {
		start = 0
	}
	end := min(index+3, len(lines)-1)
	hasPermissionTitle := false
	hasAllow := false
	hasDeny := false
	for _, nearby := range lines[start : end+1] {
		nearby = strings.ToLower(nearby)
		if strings.Contains(nearby, "permission required") {
			hasPermissionTitle = true
		}
		if strings.Contains(nearby, "allow") {
			hasAllow = true
		}
		if strings.Contains(nearby, "deny") {
			hasDeny = true
		}
	}
	return hasPermissionTitle && hasAllow && hasDeny
}

func crushLineLooksActive(line string) bool {
	if strings.Contains(line, "esc to interrupt") || strings.Contains(line, "ctrl+c to interrupt") {
		return true
	}
	return crushPromptLineHasPlaceholder(line, crushWorkingPlaceholders)
}

func crushLineLooksIdle(line string) bool {
	return crushPromptLineHasPlaceholder(line, crushReadyPlaceholders)
}

func crushPromptLineHasPlaceholder(line string, placeholders []string) bool {
	after, ok := strings.CutPrefix(strings.TrimSpace(line), ">")
	if !ok {
		return false
	}
	text := strings.TrimSpace(after)
	return slices.Contains(placeholders, text)
}

func crushTerminalLines(output string) []string {
	plain := crushTerminalEscape.ReplaceAllString(strings.ReplaceAll(output, "\r", "\n"), "")
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
