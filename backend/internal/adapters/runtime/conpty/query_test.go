package conpty

import (
	"bytes"
	"testing"
)

func TestTerminalQueryResponderAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   []string
	}{
		{"cursor position", "\x1b[6n", []string{"\x1b[1;1R"}},
		{"device status", "\x1b[5n", []string{"\x1b[0n"}},
		{"device attributes", "\x1b[c", []string{"\x1b[?62;22c"}},
		{"secondary device attributes", "\x1b[>c", []string{"\x1b[>41;330;0c"}},
		{"kitty keyboard flags", "\x1b[?u", []string{"\x1b[?0u"}},
		{"startup burst", "\x1b[c\x1b[6n\x1b[?u", []string{"\x1b[?62;22c", "\x1b[1;1R", "\x1b[?0u"}},
		{"query after ordinary output", "hello\x1b[1;32mworld\x1b[6n", []string{"\x1b[1;1R"}},
		{"plain text only", "just some output\n", nil},
		{"unanswered sequences", "\x1b]10;?\x1b]11;?\x1b]4;0;?\x1b[38;5;9m\x1b[?25l", nil},
		{"osc 4 set is not a query", "\x1b]4;0;#ff0000\x07", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r terminalQueryResponder
			got := r.Feed([]byte(tc.output))
			assertReplies(t, got, tc.want)
		})
	}
}

func TestTerminalQueryResponderCarriesSplitSequences(t *testing.T) {
	var r terminalQueryResponder
	if got := r.Feed([]byte("\x1b[6")); len(got) != 0 {
		t.Fatalf("partial query produced replies: %q", got)
	}
	if got := r.Feed([]byte("n")); len(got) != 1 || string(got[0]) != "\x1b[1;1R" {
		t.Fatalf("split query replies = %q, want the cursor report", got)
	}

	// A split that never becomes a query must not swallow the next one.
	if got := r.Feed([]byte("\x1b[0")); len(got) != 0 {
		t.Fatalf("partial non-query produced replies: %q", got)
	}
	got := r.Feed([]byte("m\x1b[6n"))
	assertReplies(t, got, []string{"\x1b[1;1R"})
}

func TestTerminalQueryResponderDoesNotDuplicateBufferedQuery(t *testing.T) {
	var r terminalQueryResponder
	// Two queries in one read where the second is incomplete: the first must be
	// answered exactly once, on this read.
	got := r.Feed([]byte("\x1b[6n\x1b[5"))
	assertReplies(t, got, []string{"\x1b[1;1R"})
	got = r.Feed([]byte("n"))
	assertReplies(t, got, []string{"\x1b[0n"})
}

func TestTerminalQueryResponderAnswersRepeatedQueries(t *testing.T) {
	var r terminalQueryResponder
	got := r.Feed([]byte("\x1b[6n\x1b[6n"))
	assertReplies(t, got, []string{"\x1b[1;1R", "\x1b[1;1R"})
}

func TestTerminalQueryResponderFeedsEmptyChunk(t *testing.T) {
	var r terminalQueryResponder
	if got := r.Feed(nil); len(got) != 0 {
		t.Fatalf("empty chunk produced replies: %q", got)
	}
	if len(r.pending) != 0 {
		t.Fatalf("empty chunk left a pending fragment: %q", r.pending)
	}
}

func assertReplies(t *testing.T, got [][]byte, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("replies = %q, want %q", got, want)
	}
	for i := range want {
		if !bytes.Equal(got[i], []byte(want[i])) {
			t.Fatalf("reply %d = %q, want %q", i, got[i], want[i])
		}
	}
}
