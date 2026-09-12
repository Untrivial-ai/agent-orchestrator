package codexappserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/commanddetail"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Translation from Codex app-server notifications into AO's provider-neutral
// Chat events.
//
// Two rules hold here.
//
// First: method names and payload shapes come from codexproto, which is generated
// from the provider's own `generate-json-schema` output. A method literal in this
// file would be a runtime typo; a hand-written payload struct would drift from the
// provider silently. Where a struct is declared locally it is because the provider
// does NOT declare the field (see threadItem) and the reason is stated.
//
// Second: the schema says what CAN arrive, and only a live probe says what DOES.
// Every "verified" note below is a frame captured from codex-cli 0.146.0 driving a
// real account; every "never observed" note is a method this build did not emit in
// any probe, handled for forward compatibility and marked so nobody mistakes it for
// a tested path.

// Codex thread item types. Aliased to the generated discriminator so a renamed
// item type breaks the build instead of quietly matching nothing.
const (
	itemUserMessage      = codexproto.ThreadItemTypeUserMessage
	itemAgentMessage     = codexproto.ThreadItemTypeAgentMessage
	itemReasoning        = codexproto.ThreadItemTypeReasoning
	itemPlan             = codexproto.ThreadItemTypePlan
	itemCommandExecution = codexproto.ThreadItemTypeCommandExecution
	itemFileChange       = codexproto.ThreadItemTypeFileChange
	itemMcpToolCall      = codexproto.ThreadItemTypeMcpToolCall
	itemWebSearch        = codexproto.ThreadItemTypeWebSearch
	// itemContextCompaction is how a current app-server reports that it summarized
	// earlier history to reclaim context. The schema also declares a
	// `thread/compacted` notification for the same thing and marks it "Deprecated:
	// use the ContextCompaction item type instead" — and 0.146.0 emits ONLY the
	// item, never the notification. Reading the notification alone would mean AO
	// silently never noticed a compaction on any current provider.
	itemContextCompaction = codexproto.ThreadItemTypeContextCompaction
)

// itemError is an item type the generated schema does NOT declare: ThreadItemType
// has no error variant in 0.146.0, and ThreadItem has no message field. It is kept
// because an older build emitted it and dropping the handling would silently lose
// a provider error, and it is spelled out here rather than in codexproto so the
// generated file stays a faithful copy of what the provider declares.
const itemError codexproto.ThreadItemType = "error"

// threadItem is codexproto.ThreadItem plus the one field the provider does not
// declare.
//
// Embedding rather than copying: the generated struct covers every field AO reads,
// including the two the previous hand-written version got wrong. `tool`, not
// `toolName`, is what an mcpToolCall item actually carries — verified against a
// live call — so the old struct's ToolName never bound and every MCP call in the
// timeline was labelled "Called tool".
type threadItem struct {
	codexproto.ThreadItem
	// Message carries the text of the undeclared error item above.
	Message string `json:"message"`
}

// itemID reports the provider's item id, which the generated schema makes optional.
func (it threadItem) itemID() string { return deref(it.ID) }

// contextEnvelope reads the conversation's position in the model's context out of
// a thread/tokenUsage/updated notification.
//
// `last` and `total` are different questions and only one of them answers "how
// full is this conversation". Measured across a compaction: total stayed at 15650
// (cumulative spend, which compaction cannot undo) while last fell from 15650 to
// 4632. So last.totalTokens is the context position, and it is the only figure
// from which a reclaim can be computed.
type contextEnvelope struct {
	TokenUsage struct {
		Last struct {
			TotalTokens int64 `json:"totalTokens"`
		} `json:"last"`
		ModelContextWindow *int64 `json:"modelContextWindow"`
	} `json:"tokenUsage"`
}

// contextPositionFrom reports the conversation's context position, or ok=false for
// a notification that is not a token-usage report.
func contextPositionFrom(n notification) (used, window int64, ok bool) {
	if n.Method != codexproto.MethodThreadTokenUsageUpdated {
		return 0, 0, false
	}
	var p contextEnvelope
	if err := json.Unmarshal(n.Params, &p); err != nil {
		return 0, 0, false
	}
	if p.TokenUsage.ModelContextWindow != nil {
		window = *p.TokenUsage.ModelContextWindow
	}
	return p.TokenUsage.Last.TotalTokens, window, true
}

// resolvedEnvelope is the params shape of serverRequest/resolved.
//
// json.RawMessage rather than codexproto.RequestID, which is a DEFINED type over
// json.RawMessage and so does not inherit its UnmarshalJSON. Decoding into it makes
// encoding/json treat the field as a byte slice and demand base64, which fails on
// the numeric ids the provider actually sends (observed: 0, 1, 2) and takes the
// whole notification down with it. A missed correlation key leaves an approval card
// actionable after the provider has already answered it, so this is read directly.
//
// `id` is tolerated alongside `requestId` for a build that renames the field.
type resolvedEnvelope struct {
	ThreadID  string          `json:"threadId"`
	RequestID json.RawMessage `json:"requestId"`
	ID        json.RawMessage `json:"id"`
}

