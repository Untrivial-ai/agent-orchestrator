package conpty

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// modeTracker follows the DEC private modes a program has negotiated on its
// PTY so a late attacher can be told about them. Ring replays raw output, and
// a bounded ring loses whatever scrolled out of it first: for a full-screen
// program that is the one-time alternate-screen / mouse-tracking / bracketed-
// paste handshake it printed at startup. A client that attaches after that
// point renders the replay in its normal buffer with mouse reporting off, and
// its wheel or touch scrolling never reaches the program (#5039). tmux never
// had this problem because it re-sends the modes on every attach; the host
// must do the same by construction (see Restore).
//
// The tracker deliberately mirrors how xterm.js 5.5 (the desktop pane and the
// phone's WebView terminal) keeps this state, because the goal is for a late
// attacher to end up in the same state as a client that saw every byte:
//
//   - 9/1000/1002/1003 select ONE mouse protocol; resetting any of them turns
//     reporting off.
//   - 1006/1016 select ONE mouse encoding; resetting either restores the
//     default. 1005 and 1015 are no-ops in xterm.js (its #2507) and are
//     ignored here for the same reason: honouring them would replay an
//     encoding change the client never made.
//   - 47/1047/1049 select the alternate buffer; resetting any of them returns
//     to the normal one. The number the program used is replayed verbatim.
//   - 1 (application cursor keys), 66 (application keypad; ESC = and ESC >
//     set and clear the same flag), 1004 (focus events) and 2004 (bracketed
//     paste) are independent flags.
//   - 7 (auto-wrap) and 25 (cursor visible) default ON, so only their reset
//     state needs replaying.
//   - DECSTR (CSI ! p) restores the flags, wrap and cursor to defaults but
//     leaves the mouse and the buffer alone. RIS (ESC c) resets everything
//     except a hidden cursor, which xterm.js shows again only on ?25h or
//     DECSTR.
//
// Left out on purpose: modes that only affect how the replay paints (6 origin,
// 45 reverse-wrap) are corrected by the program's next repaint, and 12, 1048
// and 2026 are not modes xterm.js keeps. ANSI modes (CSI Pn h without '?') are
// not tracked either. The rendered surface's emulator exports only its
// alternate-screen bit, so the tracker keeps all of this state itself rather
// than splitting one rule across two sources.
//
// The tracker is a byte-level state machine, so sequences split across PTY
// reads are handled; it is not a full VT parser and executes nothing.
type modeTracker struct {
	mu sync.Mutex

	// Parser state.
	state  parseState
	params []byte // CSI parameter and intermediate bytes, capped at maxParams
	// belEnds is true while inside an OSC string, the only string kind BEL
	// terminates; DCS, SOS, PM and APC payloads may contain 0x07 and end only
	// at ST (or CAN/SUB), as in xterm's parser. Kept for parity: an ESC ends
	// every string kind, so for the 7-bit sequences tracked here the
	// distinction has no observable effect.
	belEnds bool

	st modeState
}

// modeState is the terminal state xterm.js would hold after the same bytes.
// It is a plain comparable value so Restore can diff two of them.
type modeState struct {
	mouseProtocol  int  // 0 (off) or one of 9, 1000, 1002, 1003
	mouseEncoding  int  // 0 (default) or one of 1006, 1016
	altScreen      int  // 0 (normal buffer) or one of 47, 1047, 1049
	appCursorKeys  bool // ?1
	appKeypad      bool // ?66
	focusEvents    bool // ?1004
	bracketedPaste bool // ?2004
	wrapOff        bool // ?7 reset
	cursorHidden   bool // ?25 reset
}

type parseState uint8

const (
	stateGround parseState = iota
	stateEscape
	stateCSI
	stateString // OSC/DCS/APC/PM/SOS payload, until ST (OSC: BEL too)
	stateStringEscape
)

// maxParams bounds CSI parameter collection; a real mode sequence is a few
// bytes, and dropping an oversized one costs nothing but that sequence.
const maxParams = 64

func newModeTracker() *modeTracker {
	return &modeTracker{}
}

// Write feeds raw PTY output through the tracker. It never blocks and never
// fails; the return values exist so it satisfies io.Writer.
func (m *modeTracker) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range p {
		m.step(b)
	}
	return len(p), nil
}

func (m *modeTracker) step(b byte) {
	switch m.state {
	case stateGround:
		if b == 0x1b {
			m.state = stateEscape
		}
	case stateEscape:
		switch {
		case b == '[':
			m.state = stateCSI
			m.params = m.params[:0]
		case b == 'c':
			m.st.fullReset()
			m.state = stateGround
		case b == '=':
			m.st.appKeypad = true
			m.state = stateGround
		case b == '>':
			m.st.appKeypad = false
			m.state = stateGround
		case b == 'P' || b == ']' || b == 'X' || b == '^' || b == '_':
			m.state = stateString
			m.belEnds = b == ']'
		case b == 0x1b:
			// Stay: ESC ESC starts over.
		case b == 0x18 || b == 0x1a: // CAN / SUB abort the sequence
			m.state = stateGround
		case b < 0x20 || b == 0x7f:
			// C0 controls are executed mid-sequence by a real terminal and DEL
			// is ignored; neither ends the sequence.
		default:
			m.state = stateGround
		}
	case stateCSI:
		switch {
		case b == 0x1b:
			m.state = stateEscape
		case b == 0x18 || b == 0x1a: // CAN / SUB abort the sequence
			m.state = stateGround
		case b < 0x20 || b == 0x7f:
			// C0 controls are executed mid-sequence by a real terminal and DEL
			// is ignored; neither ends the sequence.
		case b <= 0x3f:
			if len(m.params) < maxParams {
				m.params = append(m.params, b)
			} else {
				m.state = stateGround
			}
		case b <= 0x7e:
			m.dispatch(b)
			m.state = stateGround
		default:
			m.state = stateGround
		}
	case stateString:
		switch {
		case b == 0x18 || b == 0x1a: // CAN / SUB abort any string
			m.state = stateGround
		case b == 0x07 && m.belEnds: // BEL ends OSC only
			m.state = stateGround
		case b == 0x1b:
			m.state = stateStringEscape
		}
	case stateStringEscape:
		// ESC \ is ST and ends the string; any other ESC restarts the escape.
		if b == '\\' {
			m.state = stateGround
		} else {
			m.state = stateEscape
			m.step(b)
		}
	}
}

