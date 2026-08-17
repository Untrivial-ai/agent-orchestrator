package ports

// TerminalSurfaceWorkState is the provider TUI's current, rendered work state.
// The zero value is deliberately unknown so an unrecognized or partial frame
// can never become evidence for a destructive action.
type TerminalSurfaceWorkState uint8

// Terminal surface work states. Unknown is the fail-closed zero value.
const (
	TerminalSurfaceWorkUnknown TerminalSurfaceWorkState = iota
	TerminalSurfaceWorkActive
	TerminalSurfaceWorkIdle
	TerminalSurfaceWorkWaitingInput
	TerminalSurfaceWorkBlocked
)

// TerminalComposerState describes only whether the current provider composer
// contains human-authored text. It intentionally carries no draft contents.
type TerminalComposerState uint8

// Terminal composer states. Unknown is the fail-closed zero value.
const (
	TerminalComposerUnknown TerminalComposerState = iota
	TerminalComposerEmpty
	TerminalComposerDraft
)

// TerminalSurfaceObservation is a factual, ephemeral interpretation of one
// current terminal snapshot. Work and composer state are independent: a TUI
// may render a composer while a turn is still active.
type TerminalSurfaceObservation struct {
	Work     TerminalSurfaceWorkState
	Composer TerminalComposerState
	// NativeConversationNotStarted is positive provider-owned proof that the
	// visible TUI is still on its initial composer and has no native history to
	// transfer. False means unknown or started; callers must fail closed.
	NativeConversationNotStarted bool
}

// TerminalSurfaceInspector is an optional agent-adapter capability for
// interpreting the provider-specific terminal UI. Implementations must be pure
// and conservative: ambiguous, partial, or changed layouts return unknown
// facts. Callers own capture, settling, retry, and transition policy.
//
// output must be a bounded snapshot of the current rendered screen with ANSI
// styling preserved. Styling distinguishes dim provider placeholders from
// normal human-authored drafts.
type TerminalSurfaceInspector interface {
	InspectTerminalSurface(output string) TerminalSurfaceObservation
}
