package worker

import "strings"

// EncodeTerminalInput renders message text as interactive-agent PTY input.
// Multi-line text is wrapped in bracketed paste so the harness TUI receives it
// as one pasted message; a bare per-line "\r" would submit each line as its
// own prompt and shred the message. Single-line text stays a plain line so
// harnesses without bracketed-paste support keep working unchanged.
//
// Both injection paths — the control plane's terminal.input requests and the
// worker's queued-turn forwarding — must use this encoder so a message is
// delivered identically whichever path carries it.
func EncodeTerminalInput(text string) []byte {
	if strings.ContainsAny(text, "\r\n") {
		return []byte("\x1b[200~" + text + "\x1b[201~\r")
	}
	return []byte(text + "\r")
}
