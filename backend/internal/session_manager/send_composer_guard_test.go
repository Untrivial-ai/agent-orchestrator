package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// draftProbeRecord builds a TUI session record whose last human keystroke is
// UNCONSUMED (the agent's last activity predates it), so composerHoldsDraft
// leaves the fast path and reaches the surface probe.
func draftProbeRecord(state domain.ActivityState, lastInputAt time.Time) domain.SessionRecord {
	return domain.SessionRecord{
		ID:      "s1",
		Mode:    domain.SessionModeTUI,
		Harness: "claude-code",
		Activity: domain.Activity{
			State:          state,
			LastActivityAt: lastInputAt.Add(-time.Second),
		},
		Metadata: domain.SessionMetadata{RuntimeHandleID: "h1"},
	}
}

func draftProbeManager(agent ports.Agent, output string, styledErr error) (*Manager, *transitionRuntime) {
	rt := &transitionRuntime{
		fakeRuntime:     &fakeRuntime{},
		outputForCall:   func(int) string { return output },
		styledOutputErr: styledErr,
	}
	return &Manager{agents: singleAgent{agent: agent}, runtime: rt}, rt
}

func TestComposerHoldsDraft(t *testing.T) {
	lastInputAt := time.Now()

	t.Run("stable draft across samples is refused", func(t *testing.T) {
		m, rt := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput, nil)
		if !m.composerHoldsDraft(context.Background(), draftProbeRecord(domain.ActivityIdle, lastInputAt), lastInputAt) {
			t.Fatal("expected a stable draft to be detected")
		}
		if rt.styledOutputCalls < sendComposerDraftSamples {
			t.Fatalf("expected >= %d captures for a confirmed draft, got %d", sendComposerDraftSamples, rt.styledOutputCalls)
		}
	})

	t.Run("empty composer delivers on the first capture", func(t *testing.T) {
		m, rt := draftProbeManager(transitionSurfaceAgent{}, idleTerminalOutput, nil)
		if m.composerHoldsDraft(context.Background(), draftProbeRecord(domain.ActivityIdle, lastInputAt), lastInputAt) {
			t.Fatal("empty composer must not be treated as a draft")
		}
		if rt.styledOutputCalls != 1 {
			t.Fatalf("a non-draft frame should stop after one capture, got %d", rt.styledOutputCalls)
		}
	})

	t.Run("capture error fails open", func(t *testing.T) {
		m, _ := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput, errors.New("capture unavailable"))
		if m.composerHoldsDraft(context.Background(), draftProbeRecord(domain.ActivityIdle, lastInputAt), lastInputAt) {
			t.Fatal("a capture error must fail open (deliver), not refuse")
		}
	})

	t.Run("consumed input takes the fast path with no capture", func(t *testing.T) {
		m, rt := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput, nil)
		rec := draftProbeRecord(domain.ActivityIdle, lastInputAt)
		// Agent activity AFTER the last keystroke => input consumed => no draft.
		rec.Activity.LastActivityAt = lastInputAt.Add(time.Second)
		if m.composerHoldsDraft(context.Background(), rec, lastInputAt) {
			t.Fatal("a consumed keystroke must not read as a draft")
		}
		if rt.styledOutputCalls != 0 {
			t.Fatalf("fast path must not capture the screen, got %d captures", rt.styledOutputCalls)
		}
	})

	t.Run("active turn is not probed", func(t *testing.T) {
		m, rt := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput, nil)
		if m.composerHoldsDraft(context.Background(), draftProbeRecord(domain.ActivityActive, lastInputAt), lastInputAt) {
			t.Fatal("an active turn has no idle composer to protect")
		}
		if rt.styledOutputCalls != 0 {
			t.Fatalf("an active turn must not be probed, got %d captures", rt.styledOutputCalls)
		}
	})

	t.Run("harness without a surface inspector fails open", func(t *testing.T) {
		m, _ := draftProbeManager(fakeAgent{}, draftTerminalOutput, nil)
		if m.composerHoldsDraft(context.Background(), draftProbeRecord(domain.ActivityIdle, lastInputAt), lastInputAt) {
			t.Fatal("a harness that cannot inspect its TUI must fail open")
		}
	})

	t.Run("non-TUI session is never a draft", func(t *testing.T) {
		m, _ := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput, nil)
		rec := draftProbeRecord(domain.ActivityIdle, lastInputAt)
		rec.Mode = domain.SessionModeChat
		if m.composerHoldsDraft(context.Background(), rec, lastInputAt) {
			t.Fatal("chat mode has no composer pane to protect")
		}
	})
}
