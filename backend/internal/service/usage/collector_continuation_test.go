package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

const continuationNativeB = "6d19d8df-continuation"

// seedContinuationSession seeds a live claude session whose workspace points at
// worktree, with an active binding nativeA whose transcript lives in the
// claude project dir encoded from worktree (mirroring ~/.claude/projects/<slug>).
func seedContinuationSession(
	t *testing.T,
	store *sqlite.Store,
	claudeRoot string,
	worktree string,
	nativeA string,
) domain.SessionRecord {
	t.Helper()
	session := collectorTestSessionWithActivity(
		t, store, domain.HarnessClaudeCode, nativeA, false, domain.ActivityIdle,
	)
	session.Metadata.WorkspacePath = worktree
	mustNoError(t, store.UpdateSession(context.Background(), session))
	projectDir := filepath.Join(claudeRoot, "encoded-worktree")
	writeUsageFixture(t, filepath.Join(projectDir, nativeA+".jsonl"),
		`{"type":"last-prompt","sessionId":"`+nativeA+`"}`+"\n")
	return session
}

func continuationBindingIDs(t *testing.T, store *sqlite.Store, sessionID domain.SessionID) map[string]int {
	t.Helper()
	bindings, err := store.ListUsageBindingsForSession(context.Background(), sessionID)
	mustNoError(t, err)
	sources := make(map[string]int)
	for _, binding := range bindings {
		rows, err := store.ListUsageSourcesForBinding(context.Background(), binding.ID)
		mustNoError(t, err)
		sources[binding.NativeRootID] = len(rows)
	}
	return sources
}

func TestCollectorDiscoversClaudeContinuationInWorkspaceDir(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store := collectorTestStore(t)
	claudeRoot := t.TempDir()
	worktree := t.TempDir()
	nativeA := "native-a"
	session := seedContinuationSession(t, store, claudeRoot, worktree, nativeA)
	seedCollectorUsageBinding(t, store, session, nativeA, domain.UsageBindingActive, now, "")

	collector := NewCollector(store, SourceRoots{ClaudeProjects: claudeRoot}, nil)
	writeUsageFixture(t, filepath.Join(claudeRoot, "encoded-worktree", continuationNativeB+".jsonl"),
		`{"type":"last-prompt","sessionId":"`+continuationNativeB+`"}`+"\n"+
			`{"type":"user","sessionId":"`+continuationNativeB+`","cwd":"`+worktree+`"}`+"\n")
	mustNoError(t, collector.ReconcileSources(ctx, 8))

	sources := continuationBindingIDs(t, store, session.ID)
	if _, ok := sources[continuationNativeB]; !ok {
		t.Fatalf("continuation binding %q not registered; bindings=%v", continuationNativeB, sources)
	}
	if sources[continuationNativeB] != 1 {
		t.Fatalf("continuation sources = %d, want 1", sources[continuationNativeB])
	}
}

func TestCollectorContinuationDiscoveryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store := collectorTestStore(t)
	claudeRoot := t.TempDir()
	worktree := t.TempDir()
	nativeA := "native-a"
	session := seedContinuationSession(t, store, claudeRoot, worktree, nativeA)
	seedCollectorUsageBinding(t, store, session, nativeA, domain.UsageBindingActive, now, "")

	collector := NewCollector(store, SourceRoots{ClaudeProjects: claudeRoot}, nil)
	writeUsageFixture(t, filepath.Join(claudeRoot, "encoded-worktree", continuationNativeB+".jsonl"),
		`{"type":"user","sessionId":"`+continuationNativeB+`","cwd":"`+worktree+`"}`+"\n")
	mustNoError(t, collector.ReconcileSources(ctx, 8))
	mustNoError(t, collector.ReconcileSources(ctx, 8))

	sources := continuationBindingIDs(t, store, session.ID)
	if len(sources) != 2 {
		t.Fatalf("bindings = %v, want exactly [%s %s]", sources, nativeA, continuationNativeB)
	}
	if sources[continuationNativeB] != 1 {
		t.Fatalf("continuation sources = %d, want 1", sources[continuationNativeB])
	}
}

func TestCollectorContinuationDiscoveryIgnoresCwdMismatch(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store := collectorTestStore(t)
	claudeRoot := t.TempDir()
	worktree := t.TempDir()
	nativeA := "native-a"
	session := seedContinuationSession(t, store, claudeRoot, worktree, nativeA)
	seedCollectorUsageBinding(t, store, session, nativeA, domain.UsageBindingActive, now, "")

	collector := NewCollector(store, SourceRoots{ClaudeProjects: claudeRoot}, nil)
	writeUsageFixture(t, filepath.Join(claudeRoot, "encoded-worktree", continuationNativeB+".jsonl"),
		`{"type":"user","sessionId":"`+continuationNativeB+`","cwd":"`+filepath.Join(worktree, "elsewhere")+`"}`+"\n")
	mustNoError(t, collector.ReconcileSources(ctx, 8))

	sources := continuationBindingIDs(t, store, session.ID)
	if _, ok := sources[continuationNativeB]; ok {
		t.Fatalf("foreign-cwd transcript must not be bound; bindings=%v", sources)
	}
}

