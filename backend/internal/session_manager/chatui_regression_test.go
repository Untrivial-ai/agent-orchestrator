//go:build chatui_regression

package sessionmanager

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// unsettledThenReadyChat models two fresh observations of the same provider
// conversation. The first immutable history snapshot is not settled; the next
// target start sees settled history and can publish its controller.
type unsettledThenReadyChat struct {
	*transitionChat

	mu                   sync.Mutex
	starts               []ChatStart
	activeControllers    int
	maxActiveControllers int
}

func (c *unsettledThenReadyChat) StartChat(_ context.Context, cfg ChatStart) (ChatStarted, error) {
	c.mu.Lock()
	c.starts = append(c.starts, cfg)
	startNumber := len(c.starts)
	c.transitionChat.start = cfg
	*c.transitionChat.log = append(*c.transitionChat.log, "start:chat")
	c.mu.Unlock()

	if startNumber == 1 {
		return ChatStarted{}, ports.ErrChatHistoryUnsettled
	}

	started := ChatStarted{
		ProviderConversationID: cfg.ProviderConversationID,
		ControllerGeneration:   "chat-generation",
	}
	c.mu.Lock()
	c.activeControllers++
	if c.activeControllers > c.maxActiveControllers {
		c.maxActiveControllers = c.activeControllers
	}
	c.mu.Unlock()
	if cfg.ControllerReady != nil {
		if _, err := cfg.ControllerReady(started); err != nil {
			return ChatStarted{}, err
		}
	}
	return started, nil
}

func (c *unsettledThenReadyChat) StopChat(_ context.Context, _ domain.SessionID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.transitionChat.log = append(*c.transitionChat.log, "stop:chat")
	if c.activeControllers > 0 {
		c.activeControllers--
	}
	return nil
}

func (c *unsettledThenReadyChat) snapshot() ([]ChatStart, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ChatStart(nil), c.starts...), c.activeControllers, c.maxActiveControllers
}

// A frozen ACP history view is one observation, not a permanent verdict on the
// native conversation. Retrying target admission must create a fresh observation
// inside the same durable transition, while the stopped TUI remains fenced and at
// most one target controller owns the session.
func TestChatUIRegressionTUIToChatRetriesFreshTargetAfterUnsettledHistory(t *testing.T) {
	manager, store, runtime, baseChat, _ := newTransitionManager(t, domain.SessionModeTUI)
	chat := &unsettledThenReadyChat{transitionChat: baseChat}
	manager.chat = chat

	transition, err := manager.StartInterfaceTransition(
		context.Background(),
		"session-1",
		domain.SessionModeChat,
		domain.SessionInterfaceTransitionInterrupt,
	)
	if err != nil {
		t.Fatalf("StartInterfaceTransition: %v", err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("transition = %+v, want the same durable transition to complete after a fresh target retry", settled)
	}
	if got := store.sessions["session-1"].Mode; got != domain.SessionModeChat {
		t.Fatalf("session mode = %q, want %q", got, domain.SessionModeChat)
	}

	starts, active, maxActive := chat.snapshot()
	if len(starts) != 2 {
		t.Fatalf("target starts = %d, want 2 fresh observations", len(starts))
	}
	for i, start := range starts {
		if start.ProviderConversationID != "native-1" {
			t.Fatalf("target start %d provider conversation = %q, want native-1", i+1, start.ProviderConversationID)
		}
		if !start.RequireNativeHistory {
			t.Fatalf("target start %d did not require native history", i+1)
		}
	}
	if maxActive > 1 {
		t.Fatalf("maximum concurrent target controllers = %d, want at most 1", maxActive)
	}
	if active != 1 {
		t.Fatalf("active target controllers = %d, want 1 after completion", active)
	}
	if runtime.created != 0 {
		t.Fatalf("source TUI was restarted %d times during target retry, want 0", runtime.created)
	}
	if runtime.destroyed != 1 {
		t.Fatalf("source TUI was stopped %d times, want exactly 1 before target admission", runtime.destroyed)
	}
	if got := fmt.Sprint(*runtime.log); got != "[interrupt:tui:runtime-1 stop:tui:runtime-1 start:chat start:chat]" {
		t.Fatalf("controller order = %s, want source stop before both fresh target observations", got)
	}
}