// normalizeNotification converts one provider notification into zero or more
// neutral events. Returning nil means "not conversation-relevant".
//
// now is passed rather than read from the clock so the rate-limit reset instant
// can be expressed as a duration deterministically. The provider reports an
// absolute timestamp; a duration is what a reader can act on without knowing
// whether AO's clock agrees with the provider's.
func normalizeNotification(n notification, now time.Time) []ports.ChatEvent {
	switch n.Method {
	case codexproto.MethodTurnStarted:
		var p codexproto.TurnStartedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:                   ports.ChatEventTurnStarted,
			ProviderTurnID:         firstNonEmpty(p.Turn.ID, turnIDFallback(n.Params)),
			ProviderConversationID: p.ThreadID,
		}}

	case codexproto.MethodTurnCompleted:
		var p codexproto.TurnCompletedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		ev := ports.ChatEvent{
			Kind:                   ports.ChatEventTurnCompleted,
			ProviderTurnID:         firstNonEmpty(p.Turn.ID, turnIDFallback(n.Params)),
			ProviderConversationID: p.ThreadID,
			TurnState:              turnStateFrom(string(p.Turn.Status)),
		}
		if p.Turn.Error != nil && p.Turn.Error.Message != "" {
			ev.Err = fmt.Errorf("%s", p.Turn.Error.Message)
		}
		return []ports.ChatEvent{ev}

	case codexproto.MethodItemAgentMessageDelta:
		var p codexproto.AgentMessageDeltaNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.Delta == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventMessageDelta,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			Delta:          p.Delta,
		}}

	case codexproto.MethodItemReasoningSummaryTextDelta:
		// Streamed reasoning summary. VERIFIED live: a turn on gpt-5.6-sol with
		// model_reasoning_summary=detailed produced summaryPartAdded followed by
		// summaryTextDelta for each reasoning item, and the same text arrived again in
		// the item's `summary` array on item/completed.
		//
		// Without this, reasoning only existed as the completed item — and the
		// previous build did not read `summary` either, so a reasoning row carried no
		// text at all, ever. Streaming it is what makes "thinking" visible while it
		// happens rather than as an empty placeholder that never fills in.
		var p codexproto.ReasoningSummaryTextDeltaNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.Delta == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventReasoningDelta,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			Delta:          p.Delta,
		}}

	case codexproto.MethodItemReasoningSummaryPartAdded:
		// A new summary section starts. VERIFIED live, always immediately before the
		// first delta of each part.
		//
		// It becomes a separator rather than an event of its own: the parts are
		// consecutive chunks of one reasoning stream, and concatenating them without a
		// break would run the end of one thought into the start of the next. Part 0
		// needs no separator, which is why the index is what decides — a stateless
		// rule, so a reconnect that misses part 0 cannot leave the stream permanently
		// mis-joined.
		var p codexproto.ReasoningSummaryPartAddedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.SummaryIndex <= 0 {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventReasoningDelta,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			Delta:          "\n\n",
		}}

	case codexproto.MethodItemReasoningTextDelta:
		// Raw reasoning, as opposed to the summary above. NEVER OBSERVED: probed with
		// show_raw_agent_reasoning=true on a ChatGPT-authed account and the provider
		// emitted only summaries. Handled because a build or model that does expose
		// raw reasoning would otherwise show an empty reasoning row.
		//
		// It folds into the same accumulation as the summary. A build emitting both
		// would interleave them in the provider's own order, which is the best account
		// available: AO cannot know which stream a reader wanted, and dropping one
		// would silently discard reasoning the provider chose to send.
		var p codexproto.ReasoningTextDeltaNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.Delta == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventReasoningDelta,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			Delta:          p.Delta,
		}}

	case codexproto.MethodItemCommandExecutionOutputDelta:
		// Streamed stdout/stderr from a running command. Two things make this worth
		// accumulating rather than waiting for aggregatedOutput on item/completed:
		// the aggregate does not exist until the command ends, so a long command
		// shows nothing while it runs; and a commandExecution that never completes
		// (three starts producing one completion has been observed) has no
		// aggregate at all, only these.
		//
		// It does NOT make the record complete. Measured on codex-cli 0.146.0: a
		// command printing tick-1..tick-8 one per second produced 7 deltas starting
		// at tick-2, and an aggregate of exactly those same 7 lines. The first
		// chunk is lost upstream of both channels, so a reader still has to say the
		// output may be partial.
		var p codexproto.CommandExecutionOutputDeltaNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.Delta == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventCommandOutputDelta,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			Delta:          p.Delta,
		}}

	case codexproto.MethodItemCommandExecutionTerminalInteraction:
		// The agent typed into a running command's terminal. VERIFIED live: asking for
		// a persistent python REPL produced one of these carrying stdin
		// "print(6*7)\n" against the PTY process id of the commandExecution item.
		//
		// Reported as its own kind, not as output. The PTY echoes what is typed —
		// measured, as ANSI insert-character sequences in the output stream — so
		// appending it to the output would duplicate it. Without this, a transcript
		// shows a REPL answering questions nobody can see being asked.
		var p codexproto.TerminalInteractionNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.Stdin == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventCommandInput,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			Delta:          p.Stdin,
			// The PTY the keystrokes went to, so a reader can tell two concurrent
			// sessions apart rather than assuming one terminal per turn.
			Detail: encodeDetail(map[string]any{"processId": p.ProcessID}),
		}}

	case codexproto.MethodItemMcpToolCallProgress:
		// Progress from a long MCP tool call. NEVER OBSERVED: a probe MCP server that
		// sent notifications/progress with the progressToken Codex supplied produced
		// no such notification from app-server, so 0.146.0 does not appear to forward
		// MCP progress. Handled because the cost is a few lines and the alternative,
		// for a build that does forward it, is a tool call that sits silent for
		// minutes.
		var p codexproto.McpToolCallProgressNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.Message == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventActivityText,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			Delta:          p.Message + "\n",
		}}

	case codexproto.MethodItemFileChangeOutputDelta:
		// Textual output from apply_patch. The generated schema documents this as
		// "Deprecated legacy notification ... The server no longer emits this
		// notification", and no probe saw one. Handled so a provider that still does
		// is not silently dropped; not a tested path.
		var p codexproto.FileChangeOutputDeltaNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.Delta == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventActivityText,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			Delta:          p.Delta,
		}}

	case codexproto.MethodItemFileChangePatchUpdated:
		// One file-change item's patch, restated. NEVER OBSERVED across four probes
		// including a single 250-line patch and a three-file patch: 0.146.0 sends the
		// complete `changes` array on item/started instead, immediately followed by
		// item/completed.
		//
		// Handled anyway, and deliberately as an update to the SAME activity the item
		// created: a build that streams a patch would otherwise leave the row showing
		// whatever the first frame contained. It does not compete with
		// turn/diff/updated, which answers a different question (what the whole turn
		// changed, aggregated) and lands on the turn rather than on an item.
		var p codexproto.FileChangePatchUpdatedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || len(p.Changes) == 0 {
			return nil
		}
		files := fileChanges(p.Changes)
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventActivityStarted,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			ActivityKind:   domain.ActivityKindFileChange,
			ActivityStatus: domain.ActivityStatusRunning,
			Summary:        fileChangeSummary(files),
			Detail:         encodeDetail(map[string]any{"files": files}),
		}}

	case codexproto.MethodItemPlanDelta:
		// Streaming of the plan tool's own text. NEVER OBSERVED: in a probe where the
		// agent used update_plan three times, the plan arrived only as
		// turn/plan/updated and no `plan` item was ever created. Handled as an
		// activity text stream rather than as a plan, because a partial plan is text
		// and parsing half of one into steps would invent structure.
		var p codexproto.PlanDeltaNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.Delta == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventActivityText,
			ProviderTurnID: p.TurnID,
			ProviderItemID: p.ItemID,
			Delta:          p.Delta,
		}}

	case codexproto.MethodTurnPlanUpdated:
		// The agent's plan for the turn. VERIFIED live: three updates in one turn,
		// each carrying the WHOLE step list with per-step status, which is why this is
		// turn state a projection overwrites rather than a timeline row per update.
		var p codexproto.TurnPlanUpdatedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		plan := planFrom(p)
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventPlanUpdated,
			ProviderTurnID: p.TurnID,
			Plan:           &plan,
			Summary:        planSummary(plan),
		}}

	case codexproto.MethodItemAutoApprovalReviewStarted:
		// The provider is deciding, on the user's behalf, whether an action is safe to
		// run unattended. VERIFIED live with approvalsReviewer=auto_review: a curl
		// command produced started, then a guardianWarning, then completed carrying
		// riskLevel "low" and the reviewer's rationale.
		//
		// Surfaced because of what it is: consent nobody gave explicitly. A user who
		// can see "this was auto-approved as low risk, here is why" can disagree with
		// it; one who cannot see it has had a decision made silently.
		var p codexproto.ItemGuardianApprovalReviewStartedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventActivityStarted,
			ProviderTurnID: p.TurnID,
			ProviderItemID: reviewItemID(p.ReviewID),
			ActivityKind:   domain.ActivityKindAutoReview,
			ActivityStatus: domain.ActivityStatusRunning,
			Summary:        autoReviewSummary(p.Action, p.Review),
			Detail: encodeDetail(autoReviewDetail(
				p.ReviewID, p.TargetItemID, p.Action, p.Review, "", p.StartedAtMs, 0)),
		}}

	case codexproto.MethodItemAutoApprovalReviewCompleted:
		var p codexproto.ItemGuardianApprovalReviewCompletedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventActivityCompleted,
			ProviderTurnID: p.TurnID,
			ProviderItemID: reviewItemID(p.ReviewID),
			ActivityKind:   domain.ActivityKindAutoReview,
			ActivityStatus: autoReviewStatus(p.Review.Status),
			Summary:        autoReviewSummary(p.Action, p.Review),
			Detail: encodeDetail(autoReviewDetail(
				p.ReviewID, p.TargetItemID, p.Action, p.Review,
				string(p.DecisionSource), p.StartedAtMs, p.CompletedAtMs)),
		}}

	case codexproto.MethodModelRerouted:
		// The provider answered with a model other than the one asked for. NEVER
		// OBSERVED: the only reason the schema declares is highRiskCyberActivity, and
		// there is no honest way to provoke that from a test.
		//
		// Handled because of the failure it prevents rather than the frequency it
		// happens at: the composer names the model it is sending to, so an unrecorded
		// substitution means every later reading of that turn attributes the answer to
		// a model that did not produce it.
		var p codexproto.ModelReroutedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.ToModel == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventModelRerouted,
			ProviderTurnID: p.TurnID,
			Reroute: &ports.ChatModelReroute{
				FromModel: p.FromModel,
				ToModel:   p.ToModel,
				Reason:    string(p.Reason),
			},
		}}

	case codexproto.MethodAccountUpdated:
		// The account's auth mode or plan changed. NEVER OBSERVED in a probe: it
		// fires on login, logout, and plan changes, none of which a test may do to a
		// real account. The payload is two optional enums, so reading it is
		// unambiguous even unverified.
		var p codexproto.AccountUpdatedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		account := ports.ChatAccount{}
		if p.AuthMode != nil {
			account.AuthMode = string(*p.AuthMode)
		}
		if p.PlanType != nil {
			account.PlanLabel = string(*p.PlanType)
		}
		if account.AuthMode == "" && account.PlanLabel == "" {
			return nil
		}
		return []ports.ChatEvent{{Kind: ports.ChatEventAccountChanged, Account: &account}}

	case codexproto.MethodThreadStatusChanged:
		// VERIFIED live: {"status":{"type":"active","activeFlags":[]}} while a turn
		// runs and {"status":{"type":"idle"}} when it ends.
		//
		// Recorded as thread state and deliberately NOT fed into AO's activity
		// signal. Turn lifecycle already drives that, and a second driver of the same
		// reduction would be two sources of truth for one status.
		var p codexproto.ThreadStatusChangedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		state := ports.ChatThreadState{
			Status:    threadStatusFrom(p.Status.Type),
			WaitingOn: waitingOnFrom(p.Status.ActiveFlags),
		}
		if state.Status == "" {
			return nil
		}
		return []ports.ChatEvent{{Kind: ports.ChatEventThreadState, ThreadState: &state}}

	case codexproto.MethodThreadArchived, codexproto.MethodThreadUnarchived:
		// VERIFIED live: thread/archive and thread/unarchive each produced their
		// notification. Archiving is the provider's own idea of putting a thread away,
		// and it is reversible, so it is tri-state rather than a one-way marker.
		archived := n.Method == codexproto.MethodThreadArchived
		return []ports.ChatEvent{{
			Kind:        ports.ChatEventThreadState,
			ThreadState: &ports.ChatThreadState{Archived: &archived},
		}}

	case codexproto.MethodThreadClosed:
		// NEVER OBSERVED: thread/unsubscribe returned {"status":"unsubscribed"} and
		// emitted nothing. Recorded rather than acted on — tearing a controller down
		// on an unverified notification would turn a provider quirk into a lost
		// session.
		return []ports.ChatEvent{{
			Kind:        ports.ChatEventThreadState,
			ThreadState: &ports.ChatThreadState{Closed: true, Status: domain.ThreadStatusClosed},
		}}

	case codexproto.MethodMcpServerStartupStatusUpdated:
		// VERIFIED live: each configured server reports starting, then ready or
		// failed, and re-reports on every turn. Previously dropped as bookkeeping,
		// which left the one explanation for a class of failure unavailable — a tool
		// call that never happened because its server did not start looks, from the
		// timeline alone, like the agent choosing not to use it.
		var p codexproto.McpServerStatusUpdatedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil || p.Name == "" {
			return nil
		}
		server := ports.ChatMCPServer{Name: p.Name, Status: string(p.Status)}
		if p.Error != nil {
			server.Error = *p.Error
		}
		if p.FailureReason != nil {
			server.FailureReason = string(*p.FailureReason)
		}
		return []ports.ChatEvent{{
			Kind:       ports.ChatEventMCPServers,
			MCPServers: []ports.ChatMCPServer{server},
		}}

	case codexproto.MethodTurnDiffUpdated:
		// The turn's running diff. Re-sent whole on every update (observed six times
		// with byte-identical payloads in one turn), so a projector must overwrite
		// per-turn state and must not append a timeline entry per update.
		var p codexproto.TurnDiffUpdatedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		files, truncated := parseTurnDiff(p.Diff)
		if len(files) == 0 {
			// An empty diff is a real state: the turn has changed nothing on disk
			// yet. Reporting it keeps a stale file list from surviving an undo.
			return []ports.ChatEvent{{
				Kind:           ports.ChatEventTurnDiff,
				ProviderTurnID: p.TurnID,
				Diff:           &ports.ChatTurnDiff{},
			}}
		}
		ev := ports.ChatEvent{
			Kind:           ports.ChatEventTurnDiff,
			ProviderTurnID: p.TurnID,
			Diff:           &ports.ChatTurnDiff{Files: files},
		}
		if truncated {
			// Carried in Summary because ChatTurnDiff has nowhere to say it, and a
			// cut list presented as a whole one would understate the change.
			ev.Summary = domain.ChatDiffTruncatedSummary
		}
		return []ports.ChatEvent{ev}

	case codexproto.MethodItemStarted:
		return normalizeItem(n.Params, false)

	case codexproto.MethodItemCompleted:
		return normalizeItem(n.Params, true)

	case codexproto.MethodThreadTokenUsageUpdated:
		var p codexproto.ThreadTokenUsageUpdatedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		// A typed usage event rather than an activity. The provider emits one of
		// these after every tool call, so a timeline row per report is what buried
		// the conversation; this is current state, and the projection overwrites.
		usage := &ports.ChatUsage{
			InputTokens:  p.TokenUsage.Total.InputTokens,
			OutputTokens: p.TokenUsage.Total.OutputTokens,
			CachedTokens: p.TokenUsage.Total.CachedInputTokens,
			TotalTokens:  p.TokenUsage.Total.TotalTokens,
			ContextKnown: true,
			TotalsKnown:  true,
			// Context fullness comes from the LAST request, not the cumulative
			// total: a turn resends the whole conversation, so the last
			// request's size is what is actually occupying the window. Using
			// the running total here would report a conversation as over
			// capacity long before it was.
			ContextUsed: p.TokenUsage.Last.TotalTokens,
		}
		if p.TokenUsage.ModelContextWindow != nil {
			usage.ContextWindow = *p.TokenUsage.ModelContextWindow
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventUsage,
			ProviderTurnID: p.TurnID,
			Usage:          usage,
		}}

	case codexproto.MethodAccountRateLimitsUpdated:
		var p capacityReadEnvelope
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		limits := chatRateLimitsFromCapacity(capacityObservationFromEnvelope(p, now.UTC(), true), now)
		return []ports.ChatEvent{{Kind: ports.ChatEventRateLimits, RateLimits: &limits}}

	// The generated constant carries the provider's own deprecation note, and reading
	// it anyway is the point: 0.146.0 does not send it, but a build old enough to do
	// so would otherwise have its compactions silently dropped.
	case codexproto.MethodThreadCompacted: //nolint:staticcheck // deliberately reading the deprecated spelling
		// The deprecated spelling, kept for a provider build old enough to send it.
		// A build that sends both this and the contextCompaction item would report
		// one compaction twice, so the conversation dedupes on the turn id.
		var p codexproto.ContextCompactedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventCompacted,
			ProviderTurnID: p.TurnID,
		}}

	case codexproto.MethodThreadNameUpdated:
		var p codexproto.ThreadNameUpdatedNotification
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		// A cleared name is reported too, as an empty title. It is a real change to
		// the conversation and the projection needs to see it; what the projection
		// must not do is blank out a label on the strength of it.
		title := ""
		if p.ThreadName != nil {
			title = strings.TrimSpace(*p.ThreadName)
		}
		return []ports.ChatEvent{{
			Kind:  ports.ChatEventThreadRenamed,
			Title: title,
		}}

	case codexproto.MethodServerRequestResolved:
		var p resolvedEnvelope
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return nil
		}
		id := rawID(p.RequestID)
		if id == "" {
			id = rawID(p.ID)
		}
		if id == "" {
			return nil
		}
		return []ports.ChatEvent{{
			Kind:      ports.ChatEventApprovalResolved,
			RequestID: id,
		}}

	case codexproto.MethodError:
		return []ports.ChatEvent{{
			Kind: ports.ChatEventError,
			Err:  fmt.Errorf("provider error: %s", truncateForLog(n.Params)),
		}}

	default:
		// Provider bookkeeping: hook/started, hook/completed,
		// remoteControl/status/changed, deprecationNotice, thread/goal/*,
		// model/safetyBuffering/updated, thread/settings/updated, and anything added
		// by a newer provider build. Deliberately not conversation events.
		//
		// Three neighbours of the handled methods are also deliberately here:
		//
		//   - guardianWarning restates an auto-approval decision in prose. The
		//     autoApprovalReview pair above carries the same rationale as structure,
		//     so reading both would put one decision on the timeline twice.
		//   - command/exec/outputDelta and process/outputDelta belong to the
		//     client-driven exec API, where the CLIENT asked the server to run
		//     something. AO does not use it, and an agent tool call never arrives on
		//     those methods.
		//   - thread/realtime/* is the voice surface. AO has none, and inventing one
		//     from a transcript delta would be a feature nobody asked for.
		return nil
	}
}

