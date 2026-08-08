package sessionmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func readNativeTranscriptTail(path, configDir string) (tail string, truncated, ok bool) {
	return readNativeTranscriptTailWithOpen(context.Background(), path, configDir, os.Open)
}

func TestNormalizeTerminalTailStripsControlsAndBoundsNewestLines(t *testing.T) {
	var in strings.Builder
	for i := 0; i < handoffTerminalMaxLines+10; i++ {
		in.WriteString("\x1b[31mline\x1b[0m\n")
	}
	got := normalizeTerminalTail(in.String())
	if strings.Contains(got, "\x1b") {
		t.Fatalf("terminal tail still contains ANSI: %q", got[:min(40, len(got))])
	}
	if lines := strings.Count(got, "\n") + 1; lines > handoffTerminalMaxLines {
		t.Fatalf("lines = %d, want <= %d", lines, handoffTerminalMaxLines)
	}
	if len(got) > handoffTerminalMaxBytes {
		t.Fatalf("bytes = %d, want <= %d", len(got), handoffTerminalMaxBytes)
	}
	if got := normalizeTerminalTail("first\r\nsecond\r\n"); got != "first\nsecond" {
		t.Fatalf("CRLF-normalized tail = %q, want %q", got, "first\nsecond")
	}
}

func TestReadNativeTranscriptTailKeepsNewestBoundedJSONLLines(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "session.jsonl")
	var transcript strings.Builder
	for i := 0; i < handoffTranscriptMaxLines+10; i++ {
		_, _ = fmt.Fprintf(&transcript, "{\"index\":%d}\n", i)
	}
	if err := os.WriteFile(path, []byte(transcript.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	tail, truncated, ok := readNativeTranscriptTail(path, configDir)
	if !ok || !truncated {
		t.Fatal("transcript tail was not marked truncated")
	}
	if !strings.HasPrefix(tail, transcriptOmittedMarker+"\n") {
		t.Fatalf("transcript tail missing omission marker: %.80q", tail)
	}
	if strings.Contains(tail, `{"index":0}`) || !strings.Contains(tail, fmt.Sprintf(`{"index":%d}`, handoffTranscriptMaxLines+9)) {
		t.Fatalf("transcript tail did not retain the newest records: %.200q", tail)
	}
	if lines := strings.Count(tail, "\n") + 1; lines != handoffTranscriptMaxLines {
		t.Fatalf("lines = %d, want exactly %d", lines, handoffTranscriptMaxLines)
	}
	if !strings.Contains(tail, "\n{\"index\":11}\n") || strings.Contains(tail, "\n{\"index\":10}\n") {
		t.Fatalf("transcript tail retained the wrong 599-record window: %.240q", tail)
	}
	if len(tail) > handoffTranscriptMaxBytes {
		t.Fatalf("bytes = %d, want <= %d", len(tail), handoffTranscriptMaxBytes)
	}
}

func TestReadNativeTranscriptTailPreservesSmallCRLFJSONLAndValidFinalRecord(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "session.jsonl")
	if err := os.WriteFile(path, []byte("{\"index\":1}\r\n{\"index\":2}"), 0o600); err != nil {
		t.Fatal(err)
	}

	tail, truncated, ok := readNativeTranscriptTail(path, configDir)
	if !ok || truncated || tail != "{\"index\":1}\n{\"index\":2}" {
		t.Fatalf("small transcript tail = (%q, %v, %v)", tail, truncated, ok)
	}
}

func TestReadNativeTranscriptTailOmitsIncompleteFinalJSONLRecord(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "session.jsonl")
	if err := os.WriteFile(path, []byte("{\"index\":1}\n{\"index\":"), 0o600); err != nil {
		t.Fatal(err)
	}

	tail, truncated, ok := readNativeTranscriptTail(path, configDir)
	if want := "{\"index\":1}\n" + transcriptIncompleteMarker; !ok || !truncated || tail != want {
		t.Fatalf("incomplete transcript tail = (%q, %v, %v)", tail, truncated, ok)
	}
	if strings.Contains(tail, transcriptOmittedMarker) {
		t.Fatalf("small transcript falsely claimed earlier content was omitted: %q", tail)
	}
}

