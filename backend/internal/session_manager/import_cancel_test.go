package sessionmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// These wrappers honor cancellation like SQLite and lifecycle; the base fakes do not.
type importCancelStore struct{ *fakeStore }

func (s *importCancelStore) DeleteSession(ctx context.Context, id domain.SessionID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.fakeStore.DeleteSession(ctx, id)
}
func (s *importCancelStore) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.SessionRecord{}, false, err
	}
	return s.fakeStore.GetSession(ctx, id)
}
func (s *importCancelStore) UpdateSession(ctx context.Context, rec domain.SessionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.fakeStore.UpdateSession(ctx, rec)
}

type importCancelLifecycle struct{ *fakeLCM }

func (l *importCancelLifecycle) MarkTerminated(ctx context.Context, id domain.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.fakeLCM.MarkTerminated(ctx, id)
}

type importCancelWorkspace struct {
	*fakeWorkspace
	cleanupBounded bool
	forced         bool
}

func (w *importCancelWorkspace) Destroy(ctx context.Context, info ports.WorkspaceInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, w.cleanupBounded = ctx.Deadline()
	return w.fakeWorkspace.Destroy(ctx, info)
}
func (w *importCancelWorkspace) ForceDestroy(context.Context, ports.WorkspaceInfo) error {
	w.forced = true
	return errors.New("must not force cleanup")
}

func TestImportCanceledChatStartupDoesNotLeaveLiveSeed(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		name := "clean"
		if dirty {
			name = "dirty-preserved"
		}
		t.Run(name, func(t *testing.T) {
			m, st, ws := importManager(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if dirty {
				ws.destroyErr = errors.New("dirty worktree must be preserved")
			}
			workspace := &importCancelWorkspace{fakeWorkspace: ws}
			m.workspace = workspace
			m.store = &importCancelStore{fakeStore: st}
			m.lcm = &importCancelLifecycle{fakeLCM: &fakeLCM{store: st}}
			m.chat = &recordingLauncher{beforeStart: func(ChatStart) { cancel() }, startErr: context.Canceled}
			_, _, _, err := m.Spawn(ctx, importSpawnConfig("feature/import"))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Spawn error = %v, want cancellation", err)
			}
			for _, rec := range st.sessions {
				if !rec.IsTerminated {
					t.Fatalf("canceled import left live phantom row: %+v", rec)
				}
			}
			if !workspace.cleanupBounded {
				t.Fatal("cleanup must have an independent bounded context")
			}
			if workspace.forced {
				t.Fatal("cleanup must never force-delete a workspace")
			}
			if dirty {
				if len(st.sessions) != 1 {
					t.Fatalf("dirty workspace lost its durable record: %d rows", len(st.sessions))
				}
				for _, rec := range st.sessions {
					if rec.Metadata.WorkspacePath == "" {
						t.Fatal("dirty workspace path was not preserved")
					}
				}
			} else if len(st.sessions) != 0 {
				t.Fatalf("clean seed remains: %d rows", len(st.sessions))
			}
		})
	}
}
