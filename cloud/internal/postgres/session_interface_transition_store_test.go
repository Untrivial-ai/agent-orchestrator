package postgres

import (
	"strings"
	"testing"
)

func TestRenewCoordinatedInterfaceClaimIsOwnerFenced(t *testing.T) {
	if !strings.Contains(renewCoordinatedInterfaceClaimSQL, "WHERE id = $2 AND claimed_by = $1") {
		t.Fatal("renew query must only update the coordinator that owns the claim")
	}
}

func TestCommitCoordinatedSessionInterfaceIsTransitionFenced(t *testing.T) {
	for _, predicate := range []string{
		"transition.id = $2",
		"transition.org_id = $3",
		"transition.claimed_by = $4",
		"transition.phase = 'source_stopped'",
		"transition.session_id = session.id",
	} {
		if !strings.Contains(commitCoordinatedSessionInterfaceSQL, predicate) {
			t.Fatalf("commit query is missing fencing predicate %q", predicate)
		}
	}
}