func TestReadNativeTranscriptTailBoundsOversizedRecordAndSanitizesControls(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "session.jsonl")
	data := append(bytes.Repeat([]byte("x"), handoffTranscriptMaxBytes+1024), 0xff, '\x1b', '[', '3', '1', 'm', 0, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	tail, truncated, ok := readNativeTranscriptTail(path, configDir)
	if !ok || !truncated || tail == "" {
		t.Fatalf("oversized transcript tail = (%q, %v, %v), want non-empty truncated content", tail, truncated, ok)
	}
	if len(tail) > handoffTranscriptMaxBytes {
		t.Fatalf("bytes = %d, want <= %d", len(tail), handoffTranscriptMaxBytes)
	}
	if strings.Contains(tail, "\x1b") || strings.ContainsRune(tail, 0) {
		t.Fatalf("transcript tail retained terminal controls: %.80q", tail)
	}
	if !strings.Contains(tail, "?") {
		t.Fatalf("invalid UTF-8 was not repaired: %.80q", tail)
	}
}

func TestReadNativeTranscriptTailRejectsUnavailableOrOutsidePath(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "provider")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.jsonl")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(configDir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(configDir, "missing.jsonl"), outside, empty} {
		if tail, truncated, ok := readNativeTranscriptTail(path, configDir); tail != "" || truncated || ok {
			t.Fatalf("readNativeTranscriptTail(%q) = (%q, %v, %v), want unavailable", path, tail, truncated, ok)
		}
	}
}

func TestReadNativeTranscriptTailRetainsOversizedValidFinalRecordSuffix(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "session.jsonl")
	record := `{"content":"` + strings.Repeat("x", handoffTranscriptMaxBytes+1024) + `"}`
	if !json.Valid([]byte(record)) {
		t.Fatal("test record is not valid JSON")
	}
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}

	tail, truncated, ok := readNativeTranscriptTail(path, configDir)
	if !ok || !truncated || !strings.Contains(tail, transcriptPartialMarker) {
		t.Fatalf("oversized final record tail = (%q, %v, %v)", tail, truncated, ok)
	}
	if strings.Contains(tail, transcriptIncompleteMarker) || !strings.HasSuffix(tail, `"}`) {
		t.Fatalf("oversized valid final record was mislabeled or lost its suffix: %.200q", tail)
	}
	if len(tail) > handoffTranscriptMaxBytes {
		t.Fatalf("bytes = %d, want <= %d", len(tail), handoffTranscriptMaxBytes)
	}
}

