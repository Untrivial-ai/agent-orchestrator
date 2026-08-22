package codexappserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Coverage for the notifications AO used to drop.
//
// Every params literal below is a frame captured verbatim from codex-cli 0.146.0
// driving a real account, with paths and ids shortened. Where a method was never
// observed the test says so and exercises the schema's declared shape instead,
// because an unobserved handler still has to be a correct reader of what the
// provider says it can send.

/* ---- reasoning --------------------------------------------------------- */

// Captured frames: summaryPartAdded arrives immediately before the first delta of
// each part, and the same text arrives again in the item's summary array on
// item/completed.
func TestNormalizeReasoningSummaryStreams(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemReasoningSummaryTextDelta,
		`{"threadId":"019fc432","turnId":"019fc432-90af","itemId":"rs_00c2658b","delta":"**Searching deferred tool in ALL_TOOLS**","summaryIndex":0}`)
	if ev.Kind != ports.ChatEventReasoningDelta {
		t.Fatalf("kind = %q, want %q", ev.Kind, ports.ChatEventReasoningDelta)
	}
	if ev.Delta != "**Searching deferred tool in ALL_TOOLS**" {
		t.Fatalf("delta = %q", ev.Delta)
	}
	if ev.ProviderItemID != "rs_00c2658b" || ev.ProviderTurnID != "019fc432-90af" {
		t.Fatalf("ids = %q/%q", ev.ProviderItemID, ev.ProviderTurnID)
	}

	// An empty delta carries no information and must not bump a revision.
	normalizeNone(t, codexproto.MethodItemReasoningSummaryTextDelta,
		`{"itemId":"rs_1","delta":"","summaryIndex":0}`)
}

// The first summary part needs no separator and later ones do. The rule is the
// index alone, so a reconnect that misses part 0 cannot leave the stream
// permanently mis-joined.
func TestNormalizeReasoningSummaryPartBreaksOnlyBetweenParts(t *testing.T) {
	normalizeNone(t, codexproto.MethodItemReasoningSummaryPartAdded,
		`{"threadId":"th","turnId":"tu","itemId":"rs_1","summaryIndex":0}`)

	ev := normalizeOne(t, codexproto.MethodItemReasoningSummaryPartAdded,
		`{"threadId":"th","turnId":"tu","itemId":"rs_1","summaryIndex":1}`)
	if ev.Kind != ports.ChatEventReasoningDelta || ev.Delta != "\n\n" {
		t.Fatalf("part break -> %+v", ev)
	}
	if ev.ProviderItemID != "rs_1" {
		t.Fatalf("item id = %q", ev.ProviderItemID)
	}
}

// Raw reasoning was NEVER OBSERVED (probed with show_raw_agent_reasoning=true on a
// ChatGPT-authed account; only summaries arrived). The handler still has to read
// the shape the schema declares.
func TestNormalizeRawReasoningDelta(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemReasoningTextDelta,
		`{"threadId":"th","turnId":"tu","itemId":"rs_1","contentIndex":0,"delta":"chain of thought"}`)
	if ev.Kind != ports.ChatEventReasoningDelta || ev.Delta != "chain of thought" {
		t.Fatalf("raw reasoning -> %+v", ev)
	}
}

// The previous build looked for a `text` field on a reasoning item. A reasoning
// item has no text field — it has `summary` — so every settled reasoning row was
// blank. Captured item/completed: summary is an array of section strings.
func TestNormalizeReasoningItemCarriesItsSummaryText(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemCompleted,
		`{"threadId":"th","turnId":"tu","completedAtMs":1785703001253,"item":{"type":"reasoning","id":"rs_1","summary":["**Rendering exact result**","**Second thought**"],"content":[]}}`)
	if ev.ActivityKind != domain.ActivityKindReasoning {
		t.Fatalf("kind = %q", ev.ActivityKind)
	}
	detail := decodeDetailMap(t, ev.Detail)
	// Joined with the same blank line the streaming path inserts on
	// summaryPartAdded, so the settled text equals the streamed text.
	if got := detail["text"]; got != "**Rendering exact result**\n\n**Second thought**" {
		t.Fatalf("reasoning text = %q", got)
	}
}

/* ---- plan -------------------------------------------------------------- */

