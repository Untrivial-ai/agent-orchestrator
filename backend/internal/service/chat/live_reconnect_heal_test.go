package chat_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// hostedConversation is a provider conversation backed by a persistent host
// that a later attachment can adopt without resuming it.
type hostedConversation struct {
	*fakeConversation
	live       bool
	terminated atomic.Bool
}

func newHostedConversation(live bool) *hostedConversation {
	return &hostedConversation{fakeConversation: newFakeConversation(), live: live}
}

func (c *hostedConversation) PreservesProviderOnClose() bool { return true }
func (c *hostedConversation) ReconnectedLive() bool          { return c.live }
func (c *hostedConversation) Terminate() error {
	c.terminated.Store(true)
	return c.Close()
}

// A durable Exited written by an older build (or any false exit) is sticky:
// lifecycle drops ordinary activity on an exited row. Adopting the same live
// provider proves the agent is running, so the reconnect must clear it.
func TestLiveReconnectClearsStaleExitedActivity(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	rec, _, err := st.GetSession(ctx, testSession)
	if err != nil {
		t.Fatal(err)
	}
	rec.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: time.Unix(100, 0).UTC()}
	rec.Metadata.ProviderConversationID = "thread-1"
	if err := st.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Reader: fullSnapshotReader(st), Sessions: st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: newHostedConversation(true)}},
		Activity: lifecycle.New(st, nil),
		Log:      slog.New(slog.DiscardHandler),
		NewID:    func() string { return "reconnected-generation" },
	})
	t.Cleanup(func() { svc.StopAll(context.Background()) })
	if _, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	after, _, err := st.GetSession(ctx, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if after.Activity.State != domain.ActivityIdle {
		t.Fatalf("activity after live reconnect = %+v, want idle", after.Activity)
	}
}

// A live-only start (startup healing of a false exit) must never adopt a
// provider the driver launched or resumed in place of a vanished host, and must
// not claim ownership before refusing.
func TestRequireLiveReconnectRefusesReplacementProvider(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	before, _, err := st.GetSession(ctx, testSession)
	if err != nil {
		t.Fatal(err)
	}
	replacement := newHostedConversation(false)
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Reader: fullSnapshotReader(st), Sessions: st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: replacement}},
		Activity: lifecycle.New(st, nil),
		Log:      slog.New(slog.DiscardHandler),
		NewID:    func() string { return "refused-generation" },
	})
	_, err = svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireLiveReconnect: true,
	})
	if !errors.Is(err, ports.ErrChatProviderNotLive) {
		t.Fatalf("Start = %v, want ErrChatProviderNotLive", err)
	}
	if !replacement.terminated.Load() {
		t.Fatal("replacement provider opened by the driver was not destroyed")
	}
	if svc.HasLiveChatController(testSession) {
		t.Fatal("refused live-only start published a controller")
	}
	after, _, err := st.GetSession(ctx, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if after.Metadata.ControllerGeneration != before.Metadata.ControllerGeneration {
		t.Fatalf("refused live-only start claimed generation %q", after.Metadata.ControllerGeneration)
	}
}
