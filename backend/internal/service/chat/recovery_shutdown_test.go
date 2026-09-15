package chat_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type historyShutdownRefusingConversation struct {
	*nativeHistoryConversation
	shutdownErr error
}

func (c *historyShutdownRefusingConversation) Terminate() error {
	_ = c.Close()
	return c.shutdownErr
}

func TestRequiredHistoryCannotAdoptTargetWhoseShutdownFailed(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	shutdownErr := errors.New("provider shutdown acknowledgement was lost")
	first := &historyShutdownRefusingConversation{
		nativeHistoryConversation: &nativeHistoryConversation{
			fakeConversation: newFakeConversation(), err: ports.ErrChatHistoryUnsettled,
		},
		shutdownErr: shutdownErr,
	}
	second := &liveReconnectedConversation{nativeHistoryConversation: &nativeHistoryConversation{
		fakeConversation: newFakeConversation(), err: ports.ErrChatHistoryUnsettled,
	}}
	var sequence atomic.Int64
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st, Reader: fullSnapshotReader(st),
		Drivers: fakeRegistry{driver: &sequenceDriver{conversations: []ports.ChatConversation{first, second}}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("required-history-%d", sequence.Add(1)) },
	})
	t.Cleanup(func() { svc.StopAll(ctx) })
	cfg := chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
	}
	_, firstErr := svc.Start(ctx, cfg)
	if !errors.Is(firstErr, ports.ErrChatRecoveryInconclusive) || !errors.Is(firstErr, shutdownErr) {
		t.Errorf("failed history cleanup error = %v, want inconclusive shutdown with cause", firstErr)
	}
	controller, retryErr := svc.Start(ctx, cfg)
	if controller != nil || !errors.Is(retryErr, ports.ErrChatRecoveryInconclusive) {
		t.Fatalf("required-history retry adopted unpublished live host: controller=%v error=%v", controller != nil, retryErr)
	}
	if _, err := svc.Controller(testSession); !errors.Is(err, chatsvc.ErrNoController) {
		t.Fatalf("unverified history published a controller: %v", err)
	}
}
