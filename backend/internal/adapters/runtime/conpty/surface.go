package conpty

import (
	"io"
	"strings"

	vt "github.com/unixshells/vt-go"
)

// renderedSurface applies the ConPTY VT output stream to a cell grid. Unlike
// Ring, it represents only the current viewport, so overwritten transcript
// text cannot be mistaken for live provider chrome during a destructive
// interface handoff.
type renderedSurface struct {
	emulator *vt.SafeEmulator
}

func newRenderedSurface(cols, rows int) *renderedSurface {
	emulator := vt.NewSafeEmulator(cols, rows)
	// The emulator answers terminal capability and status queries through its
	// input pipe. This surface is a passive capture-only observer, so nobody
	// consumes those replies unless we drain them here. Leaving the pipe unread
	// makes Emulator.Write block on the first query (for example DA1), which in
	// turn stalls the real PTY output pump and leaves the attached xterm blank.
	go func() { _, _ = io.Copy(io.Discard, emulator) }()
	return &renderedSurface{emulator: emulator}
}

func (s *renderedSurface) Write(p []byte) {
	_, _ = s.emulator.Write(p)
}

func (s *renderedSurface) Resize(cols, rows int) {
	s.emulator.Resize(cols, rows)
}

func (s *renderedSurface) Tail(lines int) string {
	if lines <= 0 {
		return ""
	}
	rendered := strings.TrimRight(s.emulator.Render(), "\n")
	if rendered == "" {
		return ""
	}
	rows := strings.Split(rendered, "\n")
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return strings.Join(rows, "\n")
}
