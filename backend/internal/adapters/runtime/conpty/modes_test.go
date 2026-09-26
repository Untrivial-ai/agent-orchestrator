package conpty

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func restoreAfter(t *testing.T, chunks ...string) string {
	t.Helper()
	m := newModeTracker()
	for _, c := range chunks {
		if _, err := m.Write([]byte(c)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	return string(m.Restore(nil))
}

// restoreOver is restoreAfter with the last chunk treated as the ring replay
// the attacher is about to receive: only what that chunk cannot re-establish
// comes back.
func restoreOver(t *testing.T, chunks ...string) string {
	t.Helper()
	m := newModeTracker()
	for _, c := range chunks {
		if _, err := m.Write([]byte(c)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	return string(m.Restore([]byte(chunks[len(chunks)-1])))
}

func TestModeTracker_DefaultStateRestoresNothing(t *testing.T) {
	if got := restoreAfter(t, "plain text\r\n\x1b[31mred\x1b[0m\n"); got != "" {
		t.Fatalf("Restore = %q, want empty", got)
	}
	if got := newModeTracker().Restore(nil); got != nil {
		t.Fatalf("Restore on a fresh tracker = %v, want nil", got)
	}
}

// The handshake Qwen Code 0.23 prints at startup (captured on the #5039
// reproduction): alternate screen, SGR any-motion mouse, bracketed paste.
func TestModeTracker_FullScreenHandshake(t *testing.T) {
	got := restoreAfter(t, "\x1b[?2004h", "banner\n", "\x1b[?1049h\x1b[?25l", "frame\n", "\x1b[?1003h\x1b[?1006h")
	// Alternate buffer first, then by mode number.
	want := "\x1b[?1049h\x1b[?25l\x1b[?1003h\x1b[?1006h\x1b[?2004h"
	if got != want {
		t.Fatalf("Restore = %q, want %q", got, want)
	}
}

func TestModeTracker_LeavingTheAltScreenClearsIt(t *testing.T) {
	got := restoreAfter(t, "\x1b[?1049h\x1b[?1003h\x1b[?1006h", "\x1b[?1003l\x1b[?1006l\x1b[?1049l")
	if got != "" {
		t.Fatalf("Restore after exit = %q, want empty", got)
	}
}

// xterm.js keeps ONE mouse protocol and ONE encoding: a later set replaces the
// earlier one, and resetting any member turns the slot off even if a different
// member was the one set. A per-mode set would replay 1003h here and put the
// late attacher in a state no live client is in.
func TestModeTracker_MouseSlotsMirrorXterm(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[?1000h\x1b[?1003h", "\x1b[?1003h"},
		{"\x1b[?1000h\x1b[?1003h\x1b[?1000l", ""},
		{"\x1b[?1006h\x1b[?1016h", "\x1b[?1016h"},
		{"\x1b[?1002h\x1b[?1006h\x1b[?1016l", "\x1b[?1002h"},
		{"\x1b[?9h", "\x1b[?9h"},
		// 1005 and 1015 are no-ops in xterm.js: neither sets nor clears anything.
		{"\x1b[?1005h", ""},
		{"\x1b[?1002h\x1b[?1006h\x1b[?1015l", "\x1b[?1002h\x1b[?1006h"},
	}
	for _, c := range cases {
		if got := restoreAfter(t, c.in); got != c.want {
			t.Errorf("after %q: Restore = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestModeTracker_AltScreenReplaysTheNumberUsed(t *testing.T) {
	for _, n := range []int{47, 1047, 1049} {
		in := fmt.Sprintf("\x1b[?%dh", n)
		if got := restoreAfter(t, in); got != in {
			t.Errorf("after %q: Restore = %q, want %q", in, got, in)
		}
		// Any member of the group leaves the alternate buffer.
		if got := restoreAfter(t, in, "\x1b[?47l"); got != "" {
			t.Errorf("after %q then 47l: Restore = %q, want empty", in, got)
		}
	}
}

func TestModeTracker_MultipleParamsInOneSequence(t *testing.T) {
	got := restoreAfter(t, "\x1b[?1049;1002;1006h")
	want := "\x1b[?1049h\x1b[?1002h\x1b[?1006h"
	if got != want {
		t.Fatalf("Restore = %q, want %q", got, want)
	}
}

func TestModeTracker_DefaultOnModesReplayOnlyWhenReset(t *testing.T) {
	if got := restoreAfter(t, "\x1b[?7h\x1b[?25h"); got != "" {
		t.Fatalf("setting default-on modes: Restore = %q, want empty", got)
	}
	got := restoreAfter(t, "\x1b[?7l\x1b[?25l")
	want := "\x1b[?7l\x1b[?25l"
	if got != want {
		t.Fatalf("Restore = %q, want %q", got, want)
	}
}

// PTY reads split sequences anywhere, including inside the parameter bytes.
func TestModeTracker_SequenceSplitAcrossWrites(t *testing.T) {
	full := "\x1b[?1049h\x1b[?1003h\x1b[?1006h"
	want := restoreAfter(t, full)
	for cut := 1; cut < len(full); cut++ {
		if got := restoreAfter(t, full[:cut], full[cut:]); got != want {
			t.Errorf("split at %d: Restore = %q, want %q", cut, got, want)
		}
	}
}

// OSC / DCS payloads are skipped up to their terminator, so a title that
// happens to contain "[?1049h" text is not mistaken for a mode change, and a
// mode set right after ST is still seen.
func TestModeTracker_StringPayloadsAreSkipped(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b]0;[?1049h\x07", ""},
		{"\x1b]0;title\x1b\\\x1b[?2004h", "\x1b[?2004h"},
		{"\x1bPq[?1003h\x1b\\", ""},
		{"\x1b_G[?47h\x1b\\\x1b[?1049h", "\x1b[?1049h"},
		// BEL is payload inside DCS / APC, not a terminator; an ESC ends every
		// string kind, so a 7-bit CSI after the BEL is still a real mode set.
		// (BEL vs ST would only show through 8-bit C1 introducers, which the
		// tracker does not read.)
		{"\x1bPq\x07\x1b[?1049h\x1b\\", "\x1b[?1049h"},
		{"\x1b_G\x07[?2004h\x1b\\\x1b[?47h", "\x1b[?47h"},
	}
	for _, c := range cases {
		if got := restoreAfter(t, c.in); got != c.want {
			t.Errorf("after %q: Restore = %q, want %q", c.in, got, c.want)
		}
	}
}

// DECSTR (CSI ! p) restores keyboard, paste, focus, wrap and cursor defaults
// but leaves the mouse and the buffer, exactly as xterm.js softReset does;
// RIS (ESC c) resets everything.
func TestModeTracker_SoftAndFullReset(t *testing.T) {
	all := "\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?1h\x1b[?66h\x1b[?1004h\x1b[?2004h\x1b[?7l\x1b[?25l"
	got := restoreAfter(t, all, "\x1b[!p")
	want := "\x1b[?1049h\x1b[?1003h\x1b[?1006h"
	if got != want {
		t.Fatalf("after DECSTR: Restore = %q, want %q", got, want)
	}
	// RIS leaves a hidden cursor hidden, as xterm.js does.
	if got := restoreAfter(t, all, "\x1bc"); got != "\x1b[?25l" {
		t.Fatalf("after RIS: Restore = %q, want only the hidden cursor", got)
	}
	if got := restoreAfter(t, "\x1b[?1049h\x1b[?1003h\x1b[?2004h", "\x1bc"); got != "" {
		t.Fatalf("after RIS with the cursor visible: Restore = %q, want empty", got)
	}
}

// ESC = and ESC > drive the same application-keypad flag as ?66.
func TestModeTracker_KeypadEscapesShareTheFlag(t *testing.T) {
	if got := restoreAfter(t, "\x1b="); got != "\x1b[?66h" {
		t.Fatalf("after ESC =: Restore = %q, want ?66h", got)
	}
	if got := restoreAfter(t, "\x1b[?66h", "\x1b>"); got != "" {
		t.Fatalf("after ?66h then ESC >: Restore = %q, want empty", got)
	}
}

// A real terminal executes C0 controls and ignores DEL inside a sequence
// without ending it, and aborts only on CAN or SUB. A line discipline or a
// program interleaving a control byte must not hide the mode from the tracker.
func TestModeTracker_ControlBytesInsideSequences(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b\r[?1049h", "\x1b[?1049h"},
		{"\x1b[?10\x0049h", "\x1b[?1049h"},
		{"\x1b[?1049\x7fh", "\x1b[?1049h"},
		{"\x1b[?1049\x18h", ""},
		{"\x1b\x1a[?1049h", ""},
		{"\x1b]0;title\x18\x1b[?2004h", "\x1b[?2004h"},
	}
	for _, c := range cases {
		if got := restoreAfter(t, c.in); got != c.want {
			t.Errorf("after %q: Restore = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestModeTracker_IgnoresAnsiModesAndUnknownPrivateModes(t *testing.T) {
	got := restoreAfter(t, "\x1b[4h\x1b[20h\x1b[?12h\x1b[?2026h\x1b[?1048h\x1b[?45h")
	if got != "" {
		t.Fatalf("Restore = %q, want empty", got)
	}
}

// A parameter run that never ends (garbage, or a hostile program) is dropped
// at maxParams and parsing resumes at the next escape.
func TestModeTracker_OversizedParametersAreDropped(t *testing.T) {
	junk := "\x1b[?" + strings.Repeat("1;", maxParams) + "h"
	got := restoreAfter(t, junk, "\x1b[?2004h")
	if got != "\x1b[?2004h" {
		t.Fatalf("Restore = %q, want only the mode set after the junk", got)
	}
}

// Restore must produce sequences the tracker itself reads back to the same
// state, so a client fed prefix+replay and one fed every byte agree.
func TestModeTracker_RestoreRoundTrips(t *testing.T) {
	inputs := []string{
		"\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?2004h\x1b[?25l",
		"\x1b[?47h\x1b[?1000h\x1b[?1h\x1b[?7l",
		"\x1b[?2004h",
	}
	for _, in := range inputs {
		first := restoreAfter(t, in)
		if again := restoreAfter(t, first); again != first {
			t.Errorf("%q: Restore(Restore) = %q, want %q", in, again, first)
		}
		if !bytes.Equal([]byte(first), []byte(restoreAfter(t, first, first))) {
			t.Errorf("%q: applying the restore twice changed the state", in)
		}
	}
}

// The prefix carries only what the replay can no longer say. A mode whose last
// change is still inside the replay is left to the replay, so an early
// attacher gets the bare ring and the program's own alternate-screen switch
// keeps the output before it in the client's normal buffer.
func TestModeTracker_PrefixCarriesOnlyWhatTheReplayLacks(t *testing.T) {
	handshake := "\x1b[?1049h\x1b[?25l\x1b[?1003h\x1b[?1006h\x1b[?2004h"
	cases := []struct {
		name, trimmed, replay, want string
	}{
		{"nothing trimmed", "", "$ prompt\n" + handshake + "frame\n", ""},
		{"everything trimmed", handshake + "frame\n", "frame\n", "\x1b[?1049h\x1b[?25l\x1b[?1003h\x1b[?1006h\x1b[?2004h"},
		{"paste toggled inside the replay", handshake, "\x1b[?2004l pasted \x1b[?2004h", "\x1b[?1049h\x1b[?25l\x1b[?1003h\x1b[?1006h"},
		{"replay re-hides the cursor", handshake, "\x1b[?25h\x1b[?25l", "\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?2004h"},
		{"program left the alt screen inside the replay", handshake, "\x1b[?1049l$ back\n", "\x1b[?25l\x1b[?1003h\x1b[?1006h\x1b[?2004h"},
		{"mouse protocol changed inside the replay", handshake, "\x1b[?1002h", "\x1b[?1049h\x1b[?25l\x1b[?1006h\x1b[?2004h"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := restoreOver(t, c.trimmed, c.replay); got != c.want {
				t.Fatalf("Restore = %q, want %q", got, c.want)
			}
		})
	}
}

// Whatever the prefix says, prefix+replay must leave a fresh terminal in the
// same state as the full stream did.
func TestModeTracker_PrefixPlusReplayEqualsFullStream(t *testing.T) {
	streams := [][]string{
		{"\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?2004h\x1b[?25l", "frame\n\x1b[?2004l\x1b[?2004h"},
		{"\x1b[?47h\x1b[?1000h\x1b[?1h\x1b[?7l", "\x1b[?1000l\x1b[?1002h"},
		{"\x1b[?1049h\x1b[?1003h", "\x1b[!p"},
		{"\x1b[?1049h\x1b[?1003h\x1b[?2004h", "\x1bc$ fresh\n"},
		{"", "\x1b[?2004h"},
	}
	for _, st := range streams {
		full := newModeTracker()
		for _, c := range st {
			_, _ = full.Write([]byte(c))
		}
		replay := st[len(st)-1]
		prefix := full.Restore([]byte(replay))
		fresh := newModeTracker()
		_, _ = fresh.Write(prefix)
		_, _ = fresh.Write([]byte(replay))
		if fresh.st != full.st {
			t.Errorf("%q: prefix %q + replay gives %+v, full stream gives %+v", st, prefix, fresh.st, full.st)
		}
	}
}

// FuzzModeTracker checks three properties on arbitrary byte streams, including
// ones no program would print: the tracker never panics; feeding the stream in
// one write or one byte at a time yields the same state (PTY reads split
// anywhere); and a fresh tracker given prefix+replay for any split point ends
// in the same state as one that saw every byte, which is the contract Restore
// exists for.
func FuzzModeTracker(f *testing.F) {
	f.Add([]byte("\x1b[?2004h$ qwen\r\n\x1b[?1049h\x1b[?25l\x1b[?1003h\x1b[?1006h\x1b[2K\x1b[1Aframe\n"), 12)
	f.Add([]byte("\x1b]0;title\x1b\\\x1b[?47h\x1b=\x1b[?7l\x1b[!p\x1bc\x1b[?1002;1016h"), 3)
	f.Add([]byte("\x1b[?"+strings.Repeat("1;", 70)+"h\x1b[?2004h\x1b\x18[?1049h\x1bP q \x07 \x1b\\"), 40)
	f.Add([]byte{}, 0)
	f.Fuzz(func(t *testing.T, stream []byte, split int) {
		whole := newModeTracker()
		_, _ = whole.Write(stream)

		byByte := newModeTracker()
		for i := range stream {
			_, _ = byByte.Write(stream[i : i+1])
		}
		if byByte.st != whole.st {
			t.Fatalf("byte-at-a-time state %+v != whole-write state %+v", byByte.st, whole.st)
		}

		if split < 0 {
			split = -split
		}
		if len(stream) > 0 {
			split %= len(stream) + 1
		} else {
			split = 0
		}
		replay := stream[split:]
		prefix := whole.Restore(replay)
		late := newModeTracker()
		_, _ = late.Write(prefix)
		_, _ = late.Write(replay)
		if late.st != whole.st {
			t.Fatalf("split %d: prefix %q + replay leaves %+v, full stream leaves %+v", split, prefix, late.st, whole.st)
		}
	})
}

// BenchmarkModeTracker measures the per-byte cost on the pump path with a
// realistic TUI frame (styled text, cursor movement, one mode toggle per
// frame), which is the only place the tracker runs per byte.
func BenchmarkModeTracker(b *testing.B) {
	var frame bytes.Buffer
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&frame, "\x1b[2K\x1b[1A\x1b[38;2;120;120;120m│ line %02d some text with unicode → ✓ and more text\x1b[0m\n", i)
	}
	frame.WriteString("\x1b[?2004l pasted \x1b[?2004h\x1b]0;title\x07")
	chunk := frame.Bytes()
	m := newModeTracker()
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.Write(chunk)
	}
}
