package daemon

import (
	"context"
	"errors"
	"testing"

	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

func TestFreshChatStartWaitsForPersistentHostReconcile(t *testing.T) {
	reconciled := make(chan struct{})
	launcher := chatLauncher{persistentHostReconcileDone: reconciled}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := launcher.StartChat(ctx, sessionmanager.ChatStart{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartChat before host reconciliation = %v, want context canceled", err)
	}
}
