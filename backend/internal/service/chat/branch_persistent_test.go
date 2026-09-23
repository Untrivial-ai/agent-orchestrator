package chat

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type persistentBranchConversation struct {
	stopped    chan struct{}
	closed     atomic.Bool
	terminated atomic.Bool
}

func (c *persistentBranchConversation) ProviderConversationID() string { return "thread-branch" }
func (c *persistentBranchConversation) Capabilities() ports.ChatCapabilities {
	return nil
}
func (c *persistentBranchConversation) SendTurn(context.Context, ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	return ports.ChatTurnRef{}, nil
}
func (c *persistentBranchConversation) Interrupt(context.Context, string) error { return nil }
func (c *persistentBranchConversation) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return nil
}
func (c *persistentBranchConversation) Events() <-chan ports.ChatEvent { return nil }
func (c *persistentBranchConversation) Close() error {
	c.closed.Store(true)
	close(c.stopped)
	return nil
}
func (c *persistentBranchConversation) PreservesProviderOnClose() bool { return true }
func (c *persistentBranchConversation) Terminate() error {
	c.terminated.Store(true)
	close(c.stopped)
	return nil
}

func TestBranchHandoffTerminatesPersistentProvider(t *testing.T) {
	stopped := make(chan struct{})
	provider := &persistentBranchConversation{stopped: stopped}
	controller := &Controller{conv: provider, stopped: stopped}

	if err := controller.closeForBranchHandoff(context.Background()); err != nil {
		t.Fatalf("closeForBranchHandoff: %v", err)
	}
	if !provider.terminated.Load() {
		t.Fatal("branch handoff detached the persistent provider instead of releasing its host")
	}
	if provider.closed.Load() {
		t.Fatal("branch handoff used ordinary daemon-detach semantics")
	}
}
