package sessionmanager

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// providerPromptTerminalOutput is a provider-owned input request (an approval
// or question picker) whose rows can read as composer text.
const providerPromptTerminalOutput = "provider-prompt"

// composerProbeAgent is the transition test agent plus a provider-owned
// waiting_input screen that also reports a draft-shaped composer.
type composerProbeAgent struct{ transitionSurfaceAgent }

func (a composerProbeAgent) InspectTerminalSurface(output string) ports.TerminalSurfaceObservation {
	if output == providerPromptTerminalOutput {
		return ports.TerminalSurfaceObservation{Work: ports.TerminalSurfaceWorkWaitingInput, Composer: ports.TerminalComposerDraft}
	}
	return a.transitionSurfaceAgent.InspectTerminalSurface(output)
}

// fastComposerProbe keeps the production probe shape with test-sized timings.
var fastComposerProbe = interfaceTransitionConfig{
	pollInterval:   time.Millisecond,
	idleSettle:     time.Nanosecond,
	staleIdleLimit: time.Second,
}

func draftProbeRecord(state domain.ActivityState) domain.SessionRecord {
	return domain.SessionRecord{
		ID:       "s1",
		Mode:     domain.SessionModeTUI,
		Harness:  "claude-code",
		Activity: domain.Activity{State: state, LastActivityAt: time.Now()},
		Metadata: domain.SessionMetadata{RuntimeHandleID: "h1"},
	}
}

func draftProbeManager(agent ports.Agent, frames ...string) (*Manager, *transitionRuntime, *bytes.Buffer) {
	rt := &transitionRuntime{
		fakeRuntime: &fakeRuntime{},
		outputForCall: func(call int) string {
			if call > len(frames) {
				return frames[len(frames)-1]
			}
			return frames[call-1]
		},
	}
	logs := &bytes.Buffer{}
	return &Manager{
		agents:              singleAgent{agent: agent},
		runtime:             rt,
		interfaceTransition: fastComposerProbe,
		logger:              slog.New(slog.NewTextHandler(logs, nil)),
	}, rt, logs
}

