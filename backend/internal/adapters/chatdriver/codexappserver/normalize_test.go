package codexappserver

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The payloads here are shaped after frames captured from a real
// `codex app-server` session (codex-cli 0.146.0), not invented.

// testNow is a fixed clock so the rate-limit reset arithmetic is deterministic. It
// is the emittedAtMs of the captured account/rateLimits/updated frame below,
// rounded to the second, which makes the expected remaining durations readable
// against the real resetsAt values.
var testNow = time.Unix(1785669503, 0)

func normalizeOne(t *testing.T, method, params string) ports.ChatEvent {
	t.Helper()
	events := normalizeNotification(notification{Method: method, Params: json.RawMessage(params)}, testNow)
	if len(events) != 1 {
		t.Fatalf("normalize(%s) produced %d events, want 1", method, len(events))
	}
	return events[0]
}

func normalizeNone(t *testing.T, method, params string) {
	t.Helper()
	events := normalizeNotification(notification{Method: method, Params: json.RawMessage(params)}, testNow)
	if len(events) != 0 {
		t.Fatalf("normalize(%s) produced %d events, want none: %+v", method, len(events), events)
	}
}

func TestNormalizeTurnLifecycle(t *testing.T) {
	started := normalizeOne(t, "turn/started", `{"threadId":"th1","turn":{"id":"tu1","status":"inProgress","items":[]}}`)
	if started.Kind != ports.ChatEventTurnStarted || started.ProviderTurnID != "tu1" ||
		started.ProviderConversationID != "th1" {
		t.Fatalf("turn/started -> %+v", started)
	}

	done := normalizeOne(t, "turn/completed", `{"threadId":"th1","turn":{"id":"tu1","status":"completed","items":[]}}`)
	if done.Kind != ports.ChatEventTurnCompleted || done.TurnState != domain.TurnStateCompleted ||
		done.ProviderConversationID != "th1" {
		t.Fatalf("turn/completed -> %+v", done)
	}
}

// The provider reports a cancelled turn with its own terminal status. AO must
// carry that through rather than calling it completed or failed.
func TestNormalizeInterruptedTurnKeepsItsOwnState(t *testing.T) {
	ev := normalizeOne(t, "turn/completed", `{"threadId":"th1","turn":{"id":"tu2","status":"interrupted","items":[]}}`)
	if ev.TurnState != domain.TurnStateInterrupted {
		t.Fatalf("state = %q, want %q", ev.TurnState, domain.TurnStateInterrupted)
	}
	if ev.Err != nil {
		t.Fatalf("interrupted turn carried an error: %v", ev.Err)
	}
}

// An unrecognized status must not be optimistically read as success.
func TestNormalizeUnknownTurnStatusFailsClosed(t *testing.T) {
	ev := normalizeOne(t, "turn/completed", `{"threadId":"th1","turn":{"id":"tu3","status":"someFutureStatus","items":[]}}`)
	if ev.TurnState != domain.TurnStateFailed {
		t.Fatalf("state = %q, want %q for an unknown status", ev.TurnState, domain.TurnStateFailed)
	}
}

func TestNormalizeAssistantStreaming(t *testing.T) {
	delta := normalizeOne(t, "item/agentMessage/delta",
		`{"threadId":"th1","turnId":"tu1","itemId":"msg_1","delta":"Running"}`)
	if delta.Kind != ports.ChatEventMessageDelta || delta.Delta != "Running" || delta.ProviderItemID != "msg_1" {
		t.Fatalf("delta -> %+v", delta)
	}

	// An empty delta carries no information and must not bump a revision.
	normalizeNone(t, "item/agentMessage/delta", `{"itemId":"msg_1","delta":""}`)

	completed := normalizeOne(t, "item/completed",
		`{"threadId":"th1","turnId":"tu1","item":{"id":"msg_1","type":"agentMessage","text":"Running the script."}}`)
	if completed.Kind != ports.ChatEventMessageCompleted || completed.Text != "Running the script." {
		t.Fatalf("agentMessage completed -> %+v", completed)
	}
}

// item/started for an assistant message adds nothing: the row is created by the
// first delta, so emitting an empty message would flash a blank bubble.
func TestNormalizeAgentMessageStartedIsIgnored(t *testing.T) {
	normalizeNone(t, "item/started", `{"turnId":"tu1","item":{"id":"msg_1","type":"agentMessage","text":""}}`)
}

