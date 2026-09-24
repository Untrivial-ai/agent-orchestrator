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
// Codex emits a complete agent reply as an `item.completed` event. Claude Code
// and Cursor aggregate streamed assistant text into one final `result` event
// whose text is the concatenation of that turn's assistant messages; projecting
// the streamed `assistant` events on top of the terminal result would double
// the reply in durable chat history, so the terminal result is the single
// projected reply for them. Process stdout is not line-buffered, so keep an
// incomplete JSONL line until its newline arrives rather than accidentally
// publishing fragmented JSON as a user-visible assistant response.
type chatOutputProjector struct {
	harness              string
	pending              []byte
	nativeConversationID string
}

func newChatOutputProjector(harness string) *chatOutputProjector {
	return &chatOutputProjector{harness: harness}
}

// projectsJSONL reports whether the projector understands the harness's
// headless stdout protocol. Unsupported harnesses keep the raw passthrough.
func (p *chatOutputProjector) projectsJSONL() bool {
	switch p.harness {
	case "codex", "claude-code", "cursor":
		return true
	default:
		return false
	}
}

func (p *chatOutputProjector) Project(output Output) []Output {
	if !p.projectsJSONL() || output.Stream != "stdout" {
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
		projected = append(projected, p.projectLine(string(line))...)
	}
	return projected
}

// Flush drains a final partial JSONL record on clean process exit so the
// thread/session identity cannot stay stranded in the projector buffer.
func (p *chatOutputProjector) Flush() []Output {
	if !p.projectsJSONL() || len(p.pending) == 0 {
		return nil
	}
	line := string(p.pending)
	p.pending = nil
	return p.projectLine(line)
}

// NativeConversationID returns the provider thread announced by a headless
// run. Chat sessions can start before a TUI has ever run, so the provider's
// first identity event is the only resume hint available for the later TUI
// restore in that case.
func (p *chatOutputProjector) NativeConversationID() string {
	return p.nativeConversationID
}

func (p *chatOutputProjector) projectLine(line string) []Output {
	switch p.harness {
	case "codex":
		return p.projectCodexJSONLine(line)
	case "claude-code":
		return p.projectClaudeJSONLine(line)
	case "cursor":
		return p.projectCursorJSONLine(line)
	default:
		return nil
	}
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
		p.captureIdentity(
			event.ThreadID,
			event.ThreadIDCamel,
			event.ConversationID,
			event.ConversationIDCamel,
		)
		return nil
	}
	if event.Type == "item.completed" && event.Item.Type == "agent_message" && strings.TrimSpace(event.Item.Text) != "" {
		return []Output{{Stream: "stdout", Text: event.Item.Text}}
	}
	return nil
}

func (p *chatOutputProjector) projectClaudeJSONLine(line string) []Output {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var event struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		IsError   bool   `json:"is_error"`
		Result    string `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		// Preserve unexpected non-JSON output for diagnosis instead of silently
		// dropping it. Valid stream-json bookkeeping is intentionally hidden.
		return []Output{{Stream: "stdout", Text: line}}
	}
	switch event.Type {
	case "system", "result":
		// The system/init event opens the stream and the terminal result event
		// closes it; both carry the session identity. Keep the first
		// announcement for this run so a provider that emits more than one
		// bookkeeping event cannot flap the shared conversation identity.
		p.captureIdentity(event.SessionID)
		if event.Type != "result" {
			return nil
		}
		if event.IsError {
			return nil
		}
		if text := strings.TrimSpace(event.Result); text != "" {
			// The result text is the concatenation of the turn's assistant
			// messages. Project it as the single reply; streamed assistant
			// events are bookkeeping for the same text and stay hidden.
			return []Output{{Stream: "stdout", Text: event.Result}}
		}
	}
	return nil
}

func (p *chatOutputProjector) projectCursorJSONLine(line string) []Output {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var event struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		IsError   bool   `json:"is_error"`
		Result    string `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return []Output{{Stream: "stdout", Text: line}}
	}
	switch event.Type {
	case "system", "result":
		p.captureIdentity(event.SessionID)
		if event.Type != "result" {
			return nil
		}
		if event.IsError {
			return nil
		}
		if text := strings.TrimSpace(event.Result); text != "" {
			return []Output{{Stream: "stdout", Text: event.Result}}
		}
	}
	return nil
}

// captureIdentity records the first announced provider conversation identity
// for this run. A resumed thread must retain its existing identity, and a
// provider that repeats the identity on several events must not flap it.
func (p *chatOutputProjector) captureIdentity(candidates ...string) {
	if p.nativeConversationID != "" {
		return
	}
	for _, candidate := range candidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			p.nativeConversationID = candidate
			return
		}
	}
}
