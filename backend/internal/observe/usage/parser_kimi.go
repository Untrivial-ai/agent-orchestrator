package usage

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type kimiWireRecord struct {
	ID    string          `json:"id"`
	Time  json.RawMessage `json:"time"`
	Type  string          `json:"type"`
	Model string          `json:"model"`
	Usage json.RawMessage `json:"usage"`
}

type kimiNativeUsage struct {
	InputOther         int64 `json:"inputOther"`
	InputCacheRead     int64 `json:"inputCacheRead"`
	InputCacheCreation int64 `json:"inputCacheCreation"`
	Output             int64 `json:"output"`
}

func parseKimi(source domain.UsageSourceContext, records []jsonlRecord, result *parseResult) {
	eventsByKey := make(map[string]domain.ModelUsageEvent)
	for _, record := range records {
		var native kimiWireRecord
		if err := json.Unmarshal(record.Data, &native); err != nil {
			recordMalformed(result)
			continue
		}
		if native.Type != "usage.record" {
			continue
		}
		model := firstNonEmpty(native.Model)
		if model == "" || !jsonValueReported(native.Usage) {
			recordMalformed(result)
			continue
		}
		var usage kimiNativeUsage
		if err := json.Unmarshal(native.Usage, &usage); err != nil {
			recordMalformed(result)
			continue
		}
		tokens, ok := normalizeAnthropicUsage(
			usage.InputOther,
			usage.InputCacheCreation,
			usage.InputCacheRead,
			usage.Output,
			nil,
			nil,
		)
		if !ok {
			recordMalformed(result)
			continue
		}
		recordTime := decodeKimiRecordTime(native.Time)
		identity := firstNonEmpty(native.ID, recordTime.Identity, strconv.FormatInt(record.Offset, 10))
		event := domain.ModelUsageEvent{
			ProviderID:        domain.UsageProviderAnthropic,
			ModelID:           model,
			MeasurementKind:   domain.UsageMeasurementNativeReported,
			Tokens:            tokens,
			ProviderUsageJSON: boundedProviderUsage(native.Usage),
			CreatedAt:         recordTime.Time,
			SourceEventKey: stableSourceEventKey(
				"kimi",
				source.NativeRootID,
				source.Source.SubagentID,
				identity,
				model,
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

type kimiRecordTime struct {
	Identity string
	Time     time.Time
}

func decodeKimiRecordTime(raw json.RawMessage) kimiRecordTime {
	if len(raw) == 0 || string(raw) == "null" {
		return kimiRecordTime{}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return kimiRecordTime{Identity: text, Time: parseUsageTimestamp(text)}
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return kimiRecordTime{}
	}
	decoded := kimiRecordTime{Identity: number.String()}
	milliseconds, err := number.Int64()
	if err != nil {
		return decoded
	}
	decoded.Time = time.UnixMilli(milliseconds).UTC()
	return decoded
}
