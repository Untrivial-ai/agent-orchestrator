package usage

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestParsePiAssistantUsage(t *testing.T) {
	source := usageSource(domain.UsageSourcePiSession)
	records := []jsonlRecord{
		{Data: []byte(`{"type":"session","id":"pi-session","cwd":"/repo","version":3}`)},
		{Offset: 100, Data: []byte(`{"type":"message","id":"msg-1","message":{"role":"assistant","provider":"zai-glm","model":"glm-4.5","usage":{"input":11,"output":7,"cacheRead":5,"cacheWrite":3,"totalTokens":26,"cost":{"total":99}}}}`)},
		{Offset: 200, Data: []byte(`{"type":"message","id":"msg-2","message":{"role":"user","usage":{"input":100}}}`)},
	}
	result := parseRecords(source, records, 300, time.Unix(1700000000, 0).UTC())
	if result.err != nil || len(result.Events) != 1 {
		t.Fatalf("result = %+v", result)
	}
	event := result.Events[0]
	if event.ModelID != "zai-glm/glm-4.5" || event.SourceEventKey == "" {
		t.Fatalf("event = %+v", event)
	}
	if got := event.Tokens; got.InputTokens != 19 || got.UncachedInputTokens != 11 ||
		got.CacheReadTokens != 5 || got.CacheWriteTokens != 3 || got.OutputTokens != 7 || got.ReasoningTokens != nil {
		t.Fatalf("tokens = %+v", got)
	}
}

func TestParsePiRejectsInvalidUsage(t *testing.T) {
	source := usageSource(domain.UsageSourcePiSession)
	record := jsonlRecord{Data: []byte(`{"type":"message","id":"bad","message":{"role":"assistant","model":"m","usage":{"input":-1,"output":1}}}`)}
	result := parseRecords(source, []jsonlRecord{record}, 100, time.Now())
	if len(result.Events) != 0 || result.Cursor.AnomalyCount != 1 {
		t.Fatalf("result = %+v", result)
	}
}
