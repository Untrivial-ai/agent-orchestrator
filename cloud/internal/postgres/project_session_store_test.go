package postgres

import (
	"strings"
	"testing"
)

// Regression guard for the cloud session-creation outage: scanSession gained
// preference destinations while INSERT ... RETURNING still emitted the old
// field count. The insert projection and scanner must advance together.
func TestSessionInsertReturningMatchesScannerArity(t *testing.T) {
	const scanSessionDestinations = 25
	if got := strings.Count(sessionInsertReturning, ",") + 1; got != scanSessionDestinations {
		t.Fatalf("session insert RETURNING fields = %d, want %d", got, scanSessionDestinations)
	}
	for _, field := range []string{
		"reviewer_harness", "auto_review_enabled", "auto_inject_ci",
		"auto_inject_review", "terminate_on_pr_merge",
	} {
		if !strings.Contains(sessionInsertReturning, field) {
			t.Fatalf("session insert RETURNING is missing %s", field)
		}
	}
}
