package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestParseClaudeFinalUsageAndSkipMainSidechain(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	source := usageSource(domain.UsageSourceClaudeMain)
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"assistant","isSidechain":true,"uuid":"side","timestamp":"2026-07-01T10:00:00Z","message":{"id":"msg-side","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5}}}`)},
		{Offset: 300, Data: []byte(`{"type":"assistant","isSidechain":false,"uuid":"main","timestamp":"2026-07-01T10:01:00Z","message":{"id":"msg-main","model":"claude-x","stop_reason":"tool_use","usage":{"input_tokens":10,"cache_creation_input_tokens":3,"cache_read_input_tokens":7,"output_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":1}}}}`)},
		{Offset: 600, Data: []byte(`{"type":"assistant","message":{"id":"stream","model":"claude-x","stop_reason":null,"usage":{"input_tokens":100,"output_tokens":20}}}`)},
	}

	result := parseRecords(source, records, 700, now)
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	got := result.Events[0]
	if tokenValue(got.Tokens.InputTokens) != 20 || tokenValue(got.Tokens.UncachedInputTokens) != 13 ||
		tokenValue(got.Tokens.CachedInputTokens) != 7 ||
		tokenValue(got.Tokens.OutputTokens) != 4 {
		t.Fatalf("tokens = %+v", got.Tokens)
	}
	if providerUsageTokens(t, got.ProviderUsageJSON, "input_tokens") != 10 ||
		providerUsageTokens(t, got.ProviderUsageJSON, "cache_creation_input_tokens") != 3 ||
		providerUsageTokens(t, got.ProviderUsageJSON, "cache_creation", "ephemeral_5m_input_tokens") != 2 ||
		providerUsageTokens(t, got.ProviderUsageJSON, "cache_creation", "ephemeral_1h_input_tokens") != 1 {
		t.Fatalf("provider usage = %s", got.ProviderUsageJSON)
	}
	if got.MeasurementKind != domain.UsageMeasurementNativeReported {
		t.Fatalf("measurement kind = %q, want native_reported", got.MeasurementKind)
	}
	if got.ModelID != "claude-x" {
		t.Fatalf("event = %+v", got)
	}
}

// The stored object is the CLI's usage record verbatim, so a field Anthropic
// adds after this code was written still reaches pricing and auditing.
func TestParseClaudeRetainsUnknownProviderUsageFieldsWithinTheBound(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeMain)
	result := parseRecords(source, []jsonlRecord{{Data: []byte(
		`{"type":"assistant","uuid":"one","message":{"id":"msg-1","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2,"output_tokens_details":{"thinking_tokens":1},"server_tool_use":{"web_search_requests":2},"service_tier":"standard"}}}`,
	)}}, 400, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	stored := result.Events[0].ProviderUsageJSON
	if providerUsageTokens(t, stored, "output_tokens_details", "thinking_tokens") != 1 ||
		providerUsageTokens(t, stored, "server_tool_use", "web_search_requests") != 2 {
		t.Fatalf("provider usage = %s, want unknown fields retained", stored)
	}
	if !strings.Contains(stored, `"service_tier":"standard"`) {
		t.Fatalf("provider usage = %s, want the object compacted verbatim", stored)
	}

	// An object far larger than the counter record the providers document is
	// dropped rather than truncated: an absent object is honest, a partial one
	// would silently misprice.
	oversized := strings.Repeat("x", maxProviderUsageBytes)
	huge := parseRecords(source, []jsonlRecord{{Data: []byte(fmt.Sprintf(
		`{"type":"assistant","uuid":"two","message":{"id":"msg-2","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2,"padding":%q}}}`,
		oversized,
	))}}, 40_000, time.Unix(1700000000, 0).UTC())
	if len(huge.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(huge.Events))
	}
	if huge.Events[0].ProviderUsageJSON != "" {
		t.Fatalf("oversized provider usage was stored: %d bytes", len(huge.Events[0].ProviderUsageJSON))
	}
	if tokenValue(huge.Events[0].Tokens.InputTokens) != 8 {
		t.Fatalf("dropping the object must not drop the neutral counters: %+v", huge.Events[0].Tokens)
	}
}

// The transcript writes the same write-once column as the hook, so it earns the
// same whitelist. A routing string AO cannot name is dropped rather than stored:
// unattributed is repairable, and a wrong attribution — priced against nothing,
// invisible to every repair path — is not.
func TestParseClaudeProviderPrecedenceAndRetention(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeMain)
	source.ProviderHint = "anthropic"
	records := []jsonlRecord{
		{Data: []byte(`{"type":"assistant","provider":" record-provider ","uuid":"one","message":{"id":"msg-1","provider":" message-provider ","model":" claude-a ","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`)},
		{Data: []byte(`{"type":"assistant","uuid":"two","message":{"id":"msg-2","model":"","stop_reason":"end_turn","usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}`)},
		{Data: []byte(`{"type":"assistant","provider":" Z.AI ","uuid":"three","message":{"id":"msg-3","model":"claude-b","stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":4}}}`)},
	}

	result := parseRecords(source, records, 500, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 3 {
		t.Fatalf("events = %+v", result.Events)
	}
	// The first two records name nothing AO recognises, so the whitelisted hook
	// hint stands. The third names a route AO does know, canonicalised, and it
	// takes precedence and persists to the next chunk.
	if result.Events[0].BillingProviderID != "anthropic" || result.Events[0].ModelID != "claude-a" ||
		result.Events[1].BillingProviderID != "anthropic" || result.Events[1].ModelID != "claude-a" ||
		result.Events[2].BillingProviderID != "zai" || result.Events[2].ModelID != "claude-b" {
		t.Fatalf("billing provider/model sequence = %+v", result.Events)
	}
	for index, event := range result.Events {
		if event.BillingProviderSource != domain.UsageBillingProviderObserved {
			t.Fatalf("event %d attribution source = %q", index, event.BillingProviderSource)
		}
	}
	state := parserStateFromResult(t, result, domain.UsageSourceClaudeMain)
	if state.Claude.Provider != "zai" {
		t.Fatalf("retained provider = %q", state.Claude.Provider)
	}
}

func TestParseClaudeProviderFallsBackToTrustedHintThenUnknown(t *testing.T) {
	record := jsonlRecord{Data: []byte(`{"type":"assistant","uuid":"one","message":{"id":"msg-1","model":"claude-a","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`)}
	for _, test := range []struct {
		name string
		hint string
		want string
	}{
		{name: "trusted hook hint", hint: "zai", want: "zai"},
		{name: "z.ai alias", hint: "z.ai", want: "zai"},
		{name: "unattributed", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := usageSource(domain.UsageSourceClaudeMain)
			source.ProviderHint = test.hint
			result := parseRecords(source, []jsonlRecord{record}, 100, time.Unix(1700000000, 0).UTC())
			if len(result.Events) != 1 || result.Events[0].BillingProviderID != test.want {
				t.Fatalf("events = %+v, want billing provider %q", result.Events, test.want)
			}
		})
	}
}

