package conpty

import (
	"strings"
	"testing"
)

func TestOSCTitleFilter(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
	}{
		{"window and icon title", "before\x1b]0;✳ session title\aafter", "before\x1b]0;\aafter"},
		{"icon title", "before\x1b]1;✳ title\aafter", "before\x1b]1;\aafter"},
		{"window title", "before\x1b]2;✳ title\aafter", "before\x1b]2;\aafter"},
		{"C1 window and icon title", "before\x9d0;✳ session title\aafter", "before\x9d0;\aafter"},
		{"C1 icon title", "before\x9d1;✳ title\aafter", "before\x9d1;\aafter"},
		{"C1 window title", "before\x9d2;✳ title\aafter", "before\x9d2;\aafter"},
		{"C1 seven bit ST", "before\x9d0;✳ title\x1b\\after", "before\x9d0;\x1b\\after"},
		{"C1 eight bit ST", "before\x9d0;✳ title\x9cafter", "before\x9d0;\x9cafter"},
		{"C1 cancel", "\x9d0;✳ title\x18after", "\x9d0;\x18after"},
		{"C1 substitute", "\x9d0;✳ title\x1aafter", "\x9d0;\x1aafter"},
		{"C1 next escape cancels title", "\x9d0;title\x1b[31mafter", "\x9d0;\x1b[31mafter"},
		{"mixed introducers", "\x9d0;first\a\x1b]2;✳ second\aafter", "\x9d0;\a\x1b]2;\aafter"},
		{"C1 unterminated title", "before\x9d0;✳ title", "before\x9d0;"},
		{"seven bit ST", "before\x1b]0;✳ title\x1b\\after", "before\x1b]0;\x1b\\after"},
		{"eight bit ST", "before\x1b]0;✳ title\x9cafter", "before\x1b]0;\x9cafter"},
		{"UTF-8 two three four bytes", "\x1b]0;Ü✳🜀 title\aafter", "\x1b]0;\aafter"},
		{"cancel", "\x1b]0;✳ title\x18after", "\x1b]0;\x18after"},
		{"substitute", "\x1b]0;✳ title\x1aafter", "\x1b]0;\x1aafter"},
		{"invalid UTF-8 then BEL", "\x1b]0;\xe2\aafter", "\x1b]0;\aafter"},
		{"invalid UTF-8 then ESC", "\x1b]0;\xe2\x1b[2Jafter", "\x1b]0;\x1b[2Jafter"},
		{"next escape cancels title", "\x1b]0;title\x1b[31mafter", "\x1b]0;\x1b[31mafter"},
		{"consecutive titles", "\x1b]0;first\a\x1b]2;✳ second\aafter", "\x1b]0;\a\x1b]2;\aafter"},
		{"unterminated title", "before\x1b]0;✳ title", "before\x1b]0;"},
		{"bounded unterminated title", "before\x1b]0;" + strings.Repeat("✳", 1000), "before\x1b]0;"},
		{"hyperlink unchanged", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"},
		{"palette unchanged", "\x1b]4;1;rgb:ff/00/00\a", "\x1b]4;1;rgb:ff/00/00\a"},
		{"non-title 10 unchanged", "\x1b]10;rgb:ff/00/00\a", "\x1b]10;rgb:ff/00/00\a"},
		{"C1 hyperlink unchanged", "\x9d8;;https://example.com\x1b\\link\x9d8;;\x1b\\", "\x9d8;;https://example.com\x1b\\link\x9d8;;\x1b\\"},
		{"C1 palette unchanged", "\x9d4;1;rgb:ff/00/00\a", "\x9d4;1;rgb:ff/00/00\a"},
		{"C1 non-title 10 unchanged", "\x9d10;rgb:ff/00/00\a", "\x9d10;rgb:ff/00/00\a"},
		{"C1 invalid prefix unchanged", "\x9d]0;text\a", "\x9d]0;text\a"},
		{"invalid prefix unchanged", "\x1b]0Xtext", "\x1b]0Xtext"},
		{"styles and cursor unchanged", "\x1b[31m❯ real draft\x1b[0m\x1b[2A\x1b[3G", "\x1b[31m❯ real draft\x1b[0m\x1b[2A\x1b[3G"},
		{"ground Unicode unchanged", "Ü✳🜀 works", "Ü✳🜀 works"},
		{"UTF-8 C1 continuation is not OSC", "Ý0;draft\a☝1;draft\a𝄞2;draft\a", "Ý0;draft\a☝1;draft\a𝄞2;draft\a"},
		{"UTF-8 then C1 title", "Ý\x9d0;✳ title\aafter", "Ý\x9d0;\aafter"},
		{"invalid UTF-8 resets before C1", "\xe2x\x9d0;✳ title\aafter", "\xe2x\x9d0;\aafter"},
		{"consecutive ESC", "\x1b\x1b]0;✳ title\aafter", "\x1b\x1b]0;\aafter"},
		{"ESC before C1 title", "\x1b\x9d0;✳ title\aafter", "\x1b\x9d0;\aafter"},
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
