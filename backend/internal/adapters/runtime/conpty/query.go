package conpty

import "bytes"

// terminalQueryResponder answers the terminal capability queries an interactive
// TUI sends before its first draw.
//
// Why this exists: when no client is attached, the pty-host is the only terminal
// the agent has, but it is a byte pump rather than an emulator and used to answer
// nothing. Full-screen TUIs probe the terminal at startup and wait for the reply:
// Muse Code 1.2.1 sends CSI 6n (cursor position) before drawing and, with nothing
// answering, blocks and then exits 0 having painted nothing — an agent that
// "would not launch" with no error anywhere in AO. Answering only the cursor
// position is enough to bring it up.
//
// Scope is deliberate. The responder answers queries whose answer describes how a
// client will consume input — cursor position, device attributes, keyboard
// protocol flags. It does not answer queries that describe the viewer's
// appearance (OSC 4/10/11 colour reports): those values belong to the attached
// terminal, inventing them here would repaint a light-themed pane with a dark
// theme's colours, and an unanswered colour report only makes an app fall back to
// its own defaults. The set is closed: anything else is left untouched.
//
// Replies are produced only while no client is attached; see
// host.answerTerminalQueries for why that rule matters.
type terminalQueryResponder struct {
	// pending holds a fragment that may still become a query, so a sequence split
	// across two PTY reads is answered on the read that completes it.
	pending []byte
}

// maxPendingQueryBytes bounds the fragment buffer. Every pattern below fits well
// inside it, so a longer tail cannot be a query prefix.
const maxPendingQueryBytes = 8

type terminalQuery struct {
	pattern []byte
	reply   []byte
}

// terminalQueries are the queries the host answers, each with the reply it
// expects. Cursor position is the one TUIs block on in practice; device
// attributes are the standard startup handshake; the kitty query answers "no
// flags set" so an app asking about a protocol this host does not implement is
// not left waiting for it.
var terminalQueries = []terminalQuery{
	// DSR 6: cursor position. The host has no cursor model, so 1;1 is a
	// placeholder that satisfies the query; an attached emulator answers with the
	// real position instead (the host stays silent then).
	{pattern: []byte("\x1b[6n"), reply: []byte("\x1b[1;1R")},
	// DSR 5: device status report, "terminal OK".
	{pattern: []byte("\x1b[5n"), reply: []byte("\x1b[0n")},
	// DA1: VT220 with ANSI colour support.
	{pattern: []byte("\x1b[c"), reply: []byte("\x1b[?62;22c")},
	// DA2: xterm's terminal-class reply.
	{pattern: []byte("\x1b[>c"), reply: []byte("\x1b[>41;330;0c")},
	// Kitty keyboard protocol query: no flags pushed.
	{pattern: []byte("\x1b[?u"), reply: []byte("\x1b[?0u")},
}

// Feed consumes one chunk of PTY output and returns the replies that chunk
// completed, in order. Fragments are carried across calls, so callers may feed
// arbitrarily split reads.
func (r *terminalQueryResponder) Feed(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}
	data := chunk
	if len(r.pending) > 0 {
		data = append(append([]byte(nil), r.pending...), chunk...)
		r.pending = nil
	}
	var replies [][]byte
	for i := 0; i < len(data); {
		if data[i] != 0x1b {
			i++
			continue
		}
		reply, consumed, needMore := matchTerminalQuery(data[i:])
		if needMore && len(data)-i <= maxPendingQueryBytes {
			r.pending = append([]byte(nil), data[i:]...)
			return replies
		}
		if consumed > 0 {
			if reply != nil {
				replies = append(replies, reply)
			}
			i += consumed
			continue
		}
		i++
	}
	return replies
}

// matchTerminalQuery reports how much of b one query consumes. needMore is true
// when b is a prefix of a pattern and the rest has not arrived yet.
func matchTerminalQuery(b []byte) (reply []byte, consumed int, needMore bool) {
	for _, query := range terminalQueries {
		if bytes.HasPrefix(b, query.pattern) {
			return query.reply, len(query.pattern), false
		}
		if bytes.HasPrefix(query.pattern, b) {
			return nil, 0, true
		}
	}
	return nil, 0, false
}