// dispatch applies a complete CSI sequence with final byte final. Only mode
// sets/resets and DECSTR are of interest; everything else on the pump path
// (cursor moves, colours, erases) returns without allocating.
func (m *modeTracker) dispatch(final byte) {
	switch final {
	case 'h', 'l':
		if len(m.params) == 0 || m.params[0] != '?' {
			return // ANSI mode, or a private marker other than '?'
		}
		on := final == 'h'
		n, digits, valid := 0, false, true
		for _, c := range m.params[1:] {
			switch {
			case c >= '0' && c <= '9':
				n = n*10 + int(c-'0')
				digits = true
			case c == ';':
				if valid && digits {
					m.st.set(n, on)
				}
				n, digits, valid = 0, false, true
			default:
				// A sub-parameter, intermediate or second marker makes this
				// field something other than a mode number.
				valid = false
			}
		}
		if valid && digits {
			m.st.set(n, on)
		}
	case 'p':
		if len(m.params) == 1 && m.params[0] == '!' {
			m.st.softReset()
		}
	}
}

func (s *modeState) set(n int, on bool) {
	switch n {
	case 9, 1000, 1002, 1003:
		s.mouseProtocol = 0
		if on {
			s.mouseProtocol = n
		}
	case 1006, 1016:
		s.mouseEncoding = 0
		if on {
			s.mouseEncoding = n
		}
	case 47, 1047, 1049:
		s.altScreen = 0
		if on {
			s.altScreen = n
		}
	case 1:
		s.appCursorKeys = on
	case 66:
		s.appKeypad = on
	case 1004:
		s.focusEvents = on
	case 2004:
		s.bracketedPaste = on
	case 7:
		s.wrapOff = !on
	case 25:
		s.cursorHidden = !on
	}
}

// softReset mirrors xterm.js softReset (DECSTR): keyboard, paste and focus
// flags, auto-wrap and cursor visibility return to defaults; the mouse service
// and the active buffer are untouched.
func (s *modeState) softReset() {
	s.appCursorKeys, s.appKeypad, s.focusEvents, s.bracketedPaste = false, false, false, false
	s.wrapOff, s.cursorHidden = false, false
}

// fullReset mirrors xterm.js reset (RIS): modes, mouse and buffer return to
// their defaults. isCursorHidden is the one piece of state xterm.js's reset
// does not touch, so a cursor the program hid stays hidden.
func (s *modeState) fullReset() {
	*s = modeState{cursorHidden: s.cursorHidden}
}

// Restore returns the control sequences that bring a client which is about to
// receive replay up to the tracked state, or nil when replay already does that
// on its own. A fresh xterm starts at every default, and replay is a suffix of
// the bytes the tracker saw, so a mode whose last change is inside replay ends
// up right by itself; the prefix carries only the modes replay no longer
// mentions. Restricting it that way keeps an early attacher (ring not yet
// trimmed) receiving the bare replay, so output that preceded the program's
// switch to the alternate buffer still lands in the client's normal buffer as
// scrollback, exactly as before.
//
// Ordering: the alternate buffer first, the way a program prints it, then the
// rest by mode number; none of these draw, so only the wire image depends on
// it.
func (m *modeTracker) Restore(replay []byte) []byte {
	m.mu.Lock()
	live := m.st
	m.mu.Unlock()

	fromReplay := newModeTracker()
	_, _ = fromReplay.Write(replay)
	rep := fromReplay.st

	type entry struct {
		n  int
		on bool
	}
	var out []entry
	if live.altScreen != 0 && rep.altScreen != live.altScreen {
		out = append(out, entry{live.altScreen, true})
	}
	var rest []entry
	flag := func(n int, want, have bool) {
		if want && !have {
			rest = append(rest, entry{n, true})
		}
	}
	reset := func(n int, want, have bool) {
		if want && !have {
			rest = append(rest, entry{n, false})
		}
	}
	if live.mouseProtocol != 0 && rep.mouseProtocol != live.mouseProtocol {
		rest = append(rest, entry{live.mouseProtocol, true})
	}
	if live.mouseEncoding != 0 && rep.mouseEncoding != live.mouseEncoding {
		rest = append(rest, entry{live.mouseEncoding, true})
	}
	flag(1, live.appCursorKeys, rep.appCursorKeys)
	flag(66, live.appKeypad, rep.appKeypad)
	flag(1004, live.focusEvents, rep.focusEvents)
	flag(2004, live.bracketedPaste, rep.bracketedPaste)
	reset(7, live.wrapOff, rep.wrapOff)
	reset(25, live.cursorHidden, rep.cursorHidden)
	sort.Slice(rest, func(i, j int) bool { return rest[i].n < rest[j].n })
	out = append(out, rest...)

	if len(out) == 0 {
		return nil
	}
	var sb strings.Builder
	for _, e := range out {
		final := byte('h')
		if !e.on {
			final = 'l'
		}
		fmt.Fprintf(&sb, "\x1b[?%d%c", e.n, final)
	}
	return []byte(sb.String())
}