// AO persists the user's message when it accepts the send. The provider echoing
// it back as an item must not create a duplicate timeline entry.
func TestNormalizeUserMessageEchoIsIgnored(t *testing.T) {
	normalizeNone(t, "item/completed", `{"turnId":"tu1","item":{"id":"um_1","type":"userMessage","content":[]}}`)
	normalizeNone(t, "item/started", `{"turnId":"tu1","item":{"id":"um_1","type":"userMessage","content":[]}}`)
}

func TestNormalizeCommandExecutionUnwrapsShellWrapper(t *testing.T) {
	started := normalizeOne(t, "item/started",
		`{"threadId":"th1","turnId":"tu1","item":{"id":"exec-1","type":"commandExecution",`+
			`"command":"/bin/zsh -lc 'date -u'","cwd":"/tmp/work"}}`)

	if started.Kind != ports.ChatEventActivityStarted {
		t.Fatalf("kind = %q", started.Kind)
	}
	if started.ActivityKind != domain.ActivityKindCommand {
		t.Fatalf("activity kind = %q", started.ActivityKind)
	}
	if started.ActivityStatus != domain.ActivityStatusRunning {
		t.Fatalf("status = %q, want running", started.ActivityStatus)
	}
	if started.Summary != "date -u" {
		t.Fatalf("summary = %q, want the shell wrapper stripped", started.Summary)
	}

	var detail map[string]any
	if err := json.Unmarshal(started.Detail, &detail); err != nil {
		t.Fatalf("detail not decodable: %v", err)
	}
	if detail["command"] != "date -u" {
		t.Errorf("detail command = %v", detail["command"])
	}
	// The raw invocation is kept so the exact thing that ran is recoverable.
	if detail["rawCommand"] != "/bin/zsh -lc 'date -u'" {
		t.Errorf("detail rawCommand = %v", detail["rawCommand"])
	}
	if detail["cwd"] != "/tmp/work" {
		t.Errorf("detail cwd = %v", detail["cwd"])
	}
}

func TestNormalizeCommandExitCodeDrivesStatus(t *testing.T) {
	ok := normalizeOne(t, "item/completed",
		`{"turnId":"tu1","item":{"id":"exec-1","type":"commandExecution","command":"true","exitCode":0,"durationMs":31,"aggregatedOutput":"hi\n"}}`)
	if ok.ActivityStatus != domain.ActivityStatusCompleted {
		t.Fatalf("exit 0 status = %q, want completed", ok.ActivityStatus)
	}

	var detail map[string]any
	if err := json.Unmarshal(ok.Detail, &detail); err != nil {
		t.Fatalf("detail not decodable: %v", err)
	}
	// Provider output aggregation was observed to drop leading bytes, so it must
	// be flagged rather than presented as the authoritative record.
	if detail["outputMayBePartial"] != true {
		t.Errorf("captured output was not flagged as possibly partial: %v", detail)
	}

	failed := normalizeOne(t, "item/completed",
		`{"turnId":"tu1","item":{"id":"exec-2","type":"commandExecution","command":"false","exitCode":1}}`)
	if failed.ActivityStatus != domain.ActivityStatusFailed {
		t.Fatalf("exit 1 status = %q, want failed", failed.ActivityStatus)
	}
}

func TestNormalizeOtherItemKinds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params string
		want   domain.ActivityKind
	}{
		{"file change", `{"turnId":"t","item":{"id":"i","type":"fileChange","changes":[]}}`, domain.ActivityKindFileChange},
		{"plan", `{"turnId":"t","item":{"id":"i","type":"plan","text":"1. do it"}}`, domain.ActivityKindPlan},
		{"reasoning", `{"turnId":"t","item":{"id":"i","type":"reasoning","summary":["thinking"]}}`, domain.ActivityKindReasoning},
		// An MCP call is not a shell command and must not render as one.
		{"mcp tool", `{"turnId":"t","item":{"id":"i","type":"mcpToolCall","server":"s","tool":"grep"}}`, domain.ActivityKindMCPTool},
		{"error", `{"turnId":"t","item":{"id":"i","type":"error","message":"boom"}}`, domain.ActivityKindError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := normalizeOne(t, "item/completed", tc.params)
			if ev.ActivityKind != tc.want {
				t.Fatalf("activity kind = %q, want %q", ev.ActivityKind, tc.want)
			}
		})
	}
}

// An item type this build does not model must produce nothing rather than an
// activity with guessed semantics.
func TestNormalizeUnknownItemTypeIsDropped(t *testing.T) {
	normalizeNone(t, "item/completed", `{"turnId":"t","item":{"id":"i","type":"someFutureItem"}}`)
}

