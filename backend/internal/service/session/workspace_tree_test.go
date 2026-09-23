package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestListWorkspaceTreeSingleRepoListsOneLevelWithStatus(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "goodbye\nupdated\n")
	writeWorkspaceFile(t, repo, "notes.txt", "note\n")
	writeWorkspaceFile(t, repo, "src/util.go", "package main\n")
	writeWorkspaceFile(t, repo, "docs/guide.md", "guide\n")
	runGit(t, repo, "add", "docs/guide.md")
	runGit(t, repo, "commit", "-m", "add docs")
	writeWorkspaceFile(t, repo, "node_modules/cache.txt", "ignored\n")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	svc := &Service{store: st}

	root, err := svc.ListWorkspaceTree(context.Background(), "ao-1", "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]WorkspaceTreeEntry{}
	var order []string
	for _, entry := range root.Entries {
		byName[entry.Name] = entry
		order = append(order, entry.Name)
	}
	wantOrder := []string{"docs", "src", ".gitignore", "README.md", "notes.txt"}
	if strings.Join(order, "|") != strings.Join(wantOrder, "|") {
		t.Fatalf("root entry order = %v, want %v", order, wantOrder)
	}
	if e := byName["docs"]; e.Type != WorkspaceTreeDir || e.HasChanges {
		t.Fatalf("docs = %#v, want unchanged dir", e)
	}
	if e := byName["src"]; e.Type != WorkspaceTreeDir || !e.HasChanges {
		t.Fatalf("src = %#v, want dir with changes", e)
	}
	if e := byName["README.md"]; e.Type != WorkspaceTreeFile || e.Status != WorkspaceFileModified {
		t.Fatalf("README.md = %#v, want modified file", e)
	}
	if e := byName["notes.txt"]; e.Type != WorkspaceTreeFile || e.Status != WorkspaceFileAdded {
		t.Fatalf("notes.txt = %#v, want added file", e)
	}
	if e := byName[".gitignore"]; e.Type != WorkspaceTreeFile || e.Status != WorkspaceFileUnmodified {
		t.Fatalf(".gitignore = %#v, want unmodified file", e)
	}
	if _, ok := byName["node_modules"]; ok {
		t.Fatal("gitignored node_modules directory was listed")
	}

	src, err := svc.ListWorkspaceTree(context.Background(), "ao-1", "src")
	if err != nil {
		t.Fatal(err)
	}
	srcByName := map[string]WorkspaceTreeEntry{}
	for _, entry := range src.Entries {
		srcByName[entry.Name] = entry
	}
	if e := srcByName["app.go"]; e.Type != WorkspaceTreeFile || e.Status != WorkspaceFileUnmodified || e.Path != "src/app.go" {
		t.Fatalf("src/app.go = %#v, want unmodified with full path", e)
	}
	if e := srcByName["util.go"]; e.Type != WorkspaceTreeFile || e.Status != WorkspaceFileAdded {
		t.Fatalf("src/util.go = %#v, want added file", e)
	}

	empty, err := svc.ListWorkspaceTree(context.Background(), "ao-1", "does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Entries) != 0 {
		t.Fatalf("nonexistent dir entries = %v, want empty", empty.Entries)
	}
}

