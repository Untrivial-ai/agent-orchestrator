package sessionimport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ReadMessages reads the provider-owned transcript without acquiring a writer
// or starting its CLI. Import itself only registers the path; this read happens
// when the user opens history. A partial final line from a live writer is ignored.
func ReadMessages(ctx context.Context, provider domain.AgentHarness, path string) ([]domain.ConversationMessage, error) {
	var messages []domain.ConversationMessage
	seen := map[string]int{}
	err := scanHistoryLines(ctx, path, provider, func(line []byte) bool {
		if ctx.Err() != nil {
			return false
		}
		var row struct {
			Type      string         `json:"type"`
			UUID      string         `json:"uuid"`
			Timestamp time.Time      `json:"timestamp"`
			Payload   historyMessage `json:"payload"`
			Message   historyMessage `json:"message"`
		}
		if json.Unmarshal(line, &row) != nil {
			return true
		}
		var msg historyMessage
		if provider == domain.HarnessCodex {
			msg = row.Payload
			if row.Type != "response_item" || msg.Type != "message" {
				return true
			}
		} else {
			if row.Type != "user" && row.Type != "assistant" {
				return true
			}
			msg = row.Message
		}
		if msg.Role != domain.MessageRoleUser && msg.Role != domain.MessageRoleAssistant {
			return true
		}
		text := string(msg.Content)
		if strings.TrimSpace(text) == "" {
			return true
		}
		id := row.UUID
		if id == "" {
			id = fmt.Sprintf("line-%d", len(messages)+1)
		}
		origin := domain.MessageOriginProvider
		if msg.Role == domain.MessageRoleUser {
			origin = domain.MessageOriginHuman
		}
		message := domain.ConversationMessage{ID: id, Role: msg.Role, Origin: origin, Text: text, Revision: 1, CreatedAt: row.Timestamp, UpdatedAt: row.Timestamp}
		if index, ok := seen[id]; ok {
			messages[index] = message
		} else {
			seen[id] = len(messages)
			messages = append(messages, message)
		}
		return true
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return messages, err
}

// Inspect only structural fields that have already been decoded. A truncated
// prefix or unusual key order remains eligible for the full parser.
func irrelevantHistoryPrefix(raw []byte, provider domain.AgentHarness) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return false
		}
		if key == "type" {
			var kind string
			if d.Decode(&kind) != nil {
				return false
			}
			if provider == domain.HarnessCodex {
				if kind != "response_item" {
					return true
				}
			} else {
				return kind != "user" && kind != "assistant"
			}
		} else if key == "payload" && provider == domain.HarnessCodex {
			token, err = d.Token()
			if err != nil || token != json.Delim('{') {
				return false
			}
			for d.More() {
				key, err = d.Token()
				if err != nil {
					return false
				}
				if key == "type" {
					var kind string
					if d.Decode(&kind) != nil {
						return false
					}
					return kind != "message"
				}
				var skip json.RawMessage
				if d.Decode(&skip) != nil {
					return false
				}
			}
			return false
		} else {
			var skip json.RawMessage
			if d.Decode(&skip) != nil {
				return false
			}
		}
	}
	return false
}

// Discard provider event/tool-output lines as they stream past. In particular,
// a multi-megabyte tool result neither allocates a huge JSON value nor prevents
// later messages from loading. Relevant messages have no arbitrary line cap.
func scanHistoryLines(ctx context.Context, path string, provider domain.AgentHarness, visit func([]byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReaderSize(f, 64*1024)
	var line []byte
	first, skip := true, false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		part, err := reader.ReadSlice('\n')
		if first {
			skip = irrelevantHistoryPrefix(part, provider)
			first = false
		}
		if !skip {
			line = append(line, part...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !skip && len(line) > 0 && !visit(line) {
			return nil
		}
		line = line[:0]
		first, skip = true, false
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Decode the envelope once instead of copying and reparsing nested raw JSON
// containing tool output. Only visible text blocks allocate message text.
type historyMessage struct {
	Type    string             `json:"type"`
	Role    domain.MessageRole `json:"role"`
	Content historyText        `json:"content"`
}
type historyText string

func (t *historyText) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		*t = historyText(value)
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return err
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" || block.Type == "input_text" || block.Type == "output_text" {
			parts = append(parts, block.Text)
		}
	}
	*t = historyText(strings.Join(parts, "\n"))
	return nil
}
