package usage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type jsonlRecord struct {
	Data   []byte
	Offset int64
}

type parseResult struct {
	Events                 []domain.ModelUsageEvent
	Cursor                 domain.SourceCursorState
	newCodexChild          bool
	pendingCodexSpawnCalls int
	err                    error
}

func parseRecordsWithState(
	source domain.UsageSourceContext,
	records []jsonlRecord,
	nextOffset int64,
	now time.Time,
	state *parserStateEnvelope,
) parseResult {
	result := parseResult{Cursor: cursorFromSource(source.Source, nextOffset, now)}
	switch source.Source.Kind {
	case domain.UsageSourceClaudeMain, domain.UsageSourceClaudeSubagent:
		parseClaude(source, records, state.Claude, &result)
	case domain.UsageSourceCodexRollout:
		parseCodex(source, records, state.Codex, &result)
		result.pendingCodexSpawnCalls = len(state.Codex.PendingSpawnCallIDs)
	case domain.UsageSourceCopilotShutdown:
		parseCopilot(source, records, state.Copilot, &result)
	case domain.UsageSourceKimiWire:
		parseKimi(source, records, &result)
	default:
		result.Cursor.AnomalyCount++
		result.Cursor.LastErrorCode = domain.UsageErrorUnsupportedSourceFormat
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		result.err = fmt.Errorf("encode parser state: %w", err)
		return result
	}
	result.Cursor.ParserStateJSON = string(encoded)
	return result
}

func cursorFromSource(source domain.UsageSourceRecord, nextOffset int64, now time.Time) domain.SourceCursorState {
	return domain.SourceCursorState{
		ByteOffset:   nextOffset,
		State:        domain.UsageSourceActive,
		FailureCount: 0,
		AnomalyCount: source.AnomalyCount,
		UpdatedAt:    now,
	}
}

const parserStateVersion = 1

const (
	maxCodexAttributionIDBytes = 256
	maxCodexAttributionIDs     = 4096
	integrityCheckpointBytes   = 4 << 10
)

type codexTokenVector struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type parserStateEnvelope struct {
	Version    int                     `json:"version"`
	SourceKind domain.UsageSourceKind  `json:"source_kind"`
	Integrity  *parserIntegrityStateV1 `json:"integrity,omitempty"`
	Claude     *claudeParserStateV1    `json:"claude,omitempty"`
	Codex      *codexParserStateV1     `json:"codex,omitempty"`
	Copilot    *copilotParserStateV1   `json:"copilot,omitempty"`
}

type parserIntegrityStateV1 struct {
	Checkpoint                *parserCheckpointV1 `json:"checkpoint,omitempty"`
	StableTail                *stableTailStateV1  `json:"stable_tail,omitempty"`
	DiscardingOversizedRecord bool                `json:"discarding_oversized_record,omitempty"`
}

type parserCheckpointV1 struct {
	EndOffset int64  `json:"end_offset"`
	ByteCount int64  `json:"byte_count"`
	SHA256    string `json:"sha256"`
}

type stableTailStateV1 struct {
	Offset            int64  `json:"offset"`
	ByteCount         int64  `json:"byte_count"`
	SHA256            string `json:"sha256"`
	QuietObservations int    `json:"quiet_observations"`
}

type claudeParserStateV1 struct {
	ModelID        string `json:"model_id,omitempty"`
	LegacyProvider string `json:"provider,omitempty"`
}

type codexParserStateV1 struct {
	Baseline            codexTokenVector `json:"baseline"`
	ModelID             string           `json:"model_id,omitempty"`
	LegacyProvider      string           `json:"provider,omitempty"`
	NativeSessionID     string           `json:"native_session_id,omitempty"`
	DirectParentID      string           `json:"direct_parent_id,omitempty"`
	PendingSpawnCallIDs []string         `json:"pending_spawn_call_ids"`
	DiscoveredChildIDs  []string         `json:"discovered_child_ids"`
}

