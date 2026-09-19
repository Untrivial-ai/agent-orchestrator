package chat_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// An asynchronous spawn puts the session on screen before its controller
// exists. Everything the user types in that window has to land somewhere
// durable, in order — refusing it would be a message lost from a session the
// user is already looking at and typing into.

func openProvisioningStore(t *testing.T, state domain.SessionProvisionState) (*sqlite.Store, domain.SessionID) {
	t.Helper()
	dir := t.TempDir()
	st := sqlitetest.MustOpenAt(t, dir)
	ctx := context.Background()
	if err := st.UpsertProject(ctx, domain.ProjectRecord{
		ID: string(testProject), Path: dir, RegisteredAt: time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	rec, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		ProvisionState: state,
		CreatedAt:      time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return st, rec.ID
}

func provisioningService(t *testing.T, st *sqlite.Store) *chatsvc.Service {
	t.Helper()
	next := 0
	return chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st, Reader: fullSnapshotReader(st),
		Drivers: fakeRegistry{driver: fakeDriver{conv: newFakeConversation()}},
		Log:     slog.New(slog.DiscardHandler),
		NewID: func() string {
			next++
			return fmt.Sprintf("queued-%d", next)
		},
	})
}

func TestSendWhileProvisioningQueuesInOrder(t *testing.T) {
	st, provisioningSession := openProvisioningStore(t, domain.SessionProvisionProvisioning)
	svc := provisioningService(t, st)
	ctx := context.Background()

	first, err := svc.Send(ctx, provisioningSession, ports.ChatUserMessage{
		Text: "first", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("send while provisioning: %v", err)
	}
	if first.State != domain.TurnStateQueued {
		t.Fatalf("turn state = %q, want queued", first.State)
	}
	if _, err := svc.Send(ctx, provisioningSession, ports.ChatUserMessage{
		Text: "second", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("second send while provisioning: %v", err)
	}

	snapshot, err := svc.Snapshot(ctx, provisioningSession)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 {
		t.Fatalf("messages = %d, want both queued messages", len(snapshot.Messages))
	}
	if snapshot.Messages[0].Text != "first" || snapshot.Messages[1].Text != "second" {
		t.Fatalf("messages out of order: %q then %q",
			snapshot.Messages[0].Text, snapshot.Messages[1].Text)
	}
	// A session that is still starting is connecting, not stopped: a client that
	// reads "stopped" hides the composer the user is meant to keep typing into.
	if snapshot.Controller != ports.ChatControllerConnecting {
		t.Fatalf("controller state = %q, want connecting", snapshot.Controller)
	}
}

// The queue is for sessions that are starting. A session with no controller for
// any other reason has a real reason, and accepting a message it will never
// dispatch would be worse than refusing it.
func TestSendWithoutControllerStillRefusedWhenNotProvisioning(t *testing.T) {
	st, provisioningSession := openProvisioningStore(t, domain.SessionProvisionReady)
	svc := provisioningService(t, st)

	_, err := svc.Send(context.Background(), provisioningSession, ports.ChatUserMessage{
		Text: "hello", Origin: domain.MessageOriginHuman,
	})
	if !errors.Is(err, chatsvc.ErrNoController) {
		t.Fatalf("err = %v, want ErrNoController", err)
	}
}

// The queue advances the conversation sequence without a single provider event.
// Start must still treat this as a conversation's first controller: reserving a
// fresh provider boundary here would open a brand-new session as if it were a
// resumed one whose provider thread could not be reached.
func TestFirstControllerAfterQueuedPromptIsNotFencedAsResume(t *testing.T) {
	st, provisioningSession := openProvisioningStore(t, domain.SessionProvisionProvisioning)
	svc := provisioningService(t, st)
	ctx := context.Background()
	t.Cleanup(func() { _ = svc.Stop(context.Background(), provisioningSession) })

	if _, err := svc.Send(ctx, provisioningSession, ports.ChatUserMessage{
		Text: "the opening brief", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("queue opening prompt: %v", err)
	}

	boundaryReserved := false
	if _, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: provisioningSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
		ControllerReady: func(result chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
			boundaryReserved = result.ProviderBoundary != nil
			return chatsvc.ControllerCommit{}, nil
		},
	}); err != nil {
		t.Fatalf("start first controller: %v", err)
	}
	if boundaryReserved {
		t.Fatal("queued turns were fenced off as provider history")
	}

	// The turn has to still be there to be drained. Start settles work a dead
	// controller left behind, and a queue written before the first controller
	// existed looks exactly like that unless it is excluded.
	snapshot, err := svc.Snapshot(ctx, provisioningSession)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Turns) != 1 {
		t.Fatalf("turns = %d, want the queued opening prompt", len(snapshot.Turns))
	}
	if state := snapshot.Turns[0].State; state == domain.TurnStateFailed {
		t.Fatalf("the opening prompt was settled as orphaned work: %q", snapshot.Turns[0].ErrorMessage)
	}
}