// Provider bookkeeping is not conversation content. These are the methods a real
// three-turn session emitted alongside the useful ones.
func TestNormalizeIgnoresProviderBookkeeping(t *testing.T) {
	for _, method := range []string{
		"hook/started",
		"hook/completed",
		"remoteControl/status/changed",
		"thread/goal/cleared",
		"thread/started",
		// The provider says when a model is being buffered for safety review. It
		// affects latency, not the conversation, and AO has nothing to do with it.
		"model/safetyBuffering/updated",
		// guardianWarning restates an auto-approval decision in prose. The
		// autoApprovalReview pair carries the same rationale as structure, so reading
		// both would put one decision on the timeline twice.
		"guardianWarning",
		// The client-driven exec API. AO never asks the server to run anything, so an
		// agent tool call never arrives on these.
		"command/exec/outputDelta",
		"process/outputDelta",
		// The voice surface. AO has none.
		"thread/realtime/transcript/delta",
		"thread/realtime/outputAudio/delta",
		"someMethodAddedNextRelease",
	} {
		t.Run(method, func(t *testing.T) { normalizeNone(t, method, `{}`) })
	}
}

// The provider broadcasts this once a request is answered; it is how a second
// client learns an approval card is no longer actionable.
func TestNormalizeServerRequestResolved(t *testing.T) {
	// The real shape: requestId is the JSON-RPC id of the original server->client
	// request, and it arrives as a number. Zero is a legitimate id — the first
	// approval of a session is id 0 — so it must not be treated as absent.
	ev := normalizeOne(t, "serverRequest/resolved", `{"threadId":"th1","requestId":0}`)
	if ev.Kind != ports.ChatEventApprovalResolved {
		t.Fatalf("kind = %q", ev.Kind)
	}
	if ev.RequestID != "0" {
		t.Fatalf("request id = %q, want %q", ev.RequestID, "0")
	}

	if ev := normalizeOne(t, "serverRequest/resolved", `{"requestId":2}`); ev.RequestID != "2" {
		t.Fatalf("request id = %q, want 2", ev.RequestID)
	}

	// A string id is also legal per the JSON-RPC envelope.
	if ev := normalizeOne(t, "serverRequest/resolved", `{"requestId":"r-42"}`); ev.RequestID != "r-42" {
		t.Fatalf("request id = %q, want r-42", ev.RequestID)
	}

	normalizeNone(t, "serverRequest/resolved", `{}`)
	normalizeNone(t, "serverRequest/resolved", `{"requestId":null}`)
}

// usageFrame is a verbatim thread/tokenUsage/updated params payload captured from
// a live codex app-server turn. The payload key is `tokenUsage`; an earlier build
// read `usage` and so recorded nothing on every single update.
const usageFrame = `{"threadId":"019fc232-2814-7980-a6c8-5b597492fab2","turnId":"019fc232-2887-7dc2-83c7-ce57a7c1d218","tokenUsage":{"total":{"totalTokens":18055,"inputTokens":18050,"cachedInputTokens":11008,"cacheWriteInputTokens":0,"outputTokens":5,"reasoningOutputTokens":0},"last":{"totalTokens":18055,"inputTokens":18050,"cachedInputTokens":11008,"cacheWriteInputTokens":0,"outputTokens":5,"reasoningOutputTokens":0},"modelContextWindow":258400}}`

func TestNormalizeTokenUsage(t *testing.T) {
	ev := normalizeOne(t, "thread/tokenUsage/updated", usageFrame)

	// A typed usage event, not an activity: one timeline row per report is what
	// buried the conversation, and the provider sends one after every tool call.
	if ev.Kind != ports.ChatEventUsage {
		t.Fatalf("kind = %q, want %q", ev.Kind, ports.ChatEventUsage)
	}
	if ev.Usage == nil {
		t.Fatal("usage event carried no usage")
	}
	if ev.ProviderTurnID != "019fc232-2887-7dc2-83c7-ce57a7c1d218" {
		t.Fatalf("turn id = %q", ev.ProviderTurnID)
	}
	// Context fullness comes from `last`, and the window from modelContextWindow.
	// Together they are the whole point: 18055 alone is a number with no scale.
	if ev.Usage.ContextUsed != 18055 || ev.Usage.ContextWindow != 258400 {
		t.Fatalf("context = %d/%d, want 18055/258400",
			ev.Usage.ContextUsed, ev.Usage.ContextWindow)
	}
	if ev.Usage.TotalTokens != 18055 || ev.Usage.InputTokens != 18050 {
		t.Fatalf("cumulative = %+v", ev.Usage)
	}
	if ev.Usage.CachedTokens != 11008 || ev.Usage.OutputTokens != 5 {
		t.Fatalf("cumulative = %+v", ev.Usage)
	}
}

