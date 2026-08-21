package usage

import (
	"encoding/json"
	"strconv"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type copilotTranscriptRecord struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Model           string `json:"model"`
		InputTokens     int64  `json:"inputTokens"`
		OutputTokens    int64  `json:"outputTokens"`
		ReasoningTokens *int64 `json:"reasoningTokens"`
		ModelMetrics    map[string]struct {
			Usage *struct {
				InputTokens      int64 `json:"inputTokens"`
				OutputTokens     int64 `json:"outputTokens"`
				CacheReadTokens  int64 `json:"cacheReadTokens"`
				CacheWriteTokens int64 `json:"cacheWriteTokens"`
				ReasoningTokens  int64 `json:"reasoningTokens"`
			} `json:"usage"`
		} `json:"modelMetrics"`
	} `json:"data"`
}

func parseCopilot(
	source domain.UsageSourceContext,
	records []jsonlRecord,
	state *copilotParserStateV1,
	result *parseResult,
) {
	hasShutdown := false
	for _, record := range records {
		var native copilotTranscriptRecord
		if json.Unmarshal(record.Data, &native) == nil && native.Type == "session.shutdown" {
			hasShutdown = true
			break
		}
	}
	for _, record := range records {
		var native copilotTranscriptRecord
		if err := json.Unmarshal(record.Data, &native); err != nil {
			recordMalformed(result)
			continue
		}
		switch native.Type {
		case "assistant.message":
			if !hasShutdown {
				appendCopilotAssistantMessageUsage(source, record, native, state, result)
			}
		case "session.shutdown":
			parseCopilotShutdownUsage(source, native, state, result)
		default:
			continue
		}
	}
}

func appendCopilotAssistantMessageUsage(
	source domain.UsageSourceContext,
	record jsonlRecord,
	native copilotTranscriptRecord,
	state *copilotParserStateV1,
	result *parseResult,
) {
	model := firstNonEmpty(native.Data.Model)
	if model == "" || native.Data.InputTokens < 0 || native.Data.OutputTokens < 0 ||
		(native.Data.ReasoningTokens != nil && (*native.Data.ReasoningTokens < 0 || *native.Data.ReasoningTokens > native.Data.OutputTokens)) {
		recordMalformed(result)
		return
	}
	if native.Data.InputTokens == 0 && native.Data.OutputTokens == 0 {
		return
	}
	tokens := domain.UsageTokenMetrics{
		InputTokens:         native.Data.InputTokens,
		UncachedInputTokens: native.Data.InputTokens,
		OutputTokens:        native.Data.OutputTokens,
		ReasoningTokens:     native.Data.ReasoningTokens,
	}
	if !validTokenMetrics(tokens) {
		recordMalformed(result)
		return
	}
	baseline := state.Models[model]
	baseline.InputTokens += native.Data.InputTokens
	baseline.OutputTokens += native.Data.OutputTokens
	if native.Data.ReasoningTokens != nil {
		baseline.ReasoningTokens += *native.Data.ReasoningTokens
	}
	state.Models[model] = baseline
	identity := firstNonEmpty(native.ID, strconv.FormatInt(record.Offset, 10))
	result.Events = append(result.Events, domain.ModelUsageEvent{
		ModelID: model,
		Tokens:  tokens,
		SourceEventKey: stableSourceEventKey(
			"copilot-message",
			source.NativeRootID,
			model,
			identity,
		),
	})
}

func parseCopilotShutdownUsage(
	source domain.UsageSourceContext,
	native copilotTranscriptRecord,
	state *copilotParserStateV1,
	result *parseResult,
) {
	for nativeModel, metrics := range native.Data.ModelMetrics {
		model := firstNonEmpty(nativeModel)
		if model == "" || metrics.Usage == nil {
			recordMalformed(result)
			continue
		}
		usage := metrics.Usage
		total := copilotTokenVector{
			InputTokens:      usage.InputTokens,
			CacheReadTokens:  usage.CacheReadTokens,
			CacheWriteTokens: usage.CacheWriteTokens,
			OutputTokens:     usage.OutputTokens,
			ReasoningTokens:  usage.ReasoningTokens,
		}
		if !validCopilotTotal(total) {
			recordMalformed(result)
			continue
		}
		baseline := state.Models[model]
		if copilotCounterRegressed(total, baseline) {
			state.Models[model] = total
			result.Cursor.AnomalyCount++
			result.Cursor.LastErrorCode = domain.UsageErrorNonMonotonicCumulativeUsage
			continue
		}
		delta := subtractCopilotTotal(total, baseline)
		if delta.ReasoningTokens > delta.OutputTokens {
			delta.ReasoningTokens = delta.OutputTokens
		}
		state.Models[model] = total
		if delta.InputTokens == 0 && delta.OutputTokens == 0 && delta.CacheWriteTokens == 0 {
			continue
		}
		uncached := delta.InputTokens - delta.CacheReadTokens - delta.CacheWriteTokens
		tokens := domain.UsageTokenMetrics{
			InputTokens:         delta.InputTokens,
			UncachedInputTokens: uncached,
			CacheReadTokens:     delta.CacheReadTokens,
			CacheWriteTokens:    delta.CacheWriteTokens,
			OutputTokens:        delta.OutputTokens,
			ReasoningTokens:     int64Ptr(delta.ReasoningTokens),
		}
		if !validTokenMetrics(tokens) {
			recordMalformed(result)
			continue
		}
		result.Events = append(result.Events, domain.ModelUsageEvent{
			ModelID: model,
			Tokens:  tokens,
			SourceEventKey: stableSourceEventKey(
				"copilot",
				source.NativeRootID,
				model,
				strconv.FormatInt(total.InputTokens, 10),
				strconv.FormatInt(total.CacheReadTokens, 10),
				strconv.FormatInt(total.CacheWriteTokens, 10),
				strconv.FormatInt(total.OutputTokens, 10),
				strconv.FormatInt(total.ReasoningTokens, 10),
			),
		})
	}
}

func validCopilotTotal(total copilotTokenVector) bool {
	return total.InputTokens >= 0 && total.CacheReadTokens >= 0 && total.CacheWriteTokens >= 0 &&
		total.OutputTokens >= 0 && total.ReasoningTokens >= 0 &&
		total.CacheReadTokens <= total.InputTokens &&
		total.CacheWriteTokens <= total.InputTokens-total.CacheReadTokens &&
		total.ReasoningTokens <= total.OutputTokens
}

func copilotCounterRegressed(total, baseline copilotTokenVector) bool {
	return total.InputTokens < baseline.InputTokens ||
		total.CacheReadTokens < baseline.CacheReadTokens ||
		total.CacheWriteTokens < baseline.CacheWriteTokens ||
		total.OutputTokens < baseline.OutputTokens ||
		total.ReasoningTokens < baseline.ReasoningTokens
}

func subtractCopilotTotal(total, baseline copilotTokenVector) copilotTokenVector {
	return copilotTokenVector{
		InputTokens:      total.InputTokens - baseline.InputTokens,
		CacheReadTokens:  total.CacheReadTokens - baseline.CacheReadTokens,
		CacheWriteTokens: total.CacheWriteTokens - baseline.CacheWriteTokens,
		OutputTokens:     total.OutputTokens - baseline.OutputTokens,
		ReasoningTokens:  total.ReasoningTokens - baseline.ReasoningTokens,
	}
}