// Captured: three updates in one turn, each carrying the WHOLE step list with
// per-step status. A plan is structure, not prose.
func TestNormalizeTurnPlanUpdated(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodTurnPlanUpdated,
		`{"threadId":"019fc430","turnId":"019fc430-d7ce","explanation":null,"plan":[{"step":"Append a line containing \"three\" to hello.txt","status":"completed"},{"step":"Create notes.md containing two short sentences","status":"completed"},{"step":"Run ls -la and report the file count","status":"inProgress"}]}`)

	if ev.Kind != ports.ChatEventPlanUpdated {
		t.Fatalf("kind = %q, want %q", ev.Kind, ports.ChatEventPlanUpdated)
	}
	if ev.ProviderTurnID != "019fc430-d7ce" {
		t.Fatalf("turn id = %q", ev.ProviderTurnID)
	}
	if ev.Plan == nil {
		t.Fatal("no plan on the event")
	}
	if len(ev.Plan.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(ev.Plan.Steps))
	}
	if ev.Plan.Steps[0].Status != domain.PlanStepCompleted {
		t.Errorf("step 0 status = %q", ev.Plan.Steps[0].Status)
	}
	// The provider's camelCase becomes AO's persisted spelling.
	if ev.Plan.Steps[2].Status != domain.PlanStepInProgress {
		t.Errorf("step 2 status = %q, want %q", ev.Plan.Steps[2].Status, domain.PlanStepInProgress)
	}
	if ev.Plan.Steps[2].Text != "Run ls -la and report the file count" {
		t.Errorf("step 2 text = %q", ev.Plan.Steps[2].Text)
	}
	// The label states progress, because progress is what changes between the three
	// or four updates one turn produces.
	if !strings.HasPrefix(ev.Summary, "Plan 2/3:") {
		t.Errorf("summary = %q, want a 2/3 progress label", ev.Summary)
	}
}

// A status this build does not know must not be read as done: ticking off work
// that may not have happened is the one way a plan can lie.
func TestNormalizePlanUnknownStatusIsPending(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodTurnPlanUpdated,
		`{"threadId":"th","turnId":"tu","plan":[{"step":"do it","status":"someFutureStatus"}]}`)
	if ev.Plan.Steps[0].Status != domain.PlanStepPending {
		t.Fatalf("status = %q, want %q", ev.Plan.Steps[0].Status, domain.PlanStepPending)
	}
}

// An empty plan is a real state: the agent dropped its plan. It must be reported
// so a stale plan cannot outlive the turn that made it.
func TestNormalizeEmptyPlanIsReported(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodTurnPlanUpdated, `{"threadId":"th","turnId":"tu","plan":[]}`)
	if ev.Plan == nil || len(ev.Plan.Steps) != 0 {
		t.Fatalf("plan -> %+v", ev.Plan)
	}
	if ev.Summary != "Cleared the plan" {
		t.Fatalf("summary = %q", ev.Summary)
	}
}

// item/plan/delta was NEVER OBSERVED: in a probe where the agent called
// update_plan three times, no plan item and no plan delta ever arrived. A partial
// plan is text, and parsing half of one into steps would invent structure.
func TestNormalizePlanDeltaIsText(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemPlanDelta,
		`{"threadId":"th","turnId":"tu","itemId":"plan_1","delta":"1. first"}`)
	if ev.Kind != ports.ChatEventActivityText || ev.Delta != "1. first" {
		t.Fatalf("plan delta -> %+v", ev)
	}
}

/* ---- file changes ------------------------------------------------------ */

// Captured item/started for a two-file patch. The provider spells the change kind
// as an object, so passing `changes` through verbatim left a client with nothing it
// could read as a status — and put a provider DTO on AO's wire.
func TestNormalizeFileChangeItemProducesNeutralFiles(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemStarted,
		`{"threadId":"th","turnId":"tu","startedAtMs":1785702980000,"item":{"type":"fileChange","id":"exec-adde2cb3","status":"inProgress","changes":[{"path":"/ws/hello.txt","kind":{"type":"update","move_path":null},"diff":"@@ -2 +2,2 @@\n two\n+three\n"},{"path":"/ws/notes.md","kind":{"type":"add"},"diff":"This is a note.\nIt is short.\n"}]}}`)

	if ev.Kind != ports.ChatEventActivityStarted || ev.ActivityKind != domain.ActivityKindFileChange {
		t.Fatalf("event -> %+v", ev)
	}
	if ev.Summary != "Edited 2 files" {
		t.Fatalf("summary = %q", ev.Summary)
	}

	files := decodeFiles(t, ev.Detail)
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	if files[0].Path != "/ws/hello.txt" || files[0].Status != "modified" {
		t.Errorf("file 0 = %+v", files[0])
	}
	if files[0].Additions != 1 || files[0].Deletions != 0 {
		t.Errorf("file 0 counts = +%d/-%d, want +1/-0", files[0].Additions, files[0].Deletions)
	}
	// The patch text is carried so a client can render the diff the moment the
	// patch lands, instead of waiting for the turn's aggregate.
	if files[0].Patch == "" {
		t.Error("file 0 carried no patch text")
	}
	// An added file's patch is the file's contents with no hunk header. Counting
	// +/- prefixes there would report deletions the change never made.
	if files[1].Status != "added" || files[1].Additions != 2 || files[1].Deletions != 0 {
		t.Errorf("file 1 = %+v", files[1])
	}
}

