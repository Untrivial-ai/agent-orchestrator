package conpty

import (
	"strings"
	"testing"
)

func TestRenderedSurfaceTracksTheVisibleAlternateScreen(t *testing.T) {
	surface := newRenderedSurface(80, 12)
	surface.Write([]byte("shell history\r\n"))
	surface.Write([]byte("\x1b[?1049h\x1b[2J\x1b[H\x1b[2mcurrent tui\x1b[0m"))

	visible := surface.Tail(12)
	if !strings.Contains(visible, "current tui") {
		t.Fatalf("alternate screen missing current content: %q", visible)
	}
	if strings.Contains(visible, "shell history") {
		t.Fatalf("alternate screen leaked hidden history: %q", visible)
	}
	if !strings.Contains(visible, "\x1b[") {
		t.Fatalf("alternate screen lost ANSI cell styling: %q", visible)
	}

	surface.Write([]byte("\x1b[?1049l"))
	restored := surface.Tail(12)
	if !strings.Contains(restored, "shell history") {
		t.Fatalf("leaving alternate screen did not restore the visible primary screen: %q", restored)
	}
	if strings.Contains(restored, "current tui") {
		t.Fatalf("leaving alternate screen retained hidden TUI content: %q", restored)
	}
}