func TestListWorkspaceTreeWorkspaceProjectChildRepo(t *testing.T) {
	root := newWorkspaceRepo(t)
	rootBase := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	child := filepath.Join(root, "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, child, "init")
	runGit(t, child, "config", "user.email", "ao@example.com")
	runGit(t, child, "config", "user.name", "AO Tests")
	writeWorkspaceFile(t, child, "service.go", "package api\n")
	runGit(t, child, "add", ".")
	runGit(t, child, "commit", "-m", "initial child")
	childBase := strings.TrimSpace(runGit(t, child, "rev-parse", "HEAD"))
	runGit(t, child, "switch", "-c", "ao/work")
	writeWorkspaceFile(t, child, "service.go", "package api\n\nfunc Added() {}\n")
	runGit(t, child, "add", "service.go")
	runGit(t, child, "commit", "-m", "child change")

	st := newFakeStore()
	st.projects["ws"] = domain.ProjectRecord{ID: "ws", Kind: domain.ProjectKindWorkspace}
	st.sessions["ws-1"] = domain.SessionRecord{
		ID:        "ws-1",
		ProjectID: "ws",
		Metadata:  domain.SessionMetadata{WorkspacePath: root},
	}
	st.worktrees["ws-1"] = []domain.SessionWorktreeRecord{
		{SessionID: "ws-1", RepoName: domain.RootWorkspaceRepoName, WorktreePath: root, BaseSHA: rootBase},
		{SessionID: "ws-1", RepoName: "api", WorktreePath: child, BaseSHA: childBase},
	}
	svc := &Service{store: st}

	rootTree, err := svc.ListWorkspaceTree(context.Background(), "ws-1", "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]WorkspaceTreeEntry{}
	for _, entry := range rootTree.Entries {
		byName[entry.Name] = entry
	}
	apiEntry, ok := byName["api"]
	if !ok || apiEntry.Type != WorkspaceTreeDir || !apiEntry.HasChanges {
		t.Fatalf("api entry = %#v ok=%v, want changed child-repo dir", apiEntry, ok)
	}
	if apiEntry.Path != "api" {
		t.Fatalf("api entry path = %q, want %q", apiEntry.Path, "api")
	}

	childTree, err := svc.ListWorkspaceTree(context.Background(), "ws-1", "api")
	if err != nil {
		t.Fatal(err)
	}
	childByName := map[string]WorkspaceTreeEntry{}
	for _, entry := range childTree.Entries {
		childByName[entry.Name] = entry
	}
	serviceEntry := childByName["service.go"]
	if serviceEntry.Type != WorkspaceTreeFile || serviceEntry.Status != WorkspaceFileModified || serviceEntry.Path != "api/service.go" {
		t.Fatalf("api/service.go = %#v, want modified file with full path", serviceEntry)
	}
}

func TestListWorkspaceTreeScratchUsesFilesystem(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "README.md", "one\ntwo\n")
	writeWorkspaceFile(t, root, "nested/task.txt", "do it")
	writeWorkspaceFile(t, root, ".git/config", "[core]\n")

	st := newFakeStore()
	st.projects["scratch"] = domain.ProjectRecord{ID: "scratch", Kind: domain.ProjectKindScratch}
	st.sessions["scratch-1"] = domain.SessionRecord{
		ID:        "scratch-1",
		ProjectID: "scratch",
		Metadata:  domain.SessionMetadata{WorkspacePath: root},
	}
	svc := &Service{store: st}

	rootTree, err := svc.ListWorkspaceTree(context.Background(), "scratch-1", "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]WorkspaceTreeEntry{}
	for _, entry := range rootTree.Entries {
		byName[entry.Name] = entry
	}
	if e := byName["nested"]; e.Type != WorkspaceTreeDir {
		t.Fatalf("nested = %#v, want dir", e)
	}
	if e := byName["README.md"]; e.Type != WorkspaceTreeFile || e.Status != WorkspaceFileAdded {
		t.Fatalf("README.md = %#v, want added file", e)
	}
	if _, ok := byName[".git"]; ok {
		t.Fatal(".git should not be listed for scratch")
	}

	nested, err := svc.ListWorkspaceTree(context.Background(), "scratch-1", "nested")
	if err != nil {
		t.Fatal(err)
	}
	if len(nested.Entries) != 1 || nested.Entries[0].Name != "task.txt" || nested.Entries[0].Path != "nested/task.txt" {
		t.Fatalf("nested entries = %#v, want single task.txt", nested.Entries)
	}
}

func TestListWorkspaceTreeHidesDirectoriesContainingOnlyAOManagedFiles(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, ".kimi/.gitignore", aoManagedGitignoreSentinel+"\n/.gitignore\n/AGENTS.md\n")
	writeWorkspaceFile(t, root, ".kimi/AGENTS.md", "AO instructions\n")

	st := newFakeStore()
	st.sessions["standalone-1"] = domain.SessionRecord{
		ID:       "standalone-1",
		Kind:     domain.KindWorker,
		Metadata: domain.SessionMetadata{WorkspacePath: root},
	}

	tree, err := (&Service{store: st}).ListWorkspaceTree(context.Background(), "standalone-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 0 {
		t.Fatalf("standalone root entries = %#v, want AO-managed files hidden", tree.Entries)
	}
}

func TestListWorkspaceTreeHidesAOManagedSymlink(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, ".kimi/.gitignore", aoManagedGitignoreSentinel+"\n/.gitignore\n/AGENTS.md\n")
	writeWorkspaceFile(t, root, "notes.txt", "user work\n")
	if err := os.Symlink(filepath.Join("..", "notes.txt"), filepath.Join(root, ".kimi", "AGENTS.md")); err != nil {
		t.Skipf("creating managed symlink: %v", err)
	}

	st := newFakeStore()
	st.sessions["standalone-1"] = domain.SessionRecord{
		ID:       "standalone-1",
		Kind:     domain.KindWorker,
		Metadata: domain.SessionMetadata{WorkspacePath: root},
	}

	tree, err := (&Service{store: st}).ListWorkspaceTree(context.Background(), "standalone-1", ".kimi")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 0 {
		t.Fatalf("standalone .kimi entries = %#v, want managed symlink hidden", tree.Entries)
	}
}

