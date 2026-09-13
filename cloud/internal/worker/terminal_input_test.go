package worker

import (
	"strings"
	"testing"
)

func TestEncodeTerminalInputSingleLine(t *testing.T) {
	got := string(EncodeTerminalInput("fix the failing test"))
	if got != "fix the failing test\r" {
		t.Fatalf("single-line input encoded as %q", got)
	}
}

func TestEncodeTerminalInputMultiLineUsesBracketedPaste(t *testing.T) {
	text := "CI failed:\nline one\nline two"
	got := string(EncodeTerminalInput(text))
	if !strings.HasPrefix(got, "\x1b[200~") {
		t.Fatalf("missing bracketed paste start: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[201~\r") {
		t.Fatalf("missing bracketed paste end + submit: %q", got)
	}
	if !strings.Contains(got, text) {
		t.Fatalf("text not preserved verbatim inside paste: %q", got)
	}
}

func TestEncodeTerminalInputCarriageReturnAlsoWraps(t *testing.T) {
	got := string(EncodeTerminalInput("a\rb"))
	if !strings.HasPrefix(got, "\x1b[200~") {
		t.Fatalf("embedded \\r must wrap: %q", got)
	}
}
