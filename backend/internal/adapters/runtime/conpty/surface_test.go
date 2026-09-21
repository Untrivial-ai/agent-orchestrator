package conpty

import (
	"strings"
	"testing"
	"time"
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

func TestRenderedSurfaceDrainsTerminalReplies(t *testing.T) {
	surface := newRenderedSurface(80, 24)
	done := make(chan struct{})
	go func() {
		// Primary Device Attributes asks the emulator to write a reply to its
		// input pipe. A passive surface must consume that reply or Write blocks.
		surface.Write([]byte("\x1b[c"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("rendered surface blocked while answering a terminal query")
	}
}
