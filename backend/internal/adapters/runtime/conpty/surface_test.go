package conpty

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/terminalui"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRenderedSurfaceC1TitleDoesNotBecomeDraft(t *testing.T) {
	for _, command := range []string{"0", "1", "2"} {
		for _, terminator := range []string{"\a", "\x1b\\", "\x9c"} {
			for _, draft := range []string{"", "keep my unsent draft"} {
				t.Run(fmt.Sprintf("command=%s/terminator=%x/draft=%t", command, terminator, draft != ""), func(t *testing.T) {
					payload := []byte("\x9d" + command + ";✳ session title" + terminator)
					check := func(chunks ...[]byte) {
						t.Helper()
						surface := newRenderedSurface(80, 12)
						border := strings.Repeat("─", 20)
						surface.Write([]byte(border + "\r\n❯ " + draft + "\r\n" + border + "\r\nfooter\x1b[2;3H"))
						for _, chunk := range chunks {
							surface.Write(chunk)
						}
						visible := surface.Tail(12)
						want := terminalui.ComposerEmpty
						if draft != "" {
							want = terminalui.ComposerDraft
						}
						if got := terminalui.LastBorderedPromptComposerState(visible, "❯"); got != want || strings.Contains(visible, "session title") {
							t.Fatalf("composer = %v, want %v; viewport: %q", got, want, visible)
						}
						if !strings.Contains(visible, draft) {
							t.Fatalf("real draft was changed: %q", visible)
						}
					}
					for split := 0; split <= len(payload); split++ {
						check(payload[:split], payload[split:])
					}
					chunks := make([][]byte, len(payload))
					for i := range payload {
						chunks[i] = payload[i : i+1]
					}
					check(chunks...)
				})
			}
		}
	}
}

func TestRenderedSurfaceDoesNotTurnUnicodeTitleIntoDraft(t *testing.T) {
	for _, draft := range []string{"", "keep my unsent draft", "first line\r\n  second line", "\r\n  second line"} {
		t.Run(draft, func(t *testing.T) {
			surface := newRenderedSurface(80, 12)
			border := strings.Repeat("─", 20)
			surface.Write([]byte(border + "\r\n❯ " + draft + "\r\n" + border + "\r\nfooter\x1b[2;3H"))
			// Claude emits this title update after completing a Terminal reply.
			surface.Write([]byte("\x1b]0;✳ session title\a"))
			visible := surface.Tail(12)
			want := terminalui.ComposerEmpty
			if draft != "" {
				want = terminalui.ComposerDraft
			}
			if got := terminalui.LastBorderedPromptComposerState(visible, "❯"); got != want {
				t.Fatalf("composer = %v, want %v; title became visible: %q", got, want, visible)
			}
			if strings.Contains(visible, "session title") {
				t.Fatalf("OSC title leaked into current viewport: %q", visible)
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