// turnIDFallback reads a top-level turnId, which some builds send instead of
// nesting the id under `turn`. Read from the raw params because codexproto's
// generated notification types declare only the nested form.
func turnIDFallback(params json.RawMessage) string {
	var p struct {
		TurnID string `json:"turnId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}
	return p.TurnID
}

// planFrom converts a provider plan into AO's structured form.
//
// An unrecognized step status becomes pending rather than being carried through:
// the statuses drive whether a step reads as done, and inventing "done" for a word
// AO does not know would tick off work that may not have happened.
func planFrom(p codexproto.TurnPlanUpdatedNotification) domain.ConversationPlan {
	plan := domain.ConversationPlan{Steps: make([]domain.ConversationPlanStep, 0, len(p.Plan))}
	if p.Explanation != nil {
		plan.Explanation = strings.TrimSpace(*p.Explanation)
	}
	for _, step := range p.Plan {
		text := strings.TrimSpace(step.Step)
		if text == "" {
			continue
		}
		plan.Steps = append(plan.Steps, domain.ConversationPlanStep{
			Text:   text,
			Status: planStepStatus(step.Status),
		})
	}
	return plan
}

func planStepStatus(status codexproto.TurnPlanStepStatus) domain.PlanStepStatus {
	switch status {
	case codexproto.TurnPlanStepStatusInProgress:
		return domain.PlanStepInProgress
	case codexproto.TurnPlanStepStatusCompleted:
		return domain.PlanStepCompleted
	default:
		return domain.PlanStepPending
	}
}

// planSummary labels a plan for a collapsed timeline row. It states progress
// rather than restating the first step, because progress is the thing that changes
// between the three or four updates one turn produces.
func planSummary(plan domain.ConversationPlan) string {
	if len(plan.Steps) == 0 {
		return "Cleared the plan"
	}
	done := 0
	current := ""
	for _, step := range plan.Steps {
		if step.Status == domain.PlanStepCompleted {
			done++
		}
		if current == "" && step.Status == domain.PlanStepInProgress {
			current = step.Text
		}
	}
	if current != "" {
		return fmt.Sprintf("Plan %d/%d: %s", done, len(plan.Steps), truncateLabel(current))
	}
	return fmt.Sprintf("Plan %d/%d steps done", done, len(plan.Steps))
}

// reviewItemID keys an auto-review activity.
//
// The provider gives a reviewId but no item id, and the review is not an item. The
// prefix is what keeps a synthetic key from ever colliding with a real provider
// item id (which are `rs_`, `msg_`, `exec-`, or a uuid), so the started and
// completed notifications update one row instead of creating two.
func reviewItemID(reviewID string) string {
	if reviewID == "" {
		return ""
	}
	return "ao-review-" + reviewID
}

// autoReviewStatus maps a review outcome onto an activity status. Anything that is
// not an approval is a failure: timedOut and aborted mean the action did not get
// consent, and reporting them as completed would read as approval.
func autoReviewStatus(status codexproto.GuardianApprovalReviewStatus) domain.ActivityStatus {
	switch status {
	case codexproto.GuardianApprovalReviewStatusApproved:
		return domain.ActivityStatusCompleted
	case codexproto.GuardianApprovalReviewStatusInProgress:
		return domain.ActivityStatusRunning
	default:
		return domain.ActivityStatusFailed
	}
}

// autoReviewSummary labels an auto-review with the decision and what it was about.
func autoReviewSummary(
	action codexproto.GuardianApprovalReviewAction,
	review codexproto.GuardianApprovalReview,
) string {
	subject := autoReviewSubject(action)
	switch review.Status {
	case codexproto.GuardianApprovalReviewStatusApproved:
		risk := ""
		if review.RiskLevel != nil {
			risk = fmt.Sprintf(" (%s risk)", *review.RiskLevel)
		}
		return "Auto-approved " + subject + risk
	case codexproto.GuardianApprovalReviewStatusDenied:
		return "Auto-declined " + subject
	case codexproto.GuardianApprovalReviewStatusInProgress:
		return "Reviewing " + subject
	default:
		return fmt.Sprintf("Auto-review %s: %s", review.Status, subject)
	}
}

// autoReviewSubject names the action under review, preferring the concrete thing
// over the category.
func autoReviewSubject(action codexproto.GuardianApprovalReviewAction) string {
	switch {
	case action.Command != nil && *action.Command != "":
		return commandSummary(*action.Command)
	case action.ToolName != nil && *action.ToolName != "":
		return "tool " + *action.ToolName
	case action.Host != nil && *action.Host != "":
		return "network access to " + *action.Host
	case len(action.Files) > 0:
		return fmt.Sprintf("changes to %d file(s)", len(action.Files))
	case action.Program != nil && *action.Program != "":
		return *action.Program
	default:
		return string(action.Type)
	}
}

// autoReviewDetail is the neutral payload for an auto-review row. Only named
// fields: the provider's action union carries a dozen optional members and passing
// it through verbatim would put a provider DTO on the wire.
func autoReviewDetail(
	reviewID string,
	targetItemID *string,
	action codexproto.GuardianApprovalReviewAction,
	review codexproto.GuardianApprovalReview,
	decisionSource string,
	startedAtMs, completedAtMs int64,
) map[string]any {
	detail := map[string]any{
		"reviewId":   reviewID,
		"actionType": string(action.Type),
		"status":     string(review.Status),
	}
	if targetItemID != nil && *targetItemID != "" {
		// The item the review was about, so a client can attach the decision to the
		// command it gated instead of showing it floating on its own.
		detail["targetItemId"] = *targetItemID
	}
	if action.Command != nil && *action.Command != "" {
		detail["command"] = commanddetail.UnwrapShell(*action.Command)
		detail["rawCommand"] = *action.Command
	}
	if action.Cwd != nil && *action.Cwd != "" {
		detail["cwd"] = string(*action.Cwd)
	}
	if action.Source != nil {
		detail["commandSource"] = string(*action.Source)
	}
	if action.ToolName != nil && *action.ToolName != "" {
		detail["toolName"] = *action.ToolName
	}
	if action.Server != nil && *action.Server != "" {
		detail["server"] = *action.Server
	}
	if action.Host != nil && *action.Host != "" {
		detail["host"] = *action.Host
	}
	if len(action.Files) > 0 {
		paths := make([]string, 0, len(action.Files))
		for _, file := range action.Files {
			paths = append(paths, string(file))
		}
		detail["files"] = paths
	}
	if review.RiskLevel != nil {
		detail["riskLevel"] = string(*review.RiskLevel)
	}
	if review.UserAuthorization != nil {
		detail["userAuthorization"] = string(*review.UserAuthorization)
	}
	if review.Rationale != nil && *review.Rationale != "" {
		detail["rationale"] = *review.Rationale
	}
	if decisionSource != "" {
		// Who decided. "agent" means a model made the call, which is a materially
		// different thing to show a user than a policy rule matching.
		detail["decisionSource"] = decisionSource
	}
	if startedAtMs > 0 && completedAtMs > startedAtMs {
		detail["durationMs"] = completedAtMs - startedAtMs
	}
	return detail
}

// threadStatusFrom maps the provider's thread status onto AO's spelling. An
// unrecognized status yields the empty string, which the caller reads as "nothing
// to record" rather than guessing.
func threadStatusFrom(status codexproto.ThreadStatusType) domain.ThreadStatus {
	switch status {
	case codexproto.ThreadStatusTypeActive:
		return domain.ThreadStatusActive
	case codexproto.ThreadStatusTypeIdle:
		return domain.ThreadStatusIdle
	case codexproto.ThreadStatusTypeNotLoaded:
		return domain.ThreadStatusNotLoaded
	case codexproto.ThreadStatusTypeSystemError:
		return domain.ThreadStatusSystemError
	default:
		return ""
	}
}

// waitingOnFrom maps the provider's active flags onto AO's spelling.
func waitingOnFrom(flags []codexproto.ThreadActiveFlag) []string {
	if len(flags) == 0 {
		return nil
	}
	out := make([]string, 0, len(flags))
	for _, flag := range flags {
		switch flag {
		case codexproto.ThreadActiveFlagWaitingOnApproval:
			out = append(out, "waiting_on_approval")
		case codexproto.ThreadActiveFlagWaitingOnUserInput:
			out = append(out, "waiting_on_user_input")
		default:
			out = append(out, string(flag))
		}
	}
	return out
}

// normalizeItem maps an item lifecycle notification. Assistant text is a message;
// everything else is an activity with a typed payload.
func normalizeItem(params json.RawMessage, completed bool) []ports.ChatEvent {
	var p struct {
		ThreadID string     `json:"threadId"`
		TurnID   string     `json:"turnId"`
		Item     threadItem `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	it := p.Item

	// The user's own message comes back from the provider as an item. AO already
	// persisted it when the send was accepted, so re-emitting it would duplicate
	// the timeline entry.
	if it.Type == itemUserMessage {
		return nil
	}

	if it.Type == itemContextCompaction {
		if !completed {
			// A compaction in flight is not yet a fact about the conversation, and the
			// reclaim is unknown until it settles: the reduced token figure arrives
			// between the item starting and completing. Emitting on start would put a
			// row in the timeline that has to be rewritten with the real numbers.
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventCompacted,
			ProviderTurnID: p.TurnID,
			ProviderItemID: it.itemID(),
		}}
	}

	if it.Type == itemAgentMessage {
		if !completed {
			// The message row is created by the first delta; a started event
			// with no text adds nothing.
			return nil
		}
		return []ports.ChatEvent{{
			Kind:           ports.ChatEventMessageCompleted,
			ProviderTurnID: p.TurnID,
			ProviderItemID: it.itemID(),
			Text:           deref(it.Text),
		}}
	}

	kind, summary, ok := activityFor(it)
	if !ok {
		return nil
	}

	ev := ports.ChatEvent{
		ProviderTurnID: p.TurnID,
		ProviderItemID: it.itemID(),
		ActivityKind:   kind,
		Summary:        summary,
		Detail:         activityDetail(it),
	}
	if kind == domain.ActivityKindReasoning {
		// The settled form of what the summary deltas streamed. Carried on the event so
		// the projection can replace its accumulation without decoding the detail
		// payload, and left empty when the provider settled nothing — a reasoning item
		// with an empty summary array is the DEFAULT on a build with
		// model_reasoning_summary unset, and replacing streamed text with that would
		// erase reasoning the user watched arrive.
		ev.Text = joinReasoning(it.Summary)
	}
	if completed {
		ev.Kind = ports.ChatEventActivityCompleted
		ev.ActivityStatus = domain.ActivityStatusCompleted
		if it.ExitCode != nil && *it.ExitCode != 0 {
			ev.ActivityStatus = domain.ActivityStatusFailed
		}
		if it.Type == itemError {
			ev.ActivityStatus = domain.ActivityStatusFailed
		}
		// An MCP tool reports failure in its own payload, not through an exit code.
		// Verified live: a successful call carries error: null and status
		// "completed"; the schema's McpToolCallError is what a failure sets.
		if it.Error != nil && it.Error.Message != "" {
			ev.ActivityStatus = domain.ActivityStatusFailed
		}
		if it.Success != nil && !*it.Success {
			ev.ActivityStatus = domain.ActivityStatusFailed
		}
	} else {
		ev.Kind = ports.ChatEventActivityStarted
		ev.ActivityStatus = domain.ActivityStatusRunning
	}
	return []ports.ChatEvent{ev}
}

