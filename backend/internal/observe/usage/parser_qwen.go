package usage

import (
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type qwenUsageRecord struct {
	SchemaVersion  int    `json:"schemaVersion"`
	ID             string `json:"id"`
	Timestamp      string `json:"timestamp"`
	SessionID      string `json:"sessionId"`
	Model          string `json:"model"`
	InputTokens    int64  `json:"inputTokens"`
	OutputTokens   int64  `json:"outputTokens"`
	CachedTokens   int64  `json:"cachedTokens"`
	ThoughtsTokens int64  `json:"thoughtsTokens"`
	TotalTokens    *int64 `json:"totalTokens"`
}

func parseQwen(source domain.UsageSourceContext, records []jsonlRecord, result *parseResult) {
	eventsByKey := make(map[string]domain.ModelUsageEvent)
	for _, record := range records {
		var native qwenUsageRecord
		if err := json.Unmarshal(record.Data, &native); err != nil {
			recordMalformed(result)
			continue
		}
		if native.SessionID != source.NativeRootID {
			continue
		}
		model := firstNonEmpty(native.Model)
		identity := firstNonEmpty(native.ID)
		if native.SchemaVersion != 1 || model == "" || identity == "" ||
			native.InputTokens < 0 || native.OutputTokens < 0 ||
			native.CachedTokens < 0 || native.ThoughtsTokens < 0 {
			recordMalformed(result)
			continue
		}
		output, ok := sumNonNegative(native.OutputTokens, native.ThoughtsTokens)
		if !ok {
			recordMalformed(result)
			continue
		}
		_, ok = sumNonNegative(native.InputTokens, output)
		if !ok || native.TotalTokens != nil && *native.TotalTokens < 0 {
			recordMalformed(result)
			continue
		}
		tokens, ok := normalizeOpenAIUsage(
			native.InputTokens, native.CachedTokens, 0, output,
		)
		if !ok {
			recordMalformed(result)
			continue
		}
		event := domain.ModelUsageEvent{
			ProviderID:        domain.UsageProviderOpenAI,
			ModelID:           model,
			MeasurementKind:   domain.UsageMeasurementNativeReported,
			Tokens:            tokens,
			ProviderUsageJSON: boundedProviderUsage(record.Data),
			CreatedAt:         parseUsageTimestamp(native.Timestamp),
			SourceEventKey: stableSourceEventKey(
				"qwen", source.NativeRootID, identity, model,
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
