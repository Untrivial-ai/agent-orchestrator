package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestTmuxServerClientAssociationRoundTrip(t *testing.T) {
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	if _, _, _, found, err := store.GetTmuxServerClient(ctx, "ao"); err != nil || found {
		t.Fatalf("initial GetTmuxServerClient = found=%v err=%v, want absent", found, err)
	}
	wantTime := time.Date(2026, 9, 28, 1, 2, 3, 0, time.UTC)
	wantHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := store.UpsertTmuxServerClient(ctx, "ao", "/tmp/tmux.retained", wantHash, true, wantTime); err != nil {
		t.Fatal(err)
	}
	path, hash, managed, found, err := store.GetTmuxServerClient(ctx, "ao")
	if err != nil || !found || path != "/tmp/tmux.retained" || hash != wantHash || !managed {
		t.Fatalf("GetTmuxServerClient = (%q, %q, %v, %v, %v)", path, hash, managed, found, err)
	}
	if err := store.DeleteTmuxServerClient(ctx, "ao"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, found, err := store.GetTmuxServerClient(ctx, "ao"); err != nil || found {
		t.Fatalf("GetTmuxServerClient after delete = found=%v err=%v", found, err)
	}
}
