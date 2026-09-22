package postgres

import (
	"strings"
	"testing"
)

func TestWorkerRequestConstraintRepairMigrationKeepsEveryRequestKind(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/00048_worker_request_kind_union.sql")
	if err != nil {
		t.Fatalf("read worker request constraint repair migration: %v", err)
	}
	sql := string(body)
	for _, kind := range []string{
		"workspace.list",
		"workspace.read",
		"workspace.write",
		"workspace.diff",
		"workspace.diff-file",
		"workspace.review.summary",
		"workspace.review.tree",
		"workspace.review.search",
		"workspace.review.file",
		"workspace.review.diffs",
		"workspace.review.revision",
		"workspace.review.write",
		"terminal.open",
		"terminal.input",
		"terminal.resize",
		"terminal.close",
		"browser.fetch",
		"harness.inspect",
		"harness.install",
	} {
		if !strings.Contains(sql, "'"+kind+"'") {
			t.Errorf("repair migration does not allow worker request kind %q", kind)
		}
	}
}