// A single-file change says which file, because "Edited files" read the same
// whether the agent renamed one path or rewrote thirty.
func TestNormalizeFileChangeSummaryNamesOneFile(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemCompleted,
		`{"threadId":"th","turnId":"tu","item":{"type":"fileChange","id":"i","changes":[{"path":"notes.md","kind":{"type":"add"},"diff":"one\n"}]}}`)
	if ev.Summary != "Created notes.md" {
		t.Fatalf("summary = %q", ev.Summary)
	}
}

// A move arrives as an update carrying the destination. The path AO shows must be
// where the file ended up, with the old path recorded as the source.
func TestNormalizeFileChangeMoveBecomesRename(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemCompleted,
		`{"threadId":"th","turnId":"tu","item":{"type":"fileChange","id":"i","changes":[{"path":"old.txt","kind":{"type":"update","move_path":"new.txt"},"diff":"@@ -1 +1 @@\n-a\n+b\n"}]}}`)
	files := decodeFiles(t, ev.Detail)
	if len(files) != 1 {
		t.Fatalf("files = %d", len(files))
	}
	if files[0].Path != "new.txt" || files[0].OldPath != "old.txt" || files[0].Status != "renamed" {
		t.Fatalf("file = %+v", files[0])
	}
}

// item/fileChange/patchUpdated was NEVER OBSERVED across four probes, including a
// single 250-line patch: 0.146.0 sends the complete changes array on item/started.
// It is handled as an update to the SAME activity so a build that does stream a
// patch rewrites the row instead of leaving the first frame's contents.
func TestNormalizePatchUpdatedUpdatesTheSameActivity(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemFileChangePatchUpdated,
		`{"threadId":"th","turnId":"tu","itemId":"exec-1","changes":[{"path":"a.py","kind":{"type":"update"},"diff":"@@ -1 +1,2 @@\n a\n+b\n"}]}`)
	if ev.Kind != ports.ChatEventActivityStarted {
		t.Fatalf("kind = %q, want the activity to be updated in place", ev.Kind)
	}
	if ev.ProviderItemID != "exec-1" || ev.ActivityKind != domain.ActivityKindFileChange {
		t.Fatalf("event -> %+v", ev)
	}
	if ev.ActivityStatus != domain.ActivityStatusRunning {
		t.Fatalf("status = %q: a restated patch is not a completion", ev.ActivityStatus)
	}
	if files := decodeFiles(t, ev.Detail); len(files) != 1 || files[0].Path != "a.py" {
		t.Fatalf("files = %+v", files)
	}
	normalizeNone(t, codexproto.MethodItemFileChangePatchUpdated,
		`{"threadId":"th","turnId":"tu","itemId":"exec-1","changes":[]}`)
}

// The generated schema documents item/fileChange/outputDelta as no longer emitted,
// and no probe saw one. Handled so a build that still sends it is not dropped.
func TestNormalizeFileChangeOutputDeltaIsText(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemFileChangeOutputDelta,
		`{"threadId":"th","turnId":"tu","itemId":"exec-1","delta":"applying patch\n"}`)
	if ev.Kind != ports.ChatEventActivityText || ev.Delta != "applying patch\n" {
		t.Fatalf("file change output -> %+v", ev)
	}
}

/* ---- MCP --------------------------------------------------------------- */

