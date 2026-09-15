package workertransport

import (
	context "context"
	os "os"
	exec "os/exec"
	"path/filepath"
	strings "strings"
	testing "testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestWorkspaceDiffFileReturnsDaemonParityForModifiedAndUntrackedText(t *testing.T) {
	workspacePath := newGitWorkspace(t)
	writeWorkspaceFile(t, workspacePath, "README.md", "before\n")
	gitWorkspace(t, workspacePath, "add", "README.md")
	gitWorkspace(t, workspacePath, "commit", "-m", "initial")
	writeWorkspaceFile(t, workspacePath, "README.md", "after\n")
	writeWorkspaceFile(t, workspacePath, "notes.txt", "new note\n")

	workspace, err := openWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	defer workspace.Close()

	modified, err := workspace.DiffFile(context.Background(), worker.WorkspaceDiffFileRequest{Path: "README.md"})
	if err != nil {
		t.Fatalf("read modified diff file: %v", err)
	}
	if modified.Status != "modified" || modified.Additions != 1 || modified.Deletions != 1 || modified.Content != "after\n" || modified.Binary || modified.Deleted || modified.DiffTruncated {
		t.Fatalf("modified detail = %+v", modified)
	}
	if !strings.Contains(modified.Diff, "-before") || !strings.Contains(modified.Diff, "+after") {
		t.Fatalf("modified diff = %q", modified.Diff)
	}

	untracked, err := workspace.DiffFile(context.Background(), worker.WorkspaceDiffFileRequest{Path: "notes.txt"})
	if err != nil {
		t.Fatalf("read untracked diff file: %v", err)
	}
	if untracked.Status != "untracked" || untracked.Content != "new note\n" || untracked.Binary || untracked.Deleted || untracked.DiffTruncated {
		t.Fatalf("untracked detail = %+v", untracked)
	}
	if !strings.Contains(untracked.Diff, "new file mode 100644") || !strings.Contains(untracked.Diff, "+new note") {
		t.Fatalf("untracked diff = %q", untracked.Diff)
	}
}

func TestWorkspaceDiffFileRepresentsDeletedAndBinaryFilesWithoutContent(t *testing.T) {
	workspacePath := newGitWorkspace(t)
	writeWorkspaceFile(t, workspacePath, "gone.txt", "remove me\n")
	writeWorkspaceFile(t, workspacePath, "image.bin", string([]byte{0, 1, 2, 3}))
	gitWorkspace(t, workspacePath, "add", "gone.txt", "image.bin")
	gitWorkspace(t, workspacePath, "commit", "-m", "initial")
	if err := os.Remove(filepath.Join(workspacePath, "gone.txt")); err != nil {
		t.Fatalf("remove tracked file: %v", err)
	}
	writeWorkspaceFile(t, workspacePath, "image.bin", string([]byte{0, 4, 5, 6}))

	workspace, err := openWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	defer workspace.Close()

	deleted, err := workspace.DiffFile(context.Background(), worker.WorkspaceDiffFileRequest{Path: "gone.txt"})
	if err != nil {
		t.Fatalf("read deleted diff file: %v", err)
	}
	if deleted.Status != "deleted" || !deleted.Deleted || deleted.Content != "" || deleted.Size != 0 || deleted.Diff == "" {
		t.Fatalf("deleted detail = %+v", deleted)
	}

	binary, err := workspace.DiffFile(context.Background(), worker.WorkspaceDiffFileRequest{Path: "image.bin"})
	if err != nil {
		t.Fatalf("read binary diff file: %v", err)
	}
	if !binary.Binary || binary.Content != "" || binary.Deleted || !strings.Contains(binary.Diff, "Binary files") {
		t.Fatalf("binary detail = %+v", binary)
	}
}

func TestWorkspaceDiffIncludesPerFileLineCounts(t *testing.T) {
	workspacePath := newGitWorkspace(t)
	writeWorkspaceFile(t, workspacePath, "README.md", "before\nremove\n")
	gitWorkspace(t, workspacePath, "add", "README.md")
	gitWorkspace(t, workspacePath, "commit", "-m", "initial")
	writeWorkspaceFile(t, workspacePath, "README.md", "after\n")
	writeWorkspaceFile(t, workspacePath, "notes.txt", "one\ntwo\n")

	workspace, err := openWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	defer workspace.Close()

	diff, err := workspace.Diff(context.Background())
	if err != nil {
		t.Fatalf("workspace diff: %v", err)
	}
	files, ok := diff["files"].([]map[string]any)
	if !ok {
		t.Fatalf("files = %#v", diff["files"])
	}
	counts := make(map[string][2]int, len(files))
	for _, file := range files {
		path, _ := file["path"].(string)
		additions, _ := file["additions"].(int)
		deletions, _ := file["deletions"].(int)
		counts[path] = [2]int{additions, deletions}
	}
	if got := counts["README.md"]; got != [2]int{1, 2} {
		t.Fatalf("README.md counts = %v, want [1 2]", got)
	}
	if got := counts["notes.txt"]; got != [2]int{2, 0} {
		t.Fatalf("notes.txt counts = %v, want [2 0]", got)
	}
}

func newGitWorkspace(t *testing.T) string {
	t.Helper()
	workspacePath := t.TempDir()
	gitWorkspace(t, workspacePath, "init")
	gitWorkspace(t, workspacePath, "config", "user.name", "AO Test")
	gitWorkspace(t, workspacePath, "config", "user.email", "test@example.com")
	return workspacePath
}

func writeWorkspaceFile(t *testing.T, workspacePath, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspacePath, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func gitWorkspace(t *testing.T, workspacePath string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", workspacePath}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}
