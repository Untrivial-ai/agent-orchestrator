package activity

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestObserverIntegrationReconcilesRealTmuxOutputIntoSQLite(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}

	tests := []struct {
		name      string
		output    string
		wantState domain.ActivityState
	}{
		{
			name:      "idle composer repairs missed stop",
			output:    "\n› Write tests for @filename\n\ngpt-5.6-sol low · ~/project\n",
			wantState: domain.ActivityIdle,
		},
		{
			name:      "working marker prevents demotion",
			output:    "\n• Working (3m 10s • esc to interrupt)\n› Add tests\n\ngpt-5.6-sol low · ~/project\n",
			wantState: domain.ActivityActive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := sqlitetest.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })

			workspace := t.TempDir()
			now := time.Now().UTC()
			staleAt := now.Add(-3 * time.Minute)
			projectID := domain.ProjectID("ao3115e2e")
			if err := store.UpsertProject(ctx, domain.ProjectRecord{
				ID:           string(projectID),
				Path:         workspace,
				RegisteredAt: staleAt,
			}); err != nil {
				t.Fatal(err)
			}
			session, err := store.CreateSession(ctx, domain.SessionRecord{
				ProjectID:     projectID,
				Kind:          domain.KindWorker,
				Harness:       domain.HarnessOpenCode,
				Activity:      domain.Activity{State: domain.ActivityActive, LastActivityAt: staleAt},
				FirstSignalAt: staleAt,
				CreatedAt:     staleAt,
				UpdatedAt:     staleAt,
			})
			if err != nil {
				t.Fatal(err)
			}

			runtime := tmux.New(tmux.Options{Timeout: 5 * time.Second, ReapGrace: 100 * time.Millisecond})
			handle, err := runtime.Create(ctx, ports.RuntimeConfig{
				SessionID:     session.ID,
				WorkspacePath: workspace,
				Argv:          []string{"sh", "-c", "printf '%s' " + shellQuote(tt.output) + "; sleep 30"},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runtime.Destroy(context.Background(), handle) })

			waitForTerminalOutput(t, runtime, handle, "gpt-5.6-sol low · ~/project")
			session.Metadata.RuntimeHandleID = handle.ID
			session.Metadata.RuntimeLaunchID = "launch-1"
			if err := store.UpdateSession(ctx, session); err != nil {
				t.Fatal(err)
			}

			manager := lifecycle.New(store, nil)
			observer := New(
				store,
				manager,
				runtime,
				fakeAgents{domain.HarnessOpenCode: detectorAgent{
					Plugin:     opencode.New(),
					continuous: true,
					detect: func(output string) (domain.ActivityState, bool) {
						if strings.Contains(output, "• Working") {
							return domain.ActivityActive, true
						}
						return domain.ActivityIdle, true
					},
				}},
				Config{
					StaleAfter: time.Minute,
					Clock:      func() time.Time { return now },
					Logger:     testLogger(),
				},
			)
			if err := observer.Poll(ctx); err != nil {
				t.Fatal(err)
			}

			got, ok, err := store.GetSession(ctx, session.ID)
			if err != nil || !ok {
				t.Fatalf("GetSession: ok=%v err=%v", ok, err)
			}
			if got.Activity.State != tt.wantState {
				t.Fatalf("activity state = %q, want %q", got.Activity.State, tt.wantState)
			}
		})
	}
}

func waitForTerminalOutput(t *testing.T, runtime *tmux.Runtime, handle ports.RuntimeHandle, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output, err := runtime.GetOutput(context.Background(), handle, 40)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("terminal output never contained %q", want)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
