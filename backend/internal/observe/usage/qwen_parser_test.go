package usage

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestParseQwenUsageForBoundSession(t *testing.T) {
	source := usageSource(domain.UsageSourceQwenMonthly)
	source.NativeRootID = "qwen-session"
	records := []jsonlRecord{
		{Data: []byte(`{"schemaVersion":1,"id":"other","sessionId":"another","model":"qwen3","inputTokens":999,"outputTokens":999,"cachedTokens":0,"thoughtsTokens":0,"totalTokens":1998}`)},
		{Offset: 100, Data: []byte(`{"schemaVersion":1,"id":"turn-1","sessionId":"qwen-session","model":"qwen3-coder","inputTokens":30,"outputTokens":12,"cachedTokens":9,"thoughtsTokens":4,"totalTokens":46}`)},
	}
	result := parseRecords(source, records, 300, time.Unix(1700000000, 0).UTC())
	if result.err != nil || len(result.Events) != 1 {
		t.Fatalf("result = %+v", result)
	}
	event := result.Events[0]
	if event.ModelID != "qwen3-coder" || event.SourceEventKey == "" {
		t.Fatalf("event = %+v", event)
	}
	if got := event.Tokens; got.InputTokens != 30 || got.UncachedInputTokens != 21 ||
		got.CacheReadTokens != 9 || got.CacheWriteTokens != 0 || got.OutputTokens != 16 ||
		got.ReasoningTokens == nil || *got.ReasoningTokens != 4 {
		t.Fatalf("tokens = %+v", got)
	}
}

func TestParseQwenNormalizesThoughtsIntoOutputTokens(t *testing.T) {
	source := usageSource(domain.UsageSourceQwenMonthly)
	source.NativeRootID = "native-root"
	record := jsonlRecord{Data: []byte(`{"schemaVersion":1,"id":"turn-1","sessionId":"native-root","model":"qwen3","inputTokens":3,"outputTokens":2,"cachedTokens":1,"thoughtsTokens":4,"totalTokens":9}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if result.err != nil || len(result.Events) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if got := result.Events[0].Tokens; got.OutputTokens != 6 || got.ReasoningTokens == nil || *got.ReasoningTokens != 4 {
		t.Fatalf("tokens = %+v", got)
	}
}

func TestParseQwenRejectsInconsistentTotals(t *testing.T) {
	source := usageSource(domain.UsageSourceQwenMonthly)
	source.NativeRootID = "native-root"
	record := jsonlRecord{Data: []byte(`{"schemaVersion":1,"id":"bad","sessionId":"native-root","model":"qwen3","inputTokens":3,"outputTokens":2,"cachedTokens":4,"thoughtsTokens":0,"totalTokens":5}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if len(result.Events) != 0 || result.Cursor.AnomalyCount != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseQwenAllowsOmittedTotalTokens(t *testing.T) {
	source := usageSource(domain.UsageSourceQwenMonthly)
	source.NativeRootID = "native-root"
	record := jsonlRecord{Data: []byte(`{"schemaVersion":1,"id":"turn-1","sessionId":"native-root","source":"managed-auto-memory-extractor","model":"qwen3","inputTokens":3,"outputTokens":2,"cachedTokens":1,"thoughtsTokens":0}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if result.err != nil || len(result.Events) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseQwenRequiresNativeRecordID(t *testing.T) {
	source := usageSource(domain.UsageSourceQwenMonthly)
	source.NativeRootID = "native-root"
	record := jsonlRecord{Data: []byte(`{"schemaVersion":1,"sessionId":"native-root","model":"qwen3","inputTokens":3,"outputTokens":2,"cachedTokens":1,"thoughtsTokens":0,"totalTokens":5}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if len(result.Events) != 0 || result.Cursor.AnomalyCount != 1 {
		t.Fatalf("result = %+v", result)
	}
}
