package usage

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TestParseKimiUsageRecord catches omitting cache buckets from Kimi's ordinary
// input field or counting unrelated wire records as usage.
func TestParseKimiUsageRecord(t *testing.T) {
	source := usageSource(domain.UsageSourceKimiWire)
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"id":"message-1","time":"2026-08-09T09:59:00Z","type":"message.create","message":{}}`)},
		{Offset: 100, Data: []byte(`{"id":"usage-1","time":"2026-08-09T10:00:00Z","type":"usage.record","model":"kimi-for-coding","usage":{"inputOther":13,"inputCacheRead":21,"inputCacheCreation":8,"output":5},"usageScope":"turn"}`)},
	}
	result := parseRecords(source, records, 300, time.Unix(1700000000, 0).UTC())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %+v, want one usage record", result.Events)
	}
	event := result.Events[0]
	if event.ModelID != "kimi-for-coding" || event.SourceEventKey == "" {
		t.Fatalf("event = %+v", event)
	}
	if got := event.Tokens; got.InputTokens != 42 || got.UncachedInputTokens != 13 ||
		got.CacheReadTokens != 21 || got.CacheWriteTokens != 8 || got.OutputTokens != 5 ||
		got.ReasoningTokens != nil {
		t.Fatalf("tokens = %+v", got)
	}
}

func TestParseKimiUsageRecordWithNumericTime(t *testing.T) {
	source := usageSource(domain.UsageSourceKimiWire)
	record := jsonlRecord{Offset: 100, Data: []byte(`{"time":1797038183476.96,"type":"usage.record","model":"kimi-code/k3","usage":{"inputOther":6697,"output":39,"inputCacheRead":19200,"inputCacheCreation":0},"usageScope":"turn"}`)}
	result := parseRecords(source, []jsonlRecord{record}, 300, time.Unix(1700000000, 0).UTC())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %+v, want one usage record", result.Events)
	}
	if got := result.Events[0].Tokens; got.InputTokens != 25897 || got.UncachedInputTokens != 6697 ||
		got.CacheReadTokens != 19200 || got.CacheWriteTokens != 0 || got.OutputTokens != 39 {
		t.Fatalf("tokens = %+v", got)
	}
	if result.Events[0].SourceEventKey == "" {
		t.Fatalf("event key is empty")
	}
}

func TestParseKimiUsageRecordHasReplayStableKey(t *testing.T) {
	source := usageSource(domain.UsageSourceKimiWire)
	record := jsonlRecord{Offset: 100, Data: []byte(`{"id":"usage-1","time":"2026-08-09T10:00:00Z","type":"usage.record","model":"kimi-for-coding","usage":{"inputOther":1,"inputCacheRead":2,"inputCacheCreation":3,"output":4}}`)}
	first := parseRecords(source, []jsonlRecord{record}, 200, time.Unix(1700000000, 0).UTC())
	second := parseRecords(source, []jsonlRecord{record}, 200, time.Unix(1700000001, 0).UTC())
	if first.err != nil || second.err != nil || len(first.Events) != 1 || len(second.Events) != 1 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if first.Events[0].SourceEventKey != second.Events[0].SourceEventKey {
		t.Fatalf("keys = %q/%q", first.Events[0].SourceEventKey, second.Events[0].SourceEventKey)
	}
}

func TestParseKimiRejectsNegativeUsage(t *testing.T) {
	source := usageSource(domain.UsageSourceKimiWire)
	record := jsonlRecord{Data: []byte(`{"id":"usage-bad","type":"usage.record","model":"kimi-for-coding","usage":{"inputOther":-1,"inputCacheRead":0,"inputCacheCreation":0,"output":1}}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Unix(1700000000, 0).UTC())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.Events) != 0 || result.Cursor.AnomalyCount != 1 || result.Cursor.LastErrorCode != domain.UsageErrorMalformedJSONL {
		t.Fatalf("result = %+v", result)
	}
}
