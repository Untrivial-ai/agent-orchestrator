package usage

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type piSessionRecord struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Message *struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Usage    *struct {
			Input      int64 `json:"input"`
			Output     int64 `json:"output"`
			CacheRead  int64 `json:"cacheRead"`
			CacheWrite int64 `json:"cacheWrite"`
		} `json:"usage"`
	} `json:"message"`
}

func parsePi(source domain.UsageSourceContext, records []jsonlRecord, result *parseResult) {
	for _, record := range records {
		var native piSessionRecord
		if err := json.Unmarshal(record.Data, &native); err != nil {
			recordMalformed(result)
			continue
		}
		if native.Type != "message" || native.Message == nil || native.Message.Role != "assistant" {
			continue
		}
		message := native.Message
		model := firstNonEmpty(message.Model)
		if model == "" || message.Usage == nil {
			recordMalformed(result)
			continue
		}
		provider := firstNonEmpty(message.Provider)
		if provider != "" {
			model = provider + "/" + model
		}
		usage := message.Usage
		input, ok := sumNonNegative(usage.Input, usage.CacheRead, usage.CacheWrite)
		if !ok || usage.Output < 0 {
			recordMalformed(result)
			continue
		}
		tokens := domain.UsageTokenMetrics{
			InputTokens:         input,
			UncachedInputTokens: usage.Input,
			CacheReadTokens:     usage.CacheRead,
			CacheWriteTokens:    usage.CacheWrite,
			OutputTokens:        usage.Output,
		}
		if !validTokenMetrics(tokens) {
			recordMalformed(result)
			continue
		}
		identity := firstNonEmpty(native.ID, strconv.FormatInt(record.Offset, 10))
		result.Events = append(result.Events, domain.ModelUsageEvent{
			ModelID: model,
			Tokens:  tokens,
			SourceEventKey: stableSourceEventKey(
				"pi",
				source.NativeRootID,
				identity,
				strings.TrimSpace(message.Provider),
				strings.TrimSpace(message.Model),
			),
		})
	}
}
