package chat

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSourceContainsExcerptSelection(t *testing.T) {
	tests := []struct {
		name, source, selection string
		want                    bool
	}{
		{"emphasis", "Use **bold** text here.", "Use bold text", true},
		{"link", "Open [the docs](https://example.com) now.", "the docs now", true},
		{"escaped punctuation", `Type \* literally.`, "Type * literally", true},
		{"encoded quotes", "Say &quot;hello&quot; now.", `Say "hello" now`, true},
		{"literal code entity", "Use `&quot;hello&quot;` now.", "&quot;hello&quot;", true},
		{"escaped raw HTML", "Use <b>bold</b> text.", "<b>bold</b> text", true},
		{"block raw HTML", "<div>\nhello\n</div>", "<div>\nhello\n</div>", true},
		{"ordinary quotes", `He said "hello" and it's fine.`, `"hello" and it's`, true},
		{"inline boundary", "First **important** and *useful* point.", "important and useful", true},
		{"line boundary", "First **important** line.\nNext line.", "line.\nNext", true},
		{"unrelated text", "Use **bold** text here.", "totally unrelated", false},
		{"link target", "Open [the docs](https://example.com) now.", "example.com", false},
		{"image alt", "![alt text](https://example.com/a.png)", "alt text", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := domain.ConversationMessage{Role: domain.MessageRoleAssistant, Text: tt.source}
			if got := sourceContainsExcerptSelection(source, tt.selection); got != tt.want {
				t.Fatalf("sourceContainsExcerptSelection(%q, %q) = %v, want %v", tt.source, tt.selection, got, tt.want)
			}
		})
	}
}
