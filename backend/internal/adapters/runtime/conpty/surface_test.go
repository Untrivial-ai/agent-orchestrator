package conpty

import (
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/terminalui"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRenderedSurfaceTitleDoesNotBecomeDraft(t *testing.T) {
	for _, tt := range []struct {
		name, draft, title string
	}{
		{"empty composer", "", "\x1b]0;✳ session title\a"},
		{"real draft", "keep my unsent draft", "\x9d00;✳ session title\x9c"},
		{"multiline draft", "first line\r\n  second line", "\x1b]02;✳ session title\x1b\\"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			surface := newRenderedSurface(80, 12)
			border := strings.Repeat("─", 20)
			surface.Write([]byte(border + "\r\n❯ " + tt.draft + "\r\n" + border + "\r\nfooter\x1b[2;3H"))
			surface.Write([]byte(tt.title))
			visible := surface.Tail(12)
			want := terminalui.ComposerEmpty
			if tt.draft != "" {
				want = terminalui.ComposerDraft
			}
			if got := terminalui.LastBorderedPromptComposerState(visible, "❯"); got != want || strings.Contains(visible, "session title") {
				t.Fatalf("composer = %v, want %v; viewport: %q", got, want, visible)
			}
			if !strings.Contains(visible, strings.ReplaceAll(tt.draft, "\r\n", "\n")) {
				t.Fatalf("real draft was changed: %q", visible)
			}
		})
	}
}

func TestRenderedSurfaceTitleDoesNotHideBusyClaudeTurn(t *testing.T) {
	surface := newRenderedSurface(80, 12)
	border := strings.Repeat("─", 20)
	surface.Write([]byte("✶ Generating… (esc to interrupt · 2s)\r\n" + border + "\r\n❯\r\n" + border + "\x1b[3;3H"))
	for _, b := range []byte("\x1b]0;✳ session title\x1b\\") {
		surface.Write([]byte{b})
	}
	got := (&claudecode.Plugin{}).InspectTerminalSurface(surface.Tail(12))
	if got.Work != ports.TerminalSurfaceWorkActive || got.Composer != ports.TerminalComposerEmpty {
		t.Fatalf("title changed active turn observation: %+v", got)
	}
}

func TestRenderedSurfaceTitleCancelsPartialEscape(t *testing.T) {
	for _, prefix := range []string{"\x1b", "\x1b[2", "\x1b[31"} {
		for _, introducer := range []string{"\x1b]", "\x9d"} {
			surface := newRenderedSurface(80, 12)
			surface.Write([]byte("❯ " + prefix))
			surface.Write([]byte(introducer + "0;✳ title\aactual draft"))
			if got := surface.Tail(12); got != "❯ actual draft" {
				t.Fatalf("prefix %q, introducer %q: got %q, want draft intact", prefix, introducer, got)
			}
		}
	}
}

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
