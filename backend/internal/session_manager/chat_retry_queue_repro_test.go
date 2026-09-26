package sessionmanager

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type retryDrainingLauncher struct{ integrationChatLauncher }

func (l retryDrainingLauncher) DrainChatQueue(ctx context.Context, id domain.SessionID) error {
	return l.service.DrainQueued(ctx, id)
}

// A failed async start may already have committed the provider handle. Retrying
// must not let Chat treat the user's unsent queue as work from a dead controller.
func TestRetryFailedChatWithProviderHandlePreservesQueuedTurn(t *testing.T) {
	fixture := newChatSwitchIntegrationFixture(t, false)
	fixture.manager.chat = retryDrainingLauncher{integrationChatLauncher{service: fixture.service}}
	ctx := context.Background()
	id := fixture.session.ID
	if err := fixture.service.StopChat(ctx, id); err != nil {
		t.Fatal(err)
	}
	// The switch fixture models a resumed controller without a Git branch; a
	// worker retry requires the durable branch in its workspace handle.
	rec, found, err := fixture.store.GetSession(ctx, id)
	if err != nil || !found {
		t.Fatalf("load session: found=%v err=%v", found, err)
	}
	rec.Metadata.Branch = "ao/retry/root"
	rec.ProvisionState = domain.SessionProvisionReady
	if err := fixture.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetSessionProvisionState(ctx, id, domain.SessionProvisionProvisioning, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	queued, err := fixture.service.QueueUserMessage(ctx, id, ports.ChatUserMessage{
		Text: "message typed while starting", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetSessionProvisionState(ctx, id, domain.SessionProvisionFailed, "start interrupted", time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.manager.ResumeAgentWithMode(ctx, id); err != nil {
		t.Fatalf("retry failed start: %v", err)
	}
	conversation, err := fixture.store.ConversationForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.store.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range snapshot.Turns {
		if turn.ID == queued.ID {
			if turn.State == domain.TurnStateFailed || turn.State == domain.TurnStateQueued {
				t.Fatalf("retry left queued turn %s: state=%q error=%q", turn.ID, turn.State, turn.ErrorMessage)
			}
			return
		}
	}
	t.Fatalf("queued turn %s missing after retry", queued.ID)
}

func TestRetryFailedChatWithProviderHandleRestoresFailedStateOnResumeError(t *testing.T) {
	fixture := newChatSwitchIntegrationFixture(t, false)
	ctx := context.Background()
	id := fixture.session.ID
	if err := fixture.service.StopChat(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetSessionProvisionState(ctx, id, domain.SessionProvisionProvisioning, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	queued, err := fixture.service.QueueUserMessage(ctx, id, ports.ChatUserMessage{
		Text: "keep this queued", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetSessionProvisionState(ctx, id, domain.SessionProvisionFailed, "start interrupted", time.Now()); err != nil {
		t.Fatal(err)
	}
	// The fixture deliberately has no branch, so provider resume fails its
	// workspace-handle check after the retry has entered provisioning.
	if _, err := fixture.manager.ResumeAgentWithMode(ctx, id); err == nil {
		t.Fatal("retry with incomplete handle succeeded")
	}
	rec, found, err := fixture.store.GetSession(ctx, id)
	if err != nil || !found {
		t.Fatalf("load session: found=%v err=%v", found, err)
	}
	if rec.ProvisionState != domain.SessionProvisionFailed {
		t.Fatalf("retry failure state = %q, want failed", rec.ProvisionState)
	}
	conversation, err := fixture.store.ConversationForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.store.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range snapshot.Turns {
		if turn.ID == queued.ID {
			if turn.State != domain.TurnStateQueued {
				t.Fatalf("retry failure changed queued turn to %q", turn.State)
			}
			return
		}
	}
	t.Fatalf("queued turn %s missing after failed retry", queued.ID)
}
