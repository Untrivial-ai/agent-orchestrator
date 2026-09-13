package conpty

import (
	"strings"
	"testing"
)

func TestOSCTitleFilter(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
	}{
		{"window and icon title", "before\x1b]0;✳ session title\aafter", "beforeafter"},
		{"icon title", "before\x1b]1;✳ title\aafter", "beforeafter"},
		{"window title", "before\x1b]2;✳ title\aafter", "beforeafter"},
		{"seven bit ST", "before\x1b]0;✳ title\x1b\\after", "before\x1b\\after"},
		{"eight bit ST", "before\x1b]0;✳ title\x9cafter", "beforeafter"},
		{"UTF-8 two three four bytes", "\x1b]0;Ü✳🜀 title\aafter", "after"},
		{"cancel", "\x1b]0;✳ title\x18after", "after"},
		{"substitute", "\x1b]0;✳ title\x1aafter", "after"},
		{"invalid UTF-8 then BEL", "\x1b]0;\xe2\aafter", "after"},
		{"invalid UTF-8 then ESC", "\x1b]0;\xe2\x1b[2Jafter", "\x1b[2Jafter"},
		{"next escape cancels title", "\x1b]0;title\x1b[31mafter", "\x1b[31mafter"},
		{"consecutive titles", "\x1b]0;first\a\x1b]2;✳ second\aafter", "after"},
		{"unterminated title", "before\x1b]0;✳ title", "before"},
		{"bounded unterminated title", "before\x1b]0;" + strings.Repeat("✳", 1000), "before"},
		{"hyperlink unchanged", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"},
		{"palette unchanged", "\x1b]4;1;rgb:ff/00/00\a", "\x1b]4;1;rgb:ff/00/00\a"},
		{"non-title 10 unchanged", "\x1b]10;rgb:ff/00/00\a", "\x1b]10;rgb:ff/00/00\a"},
		{"invalid prefix unchanged", "\x1b]0Xtext", "\x1b]0Xtext"},
		{"styles and cursor unchanged", "\x1b[31m❯ real draft\x1b[0m\x1b[2A\x1b[3G", "\x1b[31m❯ real draft\x1b[0m\x1b[2A\x1b[3G"},
		{"ground Unicode unchanged", "Ü✳🜀 works", "Ü✳🜀 works"},
		{"consecutive ESC", "\x1b\x1b]0;✳ title\aafter", "\x1bafter"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// PTY reads may split anywhere, including inside an escape or rune.
			for split := 0; split <= len(tt.input); split++ {
				var filter oscTitleFilter
				got := string(filter.filter([]byte(tt.input[:split]))) + string(filter.filter([]byte(tt.input[split:])))
				if got != tt.want {
					t.Fatalf("split %d: got %q, want %q", split, got, tt.want)
				}
			}
			var filter oscTitleFilter
			var got strings.Builder
			for _, b := range []byte(tt.input) {
				got.Write(filter.filter([]byte{b}))
				if len(filter.prefix) > 3 {
					t.Fatalf("unbounded buffered prefix: %d bytes", len(filter.prefix))
				}
			}
			if got.String() != tt.want {
				t.Fatalf("byte at a time: got %q, want %q", got.String(), tt.want)
			}
		})
	}
}