func TestParseClaudeCacheWriteSplitsAreCapturedOrRejected(t *testing.T) {
	tests := []struct {
		name          string
		cacheCreation string
		want5m        *int64
		want1h        *int64
		wantEvents    int
		wantAnomaly   int64
	}{
		{name: "absent", wantEvents: 1},
		{name: "explicit zero", cacheCreation: `,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}`, want5m: int64Ptr(0), want1h: int64Ptr(0), wantEvents: 1},
		{name: "valid", cacheCreation: `,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":1}`, want5m: int64Ptr(2), want1h: int64Ptr(1), wantEvents: 1},
		// An absent TTL member stays absent rather than becoming a known zero,
		// and splits that do not sum to the generic total are still persisted:
		// pricing treats either as an unknown input cost rather than rejecting
		// the tokens.
		{name: "missing member", cacheCreation: `,"cache_creation":{"ephemeral_5m_input_tokens":3}`, want5m: int64Ptr(3), wantEvents: 1},
		{name: "negative", cacheCreation: `,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":-1}`, wantAnomaly: 1},
		{name: "sum below generic total", cacheCreation: `,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":1}`, want5m: int64Ptr(1), want1h: int64Ptr(1), wantEvents: 1},
		{name: "sum above generic total", cacheCreation: `,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":2}`, wantAnomaly: 1},
		{name: "malformed member", cacheCreation: `,"cache_creation":{"ephemeral_5m_input_tokens":"three","ephemeral_1h_input_tokens":0}`, wantAnomaly: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			generic := int64(3)
			if test.name == "explicit zero" {
				generic = 0
			}
			record := jsonlRecord{Data: []byte(fmt.Sprintf(
				`{"type":"assistant","uuid":"one","message":{"id":"msg-1","model":"claude-a","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":%d,"cache_read_input_tokens":0,"output_tokens":2%s}}}`,
				generic, test.cacheCreation,
			))}
			result := parseRecords(usageSource(domain.UsageSourceClaudeMain), []jsonlRecord{record}, 100, time.Unix(1700000000, 0).UTC())
			if len(result.Events) != test.wantEvents || result.Cursor.AnomalyCount != test.wantAnomaly {
				t.Fatalf("events=%+v anomalies=%d, want %d events and %d anomalies",
					result.Events, result.Cursor.AnomalyCount, test.wantEvents, test.wantAnomaly)
			}
			if test.wantEvents == 0 {
				return
			}
			stored := result.Events[0].ProviderUsageJSON
			got5m := providerUsageValue(t, stored, "cache_creation", "ephemeral_5m_input_tokens")
			got1h := providerUsageValue(t, stored, "cache_creation", "ephemeral_1h_input_tokens")
			if !equalInt64Ptr(got5m, test.want5m) || !equalInt64Ptr(got1h, test.want1h) {
				t.Fatalf("splits = %s, want %v/%v", stored, test.want5m, test.want1h)
			}
		})
	}
}

func equalInt64Ptr(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func TestParseClaudeSubagentIncludesSidechainTranscript(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeSubagent)
	record := jsonlRecord{Data: []byte(`{"type":"assistant","isSidechain":true,"uuid":"sub","message":{"id":"msg-sub","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`)}
	result := parseRecords(source, []jsonlRecord{record}, 200, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want subagent usage", len(result.Events))
	}
}

func TestParseClaudeSkipsSyntheticControlResponses(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeMain)
	source.InitialModelID = "claude-fallback"
	records := []jsonlRecord{
		{Data: []byte(`{"type":"assistant","uuid":"synthetic","message":{"id":"synthetic","model":"<synthetic>","stop_reason":"stop_sequence","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`)},
		{Data: []byte(`{"type":"assistant","uuid":"real","message":{"id":"real","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`)},
	}

	result := parseRecords(source, records, 400, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 1 || result.Events[0].ModelID != "claude-fallback" {
		t.Fatalf("events = %+v, want only the real fallback-model event", result.Events)
	}
	state := parserStateFromResult(t, result, domain.UsageSourceClaudeMain)
	if state.Claude.ModelID != "claude-fallback" {
		t.Fatalf("Claude parser state model = %q, want fallback model", state.Claude.ModelID)
	}
}

func TestParseClaudeReferenceUsageDeduplicatesLogicalResponses(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeMain)
	type response struct {
		id                            string
		direct, creation, cached, out int64
	}
	responses := []response{
		{id: "msg-1", direct: 10, creation: 34983, out: 547},
		{id: "msg-2", direct: 10, creation: 35619, out: 481},
		{id: "msg-3", direct: 10, creation: 529, cached: 35619, out: 3602},
		{id: "msg-4", direct: 10, creation: 2078, cached: 39757, out: 20363},
	}
	var records []jsonlRecord
	for index, response := range responses {
		line := fmt.Sprintf(`{"type":"assistant","uuid":"physical-%d","timestamp":"2026-08-20T10:00:0%dZ","message":{"id":%q,"model":"claude-sonnet","stop_reason":"end_turn","usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":%d}}}}`, index, index, response.id, response.direct, response.creation, response.cached, response.out, response.creation)
		records = append(records,
			jsonlRecord{Offset: int64(index * 200), Data: []byte(line)},
			jsonlRecord{Offset: int64(index*200 + 100), Data: []byte(strings.Replace(line, fmt.Sprintf("physical-%d", index), fmt.Sprintf("duplicate-%d", index), 1))},
		)
	}

	result := parseRecords(source, records, 1600, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 4 || result.Cursor.AnomalyCount != 0 {
		t.Fatalf("result = %+v, want four logical events", result)
	}
	assertCanonicalEventTotals(t, result.Events, 148625, 75376, 73249, 24993)
	var direct, creation, creation5m, creation1h int64
	for _, event := range result.Events {
		if event.ProviderID != domain.UsageProviderAnthropic || event.ProviderUsageJSON == "" {
			t.Fatalf("provider mapping = %+v", event)
		}
		direct += providerUsageTokens(t, event.ProviderUsageJSON, "input_tokens")
		creation += providerUsageTokens(t, event.ProviderUsageJSON, "cache_creation_input_tokens")
		creation5m += providerUsageTokens(t, event.ProviderUsageJSON, "cache_creation", "ephemeral_5m_input_tokens")
		creation1h += providerUsageTokens(t, event.ProviderUsageJSON, "cache_creation", "ephemeral_1h_input_tokens")
	}
	if direct != 40 || creation != 73209 || creation5m != 0 || creation1h != 73209 {
		t.Fatalf("Anthropic usage = direct:%d creation:%d 5m:%d 1h:%d", direct, creation, creation5m, creation1h)
	}
}

