package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	usagepipeline "github.com/aoagents/agent-orchestrator/backend/internal/observe/usage"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
)

// TestUsagePipelineWatchRootsIncludesKimiWrites catches daemon wiring that
// registers Kimi sources but omits their managed root from the file watcher.
func TestUsagePipelineWatchRootsIncludesKimiWrites(t *testing.T) {
	kimiHome := filepath.Join(t.TempDir(), "kimi")
	wireDir := filepath.Join(kimiHome, "sessions", "native-1", "agents", "main")
	if err := os.MkdirAll(wireDir, 0o700); err != nil {
		t.Fatal(err)
	}
	wirePath := filepath.Join(wireDir, "wire.jsonl")
	if err := os.WriteFile(wirePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	roots := usagesvc.SourceRoots{
		ClaudeProjects: t.TempDir(),
		CodexSessions:  t.TempDir(),
		CodexArchived:  t.TempDir(),
		KimiHome:       kimiHome,
	}
	watcher, err := usagepipeline.NewTranscriptWatcher(
		context.Background(), usagePipelineWatchRoots(roots),
	)
	if err != nil {
		t.Fatalf("create usage watcher: %v", err)
	}
	if err := watcher.Rebuild(context.Background(), []string{wirePath}); err != nil {
		t.Fatalf("rebuild usage watcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := watcher.Start(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("usage watcher did not stop")
		}
	})

	if err := os.WriteFile(wirePath, []byte("{\"updated\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantPath, err := filepath.EvalSymlinks(wirePath)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-watcher.Events():
		if event.Path != wantPath {
			t.Fatalf("usage event path = %q, want %q", event.Path, wantPath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kimi wire write did not reach the usage watcher")
	}
}
