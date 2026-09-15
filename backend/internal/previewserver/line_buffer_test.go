package previewserver

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

func writeLog(t *testing.T, buffer *lineBuffer, text string) {
	t.Helper()
	n, err := buffer.Write([]byte(text))
	if n != len(text) || err != nil {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(text))
	}
}

func TestLineBufferOversizedCompleteLine(t *testing.T) {
	buffer := newLineBuffer(maxBufferedLogLines)
	line := strings.Repeat("older text ", 100000) + "newest text"
	writeLog(t, buffer, line+"\n")
	got := buffer.Last(1)
	if len(got) != 1 {
		t.Fatalf("Last length = %d, want 1", len(got))
	}
	if len(got[0]) > maxBufferedPartialBytes {
		t.Fatalf("completed line retained %d bytes, want at most %d", len(got[0]), maxBufferedPartialBytes)
	}
	if got[0] != line[len(line)-maxBufferedPartialBytes:] {
		t.Fatal("completed line did not retain its newest suffix")
	}
}

func TestLineBufferCombinedByteBudget(t *testing.T) {
	const budget = 256 * 1024
	buffer := newLineBuffer(maxBufferedLogLines)
	var want []string
	for i := 0; i < budget/maxBufferedPartialBytes+4; i++ {
		line := fmt.Sprintf("%04d", i) + strings.Repeat("x", maxBufferedPartialBytes-4)
		writeLog(t, buffer, line+"\n")
		want = append(want, line)
	}
	writeLog(t, buffer, "partial")
	got := buffer.Last(maxBufferedLogLines + 1)
	retained := 0
	for _, text := range got {
		retained += len(text)
	}
	if retained > budget {
		t.Fatalf("completed and partial text retained %d bytes, want at most %d", retained, budget)
	}
	if len(got) != budget/maxBufferedPartialBytes || got[len(got)-1] != "partial" {
		t.Fatalf("retained %d lines, want %d including newest partial", len(got), budget/maxBufferedPartialBytes)
	}
	want = append(want[len(want)-(budget/maxBufferedPartialBytes-1):], "partial")
	if !slices.Equal(got, want) {
		t.Fatal("byte eviction did not retain the newest completed lines followed by partial text")
	}
}

func TestLineBufferChunkBoundariesAndCRLF(t *testing.T) {
	for _, test := range []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"empty", []string{""}, []string{}},
		{"empty_lines", []string{"\n\n"}, []string{"", ""}},
		{"split_crlf", []string{"first\r", "\nsecond\n", "\nthird\r", "\nlast\r"}, []string{"first", "second", "", "third", "last\r"}},
		{"bare_cr", []string{"first\rsecond\r\r\n"}, []string{"first\rsecond\r"}},
		{"partial_join", []string{"fir", "", "st\nse", "cond"}, []string{"first", "second"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			buffer := newLineBuffer(10)
			for _, chunk := range test.chunks {
				writeLog(t, buffer, chunk)
			}
			if got := buffer.Last(10); !slices.Equal(got, test.want) {
				t.Fatalf("Last = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLineBufferOversizedChunking(t *testing.T) {
	line := strings.Repeat("0123456789", 1700) + "\r"
	partial := strings.Repeat("abcdefghij", 1700) + "newest"
	text := line + "\nmiddle\n" + partial
	want := []string{strings.TrimSuffix(line[len(line)-maxBufferedPartialBytes:], "\r"), "middle", partial[len(partial)-maxBufferedPartialBytes:]}
	for _, size := range []int{1, 7, 4096, len(text)} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			buffer := newLineBuffer(10)
			for offset := 0; offset < len(text); offset += size {
				writeLog(t, buffer, text[offset:min(offset+size, len(text))])
			}
			if got := buffer.Last(10); !slices.Equal(got, want) {
				t.Fatalf("chunk size %d produced a different retained suffix", size)
			}
		})
	}
}

func TestLineBufferLineLimitAndLast(t *testing.T) {
	buffer := newLineBuffer(3)
	writeLog(t, buffer, "first\nsecond\nthird\nfourth\nfifth\npartial")
	for _, test := range []struct {
		limit int
		want  []string
	}{
		{-1, []string{}},
		{0, []string{}},
		{1, []string{"partial"}},
		{2, []string{"fifth", "partial"}},
		{3, []string{"fourth", "fifth", "partial"}},
		{4, []string{"third", "fourth", "fifth", "partial"}},
		{100, []string{"third", "fourth", "fifth", "partial"}},
	} {
		t.Run(fmt.Sprint(test.limit), func(t *testing.T) {
			if got := buffer.Last(test.limit); !slices.Equal(got, test.want) {
				t.Fatalf("Last(%d) = %q, want %q", test.limit, got, test.want)
			}
		})
	}
	writeLog(t, buffer, "\n")
	if got, want := buffer.Last(100), []string{"fourth", "fifth", "partial"}; !slices.Equal(got, want) {
		t.Fatalf("completed partial = %q, want %q", got, want)
	}
}

