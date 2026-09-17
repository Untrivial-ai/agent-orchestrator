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
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
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
	ModelID  string `json:"model_id,omitempty"`
	Provider string `json:"billing_provider,omitempty"`
	// LegacyProvider is the retired "provider" key. A build before #2928 filled
	// it with the harness name — "claude-code", "openai" — not a billing route,
	// so decoding must accept it (DisallowUnknownFields) and must never read
	// it. Reusing the key for the billing route would stamp the harness name
	// onto every event newly ingested from such a source, marked observed and
	// therefore beyond every repair path.
	LegacyProvider string `json:"provider,omitempty"`
}

type codexParserStateV1 struct {
	Baseline       codexTokenVector `json:"baseline"`
	ModelID        string           `json:"model_id,omitempty"`
	Provider       string           `json:"billing_provider,omitempty"`
	LegacyProvider string           `json:"provider,omitempty"` // see claudeParserStateV1

	NativeSessionID     string   `json:"native_session_id,omitempty"`
	DirectParentID      string   `json:"direct_parent_id,omitempty"`
	PendingSpawnCallIDs []string `json:"pending_spawn_call_ids"`
	DiscoveredChildIDs  []string `json:"discovered_child_ids"`
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
		if state.Claude == nil || state.Codex != nil {
			return nil, errors.New("claude state has invalid parser payload")
		}
		state.Claude.LegacyProvider = ""
	case domain.UsageSourceCodexRollout:
		if state.Codex == nil || state.Claude != nil {
			return nil, errors.New("codex state has invalid parser payload")
		}
		state.Codex.LegacyProvider = ""
		if state.Codex.PendingSpawnCallIDs == nil {
			state.Codex.PendingSpawnCallIDs = []string{}
		}
		if state.Codex.DiscoveredChildIDs == nil {
			state.Codex.DiscoveredChildIDs = []string{}
		}
		if err := normalizeCodexParserState(state.Codex); err != nil {
			return nil, err
		}
		if err := validateCodexDirectParent(source, state.Codex); err != nil {
			return nil, err
		}
	case domain.UsageSourceKimiWire:
		if state.Claude != nil || state.Codex != nil {
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
	case domain.UsageSourceKimiWire:
		// Kimi records carry stable native IDs, so no provider-specific
		// cumulative baseline is required.
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
	Provider    string `json:"provider"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID         string  `json:"id"`
		Model      string  `json:"model"`
		Provider   string  `json:"provider"`
		StopReason *string `json:"stop_reason"`
		// Decoded twice on purpose: the typed view drives the neutral counters,
		// and the raw bytes are the bounded provider object stored verbatim so
		// fields Anthropic adds later survive without a schema change here.
		Usage json.RawMessage `json:"usage"`
	} `json:"message"`
}

type claudeNativeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreation            *struct {
		Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func parseClaude(source domain.UsageSourceContext, records []jsonlRecord, state *claudeParserStateV1, result *parseResult) {
	eventsByKey := make(map[string]domain.ModelUsageEvent)
	for _, record := range records {
		var native claudeTranscriptRecord
		if err := json.Unmarshal(record.Data, &native); err != nil {
			recordMalformed(result)
			continue
		}
		// The transcript feeds the same write-once column as the hook, so it
		// earns the same whitelist. An unrecognised string is dropped rather
		// than stored: unattributed is repairable, a wrong attribution is not.
		nativeProvider := pricing.TrustedClaudeBillingProvider(
			firstNonEmpty(native.Message.Provider, native.Provider))
		if nativeProvider != "" {
			state.Provider = nativeProvider
		}
		if native.Type != "assistant" || !jsonValueReported(native.Message.Usage) ||
			native.Message.StopReason == nil || strings.TrimSpace(*native.Message.StopReason) == "" {
			continue
		}
		if source.Source.Kind == domain.UsageSourceClaudeMain && native.IsSidechain {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(native.Message.Model), "<synthetic>") {
			continue
		}
		var usage claudeNativeUsage
		if err := json.Unmarshal(native.Message.Usage, &usage); err != nil {
			recordMalformed(result)
			continue
		}
		var creation5m, creation1h *int64
		if usage.CacheCreation != nil {
			creation5m = int64Ptr(usage.CacheCreation.Ephemeral5mInputTokens)
			creation1h = int64Ptr(usage.CacheCreation.Ephemeral1hInputTokens)
		}
		tokens, ok := normalizeAnthropicUsage(
			usage.InputTokens,
			usage.CacheCreationInputTokens,
			usage.CacheReadInputTokens,
			usage.OutputTokens,
			creation5m,
			creation1h,
		)
		if !ok {
			recordMalformed(result)
			continue
		}
		model := firstNonEmpty(native.Message.Model, state.ModelID, source.InitialModelID, "unknown")
		state.ModelID = model
		billingProvider := pricing.TrustedClaudeBillingProvider(firstNonEmpty(
			nativeProvider, state.Provider, source.ProviderHint,
		))
		keyID := firstNonEmpty(native.Message.ID, native.UUID, strconv.FormatInt(record.Offset, 10))
		event := domain.ModelUsageEvent{
			ProviderID:            domain.UsageProviderAnthropic,
			BillingProviderID:     billingProvider,
			BillingProviderSource: domain.ObservedBillingProviderSource(billingProvider),
			ModelID:               model,
			MeasurementKind:       domain.UsageMeasurementNativeReported,
			Tokens:                tokens,
			ProviderUsageJSON:     boundedProviderUsage(native.Message.Usage),
			CreatedAt:             parseUsageTimestamp(native.Timestamp),
			SourceEventKey: stableSourceEventKey(
				"claude",
				source.NativeRootID,
				string(source.Source.Kind),
				source.Source.SubagentID,
				source.Source.NativeSessionID,
				keyID,
			),
		}
		if existing, duplicate := eventsByKey[event.SourceEventKey]; duplicate {
			if !usageEventsEqual(existing, event) {
				result.Cursor.AnomalyCount++
				result.Cursor.LastErrorCode = domain.UsageErrorSourceEventConflict
			}
			continue
		}
		eventsByKey[event.SourceEventKey] = event
		result.Events = append(result.Events, event)
	}
}

// canonicalBillingProvider normalizes one observed routing fact into a catalog
// provider identifier. Unknown or conflicting routing stays empty so the event
// is stored unattributed rather than priced against the wrong provider.
func canonicalBillingProvider(observed string) string {
	provider := pricing.CanonicalProviderID(observed)
	if provider == "unknown" {
		return ""
	}
	return provider
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
		case "session_meta":
			var payload struct {
				ModelProvider string `json:"model_provider"`
			}
			if json.Unmarshal(envelope.Payload, &payload) == nil {
				state.Provider = firstNonEmpty(payload.ModelProvider, state.Provider)
			}
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
	// payload.info is the bounded object; payload.rate_limits is its sibling and
	// is deliberately never read, so account-level quota state cannot be stored.
	var payload struct {
		Type string          `json:"type"`
		Info json.RawMessage `json:"info"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil ||
		payload.Type != "token_count" || !jsonValueReported(payload.Info) {
		return
	}
	var info struct {
		Total              codexTokenVector  `json:"total_token_usage"`
		Last               *codexTokenVector `json:"last_token_usage"`
		ModelContextWindow int64             `json:"model_context_window"`
	}
	if err := json.Unmarshal(payload.Info, &info); err != nil {
		return
	}
	total := info.Total
	if isCodexContextFill(total, info.ModelContextWindow) {
		state.Baseline = codexTokenVector{}
		return
	}
	if !validCodexTotal(total) {
		recordMalformed(result)
		return
	}
	if total.InputTokens < state.Baseline.InputTokens ||
		total.CachedInputTokens < state.Baseline.CachedInputTokens ||
		total.CacheWriteInputTokens < state.Baseline.CacheWriteInputTokens ||
		total.OutputTokens < state.Baseline.OutputTokens ||
		total.ReasoningOutputTokens < state.Baseline.ReasoningOutputTokens ||
		(total.TotalTokens != 0 && state.Baseline.TotalTokens != 0 && total.TotalTokens < state.Baseline.TotalTokens) {
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
	reportedTotal := int64(0)
	if total.TotalTokens != 0 {
		if state.Baseline.TotalTokens != 0 {
			reportedTotal = total.TotalTokens - state.Baseline.TotalTokens
		} else {
			reportedTotal = input + output
		}
	}
	delta := codexTokenVector{
		InputTokens: input, CachedInputTokens: cached, CacheWriteInputTokens: cacheWrite,
		OutputTokens: output, ReasoningOutputTokens: reasoning, TotalTokens: reportedTotal,
	}
	selected := delta
	if info.Last != nil {
		last := *info.Last
		if !validCodexTotal(last) {
			result.Cursor.AnomalyCount++
			result.Cursor.LastErrorCode = domain.UsageErrorNonMonotonicCumulativeUsage
			state.Baseline = total
			return
		}
		if !codexVectorMatchesDelta(last, input, cached, cacheWrite, output, reasoning, reportedTotal) {
			result.Cursor.AnomalyCount++
			result.Cursor.LastErrorCode = domain.UsageErrorNonMonotonicCumulativeUsage
			selected = last
		} else if selected.TotalTokens == 0 && last.TotalTokens != 0 {
			selected.TotalTokens = last.TotalTokens
		}
	}
	if selected.InputTokens == 0 && selected.CachedInputTokens == 0 && selected.OutputTokens == 0 &&
		selected.CacheWriteInputTokens == 0 && selected.ReasoningOutputTokens == 0 {
		state.Baseline = total
		return
	}
	tokens, ok := normalizeOpenAIUsage(
		selected.InputTokens,
		selected.CachedInputTokens,
		selected.CacheWriteInputTokens,
		selected.OutputTokens,
	)
	if !ok {
		result.Cursor.AnomalyCount++
		result.Cursor.LastErrorCode = domain.UsageErrorNonMonotonicCumulativeUsage
		return
	}
	// A record that carried no per-event vector still gives up its cache-write
	// count, because the neutral counters beside it are the same subtraction of
	// the same two native readings. Persisting it is what keeps a write-rated
	// model priceable; see codexProviderUsage.
	var derivedCacheWrite *int64
	if info.Last == nil {
		write := selected.CacheWriteInputTokens
		derivedCacheWrite = &write
	}
	state.Baseline = total
	model := firstNonEmpty(state.ModelID, source.InitialModelID, "unknown")
	state.ModelID = model
	event := domain.ModelUsageEvent{
		ProviderID:            domain.UsageProviderOpenAI,
		BillingProviderID:     canonicalBillingProvider(state.Provider),
		BillingProviderSource: domain.ObservedBillingProviderSource(canonicalBillingProvider(state.Provider)),
		ModelID:               model,
		MeasurementKind:       domain.UsageMeasurementNativeReported,
		Tokens:                tokens,
		ProviderUsageJSON:     codexProviderUsage(payload.Info, derivedCacheWrite),
		CreatedAt:             parseUsageTimestamp(envelope.Timestamp),
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
	return total.ReasoningOutputTokens <= total.OutputTokens &&
		(total.TotalTokens == 0 || total.TotalTokens == total.InputTokens+total.OutputTokens)
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

func normalizeOpenAIUsage(input, cachedInput, cacheWriteInput, output int64) (domain.UsageTokenMetrics, bool) {
	if input < 0 || cachedInput < 0 || cachedInput > input || cacheWriteInput < 0 ||
		cacheWriteInput > input-cachedInput || output < 0 {
		return domain.UsageTokenMetrics{}, false
	}
	return domain.UsageTokenMetrics{
		InputTokens:         int64Ptr(input),
		CachedInputTokens:   int64Ptr(cachedInput),
		UncachedInputTokens: int64Ptr(input - cachedInput),
		OutputTokens:        int64Ptr(output),
	}, true
}

func normalizeAnthropicUsage(directInput, cacheCreationInput, cachedInput, output int64, creation5m, creation1h *int64) (domain.UsageTokenMetrics, bool) {
	uncachedInput, ok := sumNonNegative(directInput, cacheCreationInput)
	if !ok {
		return domain.UsageTokenMetrics{}, false
	}
	input, ok := sumNonNegative(cachedInput, uncachedInput)
	if !ok || output < 0 || !validAnthropicCacheCreation(cacheCreationInput, creation5m, creation1h) {
		return domain.UsageTokenMetrics{}, false
	}
	return domain.UsageTokenMetrics{
		InputTokens:         int64Ptr(input),
		CachedInputTokens:   int64Ptr(cachedInput),
		UncachedInputTokens: int64Ptr(uncachedInput),
		OutputTokens:        int64Ptr(output),
	}, true
}

// maxProviderUsageBytes bounds one stored provider usage object. Transcripts are
// untrusted input, and an object this large is no longer the small counter
// record the providers document, so it is dropped rather than persisted.
const maxProviderUsageBytes = 8 << 10

// jsonValueReported reports whether raw carries a value at all, as opposed to
// being absent or an explicit JSON null.
//
// That distinction used to come free from a nil pointer. A json.RawMessage
// holding `null` has nonzero length and decodes into a typed struct as all-zero
// values, so a record reporting no usage would otherwise become a synthetic
// zero event — or, for Codex, a cumulative baseline of zero that recharges
// every earlier token. Callers still decode the value afterwards, so a present
// but non-object member remains malformed rather than being silently dropped.
func jsonValueReported(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// isJSONObject reports whether raw is a present JSON object. Unmarshalling into
// a map is the check: JSON null yields a nil map without an error, and any
// other JSON type fails outright.
func isJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

// codexProviderUsage stores the CLI's own usage object, plus the per-event
// cache-write count when the CLI reported only cumulative totals.
//
// The estimator needs a per-event write bucket to price a model that publishes
// a cache-write rate — every current gpt-5.6 variant does — and it reads that
// bucket from last_token_usage, which is optional. On a record without one the
// counters AO stores are already a delta of two native cumulative readings, and
// this is that same subtraction over the same readings, so it is derived rather
// than invented. It is namespaced instead of folded into a last_token_usage the
// CLI never emitted: a reader must still be able to tell what the provider said
// from what AO worked out.
func codexProviderUsage(raw json.RawMessage, derivedCacheWrite *int64) string {
	stored := boundedProviderUsage(raw)
	if stored == "" || derivedCacheWrite == nil {
		return stored
	}
	derived := json.RawMessage(strconv.FormatInt(*derivedCacheWrite, 10))
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(stored), &object); err != nil {
		return stored
	}
	object[derivedCacheWriteKey] = derived
	encoded, err := json.Marshal(object)
	if err != nil || len(encoded) > maxProviderUsageBytes {
		return stored
	}
	return string(encoded)
}

// derivedCacheWriteKey names the one member AO adds to a stored provider usage
// object. The ao_ prefix is the whole point: nothing reading this object should
// mistake it for something a provider reported.
const derivedCacheWriteKey = "ao_derived_cache_write_input_tokens"

// boundedProviderUsage compacts one provider usage object for durable storage.
// It returns empty for anything that is not a JSON object or exceeds the bound,
// which stores SQL NULL: an absent object is honest, a truncated one is not.
func boundedProviderUsage(raw json.RawMessage) string {
	if len(raw) > maxProviderUsageBytes || !isJSONObject(raw) {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return ""
	}
	return compact.String()
}

func validAnthropicCacheCreation(total int64, creation5m, creation1h *int64) bool {
	if creation5m == nil && creation1h == nil {
		return true
	}
	if creation5m == nil || creation1h == nil || *creation5m < 0 || *creation1h < 0 {
		return false
	}
	return *creation5m <= total && *creation1h <= total-*creation5m
}

func codexVectorMatchesDelta(last codexTokenVector, input, cachedInput, cacheWriteInput, output, reasoningOutput, total int64) bool {
	return validCodexTotal(last) && last.InputTokens == input && last.CachedInputTokens == cachedInput &&
		last.CacheWriteInputTokens == cacheWriteInput && last.OutputTokens == output &&
		last.ReasoningOutputTokens == reasoningOutput &&
		(last.TotalTokens == 0 || total == 0 || last.TotalTokens == total)
}

func parseUsageTimestamp(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func usageEventsEqual(a, b domain.ModelUsageEvent) bool {
	a.CreatedAt, b.CreatedAt = time.Time{}, time.Time{}
	a.SourceEventKey, b.SourceEventKey = "", ""
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
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
