package usage

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TestParseQwenUsageForBoundSession catches cross-session attribution and
// preserves Qwen's cached and reasoning token buckets.
func TestParseQwenUsageForBoundSession(t *testing.T) {
	source := usageSource(domain.UsageSourceQwenMonthly)
	source.NativeRootID = "qwen-session"
	records := []jsonlRecord{
		{Data: []byte(`{"schemaVersion":1,"id":"other","sessionId":"another","model":"qwen3","inputTokens":999,"outputTokens":999,"cachedTokens":0,"thoughtsTokens":0,"totalTokens":1998}`)},
		{Offset: 100, Data: []byte(`{"schemaVersion":1,"id":"turn-1","timestamp":"2026-08-09T10:00:00Z","sessionId":"qwen-session","model":"qwen3-coder","inputTokens":30,"outputTokens":12,"cachedTokens":9,"thoughtsTokens":4,"totalTokens":46}`)},
	}

	result := parseRecords(source, records, 300, time.Unix(1700000000, 0).UTC())
	if result.err != nil || len(result.Events) != 1 {
		t.Fatalf("result = %+v", result)
	}
	event := result.Events[0]
	if event.ProviderID != domain.UsageProviderOpenAI || event.ModelID != "qwen3-coder" ||
		event.CreatedAt != time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC) || event.SourceEventKey == "" {
		t.Fatalf("event = %+v", event)
	}
	if tokenValue(event.Tokens.InputTokens) != 30 ||
		tokenValue(event.Tokens.CachedInputTokens) != 9 ||
		tokenValue(event.Tokens.UncachedInputTokens) != 21 ||
		tokenValue(event.Tokens.OutputTokens) != 16 {
		t.Fatalf("tokens = %+v", event.Tokens)
	}
	if event.MeasurementKind != domain.UsageMeasurementNativeReported {
		t.Fatalf("measurement kind = %q, want native_reported", event.MeasurementKind)
	}
	if providerUsageTokens(t, event.ProviderUsageJSON, "thoughtsTokens") != 4 ||
		providerUsageTokens(t, event.ProviderUsageJSON, "totalTokens") != 46 {
		t.Fatalf("provider usage = %s", event.ProviderUsageJSON)
	}
}

func TestParseQwenRejectsCachedTokensExceedingInput(t *testing.T) {
	source := usageSource(domain.UsageSourceQwenMonthly)
	source.NativeRootID = "native-root"
	record := jsonlRecord{Data: []byte(`{"schemaVersion":1,"id":"bad","sessionId":"native-root","model":"qwen3","inputTokens":3,"outputTokens":2,"cachedTokens":4,"thoughtsTokens":0,"totalTokens":5}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.Events) != 0 || result.Cursor.AnomalyCount != 1 ||
		result.Cursor.LastErrorCode != domain.UsageErrorMalformedJSONL {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseQwenKeepsProviderReportedTotal(t *testing.T) {
	source := usageSource(domain.UsageSourceQwenMonthly)
	source.NativeRootID = "native-root"
	record := jsonlRecord{Data: []byte(`{"schemaVersion":1,"id":"turn-2","sessionId":"native-root","model":"qwen3","inputTokens":3,"outputTokens":2,"cachedTokens":1,"thoughtsTokens":0,"totalTokens":9}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if result.err != nil || len(result.Events) != 1 || result.Cursor.AnomalyCount != 0 {
		t.Fatalf("result = %+v", result)
	}
	if providerUsageTokens(t, result.Events[0].ProviderUsageJSON, "totalTokens") != 9 {
		t.Fatalf("provider usage = %s", result.Events[0].ProviderUsageJSON)
	}
}

func TestParseQwenAllowsOmittedTotalTokens(t *testing.T) {
	source := usageSource(domain.UsageSourceQwenMonthly)
	source.NativeRootID = "native-root"
	record := jsonlRecord{Data: []byte(`{"schemaVersion":1,"id":"turn-1","sessionId":"native-root","model":"qwen3","inputTokens":3,"outputTokens":2,"cachedTokens":1,"thoughtsTokens":0}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if result.err != nil || len(result.Events) != 1 {
		t.Fatalf("result = %+v", result)
	}
}
