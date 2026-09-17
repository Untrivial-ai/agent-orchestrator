package usage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const testKimiWireSource domain.UsageSourceKind = "kimi_wire"

// TestParseKimiUsageRecord catches dropping Kimi's cache buckets or counting
// non-usage wire records as model usage.
func TestParseKimiUsageRecord(t *testing.T) {
	source := usageSource(testKimiWireSource)
	source.NativeRootID = "kimi-session"
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
	if event.ProviderID != domain.UsageProviderAnthropic || event.ModelID != "kimi-for-coding" ||
		event.MeasurementKind != domain.UsageMeasurementNativeReported ||
		event.CreatedAt != time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC) || event.SourceEventKey == "" {
		t.Fatalf("event = %+v", event)
	}
	if tokenValue(event.Tokens.InputTokens) != 42 || tokenValue(event.Tokens.CachedInputTokens) != 21 ||
		tokenValue(event.Tokens.UncachedInputTokens) != 21 || tokenValue(event.Tokens.OutputTokens) != 5 {
		t.Fatalf("tokens = %+v", event.Tokens)
	}
	if providerUsageTokens(t, event.ProviderUsageJSON, "inputOther") != 13 ||
		providerUsageTokens(t, event.ProviderUsageJSON, "inputCacheRead") != 21 ||
		providerUsageTokens(t, event.ProviderUsageJSON, "inputCacheCreation") != 8 ||
		providerUsageTokens(t, event.ProviderUsageJSON, "output") != 5 {
		t.Fatalf("provider usage = %s", event.ProviderUsageJSON)
	}
}

func TestParseKimiUsageRecordHasReplayStableKey(t *testing.T) {
	source := usageSource(testKimiWireSource)
	source.NativeRootID = "kimi-session"
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

// TestParseKimiUsageRecordWithEpochMilliseconds catches treating Kimi's
// numeric time field as RFC3339 text and silently dropping CreatedAt.
func TestParseKimiUsageRecordWithEpochMilliseconds(t *testing.T) {
	source := usageSource(testKimiWireSource)
	source.NativeRootID = "kimi-session"
	record := jsonlRecord{Offset: 100, Data: []byte(`{"id":"usage-epoch","time":1786269600123,"type":"usage.record","model":"kimi-for-coding","usage":{"inputOther":1,"inputCacheRead":2,"inputCacheCreation":3,"output":4}}`)}

	result := parseRecords(source, []jsonlRecord{record}, 200, time.Unix(1700000000, 0).UTC())
	if result.err != nil || len(result.Events) != 1 {
		t.Fatalf("result = %+v", result)
	}
	want := time.UnixMilli(1786269600123).UTC()
	if got := result.Events[0].CreatedAt; !got.Equal(want) {
		t.Fatalf("CreatedAt = %v, want %v", got, want)
	}
}

// TestDecodeKimiRecordTime catches timestamp identity and event time being
// decoded by separate format rules.
func TestDecodeKimiRecordTime(t *testing.T) {
	tests := []struct {
		name         string
		raw          json.RawMessage
		wantIdentity string
		wantTime     time.Time
	}{
		{
			name:         "RFC3339",
			raw:          json.RawMessage(`"2026-08-09T10:00:00Z"`),
			wantIdentity: "2026-08-09T10:00:00Z",
			wantTime:     time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC),
		},
		{
			name:         "epoch milliseconds",
			raw:          json.RawMessage(`1786269600123`),
			wantIdentity: "1786269600123",
			wantTime:     time.UnixMilli(1786269600123).UTC(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := decodeKimiRecordTime(test.raw)
			if got.Identity != test.wantIdentity || !got.Time.Equal(test.wantTime) {
				t.Fatalf("decoded time = %+v, want identity %q and time %v", got, test.wantIdentity, test.wantTime)
			}
		})
	}
}

func TestParseKimiRejectsNegativeUsage(t *testing.T) {
	source := usageSource(testKimiWireSource)
	record := jsonlRecord{Data: []byte(`{"id":"usage-bad","type":"usage.record","model":"kimi-for-coding","usage":{"inputOther":-1,"inputCacheRead":0,"inputCacheCreation":0,"output":1}}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Unix(1700000000, 0).UTC())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.Events) != 0 || result.Cursor.AnomalyCount != 1 || result.Cursor.LastErrorCode != domain.UsageErrorMalformedJSONL {
		t.Fatalf("result = %+v", result)
	}
}
