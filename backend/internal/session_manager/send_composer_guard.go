package sessionmanager

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// sendComposerDraftSamples is how many consecutive captures must show a draft
	// before a send is refused. It mirrors the interface-transition drain
	// (interfaceTransitionSurfaceIdleSamples): a single frame can misread provider
	// chrome (a product label, statusline, or rule) as a draft, so one capture is
	// never enough to block delivery (#5482 / #5486).
	sendComposerDraftSamples = interfaceTransitionSurfaceIdleSamples
	// sendComposerProbeLines bounds the rendered viewport each capture reads.
	sendComposerProbeLines = interfaceTransitionOutputLines
)

// composerHoldsDraft reports whether a TUI session's composer positively holds
// an unsent human draft, so a send must not concatenate its payload with the
// operator's half-typed text and submit both as one prompt (#5711).
//
// Only the screen can prove it. Recency cannot: an interrupt's Stop hook
// refreshes LastActivityAt while the draft stays on screen, and keystrokes typed
// over a direct tmux attach never reach lastInputAt. So when the harness and
// runtime can read the screen, at least one capture always runs, as in the
// interface-transition drain; lastInputAt only delays sampling until the last
// keystroke has settled.
//
// It fails OPEN (returns false, so the send is delivered) on every case it
// cannot prove: a non-TUI session, a turn that is not at a prompt, a harness or
// runtime that cannot read the screen, a capture error, a provider-owned screen,
// a draft that does not persist across sendComposerDraftSamples captures, or a
// probe that runs out of time.
//
// The refusal follows what the running agent renders, so an agent that paints a
// composer-shaped screen can hold sends in retryable 409s while it does. That is
// accepted: the operator keeps direct terminal access, and a refused send is
// never written.
func (m *Manager) composerHoldsDraft(ctx context.Context, rec domain.SessionRecord, lastInputAt time.Time) bool {
	if domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeTUI {
		return false
	}
	// Only a prompt can hold an unsent human draft. An active turn is the agent
	// working, and a blocked decision is already refused upstream.
	if rec.Activity.State != domain.ActivityIdle && rec.Activity.State != domain.ActivityWaitingInput {
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

	poll, settle, limit := m.composerProbeTiming()
	// The probe runs under the held input lease, so it is bounded like the
	// drain's unverified-idle proof; running out of time delivers.
	probeCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// A composer can still show the text of a just-submitted prompt for a moment
	// after Enter, so give the last keystroke its settle window first.
	if !lastInputAt.IsZero() {
		if wait := time.Until(lastInputAt.Add(settle)); wait > 0 {
			select {
			case <-probeCtx.Done():
				return m.composerProbeGaveUp(probeCtx, rec)
			case <-time.After(wait):
			}
		}
	}
	for i := 0; i < sendComposerDraftSamples; i++ {
		if i > 0 {
			select {
			case <-probeCtx.Done():
				return m.composerProbeGaveUp(probeCtx, rec)
			case <-time.After(poll):
			}
		}
		output, err := styledOutput.GetStyledOutput(probeCtx, handle, sendComposerProbeLines)
		if err != nil {
			if probeCtx.Err() != nil {
				return m.composerProbeGaveUp(probeCtx, rec)
			}
			m.logger.Warn("send: composer draft check skipped, screen capture failed; delivering", "sessionID", rec.ID, "error", err)
			return false
		}
		observation := surfaceInspector.InspectTerminalSurface(output)
		switch {
		case observation.Work == ports.TerminalSurfaceWorkWaitingInput,
			observation.Work == ports.TerminalSurfaceWorkBlocked:
			// A provider-owned approval or input request, not a human draft; its
			// rows can read as composer text. The drain treats it the same way.
			return false
		case observation.Composer != ports.TerminalComposerDraft:
			return false // one empty or unknown frame is enough to deliver
		}
	}
	return true
}

// composerProbeGaveUp logs why the probe stopped early and delivers. A caller
// that cancelled its context is refused a write by the guard itself.
func (m *Manager) composerProbeGaveUp(probeCtx context.Context, rec domain.SessionRecord) bool {
	m.logger.Warn("send: composer draft check did not finish; delivering", "sessionID", rec.ID, "error", probeCtx.Err())
	return false
}

// composerProbeTiming shares the interface-transition drain's timings: its poll
// interval between samples, its settle window after the last keystroke, and its
// bound for proving an unverified idle.
func (m *Manager) composerProbeTiming() (poll, settle, limit time.Duration) {
	cfg := m.interfaceTransition
	poll, settle, limit = cfg.pollInterval, cfg.idleSettle, cfg.staleIdleLimit
	if poll <= 0 {
		poll = interfaceTransitionPoll
	}
	if settle <= 0 {
		settle = interfaceTransitionIdleSettle
	}
	if limit <= 0 {
		limit = interfaceTransitionStaleIdleLimit
	}
	return poll, settle, limit
}

// composerBusyCheck builds the guard callback for a send. It reads the last
// accepted keystroke without closing terminal input: closing it would drop the
// operator's keystrokes for the duration, and the guard's input lease already
// fences AO writes around the check.
func (m *Manager) composerBusyCheck() func(context.Context, domain.SessionRecord) bool {
	return func(ctx context.Context, rec domain.SessionRecord) bool {
		return m.composerHoldsDraft(ctx, rec, m.lastTerminalInputAt(rec))
	}
}