type copilotTokenVector struct {
	InputTokens      int64 `json:"input_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

type copilotParserStateV1 struct {
	Models map[string]copilotTokenVector `json:"models"`
}

func decodeParserState(source domain.UsageSourceRecord) (*parserStateEnvelope, error) {
	raw := strings.TrimSpace(source.ParserStateJSON)
	if raw == "" || raw[0] != '{' {
		return nil, errors.New("state must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return nil, err
	}
	if len(object) == 0 {
		state, err := newParserState(source.Kind)
		if err != nil {
			return nil, err
		}
		if source.Kind == domain.UsageSourceCodexRollout {
			if err := validateCodexDirectParent(source, state.Codex); err != nil {
				return nil, err
			}
		}
		return state, nil
	}
	var state parserStateEnvelope
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, err
	}
	if state.Version != parserStateVersion {
		return nil, fmt.Errorf("unsupported version %d", state.Version)
	}
	if state.SourceKind != source.Kind {
		return nil, fmt.Errorf("source kind %q does not match %q", state.SourceKind, source.Kind)
	}
	if state.Integrity == nil {
		state.Integrity = &parserIntegrityStateV1{}
	}
	if err := validateParserIntegrityState(source, state.Integrity); err != nil {
		return nil, err
	}
	switch source.Kind {
	case domain.UsageSourceClaudeMain, domain.UsageSourceClaudeSubagent:
		if state.Claude == nil || state.Codex != nil || state.Copilot != nil {
			return nil, errors.New("claude state has invalid parser payload")
		}
		state.Claude.LegacyProvider = ""
	case domain.UsageSourceCodexRollout:
		if state.Codex == nil || state.Claude != nil || state.Copilot != nil {
			return nil, errors.New("codex state has invalid parser payload")
		}
		if state.Codex.PendingSpawnCallIDs == nil {
			state.Codex.PendingSpawnCallIDs = []string{}
		}
		if state.Codex.DiscoveredChildIDs == nil {
			state.Codex.DiscoveredChildIDs = []string{}
		}
		state.Codex.LegacyProvider = ""
		if err := normalizeCodexParserState(state.Codex); err != nil {
			return nil, err
		}
		if err := validateCodexDirectParent(source, state.Codex); err != nil {
			return nil, err
		}
	case domain.UsageSourceCopilotShutdown:
		if state.Copilot == nil || state.Claude != nil || state.Codex != nil {
			return nil, errors.New("copilot state has invalid parser payload")
		}
		if state.Copilot.Models == nil {
			state.Copilot.Models = make(map[string]copilotTokenVector)
		}
		for model, total := range state.Copilot.Models {
			if firstNonEmpty(model) == "" || !validCopilotTotal(total) {
				return nil, errors.New("copilot state has invalid model baseline")
			}
		}
	case domain.UsageSourceKimiWire:
		if state.Claude != nil || state.Codex != nil || state.Copilot != nil {
			return nil, errors.New("append-only state has invalid parser payload")
		}
	default:
		return nil, fmt.Errorf("unsupported source kind %q", source.Kind)
	}
	return &state, nil
}
func newParserState(kind domain.UsageSourceKind) (*parserStateEnvelope, error) {
	state := &parserStateEnvelope{
		Version:    parserStateVersion,
		SourceKind: kind,
		Integrity:  &parserIntegrityStateV1{},
	}
	switch kind {
	case domain.UsageSourceClaudeMain, domain.UsageSourceClaudeSubagent:
		state.Claude = &claudeParserStateV1{}
	case domain.UsageSourceCodexRollout:
		state.Codex = &codexParserStateV1{
			PendingSpawnCallIDs: []string{},
			DiscoveredChildIDs:  []string{},
		}
	case domain.UsageSourceCopilotShutdown:
		state.Copilot = &copilotParserStateV1{Models: make(map[string]copilotTokenVector)}
	case domain.UsageSourceKimiWire:
		// Append-only sources derive replay-stable event keys from native record IDs
		// and do not need a provider-specific parser baseline.
	default:
		return nil, fmt.Errorf("unsupported source kind %q", kind)
	}
	return state, nil
}

func validateParserIntegrityState(source domain.UsageSourceRecord, state *parserIntegrityStateV1) error {
	if checkpoint := state.Checkpoint; checkpoint != nil {
		if checkpoint.EndOffset != source.ByteOffset ||
			checkpoint.ByteCount != min(checkpoint.EndOffset, int64(integrityCheckpointBytes)) ||
			!validSHA256Digest(checkpoint.SHA256) {
			return errors.New("invalid integrity checkpoint")
		}
	}
	if tail := state.StableTail; tail != nil {
		if tail.Offset != source.ByteOffset || tail.ByteCount <= 0 ||
			tail.QuietObservations < 0 || tail.QuietObservations > 1 || !validSHA256Digest(tail.SHA256) {
			return errors.New("invalid stable tail state")
		}
	}
	return nil
}

func validSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type claudeTranscriptRecord struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID         string  `json:"id"`
		Model      string  `json:"model"`
		StopReason *string `json:"stop_reason"`
		Usage      *struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func parseClaude(source domain.UsageSourceContext, records []jsonlRecord, state *claudeParserStateV1, result *parseResult) {
	for _, record := range records {
		var native claudeTranscriptRecord
		if err := json.Unmarshal(record.Data, &native); err != nil {
			recordMalformed(result)
			continue
		}
		if native.Type != "assistant" || native.Message.Usage == nil || native.Message.StopReason == nil || strings.TrimSpace(*native.Message.StopReason) == "" {
			continue
		}
		if source.Source.Kind == domain.UsageSourceClaudeMain && native.IsSidechain {
			continue
		}
		usage := native.Message.Usage
		input, ok := sumNonNegative(
			usage.InputTokens,
			usage.CacheCreationInputTokens,
			usage.CacheReadInputTokens,
		)
		if !ok || usage.OutputTokens < 0 {
			recordMalformed(result)
			continue
		}
		tokens := domain.UsageTokenMetrics{
			InputTokens:         input,
			UncachedInputTokens: usage.InputTokens,
			CacheReadTokens:     usage.CacheReadInputTokens,
			CacheWriteTokens:    usage.CacheCreationInputTokens,
			OutputTokens:        usage.OutputTokens,
		}
		if !validTokenMetrics(tokens) {
			recordMalformed(result)
			continue
		}
		model := firstNonEmpty(native.Message.Model, state.ModelID, source.InitialModelID, "unknown")
		state.ModelID = model
		keyID := firstNonEmpty(native.Message.ID, native.UUID, strconv.FormatInt(record.Offset, 10))
		event := domain.ModelUsageEvent{
			ModelID: model,
			Tokens:  tokens,
			SourceEventKey: stableSourceEventKey(
				"claude",
				source.NativeRootID,
				string(source.Source.Kind),
				source.Source.SubagentID,
				source.Source.NativeSessionID,
				keyID,
			),
		}
		result.Events = append(result.Events, event)
	}
}

type codexEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func parseCodex(source domain.UsageSourceContext, records []jsonlRecord, state *codexParserStateV1, result *parseResult) {
	for _, record := range records {
		var envelope codexEnvelope
		if err := json.Unmarshal(record.Data, &envelope); err != nil {
			recordMalformed(result)
			continue
		}
		switch envelope.Type {
		case "turn_context":
			var payload struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(envelope.Payload, &payload) == nil {
				state.ModelID = firstNonEmpty(payload.Model, state.ModelID)
			}
		case "event_msg":
			parseCodexEvent(source, envelope, state, result)
		case "response_item":
			parseCodexResponseItem(envelope.Payload, state, result)
		}
	}
}

func parseCodexResponseItem(raw json.RawMessage, state *codexParserStateV1, result *parseResult) {
	var payload struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	switch payload.Type {
	case "function_call":
		if payload.Name != "spawn_agent" {
			return
		}
		if !validCodexCallID(payload.CallID) ||
			(!containsString(state.PendingSpawnCallIDs, payload.CallID) && len(state.PendingSpawnCallIDs) >= maxCodexAttributionIDs) {
			recordMalformed(result)
			return
		}
		state.PendingSpawnCallIDs = appendUniqueString(state.PendingSpawnCallIDs, payload.CallID)
	case "function_call_output":
		if !containsString(state.PendingSpawnCallIDs, payload.CallID) {
			return
		}
		var output struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal([]byte(payload.Output), &output); err != nil || !validCodexAgentID(output.AgentID) {
			recordMalformed(result)
			return
		}
		alreadyDiscovered := containsString(state.DiscoveredChildIDs, output.AgentID)
		if !alreadyDiscovered && len(state.DiscoveredChildIDs) >= maxCodexAttributionIDs {
			recordMalformed(result)
			return
		}
		state.PendingSpawnCallIDs = removeString(state.PendingSpawnCallIDs, payload.CallID)
		if !alreadyDiscovered {
			state.DiscoveredChildIDs = append(state.DiscoveredChildIDs, output.AgentID)
			result.newCodexChild = true
		}
	}
}

func normalizeCodexParserState(state *codexParserStateV1) error {
	pending, err := normalizeCodexIDs(state.PendingSpawnCallIDs, validCodexCallID)
	if err != nil {
		return fmt.Errorf("invalid pending spawn call ids: %w", err)
	}
	discovered, err := normalizeCodexIDs(state.DiscoveredChildIDs, validCodexAgentID)
	if err != nil {
		return fmt.Errorf("invalid discovered child ids: %w", err)
	}
	state.PendingSpawnCallIDs = pending
	state.DiscoveredChildIDs = discovered
	return nil
}

func validateCodexDirectParent(source domain.UsageSourceRecord, state *codexParserStateV1) error {
	if state.NativeSessionID != "" && state.NativeSessionID != source.NativeSessionID {
		return errors.New("codex parser state has a mismatched native session")
	}
	if source.SubagentID == "" {
		if state.DirectParentID != "" {
			return errors.New("root source has a direct parent")
		}
		return nil
	}
	if source.SubagentID != source.NativeSessionID ||
		!validCodexAgentID(state.DirectParentID) ||
		state.DirectParentID == source.NativeSessionID {
		return errors.New("invalid child direct parent")
	}
	return nil
}

func codexSessionMetaFromRecord(data []byte) (nativeSessionID, directParentID string, ok bool) {
	var envelope struct {
		Type    string `json:"type"`
		Payload struct {
			ID     string          `json:"id"`
			Source json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Type != "session_meta" || envelope.Payload.ID == "" {
		return "", "", false
	}
	directParentID, ok = codexParentThreadIDFromSource(envelope.Payload.Source)
	if !ok {
		return "", "", false
	}
	return envelope.Payload.ID, directParentID, true
}

func codexParentThreadIDFromSource(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", true
	}
	if raw[0] == '"' {
		var source string
		return "", json.Unmarshal(raw, &source) == nil
	}
	if raw[0] != '{' {
		return "", false
	}
	var source struct {
		Subagent struct {
			ThreadSpawn struct {
				ParentThreadID string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return "", false
	}
	return source.Subagent.ThreadSpawn.ParentThreadID, true
}

func normalizeCodexIDs(values []string, valid func(string) bool) ([]string, error) {
	if len(values) > maxCodexAttributionIDs {
		return nil, errors.New("too many ids")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !valid(value) {
			return nil, errors.New("invalid id")
		}
		result = appendUniqueString(result, value)
	}
	return result, nil
}

func validCodexCallID(value string) bool {
	if value == "" || len(value) > maxCodexAttributionIDBytes {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validCodexAgentID(value string) bool {
	if len(value) > maxCodexAttributionIDBytes {
		return false
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func appendUniqueString(values []string, value string) []string {
	if containsString(values, value) {
		return values
	}
	return append(values, value)
}

func removeString(values []string, target string) []string {
	for index, value := range values {
		if value == target {
			return append(values[:index], values[index+1:]...)
		}
	}
	return values
}

func parseCodexEvent(source domain.UsageSourceContext, envelope codexEnvelope, state *codexParserStateV1, result *parseResult) {
	var payload struct {
		Type string `json:"type"`
		Info *struct {
			Total              codexTokenVector `json:"total_token_usage"`
			ModelContextWindow int64            `json:"model_context_window"`
		} `json:"info"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload.Type != "token_count" || payload.Info == nil {
		return
	}
	total := payload.Info.Total
	if !validCodexTotal(total) {
		recordMalformed(result)
		return
	}
	if isCodexContextFill(total, payload.Info.ModelContextWindow) {
		state.Baseline = total
		return
	}
	if total.InputTokens < state.Baseline.InputTokens ||
		total.CachedInputTokens < state.Baseline.CachedInputTokens ||
		total.CacheWriteInputTokens < state.Baseline.CacheWriteInputTokens ||
		total.OutputTokens < state.Baseline.OutputTokens ||
		total.ReasoningOutputTokens < state.Baseline.ReasoningOutputTokens {
		result.Cursor.AnomalyCount++
		result.Cursor.LastErrorCode = domain.UsageErrorNonMonotonicCumulativeUsage
		state.Baseline = total
		return
	}
	input := total.InputTokens - state.Baseline.InputTokens
	cached := total.CachedInputTokens - state.Baseline.CachedInputTokens
	cacheWrite := total.CacheWriteInputTokens - state.Baseline.CacheWriteInputTokens
	output := total.OutputTokens - state.Baseline.OutputTokens
	reasoning := total.ReasoningOutputTokens - state.Baseline.ReasoningOutputTokens
	state.Baseline = total
	if input == 0 && output == 0 && cacheWrite == 0 {
		return
	}
	uncached := input - cached - cacheWrite
	if uncached < 0 {
		uncached = 0
		result.Cursor.AnomalyCount++
		result.Cursor.LastErrorCode = domain.UsageErrorNonMonotonicCumulativeUsage
	}
	model := firstNonEmpty(state.ModelID, source.InitialModelID, "unknown")
	state.ModelID = model
	tokens := domain.UsageTokenMetrics{
		InputTokens:         input,
		UncachedInputTokens: uncached,
		CacheReadTokens:     cached,
		CacheWriteTokens:    cacheWrite,
		OutputTokens:        output,
		ReasoningTokens:     int64Ptr(reasoning),
	}
	if !validTokenMetrics(tokens) {
		result.Cursor.AnomalyCount++
		result.Cursor.LastErrorCode = domain.UsageErrorNonMonotonicCumulativeUsage
		return
	}
	event := domain.ModelUsageEvent{
		ModelID: model,
		Tokens:  tokens,
		SourceEventKey: stableSourceEventKey(
			"codex",
			source.NativeRootID,
			source.Source.NativeSessionID,
			envelope.Timestamp,
			strconv.FormatInt(total.InputTokens, 10),
			strconv.FormatInt(total.CachedInputTokens, 10),
			strconv.FormatInt(total.CacheWriteInputTokens, 10),
			strconv.FormatInt(total.OutputTokens, 10),
			strconv.FormatInt(total.ReasoningOutputTokens, 10),
		),
	}
	result.Events = append(result.Events, event)
}

