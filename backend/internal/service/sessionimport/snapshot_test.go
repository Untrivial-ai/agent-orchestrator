package sessionimport

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// countingSource reports how many times a scan actually reached the disk.
type countingSource struct {
	inner Source
	scans int
}

func (c *countingSource) Provider() domain.AgentHarness { return c.inner.Provider() }
func (c *countingSource) Discover(ctx context.Context, opts DiscoverOptions) ([]ImportableSession, error) {
	c.scans++
	return c.inner.Discover(ctx, opts)
}

// Importing a whole history resolves one id after another. Rescanning every
// transcript for each of them is what made a bulk import take minutes.
func TestLocateReusesOneScanAcrossABurst(t *testing.T) {
	root := t.TempDir()
	claudeDir := filepath.Join(root, ".claude")
	writeFile(t, filepath.Join(claudeDir, "projects", "-p", "33333333-3333-4333-8333-333333333333.jsonl"), claudeToolTranscript)
	writeFile(t, filepath.Join(claudeDir, "projects", "-p", "22222222-2222-4222-8222-222222222222.jsonl"), claudeGreetingTranscript)

	src := &countingSource{inner: NewClaudeSourceAt(claudeDir)}
	svc := NewService(nil, src)

	for i := 0; i < 10; i++ {
		if _, ok, err := svc.Locate(context.Background(), domain.HarnessClaudeCode, "33333333-3333-4333-8333-333333333333"); err != nil || !ok {
			t.Fatalf("locate %d: ok=%v err=%v", i, ok, err)
		}
	}
	if src.scans != 1 {
		t.Errorf("ten lookups should share one scan, got %d scans", src.scans)
	}
}

// The cache must not outlive its window, or a conversation created just after a
// burst would stay invisible to import.
func TestLocateScanExpires(t *testing.T) {
	root := t.TempDir()
	claudeDir := filepath.Join(root, ".claude")
	writeFile(t, filepath.Join(claudeDir, "projects", "-p", "33333333-3333-4333-8333-333333333333.jsonl"), claudeToolTranscript)

	src := &countingSource{inner: NewClaudeSourceAt(claudeDir)}
	svc := NewService(nil, src)

	now := time.Now()
	svc.snapshots.now = func() time.Time { return now }

	if _, _, err := svc.Locate(context.Background(), domain.HarnessClaudeCode, "33333333-3333-4333-8333-333333333333"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(snapshotTTL + time.Second)
	if _, _, err := svc.Locate(context.Background(), domain.HarnessClaudeCode, "33333333-3333-4333-8333-333333333333"); err != nil {
		t.Fatal(err)
	}
	if src.scans != 2 {
		t.Errorf("an expired scan must be redone, got %d scans", src.scans)
	}
}

// The browse list is what a user reads, so it is never served from the cache.
func TestDiscoverAlwaysScansFresh(t *testing.T) {
	root := t.TempDir()
	claudeDir := filepath.Join(root, ".claude")
	writeFile(t, filepath.Join(claudeDir, "projects", "-p", "33333333-3333-4333-8333-333333333333.jsonl"), claudeToolTranscript)

	src := &countingSource{inner: NewClaudeSourceAt(claudeDir)}
	svc := NewService(nil, src)

	for i := 0; i < 3; i++ {
		if _, err := svc.Discover(context.Background(), DiscoverOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if src.scans != 3 {
		t.Errorf("the browse list must not be cached, got %d scans for 3 listings", src.scans)
	}
}
