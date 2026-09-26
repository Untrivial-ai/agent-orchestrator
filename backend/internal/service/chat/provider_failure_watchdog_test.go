package chat_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

const staleProviderFailureError = "Connection to Claude was interrupted and never confirmed completion after reconnecting. Please retry."

const sharedProviderHostRestartError = "This turn was interrupted because AO restarted the shared Claude connection after another turn stopped responding. Please retry."

func TestProviderFailureWatchdogOnlySettlesStaleReconnect(t *testing.T) {
	tests := []struct {
		name             string
		reconnecting     bool
		reconnectSettled bool
		age              time.Duration
		wantState        domain.TurnState
		wantController   ports.ChatControllerState
		wantActivityFail bool
	}{
		{
			name:             "stale reconnect",
			reconnecting:     true,
			age:              chatsvc.StaleProviderFailureTimeout + time.Second,
			wantState:        domain.TurnStateFailed,
			wantController:   ports.ChatControllerReady,
			wantActivityFail: true,
		},
		{
			name:           "fresh reconnect",
			reconnecting:   true,
			age:            chatsvc.StaleProviderFailureTimeout - time.Second,
			wantState:      domain.TurnStateRunning,
			wantController: ports.ChatControllerBusy,
		},
		{
			name:             "completed reconnect activity followed by silence",
			reconnecting:     true,
			reconnectSettled: true,
			age:              2 * chatsvc.StaleProviderFailureTimeout,
			wantState:        domain.TurnStateFailed,
			wantController:   ports.ChatControllerReady,
		},
		{
			name:           "no reconnect activity",
			age:            2 * chatsvc.StaleProviderFailureTimeout,
			wantState:      domain.TurnStateRunning,
			wantController: ports.ChatControllerBusy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newPeriodicWatchdogHarness(t)
			turn, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "keep working"})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			h.conv.emit(ports.ChatEvent{
				Kind: ports.ChatEventTurnStarted, ProviderEventID: "turn-started",
				ProviderTurnID:         turn.ProviderTurnID,
				ProviderConversationID: h.conv.ProviderConversationID(),
			})
			if tt.reconnecting {
				h.conv.emit(ports.ChatEvent{
					Kind: ports.ChatEventActivityStarted, ProviderEventID: "provider-failure",
					ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "provider-failure",
					ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusRunning,
					Summary: "Reconnecting to Claude, attempt 1 of 10",
					Detail:  []byte(`{"event":"provider.failure"}`),
				})
				if tt.reconnectSettled {
					h.conv.emit(ports.ChatEvent{
						Kind: ports.ChatEventActivityCompleted, ProviderEventID: "provider-failure-completed",
						ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "provider-failure",
						ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusCompleted,
						Summary: "Connection restored",
						Detail:  []byte(`{"event":"provider.failure"}`),
					})
				}
			}
			h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
				if len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateRunning {
					return false
				}
				if !tt.reconnecting {
					return true
				}
				if len(snapshot.Activities) != 1 {
					return false
				}
				return !tt.reconnectSettled || snapshot.Activities[0].Status == domain.ActivityStatusCompleted
			})

			h.advance(tt.age)
			if err := h.svc.RecoverStaleProviderFailure(context.Background(), testSession, h.now()); err != nil {
				t.Fatalf("RecoverStaleProviderFailure: %v", err)
			}
			snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
			if err != nil {
				t.Fatalf("LoadConversationSnapshot: %v", err)
			}
			if got := snapshot.Turns[0].State; got != tt.wantState {
				t.Fatalf("turn state = %q, want %q", got, tt.wantState)
			}
			current, controllerErr := h.svc.Controller(testSession)
			if controllerErr != nil {
				t.Fatalf("Controller after recovery: %v", controllerErr)
			}
			if got := current.State(); got != tt.wantController {
				t.Fatalf("controller state = %q, want %q", got, tt.wantController)
			}
			if tt.wantActivityFail {
				if got := snapshot.Turns[0].ErrorMessage; got != staleProviderFailureError {
					t.Fatalf("turn error = %q, want %q", got, staleProviderFailureError)
				}
				if got := findActivity(t, snapshot, "provider-failure").Status; got != domain.ActivityStatusFailed {
					t.Fatalf("provider failure activity = %q, want %q", got, domain.ActivityStatusFailed)
				}
				if !hasActivitySignal(h.activity.snapshot(), domain.ActivityIdle, "chat.turn.completed") {
					t.Fatalf("watchdog did not publish idle lifecycle signal: %+v", h.activity.snapshot())
				}
			} else if tt.reconnecting {
				want := domain.ActivityStatusRunning
				if tt.reconnectSettled {
					want = domain.ActivityStatusCompleted
				}
				if got := findActivity(t, snapshot, "provider-failure").Status; got != want {
					t.Fatalf("provider failure activity = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestProviderFailureWatchdogSettlesAfterOutputClosedReconnectMarker(t *testing.T) {
	h, _, _ := newPeriodicWatchdogHarness(t)
	turn, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "keep working"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderEventID: "resumed-turn-started",
			ProviderTurnID: turn.ProviderTurnID, ProviderConversationID: h.conv.ProviderConversationID(),
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderEventID: "resumed-provider-failure",
			ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "resumed-provider-failure",
			ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusRunning,
			Summary: "Reconnecting to Claude, attempt 2 of 10",
			Detail:  []byte(`{"event":"provider.failure"}`),
		},
		// ACP emits this as soon as provider output resumes, before the output event.
		ports.ChatEvent{
			Kind: ports.ChatEventActivityCompleted, ProviderEventID: "resumed-provider-failure-completed",
			ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "resumed-provider-failure",
			ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusCompleted,
			Summary: "Reconnecting to Claude, attempt 2 of 10",
			Detail:  []byte(`{"event":"provider.failure"}`),
		},
		ports.ChatEvent{
			Kind: ports.ChatEventMessageDelta, ProviderEventID: "resumed-output",
			ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "resumed-assistant",
			Delta: "Output resumed, but no terminal event will follow.",
		},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Turns) == 1 && snapshot.Turns[0].State == domain.TurnStateRunning &&
			len(snapshot.Messages) == 2 && snapshot.Messages[1].Streaming &&
			len(snapshot.Activities) == 1 && snapshot.Activities[0].ProviderItemID == "resumed-provider-failure" &&
			snapshot.Activities[0].Status == domain.ActivityStatusCompleted
	})

	h.advance(chatsvc.StaleProviderFailureTimeout + time.Second)
	if err := h.svc.RecoverStaleProviderFailure(context.Background(), testSession, h.now()); err != nil {
		t.Fatalf("RecoverStaleProviderFailure: %v", err)
	}
	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if got := snapshot.Turns[0].State; got != domain.TurnStateFailed {
		t.Fatalf("turn state = %q, want %q", got, domain.TurnStateFailed)
	}
	if snapshot.Messages[1].Streaming {
		t.Fatal("assistant message remained streaming after stale resumed output")
	}
}