func TestComposerHoldsDraft(t *testing.T) {
	ctx := context.Background()
	noInput := time.Time{}

	t.Run("stable draft across samples is refused", func(t *testing.T) {
		m, rt, _ := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput)
		if !m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityIdle), noInput) {
			t.Fatal("expected a stable draft to be detected")
		}
		if rt.styledOutputCalls != sendComposerDraftSamples {
			t.Fatalf("captures = %d, want %d for a confirmed draft", rt.styledOutputCalls, sendComposerDraftSamples)
		}
	})

	// Review finding 2: agent activity after the last keystroke does not prove the
	// composer is empty. An interrupt's Stop hook refreshes LastActivityAt while
	// the draft stays on screen, and keystrokes typed over a direct tmux attach
	// never reach lastInputAt at all. The screen is the only proof.
	t.Run("draft is refused even when the agent acted after the last keystroke", func(t *testing.T) {
		m, _, _ := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput)
		rec := draftProbeRecord(domain.ActivityIdle)
		lastInputAt := rec.Activity.LastActivityAt.Add(-time.Minute)
		if !m.composerHoldsDraft(ctx, rec, lastInputAt) {
			t.Fatal("recency must not skip the probe when the screen can be read")
		}
	})
	t.Run("draft typed outside AO (no recorded keystroke) is refused", func(t *testing.T) {
		m, _, _ := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput)
		if !m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityIdle), noInput) {
			t.Fatal("a draft with no AO-recorded keystroke must still be detected")
		}
	})

	// Review finding 3: provider-owned approval and input screens are not drafts.
	t.Run("provider decision screen is not a draft", func(t *testing.T) {
		m, rt, _ := draftProbeManager(transitionSurfaceAgent{}, decisionTerminalOutput)
		if m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityIdle), noInput) {
			t.Fatal("a blocked provider screen must not refuse sends")
		}
		if rt.styledOutputCalls != 1 {
			t.Fatalf("captures = %d, want 1", rt.styledOutputCalls)
		}
	})
	t.Run("provider input request is not a draft", func(t *testing.T) {
		m, _, _ := draftProbeManager(composerProbeAgent{}, providerPromptTerminalOutput)
		if m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityWaitingInput), noInput) {
			t.Fatal("a provider input request must not refuse sends")
		}
	})

	t.Run("waiting_input session with a real draft is refused", func(t *testing.T) {
		m, _, _ := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput)
		if !m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityWaitingInput), noInput) {
			t.Fatal("a draft at a waiting_input prompt must be protected")
		}
	})

	t.Run("draft that clears mid-sequence delivers", func(t *testing.T) {
		m, rt, _ := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput, idleTerminalOutput)
		if m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityIdle), noInput) {
			t.Fatal("a draft must persist across every sample before refusing")
		}
		if rt.styledOutputCalls != 2 {
			t.Fatalf("captures = %d, want 2 (stop at the first empty frame)", rt.styledOutputCalls)
		}
	})

	t.Run("empty composer delivers on the first capture", func(t *testing.T) {
		m, rt, _ := draftProbeManager(transitionSurfaceAgent{}, idleTerminalOutput)
		if m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityIdle), noInput) {
			t.Fatal("empty composer must not be treated as a draft")
		}
		if rt.styledOutputCalls != 1 {
			t.Fatalf("captures = %d, want 1", rt.styledOutputCalls)
		}
	})

	t.Run("waits for the last keystroke to settle before sampling", func(t *testing.T) {
		m, rt, _ := draftProbeManager(transitionSurfaceAgent{}, idleTerminalOutput)
		m.interfaceTransition.idleSettle = 40 * time.Millisecond
		lastInputAt := time.Now()
		m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityIdle), lastInputAt)
		if len(rt.outputCallTimes) == 0 || rt.outputCallTimes[0].Before(lastInputAt.Add(m.interfaceTransition.idleSettle)) {
			t.Fatalf("sampled before the settle window elapsed: %v", rt.outputCallTimes)
		}
	})

	t.Run("capture error fails open and is logged", func(t *testing.T) {
		m, rt, logs := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput)
		rt.styledOutputErr = errors.New("capture unavailable")
		if m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityIdle), noInput) {
			t.Fatal("a capture error must fail open (deliver), not refuse")
		}
		if !strings.Contains(logs.String(), "capture unavailable") {
			t.Fatalf("capture error was not logged: %q", logs.String())
		}
	})

	t.Run("a hung capture is bounded and fails open", func(t *testing.T) {
		m, rt, logs := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput)
		m.runtime = hangingStyledRuntime{rt}
		m.interfaceTransition.staleIdleLimit = 30 * time.Millisecond
		start := time.Now()
		if m.composerHoldsDraft(ctx, draftProbeRecord(domain.ActivityIdle), noInput) {
			t.Fatal("a probe that cannot finish must fail open")
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("probe ran %v past its %v limit", elapsed, m.interfaceTransition.staleIdleLimit)
		}
		if !strings.Contains(logs.String(), "deadline") {
			t.Fatalf("probe timeout was not logged: %q", logs.String())
		}
	})

	t.Run("cancelled context fails open", func(t *testing.T) {
		m, _, _ := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if m.composerHoldsDraft(cancelled, draftProbeRecord(domain.ActivityIdle), noInput) {
			t.Fatal("a cancelled probe must not refuse")
		}
	})

	failOpen := []struct {
		name   string
		mutate func(*Manager, *domain.SessionRecord)
	}{
		{"non-TUI session", func(_ *Manager, rec *domain.SessionRecord) { rec.Mode = domain.SessionModeChat }},
		{"active turn", func(_ *Manager, rec *domain.SessionRecord) { rec.Activity.State = domain.ActivityActive }},
		{"no agent registry", func(m *Manager, _ *domain.SessionRecord) { m.agents = nil }},
		{"harness without a surface inspector", func(m *Manager, _ *domain.SessionRecord) { m.agents = singleAgent{agent: fakeAgent{}} }},
		{"runtime without styled capture", func(m *Manager, _ *domain.SessionRecord) {
			m.runtime = &unstyledTransitionRuntime{runtime: m.runtime.(*transitionRuntime)}
		}},
		{"no runtime handle", func(_ *Manager, rec *domain.SessionRecord) { rec.Metadata.RuntimeHandleID = "" }},
	}
	for _, tc := range failOpen {
		t.Run(tc.name+" fails open without capturing", func(t *testing.T) {
			m, rt, _ := draftProbeManager(transitionSurfaceAgent{}, draftTerminalOutput)
			rec := draftProbeRecord(domain.ActivityIdle)
			tc.mutate(m, &rec)
			if m.composerHoldsDraft(ctx, rec, noInput) {
				t.Fatal("an unprovable case must fail open (deliver)")
			}
			if rt.styledOutputCalls != 0 {
				t.Fatalf("captures = %d, want 0", rt.styledOutputCalls)
			}
		})
	}
}

// hangingStyledRuntime is a runtime whose styled capture never returns until
// its context ends, like a wedged tmux server.
type hangingStyledRuntime struct{ *transitionRuntime }