func TestLineBufferByteEvictionAtCRLFBoundary(t *testing.T) {
	for _, chunks := range [][]string{{"\r\n"}, {"\r", "\n"}} {
		buffer := newLineBuffer(maxBufferedLogLines)
		line := strings.Repeat("x", maxBufferedPartialBytes)
		for range maxBufferedLogBytes / maxBufferedPartialBytes {
			writeLog(t, buffer, line+"\n")
		}
		for _, chunk := range chunks {
			writeLog(t, buffer, chunk)
		}
		got := buffer.Last(maxBufferedLogLines)
		if len(got) != maxBufferedLogBytes/maxBufferedPartialBytes || got[len(got)-1] != "" {
			t.Fatalf("chunks %q retained %d lines, want byte eviction before the CRLF completes", chunks, len(got))
		}
	}
}

func TestLineBufferSustainedPartialOutput(t *testing.T) {
	buffer := newLineBuffer(maxBufferedLogLines)
	chunk := strings.Repeat("x", 8*1024)
	for i := 0; i < 128; i++ {
		writeLog(t, buffer, chunk)
		got := buffer.Last(1)
		if len(got) != 1 || len(got[0]) != min((i+1)*len(chunk), maxBufferedPartialBytes) {
			t.Fatalf("write %d did not retain a bounded partial suffix", i)
		}
	}
	writeLog(t, buffer, "newest\n")
	got := buffer.Last(1)
	if len(got) != 1 || len(got[0]) != maxBufferedPartialBytes || !strings.HasSuffix(got[0], "newest") {
		t.Fatal("completing sustained partial output lost the newest suffix")
	}
}

func TestLineBufferZeroLineCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		buffer := newLineBuffer(capacity)
		writeLog(t, buffer, "discarded\npartial")
		if got := buffer.Last(10); !slices.Equal(got, []string{"partial"}) {
			t.Fatalf("capacity %d: Last = %q, want partial", capacity, got)
		}
		writeLog(t, buffer, "\n")
		if got := buffer.Last(10); len(got) != 0 {
			t.Fatalf("capacity %d retained completed lines: %q", capacity, got)
		}
	}
}

func TestLineBufferSnapshotOwnership(t *testing.T) {
	buffer := newLineBuffer(2)
	input := []byte("first\npartial")
	if _, err := buffer.Write(input); err != nil {
		t.Fatal(err)
	}
	clear(input)
	snapshot := buffer.Last(10)
	if !slices.Equal(snapshot, []string{"first", "partial"}) {
		t.Fatalf("input mutation changed retained text: %q", snapshot)
	}
	snapshot[0] = "changed"
	writeLog(t, buffer, "-continued\nlast\n")
	if snapshot[1] != "partial" {
		t.Fatalf("later writes changed prior snapshot: %q", snapshot)
	}
	if got := buffer.Last(10); !slices.Equal(got, []string{"partial-continued", "last"}) {
		t.Fatalf("snapshot mutation or later write corrupted retained text: %q", got)
	}
}

func TestLineBufferConcurrentReadersAndWriters(t *testing.T) {
	buffer := newLineBuffer(maxBufferedLogLines)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				writeLog(t, buffer, "a complete line\n")
			}
		})
		wg.Go(func() {
			for range 100 {
				for _, line := range buffer.Last(40) {
					if line != "a complete line" {
						t.Errorf("read incomplete or corrupt line %q", line)
					}
				}
			}
		})
	}
	wg.Wait()
	if got := len(buffer.Last(maxBufferedLogLines + 1)); got != maxBufferedLogLines {
		t.Fatalf("completed lines = %d, want %d", got, maxBufferedLogLines)
	}
}

func BenchmarkLineBufferFullWrites(b *testing.B) {
	for _, capacity := range []int{1, 200, 2000} {
		b.Run(fmt.Sprint(capacity), func(b *testing.B) {
			buffer := newLineBuffer(capacity)
			line := []byte("a complete log line\n")
			for range capacity {
				_, _ = buffer.Write(line)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = buffer.Write(line)
			}
		})
	}
}

func BenchmarkLineBufferLast(b *testing.B) {
	for _, limit := range []int{1, 40, 200} {
		b.Run(fmt.Sprint(limit), func(b *testing.B) {
			buffer := newLineBuffer(maxBufferedLogLines)
			for range maxBufferedLogLines {
				_, _ = buffer.Write([]byte("a complete log line\n"))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = buffer.Last(limit)
			}
		})
	}
}

func BenchmarkLineBufferOversizedWrites(b *testing.B) {
	buffer := newLineBuffer(maxBufferedLogLines)
	data := []byte(strings.Repeat("x", 4*1024*1024) + "\nshort\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = buffer.Write(data)
	}
}
