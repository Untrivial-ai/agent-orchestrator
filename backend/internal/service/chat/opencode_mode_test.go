package chat_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type modeConversation struct {
	*fakeConversation
	mode   string
	setErr error
	ignore bool
}

func (c *modeConversation) ListConfigOptions(context.Context) ([]ports.ChatConfigOption, error) {
	return []ports.ChatConfigOption{{ID: "mode", Current: ports.ChatConfigOptionValue{Select: c.mode}}}, nil
}

func (c *modeConversation) SetConfigOption(ctx context.Context, _ string, value ports.ChatConfigOptionValue) ([]ports.ChatConfigOption, error) {
	if c.setErr != nil {
		return nil, c.setErr
	}
	if !c.ignore {
		c.mode = value.Select
	}
	return c.ListConfigOptions(ctx)
}

func TestOpenCodeModeSurvivesControllerRestart(t *testing.T) {
	for _, mode := range []string{"plan", "build", "ao-plan-project-1"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			created, err := st.CreateSession(ctx, domain.SessionRecord{ProjectID: testProject, Kind: domain.KindWorker, Harness: domain.HarnessOpenCode, Mode: domain.SessionModeChat, CreatedAt: time.Now(), UpdatedAt: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			id := created.ID
			makeService := func(conv ports.ChatConversation) *chatsvc.Service {
				return chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: conv}}, Log: slog.New(slog.DiscardHandler), NewID: uuid.NewString})
			}
			first := &modeConversation{fakeConversation: newFakeConversation(), mode: "build"}
			first.providerConversationID = "opencode-thread"
			svc := makeService(first)
			cfg := chatsvc.StartConfig{SessionID: id, ProjectID: testProject, Harness: domain.HarnessOpenCode, WorkspacePath: t.TempDir()}
			if _, err := svc.Start(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = svc.Stop(ctx, id) })
			if _, err := svc.SetConfigOption(ctx, id, "mode", ports.ChatConfigOptionValue{Select: mode}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.SetTurnSettings(ctx, id, domain.ConversationSettings{Model: "another-model"}); err != nil {
				t.Fatal(err)
			}
			stored, err := st.ConversationForSession(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Settings.OpenCodeMode != mode {
				t.Fatalf("stored mode = %q", stored.Settings.OpenCodeMode)
			}
			if err := svc.Stop(ctx, id); err != nil {
				t.Fatal(err)
			}
			second := &modeConversation{fakeConversation: newFakeConversation(), mode: "build"}
			second.providerConversationID = "opencode-thread"
			svc = makeService(second)
			cfg.ProviderConversationID = "opencode-thread"
			if _, err := svc.Start(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			if second.mode != mode {
				t.Fatalf("resumed mode = %q, want %q", second.mode, mode)
			}
		})
	}
}

func TestOpenCodeModeRestoreFailureDoesNotPublishController(t *testing.T) {
	for _, silent := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "not confirmed"}[silent], func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			conversation, err := st.CreateConversation(ctx, "saved-plan", domain.ConversationScopeProject, testProject, testSession, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := st.SetConversationSettings(ctx, conversation.ID, domain.ConversationSettings{OpenCodeMode: "plan"}, time.Now()); err != nil {
				t.Fatal(err)
			}
			conv := &modeConversation{fakeConversation: newFakeConversation(), mode: "build", ignore: silent}
			if !silent {
				conv.setErr = errors.New("mode unavailable")
			}
			conv.providerConversationID = "opencode-thread"
			svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: conv}}, Log: slog.New(slog.DiscardHandler), NewID: uuid.NewString})
			t.Cleanup(func() { _ = svc.Stop(ctx, testSession) })
			_, err = svc.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessOpenCode, WorkspacePath: t.TempDir(), ProviderConversationID: "opencode-thread"})
			if err == nil {
				t.Fatal("expected mode restoration error")
			}
			if _, err := svc.Controller(testSession); err == nil {
				t.Fatal("published a controller after failed Plan restoration")
			}
		})
	}
}
