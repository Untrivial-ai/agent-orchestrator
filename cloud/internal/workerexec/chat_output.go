package workerexec

import (
	"bytes"
	"encoding/json"
	"strings"
)

// chatOutputProjector translates a harness's machine-readable headless output
// into the assistant text that belongs in AO's Chat UI. The worker still owns
// the raw provider process; this keeps that provider protocol at the worker
// boundary instead of making the renderer understand Codex JSONL.
//
// Codex emits a complete agent reply as an `item.completed` event. Process
// stdout is not line-buffered, so keep an incomplete JSONL line until its
// newline arrives rather than accidentally publishing fragmented JSON as a
// user-visible assistant response.
type chatOutputProjector struct {
	harness              string
	pending              []byte
	nativeConversationID string
}

func newChatOutputProjector(harness string) *chatOutputProjector {
	return &chatOutputProjector{harness: harness}
}

func (p *chatOutputProjector) Project(output Output) []Output {
	if p.harness != "codex" || output.Stream != "stdout" {
		return []Output{output}
	}
	p.pending = append(p.pending, output.Text...)
	var projected []Output
	for {
		line, rest, found := bytes.Cut(p.pending, []byte{'\n'})
		if !found {
			break
		}
		p.pending = rest
		projected = append(projected, p.projectCodexJSONLine(string(line))...)
	}
	return projected
}

func (p *chatOutputProjector) Flush() []Output {
	if p.harness != "codex" || len(p.pending) == 0 {
		return nil
	}
	line := string(p.pending)
	p.pending = nil
	return p.projectCodexJSONLine(line)
}

// NativeConversationID returns the provider thread announced by a headless
// Codex run. Chat sessions can start before a TUI has ever run, so the
// provider's thread.started event is the only identity available for the
// later TUI restore in that case.
func (p *chatOutputProjector) NativeConversationID() string {
	return p.nativeConversationID
}

func (p *chatOutputProjector) projectCodexJSONLine(line string) []Output {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var event struct {
		Type                string `json:"type"`
		ThreadID            string `json:"thread_id"`
		ThreadIDCamel       string `json:"threadId"`
		ConversationID      string `json:"conversation_id"`
		ConversationIDCamel string `json:"conversationId"`
		Item                struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		// Preserve unexpected non-JSON output for diagnosis instead of silently
		// dropping it. Valid Codex protocol bookkeeping is intentionally hidden.
		return []Output{{Stream: "stdout", Text: line}}
	}
	if event.Type == "thread.started" {
		for _, candidate := range []string{
			event.ThreadID,
			event.ThreadIDCamel,
			event.ConversationID,
			event.ConversationIDCamel,
		} {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				// Keep the first announcement for this run. A resumed thread
				// should retain its existing identity, while a provider that
				// emits more than one bookkeeping event must not flap it.
				if p.nativeConversationID == "" {
					p.nativeConversationID = candidate
				}
				return nil
			}
		}
	}
	if event.Type == "item.completed" && event.Item.Type == "agent_message" && strings.TrimSpace(event.Item.Text) != "" {
		return []Output{{Stream: "stdout", Text: event.Item.Text}}
	}
	return nil
}