func TestPeriodicProviderFailureRecoveryReplacesHostBeforeNextPrompt(t *testing.T) {
	h, staleHost, replacement := newPeriodicWatchdogHarness(t)
	turn, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "finish the first task"})
	if err != nil {
		t.Fatalf("Send first turn: %v", err)
	}
	h.conv.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderEventID: "next-prompt-turn-started",
			ProviderTurnID: turn.ProviderTurnID, ProviderConversationID: h.conv.ProviderConversationID(),
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderEventID: "next-prompt-provider-failure",
			ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "next-prompt-provider-failure",
			ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusRunning,
			Summary: "Reconnecting to Claude, attempt 10 of 10",
			Detail:  []byte(`{"event":"provider.failure"}`),
		},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Turns) == 1 && snapshot.Turns[0].State == domain.TurnStateRunning &&
			len(snapshot.Activities) == 1
	})
	h.advance(chatsvc.StaleProviderFailureTimeout + time.Second)

	if err := h.svc.RecoverStaleProviderFailure(context.Background(), testSession, h.now()); err != nil {
		t.Fatalf("RecoverStaleProviderFailure: %v", err)
	}
	if !staleHost.terminated.Load() {
		t.Fatal("periodic recovery did not terminate the stale provider host")
	}
	pending, err := h.st.ProviderHostTerminationPending(
		context.Background(), h.ctrl.ConversationID(), testSession)
	if err != nil || pending {
		t.Fatalf("termination pending after periodic replacement = %v, err=%v; want false", pending, err)
	}

	next, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{Text: "start the next task"})
	if err != nil {
		t.Fatalf("Send next turn after periodic recovery: %v", err)
	}
	if next.State != domain.TurnStateRunning {
		t.Fatalf("next turn state = %q, want %q", next.State, domain.TurnStateRunning)
	}
	if got := replacement.sentTexts(); len(got) != 1 || got[0] != "start the next task" {
		t.Fatalf("replacement provider sends = %#v, want next prompt", got)
	}
}

