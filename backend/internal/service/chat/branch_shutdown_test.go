package chat_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	browsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// Models the real Codex termination order: detaching the transport stops the
// controller's event stream; a failed authenticated shutdown leaves the provider
// process alive with its original, immutable environment.
type shutdownRefusingHost struct {
	*historyRecorder
	browserToken string
}

func (h *shutdownRefusingHost) PreservesProviderOnClose() bool { return true }
func (h *shutdownRefusingHost) Terminate() error {
	_ = h.fakeConversation.Close()
	return errors.New("read shutdown acknowledgement: connection reset by peer")
}

func TestFailedBranchShutdownPreservesSurvivingHostCredentials(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	source := &shutdownRefusingHost{historyRecorder: newHistoryRecorder()}
	reattached := newFakeConversation()
	reattached.providerConversationID = "thread-1"
	authority := browsersvc.NewAuthority()
	var ids atomic.Int64
	rotations := 0
	reattachments := 0
	prepare := func(ctx context.Context, expected domain.SessionControllerOwner) (map[string]string, error) {
		token, verifier, err := authority.Issue(testSession)
		if err != nil {
			return nil, err
		}
		applied, err := st.UpdateBrowserCapabilityVerifier(ctx, testSession, expected, verifier, time.Now())
		if err != nil {
			return nil, err
		}
		if !applied {
			return nil, errors.New("credential owner changed")
		}
		rotations++
		return map[string]string{"AO_BROWSER_CAPABILITY": token}, nil
	}
	driver := fakeDriver{
		start: func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
			source.browserToken = cfg.Env["AO_BROWSER_CAPABILITY"]
			return source, nil
		},
		resume: func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
			reattachments++
			if cfg.ProviderConversationID != "thread-1" {
				return nil, fmt.Errorf("unexpected resume %s", cfg.ProviderConversationID)
			}
			// The real Codex Resume reconnected path reuses the host without
			// applying cfg.Env or issuing thread/resume. Preserve its old token.
			return reattached, nil
		},
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st, Reader: fullSnapshotReader(st),
		Drivers: fakeRegistry{driver: driver}, Log: slog.New(slog.DiscardHandler),
		NewID: func() string { return fmt.Sprintf("shutdown-test-%d", ids.Add(1)) },
	})
	rec, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("read initial session: %v", err)
	}
	ctrl, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ExpectedControllerOwner: rec.ControllerOwner(), PrepareControllerEnv: prepare,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	rec, found, err = st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("read started session: %v", err)
	}
	rec.Metadata.ProviderConversationID = "thread-1"
	rec.Metadata.ControllerGeneration = ctrl.Generation()
	if err := st.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	h := &harness{svc: svc, st: st, conv: source.fakeConversation, ctrl: ctrl}
	completeTurn(t, h, "A", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	second := completeTurn(t, h, "B", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })
	_, editErr := svc.EditMessage(ctx, testSession, second, ports.ChatUserMessage{
		Text: "B edited", ClientMessageID: "unconfirmed-host", Origin: domain.MessageOriginHuman,
	})
	if editErr == nil {
		t.Fatal("edit succeeded despite rejected shutdown")
	}
	rec, found, err = st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("read after edit: %v", err)
	}
	current, controllerErr := svc.Controller(testSession)
	// The watcher may already have removed the stopped source after handoff
	// aborts. Neither timing may publish a replacement for the surviving host.
	if controllerErr != nil && !errors.Is(controllerErr, chatsvc.ErrNoController) {
		t.Fatal(controllerErr)
	}
	if rotations != 1 || reattachments != 0 || (current != nil && current != ctrl) {
		t.Fatalf("unconfirmed host was replaced: rotations=%d reattachments=%d controllerErr=%v", rotations, reattachments, controllerErr)
	}
	if !authority.Valid(testSession, source.browserToken, rec.Metadata.BrowserCapabilityVerifier) {
		t.Fatal("surviving host browser token became unauthorized before confirmed termination")
	}
}