// activityFor maps a provider item type onto an activity kind and label.
func activityFor(it threadItem) (domain.ActivityKind, string, bool) {
	switch it.Type {
	case itemCommandExecution:
		return domain.ActivityKindCommand, commandSummary(deref(it.Command)), true
	case itemFileChange:
		return domain.ActivityKindFileChange, fileChangeSummary(fileChanges(it.Changes)), true
	case itemPlan:
		return domain.ActivityKindPlan, "Updated plan", true
	case itemReasoning:
		return domain.ActivityKindReasoning, "Reasoning", true
	case itemMcpToolCall:
		return domain.ActivityKindMCPTool, mcpToolSummary(it), true
	case itemWebSearch:
		if query := strings.TrimSpace(deref(it.Query)); query != "" {
			return domain.ActivityKindCommand, "Searched the web for " + truncateLabel(query), true
		}
		return domain.ActivityKindCommand, "Searched the web", true
	case itemError:
		return domain.ActivityKindError, firstNonEmpty(it.Message, "Provider error"), true
	default:
		return "", "", false
	}
}

// mcpToolSummary labels an MCP tool call with the server and tool it names.
//
// `tool` and `server` are the fields the provider actually sends — verified
// against a live call: {"type":"mcpToolCall","server":"probe","tool":"slow_echo",
// "arguments":{...}}. The previous build read `toolName`, which no build sends, so
// every MCP call in the timeline read "Called tool".
func mcpToolSummary(it threadItem) string {
	tool := strings.TrimSpace(deref(it.Tool))
	server := strings.TrimSpace(deref(it.Server))
	switch {
	case tool != "" && server != "":
		return "Called " + server + "/" + tool
	case tool != "":
		return "Called " + tool
	case server != "":
		return "Called a tool on " + server
	default:
		return "Called a tool"
	}
}