func TestProviderFailureWatchdogAtomicallyRejectsProgressAtSettlementBoundary(t *testing.T) {
	var hookedStore *watchdogProjectionHookStore
	h := newHarnessWithConversationAndStore(t, nil, func(st *sqlite.Store) chatsvc.Store {
		hookedStore = &watchdogProjectionHookStore{Store: st}
		return hookedStore
	})
	turn, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "keep working"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderEventID: "atomic-race-turn-started",
			ProviderTurnID: turn.ProviderTurnID, ProviderConversationID: h.conv.ProviderConversationID(),
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderEventID: "atomic-race-provider-failure",
			ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "atomic-race-provider-failure",
			ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusRunning,
			Summary: "Reconnecting to Claude, attempt 10 of 10",
			Detail:  []byte(`{"event":"provider.failure"}`),
		},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Turns) == 1 && snapshot.Turns[0].State == domain.TurnStateRunning &&
			len(snapshot.Activities) == 1
	})
	h.advance(chatsvc.StaleProviderFailureTimeout + time.Second)

	hookedStore.beforeWatchdogProjection = func(ctx context.Context) error {
		return h.st.AppendAssistantDelta(
			ctx,
			h.ctrl.ConversationID(),
			"atomic-race-progress",
			turn.ProviderTurnID,
			"progress committed at the settlement boundary",
			"atomic-race-progress-message",
			h.now(),
		)
	}
	if err := h.svc.RecoverStaleProviderFailure(context.Background(), testSession, h.now()); err != nil {
		t.Fatalf("RecoverStaleProviderFailure: %v", err)
	}
	if !hookedStore.fired.Load() {
		t.Fatal("watchdog projection hook did not run")
	}

	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if got := snapshot.Turns[0].State; got != domain.TurnStateRunning {
		t.Fatalf("turn state after boundary progress = %q, want %q", got, domain.TurnStateRunning)
	}
	if got := h.ctrl.State(); got != ports.ChatControllerBusy {
		t.Fatalf("controller state after boundary progress = %q, want %q", got, ports.ChatControllerBusy)
	}
	progressFound := false
	for _, message := range snapshot.Messages {
		if message.ProviderItemID == "atomic-race-progress" {
			progressFound = message.Streaming && message.Text == "progress committed at the settlement boundary"
		}
	}
	if !progressFound {
		t.Fatalf("boundary progress was not durably preserved: %+v", snapshot.Messages)
	}
}