func recordMalformed(result *parseResult) {
	result.Cursor.AnomalyCount++
	result.Cursor.LastErrorCode = domain.UsageErrorMalformedJSONL
}

func validCodexTotal(total codexTokenVector) bool {
	if total.InputTokens < 0 || total.CachedInputTokens < 0 || total.CacheWriteInputTokens < 0 ||
		total.OutputTokens < 0 || total.ReasoningOutputTokens < 0 || total.TotalTokens < 0 {
		return false
	}
	if total.CachedInputTokens > total.InputTokens ||
		total.CacheWriteInputTokens > total.InputTokens-total.CachedInputTokens {
		return false
	}
	return total.ReasoningOutputTokens <= total.OutputTokens
}

func isCodexContextFill(total codexTokenVector, modelContextWindow int64) bool {
	return modelContextWindow > 0 &&
		total.InputTokens == 0 &&
		total.CachedInputTokens == 0 &&
		total.CacheWriteInputTokens == 0 &&
		total.OutputTokens == 0 &&
		total.ReasoningOutputTokens == 0 &&
		total.TotalTokens == modelContextWindow
}

func validTokenMetrics(tokens domain.UsageTokenMetrics) bool {
	if tokens.InputTokens < 0 || tokens.UncachedInputTokens < 0 ||
		tokens.CacheReadTokens < 0 || tokens.CacheWriteTokens < 0 ||
		tokens.OutputTokens < 0 {
		return false
	}
	if tokens.UncachedInputTokens > tokens.InputTokens ||
		tokens.CacheReadTokens > tokens.InputTokens ||
		tokens.CacheWriteTokens > tokens.InputTokens {
		return false
	}
	if tokens.CacheReadTokens > tokens.InputTokens-tokens.UncachedInputTokens ||
		tokens.CacheWriteTokens > tokens.InputTokens-tokens.UncachedInputTokens-tokens.CacheReadTokens {
		return false
	}
	return tokens.ReasoningTokens == nil ||
		(*tokens.ReasoningTokens >= 0 && *tokens.ReasoningTokens <= tokens.OutputTokens)
}

func sumNonNegative(values ...int64) (int64, bool) {
	const maxInt64 = int64(1<<63 - 1)
	var total int64
	for _, value := range values {
		if value < 0 || value > maxInt64-total {
			return 0, false
		}
		total += value
	}
	return total, true
}

func stableSourceEventKey(prefix string, parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%d:", len(part))
		_, _ = hash.Write([]byte(part))
	}
	return fmt.Sprintf("%s:sha256:%x", prefix, hash.Sum(nil))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && len(value) <= 256 {
			return value
		}
	}
	return ""
}

func int64Ptr(value int64) *int64 {
	return &value
}
