package sessionmanager

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// sendComposerDraftSamples is how many consecutive captures must show a draft
	// before a user-initiated send is refused. It mirrors the interface-transition
	// drain's requirement (interfaceTransitionSurfaceIdleSamples): a single frame
	// can misread provider chrome (a product label, statusline, or rule) as a
	// draft, so one capture is never enough to block delivery (#5482 / #5486).
	sendComposerDraftSamples = interfaceTransitionSurfaceIdleSamples
	// sendComposerProbeLines bounds the rendered viewport each capture reads.
	sendComposerProbeLines = interfaceTransitionOutputLines
)

// composerHoldsDraft reports whether a TUI session's composer positively holds
// an unsent human draft, so a user-initiated Send must not concatenate its
// payload with the operator's half-typed text and submit both as one prompt
// (#5711).
//
// It fails OPEN — returns false — on every uncertain case: a non-TUI session, a
// last human keystroke the agent already consumed, a harness without a surface
// inspector, a runtime that cannot preserve terminal styling, a capture error,
// or a draft that cannot be proven stable. Delivery is preserved wherever the
// draft state cannot be established. Only a draft observed across
// sendComposerDraftSamples consecutive captures returns true.
//
// The lastInputAt gate is the hot-path optimization: the surface probe (a
// cross-process capture per sample) runs ONLY when a human keystroke is still
// unconsumed, so ordinary sends into a settled session pay nothing beyond the
// record the guard already read.
func (m *Manager) composerHoldsDraft(ctx context.Context, rec domain.SessionRecord, lastInputAt time.Time) bool {
	if domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeTUI {
		return false
	}
	// Only an idle or waiting-input prompt can hold an unsent human draft. An
	// active turn is the agent working (a send there is mid-turn steering, handled
	// elsewhere), and a blocked decision is already refused upstream — neither is a
	// human composer, so skip the probe entirely.
	if rec.Activity.State != domain.ActivityIdle && rec.Activity.State != domain.ActivityWaitingInput {
		return false
	}
	// Fast path: if the agent has acted since the last human keystroke, that
	// input was consumed (submitted), so the composer holds no unsent draft. This
	// is the common case for any send into a settled session and costs nothing.
	if tuiIdleAfterInput(rec, lastInputAt) {
		return false
	}
	if m.agents == nil {
		return false
	}
	agent, ok := m.agents.Agent(rec.Harness)
	if !ok {
		return false
	}
	surfaceInspector, ok := agent.(ports.TerminalSurfaceInspector)
	if !ok {
		return false
	}
	styledOutput, ok := m.runtime.(ports.StyledTerminalOutputReader)
	if !ok {
		return false
	}
	handle := runtimeHandle(rec.Metadata)
	if handle.ID == "" {
		return false
	}
	// Reached only when a human keystroke is unconsumed — a possible unsent
	// draft. Confirm a real draft rather than provider chrome, requiring the same
	// repeated evidence the transition drain demands before it trusts a draft.
	for i := 0; i < sendComposerDraftSamples; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(interfaceTransitionPoll):
			}
		}
		output, err := styledOutput.GetStyledOutput(ctx, handle, sendComposerProbeLines)
		if err != nil {
			return false // cannot capture the screen — fail open, preserve delivery
		}
		if surfaceInspector.InspectTerminalSurface(output).Composer != ports.TerminalComposerDraft {
			return false // a single empty/unknown frame is enough to deliver
		}
	}
	return true
}

// composerBusyCheck builds the guard callback for a user-initiated send. It
// snapshots the terminal's last accepted human keystroke and releases the input
// barrier immediately: the guard already holds the session-input lease across
// the write, so the barrier is only read here for the lastInputAt fast-path and
// must not be held across the probe (that would drop the operator's keystrokes).
func (m *Manager) composerBusyCheck() func(context.Context, domain.SessionRecord) bool {
	return func(ctx context.Context, rec domain.SessionRecord) bool {
		lastInputAt, release := m.beginTerminalInputDrain(rec)
		if release != nil {
			release()
		}
		return m.composerHoldsDraft(ctx, rec, lastInputAt)
	}
}
