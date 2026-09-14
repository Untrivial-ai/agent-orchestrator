package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestImportedHistoryIsReadableWithoutProviderAndPagesWithoutDuplicates(t *testing.T) {
	for _, provider := range []domain.AgentHarness{domain.HarnessCodex, domain.HarnessClaudeCode} {
		t.Run(string(provider), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archived.jsonl")
			data := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}
{"type":"event_msg","payload":{"type":"agent_message","message":"answer"}}
{"type":`
			if provider == domain.HarnessClaudeCode {
				data = `{"type":"user","uuid":"u","message":{"role":"user","content":"question"}}
{"type":"assistant","uuid":"a","message":{"role":"assistant","content":[{"type":"text","text":"answer"}]}}
{"type":"assistant","uuid":"a","message":{"role":"assistant","content":[{"type":"text","text":"answer"}]}}
{"type":`
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			rec := domain.SessionRecord{ID: "p-1", Harness: provider, Metadata: domain.SessionMetadata{NativeTranscriptPath: path}}
			page, err := importedHistorySnapshot(context.Background(), rec, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !page.ImportedHistory || !page.HasMoreBefore || len(page.Messages) != 1 || page.Messages[0].Text != "answer" {
				t.Fatalf("bad last page: %+v", page)
			}
			older, err := importedHistorySnapshot(context.Background(), rec, page.OldestSequence, 1)
			if err != nil {
				t.Fatal(err)
			}
			if older.HasMoreBefore || len(older.Messages) != 1 || older.Messages[0].Text != "question" {
				t.Fatalf("bad first page: %+v", older)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := importedHistorySnapshot(ctx, rec, 0, 1); err == nil {
				t.Fatal("cancelled read succeeded")
			}
		})
	}
}
