package conpty

import (
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
	return &renderedSurface{emulator: vt.NewSafeEmulator(cols, rows)}
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