func TestListWorkspaceTreeHidesAOManagedCopilotProfileDirectory(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, ".github/agents/ao-standalone-4.agent.md", "---\nname: ao-standalone-4\ntarget: github-copilot\n---\n\n"+aoManagedCopilotProfileSentinel+"\n\nAO instructions\n")

	st := newFakeStore()
	st.sessions["standalone-4"] = domain.SessionRecord{
		ID:       "standalone-4",
		Kind:     domain.KindWorker,
		Metadata: domain.SessionMetadata{WorkspacePath: root},
	}

	tree, err := (&Service{store: st}).ListWorkspaceTree(context.Background(), "standalone-4", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 0 {
		t.Fatalf("standalone root entries = %#v, want AO-managed Copilot profile hidden", tree.Entries)
	}
}

func TestListWorkspaceTreeKeepsAgentDirectoryContainingUserWork(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, ".kimi/.gitignore", aoManagedGitignoreSentinel+"\n/.gitignore\n/AGENTS.md\n")
	writeWorkspaceFile(t, root, ".kimi/AGENTS.md", "AO instructions\n")
	writeWorkspaceFile(t, root, ".kimi/draft.md", "agent-created work\n")

	st := newFakeStore()
	st.sessions["standalone-1"] = domain.SessionRecord{
		ID:       "standalone-1",
		Kind:     domain.KindWorker,
		Metadata: domain.SessionMetadata{WorkspacePath: root},
	}
	svc := &Service{store: st}

	rootTree, err := svc.ListWorkspaceTree(context.Background(), "standalone-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rootTree.Entries) != 1 || rootTree.Entries[0].Path != ".kimi" {
		t.Fatalf("standalone root entries = %#v, want visible .kimi work directory", rootTree.Entries)
	}

	kimiTree, err := svc.ListWorkspaceTree(context.Background(), "standalone-1", ".kimi")
	if err != nil {
		t.Fatal(err)
	}
	if len(kimiTree.Entries) != 1 || kimiTree.Entries[0].Path != ".kimi/draft.md" {
		t.Fatalf("standalone .kimi entries = %#v, want only agent-created draft", kimiTree.Entries)
	}
}

func TestListWorkspaceTreeReadableDirectoryIgnoresUnreadableSiblingMarker(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "visible/notes.txt", "visible work\n")
	writeWorkspaceFile(t, root, "locked/.gitignore", aoManagedGitignoreSentinel+"\n/.gitignore\n/AGENTS.md\n")
	marker := filepath.Join(root, "locked", ".gitignore")
	if err := os.Chmod(marker, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })
	if _, err := os.ReadFile(marker); err == nil {
		t.Skip("filesystem does not enforce unreadable file permissions for this user")
	}

	st := newFakeStore()
	st.sessions["standalone-1"] = domain.SessionRecord{
		ID:       "standalone-1",
		Kind:     domain.KindWorker,
		Metadata: domain.SessionMetadata{WorkspacePath: root},
	}

	tree, err := (&Service{store: st}).ListWorkspaceTree(context.Background(), "standalone-1", "visible")
	if err != nil {
		t.Fatalf("list readable directory with unreadable sibling marker: %v", err)
	}
	if len(tree.Entries) != 1 || tree.Entries[0].Path != "visible/notes.txt" {
		t.Fatalf("visible entries = %#v, want notes.txt", tree.Entries)
	}
}

func TestListWorkspaceTreeGlobalCapNotPerDirectory(t *testing.T) {
	repo := newWorkspaceRepo(t)
	for i := 0; i < maxWorkspaceFiles+50; i++ {
		writeWorkspaceFile(t, repo, filepath.Join("many", fmt.Sprintf("file-%05d.txt", i)), "x\n")
	}
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	svc := &Service{store: st}

	root, err := svc.ListWorkspaceTree(context.Background(), "ao-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !root.Truncated {
		t.Fatal("root.Truncated = false, want true once the flat visible-path list exceeds the global cap")
	}
}
