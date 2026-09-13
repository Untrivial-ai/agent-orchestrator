package conpty

import (
	"bytes"
	"sync"
)

// MaxOutputLines is the maximum number of completed lines retained.
const MaxOutputLines = 1000

// MaxOutputBytes bounds retained output, including the unfinished line. A TUI
// can repaint indefinitely without producing a newline.
const MaxOutputBytes = 1 << 20

// Ring stores a bounded suffix of terminal output. Bytes are preserved verbatim,
// but eviction can cut through a line, UTF-8 character, or ANSI sequence. Replay
// is truncated history, not a reconstructed screen snapshot.
// All methods are safe to call concurrently.
type Ring struct {
	mu sync.Mutex

	data  []byte // circular storage, grown lazily up to MaxOutputBytes
	start int
	size  int

	lineLengths [MaxOutputLines]int
	lineStart   int
	lineCount   int
	partial     int
}

// NewRing returns an empty Ring.
func NewRing() *Ring { return &Ring{} }

// Append retains completed lines and the trailing unfinished fragment. Appending
// to a long unfinished line copies only new bytes once storage has grown.
func (r *Ring) Append(raw []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for len(raw) > 0 {
		end := bytes.IndexByte(raw, '\n')
		if end < 0 {
			r.appendBytes(raw)
			return
		}
		r.appendBytes(raw[:end+1])
		r.completeLine()
		raw = raw[end+1:]
	}
}

// FlushPartial stores the unfinished fragment as a final line on PTY exit.
func (r *Ring) FlushPartial() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.partial > 0 {
		r.completeLine()
	}
}

// Snapshot returns completed output, excluding the unfinished fragment.
func (r *Ring) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.copyRange(0, r.size-r.partial)
}

// Replay includes the unfinished fragment, so newline-free TUI output is visible
// to a newly attached viewer. The caller owns the returned bytes.
func (r *Ring) Replay() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.copyRange(0, r.size)
}

// Tail returns the newest n completed lines. n <= 0 returns an empty string.
func (r *Ring) Tail(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 {
		return ""
	}
	length := 0
	for i := 0; i < min(n, r.lineCount); i++ {
		length += r.lineLengths[(r.lineStart+r.lineCount-1-i)%MaxOutputLines]
	}
	return string(r.copyRange(r.size-r.partial-length, length))
}

// The helpers below require mu.
func (r *Ring) appendBytes(raw []byte) {
	if len(raw) > MaxOutputBytes {
		raw = raw[len(raw)-MaxOutputBytes:]
	}
	if excess := r.size + len(raw) - MaxOutputBytes; excess > 0 {
		r.dropBytes(excess)
	}
	needed := r.size + len(raw)
	if needed > len(r.data) {
		capacity := min(MaxOutputBytes, max(4096, max(needed, 2*len(r.data))))
		data := make([]byte, capacity)
		r.copyInto(data[:r.size], 0)
		r.data = data
		r.start = 0
	}
	end := (r.start + r.size) % len(r.data)
	n := copy(r.data[end:], raw)
	copy(r.data, raw[n:])
	r.size += len(raw)
	r.partial += len(raw)
}

func (r *Ring) completeLine() {
	if r.lineCount == MaxOutputLines {
		r.dropBytes(r.lineLengths[r.lineStart])
	}
	r.lineLengths[(r.lineStart+r.lineCount)%MaxOutputLines] = r.partial
	r.lineCount++
	r.partial = 0
}

func (r *Ring) dropBytes(n int) {
	r.start = (r.start + n) % len(r.data)
	r.size -= n
	for n > 0 && r.lineCount > 0 {
		length := r.lineLengths[r.lineStart]
		if n < length {
			r.lineLengths[r.lineStart] -= n
			return
		}
		n -= length
		r.lineLengths[r.lineStart] = 0
		r.lineStart = (r.lineStart + 1) % MaxOutputLines
		r.lineCount--
	}
	r.partial -= n
}

func (r *Ring) copyRange(offset, length int) []byte {
	out := make([]byte, length)
	r.copyInto(out, offset)
	return out
}

func (r *Ring) copyInto(out []byte, offset int) {
	if len(out) == 0 {
		return
	}
	start := (r.start + offset) % len(r.data)
	n := copy(out, r.data[start:])
	copy(out[n:], r.data)
}