func TestCollectorContinuationDiscoveryIgnoresPathBoundToAnotherSession(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store := collectorTestStore(t)
	claudeRoot := t.TempDir()
	worktree := t.TempDir()
	nativeA := "native-a"
	session := seedContinuationSession(t, store, claudeRoot, worktree, nativeA)
	seedCollectorUsageBinding(t, store, session, nativeA, domain.UsageBindingActive, now, "")

	other := collectorTestSessionWithActivity(
		t, store, domain.HarnessClaudeCode, continuationNativeB, false, domain.ActivityIdle,
	)
	otherBinding := seedCollectorUsageBinding(
		t, store, other, continuationNativeB, domain.UsageBindingActive, now, "",
	)
	claimedPath := filepath.Join(claudeRoot, "encoded-worktree", continuationNativeB+".jsonl")
	writeUsageFixture(t, claimedPath,
		`{"type":"user","sessionId":"`+continuationNativeB+`","cwd":"`+worktree+`"}`+"\n")
	identity, err := SourceIdentity(ctx, claimedPath)
	mustNoError(t, err)
	// Production always stores the resolved (symlink-followed) path; mirror that.
	resolvedClaimed, err := filepath.EvalSymlinks(filepath.Clean(claimedPath))
	mustNoError(t, err)
	_, err = store.InsertUsageSource(ctx, domain.UsageSourceRecord{
		BindingID:       otherBinding.ID,
		Kind:            domain.UsageSourceClaudeMain,
		NativeSessionID: continuationNativeB,
		ArtifactPath:    resolvedClaimed,
		FileIdentity:    identity,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now,
	})
	mustNoError(t, err)

	collector := NewCollector(store, SourceRoots{ClaudeProjects: claudeRoot}, nil)
	mustNoError(t, collector.ReconcileSources(ctx, 8))

	sources := continuationBindingIDs(t, store, session.ID)
	if _, ok := sources[continuationNativeB]; ok {
		t.Fatalf("transcript bound to another session must not be re-bound; bindings=%v", sources)
	}
}

func TestReadClaudeTranscriptMetaSkipsRecordsWithoutCwd(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "meta.jsonl")
	content := "{\"type\":\"last-prompt\",\"sessionId\":\"session-1\"}\n" +
		"not-json\n" +
		"{\"type\":\"mode\"}\n" +
		"{\"type\":\"user\",\"sessionId\":\"session-1\",\"cwd\":\"/tmp/worktree\"}\n"
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))

	nativeID, cwd, ok := readClaudeTranscriptMeta(ctx, path)
	if !ok {
		t.Fatal("meta not found")
	}
	if nativeID != "session-1" || cwd != "/tmp/worktree" {
		t.Fatalf("nativeID=%q cwd=%q", nativeID, cwd)
	}
}

func TestReadClaudeTranscriptMetaSkipsOversizedRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "meta.jsonl")
	content := "{\"type\":\"last-prompt\",\"sessionId\":\"session-1\"}\n" +
		strings.Repeat("x", maxClaudeContinuationScanBytes+1) + "\n" +
		"{\"type\":\"user\",\"sessionId\":\"session-1\",\"cwd\":\"/tmp/worktree\"}\n"
	mustNoError(t, os.WriteFile(path, []byte(content), 0o600))

	nativeID, cwd, ok := readClaudeTranscriptMeta(ctx, path)
	if !ok {
		t.Fatal("meta not found behind oversized record")
	}
	if nativeID != "session-1" || cwd != "/tmp/worktree" {
		t.Fatalf("nativeID=%q cwd=%q", nativeID, cwd)
	}
}

func TestCollectorContinuationDiscoveryIgnoresSessionIdFilenameMismatch(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	store := collectorTestStore(t)
	claudeRoot := t.TempDir()
	worktree := t.TempDir()
	nativeA := "native-a"
	session := seedContinuationSession(t, store, claudeRoot, worktree, nativeA)
	seedCollectorUsageBinding(t, store, session, nativeA, domain.UsageBindingActive, now, "")

	collector := NewCollector(store, SourceRoots{ClaudeProjects: claudeRoot}, nil)
	// Records carry continuationNativeB but the file name does not; binding it
	// would strand a zero-source binding that fails every reconcile pass.
	writeUsageFixture(t, filepath.Join(claudeRoot, "encoded-worktree", "mismatched.jsonl"),
		`{"type":"last-prompt","sessionId":"`+continuationNativeB+`"}`+"\n"+
			`{"type":"user","sessionId":"`+continuationNativeB+`","cwd":"`+worktree+`"}`+"\n")
	mustNoError(t, collector.ReconcileSources(ctx, 8))
	mustNoError(t, collector.ReconcileSources(ctx, 8))

	sources := continuationBindingIDs(t, store, session.ID)
	if _, ok := sources[continuationNativeB]; ok {
		t.Fatalf("filename/sessionId mismatch must not mint a binding; bindings=%v", sources)
	}
}
