package activity

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeSessions struct {
	rows []domain.SessionRecord
	err  error
}

func (f fakeSessions) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return f.rows, f.err
}

type fakeSink struct {
	id      domain.SessionID
	signals []ports.ActivitySignal
}

func (f *fakeSink) ApplyActivitySignal(_ context.Context, id domain.SessionID, signal ports.ActivitySignal) error {
	f.id = id
	f.signals = append(f.signals, signal)
	return nil
}

type fakeRuntime struct {
	output string
	err    error
	calls  int
}

func (f *fakeRuntime) GetOutput(context.Context, ports.RuntimeHandle, int) (string, error) {
	f.calls++
	return f.output, f.err
}

type fakeAgents map[domain.AgentHarness]ports.Agent

func (f fakeAgents) Agent(harness domain.AgentHarness) (ports.Agent, bool) {
	agent, ok := f[harness]
	return agent, ok
}

// detectorAgent wraps the opencode plugin with the terminal-activity detector
// capabilities the observer branches on. Only the opencode adapter ships today,
// so terminal-screen reconciliation is exercised through this test double with
// the capability matrix the deleted TUI adapters used to provide.
type detectorAgent struct {
	*opencode.Plugin
	detect          func(string) (domain.ActivityState, bool)
	continuous      bool
	waitingDetector bool
}

func (d detectorAgent) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	if d.detect == nil {
		return "", false
	}
	return d.detect(output)
}

func (d detectorAgent) ContinuouslyDetectTerminalActivity() bool { return d.continuous }

func (d detectorAgent) ContinuouslyDetectTerminalActivityWhileWaiting() bool {
	return d.waitingDetector
}

func activeSession(now time.Time, harness domain.AgentHarness) domain.SessionRecord {
	return domain.SessionRecord{
		ID:        "ao-1",
		Harness:   harness,
		Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now.Add(-3 * time.Minute)},
		UpdatedAt: now.Add(-3 * time.Minute),
		Metadata: domain.SessionMetadata{
			RuntimeHandleID: "ao-1",
			RuntimeLaunchID: "launch-1",
		},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPollReconcilesStaleActiveAtComposer(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	session := activeSession(now, domain.HarnessOpenCode)
	sink := &fakeSink{}
	runtime := &fakeRuntime{output: "› Write tests for @filename\n\ngpt-5.6-sol low · ~/project\n"}
	observer := New(
		fakeSessions{rows: []domain.SessionRecord{session}},
		sink,
		runtime,
		fakeAgents{domain.HarnessOpenCode: detectorAgent{
			Plugin:     opencode.New(),
			continuous: true,
			detect: func(output string) (domain.ActivityState, bool) {
				if strings.HasPrefix(output, "›") {
					return domain.ActivityIdle, true
				}
				return domain.ActivityActive, true
			},
		}},
		Config{Clock: func() time.Time { return now }, Logger: testLogger()},
	)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.signals) != 1 {
		t.Fatalf("signals = %d, want 1", len(sink.signals))
	}
	signal := sink.signals[0]
	if sink.id != session.ID || signal.State != domain.ActivityIdle || signal.Event != "terminal-idle" {
		t.Fatalf("unexpected reconciliation: id=%q signal=%+v", sink.id, signal)
	}
	if signal.ExpectedRevision == nil || *signal.ExpectedRevision != session.Revision || signal.LaunchID != "launch-1" {
		t.Fatalf("reconciliation fence = %+v, want revision=%d launch=launch-1", signal, session.Revision)
	}
}

func TestPollKeepsGenuineLongTurnActive(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	sink := &fakeSink{}
	runtime := &fakeRuntime{output: "• Working (3m 10s • esc to interrupt)\n› Add tests\n\ngpt-5.6-sol low · ~/project\n"}
	observer := New(
		fakeSessions{rows: []domain.SessionRecord{activeSession(now, domain.HarnessOpenCode)}},
		sink,
		runtime,
		fakeAgents{domain.HarnessOpenCode: detectorAgent{
			Plugin:     opencode.New(),
			continuous: true,
			detect: func(output string) (domain.ActivityState, bool) {
				return domain.ActivityActive, true
			},
		}},
		Config{Clock: func() time.Time { return now }, Logger: testLogger()},
	)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.signals) != 0 {
		t.Fatalf("long active turn emitted reconciliation: %+v", sink.signals)
	}
}

