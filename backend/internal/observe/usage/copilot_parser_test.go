package usage

import (
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TestParseCopilotCumulativeShutdownDeltas catches counting repeated shutdown
// summaries twice or treating inclusive input as ordinary input.
func TestParseCopilotCumulativeShutdownDeltas(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	source := usageSource(domain.UsageSourceCopilotShutdown)
	records := []jsonlRecord{
		{Offset: 0, Data: copilotShutdownLine("claude-haiku-4.5", 111529, 86021, 25483, 901, 251)},
		{Offset: 100, Data: copilotShutdownLine("claude-haiku-4.5", 111529, 86021, 25483, 901, 251)},
		{Offset: 200, Data: copilotShutdownLine("claude-haiku-4.5", 120000, 90000, 26000, 950, 275)},
	}

	result := parseRecords(source, records, 300, now)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("events = %d, want initial total and one positive delta", len(result.Events))
	}
	first := result.Events[0]
	if first.ModelID != "claude-haiku-4.5" {
		t.Fatalf("model = %q", first.ModelID)
	}
	if got := first.Tokens; got.InputTokens != 111529 || got.UncachedInputTokens != 25 ||
		got.CacheReadTokens != 86021 || got.CacheWriteTokens != 25483 ||
		got.OutputTokens != 901 || got.ReasoningTokens == nil || *got.ReasoningTokens != 251 {
		t.Fatalf("first tokens = %+v", got)
	}
	if got := result.Events[1].Tokens; got.InputTokens != 8471 || got.UncachedInputTokens != 3975 ||
		got.CacheReadTokens != 3979 || got.CacheWriteTokens != 517 ||
		got.OutputTokens != 49 || got.ReasoningTokens == nil || *got.ReasoningTokens != 24 {
		t.Fatalf("delta tokens = %+v", got)
	}
	if first.SourceEventKey == "" || first.SourceEventKey == result.Events[1].SourceEventKey {
		t.Fatalf("event keys = %q/%q", first.SourceEventKey, result.Events[1].SourceEventKey)
	}
}

// TestParseCopilotTracksModelsIndependently catches one model switch replacing
// the baseline for another model in the same native session.
func TestParseCopilotTracksModelsIndependently(t *testing.T) {
	source := usageSource(domain.UsageSourceCopilotShutdown)
	record := jsonlRecord{Data: []byte(`{"type":"session.shutdown","data":{"modelMetrics":{"gpt-5-mini":{"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":60,"cacheWriteTokens":10,"reasoningTokens":5}},"claude-haiku-4.5":{"usage":{"inputTokens":40,"outputTokens":8,"cacheReadTokens":20,"cacheWriteTokens":5,"reasoningTokens":2}}}}}`)}
	result := parseRecords(source, []jsonlRecord{record}, 200, time.Unix(1700000000, 0).UTC())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("events = %+v, want one event per model", result.Events)
	}
	state := parserStateFromResult(t, result, domain.UsageSourceCopilotShutdown)
	if len(state.Copilot.Models) != 2 || state.Copilot.Models["gpt-5-mini"].InputTokens != 100 {
		t.Fatalf("state = %+v", state.Copilot)
	}
}

func TestParseCopilotAssistantMessageOutputTokens(t *testing.T) {
	source := usageSource(domain.UsageSourceCopilotShutdown)
	records := []jsonlRecord{
		{Data: []byte(`{"type":"assistant.message","id":"message-1","data":{"model":"gpt-5-mini","outputTokens":944}}`)},
		{Data: []byte(`{"type":"assistant.message","id":"message-2","data":{"model":"gpt-5-mini","outputTokens":17}}`)},
	}
	result := parseRecords(source, records, 200, time.Unix(1700000000, 0).UTC())
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("events = %+v, want one usage event per assistant message", result.Events)
	}
	if got := result.Events[0].Tokens; got.InputTokens != 0 || got.OutputTokens != 944 || got.ReasoningTokens != nil {
		t.Fatalf("first tokens = %+v", got)
	}
	if got := result.Events[1].Tokens; got.InputTokens != 0 || got.OutputTokens != 17 || got.ReasoningTokens != nil {
		t.Fatalf("second tokens = %+v", got)
	}
	if result.Events[0].SourceEventKey == "" || result.Events[0].SourceEventKey == result.Events[1].SourceEventKey {
		t.Fatalf("event keys = %q/%q", result.Events[0].SourceEventKey, result.Events[1].SourceEventKey)
	}
}