// fileChangeSummary labels a file-change activity with what it touched, so a
// collapsed row says something. "Edited files" was the same label whether the
// agent renamed one path or rewrote thirty.
func fileChangeSummary(files []fileChangeDetail) string {
	switch len(files) {
	case 0:
		return "Edited files"
	case 1:
		return fileChangeVerb(files[0].Status) + " " + truncateLabel(files[0].Path)
	default:
		return fmt.Sprintf("Edited %d files", len(files))
	}
}

func fileChangeVerb(status string) string {
	switch status {
	case diffStatusAdded:
		return "Created"
	case diffStatusDeleted:
		return "Deleted"
	case diffStatusRenamed:
		return "Renamed"
	default:
		return "Edited"
	}
}

// commandSummary produces a one-line label. Codex wraps commands in a shell
// invocation (`/bin/zsh -lc '…'`); the wrapper is noise in a timeline.
func commandSummary(command string) string {
	cmd := commanddetail.UnwrapShell(strings.TrimSpace(command))
	if cmd == "" {
		return "Ran a command"
	}
	return truncateLabel(cmd)
}

// truncateLabel caps a one-line label. 120 characters is about where a timeline
// row stops being scannable.
func truncateLabel(value string) string {
	const limit = 120
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// activityDetail is the provider-neutral payload AO persists for an activity.
// Provider DTOs are not passed through: only named fields AO renders.
func activityDetail(it threadItem) []byte {
	detail := map[string]any{}
	if cmd := deref(it.Command); cmd != "" {
		detail["command"] = commanddetail.UnwrapShell(cmd)
		detail["rawCommand"] = cmd
	}
	if it.Cwd != nil && *it.Cwd != "" {
		detail["cwd"] = string(*it.Cwd)
	}
	if it.ProcessID != nil && *it.ProcessID != "" {
		// The PTY behind an interactive command. Needed to correlate the keystrokes
		// item/commandExecution/terminalInteraction reports with the command they
		// were typed into.
		detail["processId"] = *it.ProcessID
	}
	if out := deref(it.AggregatedOutput); out != "" {
		// Marked partial because the provider's own aggregation was observed to
		// drop leading output. A reader must not present it as the full record.
		detail["output"] = out
		detail["outputMayBePartial"] = true
		// Named so a reader can tell this from the accumulated delta stream. Only
		// the aggregate exists for a fast command, where the provider finishes
		// before it flushes a single delta; only the stream exists for a command
		// that never completes. Both are partial, for different reasons.
		detail["outputSource"] = "aggregate"
	}
	if it.ExitCode != nil {
		detail["exitCode"] = *it.ExitCode
	}
	if it.DurationMs != nil {
		detail["durationMs"] = *it.DurationMs
	}
	if text := strings.TrimSpace(deref(it.Text)); text != "" {
		detail["text"] = text
	}
	// Reasoning arrives as a list of summary sections, and the previous build read
	// none of them: it looked for a `text` field that a reasoning item does not
	// have, so every settled reasoning row was blank. Verified live: item/completed
	// carries summary: ["**Searching deferred tool in ALL_TOOLS**"].
	if summary := joinReasoning(it.Summary); summary != "" {
		detail["text"] = summary
	}
	if len(it.Changes) > 0 {
		// AO's own per-file shape, not the provider's `changes` array. The provider
		// spells the change kind as an object ({"type":"update","move_path":null}),
		// so passing it through left a client with nothing it could read as a status,
		// and it was a provider DTO on AO's wire besides.
		detail["files"] = fileChanges(it.Changes)
	}
	if tool := deref(it.Tool); tool != "" {
		detail["toolName"] = tool
	}
	if server := deref(it.Server); server != "" {
		detail["server"] = server
	}
	if ns := deref(it.Namespace); ns != "" {
		detail["namespace"] = ns
	}
	if len(it.Arguments) > 0 && !isJSONNull(it.Arguments) {
		// What the tool was called WITH. A tool call with no arguments shown is a
		// claim the user cannot check: "called search" says nothing about what was
		// searched for. Capped, because a tool can be handed a whole file.
		detail["arguments"] = truncatedJSON(it.Arguments)
	}
	if len(it.Result) > 0 && !isJSONNull(it.Result) {
		detail["result"] = truncatedJSON(it.Result)
	}
	if it.Error != nil && it.Error.Message != "" {
		detail["error"] = it.Error.Message
	}
	if it.Success != nil {
		detail["success"] = *it.Success
	}
	if query := strings.TrimSpace(deref(it.Query)); query != "" {
		detail["query"] = query
	}
	if it.Message != "" {
		detail["message"] = it.Message
	}
	if len(detail) == 0 {
		return nil
	}
	return encodeDetail(detail)
}

// maxToolPayloadChars caps one tool call's arguments or result.
//
// A tool can be handed a whole file and can hand one back, and this payload is
// re-read by every conversation snapshot poll. 8K is well past what a card shows
// and far short of what an unbounded result would cost; over the cap the JSON is
// replaced by a marker rather than cut, because half a JSON document is not JSON
// and a client that tried to parse it would fail on valid provider data.
const maxToolPayloadChars = 8 * 1024

// truncatedJSON keeps a payload only while it is small enough to be worth keeping.
func truncatedJSON(raw json.RawMessage) any {
	if len(raw) <= maxToolPayloadChars {
		return raw
	}
	return map[string]any{
		"truncated": true,
		"bytes":     len(raw),
		"note":      "payload exceeded the daemon's tool payload cap and was not stored",
	}
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// joinReasoning flattens a reasoning item's summary sections into one block.
//
// The generated ThreadItem declares summary as []string and a live item/completed
// carried exactly that. Sections are joined with a blank line, matching the
// separator the streaming path inserts on summaryPartAdded, so the settled text
// and the streamed text are the same string rather than two spellings of it.
func joinReasoning(summary []string) string {
	parts := make([]string, 0, len(summary))
	for _, part := range summary {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n")
}

// fileChangeDetail is one changed file as AO puts it on the wire.
//
// Declared here with its own JSON tags rather than reusing ports.ChatDiffFile,
// which has none: marshalling that type directly would put Go field names
// ("Path", "Additions") into the payload a client reads. The port type describes an
// event in memory; this describes the wire.
type fileChangeDetail struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Patch is this file's unified diff, so a client can render the change as soon as
	// the patch lands instead of waiting for the turn's aggregate.
	Patch string `json:"patch,omitempty"`
	// PatchTruncated reports that the patch was cut at AO's cap, so a partial diff is
	// never presented as a whole one.
	PatchTruncated bool `json:"patchTruncated,omitempty"`
}

// maxPatchChars caps one file's stored patch.
//
// A new file's "patch" is its entire contents, and this payload is re-read by every
// conversation snapshot poll, so an agent generating a 5000-line file would otherwise
// put it in a row that is fetched once a second. 16K is well past what a diff card
// shows; past it the worktree and the turn's aggregate diff are where the full change
// lives.
const maxPatchChars = 16 * 1024

// fileChanges converts the provider's per-file patches into AO's diff shape.
//
// Counts are read out of the patch text here because the provider does not send
// them. Two patch shapes arrive, both verified live: an update carries a unified
// hunk (`@@ -2 +2,2 @@\n two\n+three\n`), and an add carries the new file's
// contents with no hunk header at all.
func fileChanges(changes []codexproto.FileUpdateChange) []fileChangeDetail {
	if len(changes) == 0 {
		return nil
	}
	files := make([]fileChangeDetail, 0, len(changes))
	for _, change := range changes {
		file := fileChangeDetail{
			Path:   change.Path,
			Status: patchStatus(change.Kind.Type),
			Patch:  change.Diff,
		}
		if change.Kind.MovePath != nil && *change.Kind.MovePath != "" {
			// A move is reported as an update carrying the destination, so the path
			// AO shows is where the file ended up and OldPath is where it was.
			file.Status = diffStatusRenamed
			file.OldPath = change.Path
			file.Path = *change.Kind.MovePath
		}
		// Counted from the WHOLE patch, before any truncation: the figures describe
		// the change, not how much of it AO chose to store.
		file.Additions, file.Deletions = countPatchLines(change.Diff, file.Status)
		if len(file.Patch) > maxPatchChars {
			file.Patch = file.Patch[:maxPatchChars]
			file.PatchTruncated = true
		}
		files = append(files, file)
	}
	return files
}

// patchStatus maps the provider's change kind onto AO's neutral status.
func patchStatus(kind codexproto.PatchChangeKindType) string {
	switch kind {
	case codexproto.PatchChangeKindTypeAdd:
		return diffStatusAdded
	case codexproto.PatchChangeKindTypeDelete:
		return diffStatusDeleted
	default:
		return diffStatusModified
	}
}

// countPatchLines counts added and removed lines in one file's patch.
//
// A patch with no hunk header is the whole file: an add is all additions and a
// delete is all deletions. Counting `+`/`-` prefixes in that case would count the
// file's own content as diff markers, which is how a new file full of bullet points
// would report deletions it never made.
func countPatchLines(patch, status string) (additions, deletions int) {
	if patch == "" {
		return 0, 0
	}
	if !strings.Contains(patch, "@@") {
		lines := strings.Count(strings.TrimSuffix(patch, "\n"), "\n") + 1
		switch status {
		case diffStatusDeleted:
			return 0, lines
		case diffStatusAdded:
			return lines, 0
		default:
			return 0, 0
		}
	}
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk:
			continue
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

// turnStateFrom maps a provider turn status onto AO's turn state. An unknown
// status is failed rather than completed: claiming success AO did not observe is
// worse than reporting an honest unknown failure.
func turnStateFrom(status string) domain.TurnState {
	switch status {
	case string(codexproto.TurnStatusCompleted):
		return domain.TurnStateCompleted
	case string(codexproto.TurnStatusInterrupted), "cancelled", "canceled":
		return domain.TurnStateInterrupted
	case string(codexproto.TurnStatusInProgress), "in_progress", "running", "active":
		return domain.TurnStateRunning
	case "queued", "pending":
		return domain.TurnStateQueued
	default:
		return domain.TurnStateFailed
	}
}

// rawID stringifies a JSON-RPC id, which the protocol allows to be either a
// number or a string. `null`, absent, and empty all yield "".
func rawID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	return strings.Trim(s, `"`)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// deref reads an optional string the generated types declare as a pointer.
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// encodeDetail marshals a neutral payload, returning nil rather than a partial
// document if it cannot be encoded.
func encodeDetail(detail map[string]any) []byte {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil
	}
	return encoded
}

func truncateForLog(raw []byte) string {
	const maxLogBytes = 400
	s := string(raw)
	if len(s) > maxLogBytes {
		return s[:maxLogBytes] + "…"
	}
	return s
}
