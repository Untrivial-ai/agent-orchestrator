package usage

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
)

// TestPipelineIngestsDiscoveredClaudeContinuation reproduces the #5745 shape:
// a claude session resumes into a new transcript uuid in the same project
// directory without emitting any hook. Reconcile must register the continuation
// (cwd matches the session workspace) and the pipeline must ingest both files,
// with the write-once event key keeping replays clean.
func TestPipelineIngestsDiscoveredClaudeContinuation(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store, session := seedUsageTestSession(
		t, t.TempDir(), "usage", domain.HarnessClaudeCode, domain.ActivityIdle, "old-root", now,
	)
	session.Metadata.WorkspacePath = filepath.Join(t.TempDir(), "worktree")
	mustNoError(t, store.UpdateSession(ctx, session))
	binding := seedUsageTestBinding(t, store, session, "old-root", domain.UsageBindingActive, now)

	claudeRoot := t.TempDir()
	projectDir := filepath.Join(claudeRoot, "encoded-worktree")
	assistantLine := func(msgID string, in, out int) string {
		return `{"type":"assistant","uuid":"` + msgID + `","message":{"id":"` + msgID + `","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":` +
			strconv.Itoa(in) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":` + strconv.Itoa(out) + `}}}` + "\n"
	}
	writeLine(t, filepath.Join(projectDir, "old-root.jsonl"), assistantLine("msg-1", 8, 2))
	writeLine(t, filepath.Join(projectDir, "continuation-root.jsonl"),
		`{"type":"last-prompt","sessionId":"continuation-root"}`+"\n"+
			`{"type":"user","sessionId":"continuation-root","cwd":"`+session.Metadata.WorkspacePath+`"}`+"\n"+
			assistantLine("msg-2", 30, 10))

	collector := usagesvc.NewCollector(store, usagesvc.SourceRoots{ClaudeProjects: claudeRoot}, nil)
	mustNoError(t, collector.ReconcileSources(ctx, -1))

	continuation, ok, err := store.GetUsageBinding(ctx, session.ID, session.Harness, "continuation-root")
	if err != nil || !ok {
		t.Fatalf("continuation binding ok=%v err=%v", ok, err)
	}
	if continuation.State != domain.UsageBindingActive {
		t.Fatalf("continuation binding state=%s, want active", continuation.State)
	}
	sources, err := store.ListUsageSourcesForBinding(ctx, binding.ID)
	mustNoError(t, err)
	if len(sources) != 1 {
		t.Fatalf("old binding sources = %d, want 1", len(sources))
	}

	ingestor := NewIngestor(store, IngestorConfig{Clock: func() time.Time { return now }})
	ingestAllWatchable(ctx, t, store, ingestor)
	// old-root: 8+2; continuation-root: 30+10.
	assertTokenAggregate(t, store, session.ID, 50)

	// A reconcile replay must not double count.
	mustNoError(t, collector.ReconcileSources(ctx, -1))
	ingestAllWatchable(ctx, t, store, ingestor)
	assertTokenAggregate(t, store, session.ID, 50)
}

func writeLine(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
