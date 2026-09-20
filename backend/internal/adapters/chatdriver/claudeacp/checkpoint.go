package claudeacp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type checkpointDriver struct {
	ports.ChatDriver
	plugin claudePlugin
}

func (d *checkpointDriver) VerifyNativeCheckpoint(ctx context.Context, request ports.NativeCheckpointRequest) (ports.NativeCheckpointBoundary, error) {
	config, ok := d.plugin.(ports.AgentNativeSessionConfigProvider)
	if !ok {
		return ports.NativeCheckpointBoundary{}, checkpointUnsettled("native config resolver unavailable")
	}
	locator, ok := d.plugin.(ports.AgentTranscriptLocator)
	if !ok {
		return ports.NativeCheckpointBoundary{}, checkpointUnsettled("native transcript locator unavailable")
	}
	dir, err := config.NativeSessionConfigDir(ctx, request.Env)
	if err != nil {
		return ports.NativeCheckpointBoundary{}, err
	}
	path, found, err := locator.LocateTranscript(ctx, ports.NativeSessionRef{NativeSessionID: request.ProviderConversationID, ConfigDir: dir})
	if err != nil {
		return ports.NativeCheckpointBoundary{}, err
	}
	if !found {
		return ports.NativeCheckpointBoundary{}, checkpointUnsettled("native transcript not flushed")
	}
	file, err := os.Open(path)
	if err != nil {
		return ports.NativeCheckpointBoundary{}, err
	}
	defer func() { _ = file.Close() }()
	return verifyCheckpointTranscript(ctx, file, request)
}

func checkpointUnsettled(reason string) error {
	return fmt.Errorf("claude native checkpoint: %s: %w", reason,
		&ports.ChatHistoryUnsettledError{Dimensions: []ports.ChatHistoryMismatchDimension{ports.ChatHistoryMismatchUnsettledBoundary}})
}

type checkpointRecord struct {
	UUID                  string `json:"uuid"`
	ParentUUID            string `json:"parentUuid"`
	SessionID             string `json:"sessionId"`
	PromptID              string `json:"promptId"`
	Type                  string `json:"type"`
	Subtype               string `json:"subtype"`
	Sidechain             bool   `json:"isSidechain"`
	IsMeta                bool   `json:"isMeta"`
	TurnCompanion         bool   `json:"turnCompanion"`
	PreventedContinuation bool   `json:"preventedContinuation"`
	Attachment            struct {
		Type      string          `json:"type"`
		HookEvent string          `json:"hookEvent"`
		Content   json.RawMessage `json:"content"`
	} `json:"attachment"`
	Message struct {
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
	} `json:"message"`
}

type checkpointTurn struct {
	promptID, userID, user, assistant string
	complete                          bool
}