func TestParseCodexCumulativeDeltasAndRepeats(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	source := usageSource(domain.UsageSourceCodexRollout)
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"session_meta","payload":{"model_provider":"openai"}}`)},
		{Offset: 100, Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-5.6"}}`)},
		{Offset: 200, Data: codexTokenLine("2026-07-01T10:00:00Z", 100, 60, 10, 20, 5)},
		{Offset: 300, Data: codexTokenLine("2026-07-01T10:00:01Z", 100, 60, 10, 20, 5)},
		{Offset: 400, Data: codexTokenLine("2026-07-01T10:00:02Z", 160, 90, 10, 35, 8)},
	}
	result := parseRecords(source, records, 500, now)
	if len(result.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(result.Events))
	}
	if got := result.Events[0].Tokens; tokenValue(got.InputTokens) != 100 || tokenValue(got.UncachedInputTokens) != 40 ||
		tokenValue(got.CachedInputTokens) != 60 || tokenValue(got.OutputTokens) != 20 {
		t.Fatalf("first tokens = %+v", got)
	}
	if got := result.Events[1].Tokens; tokenValue(got.InputTokens) != 60 || tokenValue(got.UncachedInputTokens) != 30 ||
		tokenValue(got.CachedInputTokens) != 30 || tokenValue(got.OutputTokens) != 15 {
		t.Fatalf("delta tokens = %+v", got)
	}
	// Each event stores payload.info exactly as emitted. These records carry only
	// cumulative counters, so the stored object holds the running totals the
	// event was derived from, not the derived delta.
	for index, wantCumulativeInput := range []int64{100, 160} {
		stored := result.Events[index].ProviderUsageJSON
		if providerUsageTokens(t, stored, "total_token_usage", "input_tokens") != wantCumulativeInput {
			t.Fatalf("event %d provider usage = %s, want cumulative input %d", index, stored, wantCumulativeInput)
		}
		if providerUsageValue(t, stored, "last_token_usage") != nil {
			t.Fatalf("event %d provider usage invented a per-event vector: %s", index, stored)
		}
	}
	state := parserStateFromResult(t, result, domain.UsageSourceCodexRollout)
	if state.Codex.Baseline.InputTokens != 160 || state.Codex.ModelID != "gpt-5.6" || state.Codex.Provider != "openai" {
		t.Fatalf("parser state = %+v", state.Codex)
	}
}

// Break caught: rejecting an oversized native object must not leave behind an
// AO-only fragment that looks like the complete provider usage record.
func TestParseCodexOversizedCumulativeUsageStaysAbsent(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	padding := strings.Repeat("x", maxProviderUsageBytes)
	records := []jsonlRecord{
		{Data: codexTokenLine("2026-07-01T10:00:00Z", 100, 60, 10, 20, 5)},
		{Data: []byte(fmt.Sprintf(
			`{"timestamp":"2026-07-01T10:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":160,"cached_input_tokens":90,"cache_write_input_tokens":15,"output_tokens":35,"reasoning_output_tokens":8,"total_tokens":195},"padding":%q}}}`,
			padding,
		))},
	}

	result := parseRecords(source, records, 20_000, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(result.Events))
	}
	got := result.Events[1]
	if got.ProviderUsageJSON != "" {
		t.Fatalf("oversized provider usage became a partial object: %s", got.ProviderUsageJSON)
	}
	if tokenValue(got.Tokens.InputTokens) != 60 || tokenValue(got.Tokens.CachedInputTokens) != 30 ||
		tokenValue(got.Tokens.UncachedInputTokens) != 30 || tokenValue(got.Tokens.OutputTokens) != 15 {
		t.Fatalf("dropping the object must not drop the neutral delta: %+v", got.Tokens)
	}
}