// The bug this fixes: the provider sends `tool`, not `toolName`. Captured from a
// live MCP call. The old struct read toolName, which nothing sends, so every MCP
// call in the timeline was labelled "Called tool" with no arguments and no result.
func TestNormalizeMcpToolCallReadsServerToolArgumentsAndResult(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemCompleted,
		`{"threadId":"th","turnId":"tu","completedAtMs":1785703100000,"item":{"type":"mcpToolCall","id":"exec-b0ff0c94","server":"probe","tool":"slow_echo","status":"completed","arguments":{"subject":"hydraulics"},"appContext":null,"pluginId":null,"result":{"content":[{"type":"text","text":"probe tool finished"}],"structuredContent":null,"_meta":null},"error":null,"durationMs":3014}}`)

	if ev.ActivityKind != domain.ActivityKindMCPTool {
		t.Fatalf("kind = %q, want %q", ev.ActivityKind, domain.ActivityKindMCPTool)
	}
	if ev.Summary != "Called probe/slow_echo" {
		t.Fatalf("summary = %q", ev.Summary)
	}
	if ev.ActivityStatus != domain.ActivityStatusCompleted {
		t.Fatalf("status = %q", ev.ActivityStatus)
	}

	detail := decodeDetailAny(t, ev.Detail)
	if detail["toolName"] != "slow_echo" || detail["server"] != "probe" {
		t.Fatalf("detail names = %+v", detail)
	}
	// What the tool was called WITH. "called search" says nothing about what was
	// searched for, and a user cannot check a claim they cannot see.
	args, ok := detail["arguments"].(map[string]any)
	if !ok || args["subject"] != "hydraulics" {
		t.Fatalf("arguments = %#v", detail["arguments"])
	}
	if _, ok := detail["result"].(map[string]any); !ok {
		t.Fatalf("result = %#v", detail["result"])
	}
	if detail["durationMs"] != float64(3014) {
		t.Fatalf("durationMs = %#v", detail["durationMs"])
	}
}

// An MCP tool reports failure in its own payload, not through an exit code. A call
// whose error is recorded but whose status stays "completed" would read as success.
func TestNormalizeMcpToolCallFailureIsFailed(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemCompleted,
		`{"threadId":"th","turnId":"tu","item":{"type":"mcpToolCall","id":"i","server":"s","tool":"t","error":{"message":"tool exploded"}}}`)
	if ev.ActivityStatus != domain.ActivityStatusFailed {
		t.Fatalf("status = %q, want failed", ev.ActivityStatus)
	}
	if detail := decodeDetailMap(t, ev.Detail); detail["error"] != "tool exploded" {
		t.Fatalf("detail = %+v", detail)
	}
}

// A tool can be handed a whole file and hand one back, and this payload is re-read
// by every snapshot poll. Over the cap the JSON is replaced by a marker rather than
// cut, because half a JSON document is not JSON.
func TestNormalizeMcpToolPayloadOverCapIsMarkedNotCut(t *testing.T) {
	big := strings.Repeat("x", maxToolPayloadChars+10)
	params := `{"threadId":"th","turnId":"tu","item":{"type":"mcpToolCall","id":"i","server":"s","tool":"t","arguments":{"blob":"` + big + `"}}}`
	ev := normalizeOne(t, codexproto.MethodItemCompleted, params)

	detail := decodeDetailAny(t, ev.Detail)
	args, ok := detail["arguments"].(map[string]any)
	if !ok {
		t.Fatalf("arguments = %#v", detail["arguments"])
	}
	if args["truncated"] != true {
		t.Fatalf("oversized arguments were not marked truncated: %#v", args)
	}
	if _, stillThere := args["blob"]; stillThere {
		t.Fatal("oversized payload was stored anyway")
	}
}

// MCP progress was NEVER OBSERVED: a probe MCP server that sent
// notifications/progress with the token Codex supplied produced no app-server
// notification, so 0.146.0 does not appear to forward it.
func TestNormalizeMcpToolProgressIsActivityText(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemMcpToolCallProgress,
		`{"threadId":"th","turnId":"tu","itemId":"exec-1","message":"probe step 1 of 3"}`)
	if ev.Kind != ports.ChatEventActivityText {
		t.Fatalf("kind = %q", ev.Kind)
	}
	if ev.Delta != "probe step 1 of 3\n" {
		t.Fatalf("delta = %q, want a newline-terminated progress line", ev.Delta)
	}
	if ev.ProviderItemID != "exec-1" {
		t.Fatalf("item id = %q", ev.ProviderItemID)
	}
}

