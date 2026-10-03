package conpty

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestRingWrapAndByteEvictionPreserveFlushBoundaries(t *testing.T) {
	r := NewRing()
	var lines []string
	// Wrap both circular buffers repeatedly while the line limit determines
	// retention. Distinct lines make a reordered or duplicated tail visible.
	for i := range 3*MaxOutputLines + 1 {
		line := fmt.Sprintf("%06d:", i) + strings.Repeat(string(rune('a'+i%26)), 1000) + "\n"
		r.Append([]byte(line))
		lines = append(lines, line)
		if len(lines) > MaxOutputLines {
			lines = lines[1:]
		}
		if i%500 == 0 && string(r.Snapshot()) != strings.Join(lines, "") {
			t.Fatalf("completed output differs after line %d", i)
		}
		if r.Tail(1) != line {
			t.Fatalf("last completed line differs after line %d", i)
		}
	}
	completed := strings.Join(lines, "")
	partial := strings.Repeat("z", scrollbackByteBudget-len(completed)+1)
	r.Append([]byte(partial))
	// Exactly one byte is evicted from the oldest completed line. The
	// unfinished fragment must not appear in Snapshot or Tail yet.
	if string(r.Snapshot()) != completed[1:] || r.Tail(MaxOutputLines) != completed[1:] {
		t.Fatal("partial byte eviction lost the completed-line boundary")
	}
	if string(r.Replay()) != completed[1:]+partial {
		t.Fatal("replay differs after byte eviction")
	}
	r.FlushPartial()
	want := strings.Join(lines[1:], "") + partial
	if string(r.Snapshot()) != want || r.Tail(1) != partial {
		t.Fatal("flush failed to evict the clipped oldest line at the line limit")
	}
	if r.Tail(2) != lines[len(lines)-1]+partial {
		t.Fatal("flush changed the previous completed line")
	}
	r.Append(nil)
	r.FlushPartial()
	if string(r.Snapshot()) != want {
		t.Fatal("empty append or repeated flush changed completed output")
	}
	r.Append([]byte("after\n"))
	if r.Tail(2) != partial+"after\n" {
		t.Fatal("append after flush merged or reordered completed lines")
	}
}

func TestRingByteLimitPreservesRawSuffixWhenSequenceIsClipped(t *testing.T) {
	for _, tt := range []struct {
		name   string
		prefix string
	}{
		{"utf8", "\xe2\x82\xac"},
		{"ansi", "\x1b[31m"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRing()
			raw := append([]byte(tt.prefix), bytes.Repeat([]byte("x"), scrollbackByteBudget-len(tt.prefix))...)
			r.Append(raw)
			r.Append([]byte("!"))
			want := append(bytes.Clone(raw[1:]), '!')
			if !bytes.Equal(r.Replay(), want) {
				t.Fatal("clipping changed the retained raw bytes")
			}
			if len(r.Snapshot()) != 0 {
				t.Fatal("clipped unfinished output appeared in Snapshot")
			}
			r.FlushPartial()
			if !bytes.Equal(r.Snapshot(), want) || r.Tail(1) != string(want) {
				t.Fatal("flush changed the clipped raw suffix")
			}
		})
	}
}