func TestProviderFailureWatchdogPreservesFailureAfterLateProviderCompletion(t *testing.T) {
	h, _, replacement := newPeriodicWatchdogHarness(t)
	turn, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "keep working"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderEventID: "late-turn-started",
			ProviderTurnID:         turn.ProviderTurnID,
			ProviderConversationID: h.conv.ProviderConversationID(),
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderEventID: "late-provider-failure",
			ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "late-provider-failure",
			ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusRunning,
			Summary: "Reconnecting to Claude, attempt 10 of 10",
			Detail:  []byte(`{"event":"provider.failure"}`),
		},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Turns) == 1 && snapshot.Turns[0].State == domain.TurnStateRunning &&
			len(snapshot.Activities) == 1
	})
	h.advance(chatsvc.StaleProviderFailureTimeout + time.Second)
	if err := h.svc.RecoverStaleProviderFailure(context.Background(), testSession, h.now()); err != nil {
		t.Fatalf("RecoverStaleProviderFailure: %v", err)
	}

	// The terminal notification this watchdog exists to replace may eventually
	// arrive after the timeout. A following marker proves the projector processed
	// that late notification before the durable turn is inspected.
	replacement.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnCompleted, ProviderEventID: "late-turn-completed",
			ProviderTurnID: turn.ProviderTurnID, ProviderConversationID: h.conv.ProviderConversationID(),
			TurnState: domain.TurnStateCompleted,
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderEventID: "late-completion-marker",
			ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "late-completion-marker",
			ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusCompleted,
			Summary: "Late completion processed",
		},
	)
	snapshot := h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		for _, activity := range snapshot.Activities {
			if activity.ProviderItemID == "late-completion-marker" {
				return true
			}
		}
		return false
	})
	if got := snapshot.Turns[0].State; got != domain.TurnStateFailed {
		t.Fatalf("turn state after late provider completion = %q, want %q", got, domain.TurnStateFailed)
	}
	if got := snapshot.Turns[0].ErrorMessage; got != staleProviderFailureError {
		t.Fatalf("turn error after late provider completion = %q, want %q", got, staleProviderFailureError)
	}
}

func TestLiveReconnectFailsStaleProviderFailureBeforeRestoringBusy(t *testing.T) {
	firstProvider := &terminatingConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, firstProvider)
	turn, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "keep working"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	firstProvider.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderEventID: "restart-turn-started",
			ProviderTurnID:         turn.ProviderTurnID,
			ProviderConversationID: firstProvider.ProviderConversationID(),
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderEventID: "restart-provider-failure",
			ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "restart-provider-failure",
			ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusRunning,
			Summary: "Reconnecting to Claude, attempt 10 of 10",
			Detail:  []byte(`{"event":"provider.failure"}`),
		},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Turns) == 1 && snapshot.Turns[0].State == domain.TurnStateRunning &&
			len(snapshot.Activities) == 1
	})
	h.svc.StopAll(context.Background())
	h.advance(chatsvc.StaleProviderFailureTimeout + time.Second)

	record, found, err := h.st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("GetSession before restart: found=%v err=%v", found, err)
	}
	record.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: h.now().Add(-time.Minute)}
	record.Metadata.ProviderConversationID = firstProvider.ProviderConversationID()
	if err := h.st.UpdateSession(context.Background(), record); err != nil {
		t.Fatalf("UpdateSession before restart: %v", err)
	}

	staleProvider := &watchdogRejectingLiveConversation{
		terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
	}
	freshProvider := &nativeHistoryConversation{fakeConversation: newFakeConversation()}
	restartDriver := &watchdogSequenceDriver{conversations: []ports.ChatConversation{staleProvider, freshProvider}}
	lcm := lifecycle.New(h.st, nil)
	var ids atomic.Int32
	second := chatsvc.New(chatsvc.Options{
		Store: h.st, Reader: fullSnapshotReader(h.st), Sessions: h.st,
		Drivers:  fakeRegistry{driver: restartDriver},
		Activity: lcm,
		Log:      slog.New(slog.DiscardHandler),
		NewID: func() string {
			return fmt.Sprintf("restart-watchdog-%d", ids.Add(1))
		},
		Now: h.now,
	})
	t.Cleanup(func() { second.StopAll(context.Background()) })
	controller, err := second.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: firstProvider.ProviderConversationID(),
		ControllerReady: func(result chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			return chatsvc.ControllerCommit{}, lcm.MarkChatReconnected(context.Background(), testSession, domain.SessionMetadata{
				ProviderConversationID: result.ProviderConversationID,
				ControllerGeneration:   result.ControllerGeneration,
			})
		},
	})
	if err != nil {
		t.Fatalf("restart Start: %v", err)
	}
	if got := staleProvider.activationCalls.Load(); got != 0 {
		t.Fatalf("stale live provider activation calls = %d, want 0", got)
	}
	if !staleProvider.terminated.Load() {
		t.Fatal("stale live provider was not terminated before native resume")
	}
	if got := restartDriver.resumeCalls.Load(); got != 2 {
		t.Fatalf("provider resume calls = %d, want live attachment plus fresh resume", got)
	}
	if got := controller.State(); got != ports.ChatControllerReady {
		t.Fatalf("restored controller state = %q, want %q", got, ports.ChatControllerReady)
	}
	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), controller.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot after restart: %v", err)
	}
	if got := snapshot.Turns[0].State; got != domain.TurnStateFailed {
		t.Fatalf("restored turn state = %q, want %q", got, domain.TurnStateFailed)
	}
	if got := snapshot.Turns[0].ErrorMessage; got != staleProviderFailureError {
		t.Fatalf("restored turn error = %q, want %q", got, staleProviderFailureError)
	}
	if got := findActivity(t, snapshot, "restart-provider-failure").Status; got != domain.ActivityStatusFailed {
		t.Fatalf("restored provider failure activity = %q, want %q", got, domain.ActivityStatusFailed)
	}
	record, found, err = h.st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("GetSession after restart: found=%v err=%v", found, err)
	}
	if record.Activity.State != domain.ActivityIdle {
		t.Fatalf("session activity after restart = %q, want %q", record.Activity.State, domain.ActivityIdle)
	}
}