func TestReadNativeTranscriptTailMarksEarlierOmissionAtIncompleteBoundary(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "session.jsonl")
	var transcript strings.Builder
	for i := 0; i < handoffTranscriptMaxLines; i++ {
		_, _ = fmt.Fprintf(&transcript, "{\"index\":%d}\n", i)
	}
	transcript.WriteString(`{"index":`)
	if err := os.WriteFile(path, []byte(transcript.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	tail, truncated, ok := readNativeTranscriptTail(path, configDir)
	if !ok || !truncated || !strings.HasPrefix(tail, transcriptOmittedMarker+"\n") || !strings.HasSuffix(tail, "\n"+transcriptIncompleteMarker) {
		t.Fatalf("boundary tail omitted required markers: %.240q", tail)
	}
	if strings.Contains(tail, `{"index":0}`) || strings.Contains(tail, `{"index":1}`) || !strings.Contains(tail, `{"index":2}`) {
		t.Fatalf("boundary tail retained the wrong complete-record window: %.240q", tail)
	}
	if lines := strings.Count(tail, "\n") + 1; lines != handoffTranscriptMaxLines {
		t.Fatalf("lines = %d, want %d", lines, handoffTranscriptMaxLines)
	}
}

func TestWriteAgentHandoffFileIsPrivateAtomicAndImmutable(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	body, err := validateAndCanonicalizeSourceSemanticHandoff(json.RawMessage(`{
		"schemaVersion": 1,
		"goal": "fix the race",
		"progressSummary": "implementation is ready for verification",
		"completedWork": ["added the generation fence"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	candidatePath, finalPath, err := m.prepareAgentHandoffPaths(context.Background(), "demo-1", "switch-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidatePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	written, err := m.writeAgentHandoffFile(context.Background(), "demo-1", "switch-1", body)
	if err != nil {
		t.Fatal(err)
	}
	if written.Hash == "" || written.Path != finalPath || filepath.Base(written.Path) != "agent-handoff.json" {
		t.Fatalf("written = %#v", written)
	}
	info, err := os.Stat(written.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %o, want 600", written.Path, info.Mode().Perm())
	}
	if dirInfo, statErr := os.Stat(filepath.Dir(written.Path)); statErr != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("handoff directory = (%v, %v), want mode 700", dirInfo, statErr)
	}
	data, err := os.ReadFile(written.Path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Goal            string `json:"goal"`
		ProgressSummary string `json:"progressSummary"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Goal != "fix the race" || decoded.ProgressSummary == "" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if _, err := m.writeAgentHandoffFile(context.Background(), "demo-1", "switch-1", body); err != nil {
		t.Fatalf("idempotent identical write: %v", err)
	}
	changed := json.RawMessage(`{"schemaVersion":1,"goal":"different","progressSummary":"changed"}`)
	if _, err := m.writeAgentHandoffFile(context.Background(), "demo-1", "switch-1", changed); err == nil {
		t.Fatal("different rewrite unexpectedly succeeded")
	}
	if err := removeTemporaryAgentHandoff(context.Background(), candidatePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(candidatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate still exists: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(written.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "agent-handoff.json" {
		t.Fatalf("retained handoff files = %#v", entries)
	}
}

func TestPrepareAgentHandoffPathsHonorsCancelledContext(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "ao-data")
	m := &Manager{dataDir: dataDir}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.prepareAgentHandoffPaths(cancelled, "demo-1", "switch-cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled preparation touched data directory: %v", err)
	}
}

func TestWriteAgentHandoffFileConcurrentDifferentSubmissionsNeverReplaceWinner(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	bodies := []json.RawMessage{
		json.RawMessage(`{"schemaVersion":1,"goal":"first","progressSummary":"first report"}`),
		json.RawMessage(`{"schemaVersion":1,"goal":"second","progressSummary":"second report"}`),
	}
	type result struct {
		written writtenAgentHandoff
		err     error
	}
	results := make(chan result, len(bodies))
	var ready sync.WaitGroup
	ready.Add(len(bodies))
	start := make(chan struct{})
	for _, body := range bodies {
		go func() {
			ready.Done()
			<-start
			written, err := m.writeAgentHandoffFile(context.Background(), "demo-1", "switch-race", body)
			results <- result{written: written, err: err}
		}()
	}
	ready.Wait()
	close(start)

	successes := 0
	var winner writtenAgentHandoff
	for range bodies {
		got := <-results
		if got.err == nil {
			successes++
			winner = got.written
		}
	}
	if successes != 1 {
		t.Fatalf("successful competing publications = %d, want exactly 1", successes)
	}
	final, err := os.ReadFile(winner.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(final, []byte(`"goal":"first"`)) && !bytes.Contains(final, []byte(`"goal":"second"`)) {
		t.Fatalf("final handoff is neither complete candidate: %s", final)
	}
	if !json.Valid(bytes.TrimSpace(final)) {
		t.Fatalf("final handoff is partial JSON: %s", final)
	}
}

func TestWriteFinalizedHandoffFilePersistsExactContinuation(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	sw := domain.AgentSwitch{
		ID: "switch-final", SessionID: "demo-1",
		FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
	}
	if _, _, err := m.prepareAgentHandoffPaths(context.Background(), sw.SessionID, string(sw.ID)); err != nil {
		t.Fatal(err)
	}
	continuation := `<ao-continuation switch-id="switch-final">hidden context</ao-continuation>`
	written, err := m.writeFinalizedHandoffFile(context.Background(), sw, continuation)
	if err != nil {
		t.Fatal(err)
	}
	sw.FinalHandoffPath = written.Path
	sw.FinalHandoffHash = written.Hash
	artifact, ok := m.readVerifiedFinalizedHandoff(context.Background(), sw)
	if !ok {
		t.Fatal("fresh finalized handoff did not verify")
	}
	if artifact.Continuation != continuation || filepath.Base(written.Path) != "handoff.json" {
		t.Fatalf("finalized handoff = %#v path=%q", artifact, written.Path)
	}
}

func TestCleanupAgentHandoffArtifactsRetainsOnlyVerifiedFinal(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	body := json.RawMessage(`{"schemaVersion":1,"goal":"finish switching","progressSummary":"ready for delivery"}`)
	written, err := m.writeAgentHandoffFile(context.Background(), "demo-1", "switch-clean", body)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(written.Path)
	if err := os.WriteFile(filepath.Join(dir, "backup.json"), []byte("unowned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "editor-backup"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "editor-backup", "draft.json"), []byte("unowned"), 0o600); err != nil {
		t.Fatal(err)
	}
	sw := domain.AgentSwitch{
		ID: "switch-clean", SessionID: "demo-1", State: domain.AgentSwitchCompleted,
		AgentHandoffStatus: domain.AgentHandoffReceived,
		AgentHandoffPath:   written.Path,
		AgentHandoffHash:   written.Hash,
	}
	if err := m.cleanupAgentHandoffArtifacts(context.Background(), sw); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "agent-handoff.json" {
		t.Fatalf("retained handoff directory entries = %#v, want only verified final", entries)
	}
}

func TestReadVerifiedAgentHandoffForDeliveryRejectsChangedSemanticFile(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	body := json.RawMessage(`{"schemaVersion":1,"goal":"finish switching","progressSummary":"ready for delivery"}`)
	written, err := m.writeAgentHandoffFile(context.Background(), "demo-1", "switch-verify", body)
	if err != nil {
		t.Fatal(err)
	}
	sw := domain.AgentSwitch{
		ID: "switch-verify", SessionID: "demo-1", AgentHandoffStatus: domain.AgentHandoffReceived,
		AgentHandoffPath: written.Path, AgentHandoffHash: written.Hash,
	}
	if _, ok := m.readVerifiedAgentHandoffForDelivery(context.Background(), sw); !ok {
		t.Fatal("fresh AO-owned semantic handoff did not verify")
	}
	if err := os.WriteFile(written.Path, []byte(`{"schemaVersion":1,"goal":"replaced","progressSummary":"tampered"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.readVerifiedAgentHandoffForDelivery(context.Background(), sw); ok {
		t.Fatal("changed semantic handoff still verified against the durable digest")
	}
	if err := m.cleanupAgentHandoffArtifacts(context.Background(), sw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(written.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid final handoff survived cleanup: %v", err)
	}
}

func TestCleanupAgentHandoffArtifactsRefusesSymlinkedSwitchDirectory(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	dir, err := m.handoffDirectory("demo-1", "switch-link")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideCandidate := filepath.Join(outside, "agent-handoff-candidate.json")
	if err := os.WriteFile(outsideCandidate, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sw := domain.AgentSwitch{ID: "switch-link", SessionID: "demo-1", State: domain.AgentSwitchFailed}
	if err := m.cleanupAgentHandoffArtifacts(context.Background(), sw); err == nil {
		t.Fatal("cleanup followed a symlinked switch directory")
	}
	if got, err := os.ReadFile(outsideCandidate); err != nil || string(got) != "must survive" {
		t.Fatalf("cleanup modified symlink target: body=%q err=%v", got, err)
	}
}

func TestCleanupAgentHandoffArtifactsRefusesReplacedDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	m := &Manager{dataDir: dataDir}
	if _, _, err := m.prepareAgentHandoffPaths(context.Background(), "demo-1", "switch-data-link"); err != nil {
		t.Fatal(err)
	}
	realDataDir := dataDir + ".original"
	if err := os.Rename(dataDir, realDataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(realDataDir) })
	outside := t.TempDir()
	redirectedDir := filepath.Join(outside, "handoffs", "demo-1", "switch-data-link")
	if err := os.MkdirAll(redirectedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideCandidate := filepath.Join(redirectedDir, "agent-handoff-candidate.json")
	if err := os.WriteFile(outsideCandidate, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dataDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sw := domain.AgentSwitch{ID: "switch-data-link", SessionID: "demo-1", State: domain.AgentSwitchFailed}
	if err := m.cleanupAgentHandoffArtifacts(context.Background(), sw); err == nil {
		t.Fatal("cleanup followed a replaced data-directory symlink")
	}
	if got, err := os.ReadFile(outsideCandidate); err != nil || string(got) != "must survive" {
		t.Fatalf("cleanup modified redirected data: body=%q err=%v", got, err)
	}
}