func TestPollContinuouslyReconcilesWaitingInput(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	tests := []struct {
		name    string
		current domain.ActivityState
		output  string
		want    domain.ActivityState
		event   string
	}{
		{"fresh active to waiting", domain.ActivityActive, "◆ Request user input Fix  1m 02s)\n", domain.ActivityWaitingInput, "terminal-waiting-input"},
		{"waiting to active", domain.ActivityWaitingInput, "◇ Finishing up (25s · esc to interrupt)\n", domain.ActivityActive, "terminal-active"},
		{"idle to waiting", domain.ActivityIdle, "Enter to select · ↑/↓ to move · Tab for an optional note · Esc to interrupt\n", domain.ActivityWaitingInput, "terminal-waiting-input"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := activeSession(now, domain.HarnessOpenCode)
			session.Activity = domain.Activity{State: tt.current, LastActivityAt: now.Add(-time.Second)}
			session.UpdatedAt = now.Add(-time.Second)
			sink := &fakeSink{}
			observer := New(
				fakeSessions{rows: []domain.SessionRecord{session}},
				sink,
				&fakeRuntime{output: tt.output},
				fakeAgents{domain.HarnessOpenCode: detectorAgent{
					Plugin:     opencode.New(),
					continuous: true,
					detect: func(output string) (domain.ActivityState, bool) {
						if strings.HasPrefix(output, "◆") {
							return domain.ActivityWaitingInput, true
						}
						if strings.HasPrefix(output, "◇") {
							return domain.ActivityActive, true
						}
						return domain.ActivityWaitingInput, true
					},
				}},
				Config{Clock: func() time.Time { return now }, Logger: testLogger()},
			)

			if err := observer.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(sink.signals) != 1 || sink.signals[0].State != tt.want || sink.signals[0].Event != tt.event {
				t.Fatalf("unexpected reconciliation: %+v", sink.signals)
			}
		})
	}
}

func TestPollReconcilesWaitingStateAfterUserResponds(t *testing.T) {
	for _, tt := range []struct {
		name   string
		output string
		want   domain.ActivityState
	}{
		{name: "resumed active", output: "> Working!\n", want: domain.ActivityActive},
		{name: "resumed idle", output: "> Ready?\n", want: domain.ActivityIdle},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(500, 0).UTC()
			session := activeSession(now, domain.HarnessOpenCode)
			session.Activity = domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: now.Add(-time.Second)}
			session.UpdatedAt = now.Add(-time.Second)
			sink := &fakeSink{}
			observer := New(
				fakeSessions{rows: []domain.SessionRecord{session}},
				sink,
				&fakeRuntime{output: tt.output},
				fakeAgents{domain.HarnessOpenCode: detectorAgent{
					Plugin:     opencode.New(),
					continuous: true,
					detect: func(output string) (domain.ActivityState, bool) {
						if strings.Contains(output, "Ready?") {
							return domain.ActivityIdle, true
						}
						return domain.ActivityActive, true
					},
				}},
				Config{Clock: func() time.Time { return now }, Logger: testLogger()},
			)

			if err := observer.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(sink.signals) != 1 || sink.signals[0].State != tt.want {
				t.Fatalf("unexpected reconciliation: %+v", sink.signals)
			}
		})
	}
}

func TestPollPreservesWaitingInputWithoutContinuousCapability(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	session := activeSession(now, domain.HarnessOpenCode)
	session.Activity = domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: now.Add(-time.Second)}
	session.UpdatedAt = now.Add(-time.Second)
	sink := &fakeSink{}
	runtime := &fakeRuntime{output: stuckActiveScreen}
	observer := New(
		fakeSessions{rows: []domain.SessionRecord{session}},
		sink,
		runtime,
		fakeAgents{domain.HarnessOpenCode: detectorAgent{
			Plugin:     opencode.New(),
			continuous: false,
			detect: func(output string) (domain.ActivityState, bool) {
				return domain.ActivityIdle, true
			},
		}},
		Config{Clock: func() time.Time { return now }, Logger: testLogger()},
	)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 0 || len(sink.signals) != 0 {
		t.Fatalf("sticky waiting state was sampled: output calls=%d signals=%+v", runtime.calls, sink.signals)
	}
}

func TestPollLeavesHarnessesWithoutTerminalDetectionUntouched(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	sink := &fakeSink{}
	runtime := &fakeRuntime{output: "› prompt\nmodel · ~/project\n"}
	observer := New(
		fakeSessions{rows: []domain.SessionRecord{activeSession(now, domain.HarnessOpenCode)}},
		sink,
		runtime,
		// A plain opencode plugin carries no TerminalActivityDetector
		// capability, so the observer must not sample its terminal.
		fakeAgents{domain.HarnessOpenCode: opencode.New()},
		Config{Clock: func() time.Time { return now }, Logger: testLogger()},
	)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 0 || len(sink.signals) != 0 {
		t.Fatalf("unaffected harness: output calls=%d signals=%+v", runtime.calls, sink.signals)
	}
}

