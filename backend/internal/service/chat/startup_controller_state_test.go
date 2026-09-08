package chat_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// The chat surface paints a "stopped" controller as a destructive alert offering
// to resume the agent. A session created seconds ago whose controller has not
// opened its conversation yet has nothing to resume — reporting it as stopped is
// a crash claim about an event that never happened, and the desktop app showed it
// on every spawn for the 3–13s the conversation row takes to appear.
// See https://github.com/Untrivial-ai/agent-orchestrator/issues/5131.

// newSessionService builds a Service over a real store with no controller ever
// started, which is exactly the state a session is in between its durable row
// and its first conversation row.
func newSessionService(t *testing.T) (*chatsvc.Service, *sqlite.Store) {
	t.Helper()
	st := openStore(t)
	svc := chatsvc.New(chatsvc.Options{
		Store:    st,
		Reader:   fullSnapshotReader(st),
		Sessions: st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: newFakeConversation()}},
		Log:      slog.New(slog.DiscardHandler),
		Now:      func() time.Time { return time.Date(2026, 9, 8, 22, 48, 1, 0, time.UTC) },
	})
	return svc, st
}

// seedChatSession writes one more chat session alongside the one openStore seeds,
// so a test can shape termination and activity without disturbing the shared row.
func seedChatSession(t *testing.T, st *sqlite.Store, mutate func(*domain.SessionRecord)) domain.SessionID {
	t.Helper()
	now := time.Now().UTC()
	rec := domain.SessionRecord{
		ProjectID: testProject,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Mode:      domain.SessionModeChat,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if mutate != nil {
		mutate(&rec)
	}
	created, err := st.CreateSession(context.Background(), rec)
	if err != nil {
		t.Fatalf("seed chat session: %v", err)
	}
	if mutate != nil {
		// CreateSession assigns the id, so anything the row must carry beyond the
		// insert defaults is written back against the real id.
		created.Activity = rec.Activity
		created.IsTerminated = rec.IsTerminated
		if err := st.UpdateSession(context.Background(), created); err != nil {
			t.Fatalf("shape seeded session: %v", err)
		}
	}
	return created.ID
}

func TestSnapshotReportsAStartingControllerRatherThanACrash(t *testing.T) {
	svc, st := newSessionService(t)
	id := seedChatSession(t, st, nil)

	snapshot, err := svc.Snapshot(context.Background(), id)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Controller != ports.ChatControllerConnecting {
		t.Fatalf("controller = %q, want %q for a session that has never had one",
			snapshot.Controller, ports.ChatControllerConnecting)
	}
	if snapshot.Conversation.ID != "" {
		t.Fatalf("conversation id = %q, want empty before the controller opens one",
			snapshot.Conversation.ID)
	}
}

// The paged read is what the desktop app actually calls, so it must not keep the
// old answer while the full read is fixed.
func TestSnapshotPageReportsAStartingControllerRatherThanACrash(t *testing.T) {
	svc, st := newSessionService(t)
	id := seedChatSession(t, st, nil)

	snapshot, err := svc.SnapshotPage(context.Background(), id, 0, 50)
	if err != nil {
		t.Fatalf("SnapshotPage: %v", err)
	}
	if snapshot.Controller != ports.ChatControllerConnecting {
		t.Fatalf("controller = %q, want %q for a session that has never had one",
			snapshot.Controller, ports.ChatControllerConnecting)
	}
}

// The whole risk of the fix is masking a real failure behind a startup spinner.
// Termination and a process that exited are durable facts, and both keep saying
// stopped from the first poll — no grace period, no age check.
func TestSnapshotStillReportsStoppedForSessionsThatReallyEnded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.SessionRecord)
	}{
		{
			name:   "terminated",
			mutate: func(rec *domain.SessionRecord) { rec.IsTerminated = true },
		},
		{
			name: "agent process exited",
			mutate: func(rec *domain.SessionRecord) {
				rec.Activity = domain.Activity{
					State:          domain.ActivityExited,
					LastActivityAt: time.Now().UTC(),
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, st := newSessionService(t)
			id := seedChatSession(t, st, tc.mutate)

			snapshot, err := svc.Snapshot(context.Background(), id)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if snapshot.Controller != ports.ChatControllerStopped {
				t.Fatalf("controller = %q, want %q — a real ending must not be masked",
					snapshot.Controller, ports.ChatControllerStopped)
			}

			paged, err := svc.SnapshotPage(context.Background(), id, 0, 50)
			if err != nil {
				t.Fatalf("SnapshotPage: %v", err)
			}
			if paged.Controller != ports.ChatControllerStopped {
				t.Fatalf("paged controller = %q, want %q",
					paged.Controller, ports.ChatControllerStopped)
			}
		})
	}
}

// A session that HAS a conversation but no live controller is the genuine
// stopped case — the agent ran and its controller is gone. It must keep the
// crash banner and its Resume affordance.
func TestSnapshotReportsStoppedOnceAConversationExistsWithoutAController(t *testing.T) {
	svc, st := newSessionService(t)
	id := seedChatSession(t, st, nil)
	if _, err := st.CreateConversation(
		context.Background(), "conv-1", domain.ConversationScopeSession,
		testProject, id, time.Now().UTC(),
	); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	snapshot, err := svc.Snapshot(context.Background(), id)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Controller != ports.ChatControllerStopped {
		t.Fatalf("controller = %q, want %q after a real controller stopped",
			snapshot.Controller, ports.ChatControllerStopped)
	}
}
