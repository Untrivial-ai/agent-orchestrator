package domain

import (
	"strings"
	"testing"
)

func TestSanitizeControlChars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text unchanged", in: "hello world", want: "hello world"},
		{name: "keeps newline tab carriage return", in: "a\nb\tc\rd", want: "a\nb\tc\rd"},
		{name: "strips ansi escape byte leaving harmless residue", in: "before\x1b[2Jafter", want: "before[2Jafter"},
		{name: "strips nul and bell", in: "x\x00y\az", want: "xyz"},
		{name: "strips osc sequence bytes", in: "\x1b]0;title\a", want: "]0;title"},
		{name: "empty stays empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeControlChars(tt.in); got != tt.want {
				t.Fatalf("SanitizeControlChars(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncateTextWithLimit(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{name: "within limit untouched", in: "hello", maxLen: 10, want: "hello"},
		{name: "exact limit untouched", in: "hello", maxLen: 5, want: "hello"},
		{name: "exceeds limit truncated with notice", in: "hello world", maxLen: 5, want: "hello\n\n... (truncated)"},
		{name: "zero or negative maxLen returns as is", in: "hello", maxLen: 0, want: "hello"},
		{name: "unicode rune boundary preserved", in: "héllo world", maxLen: 3, want: "hé\n\n... (truncated)"},                 // 'é' is 2 bytes (bytes 1-2). maxLen=3 cuts between 'é' and 'l' (byte 3).
		{name: "unicode 4-byte emoji boundary preserved", in: "fix 🚀 immediately", maxLen: 6, want: "fix \n\n... (truncated)"}, // 🚀 is 4 bytes at indices 4..7; maxLen 6 lands inside 🚀 and backs up to 4.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TruncateTextWithLimit(tt.in, tt.maxLen); got != tt.want {
				t.Fatalf("TruncateTextWithLimit(%q, %d) = %q, want %q", tt.in, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestSanitizeReviewBody(t *testing.T) {
	in := "Fix issue\x1b[2J now: \x00done"
	want := "Fix issue[2J now: done"
	if got := SanitizeReviewBody(in); got != want {
		t.Fatalf("SanitizeReviewBody(%q) = %q, want %q", in, got, want)
	}

	huge := strings.Repeat("a", ReviewNudgeBodyLimit+100)
	got := SanitizeReviewBody(huge)
	if len(got) <= ReviewNudgeBodyLimit {
		t.Fatalf("expected truncated string with suffix, len=%d", len(got))
	}
	if !strings.HasSuffix(got, "\n\n... (truncated)") {
		t.Fatalf("expected suffix '\n\n... (truncated)', got %q", got[len(got)-30:])
	}
}

func TestValidateReviewBody(t *testing.T) {
	if err := ValidateReviewBody("valid review body"); err != nil {
		t.Fatalf("expected valid review body to pass, got %v", err)
	}
	if err := ValidateReviewBody(""); err == nil {
		t.Fatal("expected empty body to fail")
	}
	if err := ValidateReviewBody("   \t\n  "); err == nil {
		t.Fatal("expected whitespace-only body to fail")
	}
	if err := ValidateReviewBody(string([]byte{0xff, 0xfe, 0xfd})); err == nil {
		t.Fatal("expected invalid UTF-8 body to fail")
	}
	huge := strings.Repeat("x", MaxReviewSubmitBodySize+1)
	if err := ValidateReviewBody(huge); err == nil {
		t.Fatal("expected oversized body to fail")
	}
}

func TestValidateReviewContent(t *testing.T) {
	if err := ValidateReviewContent("valid optional content"); err != nil {
		t.Fatalf("expected valid content to pass, got %v", err)
	}
	if err := ValidateReviewContent(string([]byte{0xff, 0xfe, 0xfd})); err == nil {
		t.Fatal("expected invalid UTF-8 content to fail")
	}
	huge := strings.Repeat("x", MaxReviewSubmitBodySize+1)
	if err := ValidateReviewContent(huge); err == nil {
		t.Fatal("expected oversized content to fail")
	}
}