func TestParseCopilotPrefersShutdownRollupOverAssistantMessages(t *testing.T) {
	source := usageSource(domain.UsageSourceCopilotShutdown)
	records := []jsonlRecord{
		{Data: []byte(`{"type":"assistant.message","id":"message-1","data":{"model":"gpt-5-mini","outputTokens":17}}`)},
		{Data: copilotShutdownLine("gpt-5-mini", 100, 60, 10, 17, 5)},
	}
	result := parseRecords(source, records, 200, time.Unix(1700000000, 0).UTC())
	if result.err != nil || len(result.Events) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if got := result.Events[0].Tokens; got.InputTokens != 100 || got.OutputTokens != 17 || got.ReasoningTokens == nil || *got.ReasoningTokens != 5 {
		t.Fatalf("tokens = %+v", got)
	}
}

func TestParseCopilotLaterShutdownSuppressesAlreadyParsedAssistantMessage(t *testing.T) {
	source := usageSource(domain.UsageSourceCopilotShutdown)
	first := parseRecords(source, []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"assistant.message","id":"message-1","data":{"model":"gpt-5-mini","outputTokens":17}}`)},
	}, 100, time.Unix(1700000000, 0).UTC())
	if first.err != nil || len(first.Events) != 1 {
		t.Fatalf("first result = %+v", first)
	}

	source.Source.ParserStateJSON = first.Cursor.ParserStateJSON
	second := parseRecords(source, []jsonlRecord{
		{Offset: 100, Data: copilotShutdownLine("gpt-5-mini", 100, 60, 10, 17, 5)},
	}, 200, time.Unix(1700000001, 0).UTC())
	if second.err != nil || len(second.Events) != 1 {
		t.Fatalf("second result = %+v", second)
	}
	if got := second.Events[0].Tokens; got.InputTokens != 100 || got.OutputTokens != 0 ||
		got.ReasoningTokens == nil || *got.ReasoningTokens != 0 {
		t.Fatalf("shutdown delta tokens = %+v", got)
	}
}

// TestParseCopilotCounterRegressionResetsBaseline catches negative usage deltas
// after Copilot starts a new cumulative epoch.
func TestParseCopilotCounterRegressionResetsBaseline(t *testing.T) {
	source := usageSource(domain.UsageSourceCopilotShutdown)
	first := parseRecords(source, []jsonlRecord{{Data: copilotShutdownLine("gpt-5-mini", 100, 60, 10, 20, 5)}}, 100, time.Unix(1700000000, 0).UTC())
	if first.err != nil || len(first.Events) != 1 {
		t.Fatalf("first parse = %+v", first)
	}
	source.Source.ParserStateJSON = first.Cursor.ParserStateJSON
	reset := parseRecords(source, []jsonlRecord{{Data: copilotShutdownLine("gpt-5-mini", 10, 5, 0, 2, 1)}}, 200, time.Unix(1700000001, 0).UTC())
	if reset.err != nil {
		t.Fatal(reset.err)
	}
	if len(reset.Events) != 0 || reset.Cursor.AnomalyCount != 1 ||
		reset.Cursor.LastErrorCode != domain.UsageErrorNonMonotonicCumulativeUsage {
		t.Fatalf("reset result = %+v", reset)
	}
	state := parserStateFromResult(t, reset, domain.UsageSourceCopilotShutdown)
	if state.Copilot.Models["gpt-5-mini"].InputTokens != 10 {
		t.Fatalf("reset state = %+v", state.Copilot)
	}
}

func copilotShutdownLine(model string, input, cacheRead, cacheWrite, output, reasoning int64) []byte {
	return []byte(`{"type":"session.shutdown","data":{"modelMetrics":{"` + model + `":{"usage":{"inputTokens":` +
		intString(input) + `,"outputTokens":` + intString(output) + `,"cacheReadTokens":` + intString(cacheRead) +
		`,"cacheWriteTokens":` + intString(cacheWrite) + `,"reasoningTokens":` + intString(reasoning) + `}}}}}`)
}

func intString(value int64) string {
	return fmt.Sprintf("%d", value)
}
