package terminalui

import (
	"strings"
	"testing"
)

func TestPlainTerminalTextStripsControlsAndPreservesVisibleRows(t *testing.T) {
	input := "\x1b[2mfirst\x1b[0m\r\n\x1b]8;;https://example.com\x1b\\second\x1b]8;;\x1b\\"
	if got, want := PlainTerminalText(input), "first\n\nsecond"; got != want {
		t.Fatalf("PlainTerminalText() = %q, want %q", got, want)
	}
}

func TestLastPromptIsEmptyOrDimPlaceholder(t *testing.T) {
	tests := []struct {
		name   string
		output string
		marker string
		want   bool
	}{
		{name: "blank claude", output: "status\n\x1b[39m❯\u00a0", marker: "❯", want: true},
		{name: "dim claude placeholder", output: "\x1b[39m❯\u00a0\x1b[2mclean up this code\x1b[0m", marker: "❯", want: true},
		{name: "typed claude draft", output: "\x1b[39m❯\u00a0do not submit this", marker: "❯", want: false},
		{name: "claude permission option", output: "permission\n❯ 1. Yes\n  2. No", marker: "❯", want: false},
		{name: "dim codex placeholder", output: "› \x1b[2mExplain this codebase\x1b[0m\n\n\x1b[2mmodel · workspace\x1b[0m", marker: "›", want: true},
		{name: "plain codex placeholder fails closed", output: "› Explain this codebase\nmodel · workspace", marker: "›", want: false},
		{name: "wrapped human draft fails closed", output: "❯\nhuman draft\nfooter", marker: "❯", want: false},
		{name: "leading blank rows in human draft fail closed", output: "❯\n\nhuman draft", marker: "❯", want: false},
		{name: "historical prompt is outside lookback", output: "❯\n1\n2\n3\n4\n5\n6\n7\n8\n9", marker: "❯", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LastPromptIsEmptyOrDimPlaceholder(tt.output, tt.marker); got != tt.want {
				t.Fatalf("LastPromptIsEmptyOrDimPlaceholder() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLastPromptComposerState(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   ComposerState
	}{
		{name: "missing prompt", output: "provider starting", want: ComposerUnknown},
		{name: "empty", output: "\x1b[39m❯\u00a0", want: ComposerEmpty},
		{name: "dim placeholder", output: "❯ \x1b[2mAsk a question\x1b[0m", want: ComposerEmpty},
		{name: "draft", output: "❯ keep this draft", want: ComposerDraft},
		{name: "wrapped draft", output: "❯\nkeep this draft", want: ComposerDraft},
		{
			// A rule below the prompt ends the composer's content region;
			// provider status chrome below that rule is not human input.
			name:   "rule below prompt closes the footer-free scan",
			output: "❯ \x1b[7m \x1b[0m\n" + strings.Repeat("─", 48) + "\n  glm-5.3 low 40.1k [Rate limited]",
			want:   ComposerEmpty,
		},
		{
			name:   "prompt-line draft above a rule is still a draft",
			output: "❯ keep this draft\n" + strings.Repeat("─", 48) + "\n⏵⏵ auto mode on",
			want:   ComposerDraft,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LastPromptComposerState(tt.output, "❯"); got != tt.want {
				t.Fatalf("LastPromptComposerState() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLastPromptHasBoldMarker(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "current provider prompt", output: "\x1b[1m›\x1b[0m\n", want: true},
		{name: "dim transcript prompt", output: "\x1b[1;2m› \x1b[0mPrior request\n", want: false},
		{name: "plain transcript text", output: "›\n", want: false},
		{name: "colored transcript marker", output: "\x1b[38;5;1m›\x1b[0m\n", want: false},
		{name: "bold colored current prompt", output: "\x1b[1;38;5;1m›\x1b[0m\n", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LastPromptHasBoldMarker(tt.output, "›"); got != tt.want {
				t.Fatalf("LastPromptHasBoldMarker() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLastBorderedPromptIsEmptyOrDimPlaceholder(t *testing.T) {
	rule := "\x1b[38;5;244m" + strings.Repeat("─", 48) + "\x1b[39m"
	footer := "\x1b[38;5;220mUpdate available!\x1b[39m\n\x1b[38;5;211m⏵⏵ bypass permissions on\x1b[39m"
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "empty above colored footer", output: rule + "\n\x1b[39m❯\u00a0\x1b[7m \x1b[0m\n" + rule + "\n" + footer, want: true},
		{name: "typed draft", output: rule + "\n❯ do not submit this\n" + rule + "\n" + footer, want: false},
		{name: "wrapped draft", output: rule + "\n❯\nwrapped human draft\n" + rule + "\n" + footer, want: false},
		{name: "permission menu", output: rule + "\n❯ 1. Yes\n  2. No\n" + rule + "\n" + footer, want: false},
		{name: "rule draft", output: rule + "\n❯\n" + strings.Repeat("─", 48) + "\n" + rule + "\n" + footer, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LastBorderedPromptIsEmptyOrDimPlaceholder(tt.output, "❯"); got != tt.want {
				t.Fatalf("LastBorderedPromptIsEmptyOrDimPlaceholder() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLastBorderedPromptComposerState(t *testing.T) {
	rule := strings.Repeat("─", 48)
	tests := []struct {
		name   string
		output string
		want   ComposerState
	}{
		{name: "missing lower border", output: rule + "\n❯ draft", want: ComposerUnknown},
		{name: "empty", output: rule + "\n❯\n" + rule + "\nstatus", want: ComposerEmpty},
		{name: "dim placeholder", output: rule + "\n❯ \x1b[2mAsk a question\x1b[0m\n" + rule, want: ComposerEmpty},
		{name: "draft", output: rule + "\n❯ keep this draft\n" + rule, want: ComposerDraft},
		{name: "wrapped draft", output: rule + "\n❯\nkeep this draft\n" + rule, want: ComposerDraft},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LastBorderedPromptComposerState(tt.output, "❯"); got != tt.want {
				t.Fatalf("LastBorderedPromptComposerState() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLastBorderedPromptComposerStateIgnoresProviderChromeLabels(t *testing.T) {
	rule := strings.Repeat("─", 48)
	output := rule + "\n❯  Claude Code\n" + rule + "\n  glm-5.3 low [Rate limited]"
	if got := LastBorderedPromptComposerState(output, "❯"); got != ComposerDraft {
		t.Fatalf("provider label without chrome list = %v, want draft (unchanged default)", got)
	}
	if got := LastBorderedPromptComposerState(output, "❯", "Claude Code"); got != ComposerEmpty {
		t.Fatalf("provider label with chrome list = %v, want empty", got)
	}
	if got := LastBorderedPromptComposerState(output, "❯", "OpenAI Codex"); got != ComposerDraft {
		t.Fatalf("unrelated chrome label = %v, want draft", got)
	}
	if got := LastPromptComposerState(output, "❯", "Claude Code"); got != ComposerEmpty {
		t.Fatalf("footer-free fallback with chrome list = %v, want empty (rule closes the region)", got)
	}
}

func TestPromptChromeLabelsAreExemptOnContinuationRows(t *testing.T) {
	rule := strings.Repeat("─", 48)
	// The provider can paint its label on its own row inside the bordered
	// composer, below the marker row. That row is chrome too, not a draft.
	bordered := rule + "\n❯ \x1b[7m \x1b[0m\n Claude Code\n" + rule + "\n⏵⏵ auto mode on"
	if got := LastBorderedPromptComposerState(bordered, "❯"); got != ComposerDraft {
		t.Fatalf("bordered label row without chrome list = %v, want draft (unchanged default)", got)
	}
	if got := LastBorderedPromptComposerState(bordered, "❯", "Claude Code"); got != ComposerEmpty {
		t.Fatalf("bordered label row with chrome list = %v, want empty", got)
	}
	// Same for the footer-free fallback: a label row below the prompt is not
	// human input.
	footerFree := "❯ \x1b[7m \x1b[0m\nClaude Code\n"
	if got := LastPromptComposerState(footerFree, "❯"); got != ComposerDraft {
		t.Fatalf("footer-free label row without chrome list = %v, want draft (unchanged default)", got)
	}
	if got := LastPromptComposerState(footerFree, "❯", "Claude Code"); got != ComposerEmpty {
		t.Fatalf("footer-free label row with chrome list = %v, want empty", got)
	}
	// A label followed by human text on the same row is still a draft.
	mixed := rule + "\n❯ Claude Code review please\n" + rule + "\n⏵⏵ auto mode on"
	if got := LastBorderedPromptComposerState(mixed, "❯", "Claude Code"); got != ComposerDraft {
		t.Fatalf("label-prefixed human text = %v, want draft", got)
	}
}
