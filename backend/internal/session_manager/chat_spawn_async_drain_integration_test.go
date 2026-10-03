package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	browsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type rejectingQueuedConversation struct{ *integrationChatConversation }

func (*rejectingQueuedConversation) SendTurn(context.Context, ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	return ports.ChatTurnRef{}, errors.New("provider rejected queued turn")
}

type realQueueDrainLauncher struct{ integrationChatLauncher }

type failedClientRequestCommitStore struct{ *sqlite.Store }

func (*failedClientRequestCommitStore) CommitClientRequestSession(context.Context, domain.SessionID) error {
	return errors.New("first agent startup interrupted before request commit")
}

func (l realQueueDrainLauncher) QueueChatPrompt(ctx context.Context, id domain.SessionID, text string) (string, error) {
	turn, err := l.service.QueueUserMessage(ctx, id, ports.ChatUserMessage{Text: text, Origin: domain.MessageOriginHuman})
	return turn.ID, err
}

func (l realQueueDrainLauncher) DrainChatQueue(ctx context.Context, id domain.SessionID) error {
	return l.service.DrainQueued(ctx, id)
}

func TestIncompleteClientRequestAfterStartupCrashDoesNotReplayOpeningPrompt(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st := sqlitetest.MustOpenAt(t, dataDir)
	if err := st.UpsertProject(ctx, domain.ProjectRecord{
		ID: string(chatTestProject), Path: dataDir, Config: testRoleAgents(), RegisteredAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	provider := newIntegrationChatConversation("provider-thread")
	var nextID atomic.Int64
	newChat := func() *chatsvc.Service {
		return chatsvc.New(chatsvc.Options{
			Store: st, Sessions: st,
			Drivers: integrationChatRegistry{domain.HarnessCodex: integrationChatDriver{
				harness: domain.HarnessCodex,
				start:   func() ports.ChatConversation { return provider },
			}},
			Log:   slog.New(slog.DiscardHandler),
			NewID: func() string { return fmt.Sprintf("startup-crash-%d", nextID.Add(1)) },
		})
	}
	firstChat := newChat()
	t.Cleanup(func() { firstChat.StopAll(context.Background()) })
	first := New(Deps{
		Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: &fakeWorkspace{path: t.TempDir()},
		Store: &failedClientRequestCommitStore{st}, Messenger: &fakeMessenger{},
		Chat:      realQueueDrainLauncher{integrationChatLauncher{service: firstChat}},
		Lifecycle: lifecycle.New(st, nil), DataDir: dataDir,
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		Logger:   slog.New(slog.DiscardHandler),
	})
	first.browserCapabilities = browsersvc.NewAuthority()
	first.runBackground = func(func()) {} // daemon dies before its scheduled startup runs
	cfg := asyncChatSpawnConfig("opening brief")
	cfg.ClientRequestID, cfg.ClientRequestHash = "draft-1", "v1:opening brief"
	if _, _, _, err := first.Spawn(ctx, cfg); !errors.Is(err, ErrSpawnCommit) {
		t.Fatalf("first create error = %v, want commit interruption", err)
	}
	rec, found, err := st.GetSessionByClientRequestID(ctx, cfg.ClientRequestID)
	if err != nil || !found || rec.ClientRequestCommitted {
		t.Fatalf("interrupted request = %+v, found=%v, err=%v", rec, found, err)
	}
	conversation, err := st.ConversationForSession(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := st.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil || len(queued.Turns) != 1 || queued.Turns[0].State != domain.TurnStateQueued || len(queued.Messages) != 1 {
		t.Fatalf("opening prompt before recovery: turns=%+v messages=%+v err=%v", queued.Turns, queued.Messages, err)
	}

	secondChat := newChat()
	t.Cleanup(func() { secondChat.StopAll(context.Background()) })
	second := New(Deps{
		Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: &fakeWorkspace{path: t.TempDir()},
		Store: st, Messenger: &fakeMessenger{},
		Chat:      realQueueDrainLauncher{integrationChatLauncher{service: secondChat}},
		Lifecycle: lifecycle.New(st, nil), DataDir: dataDir,
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		Logger:   slog.New(slog.DiscardHandler),
	})
	second.browserCapabilities = browsersvc.NewAuthority()
	second.runBackground = func(work func()) { work() }
	if _, _, _, err := second.Spawn(ctx, cfg); !errors.Is(err, ErrClientRequestIncomplete) {
		t.Fatalf("same-key retry error = %v, want incomplete request", err)
	}
	if err := second.FailInterruptedProvisioning(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ResumeAgentWithMode(ctx, rec.ID); err != nil {
		t.Fatalf("resume interrupted worker: %v", err)
	}
	if _, _, _, err := second.Spawn(ctx, cfg); !errors.Is(err, ErrClientRequestIncomplete) {
		t.Fatalf("same-key retry after recovery = %v, want incomplete request", err)
	}
	if err := secondChat.DrainQueued(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	final, err := st.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil || len(final.Turns) != 1 || len(final.Messages) != 1 {
		t.Fatalf("opening prompt after recovery: turns=%+v messages=%+v err=%v", final.Turns, final.Messages, err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.sent) != 1 || provider.sent[0].Text != "opening brief" {
		t.Fatalf("provider prompts after retry and queue drain = %+v, want opening brief once", provider.sent)
	}
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
