package chat_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

func TestReadOnlyRejectsUnsupportedDriverBeforeOpeningConversation(t *testing.T) {
	st := openStore(t)
	driver := fakeDriver{probe: func() error { t.Fatal("unsupported policy reached provider probe"); return nil }}
	svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Drivers: fakeRegistry{driver: driver}})
	ctx := context.Background()
	if err := svc.PreflightChat(ctx, domain.HarnessCodex, ports.PermissionModeReadOnly); !errors.Is(err, ports.ErrChatPermissionModeUnsupported) {
		t.Fatalf("preflight = %v", err)
	}
	if _, err := svc.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, Permissions: ports.PermissionModeReadOnly}); !errors.Is(err, ports.ErrChatPermissionModeUnsupported) {
		t.Fatalf("start = %v", err)
	}
	if _, err := st.ConversationForSession(ctx, testSession); !errors.Is(err, domain.ErrNoConversation) {
		t.Fatalf("unsupported request created conversation: %v", err)
	}
}

func TestReadOnlySessionConstrainsResumeActivationAndDispatch(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.Now()
	record, err := st.CreateConversation(ctx, "readonly-conversation", domain.ConversationScopeProject, testProject, testSession, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetConversationSettings(ctx, record.ID, domain.ConversationSettings{ApprovalMode: ports.PermissionModeBypassPermissions, Model: "saved-model"}, now); err != nil {
		t.Fatal(err)
	}
	conv := newFakeConversation()
	caps := productionCaps()
	caps[ports.ChatCapabilityPreventiveReadOnly] = true
	conv.setCapabilities(caps)
	var resumed ports.ChatResumeConfig
	nextID := 0
	svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: conv, caps: caps, resumeCfg: &resumed}}, NewID: func() string { nextID++; return fmt.Sprintf("readonly-%d", nextID) }})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	controller, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		Permissions: ports.PermissionModeReadOnly, ProviderConversationID: "thread-1", WorkspacePath: t.TempDir(),
		ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			// A coordinator can return an older settings row during ownership transfer.
			committed := started.Conversation
			committed.Settings.ApprovalMode = ports.PermissionModeBypassPermissions
			return chatsvc.ControllerCommit{Conversation: committed}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Permissions != ports.PermissionModeReadOnly || resumed.Model != "saved-model" {
		t.Fatalf("resume = %+v", resumed)
	}
	settings := controller.Settings()
	if settings.ApprovalMode != ports.PermissionModeReadOnly {
		t.Fatalf("activation settings = %+v", settings)
	}
	for _, broader := range []ports.PermissionMode{"", ports.PermissionModeDefault, ports.PermissionModeAcceptEdits, ports.PermissionModeAuto, ports.PermissionModeBypassPermissions} {
		if _, err := svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{ApprovalMode: broader}); !errors.Is(err, ports.ErrChatPermissionModeUnsupported) {
			t.Fatalf("broaden to %q: %v", broader, err)
		}
	}
	settings.Model = "new-model"
	if _, err := svc.SetTurnSettings(ctx, testSession, settings); err != nil {
		t.Fatalf("read-only model change: %v", err)
	}
	if _, err := controller.Send(ctx, ports.ChatUserMessage{Text: "inspect", ClientMessageID: "read", Origin: domain.MessageOriginHuman, Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions}}); err != nil {
		t.Fatal(err)
	}
	sent := conv.sentMessages()
	if len(sent) != 1 || sent[0].Settings.Approval != ports.PermissionModeReadOnly || sent[0].Settings.Model != "new-model" {
		t.Fatalf("dispatched = %+v", sent)
	}
	persisted, err := st.ConversationForSession(ctx, testSession)
	if err != nil || persisted.Settings.ApprovalMode != ports.PermissionModeReadOnly {
		t.Fatalf("persisted settings = %+v, %v", persisted.Settings, err)
	}
}

func TestReadOnlyTurnRequiresLiveProviderCapability(t *testing.T) {
	st := openStore(t)
	svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: newFakeConversation()}}, NewID: func() string { return "readonly-settings" }})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	_, err := svc.Start(context.Background(), chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, Permissions: ports.PermissionModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetTurnSettings(context.Background(), testSession, domain.ConversationSettings{ApprovalMode: ports.PermissionModeReadOnly}); !errors.Is(err, ports.ErrChatPermissionModeUnsupported) {
		t.Fatalf("unsupported turn policy = %v", err)
	}
}
