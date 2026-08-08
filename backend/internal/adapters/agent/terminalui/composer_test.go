package terminalui

import (
	"strings"
	"testing"
)

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
