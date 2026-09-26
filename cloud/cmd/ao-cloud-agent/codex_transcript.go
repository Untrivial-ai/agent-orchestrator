package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxCodexTranscriptTail = 4 << 20

// latestCodexAssistantMessage reads the tail of the provider-native rollout.
// Each Cloud worker has an isolated CODEX_HOME, so falling back to its most
// recently written rollout is safe when a hook omitted the native id.
func latestCodexAssistantMessage(codexHome, nativeID string) string {
	if codexHome == "" {
		return ""
	}
	path := findCodexTranscript(filepath.Join(codexHome, "sessions"), nativeID)
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	var reader io.Reader = file
	if info, err := file.Stat(); err == nil && info.Size() > maxCodexTranscriptTail {
		if _, err := file.Seek(-maxCodexTranscriptTail, io.SeekEnd); err != nil {
			return ""
		}
		// The seek can land midway through a JSONL record. Drop that fragment.
		tail, err := io.ReadAll(io.LimitReader(file, maxCodexTranscriptTail))
		if err != nil {
			return ""
		}
		if newline := bytes.IndexByte(tail, '\n'); newline >= 0 {
			tail = tail[newline+1:]
		}
		reader = bytes.NewReader(tail)
	}
	return latestCodexAssistantLine(reader)
}

func findCodexTranscript(root, nativeID string) string {
	var newest string
	var newestMod int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		if nativeID != "" && !strings.Contains(entry.Name(), nativeID) {
			return nil
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().UnixNano() >= newestMod {
			newest, newestMod = path, info.ModTime().UnixNano()
		}
		return nil
	})
	return newest
}

func latestCodexAssistantLine(reader io.Reader) string {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 32<<10), 1<<20)
	var latest string
	for scanner.Scan() {
		if message := codexAssistantMessage(scanner.Bytes()); message != "" {
			latest = message
		}
	}
	return latest
}

func codexAssistantMessage(line []byte) string {
	var event struct {
		Type    string `json:"type"`
		Payload struct {
			Type             string `json:"type"`
			Role             string `json:"role"`
			Message          string `json:"message"`
			LastAgentMessage string `json:"last_agent_message"`
			Content          []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &event) != nil {
		return ""
	}
	if event.Type == "event_msg" && event.Payload.Type == "task_complete" {
		return strings.TrimSpace(event.Payload.LastAgentMessage)
	}
	if event.Type == "event_msg" && event.Payload.Type == "agent_message" {
		return strings.TrimSpace(event.Payload.Message)
	}
	if event.Type == "response_item" && event.Payload.Type == "message" && event.Payload.Role == "assistant" {
		var parts []string
		for _, content := range event.Payload.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, content.Text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}
