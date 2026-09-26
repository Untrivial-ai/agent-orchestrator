package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	browsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type rejectingQueuedConversation struct{ *integrationChatConversation }

func (*rejectingQueuedConversation) SendTurn(context.Context, ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	return ports.ChatTurnRef{}, errors.New("provider rejected queued turn")
}

type realQueueDrainLauncher struct{ integrationChatLauncher }

func (l realQueueDrainLauncher) QueueChatPrompt(ctx context.Context, id domain.SessionID, text string) (string, error) {
	turn, err := l.service.QueueUserMessage(ctx, id, ports.ChatUserMessage{Text: text, Origin: domain.MessageOriginHuman})
	return turn.ID, err
}

func (l realQueueDrainLauncher) DrainChatQueue(ctx context.Context, id domain.SessionID) error {
	return l.service.DrainQueued(ctx, id)
}

// The real Chat service used to swallow its queued dispatch error and tell
// Session Manager the initial drain succeeded, leaving a failed brief in a
// session marked ready with no Retry banner.
func TestAsyncChatSpawnRealDrainFailureStaysRetryable(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st := sqlitetest.MustOpenAt(t, dataDir)
	if err := st.UpsertProject(ctx, domain.ProjectRecord{
		ID: string(chatTestProject), Path: dataDir, Config: testRoleAgents(),
		RegisteredAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	retryConversation := newIntegrationChatConversation("provider-thread")
	nextChatID := 0
	service := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Drivers: integrationChatRegistry{domain.HarnessCodex: integrationChatDriver{
			harness: domain.HarnessCodex,
			start: func() ports.ChatConversation {
				return &rejectingQueuedConversation{newIntegrationChatConversation("provider-thread")}
			},
			resume: func() ports.ChatConversation { return retryConversation },
		}},
		Log: slog.New(slog.DiscardHandler), NewID: func() string {
			nextChatID++
			return fmt.Sprintf("chat-drain-%d", nextChatID)
		},
	})
	t.Cleanup(func() { service.StopAll(context.Background()) })
	m := New(Deps{
		Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: &fakeWorkspace{path: t.TempDir()},
		Store: st, Messenger: &fakeMessenger{}, Chat: realQueueDrainLauncher{integrationChatLauncher{service: service}},
		Lifecycle: lifecycle.New(st, nil), DataDir: dataDir,
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		Logger:   slog.New(slog.DiscardHandler),
	})
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	rec, _, _, err := m.Spawn(ctx, asyncChatSpawnConfig("opening brief"))
	if err != nil {
		t.Fatal(err)
	}
	(*deferred)[0]()
	conversation, err := st.ConversationForSession(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateFailed {
		t.Fatalf("provider dispatch was not exercised: turns = %+v", snapshot.Turns)
	}
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].Text != "opening brief" {
		t.Fatalf("failed opening prompt is not visible for explicit retry: messages = %+v", snapshot.Messages)
	}
	stored, found, err := st.GetSession(ctx, rec.ID)
	if err != nil || !found {
		t.Fatalf("session: found=%v err=%v", found, err)
	}
	if stored.ProvisionState != domain.SessionProvisionFailed || !strings.Contains(stored.ProvisionError, "provider rejected queued turn") {
		t.Fatalf("failed opening turn left session state=%q error=%q; want retryable failure",
			stored.ProvisionState, stored.ProvisionError)
	}
	if _, err := m.ResumeAgentWithMode(ctx, rec.ID); err != nil {
		t.Fatalf("resume failed session before explicit turn retry: %v", err)
	}
	// SendTurn failed before returning a provider id, so automatic retry would
	// risk duplicate work if the provider actually accepted it. The visible
	// original remains available for an explicit user resend after recovery.
	if _, err := service.Send(ctx, rec.ID, ports.ChatUserMessage{
		Text: snapshot.Messages[0].Text, Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("explicitly resend original brief: %v", err)
	}
	retryConversation.mu.Lock()
	defer retryConversation.mu.Unlock()
	if len(retryConversation.sent) != 1 || retryConversation.sent[0].Text != "opening brief" {
		t.Fatalf("explicit retry sent %+v, want original brief once", retryConversation.sent)
	}
}
