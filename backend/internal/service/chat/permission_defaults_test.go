package chat_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

func TestReplacingBypassSettingsWithEmptyApprovalDispatchesDefault(t *testing.T) {
	for _, replacement := range []domain.ConversationSettings{{}, {Model: "new-model"}} {
		name := "empty-settings"
		if replacement.Model != "" {
			name = "model-only"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if _, err := h.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{ApprovalMode: domain.PermissionModeBypassPermissions}); err != nil {
				t.Fatal(err)
			}
			// PATCH settings replaces the durable choices, including omitted approvalMode.
			if _, err := h.svc.SetTurnSettings(ctx, testSession, replacement); err != nil {
				t.Fatal(err)
			}
			if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "next", Origin: domain.MessageOriginHuman}); err != nil {
				t.Fatal(err)
			}
			sent := h.conv.sentMessages()
			if len(sent) != 1 || sent[0].Settings.Approval != ports.PermissionModeDefault {
				t.Fatalf("next turn must explicitly reset native permissions: %+v", sent)
			}
		})
	}
}

func TestExplicitChatPermissionModes(t *testing.T) {
	for _, mode := range []domain.PermissionMode{domain.PermissionModeManual, domain.PermissionModeDontAsk} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			h := newHarness(t)
			if _, err := h.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{ApprovalMode: mode}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "next", Origin: domain.MessageOriginHuman}); err != nil {
				t.Fatal(err)
			}
			sent := h.conv.sentMessages()
			if len(sent) != 1 || sent[0].Settings.Approval != mode {
				t.Fatalf("approval not dispatched: %+v", sent)
			}
			other := newHarnessForHarness(t, domain.HarnessClaudeCode)
			if _, err := other.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{ApprovalMode: mode}); !errors.Is(err, ports.ErrChatPermissionModeUnsupported) {
				t.Fatalf("unsupported mode error = %v", err)
			}
			controller, err := other.svc.Controller(testSession)
			if err != nil {
				t.Fatal(err)
			}
			if controller.Settings().ApprovalMode == mode {
				t.Fatal("unsupported policy persisted")
			}
		})
	}
}

// Signal when the drain reaches its cancellation wait, without timing sleeps.
type observedDrainContext struct {
	context.Context
	waiting chan struct{}
}

func (c observedDrainContext) Done() <-chan struct{} {
	select {
	case c.waiting <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func TestSettingsCannotChangeWhileChatHandoffDrains(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	initial := domain.ConversationSettings{ApprovalMode: domain.PermissionModeAcceptEdits}
	if _, err := h.svc.SetTurnSettings(ctx, testSession, initial); err != nil {
		t.Fatal(err)
	}
	// Keep a real accepted turn running so drain cannot complete on its own.
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "still running"}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ArmChatHandoff(ctx, testSession, domain.SessionInterfaceTransitionDrain); err != nil {
		t.Fatal(err)
	}
	drainCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	waiting := make(chan struct{}, 1)
	observed := observedDrainContext{Context: drainCtx, waiting: waiting}
	go func() {
		done <- h.svc.PrepareChatHandoff(observed, testSession, domain.SessionInterfaceTransitionDrain)
	}()
	select {
	case <-waiting:
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not reach active-turn wait")
	}
	for _, next := range []domain.ConversationSettings{
		{ApprovalMode: domain.PermissionModeManual}, {ApprovalMode: domain.PermissionModeDontAsk}, {Model: "new-model"},
	} {
		if _, err := h.svc.SetTurnSettings(ctx, testSession, next); !errors.Is(err, chatsvc.ErrControllerHandoff) {
			t.Fatalf("settings during armed handoff: %v", err)
		}
	}
	controller, err := h.svc.Controller(testSession)
	if err != nil {
		t.Fatal(err)
	}
	if controller.Settings() != initial {
		t.Fatalf("in-memory settings changed: %+v", controller.Settings())
	}
	conversation, err := h.st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Settings != initial {
		t.Fatalf("durable settings changed during handoff: %+v", conversation.Settings)
	}
	select {
	case err := <-done:
		t.Fatalf("drain settled with turn running: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel drain: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not cancel")
	}
	// A failed/cancelled handoff reopens settings together with message intake.
	if _, err := h.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{ApprovalMode: domain.PermissionModeManual}); err != nil {
		t.Fatalf("settings after cancelled handoff: %v", err)
	}
}
