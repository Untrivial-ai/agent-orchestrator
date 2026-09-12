package sessionimportsvc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"

	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
)

func TestDiscoveryRequiresProject(t *testing.T) {
	svc := New(&fakeSessions{}, &fakeStore{}, &fakeProjects{})
	if _, err := svc.Discover(context.Background(), sessionimport.DiscoverOptions{}, ""); err == nil {
		t.Fatal("global discovery must be rejected; a registered project is required")
	}
}

func TestDirectImportEnforcesProjectAgeAndTokens(t *testing.T) {
	for _, tc := range []struct {
		name, cwd string
		age       int
		tokens    int64
		want      bool
	}{
		{"eligible", "/project", 1, 15000, true},
		{"below cutoff", "/project", 1, 14999, false},
		{"old", "/project", 16, 15000, false},
		{"wrong project", "/other", 1, 15000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &fakeSessions{}
			source := &fakeSource{provider: domain.HarnessCodex, sessions: []sessionimport.ImportableSession{{Provider: domain.HarnessCodex, NativeSessionID: "id", CWD: tc.cwd, LastActivity: time.Now().AddDate(0, 0, -tc.age), TokenCount: tc.tokens}}}
			projects := &fakeProjects{list: []projectsvc.Summary{{ID: "p", Path: "/project"}}}
			svc := New(sessions, &fakeStore{}, projects, source)
			_, _, err := svc.Import(context.Background(), domain.HarnessCodex, "id", "p")
			if (err == nil) != tc.want {
				t.Fatalf("import error %v; want success %v", err, tc.want)
			}
			if !tc.want && sessions.spawned.Harness != "" {
				t.Fatal("ineligible direct import spawned a session")
			}
		})
	}
}

func TestDuplicateCannotMoveToNestedProject(t *testing.T) {
	store := &fakeStore{recs: []domain.SessionRecord{{ID: "existing", ProjectID: "parent", Harness: domain.HarnessCodex, Metadata: domain.SessionMetadata{ProviderConversationID: "id"}}}}
	sessions := &fakeSessions{}
	svc := New(sessions, store, &fakeProjects{list: []projectsvc.Summary{{ID: "nested", Path: "/project/nested"}}})
	if _, _, err := svc.Import(context.Background(), domain.HarnessCodex, "id", "nested"); err == nil {
		t.Fatal("same native conversation must not be imported into another project")
	}
	if sessions.spawned.Harness != "" {
		t.Fatal("duplicate spawned")
	}
}

func TestExternalWorktreeBelongsToRegisteredProject(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	tree := filepath.Join(root, "outside")
	admin := filepath.Join(repo, ".git", "worktrees", "outside")
	for _, path := range []string{admin, tree} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tree, ".git"), []byte("gitdir: "+admin+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0644); err != nil {
		t.Fatal(err)
	}
	scope := projectScope([]projectsvc.Summary{{ID: "p", Path: repo}}, "p")
	if !scope(tree) || !scope(filepath.Join(tree, "src")) {
		t.Fatal("external worktree was not attributed to its repository")
	}
	if scope(filepath.Join(root, "unrelated")) {
		t.Fatal("unrelated folder included")
	}
}