// A model the provider will not state a window for must still report its tokens.
// Dropping the event would leave the header blank; claiming a window would invent
// a scale. The window stays zero and the reader decides what to draw.
func TestNormalizeTokenUsageWithoutContextWindow(t *testing.T) {
	ev := normalizeOne(t, "thread/tokenUsage/updated",
		`{"threadId":"th1","turnId":"tu1","tokenUsage":{"total":{"totalTokens":900,"inputTokens":800,"outputTokens":100},"last":{"totalTokens":900,"inputTokens":800,"outputTokens":100}}}`)
	if ev.Usage == nil || ev.Usage.ContextWindow != 0 || ev.Usage.ContextUsed != 900 {
		t.Fatalf("usage = %+v", ev.Usage)
	}
}

// rateLimitsFrame is a verbatim account/rateLimits/updated params payload from a
// live pro account. usedPercent is a percentage, not a token count, and resetsAt
// is an absolute unix timestamp rather than a duration.
const rateLimitsFrame = `{"rateLimits":{"limitId":"codex","limitName":null,"primary":{"usedPercent":71,"windowDurationMins":10080,"resetsAt":1786159947},"secondary":null,"credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"individualLimit":null,"spendControlReached":null,"planType":"pro","rateLimitReachedType":null}}`

func TestNormalizeRateLimits(t *testing.T) {
	ev := normalizeOne(t, "account/rateLimits/updated", rateLimitsFrame)

	if ev.Kind != ports.ChatEventRateLimits {
		t.Fatalf("kind = %q, want %q", ev.Kind, ports.ChatEventRateLimits)
	}
	if ev.RateLimits == nil {
		t.Fatal("rate limit event carried no limits")
	}
	if ev.RateLimits.PrimaryUsedPercent != 71 {
		t.Fatalf("primary = %v, want 71", ev.RateLimits.PrimaryUsedPercent)
	}
	// This account reports no secondary window at all. Negative is the port's
	// "not reported"; zero would claim the quota is untouched, which is a much
	// more reassuring statement than "no such window".
	if ev.RateLimits.SecondaryUsedPercent >= 0 {
		t.Fatalf("secondary = %v, want negative for an unreported window",
			ev.RateLimits.SecondaryUsedPercent)
	}
	if ev.RateLimits.SecondaryResetsInSeconds != 0 {
		t.Fatalf("secondary resets = %d, want 0", ev.RateLimits.SecondaryResetsInSeconds)
	}
	// 1786159947 - 1785669503, the remaining seconds of a 7 day window.
	if got := ev.RateLimits.PrimaryResetsInSeconds; got != 490444 {
		t.Fatalf("primary resets in %d, want 490444", got)
	}
	if ev.RateLimits.PlanLabel != "pro" {
		t.Fatalf("plan = %q, want pro", ev.RateLimits.PlanLabel)
	}
}

// A reset instant already in the past has nothing left to wait for. Reporting the
// negative remainder would render as a window that refills in the past.
func TestNormalizeRateLimitsFloorsElapsedReset(t *testing.T) {
	ev := normalizeOne(t, "account/rateLimits/updated",
		`{"rateLimits":{"primary":{"usedPercent":12,"resetsAt":1,"windowDurationMins":300},"planType":"plus"}}`)
	if ev.RateLimits.PrimaryResetsInSeconds != 0 {
		t.Fatalf("resets in %d, want 0", ev.RateLimits.PrimaryResetsInSeconds)
	}
	if ev.RateLimits.PrimaryUsedPercent != 12 {
		t.Fatalf("primary = %v", ev.RateLimits.PrimaryUsedPercent)
	}
}

// A window reported as genuinely empty is not the same as an absent one, so 0 must
// survive as 0 rather than collapsing into the negative "not reported" signal.
func TestNormalizeRateLimitsKeepsReportedZero(t *testing.T) {
	ev := normalizeOne(t, "account/rateLimits/updated",
		`{"rateLimits":{"primary":{"usedPercent":0,"windowDurationMins":10080,"resetsAt":1786274031},"secondary":{"usedPercent":40,"resetsAt":1785670000},"planType":"pro"}}`)
	if ev.RateLimits.PrimaryUsedPercent != 0 {
		t.Fatalf("primary = %v, want a reported 0", ev.RateLimits.PrimaryUsedPercent)
	}
	if ev.RateLimits.SecondaryUsedPercent != 40 {
		t.Fatalf("secondary = %v, want 40", ev.RateLimits.SecondaryUsedPercent)
	}
	if ev.RateLimits.SecondaryResetsInSeconds != 497 {
		t.Fatalf("secondary resets in %d, want 497", ev.RateLimits.SecondaryResetsInSeconds)
	}
}