// Captured live: every configured server reports starting, then ready or failed.
// Previously dropped as bookkeeping, which left a tool call that never happened
// because its server never started looking like the agent choosing not to use it.
func TestNormalizeMcpServerStartupStatus(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodMcpServerStartupStatusUpdated,
		`{"threadId":"019fc432","name":"probe","status":"ready","error":null,"failureReason":null}`)
	if ev.Kind != ports.ChatEventMCPServers {
		t.Fatalf("kind = %q", ev.Kind)
	}
	if len(ev.MCPServers) != 1 {
		t.Fatalf("servers = %d", len(ev.MCPServers))
	}
	if ev.MCPServers[0].Name != "probe" || ev.MCPServers[0].Status != "ready" {
		t.Fatalf("server = %+v", ev.MCPServers[0])
	}

	failed := normalizeOne(t, codexproto.MethodMcpServerStartupStatusUpdated,
		`{"threadId":"th","name":"github","status":"failed","error":"connection refused","failureReason":"reauthenticationRequired"}`)
	if failed.MCPServers[0].Error != "connection refused" {
		t.Errorf("error = %q", failed.MCPServers[0].Error)
	}
	// The classification is actionable in a way a message is not.
	if failed.MCPServers[0].FailureReason != "reauthenticationRequired" {
		t.Errorf("failure reason = %q", failed.MCPServers[0].FailureReason)
	}

	// A report naming no server says nothing about any server.
	normalizeNone(t, codexproto.MethodMcpServerStartupStatusUpdated, `{"threadId":"th","status":"ready"}`)
}

/* ---- terminal interaction ---------------------------------------------- */

// Captured live: asking for a persistent python REPL produced this, carrying the
// keystrokes the agent sent to the PTY of a commandExecution item.
//
// It is NOT output. The PTY echoes what is typed — measured, as ANSI
// insert-character sequences in the output stream — so folding it into the output
// would print it twice.
func TestNormalizeTerminalInteraction(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemCommandExecutionTerminalInteraction,
		`{"threadId":"019fc435","turnId":"019fc435-ff11","itemId":"exec-cbf47a22","processId":"7409","stdin":"print(6*7)\n"}`)

	if ev.Kind != ports.ChatEventCommandInput {
		t.Fatalf("kind = %q, want %q", ev.Kind, ports.ChatEventCommandInput)
	}
	if ev.Delta != "print(6*7)\n" {
		t.Fatalf("delta = %q", ev.Delta)
	}
	if ev.ProviderItemID != "exec-cbf47a22" {
		t.Fatalf("item id = %q", ev.ProviderItemID)
	}
	// The PTY the keystrokes went to, so two concurrent sessions can be told apart.
	if detail := decodeDetailMap(t, ev.Detail); detail["processId"] != "7409" {
		t.Fatalf("detail = %+v", detail)
	}

	normalizeNone(t, codexproto.MethodItemCommandExecutionTerminalInteraction,
		`{"itemId":"exec-1","processId":"1","stdin":""}`)
}

// The command item carries the PTY id, which is what correlates the keystrokes
// above with the command they were typed into.
func TestNormalizeCommandItemCarriesProcessID(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodItemStarted,
		`{"threadId":"th","turnId":"tu","startedAtMs":1,"item":{"type":"commandExecution","id":"exec-1","command":"/bin/zsh -lc python3","cwd":"/ws","processId":"7409","source":"unifiedExecStartup","status":"inProgress"}}`)
	if detail := decodeDetailMap(t, ev.Detail); detail["processId"] != "7409" {
		t.Fatalf("detail = %+v", detail)
	}
}

/* ---- auto-approval review ---------------------------------------------- */

