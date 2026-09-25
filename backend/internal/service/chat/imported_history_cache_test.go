package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const codexRollout = `{"timestamp":"2026-08-21T09:00:00.000Z","type":"session_meta","payload":{"session_id":"019fbaf8-67a4-79b2-aa80-01283063aab8","id":"019fbaf8-67a4-79b2-aa80-01283063aab8","cwd":"/repo"}}
{"timestamp":"2026-08-21T09:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Add a health endpoint"}]}}
{"timestamp":"2026-08-21T09:00:09.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}
`

func writeRollout(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(codexRollout), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resetTranscriptCache() {
	importedTranscripts.mu.Lock()
	defer importedTranscripts.mu.Unlock()
	importedTranscripts.entries = map[string]cachedTranscript{}
	importedTranscripts.order = nil
}

// Opening a session and paging back through it hits the same transcript over
// and over. Reparsing a large file each time makes the paging decorative.
func TestImportedTranscriptIsParsedOnce(t *testing.T) {
	resetTranscriptCache()
	path := filepath.Join(t.TempDir(), "sessions", "2026", "rollout-a.jsonl")
	writeRollout(t, path)

	first, err := readImportedMessages(context.Background(), domain.HarnessCodex, path)
	if err != nil || len(first) == 0 {
		t.Fatalf("read: %d messages, err=%v", len(first), err)
	}
	second, err := readImportedMessages(context.Background(), domain.HarnessCodex, path)
	if err != nil {
		t.Fatal(err)
	}
	if &first[0] != &second[0] {
		t.Error("a second read reparsed the transcript instead of reusing it")
	}
}

// A transcript the provider appended to must not be served from the parse taken
// before the change.
func TestImportedTranscriptRefreshesWhenItChanges(t *testing.T) {
	resetTranscriptCache()
	path := filepath.Join(t.TempDir(), "sessions", "rollout-b.jsonl")
	writeRollout(t, path)

	before, err := readImportedMessages(context.Background(), domain.HarnessCodex, path)
	if err != nil {
		t.Fatal(err)
	}
	extra := codexRollout + `{"timestamp":"2026-08-21T09:01:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"And readiness"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(extra), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := readImportedMessages(context.Background(), domain.HarnessCodex, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) <= len(before) {
		t.Errorf("a continued conversation should be reparsed: before=%d after=%d", len(before), len(after))
	}
}

// Codex moves a conversation into archived_sessions when it is archived. The
// recorded path then no longer exists, and the session rendered as an error.
func TestImportedTranscriptFollowsAnArchivedMove(t *testing.T) {
	resetTranscriptCache()
	home := t.TempDir()
	recorded := filepath.Join(home, "sessions", "2026", "rollout-c.jsonl")
	archived := filepath.Join(home, "archived_sessions", "2026", "rollout-c.jsonl")
	writeRollout(t, archived)

	got, err := readImportedMessages(context.Background(), domain.HarnessCodex, recorded)
	if err != nil {
		t.Fatalf("an archived transcript must still be readable: %v", err)
	}
	if len(got) == 0 {
		t.Error("no messages read from the archived transcript")
	}
}

func TestImportedTranscriptStillReportsAMissingFile(t *testing.T) {
	resetTranscriptCache()
	if _, err := readImportedMessages(context.Background(), domain.HarnessCodex, filepath.Join(t.TempDir(), "sessions", "gone.jsonl")); err == nil {
		t.Error("a transcript that is genuinely gone must report an error")
	}
}