func TestLiveReconnectFailsOtherRunningTurnsBeforeTerminatingSharedHost(t *testing.T) {
	firstProvider := &terminatingConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, firstProvider)
	root, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "keep working"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	firstProvider.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderEventID: "shared-root-started",
			ProviderTurnID: root.ProviderTurnID, ProviderConversationID: firstProvider.ProviderConversationID(),
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderEventID: "shared-root-provider-failure",
			ProviderTurnID: root.ProviderTurnID, ProviderItemID: "shared-root-provider-failure",
			ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusRunning,
			Summary: "Reconnecting to Claude, attempt 10 of 10",
			Detail:  []byte(`{"event":"provider.failure"}`),
		},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Turns) == 1 && snapshot.Turns[0].State == domain.TurnStateRunning &&
			len(snapshot.Activities) == 1
	})

	// The root goes stale, then fresh nested work starts on the same connection.
	// Its later requested_at makes it the row the old newest-turn heuristic would
	// have restored, hiding the stale root entirely.
	h.advance(chatsvc.StaleProviderFailureTimeout + time.Second)
	const nestedProviderTurnID = "healthy-nested-turn"
	firstProvider.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderEventID: "healthy-nested-started",
			ProviderTurnID: nestedProviderTurnID, ProviderConversationID: "nested-provider-thread",
		},
		ports.ChatEvent{
			Kind: ports.ChatEventMessageDelta, ProviderEventID: "healthy-nested-output",
			ProviderTurnID: nestedProviderTurnID, ProviderConversationID: "nested-provider-thread",
			ProviderItemID: "healthy-nested-message", Delta: "still making progress",
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderEventID: "healthy-nested-tool-started",
			ProviderTurnID: nestedProviderTurnID, ProviderConversationID: "nested-provider-thread",
			ProviderItemID: "healthy-nested-tool", ActivityKind: domain.ActivityKindCommand,
			ActivityStatus: domain.ActivityStatusRunning, Summary: "Healthy nested tool",
		},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		if len(snapshot.Turns) != 2 {
			return false
		}
		turnReady := false
		for _, turn := range snapshot.Turns {
			if turn.ProviderTurnID == nestedProviderTurnID {
				turnReady = turn.State == domain.TurnStateRunning && turn.RequestedAt.After(root.RequestedAt)
			}
		}
		messageReady := false
		for _, message := range snapshot.Messages {
			if message.ProviderItemID == "healthy-nested-message" {
				messageReady = message.Streaming
			}
		}
		activityReady := false
		for _, activity := range snapshot.Activities {
			if activity.ProviderItemID == "healthy-nested-tool" {
				activityReady = activity.Status == domain.ActivityStatusRunning
			}
		}
		return turnReady && messageReady && activityReady
	})
	h.svc.StopAll(context.Background())

	record, found, err := h.st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("GetSession before restart: found=%v err=%v", found, err)
	}
	record.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: h.now()}
	record.Metadata.ProviderConversationID = firstProvider.ProviderConversationID()
	if err := h.st.UpdateSession(context.Background(), record); err != nil {
		t.Fatalf("UpdateSession before restart: %v", err)
	}

	staleProvider := &watchdogRejectingLiveConversation{
		terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
	}
	freshProvider := &nativeHistoryConversation{fakeConversation: newFakeConversation()}
	restartDriver := &watchdogSequenceDriver{conversations: []ports.ChatConversation{staleProvider, freshProvider}}
	lcm := lifecycle.New(h.st, nil)
	var ids atomic.Int32
	second := chatsvc.New(chatsvc.Options{
		Store: h.st, Reader: fullSnapshotReader(h.st), Sessions: h.st,
		Drivers:  fakeRegistry{driver: restartDriver},
		Activity: lcm,
		Log:      slog.New(slog.DiscardHandler),
		NewID: func() string {
			return fmt.Sprintf("shared-host-watchdog-%d", ids.Add(1))
		},
		Now: h.now,
	})
	t.Cleanup(func() { second.StopAll(context.Background()) })
	controller, err := second.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: firstProvider.ProviderConversationID(),
		ControllerReady: func(result chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			return chatsvc.ControllerCommit{}, lcm.MarkChatReconnected(context.Background(), testSession, domain.SessionMetadata{
				ProviderConversationID: result.ProviderConversationID,
				ControllerGeneration:   result.ControllerGeneration,
			})
		},
	})
	if err != nil {
		t.Fatalf("restart Start: %v", err)
	}
	if !staleProvider.terminated.Load() {
		t.Fatal("shared stale provider host was not terminated")
	}
	if got := staleProvider.activationCalls.Load(); got != 0 {
		t.Fatalf("stale live provider activation calls = %d, want 0", got)
	}

	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), controller.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot after restart: %v", err)
	}
	if len(snapshot.Turns) != 2 {
		t.Fatalf("turn count = %d, want 2", len(snapshot.Turns))
	}
	for _, turn := range snapshot.Turns {
		if turn.State == domain.TurnStateRunning {
			t.Fatalf("turn %q remained running after its shared host was terminated", turn.ProviderTurnID)
		}
		if turn.State != domain.TurnStateFailed {
			t.Fatalf("turn %q state = %q, want %q", turn.ProviderTurnID, turn.State, domain.TurnStateFailed)
		}
		switch turn.ProviderTurnID {
		case root.ProviderTurnID:
			if turn.ErrorMessage != staleProviderFailureError {
				t.Fatalf("stale root error = %q, want %q", turn.ErrorMessage, staleProviderFailureError)
			}
		case nestedProviderTurnID:
			if turn.ErrorMessage != sharedProviderHostRestartError {
				t.Fatalf("nested turn error = %q, want %q", turn.ErrorMessage, sharedProviderHostRestartError)
			}
		default:
			t.Fatalf("unexpected provider turn %q", turn.ProviderTurnID)
		}
	}
	for _, message := range snapshot.Messages {
		if message.ProviderItemID == "healthy-nested-message" && message.Streaming {
			t.Fatal("nested assistant message remained streaming after its shared host was terminated")
		}
	}
	if got := findActivity(t, snapshot, "healthy-nested-tool").Status; got != domain.ActivityStatusFailed {
		t.Fatalf("nested activity = %q, want %q", got, domain.ActivityStatusFailed)
	}
}