// Captured live with approvalsReviewer=auto_review: a curl command produced
// started, then completed carrying riskLevel "low" and the reviewer's rationale.
//
// This is consent nobody gave explicitly, which is exactly why it has to be
// visible: a user who can see "auto-approved as low risk, here is why" can
// disagree with it.
func TestNormalizeAutoApprovalReview(t *testing.T) {
	started := normalizeOne(t, codexproto.MethodItemAutoApprovalReviewStarted,
		`{"threadId":"019fc434","turnId":"019fc434-39c6","startedAtMs":1785703203778,"reviewId":"5b95c779","targetItemId":"exec-de86ce2c","review":{"status":"inProgress","riskLevel":null,"userAuthorization":null,"rationale":null},"action":{"type":"command","source":"unifiedExec","command":"/bin/zsh -lc 'curl -s https://example.com'","cwd":"/ws"}}`)

	if started.Kind != ports.ChatEventActivityStarted {
		t.Fatalf("kind = %q", started.Kind)
	}
	if started.ActivityKind != domain.ActivityKindAutoReview {
		t.Fatalf("activity kind = %q, want %q", started.ActivityKind, domain.ActivityKindAutoReview)
	}
	if started.ActivityStatus != domain.ActivityStatusRunning {
		t.Fatalf("status = %q", started.ActivityStatus)
	}
	// The provider gives a reviewId but no item id. The synthetic key is what makes
	// started and completed update ONE row instead of creating two.
	if started.ProviderItemID != "ao-review-5b95c779" {
		t.Fatalf("item id = %q", started.ProviderItemID)
	}
	if started.Summary != "Reviewing curl -s https://example.com" {
		t.Fatalf("summary = %q", started.Summary)
	}

	completed := normalizeOne(t, codexproto.MethodItemAutoApprovalReviewCompleted,
		`{"threadId":"019fc434","turnId":"019fc434-39c6","startedAtMs":1785703203778,"completedAtMs":1785703208165,"reviewId":"5b95c779","targetItemId":"exec-de86ce2c","decisionSource":"agent","review":{"status":"approved","riskLevel":"low","userAuthorization":"unknown","rationale":"Auto-review returned a low-risk allow decision."},"action":{"type":"command","source":"unifiedExec","command":"/bin/zsh -lc 'curl -s https://example.com'","cwd":"/ws"}}`)

	if completed.ProviderItemID != started.ProviderItemID {
		t.Fatalf("completed keyed on %q, started on %q", completed.ProviderItemID, started.ProviderItemID)
	}
	if completed.ActivityStatus != domain.ActivityStatusCompleted {
		t.Fatalf("status = %q", completed.ActivityStatus)
	}
	if completed.Summary != "Auto-approved curl -s https://example.com (low risk)" {
		t.Fatalf("summary = %q", completed.Summary)
	}

	detail := decodeDetailMap(t, completed.Detail)
	for key, want := range map[string]string{
		"reviewId":          "5b95c779",
		"actionType":        "command",
		"status":            "approved",
		"targetItemId":      "exec-de86ce2c",
		"riskLevel":         "low",
		"userAuthorization": "unknown",
		"rationale":         "Auto-review returned a low-risk allow decision.",
		"decisionSource":    "agent",
		"command":           "curl -s https://example.com",
		"cwd":               "/ws",
		"commandSource":     "unifiedExec",
	} {
		if detail[key] != want {
			t.Errorf("detail[%q] = %q, want %q", key, detail[key], want)
		}
	}
}

// A review that did not approve must not read as approval. timedOut and aborted
// mean the action never got consent.
func TestNormalizeAutoApprovalNonApprovalIsFailed(t *testing.T) {
	for _, status := range []string{"denied", "timedOut", "aborted"} {
		ev := normalizeOne(t, codexproto.MethodItemAutoApprovalReviewCompleted,
			`{"threadId":"th","turnId":"tu","reviewId":"r","startedAtMs":1,"completedAtMs":2,"decisionSource":"agent","review":{"status":"`+status+`"},"action":{"type":"command","command":"rm -rf /"}}`)
		if ev.ActivityStatus != domain.ActivityStatusFailed {
			t.Errorf("%s -> status %q, want failed", status, ev.ActivityStatus)
		}
	}
}

/* ---- model reroute ----------------------------------------------------- */

// NEVER OBSERVED: the only reason the schema declares is highRiskCyberActivity,
// which a test cannot honestly provoke. Handled because of the failure it prevents
// rather than how often it happens: the composer names the model it is sending to,
// so an unrecorded substitution attributes the answer to a model that did not
// produce it.
func TestNormalizeModelRerouted(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodModelRerouted,
		`{"threadId":"th","turnId":"tu","fromModel":"gpt-5.6-sol","toModel":"gpt-5.6-safety","reason":"highRiskCyberActivity"}`)
	if ev.Kind != ports.ChatEventModelRerouted {
		t.Fatalf("kind = %q", ev.Kind)
	}
	if ev.Reroute == nil {
		t.Fatal("no reroute payload")
	}
	if ev.Reroute.FromModel != "gpt-5.6-sol" || ev.Reroute.ToModel != "gpt-5.6-safety" {
		t.Fatalf("reroute = %+v", ev.Reroute)
	}
	if ev.Reroute.Reason != "highRiskCyberActivity" {
		t.Fatalf("reason = %q", ev.Reroute.Reason)
	}
	if ev.ProviderTurnID != "tu" {
		t.Fatalf("turn id = %q: a reroute belongs to the turn it happened on", ev.ProviderTurnID)
	}

	// A reroute that names no destination says nothing usable.
	normalizeNone(t, codexproto.MethodModelRerouted, `{"threadId":"th","turnId":"tu","fromModel":"a"}`)
}

