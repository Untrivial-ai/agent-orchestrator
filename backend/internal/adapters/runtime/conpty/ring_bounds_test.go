package conpty

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Keep the expected public budget explicit so this test also runs on the base.
const scrollbackByteBudget = 1 << 20

func TestRingByteBoundIncludesPartialAndCompleted(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			r := NewRing()
			r.Append([]byte("old\n"))
			stream := bytes.Repeat([]byte("\x1b[2K\rrepaint"), scrollbackByteBudget/4)
			if completed {
				stream = append(stream, '\n')
			}
			for start := 0; start < len(stream); start += 8192 {
				r.Append(stream[start:min(start+8192, len(stream))])
			}
			want := stream[len(stream)-scrollbackByteBudget:]
			if got := r.Replay(); !bytes.Equal(got, want) {
				t.Fatalf("Replay has %d bytes, want the newest %d bytes", len(got), len(want))
			}
			if !completed && len(r.Snapshot()) != 0 {
				t.Fatal("Snapshot retained evicted completed output or included partial output")
			}
			r.FlushPartial()
			if got := r.Snapshot(); !bytes.Equal(got, want) {
				t.Fatalf("Snapshot after flush has %d bytes, want %d", len(got), len(want))
			}
		})
	}
}

func TestRingOversizedAppendOwnsItsBytes(t *testing.T) {
	r := NewRing()
	raw := bytes.Repeat([]byte("x"), 2*scrollbackByteBudget)
	raw = append(raw, '\n')
	r.Append(raw)
	clear(raw)
	want := strings.Repeat("x", scrollbackByteBudget-1) + "\n"
	if got := r.Tail(1); got != want {
		t.Fatalf("Tail retained %d bytes, want %d independent bytes", len(got), len(want))
	}
	replay := r.Replay()
	clear(replay)
	if string(r.Snapshot()) != want {
		t.Fatal("mutating Replay changed stored output")
	}
}

func TestRingFlushPartialEnforcesLineLimit(t *testing.T) {
	r := NewRing()
	r.Append([]byte("first\n" + strings.Repeat("line\n", MaxOutputLines-1) + "last"))
	r.FlushPartial()
	want := strings.Repeat("line\n", MaxOutputLines-1) + "last"
	if got := string(r.Snapshot()); got != want {
		t.Fatalf("flush retained %d bytes, want %d with first line evicted", len(got), len(want))
	}
	if r.Tail(1) != "last" {
		t.Fatal("flush did not preserve the final unterminated line")
	}
}

func TestRingMixedOperationsMatchRetainedSuffix(t *testing.T) {
	r := NewRing()
	var lines []string
	partial := ""
	rng := rand.New(rand.NewSource(42))
	for step := 0; step < 250; step++ {
		if step%7 == 0 {
			r.FlushPartial()
			if partial != "" {
				lines = append(lines, partial)
				partial = ""
			}
		} else {
			fragment := strings.Repeat("x", rng.Intn(65536)) + "\r\x1b[0m"
			if step%3 == 0 {
				fragment += "\nnext\n"
			}
			r.Append([]byte(fragment))
			parts := strings.SplitAfter(partial+fragment, "\n")
			partial = parts[len(parts)-1]
			lines = append(lines, parts[:len(parts)-1]...)
		}
		if len(lines) > MaxOutputLines {
			lines = lines[len(lines)-MaxOutputLines:]
		}
		excess := len(strings.Join(lines, "")) + len(partial) - scrollbackByteBudget
		for excess > 0 && len(lines) > 0 {
			if len(lines[0]) <= excess {
				excess -= len(lines[0])
				lines = lines[1:]
			} else {
				lines[0] = lines[0][excess:]
				excess = 0
			}
		}
		if excess > 0 {
			partial = partial[excess:]
		}
		completed := strings.Join(lines, "")
		if string(r.Replay()) != completed+partial || string(r.Snapshot()) != completed {
			t.Fatalf("retained output differs at step %d", step)
		}
		for _, count := range []int{0, 1, 4, MaxOutputLines + 1} {
			want := strings.Join(lines[max(0, len(lines)-count):], "")
			if r.Tail(count) != want {
				t.Fatalf("Tail(%d) differs at step %d", count, step)
			}
		}
	}
}

func BenchmarkRingNewlineFreeStream(b *testing.B) {
	chunk := bytes.Repeat([]byte("\r\x1b[2Kxx"), 1024)
	for _, chunks := range []int{8, 128, 512} {
		b.Run(fmt.Sprint(chunks), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(chunks * len(chunk)))
			for i := 0; i < b.N; i++ {
				r := NewRing()
				for j := 0; j < chunks; j++ {
					r.Append(chunk)
				}
			}
		})
	}
}