func TestParseCodexSessionMetaProviderAppliesOnlyToSubsequentEvents(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	result := parseRecords(source, []jsonlRecord{
		{Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-a"}}`)},
		{Data: codexTokenLine("2026-07-01T10:00:00Z", 10, 5, 0, 2, 1)},
		{Data: []byte(`{"type":"session_meta","payload":{"model_provider":" openai "}}`)},
		{Data: codexTokenLine("2026-07-01T10:01:00Z", 20, 10, 0, 4, 2)},
		{Data: []byte(`{"type":"session_meta","payload":{"model_provider":" azure "}}`)},
		{Data: codexTokenLine("2026-07-01T10:02:00Z", 30, 15, 0, 6, 3)},
	}, 500, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 3 ||
		result.Events[0].BillingProviderID != "" ||
		result.Events[1].BillingProviderID != "openai" ||
		result.Events[2].BillingProviderID != "azure" {
		t.Fatalf("billing provider sequence = %+v", result.Events)
	}
	state := parserStateFromResult(t, result, domain.UsageSourceCodexRollout)
	if state.Codex.Provider != "azure" {
		t.Fatalf("retained provider = %q", state.Codex.Provider)
	}
}

func TestParseCodexReferenceUsageReconcilesLastAndCumulativeTotals(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	records := []jsonlRecord{
		{Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-5.5"}}`)},
		{Data: codexTokenLineWithLast("2026-08-20T10:00:00Z", codexTokenVector{InputTokens: 16568, CachedInputTokens: 4480, OutputTokens: 638, ReasoningOutputTokens: 148, TotalTokens: 17206}, codexTokenVector{InputTokens: 16568, CachedInputTokens: 4480, OutputTokens: 638, ReasoningOutputTokens: 148, TotalTokens: 17206})},
		{Data: codexTokenLineWithLast("2026-08-20T10:01:00Z", codexTokenVector{InputTokens: 22673, CachedInputTokens: 4480, OutputTokens: 1895, ReasoningOutputTokens: 516, TotalTokens: 24568}, codexTokenVector{InputTokens: 39241, CachedInputTokens: 8960, OutputTokens: 2533, ReasoningOutputTokens: 664, TotalTokens: 41774})},
		{Data: codexTokenLineWithLast("2026-08-20T10:02:00Z", codexTokenVector{InputTokens: 24325, CachedInputTokens: 21888, OutputTokens: 2525, ReasoningOutputTokens: 516, TotalTokens: 26850}, codexTokenVector{InputTokens: 63566, CachedInputTokens: 30848, OutputTokens: 5058, ReasoningOutputTokens: 1180, TotalTokens: 68624})},
	}

	result := parseRecords(source, records, 1000, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 3 || result.Cursor.AnomalyCount != 0 {
		t.Fatalf("result = %+v, want three reconciled events", result)
	}
	assertCanonicalEventTotals(t, result.Events, 63566, 30848, 32718, 5058)
	// last_token_usage is the per-event vector, so the reasoning and cache-write
	// subsets pricing needs are recoverable from each stored object.
	var reasoning, cacheWrite int64
	for _, event := range result.Events {
		if event.ProviderID != domain.UsageProviderOpenAI || event.ProviderUsageJSON == "" {
			t.Fatalf("provider mapping = %+v", event)
		}
		reasoning += providerUsageTokens(t, event.ProviderUsageJSON, "last_token_usage", "reasoning_output_tokens")
		cacheWrite += providerUsageTokens(t, event.ProviderUsageJSON, "last_token_usage", "cache_write_input_tokens")
	}
	if reasoning != 1180 || cacheWrite != 0 {
		t.Fatalf("OpenAI usage = reasoning:%d cache-write:%d", reasoning, cacheWrite)
	}
	if last := result.Events[2].ProviderUsageJSON; providerUsageTokens(t, last, "total_token_usage", "total_tokens") != 68624 {
		t.Fatalf("cumulative counters must survive beside the per-event vector: %s", last)
	}
}

func TestParseCodexOptionalReportedTotalDoesNotDropDeltas(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	records := []jsonlRecord{
		{Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-5.6"}}`)},
		{Data: codexTokenLineWithTotal("2026-08-20T10:00:00Z", codexTokenVector{InputTokens: 100, CachedInputTokens: 60, OutputTokens: 20, ReasoningOutputTokens: 5, TotalTokens: 120})},
		{Data: codexTokenLineWithTotal("2026-08-20T10:01:00Z", codexTokenVector{InputTokens: 160, CachedInputTokens: 90, OutputTokens: 35, ReasoningOutputTokens: 8})},
		{Data: codexTokenLineWithTotal("2026-08-20T10:02:00Z", codexTokenVector{InputTokens: 200, CachedInputTokens: 100, OutputTokens: 50, ReasoningOutputTokens: 10, TotalTokens: 250})},
	}

	result := parseRecords(source, records, 1000, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 3 || result.Cursor.AnomalyCount != 0 {
		t.Fatalf("result = %+v, want three clean events", result)
	}
	assertCanonicalEventTotals(t, result.Events, 200, 100, 100, 50)
	// An optional field the CLI omitted stays absent rather than being
	// manufactured as a zero.
	if first := providerUsageValue(t, result.Events[0].ProviderUsageJSON, "total_token_usage", "total_tokens"); first == nil || *first != 120 {
		t.Fatalf("first reported total = %v, want 120", first)
	}
	if middle := providerUsageValue(t, result.Events[1].ProviderUsageJSON, "total_token_usage", "total_tokens"); middle == nil || *middle != 0 {
		t.Fatalf("omitted reported total = %v, want the vector's own zero", middle)
	}
	if last := providerUsageValue(t, result.Events[2].ProviderUsageJSON, "total_token_usage", "total_tokens"); last == nil || *last != 250 {
		t.Fatalf("reported total after missing value = %v, want the cumulative 250", last)
	}
	state := parserStateFromResult(t, result, domain.UsageSourceCodexRollout)
	if state.Codex.Baseline.TotalTokens != 250 {
		t.Fatalf("baseline = %+v, want latest cumulative total", state.Codex.Baseline)
	}
}

func TestParseCodexRejectedDeltaDoesNotAdvanceBaseline(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	records := []jsonlRecord{
		{Data: codexTokenLineWithTotal("2026-08-20T10:00:00Z", codexTokenVector{InputTokens: 100, CachedInputTokens: 90, OutputTokens: 20, ReasoningOutputTokens: 5, TotalTokens: 120})},
		{Data: codexTokenLineWithTotal("2026-08-20T10:01:00Z", codexTokenVector{InputTokens: 110, CachedInputTokens: 105, OutputTokens: 21, ReasoningOutputTokens: 5, TotalTokens: 131})},
		{Data: codexTokenLineWithTotal("2026-08-20T10:02:00Z", codexTokenVector{InputTokens: 120, CachedInputTokens: 100, OutputTokens: 25, ReasoningOutputTokens: 6, TotalTokens: 145})},
	}

	result := parseRecords(source, records, 1000, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 2 || result.Cursor.AnomalyCount != 1 {
		t.Fatalf("result = %+v, want the invalid middle delta skipped", result)
	}
	assertCanonicalEventTotals(t, result.Events, 120, 100, 20, 25)
	state := parserStateFromResult(t, result, domain.UsageSourceCodexRollout)
	if state.Codex.Baseline.InputTokens != 120 || state.Codex.Baseline.CachedInputTokens != 100 {
		t.Fatalf("baseline = %+v, want recovery from the last valid baseline", state.Codex.Baseline)
	}
}

func TestParseCodexLastMismatchPrefersLastUsage(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	records := []jsonlRecord{
		{Data: codexTokenLineWithTotal("2026-08-20T10:00:00Z", codexTokenVector{InputTokens: 100, CachedInputTokens: 60, OutputTokens: 20, ReasoningOutputTokens: 5, TotalTokens: 120})},
		{Data: codexTokenLineWithLast(
			"2026-08-20T10:01:00Z",
			codexTokenVector{InputTokens: 55, CachedInputTokens: 25, OutputTokens: 14, ReasoningOutputTokens: 2, TotalTokens: 69},
			codexTokenVector{InputTokens: 160, CachedInputTokens: 90, OutputTokens: 35, ReasoningOutputTokens: 8, TotalTokens: 195},
		)},
	}

	result := parseRecords(source, records, 1000, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 2 || result.Cursor.AnomalyCount != 1 {
		t.Fatalf("result = %+v, want last usage persisted with an integrity anomaly", result)
	}
	got := result.Events[1]
	if tokenValue(got.Tokens.InputTokens) != 55 || tokenValue(got.Tokens.CachedInputTokens) != 25 ||
		tokenValue(got.Tokens.UncachedInputTokens) != 30 || tokenValue(got.Tokens.OutputTokens) != 14 {
		t.Fatalf("mismatched event tokens = %+v, want last_token_usage", got.Tokens)
	}
	if providerUsageTokens(t, got.ProviderUsageJSON, "last_token_usage", "reasoning_output_tokens") != 2 ||
		providerUsageTokens(t, got.ProviderUsageJSON, "last_token_usage", "total_tokens") != 69 {
		t.Fatalf("mismatched event usage = %s, want last_token_usage", got.ProviderUsageJSON)
	}
	state := parserStateFromResult(t, result, domain.UsageSourceCodexRollout)
	if state.Codex.Baseline.InputTokens != 160 || state.Codex.Baseline.TotalTokens != 195 {
		t.Fatalf("baseline = %+v, want cumulative total", state.Codex.Baseline)
	}
}

func TestParseCodexInvalidLastAdvancesCumulativeBaseline(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	records := []jsonlRecord{
		{Data: codexTokenLineWithTotal("2026-08-20T10:00:00Z", codexTokenVector{InputTokens: 100, CachedInputTokens: 60, OutputTokens: 20, ReasoningOutputTokens: 5, TotalTokens: 120})},
		{Data: codexTokenLineWithLast(
			"2026-08-20T10:01:00Z",
			codexTokenVector{InputTokens: 60, CachedInputTokens: 70, OutputTokens: 15, ReasoningOutputTokens: 3, TotalTokens: 75},
			codexTokenVector{InputTokens: 160, CachedInputTokens: 90, OutputTokens: 35, ReasoningOutputTokens: 8, TotalTokens: 195},
		)},
		{Data: codexTokenLineWithTotal("2026-08-20T10:02:00Z", codexTokenVector{InputTokens: 200, CachedInputTokens: 100, OutputTokens: 50, ReasoningOutputTokens: 10, TotalTokens: 250})},
	}

	result := parseRecords(source, records, 1000, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 2 || result.Cursor.AnomalyCount != 1 {
		t.Fatalf("result = %+v, want invalid last usage skipped with one anomaly", result)
	}
	got := result.Events[1]
	if tokenValue(got.Tokens.InputTokens) != 40 || tokenValue(got.Tokens.CachedInputTokens) != 10 ||
		tokenValue(got.Tokens.UncachedInputTokens) != 30 || tokenValue(got.Tokens.OutputTokens) != 15 {
		t.Fatalf("post-anomaly tokens = %+v, want delta from the skipped record's cumulative total", got.Tokens)
	}
	state := parserStateFromResult(t, result, domain.UsageSourceCodexRollout)
	if state.Codex.Baseline.InputTokens != 200 || state.Codex.Baseline.TotalTokens != 250 {
		t.Fatalf("baseline = %+v, want latest cumulative total", state.Codex.Baseline)
	}
}

func TestParseCodexCounterResetNeverEmitsNegativeUsage(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	source := usageSource(domain.UsageSourceCodexRollout)
	source.Source.ParserStateJSON = `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{"input_tokens":500,"cached_input_tokens":300,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`
	result := parseRecords(source, []jsonlRecord{{
		Offset: 20,
		Data:   codexTokenLine("2026-07-01T10:00:00Z", 10, 5, 0, 2, 1),
	}}, 100, now)
	if len(result.Events) != 0 {
		t.Fatalf("events = %+v, want no negative delta event", result.Events)
	}
	state := parserStateFromResult(t, result, domain.UsageSourceCodexRollout)
	if result.Cursor.AnomalyCount != 1 ||
		result.Cursor.LastErrorCode != domain.UsageErrorNonMonotonicCumulativeUsage ||
		state.Codex.Baseline.InputTokens != 10 {
		t.Fatalf("cursor = %+v", result.Cursor)
	}
}

func TestParseCodexContextFillStartsNewEpochWithoutAnomaly(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	source := usageSource(domain.UsageSourceCodexRollout)
	source.Source.ParserStateJSON = `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{"input_tokens":500,"cached_input_tokens":300,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":550},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`
	records := []jsonlRecord{
		{Offset: 20, Data: codexContextFillLine("2026-07-01T10:00:00Z", 258400)},
		{Offset: 40, Data: codexTokenLine("2026-07-01T10:00:01Z", 10, 5, 0, 2, 1)},
	}

	result := parseRecords(source, records, 100, now)
	if result.Cursor.AnomalyCount != 0 || result.Cursor.LastErrorCode != "" {
		t.Fatalf("cursor = %+v, want clean context-fill transition", result.Cursor)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %+v, want one event from the new epoch", result.Events)
	}
	if got := result.Events[0].Tokens; tokenValue(got.InputTokens) != 10 || tokenValue(got.CachedInputTokens) != 5 ||
		tokenValue(got.UncachedInputTokens) != 5 || tokenValue(got.OutputTokens) != 2 {
		t.Fatalf("new epoch tokens = %+v", got)
	}
}

func TestDecodeParserStateTreatsWhitespaceOnlyObjectAsFreshState(t *testing.T) {
	tests := []struct {
		name string
		kind domain.UsageSourceKind
		raw  string
	}{
		{name: "claude spaces", kind: domain.UsageSourceClaudeMain, raw: "{ }"},
		{name: "claude newline", kind: domain.UsageSourceClaudeSubagent, raw: "{\n\t}"},
		{name: "codex spaces", kind: domain.UsageSourceCodexRollout, raw: "{   }"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := decodeParserState(domain.UsageSourceRecord{
				Kind:            test.kind,
				ParserStateJSON: test.raw,
			})
			if err != nil {
				t.Fatalf("decode semantically empty parser state: %v", err)
			}
			if state.Version != parserStateVersion || state.SourceKind != test.kind {
				t.Fatalf("state = %+v, want fresh %s state", state, test.kind)
			}
			switch test.kind {
			case domain.UsageSourceCodexRollout:
				if state.Codex == nil || state.Claude != nil {
					t.Fatalf("Codex state = %+v", state)
				}
			default:
				if state.Claude == nil || state.Codex != nil {
					t.Fatalf("Claude state = %+v", state)
				}
			}
		})
	}
}

// Break caught: a build before #2928 wrote the harness name — "claude-code",
// "openai" — into the parser state's "provider" key, and cleared it on decode
// for exactly that reason. Reusing the key for the billing route would stamp the
// harness onto every event newly ingested from such a source, marked observed
// and so beyond every repair path. The route persists under its own key; the
// retired one is still accepted and still discarded.
func TestDecodeParserStateIgnoresTheRetiredProviderField(t *testing.T) {
	tests := []struct {
		kind        domain.UsageSourceKind
		retired     string
		current     string
		wantRoute   string
		wantPersist string
	}{
		{
			kind:        domain.UsageSourceClaudeMain,
			retired:     `{"version":1,"source_kind":"claude_main","claude":{"model_id":"claude-test","provider":"claude-code"}}`,
			current:     `{"version":1,"source_kind":"claude_main","claude":{"model_id":"claude-test","billing_provider":"anthropic"}}`,
			wantRoute:   "anthropic",
			wantPersist: `"billing_provider":"anthropic"`,
		},
		{
			kind:        domain.UsageSourceCodexRollout,
			retired:     `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"provider":"openai","pending_spawn_call_ids":[],"discovered_child_ids":[]}}`,
			current:     `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"billing_provider":"openai","pending_spawn_call_ids":[],"discovered_child_ids":[]}}`,
			wantRoute:   "openai",
			wantPersist: `"billing_provider":"openai"`,
		},
	}
	for _, test := range tests {
		retired, err := decodeParserState(domain.UsageSourceRecord{Kind: test.kind, ParserStateJSON: test.retired})
		if err != nil {
			t.Fatalf("decode %s state with retired provider field: %v", test.kind, err)
		}
		if got := persistedRoute(retired); got != "" {
			t.Fatalf("%s read the retired provider key as a billing route: %q", test.kind, got)
		}
		current, err := decodeParserState(domain.UsageSourceRecord{Kind: test.kind, ParserStateJSON: test.current})
		if err != nil {
			t.Fatalf("decode %s state: %v", test.kind, err)
		}
		if got := persistedRoute(current); got != test.wantRoute {
			t.Fatalf("%s persisted route = %q, want %q", test.kind, got, test.wantRoute)
		}
		parsed := parseRecordsWithState(
			domain.UsageSourceContext{Source: domain.UsageSourceRecord{Kind: test.kind}},
			nil,
			0,
			time.Unix(1700000000, 0).UTC(),
			current,
		)
		if parsed.err != nil {
			t.Fatalf("encode normalized %s state: %v", test.kind, parsed.err)
		}
		if !strings.Contains(parsed.Cursor.ParserStateJSON, test.wantPersist) {
			t.Fatalf("normalized %s state dropped the route: %s", test.kind, parsed.Cursor.ParserStateJSON)
		}
		if strings.Contains(parsed.Cursor.ParserStateJSON, `"provider"`) {
			t.Fatalf("normalized %s state rewrote the retired key: %s", test.kind, parsed.Cursor.ParserStateJSON)
		}
	}
}

func persistedRoute(state *parserStateEnvelope) string {
	if state.Claude != nil {
		return state.Claude.Provider
	}
	if state.Codex != nil {
		return state.Codex.Provider
	}
	return ""
}

func TestDecodeParserStateValidatesV1Integrity(t *testing.T) {
	initial := `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`
	state, err := decodeParserState(domain.UsageSourceRecord{
		Kind:            domain.UsageSourceCodexRollout,
		ByteOffset:      12,
		ParserStateJSON: initial,
	})
	if err != nil {
		t.Fatalf("decode initial state: %v", err)
	}
	if state.Integrity == nil || state.Codex == nil {
		t.Fatalf("initial state = %+v", state)
	}

	digest := strings.Repeat("a", 64)
	valid := `{"version":1,"source_kind":"codex_rollout","integrity":{"checkpoint":{"end_offset":12,"byte_count":12,"sha256":"` + digest + `"}},"codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`
	if _, err := decodeParserState(domain.UsageSourceRecord{
		Kind:            domain.UsageSourceCodexRollout,
		ByteOffset:      12,
		ParserStateJSON: valid,
	}); err != nil {
		t.Fatalf("decode valid integrity state: %v", err)
	}

	invalid := []string{
		`{"version":1,"source_kind":"codex_rollout","integrity":{"checkpoint":{"end_offset":11,"byte_count":11,"sha256":"` + digest + `"}},"codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`,
		`{"version":1,"source_kind":"codex_rollout","integrity":{"checkpoint":{"end_offset":12,"byte_count":12,"sha256":"short"}},"codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`,
		`{"version":1,"source_kind":"codex_rollout","integrity":{"stable_tail":{"offset":12,"byte_count":4,"sha256":"` + digest + `","quiet_observations":2}},"codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`,
		`{"version":1,"source_kind":"codex_rollout","integrity":{"stable_tail":{"offset":12,"byte_count":4,"sha256":"` + digest + `","quiet_observations":0,"content":"private"}},"codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`,
	}
	for index, raw := range invalid {
		if _, err := decodeParserState(domain.UsageSourceRecord{
			Kind:            domain.UsageSourceCodexRollout,
			ByteOffset:      12,
			ParserStateJSON: raw,
		}); err == nil {
			t.Errorf("invalid state %d was accepted", index)
		}
	}
}

func TestDecodeParserStateValidatesCodexDirectParent(t *testing.T) {
	validChild := `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"native_session_id":"22222222-2222-4222-8222-222222222222","direct_parent_id":"11111111-1111-4111-8111-111111111111","pending_spawn_call_ids":[],"discovered_child_ids":[]}}`
	child := domain.UsageSourceRecord{
		Kind:            domain.UsageSourceCodexRollout,
		NativeSessionID: "22222222-2222-4222-8222-222222222222",
		SubagentID:      "22222222-2222-4222-8222-222222222222",
		ParserStateJSON: validChild,
	}
	state, err := decodeParserState(child)
	mustNoError(t, err, "decode child direct parent")
	parsed := parseRecordsWithState(
		domain.UsageSourceContext{Source: child},
		nil,
		0,
		time.Unix(1700000000, 0).UTC(),
		state,
	)
	if parsed.err != nil || !strings.Contains(
		parsed.Cursor.ParserStateJSON,
		`"native_session_id":"22222222-2222-4222-8222-222222222222"`,
	) || !strings.Contains(
		parsed.Cursor.ParserStateJSON,
		`"direct_parent_id":"11111111-1111-4111-8111-111111111111"`,
	) {
		t.Fatalf("persisted child state = %q, err=%v", parsed.Cursor.ParserStateJSON, parsed.err)
	}

	invalid := []struct {
		name   string
		source domain.UsageSourceRecord
	}{
		{
			name: "child mismatched native session",
			source: domain.UsageSourceRecord{
				Kind:            domain.UsageSourceCodexRollout,
				NativeSessionID: "33333333-3333-4333-8333-333333333333",
				SubagentID:      "33333333-3333-4333-8333-333333333333",
				ParserStateJSON: validChild,
			},
		},
		{
			name: "child missing direct parent",
			source: domain.UsageSourceRecord{
				Kind:            domain.UsageSourceCodexRollout,
				NativeSessionID: child.NativeSessionID,
				SubagentID:      child.SubagentID,
				ParserStateJSON: `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`,
			},
		},
		{
			name: "child invalid direct parent",
			source: domain.UsageSourceRecord{
				Kind:            domain.UsageSourceCodexRollout,
				NativeSessionID: child.NativeSessionID,
				SubagentID:      child.SubagentID,
				ParserStateJSON: `{"version":1,"source_kind":"codex_rollout","codex":{"baseline":{},"direct_parent_id":"not-a-thread","pending_spawn_call_ids":[],"discovered_child_ids":[]}}`,
			},
		},
		{
			name: "root has direct parent",
			source: domain.UsageSourceRecord{
				Kind:            domain.UsageSourceCodexRollout,
				NativeSessionID: "11111111-1111-4111-8111-111111111111",
				ParserStateJSON: validChild,
			},
		},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeParserState(test.source); err == nil {
				t.Fatal("invalid Codex direct-parent state was accepted")
			}
		})
	}
}

func TestParsersRejectInvalidTokenVectorsAndAdvanceCursor(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	tests := []struct {
		name   string
		source domain.UsageSourceContext
		record []byte
	}{
		{
			name:   "claude negative cache input",
			source: usageSource(domain.UsageSourceClaudeMain),
			record: []byte(`{"type":"assistant","uuid":"bad","message":{"id":"bad","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":-1,"cache_read_input_tokens":0,"output_tokens":2}}}`),
		},
		{
			name:   "codex cached input exceeds input",
			source: usageSource(domain.UsageSourceCodexRollout),
			record: codexTokenLine("2026-07-01T10:00:00Z", 10, 11, 0, 2, 1),
		},
		{
			name:   "codex reasoning exceeds output",
			source: usageSource(domain.UsageSourceCodexRollout),
			record: codexTokenLine("2026-07-01T10:00:00Z", 10, 5, 0, 2, 3),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := parseRecords(test.source, []jsonlRecord{{Data: test.record}}, 777, now)
			if len(result.Events) != 0 {
				t.Fatalf("events = %+v, want none", result.Events)
			}
			if result.Cursor.ByteOffset != 777 || result.Cursor.AnomalyCount != 1 ||
				result.Cursor.LastErrorCode != domain.UsageErrorMalformedJSONL {
				t.Fatalf("cursor = %+v", result.Cursor)
			}
		})
	}
}

func TestParserEventKeysSurvivePhysicalSourceReplacement(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	claudeRecord := jsonlRecord{
		Offset: 120,
		Data: []byte(
			`{"type":"assistant","uuid":"native-message","message":{"id":"msg-1","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`,
		),
	}
	firstClaude := usageSource(domain.UsageSourceClaudeMain)
	firstClaude.NativeRootID = "claude-root"
	firstClaude.Source.NativeSessionID = "claude-root"
	secondClaude := firstClaude
	secondClaude.Source.ID = 99

	firstClaudeResult := parseRecords(firstClaude, []jsonlRecord{claudeRecord}, 400, now)
	secondClaudeResult := parseRecords(secondClaude, []jsonlRecord{claudeRecord}, 400, now)
	if len(firstClaudeResult.Events) != 1 || len(secondClaudeResult.Events) != 1 ||
		firstClaudeResult.Events[0].SourceEventKey != secondClaudeResult.Events[0].SourceEventKey {
		t.Fatalf("claude keys=%+v/%+v", firstClaudeResult.Events, secondClaudeResult.Events)
	}

	codexRecords := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-5.6"}}`)},
		{Offset: 100, Data: codexTokenLine("2026-07-28T10:00:00Z", 100, 60, 0, 20, 5)},
	}
	firstCodex := usageSource(domain.UsageSourceCodexRollout)
	firstCodex.NativeRootID = "codex-root"
	firstCodex.Source.NativeSessionID = "codex-root"
	secondCodex := firstCodex
	secondCodex.Source.ID = 100
	firstCodexResult := parseRecords(firstCodex, codexRecords, 300, now)
	secondCodexResult := parseRecords(secondCodex, codexRecords, 300, now)
	if len(firstCodexResult.Events) != 1 || len(secondCodexResult.Events) != 1 ||
		firstCodexResult.Events[0].SourceEventKey != secondCodexResult.Events[0].SourceEventKey {
		t.Fatalf("codex keys=%+v/%+v", firstCodexResult.Events, secondCodexResult.Events)
	}
}

func TestParserEventKeysSeparateSubagentsAndCounterResetEpochs(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	record := jsonlRecord{
		Offset: 20,
		Data: []byte(
			`{"type":"assistant","uuid":"native-message","message":{"id":"msg-1","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`,
		),
	}
	firstSubagent := usageSource(domain.UsageSourceClaudeSubagent)
	firstSubagent.NativeRootID = "claude-root"
	firstSubagent.Source.NativeSessionID = "claude-root"
	firstSubagent.Source.SubagentID = "sub-1"
	secondSubagent := firstSubagent
	secondSubagent.Source.ID = 9
	secondSubagent.Source.SubagentID = "sub-2"
	firstResult := parseRecords(firstSubagent, []jsonlRecord{record}, 300, now)
	secondResult := parseRecords(secondSubagent, []jsonlRecord{record}, 300, now)
	if firstResult.Events[0].SourceEventKey == secondResult.Events[0].SourceEventKey {
		t.Fatalf("distinct subagents shared key %q", firstResult.Events[0].SourceEventKey)
	}

	codex := usageSource(domain.UsageSourceCodexRollout)
	codex.NativeRootID = "codex-root"
	firstEpoch := parseRecords(codex, []jsonlRecord{
		{Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-5.6"}}`)},
		{Data: codexTokenLine("2026-07-28T10:00:00Z", 10, 5, 0, 2, 1)},
	}, 200, now)
	secondEpoch := parseRecords(codex, []jsonlRecord{
		{Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-5.6"}}`)},
		{Data: codexTokenLine("2026-07-28T11:00:00Z", 10, 5, 0, 2, 1)},
	}, 200, now)
	if firstEpoch.Events[0].SourceEventKey == secondEpoch.Events[0].SourceEventKey {
		t.Fatalf("distinct Codex epochs shared key %q", firstEpoch.Events[0].SourceEventKey)
	}
}

func TestParsersTrackProviderModelChanges(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	claude := usageSource(domain.UsageSourceClaudeMain)
	claudeResult := parseRecords(claude, []jsonlRecord{
		{Data: []byte(`{"type":"assistant","uuid":"one","message":{"id":"msg-1","model":"claude-a","stop_reason":"end_turn","usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`)},
		{Data: []byte(`{"type":"assistant","uuid":"two","message":{"id":"msg-2","model":"claude-b","stop_reason":"end_turn","usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}`)},
	}, 500, now)
	if len(claudeResult.Events) != 2 ||
		claudeResult.Events[0].ModelID != "claude-a" ||
		claudeResult.Events[1].ModelID != "claude-b" {
		t.Fatalf("claude events=%+v", claudeResult.Events)
	}

	codex := usageSource(domain.UsageSourceCodexRollout)
	codexResult := parseRecords(codex, []jsonlRecord{
		{Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-a"}}`)},
		{Data: codexTokenLine("2026-07-28T10:00:00Z", 10, 5, 0, 2, 1)},
		{Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-b"}}`)},
		{Data: codexTokenLine("2026-07-28T10:01:00Z", 20, 10, 0, 4, 2)},
	}, 500, now)
	if len(codexResult.Events) != 2 ||
		codexResult.Events[0].ModelID != "gpt-a" ||
		codexResult.Events[1].ModelID != "gpt-b" {
		t.Fatalf("codex events=%+v", codexResult.Events)
	}
}

func TestReadJSONLChunkRetainsPartialTailAndSkipsOversizedRecord(t *testing.T) {
	path := t.TempDir() + "/rollout.jsonl"
	mustNoError(t, osWrite(path, `{"a":1}`+"\n"+`{"b":`))
	first, err := readJSONLChunkAtPath(path, 0, 1024, 32, false)
	mustNoError(t, err)
	if len(first.records) != 1 || first.atEOF || !first.readToEOF ||
		string(first.trailing) != `{"b":` ||
		first.nextOffset != int64(len(`{"a":1}`+"\n")) {
		t.Fatalf("first chunk = %+v", first)
	}
	mustNoError(t, osAppend(path, `2}`+"\n"))
	second, err := readJSONLChunkAtPath(path, first.nextOffset, 1024, 32, false)
	mustNoError(t, err)
	if len(second.records) != 1 || !second.atEOF {
		t.Fatalf("second chunk = %+v", second)
	}

	mustNoError(t, osWrite(path, strings.Repeat("x", 40)+"\n"))
	large, err := readJSONLChunkAtPath(path, 0, 1024, 16, false)
	mustNoError(t, err)
	if large.anomalies != 1 || large.errorCode != domain.UsageErrorRecordTooLarge || large.nextOffset != 41 {
		t.Fatalf("oversized chunk = %+v", large)
	}
}

func TestReadJSONLChunkDoesNotSkipRecordAfterCompleteOversizedLine(t *testing.T) {
	path := t.TempDir() + "/rollout.jsonl"
	oversized := strings.Repeat("x", 40) + "\n"
	valid := `{"valid":true}` + "\n"
	mustNoError(t, osWrite(path, oversized+valid))

	first, err := readJSONLChunkAtPath(path, 0, int64(len(oversized)), 16, false)
	mustNoError(t, err)
	if first.nextOffset != int64(len(oversized)) || first.discardingOversizedRecord {
		t.Fatalf("first chunk = %+v, want a fully consumed oversized record", first)
	}
	second, err := readJSONLChunkAtPath(path, first.nextOffset, 1024, 16, first.discardingOversizedRecord)
	mustNoError(t, err)
	if len(second.records) != 1 || string(second.records[0].Data) != `{"valid":true}` {
		t.Fatalf("second chunk = %+v, want the valid record", second)
	}
}

func TestContextReaderStopsBetweenTranscriptReadChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelAfterChunkReader{cancel: cancel}

	_, err := io.ReadAll(contextReader{ctx: ctx, reader: reader})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadAll() error = %v, want context canceled", err)
	}
}

type cancelAfterChunkReader struct {
	cancel context.CancelFunc
	read   bool
}

func (r *cancelAfterChunkReader) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	buffer[0] = 'x'
	r.cancel()
	return 1, nil
}

func usageSource(kind domain.UsageSourceKind) domain.UsageSourceContext {
	return domain.UsageSourceContext{
		Source: domain.UsageSourceRecord{
			ID:              7,
			Kind:            kind,
			ParserStateJSON: "{}",
			State:           domain.UsageSourceActive,
		},
		InitialModelID: "fallback-model",
	}
}

func parserStateFromResult(t *testing.T, result parseResult, kind domain.UsageSourceKind) *parserStateEnvelope {
	t.Helper()
	if result.err != nil {
		t.Fatalf("parse records: %v", result.err)
	}
	state, err := decodeParserState(domain.UsageSourceRecord{
		Kind:            kind,
		ByteOffset:      result.Cursor.ByteOffset,
		ParserStateJSON: result.Cursor.ParserStateJSON,
	})
	if err != nil {
		t.Fatalf("decode result parser state: %v", err)
	}
	return state
}

func codexTokenLine(timestamp string, input, cached, cacheWrite, output, reasoning int64) []byte {
	return []byte(fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}}}}`,
		timestamp, input, cached, cacheWrite, output, reasoning, input+output,
	))
}

func codexContextFillLine(timestamp string, modelContextWindow int64) []byte {
	return []byte(fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":0,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":%d},"model_context_window":%d}}}`,
		timestamp, modelContextWindow, modelContextWindow,
	))
}

