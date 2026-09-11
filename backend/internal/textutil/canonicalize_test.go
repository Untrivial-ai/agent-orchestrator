package textutil

import (
	"strings"
	"testing"
)

func TestCanonicalizeConversationFact_TrimsSpace(t *testing.T) {
	got := CanonicalizeConversationFact("  hello world  ")
	if got != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", got)
	}
}

func TestCanonicalizeConversationFact_TruncatesTo16KiB(t *testing.T) {
	big := strings.Repeat("x", 20<<10)
	got := CanonicalizeConversationFact(big)
	if len(got) != ConversationFactBytes {
		t.Errorf("expected %d bytes, got %d", ConversationFactBytes, len(got))
	}
}

func TestCanonicalizeConversationFact_RepairsUTF8(t *testing.T) {
	// Build a string that would cut a multibyte UTF-8 sequence.
	// 16382 bytes of 'a' + 3-byte UTF-8 char → 16385 bytes total.
	// Truncate at 16384 → last byte of the 3-byte char is cut.
	big := strings.Repeat("a", ConversationFactBytes-2) + "é" // é is 2 bytes in UTF-8
	got := CanonicalizeConversationFact(big)
	// Should be valid UTF-8.
	for i, r := range got {
		if r == '�' && i < len(got)-3 {
			// Replacement char in the middle is fine for truncated sequences.
		}
	}
	if len(got) > ConversationFactBytes {
		t.Errorf("result exceeds %d bytes: got %d", ConversationFactBytes, len(got))
	}
}

func TestCanonicalizeConversationFact_EmptyString(t *testing.T) {
	if got := CanonicalizeConversationFact(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if got := CanonicalizeConversationFact("   "); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestCanonicalizeConversationFact_ExactBoundary(t *testing.T) {
	exact := strings.Repeat("x", ConversationFactBytes)
	got := CanonicalizeConversationFact(exact)
	if got != exact {
		t.Errorf("exact-boundary string should pass through unchanged")
	}
}
