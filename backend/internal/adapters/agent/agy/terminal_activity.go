package agy

import (
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/terminalui"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const (
	agyFooterMarker          = "? for shortcuts"
	agyPromptMarker          = ">"
	agyTerminalLookbackLines = 20
)

// DetectTerminalActivity recognizes authoritative idle states in Agy's TUI.
//
// Agy's durable activity is hook-driven, but an aborted turn (e.g. user pressing
// Escape during tool execution) causes the CLI to abort in-flight work and return
// to the interactive prompt without emitting its Stop hook.
//
// This capability opts the adapter into the activity observer's stale-active
// reconciliation, which screen-proves idle after hook activity has been quiet
// past the staleness threshold.
func (p *Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	lines := terminalui.PlainTerminalLines(output)
	if len(lines) == 0 {
		return "", false
	}

	// Filter trailing blank lines to locate the active screen bottom.
	nonEmpty := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	if len(nonEmpty) == 0 {
		return "", false
	}

	start := len(nonEmpty) - agyTerminalLookbackLines
	if start < 0 {
		start = 0
	}
	recent := nonEmpty[start:]

	// 1. If in-flight active work (tool execution, thinking, generating) is visible anywhere in the recent window,
	// the session is actively executing a turn.
	for _, l := range recent {
		if isAgyActiveIndicator(strings.ToLower(l)) {
			return domain.ActivityActive, true
		}
	}

	// 2. Locate the Agy footer line near the bottom (within the last 4 non-empty rows).
	footerIdx := -1
	for i := len(recent) - 1; i >= 0 && i >= len(recent)-4; i-- {
		if isAgyFooterLine(recent[i]) {
			footerIdx = i
			break
		}
	}
	if footerIdx < 0 {
		return "", false
	}

	// Any lines rendered below the footer must not be a new prompt (e.g. scrollback).
	for i := footerIdx + 1; i < len(recent); i++ {
		if strings.HasPrefix(recent[i], agyPromptMarker) {
			return domain.ActivityActive, true
		}
	}

	// 2. Locate the prompt marker above the footer (within 3 rows above the footer).
	promptIdx := -1
	for i := footerIdx - 1; i >= 0 && i >= footerIdx-3; i-- {
		if strings.HasPrefix(recent[i], agyPromptMarker) {
			promptIdx = i
			break
		}
	}
	if promptIdx < 0 {
		return "", false
	}

	// 3. Confirm there is no active tool execution or thinking indicator after the prompt.
	for i := promptIdx + 1; i < footerIdx; i++ {
		line := strings.ToLower(recent[i])
		if isAgyActiveIndicator(line) {
			return domain.ActivityActive, true
		}
	}

	// If prompt is followed by footer and no in-flight execution is below it,
	// Agy is sitting idle at the interactive prompt.
	return domain.ActivityIdle, true
}

func isAgyFooterLine(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, agyFooterMarker) || strings.Contains(lower, "esc to cancel")
}

func isAgyActiveIndicator(line string) bool {
	return strings.Contains(line, "(esc to interrupt)") ||
		strings.Contains(line, "(ctrl+c to cancel)") ||
		strings.Contains(line, "thinking...") ||
		strings.Contains(line, "generating...")
}
