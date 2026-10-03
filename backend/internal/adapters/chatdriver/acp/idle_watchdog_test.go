package acp

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// startSilentTurn opens a conversation whose agent accepts a prompt and then goes
// silent (emits no session update) until release is closed, at which point it ends
// the turn normally. It returns the running conversation, the turn ref, and the
// release channel. It reproduces #4613's OpenCode symptom: a turn that stays
// Working with no provider output.
func startSilentTurn(t *testing.T) (ports.ChatConversation, ports.ChatTurnRef, *fakeAgent, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	agent := &fakeAgent{
		customPrompt: func(ctx context.Context, _ acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
			case <-ctx.Done():
				return acpsdk.PromptResponse{}, ctx.Err()
			}
		},
	}
	driver := New(Config{
		Harness:      domain.HarnessOpenCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.useTestProcess(fakeSpawn(agent))

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { conv.Close() })
	_ = nextEvent(t, conv.Events()) // controller.ready

	ref, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "hi"})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conv.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}
	<-started
	return conv, ref, agent, release
}

// A native ACP turn whose agent goes silent must surface a single non-terminal,
// turn-scoped "waiting" row, and must not be cancelled by the watchdog: when the
// agent finally responds the row settles and the turn completes normally.
func TestACPIdleWatchdogSurfacesSilentTurnThenSettles(t *testing.T) {
	prev := idleActivityTimeout
	idleActivityTimeout = 20 * time.Millisecond
	defer func() { idleActivityTimeout = prev }()

	conv, ref, _, release := startSilentTurn(t)
	itemID := idleStallItemID(ref.ProviderTurnID)

	// Watchdog surfaces the stall.
	var idle ports.ChatEvent
	for idle.Kind == "" {
		e := nextEvent(t, conv.Events())
		if e.Kind == ports.ChatEventActivityStarted && e.ProviderItemID == itemID {
			idle = e
		}
	}
	if idle.ActivityKind != domain.ActivityKindSystem {
		t.Fatalf("idle activity kind = %q, want %q", idle.ActivityKind, domain.ActivityKindSystem)
	}
	if idle.ActivityStatus != domain.ActivityStatusRunning {
		t.Fatalf("idle activity status = %q, want %q", idle.ActivityStatus, domain.ActivityStatusRunning)
	}
	if idle.Err != nil {
		t.Fatalf("idle row must not be terminal, got Err = %v", idle.Err)
	}
	if idle.ProviderTurnID != ref.ProviderTurnID {
		t.Fatalf("idle turn id = %q, want %q", idle.ProviderTurnID, ref.ProviderTurnID)
	}

	// Let the agent respond. The row settles before the turn completes, and the
	// turn ends completed rather than cancelled.
	close(release)
	settled := false
	for {
		e := nextEvent(t, conv.Events())
		if e.Kind == ports.ChatEventActivityCompleted && e.ProviderItemID == itemID {
			settled = true
			if e.ActivityStatus != domain.ActivityStatusCompleted {
				t.Fatalf("idle settle status = %q, want %q", e.ActivityStatus, domain.ActivityStatusCompleted)
			}
		}
		if e.Kind == ports.ChatEventTurnCompleted {
			if !settled {
				t.Fatal("turn completed before the idle row settled")
			}
			if e.TurnState != domain.TurnStateCompleted {
				t.Fatalf("turn state = %q, want completed (watchdog must not cancel the turn)", e.TurnState)
			}
			return
		}
	}
}

// A turn that stays lively (the agent keeps streaming updates) must never trip the
// watchdog: no idle row is surfaced.
func TestACPIdleWatchdogStaysQuietWhileAgentStreams(t *testing.T) {
	prev := idleActivityTimeout
	idleActivityTimeout = 40 * time.Millisecond
	defer func() { idleActivityTimeout = prev }()

	stop := make(chan struct{})
	done := make(chan struct{})
	agent := &fakeAgent{}
	agent.customPrompt = func(ctx context.Context, params acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
			case <-ctx.Done():
				return acpsdk.PromptResponse{}, ctx.Err()
			case <-ticker.C:
				// Keep streaming activity so the watchdog stays idle.
				_ = agentSessionUpdate(ctx, agent, params.SessionId)
			}
		}
	}
	driver := New(Config{
		Harness:      domain.HarnessOpenCode,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true},
		Probe:        func(context.Context) error { return nil },
		Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.useTestProcess(fakeSpawn(agent))

	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conv.Close()
	_ = nextEvent(t, conv.Events()) // controller.ready

	ref, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "hi"})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conv.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}

	// Stream for well over the idle window, then stop.
	time.AfterFunc(200*time.Millisecond, func() { close(stop) })
	itemID := idleStallItemID(ref.ProviderTurnID)
	for {
		e := nextEvent(t, conv.Events())
		if e.ProviderItemID == itemID {
			t.Fatalf("idle row surfaced for a turn that kept streaming: %+v", e)
		}
		if e.Kind == ports.ChatEventTurnCompleted {
			break
		}
	}
	<-done
}

