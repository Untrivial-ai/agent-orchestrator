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
			native.InputTokens < 0 || native.OutputTokens < 0 || native.CachedTokens < 0 ||
			native.ThoughtsTokens < 0 ||
			native.CachedTokens > native.InputTokens ||
			native.TotalTokens != nil && (*native.TotalTokens < 0 ||
				*native.TotalTokens != native.InputTokens+native.OutputTokens+native.ThoughtsTokens) {
			recordMalformed(result)
			continue
		}
		tokens := domain.UsageTokenMetrics{
			InputTokens:         native.InputTokens,
			UncachedInputTokens: native.InputTokens - native.CachedTokens,
			CacheReadTokens:     native.CachedTokens,
			OutputTokens:        native.OutputTokens + native.ThoughtsTokens,
			ReasoningTokens:     int64Ptr(native.ThoughtsTokens),
		}
		if !validTokenMetrics(tokens) {
			recordMalformed(result)
			continue
		}
		result.Events = append(result.Events, domain.ModelUsageEvent{
			ModelID: model,
			Tokens:  tokens,
			SourceEventKey: stableSourceEventKey(
				"qwen",
				source.NativeRootID,
				identity,
				model,
			),
		})
	}
}