// stuckActiveScreen is a rendered TUI surface after a turn aborted without its
// Stop signal (expired login): idle composer holding an unsent draft, provider
// footer below, no active chrome. This is the screen a session stranded in
// durable "active" actually shows.
const stuckActiveScreen = "⏺ Login expired · Please run /login\n" +
	"\n" +
	"✻ Worked for 0s\n" +
	"\n" +
	"────────────────────────────────────────────────\n" +
	"❯ so btw, the status isn't permanently stuck\n" +
	"────────────────────────────────────────────────\n" +
	"\n" +
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · PR #4090\n"

func TestPollReconcilesStaleActiveAfterAbortedTurn(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	session := activeSession(now, domain.HarnessOpenCode)
	sink := &fakeSink{}
	runtime := &fakeRuntime{output: stuckActiveScreen}
	observer := New(
		fakeSessions{rows: []domain.SessionRecord{session}},
		sink,
		runtime,
		fakeAgents{domain.HarnessOpenCode: detectorAgent{
			Plugin:     opencode.New(),
			continuous: false,
			detect: func(output string) (domain.ActivityState, bool) {
				return domain.ActivityIdle, true
			},
		}},
		Config{Clock: func() time.Time { return now }, Logger: testLogger()},
	)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.signals) != 1 {
		t.Fatalf("signals = %d, want 1", len(sink.signals))
	}
	signal := sink.signals[0]
	if sink.id != session.ID || signal.State != domain.ActivityIdle || signal.Event != "terminal-idle" {
		t.Fatalf("unexpected reconciliation: id=%q signal=%+v", sink.id, signal)
	}
	if signal.ExpectedRevision == nil || *signal.ExpectedRevision != session.Revision || signal.LaunchID != "launch-1" {
		t.Fatalf("reconciliation fence = %+v, want revision=%d launch=launch-1", signal, session.Revision)
	}
}

func TestPollKeepsGenuineLongComputingTurnActive(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	sink := &fakeSink{}
	runtime := &fakeRuntime{output: "✻ Computing… (3m 10s · ↓ 114 tokens)\n" +
		"────────────────────────────────────────────────\n" +
		"❯\n" +
		"────────────────────────────────────────────────\n" +
		"⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents\n"}
	observer := New(
		fakeSessions{rows: []domain.SessionRecord{activeSession(now, domain.HarnessOpenCode)}},
		sink,
		runtime,
		fakeAgents{domain.HarnessOpenCode: detectorAgent{
			Plugin:     opencode.New(),
			continuous: false,
			detect: func(output string) (domain.ActivityState, bool) {
				return domain.ActivityActive, true
			},
		}},
		Config{Clock: func() time.Time { return now }, Logger: testLogger()},
	)

	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.signals) != 0 {
		t.Fatalf("long active turn emitted reconciliation: %+v", sink.signals)
	}
}

func TestPollSkipsFreshActiveAndOutputFailures(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	fresh := activeSession(now, domain.HarnessOpenCode)
	fresh.Activity.LastActivityAt = now.Add(-time.Minute)
	runtime := &fakeRuntime{err: errors.New("capture failed")}
	observer := New(
		fakeSessions{rows: []domain.SessionRecord{fresh}},
		&fakeSink{},
		runtime,
		fakeAgents{domain.HarnessOpenCode: detectorAgent{
			Plugin:     opencode.New(),
			continuous: false,
			detect: func(output string) (domain.ActivityState, bool) {
				return domain.ActivityIdle, true
			},
		}},
		Config{Clock: func() time.Time { return now }, Logger: testLogger()},
	)
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 0 {
		t.Fatalf("fresh session output calls = %d, want 0", runtime.calls)
	}

	stale := activeSession(now, domain.HarnessOpenCode)
	observer.sessions = fakeSessions{rows: []domain.SessionRecord{stale}}
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPollReturnsSessionListFailure(t *testing.T) {
	want := errors.New("list failed")
	observer := New(fakeSessions{err: want}, &fakeSink{}, &fakeRuntime{}, nil, Config{Logger: testLogger()})
	if err := observer.Poll(context.Background()); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