func (hangingStyledRuntime) GetStyledOutput(ctx context.Context, _ ports.RuntimeHandle, _ int) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// recordingInputGate is a terminal input gate that also exposes the last
// accepted keystroke without closing input.
type recordingInputGate struct {
	lastInputAt time.Time
	drainCalls  int
}

func (g *recordingInputGate) BeginInputDrain(string) (time.Time, func()) {
	g.drainCalls++
	return g.lastInputAt, func() {}
}

func (g *recordingInputGate) LastInputAt(string) time.Time { return g.lastInputAt }

// drainOnlyInputGate only supports closing input, like an older gate.
type drainOnlyInputGate struct{ drainCalls int }

func (g *drainOnlyInputGate) BeginInputDrain(string) (time.Time, func()) {
	g.drainCalls++
	return time.Now(), func() {}
}

func TestComposerBusyCheckNeverClosesTerminalInput(t *testing.T) {
	// Closing the input barrier DROPS the operator's keystrokes (they are not
	// buffered), so reading the last keystroke for the probe must be read-only.
	m, rt, _ := draftProbeManager(transitionSurfaceAgent{}, idleTerminalOutput)
	m.interfaceTransition.idleSettle = 40 * time.Millisecond
	gate := &recordingInputGate{lastInputAt: time.Now()}
	m.SetTerminalInputGate(gate)

	m.composerBusyCheck()(context.Background(), draftProbeRecord(domain.ActivityIdle))

	if gate.drainCalls != 0 {
		t.Fatalf("the send probe closed terminal input %d time(s)", gate.drainCalls)
	}
	// The read-only keystroke time was honored: sampling waited for it to settle.
	if len(rt.outputCallTimes) == 0 || rt.outputCallTimes[0].Before(gate.lastInputAt.Add(m.interfaceTransition.idleSettle)) {
		t.Fatalf("probe ignored the last keystroke time: %v", rt.outputCallTimes)
	}

	drainOnly := &drainOnlyInputGate{}
	m.SetTerminalInputGate(drainOnly)
	m.composerBusyCheck()(context.Background(), draftProbeRecord(domain.ActivityIdle))
	if drainOnly.drainCalls != 0 {
		t.Fatalf("a gate without a read-only accessor was drained %d time(s)", drainOnly.drainCalls)
	}
}

func composerSendTestManager(t *testing.T, frame string) (*Manager, *fakeMessenger) {
	t.Helper()
	st := newFakeStore()
	st.sessions["s1"] = pastStartupGate(domain.SessionRecord{
		ID: "s1", Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now()},
		Metadata: domain.SessionMetadata{RuntimeHandleID: "h1"},
	})
	msg := &fakeMessenger{}
	m := newSendTestManager(t, transitionSurfaceAgent{}, msg, st)
	m.runtime = &transitionRuntime{fakeRuntime: &fakeRuntime{}, outputForCall: func(int) string { return frame }}
	m.interfaceTransition = fastComposerProbe
	return m, msg
}

func TestSend_TUIComposerDraftRejectsDelivery(t *testing.T) {
	m, msg := composerSendTestManager(t, draftTerminalOutput)
	err := m.Send(context.Background(), "s1", "run the tests", nil)
	if !errors.Is(err, ErrComposerBusy) {
		t.Fatalf("Send error = %v, want ErrComposerBusy", err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("Send wrote %d message(s) onto an unsent draft", len(msg.msgs))
	}
}

func TestSend_TUIEmptyComposerDelivers(t *testing.T) {
	m, msg := composerSendTestManager(t, idleTerminalOutput)
	if err := m.Send(context.Background(), "s1", "run the tests", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("Send calls = %d, want 1", len(msg.msgs))
	}
}

func TestSend_EnterOnlyResubmitSkipsTheComposerCheck(t *testing.T) {
	// An empty message is the deliberate Enter that submits the existing draft.
	m, msg := composerSendTestManager(t, draftTerminalOutput)
	if err := m.Send(context.Background(), "s1", "", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("Send calls = %d, want the Enter-only resubmit delivered", len(msg.msgs))
	}
}

func TestSend_InternalReportDeliverySkipsTheComposerCheck(t *testing.T) {
	// Report deliveries are AO-internal and have no durable queue behind a 409.
	m, msg := composerSendTestManager(t, draftTerminalOutput)
	report := domain.WrapReportDelivery("cm-1", "worker finished")
	if err := m.Send(context.Background(), "s1", report, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("Send calls = %d, want the internal report delivered", len(msg.msgs))
	}
}