/* ---- account ----------------------------------------------------------- */

// NEVER OBSERVED: account/updated fires on login, logout and plan changes, none of
// which a test may do to a real account. Two optional enums, so reading it is
// unambiguous even unverified.
func TestNormalizeAccountUpdated(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodAccountUpdated, `{"authMode":"chatgpt","planType":"pro"}`)
	if ev.Kind != ports.ChatEventAccountChanged {
		t.Fatalf("kind = %q", ev.Kind)
	}
	if ev.Account.AuthMode != "chatgpt" || ev.Account.PlanLabel != "pro" {
		t.Fatalf("account = %+v", ev.Account)
	}
	if ev.Account.ReauthRequired {
		t.Error("an account update is not a demand for credentials")
	}

	// A report that says nothing is not a change.
	normalizeNone(t, codexproto.MethodAccountUpdated, `{}`)
}

/* ---- thread lifecycle -------------------------------------------------- */

// Captured live: active while a turn runs, idle when it ends.
func TestNormalizeThreadStatusChanged(t *testing.T) {
	active := normalizeOne(t, codexproto.MethodThreadStatusChanged,
		`{"threadId":"019fc432","status":{"type":"active","activeFlags":[]}}`)
	if active.Kind != ports.ChatEventThreadState {
		t.Fatalf("kind = %q", active.Kind)
	}
	if active.ThreadState.Status != domain.ThreadStatusActive {
		t.Fatalf("status = %q", active.ThreadState.Status)
	}

	idle := normalizeOne(t, codexproto.MethodThreadStatusChanged,
		`{"threadId":"019fc432","status":{"type":"idle"}}`)
	if idle.ThreadState.Status != domain.ThreadStatusIdle {
		t.Fatalf("status = %q", idle.ThreadState.Status)
	}

	systemError := normalizeOne(t, codexproto.MethodThreadStatusChanged,
		`{"threadId":"019fc432","status":{"type":"systemError"}}`)
	if systemError.ThreadState.Status != domain.ThreadStatusSystemError {
		t.Fatalf("status = %q", systemError.ThreadState.Status)
	}

	// A thread can be working AND blocked on a person; those are different states.
	blocked := normalizeOne(t, codexproto.MethodThreadStatusChanged,
		`{"threadId":"th","status":{"type":"active","activeFlags":["waitingOnApproval"]}}`)
	if len(blocked.ThreadState.WaitingOn) != 1 || blocked.ThreadState.WaitingOn[0] != "waiting_on_approval" {
		t.Fatalf("waiting on = %+v", blocked.ThreadState.WaitingOn)
	}

	// A status this build does not recognize is not flattened into one it does.
	normalizeNone(t, codexproto.MethodThreadStatusChanged,
		`{"threadId":"th","status":{"type":"someFutureStatus"}}`)
}

// Captured live: thread/archive and thread/unarchive each produced their
// notification. Archiving is reversible, so Archived is tri-state: a report that
// says nothing about archiving must not be read as "not archived".
func TestNormalizeThreadArchiveIsTriState(t *testing.T) {
	archived := normalizeOne(t, codexproto.MethodThreadArchived, `{"threadId":"019fc430"}`)
	if archived.ThreadState.Archived == nil || !*archived.ThreadState.Archived {
		t.Fatalf("archived -> %+v", archived.ThreadState)
	}

	unarchived := normalizeOne(t, codexproto.MethodThreadUnarchived, `{"threadId":"019fc430"}`)
	if unarchived.ThreadState.Archived == nil || *unarchived.ThreadState.Archived {
		t.Fatalf("unarchived -> %+v", unarchived.ThreadState)
	}

	status := normalizeOne(t, codexproto.MethodThreadStatusChanged,
		`{"threadId":"th","status":{"type":"idle"}}`)
	if status.ThreadState.Archived != nil {
		t.Fatal("a status report claimed to know whether the thread is archived")
	}
}