// Native logs are append-only graphs, not a list of alternating text messages.
// Follow the last main-chain leaf's parent UUIDs; never use a matching sidechain,
// an earlier repeated answer, or file stability to discharge a hook witness.
func verifyCheckpointTranscript(ctx context.Context, input io.Reader, request ports.NativeCheckpointRequest) (ports.NativeCheckpointBoundary, error) {
	var evidence domain.NativeCheckpointEvidence
	if json.Unmarshal([]byte(request.Evidence), &evidence) != nil || evidence.Invalid ||
		evidence.NativeID != request.ProviderConversationID || len(evidence.Events) == 0 {
		return ports.NativeCheckpointBoundary{}, checkpointUnsettled("missing or invalid owned observations")
	}
	records := make(map[string]checkpointRecord)
	wantedSubmissions := make(map[string]bool)
	for _, observation := range evidence.Events {
		if observation.Submission && observation.SubmissionID != "" {
			wantedSubmissions[domain.NativeSubmissionContext(observation.SubmissionID)] = true
		}
	}
	leaf := ""
	const maxTranscriptBytes = 64 << 20
	limited := &io.LimitedReader{R: input, N: maxTranscriptBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return ports.NativeCheckpointBoundary{}, err
		}
		var record checkpointRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("incomplete native record")
		}
		if record.Type == "system" && record.Subtype == "compact_boundary" {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("compacted native ancestry requires reconciliation")
		}
		if record.UUID == "" || record.Sidechain {
			continue
		}
		if record.SessionID != request.ProviderConversationID {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("native record identity mismatch")
		}
		if _, exists := records[record.UUID]; exists {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("ambiguous native UUID")
		}
		records[record.UUID] = record
		leaf = record.UUID
	}
	if scanner.Err() != nil || limited.N == 0 {
		return ports.NativeCheckpointBoundary{}, checkpointUnsettled("native transcript exceeds readable bounds")
	}
	var chain []checkpointRecord
	seen := make(map[string]bool)
	for leaf != "" {
		record, found := records[leaf]
		if !found || seen[leaf] {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("incomplete native ancestry")
		}
		seen[leaf] = true
		chain = append(chain, record)
		leaf = record.ParentUUID
	}
	slices.Reverse(chain)
	var turns []checkpointTurn
	submissions := make(map[string]int)
	for _, record := range chain {
		if record.Type == "user" {
			// Claude may append auxiliary context as a meta turn-companion user
			// record. It deliberately shares the human turn's prompt ID and remains
			// on the main ancestry, but it is not a second conversational boundary.
			if record.IsMeta && record.TurnCompanion {
				continue
			}
			if checkpointToolResults(record.Message.Content) {
				continue
			}
			if record.PromptID == "" {
				return ports.NativeCheckpointBoundary{}, checkpointUnsettled("native user has no prompt identity")
			}
			text, err := checkpointContent(record.Message.Content)
			if err != nil {
				return ports.NativeCheckpointBoundary{}, err
			}
			turns = append(turns, checkpointTurn{promptID: record.PromptID, userID: record.UUID, user: text})
			continue
		}
		if len(turns) == 0 {
			continue
		}
		turn := &turns[len(turns)-1]
		switch record.Type {
		case "attachment":
			if record.Attachment.Type == "hook_additional_context" && record.Attachment.HookEvent == "UserPromptSubmit" {
				var contents []string
				if json.Unmarshal(record.Attachment.Content, &contents) != nil {
					return ports.NativeCheckpointBoundary{}, checkpointUnsettled("invalid native submission attachment")
				}
				for _, content := range contents {
					if !wantedSubmissions[content] {
						continue
					}
					if _, duplicate := submissions[content]; duplicate {
						return ports.NativeCheckpointBoundary{}, checkpointUnsettled("ambiguous native submission attachment")
					}
					submissions[content] = len(turns) - 1
				}
			}
		case "assistant":
			text, err := checkpointContent(record.Message.Content)
			if err != nil {
				return ports.NativeCheckpointBoundary{}, err
			}
			turn.assistant = text
			turn.complete = record.Message.StopReason == "end_turn"
		case "system":
			if record.Subtype == "stop_hook_summary" && record.PreventedContinuation {
				turn.complete = false
			}
		}
	}
	if len(turns) == 0 {
		return ports.NativeCheckpointBoundary{}, checkpointUnsettled("no native user boundary")
	}
	byPrompt := make(map[string]int)
	for i, turn := range turns {
		if _, duplicate := byPrompt[turn.promptID]; duplicate {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("ambiguous native prompt ID")
		}
		byPrompt[turn.promptID] = i
	}
	for _, observation := range evidence.Events {
		if observation.Generation == "" {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("unowned native observation")
		}
		index, found := byPrompt[observation.PromptID]
		if observation.Submission {
			index, found = submissions[domain.NativeSubmissionContext(observation.SubmissionID)]
			if observation.SubmissionID == "" || !found ||
				(!observation.Coordination && !domain.NativeCheckpointTextMatches(observation.Text, turns[index].user)) {
				return ports.NativeCheckpointBoundary{}, checkpointUnsettled("native submission attachment not flushed")
			}
		} else if !found {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("observed native prompt not flushed")
		} else if observation.Text != "" && !domain.NativeCheckpointTextMatches(observation.Text, turns[index].assistant) {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("Stop answer does not match its native prompt")
		}
		if !turns[index].complete {
			return ports.NativeCheckpointBoundary{}, checkpointUnsettled("observed native turn not completed")
		}
	}
	latest := turns[len(turns)-1]
	if !latest.complete {
		return ports.NativeCheckpointBoundary{}, checkpointUnsettled("native tail still pending")
	}
	return ports.NativeCheckpointBoundary{UserMessageID: latest.userID, UserText: latest.user, AssistantText: latest.assistant}, nil
}

func checkpointToolResults(raw json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if block.Type != "tool_result" {
			return false
		}
	}
	return true
}

func checkpointContent(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", checkpointUnsettled("unsupported native content")
	}
	var texts []string
	for _, block := range blocks {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}