// After a stall is surfaced, the first resumed provider signal must settle the
// "waiting" row promptly rather than leaving it running until the timer's next
// fire, which could be up to a full idle window away. Reproduces P2: without an
// immediate wake, recovery lags by nearly the whole idle timeout.
func TestACPIdleWatchdogRecoversPromptlyOnResumedActivity(t *testing.T) {
	prev := idleActivityTimeout
	// Large enough that a full-window recovery (the pre-fix behaviour) would clearly
	// exceed the promptness bound asserted below.
	idleActivityTimeout = 200 * time.Millisecond
	defer func() { idleActivityTimeout = prev }()

	conv, ref, agent, release := startSilentTurn(t)
	defer close(release)
	itemID := idleStallItemID(ref.ProviderTurnID)

	// Wait for the stall to surface.
	for {
		e := nextEvent(t, conv.Events())
		if e.Kind == ports.ChatEventActivityStarted && e.ProviderItemID == itemID {
			break
		}
	}

	// The agent produces output again. The row must settle as recovered right away.
	resumedAt := time.Now()
	if err := agentSessionUpdate(context.Background(), agent, acpsdk.SessionId(conv.ProviderConversationID())); err != nil {
		t.Fatalf("resume session update: %v", err)
	}
	for {
		e := nextEvent(t, conv.Events())
		if e.Kind == ports.ChatEventActivityCompleted && e.ProviderItemID == itemID {
			if e.ActivityStatus != domain.ActivityStatusRecovered {
				t.Fatalf("idle settle status = %q, want %q", e.ActivityStatus, domain.ActivityStatusRecovered)
			}
			if elapsed := time.Since(resumedAt); elapsed > idleActivityTimeout/2 {
				t.Fatalf("recovery took %v after activity resumed; the watchdog waited out the idle window instead of settling on the wake", elapsed)
			}
			return
		}
	}
}

// A watchdog tick that surfaces a stall must not race turn completion into a
// stranded "waiting" row: the started event may never arrive after the row's
// completed event. Reproduces P1, and is meant to run under -race. Each iteration
// times the turn's completion to coincide with the idle tick, then asserts the
// idle item's events are a well-formed started/completed pairing with no dangling
// running row.
func TestACPIdleWatchdogSurfaceDoesNotRaceCompletion(t *testing.T) {
	prev := idleActivityTimeout
	idleActivityTimeout = time.Millisecond
	defer func() { idleActivityTimeout = prev }()

	for i := 0; i < 100; i++ {
		// startSilentTurn's agent already accepts the prompt, blocks until release,
		// then ends the turn. Releasing it once the idle window has elapsed puts the
		// watchdog mid-surface while finishPrompt settles, which is the P1 race.
		conv, ref, _, release := startSilentTurn(t)
		itemID := idleStallItemID(ref.ProviderTurnID)
		time.AfterFunc(idleActivityTimeout, func() { close(release) })

		running := 0
		turnDone := false
		// Drain the turn, then keep reading briefly after completion so a stray
		// started event emitted by a racing watchdog tick cannot slip past unseen.
		checkEvent := func(e ports.ChatEvent) {
			switch {
			case e.ProviderItemID == itemID && e.Kind == ports.ChatEventActivityStarted:
				if turnDone {
					t.Fatalf("iteration %d: idle row started after the turn completed (stranded waiting row)", i)
				}
				running++
				if running > 1 {
					t.Fatalf("iteration %d: idle row started twice without settling", i)
				}
			case e.ProviderItemID == itemID && e.Kind == ports.ChatEventActivityCompleted:
				if running == 0 {
					t.Fatalf("iteration %d: idle row completed before it started", i)
				}
				running--
			case e.Kind == ports.ChatEventTurnCompleted:
				turnDone = true
			}
		}
		for !turnDone {
			checkEvent(nextEvent(t, conv.Events()))
		}
		grace := time.After(50 * time.Millisecond)
	drain:
		for {
			select {
			case e := <-conv.Events():
				checkEvent(e)
			case <-grace:
				break drain
			}
		}
		// A started row must have been settled by the time the turn completes.
		if running != 0 {
			t.Fatalf("iteration %d: turn completed with %d unsettled idle row(s)", i, running)
		}
	}
}

// agentSessionUpdate sends one streamed message chunk from the fake agent so the
// driver records provider activity.
func agentSessionUpdate(ctx context.Context, agent *fakeAgent, session acpsdk.SessionId) error {
	return agent.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
		SessionId: session,
		Update:    acpsdk.UpdateAgentMessageText("working"),
	})
}
