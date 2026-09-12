package domain

import "fmt"

// SessionMode is the conversation controller currently committed for a session.
// It is chosen before the first controller launches and can change only through
// the durable interface-transition coordinator. At every controller epoch a live
// session still has exactly one writer: two concurrent writers on one provider
// conversation would race on turns, approvals, compaction, and reconnect state.
//
//   - SessionModeTUI: the provider's native CLI/TUI inside a terminal runtime is
//     the only conversation controller. This is the historical behavior and the
//     compatibility default.
//   - SessionModeChat: AO owns a structured provider controller (Codex
//     app-server today) and the terminal, if opened, is a plain worktree shell —
//     never a second copy of the agent.
type SessionMode string

// The session modes.
const (
	SessionModeTUI  SessionMode = "tui"
	SessionModeChat SessionMode = "chat"
)

// DefaultSessionMode is the compatibility fallback when the daemon-owned
// preference is unavailable or invalid. Keep it TUI so a settings read failure
// cannot turn a working terminal spawn into a Chat-only failure.
const DefaultSessionMode = SessionModeTUI

// Valid reports whether mode is one AO knows how to dispatch.
func (m SessionMode) Valid() bool {
	switch m {
	case SessionModeTUI, SessionModeChat:
		return true
	default:
		return false
	}
}

// NormalizeSessionMode collapses an empty or unrecognized mode to the
// compatibility default. Use it when reading durable state: a row written before
// this feature, or by a newer build that knows a mode this one does not, must
// still dispatch somewhere safe.
//
// Do not use it on API input — see ParseSessionMode. Normalizing a requested
// mode would silently hand the caller a TUI session when they asked for Chat.
func NormalizeSessionMode(mode SessionMode) SessionMode {
	if mode.Valid() {
		return mode
	}
	return DefaultSessionMode
}

// ParseSessionMode converts caller-supplied input into a mode, strictly. An
// empty string means "no mode requested" and yields the zero value with no
// error, so callers can distinguish absent from invalid and apply their own
// precedence. Anything else unrecognized is an error: a spawn that asked for a
// mode AO cannot honor must fail loudly rather than downgrade.
func ParseSessionMode(raw string) (SessionMode, error) {
	if raw == "" {
		return "", nil
	}
	mode := SessionMode(raw)
	if !mode.Valid() {
		return "", fmt.Errorf("unknown session mode %q: want %q or %q", raw, SessionModeChat, SessionModeTUI)
	}
	return mode, nil
}
