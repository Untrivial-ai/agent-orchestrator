package workerexec

import (
	"reflect"
	"testing"
)

func TestChatOutputProjectorPublishesOnlyCodexAgentMessages(t *testing.T) {
	projector := newChatOutputProjector("codex")
	if got := projector.Project(Output{Stream: "stdout", Text: `{"type":"thread.started"}` + "\n"}); len(got) != 0 {
		t.Fatalf("bookkeeping event = %#v, want no output", got)
	}
	if got := projector.Project(Output{Stream: "stdout", Text: `{"type":"item.completed","item":{"type":"agent_message","text":"hel`}); len(got) != 0 {
		t.Fatalf("partial JSONL output = %#v, want no output", got)
	}
	got := projector.Project(Output{Stream: "stdout", Text: `lo"}}` + "\n"})
	want := []Output{{Stream: "stdout", Text: "hello"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected output = %#v, want %#v", got, want)
	}
}

func TestChatOutputProjectorPreservesUnexpectedCodexOutput(t *testing.T) {
	projector := newChatOutputProjector("codex")
	got := projector.Project(Output{Stream: "stdout", Text: "provider diagnostic\n"})
	want := []Output{{Stream: "stdout", Text: "provider diagnostic"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected output = %#v, want %#v", got, want)
	}
}
