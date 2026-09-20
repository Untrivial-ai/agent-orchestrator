package postgres

import (
	"strings"
	"testing"
)

func TestHarnessRequestMigrationAllowsInspectAndInstall(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/00044_harness_worker_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, kind := range []string{"'harness.inspect'", "'harness.install'"} {
		if !strings.Contains(migration, kind) {
			t.Errorf("migration does not allow worker request kind %s", kind)
		}
	}
}