func codexTokenLineWithLast(timestamp string, last, total codexTokenVector) []byte {
	return []byte(fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":%s,"total_token_usage":%s}}}`,
		timestamp, mustJSONTokenVector(last), mustJSONTokenVector(total),
	))
}

func codexTokenLineWithTotal(timestamp string, total codexTokenVector) []byte {
	return []byte(fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":%s}}}`,
		timestamp, mustJSONTokenVector(total),
	))
}

func mustJSONTokenVector(vector codexTokenVector) string {
	encoded, err := json.Marshal(vector)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func assertCanonicalEventTotals(t *testing.T, events []domain.ModelUsageEvent, input, cachedInput, uncachedInput, output int64) {
	t.Helper()
	var gotInput, gotCachedInput, gotUncachedInput, gotOutput int64
	for _, event := range events {
		gotInput += tokenValue(event.Tokens.InputTokens)
		gotCachedInput += tokenValue(event.Tokens.CachedInputTokens)
		gotUncachedInput += tokenValue(event.Tokens.UncachedInputTokens)
		gotOutput += tokenValue(event.Tokens.OutputTokens)
	}
	if gotInput != input || gotCachedInput != cachedInput || gotUncachedInput != uncachedInput || gotOutput != output {
		t.Fatalf("canonical totals = (%d, %d, %d, %d), want (%d, %d, %d, %d)", gotInput, gotCachedInput, gotUncachedInput, gotOutput, input, cachedInput, uncachedInput, output)
	}
}

func tokenValue(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

// providerUsageValue reads one counter back out of a stored bounded provider
// usage object, so tests assert on exactly what pricing will later read.
// A missing path returns nil rather than zero.
func providerUsageValue(t *testing.T, encoded string, path ...string) *int64 {
	t.Helper()
	if encoded == "" {
		return nil
	}
	var node any
	if err := json.Unmarshal([]byte(encoded), &node); err != nil {
		t.Fatalf("decode provider usage %q: %v", encoded, err)
	}
	for _, key := range path {
		object, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		if node, ok = object[key]; !ok {
			return nil
		}
	}
	number, ok := node.(float64)
	if !ok {
		return nil
	}
	value := int64(number)
	return &value
}

func providerUsageTokens(t *testing.T, encoded string, path ...string) int64 {
	t.Helper()
	return tokenValue(providerUsageValue(t, encoded, path...))
}

func osWrite(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func readJSONLChunkAtPath(
	path string,
	offset int64,
	maxBytes int64,
	maxRecord int,
	discardingOversizedRecord bool,
) (jsonlChunk, error) {
	file, err := os.Open(path) //nolint:gosec // test-controlled path.
	if err != nil {
		return jsonlChunk{}, err
	}
	defer func() { _ = file.Close() }()
	return readJSONLChunkFromFile(file, offset, maxBytes, maxRecord, discardingOversizedRecord)
}

func osAppend(path, content string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.WriteString(content)
	return err
}

// Break caught: once usage became a json.RawMessage, an explicit `usage: null`
// no longer looked absent. It decoded as all-zero counters and produced a
// synthetic event whose provider object was the literal `null`, which the
// object-only column constraint rejects — rolling back the chunk without
// advancing the cursor, so every retry hit the same record and no later usage
// on that source could ever be collected.
func TestParseClaudeTreatsNullUsageAsNoUsageWithoutWedgingTheSource(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeMain)
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"assistant","uuid":"null-usage","timestamp":"2026-07-01T10:00:00Z","message":{"id":"msg-null","model":"claude-x","stop_reason":"end_turn","usage":null}}`)},
		{Offset: 200, Data: []byte(`{"type":"assistant","uuid":"scalar-usage","timestamp":"2026-07-01T10:00:01Z","message":{"id":"msg-scalar","model":"claude-x","stop_reason":"end_turn","usage":7}}`)},
		{Offset: 400, Data: []byte(`{"type":"assistant","uuid":"real","timestamp":"2026-07-01T10:00:02Z","message":{"id":"msg-real","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":4}}}`)},
	}

	result := parseRecords(source, records, 600, time.Unix(1700000000, 0).UTC())

	// A null usage member is skipped exactly like an absent one, a present
	// non-object member stays a malformed anomaly, and the record after them is
	// still collected.
	if len(result.Events) != 1 {
		t.Fatalf("events = %+v, want only the record carrying real usage", result.Events)
	}
	if result.Cursor.AnomalyCount != 1 || result.Cursor.LastErrorCode != domain.UsageErrorMalformedJSONL {
		t.Fatalf("anomalies = %d (%q), want exactly the scalar usage record",
			result.Cursor.AnomalyCount, result.Cursor.LastErrorCode)
	}
	got := result.Events[0]
	if got.SourceEventKey == "" || tokenValue(got.Tokens.InputTokens) != 10 || tokenValue(got.Tokens.OutputTokens) != 4 {
		t.Fatalf("event = %+v, want the real usage record", got)
	}
	if got.ProviderUsageJSON == "" || got.ProviderUsageJSON == "null" {
		t.Fatalf("provider usage = %q, want the emitted object", got.ProviderUsageJSON)
	}
	if result.Cursor.ByteOffset != 600 {
		t.Fatalf("cursor = %d, want the chunk to advance past the unusable records", result.Cursor.ByteOffset)
	}
}

// Break caught: `info: null` decoded into an all-zero cumulative vector instead
// of being ignored. Against an existing baseline that read as a backwards jump,
// which reset the baseline to zero, so the next real cumulative record was
// charged from zero and durably double-counted every earlier token.
func TestParseCodexNullInfoDoesNotResetTheCumulativeBaseline(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"turn_context","payload":{"model":"gpt-5.6"}}`)},
		{Offset: 100, Data: codexTokenLine("2026-07-01T10:00:00Z", 100, 60, 0, 20, 5)},
		{Offset: 200, Data: []byte(`{"timestamp":"2026-07-01T10:00:01Z","type":"event_msg","payload":{"type":"token_count","info":null}}`)},
		{Offset: 300, Data: codexTokenLine("2026-07-01T10:00:02Z", 160, 90, 0, 35, 8)},
	}

	result := parseRecords(source, records, 400, time.Unix(1700000000, 0).UTC())

	if len(result.Events) != 2 || result.Cursor.AnomalyCount != 0 {
		t.Fatalf("result = %+v, want two events and no anomaly", result)
	}
	// The second event is the delta against the first record's totals. A reset
	// baseline would charge the full cumulative 160/90/35 again.
	if got := result.Events[1].Tokens; tokenValue(got.InputTokens) != 60 ||
		tokenValue(got.CachedInputTokens) != 30 || tokenValue(got.OutputTokens) != 15 {
		t.Fatalf("delta tokens = %+v, want the baseline preserved across the null record", got)
	}
	state := parserStateFromResult(t, result, domain.UsageSourceCodexRollout)
	if state.Codex.Baseline.InputTokens != 160 {
		t.Fatalf("baseline = %+v, want the latest cumulative total", state.Codex.Baseline)
	}
}
