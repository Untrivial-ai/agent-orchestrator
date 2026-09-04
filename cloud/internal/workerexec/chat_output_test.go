package workerexec

import (
	"reflect"
	"testing"
)

func TestChatOutputProjectorPublishesOnlyCodexAgentMessages(t *testing.T) {
	projector := newChatOutputProjector("codex")
	if got := projector.Project(Output{Stream: "stdout", Text: `{"type":"thread.started","thread_id":"native-chat-1"}` + "\n"}); len(got) != 0 {
		t.Fatalf("bookkeeping event = %#v, want no output", got)
	}
	if got := projector.NativeConversationID(); got != "native-chat-1" {
		t.Fatalf("native conversation id = %q, want native-chat-1", got)
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

// A Chat-first Codex session can strand the thread.started bookkeeping event
// in an unterminated final line; Flush must still recover the native identity
// so the later TUI restore resumes the same conversation.
func TestChatOutputProjectorFlushRecoversCodexIdentity(t *testing.T) {
	projector := newChatOutputProjector("codex")
	got := projector.Project(Output{Stream: "stdout", Text: `{"type":"thread.started","thread_id":"native-partial"}`})
	if len(got) != 0 {
		t.Fatalf("unterminated JSONL output = %#v, want no output", got)
	}
	if flushed := projector.Flush(); len(flushed) != 0 {
		t.Fatalf("flushed bookkeeping = %#v, want no output", flushed)
	}
	if got := projector.NativeConversationID(); got != "native-partial" {
		t.Fatalf("native conversation id = %q, want native-partial", got)
	}
}

func TestChatOutputProjectorProjectsClaudeResultAndIdentity(t *testing.T) {
	projector := newChatOutputProjector("claude-code")
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"claude-native-1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done, see summary below"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"working on it\ndone, see summary below","session_id":"claude-native-1"}`,
	}
	var got []Output
	for _, line := range lines {
		got = append(got, projector.Project(Output{Stream: "stdout", Text: line + "\n"})...)
	}
	// The terminal result event is the concatenation of the streamed assistant
	// texts; projecting the intermediates on top of it would double the reply
	// in durable chat history.
	want := []Output{{Stream: "stdout", Text: "working on it\ndone, see summary below"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected output = %#v, want %#v", got, want)
	}
	if identity := projector.NativeConversationID(); identity != "claude-native-1" {
		t.Fatalf("native conversation id = %q, want claude-native-1", identity)
	}
}

// Claude hard-errors on `--print --output-format stream-json` without
// --verbose. The projector must treat that stderr diagnostic as passthrough,
// never as an assistant reply.
func TestChatOutputProjectorSuppressesClaudeErrorResult(t *testing.T) {
	projector := newChatOutputProjector("claude-code")
	got := projector.Project(Output{Stream: "stdout", Text: `{"type":"result","subtype":"error","is_error":true,"result":"rate limited","session_id":"claude-native-2"}` + "\n"})
	if len(got) != 0 {
		t.Fatalf("error result output = %#v, want no assistant reply", got)
	}
	if identity := projector.NativeConversationID(); identity != "claude-native-2" {
		t.Fatalf("native conversation id = %q, want claude-native-2", identity)
	}
}

func TestChatOutputProjectorProjectsCursorResultAndIdentity(t *testing.T) {
	projector := newChatOutputProjector("cursor")
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"cursor-native-1"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello from cursor"}]}}`,
		`{"type":"tool_call","subtype":"started","call_id":"call-1","tool_call":{"readToolCall":{"args":{"path":"f"}}}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"hello from cursor","session_id":"cursor-native-1"}`,
	}
	var got []Output
	for _, line := range lines {
		got = append(got, projector.Project(Output{Stream: "stdout", Text: line + "\n"})...)
	}
	want := []Output{{Stream: "stdout", Text: "hello from cursor"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected output = %#v, want %#v", got, want)
	}
	if identity := projector.NativeConversationID(); identity != "cursor-native-1" {
		t.Fatalf("native conversation id = %q, want cursor-native-1", identity)
	}
}

// A resumed Claude/Cursor conversation must keep its existing identity. The
// first announced identity wins; later bookkeeping events must not flap it.
func TestChatOutputProjectorKeepsFirstNativeIdentity(t *testing.T) {
	for _, harness := range []string{"claude-code", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			projector := newChatOutputProjector(harness)
			projector.Project(Output{Stream: "stdout", Text: `{"type":"system","subtype":"init","session_id":"native-first"}` + "\n"})
			projector.Project(Output{Stream: "stdout", Text: `{"type":"result","subtype":"success","is_error":false,"result":"reply","session_id":"native-first"}` + "\n"})
			if identity := projector.NativeConversationID(); identity != "native-first" {
				t.Fatalf("native conversation id = %q, want native-first", identity)
			}
		})
	}
}

func TestChatOutputProjectorPassesThroughUnsupportedHarness(t *testing.T) {
	projector := newChatOutputProjector("unknown")
	out := Output{Stream: "stdout", Text: "raw output\n"}
	got := projector.Project(out)
	if !reflect.DeepEqual(got, []Output{out}) {
		t.Fatalf("projected output = %#v, want raw passthrough %#v", got, out)
	}
	if flushed := projector.Flush(); len(flushed) != 0 {
		t.Fatalf("flushed output = %#v, want none for unsupported harness", flushed)
	}
}

func TestChatOutputProjectorLeavesStderrUntouched(t *testing.T) {
	for _, harness := range []string{"codex", "claude-code", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			projector := newChatOutputProjector(harness)
			out := Output{Stream: "stderr", Text: "provider warning\n"}
			got := projector.Project(out)
			if !reflect.DeepEqual(got, []Output{out}) {
				t.Fatalf("projected stderr = %#v, want raw passthrough %#v", got, out)
			}
		})
	}
}