func TestLiveReconnectRetriesDurableHostTerminationAfterRestart(t *testing.T) {
	terminateFailure := errors.New("injected host termination failure")
	firstProvider := &failingPeriodicTermination{
		terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
		err:                     terminateFailure,
	}
	h := newHarnessWithConversation(t, firstProvider)
	root, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "keep working"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	firstProvider.emit(
		ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderEventID: "pending-restart-turn-started",
			ProviderTurnID: root.ProviderTurnID, ProviderConversationID: firstProvider.ProviderConversationID(),
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderEventID: "pending-restart-provider-failure",
			ProviderTurnID: root.ProviderTurnID, ProviderItemID: "pending-restart-provider-failure",
			ActivityKind: domain.ActivityKindSystem, ActivityStatus: domain.ActivityStatusRunning,
			Summary: "Reconnecting to Claude, attempt 10 of 10",
			Detail:  []byte(`{"event":"provider.failure"}`),
		},
	)
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Turns) == 1 && snapshot.Turns[0].State == domain.TurnStateRunning &&
			len(snapshot.Activities) == 1
	})
	h.advance(chatsvc.StaleProviderFailureTimeout + time.Second)
	if err := h.svc.RecoverStaleProviderFailure(context.Background(), testSession, h.now()); !errors.Is(err, terminateFailure) {
		t.Fatalf("periodic recovery error = %v, want injected termination failure", err)
	}
	if got := firstProvider.terminateCalls.Load(); got != 1 {
		t.Fatalf("periodic termination attempts = %d, want 1", got)
	}

	record, found, err := h.st.GetSession(context.Background(), testSession)
	if err != nil || !found {
		t.Fatalf("GetSession after failed periodic recovery: found=%v err=%v", found, err)
	}
	record.Metadata.ProviderConversationID = firstProvider.ProviderConversationID()
	if err := h.st.UpdateSession(context.Background(), record); err != nil {
		t.Fatalf("UpdateSession before daemon restart: %v", err)
	}

	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LoadConversationSnapshot after failed termination: %v", err)
	}
	if got := snapshot.Turns[0].State; got != domain.TurnStateFailed {
		t.Fatalf("root state after failed termination = %q, want %q", got, domain.TurnStateFailed)
	}
	pending, err := h.st.ProviderHostTerminationPending(
		context.Background(), h.ctrl.ConversationID(), testSession)
	if err != nil || !pending {
		t.Fatalf("termination pending after failed termination = %v, err=%v; want true", pending, err)
	}

	// Discard the first service without cleanup, exactly as a daemon crash would.
	// The next service must use the marker rather than the now-terminal root state.
	retryHost := &watchdogRejectingLiveConversation{
		terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
	}
	freshBase := newFakeConversation()
	freshBase.turnSeq = 100
	freshProvider := &nativeHistoryConversation{fakeConversation: freshBase}
	retryDriver := &watchdogSequenceDriver{conversations: []ports.ChatConversation{retryHost, freshProvider}}
	lcm := lifecycle.New(h.st, nil)
	restarted := chatsvc.New(chatsvc.Options{
		Store: h.st, Reader: fullSnapshotReader(h.st), Sessions: h.st,
		Drivers: fakeRegistry{driver: retryDriver}, Activity: lcm,
		Log: slog.New(slog.DiscardHandler), NewID: func() string { return "retried-termination-generation" },
		Now: h.now,
	})
	t.Cleanup(func() { restarted.StopAll(context.Background()) })
	controller, err := restarted.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: firstProvider.ProviderConversationID(),
		ControllerReady: func(result chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			return chatsvc.ControllerCommit{}, lcm.MarkChatReconnected(context.Background(), testSession, domain.SessionMetadata{
				ProviderConversationID: result.ProviderConversationID,
				ControllerGeneration:   result.ControllerGeneration,
			})
		},
	})
	if err != nil {
		t.Fatalf("restart with durable termination marker: %v", err)
	}
	if !retryHost.terminated.Load() {
		t.Fatal("fresh restart did not retry shared host termination")
	}
	if got := retryHost.activationCalls.Load(); got != 0 {
		t.Fatalf("pending host was activated %d times, want 0", got)
	}
	if got := retryDriver.resumeCalls.Load(); got != 2 {
		t.Fatalf("retry resume calls = %d, want live attachment plus fresh resume", got)
	}
	pending, err = h.st.ProviderHostTerminationPending(
		context.Background(), controller.ConversationID(), testSession)
	if err != nil || pending {
		t.Fatalf("termination pending after successful retry = %v, err=%v; want false", pending, err)
	}
	next, err := restarted.Send(context.Background(), testSession, ports.ChatUserMessage{Text: "continue after daemon restart"})
	if err != nil {
		t.Fatalf("Send after durable termination retry: %v", err)
	}
	if next.State != domain.TurnStateRunning {
		t.Fatalf("next turn after restart = %q, want %q", next.State, domain.TurnStateRunning)
	}
	if got := freshProvider.sentTexts(); len(got) != 1 || got[0] != "continue after daemon restart" {
		t.Fatalf("fresh provider sends after restart = %#v, want next prompt", got)
	}

	// Release the abandoned first controller after the replacement has claimed a
	// new generation, mirroring process teardown after the simulated crash.
	_ = firstProvider.Close()
}

