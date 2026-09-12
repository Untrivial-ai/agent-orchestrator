package sessionimportsvc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestBatchPersists180HistoriesWithoutLaunchingAndRetriesWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	st := sqlitetest.MustOpen(t)
	if err := st.UpsertProject(ctx, domain.ProjectRecord{ID: "p", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	project, _, err := st.GetProject(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{provider: domain.HarnessCodex}
	var selected []Selection
	for i := range 180 {
		id := fmt.Sprintf("native-%d", i)
		selected = append(selected, Selection{Provider: "codex", NativeSessionID: id})
		source.sessions = append(source.sessions, sessionimport.ImportableSession{Provider: domain.HarnessCodex, NativeSessionID: id, CWD: project.Path, TranscriptPath: "/history/" + id + ".jsonl", LastActivity: time.Now(), TokenCount: 15000})
	}
	// A real SQLite boundary and no manager: no process/worktree can launch.
	svc := New(sessionsvc.New(nil, st), st, &fakeProjects{list: []projectsvc.Summary{{ID: "p", Path: project.Path}}}, source)
	started := time.Now()
	results := svc.ImportBatch(ctx, "p", selected)
	t.Logf("180 first imports: %s", time.Since(started))
	if len(results) != 180 {
		t.Fatalf("results=%d", len(results))
	}
	for _, result := range results {
		if result.Error != "" || result.SessionID == "" || result.AlreadyImported {
			t.Fatalf("first import: %+v", result)
		}
	}
	for _, result := range svc.ImportBatch(ctx, "p", selected) {
		if result.Error != "" || !result.AlreadyImported {
			t.Fatalf("retry: %+v", result)
		}
	}
	rows, err := st.ListAllSessions(ctx)
	if err != nil || len(rows) != 180 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := svc.ImportBatch(cancelled, "p", selected); len(got) != 0 {
		t.Fatal("cancelled batch performed work")
	}
}

type rejectedImportSource struct{}

func (rejectedImportSource) Provider() domain.AgentHarness { return domain.HarnessClaudeCode }
func (rejectedImportSource) Discover(context.Context, sessionimport.DiscoverOptions) ([]sessionimport.ImportableSession, error) {
	return nil, fmt.Errorf("unselected provider must not be scanned")
}
func TestBatchDoesNotScanUnselectedProvider(t *testing.T) {
	source := &fakeSource{provider: domain.HarnessCodex, sessions: []sessionimport.ImportableSession{{Provider: domain.HarnessCodex, NativeSessionID: "selected", CWD: "/project", TokenCount: MinimumTokens, LastActivity: time.Now()}}}
	svc := New(&fakeSessions{}, &fakeStore{}, &fakeProjects{list: []projectsvc.Summary{{ID: "p", Path: "/project"}}}, source, rejectedImportSource{})
	got := svc.ImportBatch(context.Background(), "p", []Selection{{Provider: string(domain.HarnessCodex), NativeSessionID: "selected"}})
	if len(got) != 1 || got[0].Error != "" || got[0].SessionID == "" {
		t.Fatalf("unrelated provider blocked selected import: %+v", got)
	}
}
