package chat_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
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

func TestPendingReadOnlyTurnSurvivesMutableHostReconnect(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%v", busy), func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			reader := fullSnapshotReader(st)
			var ids atomic.Int32
			newID := func() string { return fmt.Sprintf("pending-policy-%d", ids.Add(1)) }
			caps := productionCaps()
			caps[ports.ChatCapabilityPreventiveReadOnly] = true
			firstProvider := &terminatingConversation{fakeConversation: newFakeConversation()}
			firstProvider.setCapabilities(caps)
			first := chatsvc.New(chatsvc.Options{Store: st, Reader: reader, Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: firstProvider, caps: caps}}, NewID: newID})
			ctrl, err := first.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, Permissions: ports.PermissionModeAuto})
			if err != nil {
				t.Fatal(err)
			}
			var running domain.ConversationTurn
			if busy {
				running, err = ctrl.Send(ctx, ports.ChatUserMessage{Text: "already running"})
				if err != nil {
					t.Fatal(err)
				}
				firstProvider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: running.ProviderTurnID})
				h := &harness{st: st, ctrl: ctrl}
				h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
					return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
				})
			}
			if _, err := first.SetTurnSettings(ctx, testSession, domain.ConversationSettings{ApprovalMode: ports.PermissionModeReadOnly}); err != nil {
				t.Fatal(err)
			}
			first.StopAll(ctx)
			provider := &liveReconnectedConversation{nativeHistoryConversation: &nativeHistoryConversation{fakeConversation: newFakeConversation()}}
			provider.setCapabilities(caps)
			driver := fakeDriver{conv: provider, caps: caps, resume: func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
				// The surviving host correctly retains its current broad policy;
				// the selected read-only policy has not been dispatched yet.
				if cfg.Permissions == ports.PermissionModeReadOnly {
					return nil, ports.ErrChatRecoveryInconclusive
				}
				return provider, nil
			}}
			second := chatsvc.New(chatsvc.Options{Store: st, Reader: reader, Sessions: st, Drivers: fakeRegistry{driver: driver}, NewID: newID})
			t.Cleanup(func() { second.StopAll(context.Background()) })
			ctrl, err = second.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, Permissions: ports.PermissionModeAuto, ProviderConversationID: "thread-1"})
			if err != nil {
				t.Fatalf("reconnect before policy dispatch: %v", err)
			}
			if _, err := ctrl.Send(ctx, ports.ChatUserMessage{Text: "read-only next turn"}); err != nil {
				t.Fatal(err)
			}
			if busy {
				if len(provider.sentMessages()) != 0 {
					t.Fatal("started next turn concurrently with surviving turn")
				}
				provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: running.ProviderTurnID, TurnState: domain.TurnStateCompleted})
			}
			h := &harness{st: st, ctrl: ctrl}
			h.awaitSnapshot(t, func(store.ConversationSnapshot) bool { return len(provider.sentMessages()) == 1 })
			if got := provider.sentMessages()[0].Settings.Approval; got != ports.PermissionModeReadOnly {
				t.Fatalf("next turn permission=%q", got)
			}
		})
	}
}