// NEVER OBSERVED: thread/unsubscribe returned {"status":"unsubscribed"} and
// emitted nothing. Recorded rather than acted on — tearing a controller down on an
// unverified notification would turn a provider quirk into a lost session.
func TestNormalizeThreadClosed(t *testing.T) {
	ev := normalizeOne(t, codexproto.MethodThreadClosed, `{"threadId":"th"}`)
	if !ev.ThreadState.Closed || ev.ThreadState.Status != domain.ThreadStatusClosed {
		t.Fatalf("closed -> %+v", ev.ThreadState)
	}
	if ev.Kind == ports.ChatEventControllerState {
		t.Fatal("a closed thread must not be reported as the controller dying")
	}
}

/* ---- regression guards ------------------------------------------------- */

// Malformed params on the newly handled methods must be skipped, never panic.
func TestNormalizeNewMethodsTolerateMalformedParams(t *testing.T) {
	for _, method := range []string{
		codexproto.MethodItemReasoningSummaryTextDelta,
		codexproto.MethodItemReasoningSummaryPartAdded,
		codexproto.MethodItemReasoningTextDelta,
		codexproto.MethodTurnPlanUpdated,
		codexproto.MethodItemPlanDelta,
		codexproto.MethodItemFileChangePatchUpdated,
		codexproto.MethodItemFileChangeOutputDelta,
		codexproto.MethodItemMcpToolCallProgress,
		codexproto.MethodMcpServerStartupStatusUpdated,
		codexproto.MethodItemCommandExecutionTerminalInteraction,
		codexproto.MethodItemAutoApprovalReviewStarted,
		codexproto.MethodItemAutoApprovalReviewCompleted,
		codexproto.MethodModelRerouted,
		codexproto.MethodAccountUpdated,
		codexproto.MethodThreadStatusChanged,
	} {
		t.Run(method, func(t *testing.T) { normalizeNone(t, method, `"not an object"`) })
	}
}

// Every method this file claims to handle must be one the generating provider
// actually declares. A method name that is not in the provider's own schema is
// either a typo or a handler for something that can never arrive.
func TestHandledMethodsAreDeclaredByTheProvider(t *testing.T) {
	for _, method := range []string{
		codexproto.MethodItemReasoningSummaryTextDelta,
		codexproto.MethodItemReasoningSummaryPartAdded,
		codexproto.MethodItemReasoningTextDelta,
		codexproto.MethodTurnPlanUpdated,
		codexproto.MethodItemPlanDelta,
		codexproto.MethodItemFileChangePatchUpdated,
		codexproto.MethodItemFileChangeOutputDelta,
		codexproto.MethodItemMcpToolCallProgress,
		codexproto.MethodMcpServerStartupStatusUpdated,
		codexproto.MethodConfigMcpServerReload,
		codexproto.MethodMcpServerStatusList,
		codexproto.MethodItemCommandExecutionTerminalInteraction,
		codexproto.MethodItemAutoApprovalReviewStarted,
		codexproto.MethodItemAutoApprovalReviewCompleted,
		codexproto.MethodModelRerouted,
		codexproto.MethodAccountUpdated,
		codexproto.MethodAccountChatgptAuthTokensRefresh,
		codexproto.MethodItemToolCall,
		codexproto.MethodThreadStatusChanged,
		codexproto.MethodThreadArchived,
		codexproto.MethodThreadUnarchived,
		codexproto.MethodThreadClosed,
	} {
		if !codexproto.Declares(method) {
			t.Errorf("%s is handled but not declared by %s", method, codexproto.ProviderVersion)
		}
	}
}

/* ---- helpers ----------------------------------------------------------- */

func decodeDetailMap(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("detail is not an object: %v (%s)", err, raw)
	}
	out := map[string]string{}
	for key, value := range decoded {
		switch typed := value.(type) {
		case string:
			out[key] = typed
		case float64:
			out[key] = strings.TrimSuffix(strings.TrimRight(formatFloat(typed), "0"), ".")
		}
	}
	return out
}

func formatFloat(value float64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func decodeDetailAny(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("detail is not an object: %v (%s)", err, raw)
	}
	return out
}

// decodeFiles reads the files a client sees. Lowercase keys on purpose: the port
// type has no JSON tags, so serializing it directly would emit Go field names.
func decodeFiles(t *testing.T, raw []byte) []fileChangeDetail {
	t.Helper()
	var detail struct {
		Files []fileChangeDetail `json:"files"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("detail is not an object: %v (%s)", err, raw)
	}
	return detail.Files
}
