package agy

import (
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var agyTerminalEscape = regexp.MustCompile(`\x1b(?:\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\][^\x07]*(?:\x07|\x1b\\))`)

// DetectTerminalActivity parses the Agy CLI terminal output to determine if the
// agent is currently idle, allowing the observer to reconcile sessions whose
// turn was aborted (e.g. via Escape) without emitting a stop hook.
//
// The detector scans the bottommost lines of the terminal capture in two passes:
//  1. Locate the footer ("? for shortcuts") and check for active execution
//     markers anywhere in the candidate frame. Lines beginning with ">" are
//     user-typed transcript entries and are excluded from the active-marker
//     scan so transcript text cannot spoof the check.
//  2. Require the composer prompt (">") to be structurally adjacent to the
//     footer—within maxPromptFooterGap lines—so an older transcript prompt
//     separated by output content does not satisfy the idle proof.
func (p *Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	lines := agyTerminalLines(output)
	if len(lines) == 0 {
		return "", false
	}

	// We only look at the bottom-most lines (the newest frame) to prevent old
	// transcript lines or user input like "> explain why it says thinking..."
	// from spoofing the detection.
	const searchWindow = 10
	searchDepth := searchWindow
	if len(lines) < searchDepth {
		searchDepth = len(lines)
	}
	searchStart := len(lines) - searchDepth

	// Pass 1: scan the full candidate frame to find the footer and detect
	// active execution chrome. Active markers must veto idle regardless of
	// whether they appear above or below the footer. Lines starting with ">"
	// are user-typed transcript entries whose text must not trigger the
	// active-marker check (e.g. "> explain why it says (esc to interrupt)").
	footerIdx := -1
	hasActiveChrome := false
	for i := len(lines) - 1; i >= searchStart; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, ">") {
			if strings.Contains(line, "(esc to interrupt)") ||
				strings.Contains(line, "(ctrl+c to cancel)") ||
				strings.Contains(line, "thinking...") {
				hasActiveChrome = true
			}
		}
		if footerIdx < 0 && strings.Contains(line, "? for shortcuts") {
			footerIdx = i
		}
	}

	if footerIdx < 0 || hasActiveChrome {
		return "", false
	}

	// Pass 2: require the composer prompt to sit directly above the footer.
	// In Agy's idle frame the ">" composer is on the line immediately before
	// "? for shortcuts", or separated by at most one blank cursor line.
	// Older transcript prompts further up must not satisfy this structural
	// check.
	const maxPromptFooterGap = 2
	promptBound := footerIdx - maxPromptFooterGap
	if promptBound < searchStart {
		promptBound = searchStart
	}
	if promptBound < 0 {
		promptBound = 0
	}
	for j := footerIdx - 1; j >= promptBound; j-- {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), ">") {
			return domain.ActivityIdle, true
		}
	}

	// Footer present but no adjacent prompt; incomplete or partial frame.
	return "", false
}

func agyTerminalLines(output string) []string {
	plain := agyTerminalEscape.ReplaceAllString(output, "")
	clean := strings.ReplaceAll(strings.ReplaceAll(plain, "\r\n", "\n"), "\r", "\n")
	clean = strings.TrimRight(clean, "\r\n ")
	if strings.TrimSpace(clean) == "" {
		return nil
	}
	return strings.Split(clean, "\n")
}
