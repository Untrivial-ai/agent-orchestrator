package codex

import (
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/terminalui"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// DetectTerminalActivity reports idle only when Codex's composer and footer are visible.
func (p *Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	observation := p.InspectTerminalSurface(output)
	if observation.Work == ports.TerminalSurfaceWorkIdle {
		return domain.ActivityIdle, true
	}
	return "", false
}

// InspectTerminalSurface reports Codex work and composer facts separately.
// A visible prompt can contain a draft, and Codex can render that prompt while
// an active turn remains interruptible.
func (p *Plugin) InspectTerminalSurface(output string) ports.TerminalSurfaceObservation {
	observation := ports.TerminalSurfaceObservation{
		Composer: codexComposerState(terminalui.LastPromptComposerState(codexComposerFrame(output), "›")),
	}
	lines := terminalLines(output)
	if len(lines) < 2 {
		return observation
	}
	start := len(lines) - 12
	if start < 0 {
		start = 0
	}
	if codexConfirmationFrame(lines, start) {
		observation.Work = ports.TerminalSurfaceWorkWaitingInput
		observation.Composer = ports.TerminalComposerUnknown
		return observation
	}
	prompt, _ := codexPromptFooter(lines, start)
	if prompt < 0 && terminalui.LastPromptHasBoldMarker(output, "›") {
		// Codex hides its footer in constrained viewports but retains a bold,
		// non-dim current-prompt marker. Plain or dim transcript prompts do not
		// satisfy this fallback, so missing structural evidence still fails closed.
		for i := len(lines) - 1; i >= start; i-- {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "›") {
				prompt = i
				break
			}
		}
	}
	if prompt >= 0 {
		observation.NativeConversationNotStarted = codexInitialComposer(lines, prompt)
		if prompt > start && codexActiveStatusLine(lines[prompt-1]) {
			observation.Work = ports.TerminalSurfaceWorkActive
			observation.NativeConversationNotStarted = false
			return observation
		}
		observation.Work = ports.TerminalSurfaceWorkIdle
		return observation
	}
	return observation
}

func codexInitialComposer(lines []string, currentPrompt int) bool {
	header := false
	prompts := 0
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "OpenAI Codex (v") {
			header = true
		}
		if strings.HasPrefix(line, "›") {
			prompts++
			if i != currentPrompt {
				return false
			}
		}
	}
	return header && prompts == 1
}

func codexConfirmationFrame(lines []string, start int) bool {
	selection := -1
	for i := len(lines) - 1; i >= start; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "›") {
			continue
		}
		// An ordinary composer below a completed picker makes the picker
		// transcript. Only the current, last prompt-shaped row can be selected.
		if !codexNumberedOption(strings.TrimSpace(strings.TrimPrefix(line, "›"))) {
			return false
		}
		selection = i
		break
	}
	if selection < 0 {
		return false
	}
	hint := -1
	for i := selection + 1; i < len(lines); i++ {
		if codexConfirmationHint(lines[i]) {
			hint = i
			break
		}
	}
	if hint < 0 {
		return false
	}
	for i := start; i < hint; i++ {
		if i != selection && codexNumberedOption(strings.TrimSpace(lines[i])) {
			return true
		}
	}
	return false
}

func codexNumberedOption(line string) bool {
	dot := strings.IndexByte(line, '.')
	if dot <= 0 {
		return false
	}
	for _, r := range line[:dot] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func codexPromptFooter(lines []string, start int) (int, int) {
	for footer := len(lines) - 1; footer > start; footer-- {
		if !strings.Contains(lines[footer], " · ") {
			continue
		}
		for prompt := footer - 1; prompt >= start; prompt-- {
			if strings.HasPrefix(strings.TrimSpace(lines[prompt]), "›") {
				return prompt, footer
			}
		}
	}
	return -1, -1
}

func codexActiveStatusLine(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "• ") && strings.Contains(strings.ToLower(line), "esc to interrupt")
}

func codexConfirmationHint(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "press enter to confirm") ||
		strings.Contains(lower, "enter to select") ||
		strings.Contains(lower, "esc to go back")
}

// codexComposerFrame excludes Codex's normal non-dim footer while retaining
// ANSI styling and any wrapped composer rows. Without this provider-owned
// boundary, safe parsing would have to mistake footer chrome for a draft.
func codexComposerFrame(output string) string {
	raw := strings.Split(strings.ReplaceAll(output, "\r", "\n"), "\n")
	start := len(raw) - 16
	if start < 0 {
		start = 0
	}
	for footer := len(raw) - 1; footer >= start; footer-- {
		plainFooter := strings.TrimSpace(terminalui.PlainTerminalText(raw[footer]))
		if !strings.Contains(plainFooter, " · ") {
			continue
		}
		for prompt := footer - 1; prompt >= start; prompt-- {
			plainPrompt := strings.TrimSpace(terminalui.PlainTerminalText(raw[prompt]))
			if strings.HasPrefix(plainPrompt, "›") {
				return strings.Join(raw[prompt:footer], "\n")
			}
		}
	}
	return output
}

func codexComposerState(state terminalui.ComposerState) ports.TerminalComposerState {
	switch state {
	case terminalui.ComposerEmpty:
		return ports.TerminalComposerEmpty
	case terminalui.ComposerDraft:
		return ports.TerminalComposerDraft
	default:
		return ports.TerminalComposerUnknown
	}
}

func terminalLines(output string) []string {
	raw := terminalui.PlainTerminalLines(output)
	lines := raw[:0]
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
