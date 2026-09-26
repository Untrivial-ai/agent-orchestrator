package postgres

import (
	"strings"
	"testing"
)

func TestHarnessRequestMigrationAllowsInspectAndInstall(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/00046_harness_worker_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, kind := range []string{
		"'harness.inspect'", "'harness.install'",
		"'workspace.diff-file'", "'workspace.review.summary'",
		"'workspace.review.tree'", "'workspace.review.search'",
		"'workspace.review.file'", "'workspace.review.diffs'",
		"'workspace.review.revision'", "'workspace.review.write'",
	} {
		if !strings.Contains(migration, kind) {
			t.Errorf("migration does not allow worker request kind %s", kind)
		}
	}
}

// The read-only-session and viewer-role guards in CreateWorkspaceRequest /
// createWorkerRequest gate file mutations by kind. The review file-write path
// dispatches "workspace.review.write", so both write kinds must be recognized or
// a viewer / read-only member could overwrite files through the review endpoint.
func TestIsWorkspaceWriteKind(t *testing.T) {
	for _, kind := range []string{"workspace.write", "workspace.review.write"} {
		if !isWorkspaceWriteKind(kind) {
			t.Errorf("isWorkspaceWriteKind(%q) = false, want true (must be gated by viewer/read-only checks)", kind)
		}
	}
	for _, kind := range []string{
		"workspace.read", "workspace.list", "workspace.diff", "workspace.diff-file",
		"workspace.review", "workspace.review.file", "terminal.open", "browser.fetch",
	} {
		if isWorkspaceWriteKind(kind) {
			t.Errorf("isWorkspaceWriteKind(%q) = true, want false", kind)
		}
	}
}
