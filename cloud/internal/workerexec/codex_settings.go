package workerexec

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CodexConversationSettings reads the last native turn's actual settings. The
// TUI and app-server both append turn_context records to the same rollout, so
// this also works when control passes between the two interfaces.
func CodexConversationSettings(dataDir, conversationID string) (string, string, error) {
	return CodexConversationSettingsAfter(dataDir, conversationID, time.Time{})
}

// CodexConversationSettingsAfter ignores a rollout context recorded before a
// persisted Chat selection. A TUI turn after the handoff supersedes it.
func CodexConversationSettingsAfter(dataDir, conversationID string, after time.Time) (string, string, error) {
	if _, err := uuid.Parse(conversationID); err != nil {
		return "", "", nil
	}
	home, err := (HarnessBuilder{DataDir: dataDir}).codexHome()
	if err != nil {
		return "", "", err
	}
	var rollout string
	match := "-" + conversationID + ".jsonl"
	err = filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), match) {
			rollout = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil || rollout == "" {
		return "", "", err
	}
	file, err := os.Open(rollout)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	var model, effort string
	var recordedAt time.Time
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var record struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Model             string `json:"model"`
				Effort            string `json:"effort"`
				CollaborationMode struct {
					Settings struct {
						ReasoningEffort string `json:"reasoning_effort"`
					} `json:"settings"`
				} `json:"collaboration_mode"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Type != "turn_context" {
			continue
		}
		if timestamp, parseErr := time.Parse(time.RFC3339Nano, record.Timestamp); parseErr == nil {
			recordedAt = timestamp
		} else {
			recordedAt = time.Time{}
		}
		if value := strings.TrimSpace(record.Payload.Model); value != "" {
			model = value
		}
		effort = strings.TrimSpace(record.Payload.Effort)
		if effort == "" {
			effort = strings.TrimSpace(record.Payload.CollaborationMode.Settings.ReasoningEffort)
		}
	}
	if !after.IsZero() && !recordedAt.After(after) {
		return "", "", scanner.Err()
	}
	return model, effort, scanner.Err()
}