type watchdogRejectingLiveConversation struct {
	*terminatingConversation
	activationCalls atomic.Int32
}

func newPeriodicWatchdogHarness(t *testing.T) (*harness, *terminatingConversation, *fakeConversation) {
	t.Helper()
	st := openStore(t)
	firstBase := newFakeConversation()
	first := &terminatingConversation{fakeConversation: firstBase}
	replacementBase := newFakeConversation()
	replacementBase.turnSeq = 100
	replacement := &nativeHistoryConversation{fakeConversation: replacementBase}
	driver := &sequenceDriver{conversations: []ports.ChatConversation{first, replacement}}
	h := &harness{
		st:       st,
		conv:     firstBase,
		activity: &recordingActivity{},
		clock:    time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC),
	}
	var ids atomic.Int32
	h.svc = chatsvc.New(chatsvc.Options{
		Store: st, Reader: fullSnapshotReader(st), Sessions: st,
		Drivers: fakeRegistry{driver: driver}, Activity: h.activity,
		Log: slog.New(slog.DiscardHandler),
		NewID: func() string {
			return fmt.Sprintf("periodic-watchdog-%d", ids.Add(1))
		},
		Now: h.now,
	})
	controller, err := h.svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start periodic watchdog harness: %v", err)
	}
	h.ctrl = controller
	t.Cleanup(func() { _ = h.svc.Stop(context.Background(), testSession) })
	return h, first, replacementBase
}

