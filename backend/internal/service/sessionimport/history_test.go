package sessionimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestHistorySkipsHugeToolOutputButPreservesHugeMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	large := strings.Repeat("x", 9*1024*1024)
	message, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": large}}}})
	// Canonical provider field order makes the tool kind available in the prefix.
	tool := []byte(`{"type":"response_item","payload":{"type":"function_call_output","output":"` + large + `"}}`)
	data := append(append(tool, '\n'), message...)
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessages(context.Background(), domain.HarnessCodex, path)
	if err != nil || len(got) != 1 {
		t.Fatalf("messages=%d err=%v", len(got), err)
	}
	if got[0].Text != large {
		t.Fatal("large user-visible message was truncated")
	}
}
func TestHistoryPrefixKeepsUnusualOrderAndContent(t *testing.T) {
	for _, raw := range []string{
		`{"payload":{"content":[],"role":"assistant","type":"message"},"type":"response_item"}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"function_call_output"}}`,
		`{"payload":{"content":"unfinished`,
	} {
		if irrelevantHistoryPrefix([]byte(raw), domain.HarnessCodex) {
			t.Fatalf("discarded possible message: %s", raw)
		}
	}
}