// Malformed params must be skipped, never panic.
func TestNormalizeToleratesMalformedParams(t *testing.T) {
	for _, method := range []string{
		"turn/started", "turn/completed", "item/started", "item/completed",
		"item/agentMessage/delta", "thread/tokenUsage/updated", "serverRequest/resolved",
		"account/rateLimits/updated",
	} {
		normalizeNone(t, method, `"not an object"`)
	}
}

// A current app-server reports compaction ONLY as a contextCompaction item. The
// schema still declares a thread/compacted notification and marks it deprecated;
// 0.146.0 never sends it. Reading only the notification would mean AO silently
// never noticed a compaction, and a conversation that quietly lost half its
// history with nothing to mark where reads as if the agent simply forgot.
func TestNormalizeContextCompactionItem(t *testing.T) {
	ev := normalizeOne(t, "item/completed",
		`{"item":{"type":"contextCompaction","id":"cc-1"},"threadId":"th1","turnId":"t1","completedAtMs":1785669337435}`)
	if ev.Kind != ports.ChatEventCompacted {
		t.Fatalf("kind = %q, want %q", ev.Kind, ports.ChatEventCompacted)
	}
	if ev.ProviderItemID != "cc-1" || ev.ProviderTurnID != "t1" {
		t.Fatalf("ids = %q/%q, want cc-1/t1", ev.ProviderItemID, ev.ProviderTurnID)
	}
}

// The reclaim is unknown until the item settles: the reduced token figure arrives
// between start and completion. A row emitted on start would have to be rewritten
// with the real numbers.
func TestNormalizeContextCompactionStartIsIgnored(t *testing.T) {
	normalizeNone(t, "item/started",
		`{"item":{"type":"contextCompaction","id":"cc-1"},"threadId":"th1","turnId":"t1"}`)
}

// The deprecated spelling, for a provider old enough to send it. It carries only
// ids: no token figures at all, which is why the reclaim has to be bracketed from
// token-usage reports rather than read off the event.
func TestNormalizeDeprecatedCompactedNotification(t *testing.T) {
	ev := normalizeOne(t, "thread/compacted", `{"threadId":"th1","turnId":"t1"}`)
	if ev.Kind != ports.ChatEventCompacted {
		t.Fatalf("kind = %q", ev.Kind)
	}
	if ev.ProviderTurnID != "t1" {
		t.Fatalf("turn id = %q, want t1", ev.ProviderTurnID)
	}
	normalizeNone(t, "thread/compacted", `"not an object"`)
}

// `last` and `total` answer different questions and only one of them is the
// context position. Measured across a compaction: total stayed at 15650 while last
// fell to 4632, because compaction cannot undo cumulative spend. Reading total
// would report every compaction as reclaiming nothing.
func TestContextPositionReadsLastNotTotal(t *testing.T) {
	used, window, ok := contextPositionFrom(notification{
		Method: "thread/tokenUsage/updated",
		Params: json.RawMessage(`{"threadId":"th1","turnId":"t1","tokenUsage":{"total":{"totalTokens":15650},"last":{"totalTokens":4632},"modelContextWindow":258400}}`),
	})
	if !ok {
		t.Fatal("token usage was not recognized")
	}
	if used != 4632 {
		t.Errorf("context used = %d, want 4632 (last, not total)", used)
	}
	if window != 258400 {
		t.Errorf("context window = %d, want 258400", window)
	}

	// The window is optional; a report without it must still yield the position.
	if used, window, ok := contextPositionFrom(notification{
		Method: "thread/tokenUsage/updated",
		Params: json.RawMessage(`{"tokenUsage":{"last":{"totalTokens":10},"total":{"totalTokens":10}}}`),
	}); !ok || used != 10 || window != 0 {
		t.Errorf("used/window/ok = %d/%d/%v, want 10/0/true", used, window, ok)
	}

	if _, _, ok := contextPositionFrom(notification{Method: "turn/started"}); ok {
		t.Error("a non-usage notification was read as a context position")
	}
}