type failingPeriodicTermination struct {
	*terminatingConversation
	err            error
	terminateCalls atomic.Int32
}

func (c *failingPeriodicTermination) Terminate() error {
	c.terminateCalls.Add(1)
	return c.err
}

type watchdogProjectionHookStore struct {
	chatsvc.Store
	beforeWatchdogProjection func(context.Context) error
	fired                    atomic.Bool
}

func (s *watchdogProjectionHookStore) ProjectProviderEvent(
	ctx context.Context,
	conversationID string,
	sessionID domain.SessionID,
	generation, providerEventID, method, payloadJSON string,
	now time.Time,
	project func(context.Context) error,
) (bool, error) {
	if strings.HasPrefix(providerEventID, "ao-provider-failure-watchdog:") &&
		s.fired.CompareAndSwap(false, true) && s.beforeWatchdogProjection != nil {
		if err := s.beforeWatchdogProjection(ctx); err != nil {
			return false, err
		}
	}
	return s.Store.ProjectProviderEvent(
		ctx, conversationID, sessionID, generation, providerEventID, method, payloadJSON, now, project)
}

func (*watchdogRejectingLiveConversation) ReconnectedLive() bool { return true }

func (c *watchdogRejectingLiveConversation) ActivateLiveReconnect(_ context.Context, providerTurnID string) error {
	c.activationCalls.Add(1)
	if providerTurnID == "" {
		return fmt.Errorf("%w: active host has no durable running turn", ports.ErrChatRecoveryInconclusive)
	}
	return nil
}

type watchdogSequenceDriver struct {
	conversations []ports.ChatConversation
	resumeCalls   atomic.Int32
}

func (*watchdogSequenceDriver) Harness() domain.AgentHarness { return domain.HarnessCodex }

func (d *watchdogSequenceDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return d.conversations[0].Capabilities(), nil
}

func (*watchdogSequenceDriver) Start(context.Context, ports.ChatStartConfig) (ports.ChatConversation, error) {
	return nil, fmt.Errorf("watchdog restart driver does not start a new conversation")
}

func (d *watchdogSequenceDriver) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	index := int(d.resumeCalls.Add(1)) - 1
	if index < 0 || index >= len(d.conversations) {
		return nil, fmt.Errorf("unexpected watchdog restart resume %d", index+1)
	}
	return d.conversations[index], nil
}
