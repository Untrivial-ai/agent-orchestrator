package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLatestCodexAssistantMessageReadsTaskComplete(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "09", "01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-09-01-native-1.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"terminal reply"}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := latestCodexAssistantMessage(home, "native-1"); got != "terminal reply" {
		t.Fatalf("latest reply = %q", got)
	}
}

func TestCodexAssistantMessageReadsResponseItem(t *testing.T) {
	line := []byte(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`)
	if got := codexAssistantMessage(line); got != "answer" {
		t.Fatalf("reply = %q", got)
	}
}
