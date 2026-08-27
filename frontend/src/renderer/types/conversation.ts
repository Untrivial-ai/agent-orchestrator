/**
 * Chat conversation view model.
 *
 * These mirror `backend/internal/domain/conversation.go` field for field. The
 * endpoint transport is generated from OpenAPI, while this file keeps the
 * renderer's discriminated semantic view of timeline details. Keep the two in
 * sync when the daemon adds a conversation shape.
 *
 * The renderer is thin on purpose: ordering, capability, permission, and
 * lifecycle decisions all belong to the daemon. Nothing here recomputes them.
 */

/** Which controller currently owns the AO session. */
export type SessionMode = "chat" | "tui";

/** One request and the agent work that followed it. */
export type TurnState = "queued" | "running" | "completed" | "recovered" | "interrupted" | "failed";

export type MessageRole = "user" | "assistant";

/**
 * Who produced a message. Durable and structural — a worker or automation is
 * never identified by sniffing a text prefix.
 */
export type MessageOrigin = "human" | "automation" | "daemon" | "provider";

/**
 * `mcp_tool` and `auto_review` are deliberately not folded into their nearest
 * neighbour. An MCP call is not a `command`: it has a server, a tool name and
 * structured arguments, and drawing it as a shell command claimed the agent had run
 * something in the worktree. An auto-review is not an `approval`: an approval is a
 * question waiting on a person, an auto-review is a decision already taken for them,
 * and those are opposites.
 */
export type ActivityKind =
	| "command"
	| "file_change"
	| "plan"
	| "reasoning"
	| "approval"
	| "usage"
	| "error"
	| "system"
	| "mcp_tool"
	| "auto_review"
	| "user_input";

/**
 * `cancelled` means the enclosing turn stopped before the provider completed the
 * item. `recovered` means replay proved the item is historical but carried no
 * portable outcome. Both are intentionally distinct from `failed`.
 */
export type ActivityStatus =
	| "running"
	| "completed"
	| "recovered"
	| "failed"
	| "cancelled"
	| "pending"
	| "resolved";

/**
 * Delivery state for a message AO sent. `uncertain` is deliberately not merged
 * into `failed`: the provider may have accepted the turn while AO lost the
 * connection, and silently retrying would run the work twice.
 */
export type DeliveryState = "queued" | "sending" | "accepted" | "uncertain" | "failed";

/** How far the agent has got with one step of its plan. */
export type PlanStepStatus = "pending" | "in_progress" | "completed";

export interface PlanStep {
	text: string;
	status: PlanStepStatus;
}

/**
 * The agent's plan for a turn.
 *
 * Current state, not history: the provider re-sends the whole plan every time it
 * changes, and the daemon overwrites the turn's copy. That is why this is on the turn
 * rather than being read out of the timeline — a row per revision would read as
 * though the agent had planned three separate times in one turn.
 */
export interface ConversationPlan {
	explanation?: string;
	steps: PlanStep[];
}

export interface ConversationTurn {
	id: string;
	state: TurnState;
	/**
	 * An undo discarded this turn. Its messages and activities are absent from the
	 * snapshot, because the agent no longer remembers them; the turn is still
	 * reported so the timeline can say what was taken back rather than just
	 * appearing to have lost some history.
	 */
	rolledBack?: boolean;
	providerTurnId?: string;
	/** Failed source whose durable prompt created this retry attempt. */
	retryOfTurnId?: string;
	/** A retry attempt exists, even if that child is outside the active branch. */
	hasRetryAttempt?: boolean;
	errorMessage?: string;
	requestedAt: string;
	startedAt?: string;
	completedAt?: string;
	/**
	 * What this turn changed on disk. Absent is not "changed nothing": an agent
	 * that cannot report diffs never reports at all, and the UI must not draw an
	 * empty changed-files panel as if the turn had been inspected.
	 */
	diff?: TurnDiff;
	/** The agent's plan for this turn, or absent when it made none. */
	plan?: ConversationPlan;
}

/** How a file changed. The daemon's neutral names, not a provider's. */
export type DiffStatus = "added" | "modified" | "deleted" | "renamed";

export interface DiffFile {
	path: string;
	additions: number;
	deletions: number;
	status: DiffStatus;
	/** Set only for a rename. */
	oldPath?: string;
}

export interface TurnDiff {
	files: DiffFile[];
	/** The daemon cut the list at its cap; the change is larger than what is shown. */
	truncated?: boolean;
}

/** Lightweight, durable prompt content retained when a human message is edited. */
export interface ConversationContentSummary {
	type: string;
	mimeType?: string;
	uri?: string;
	name?: string;
}

export interface ConversationMessage {
	kind: "message";
	id: string;
	turnId?: string;
	/** Conversation-scoped, immutable. The only ordering key. */
	sequence: number;
	/** Bumped on each streaming rewrite so a gap is detectable. */
	revision: number;
	role: MessageRole;
	origin: MessageOrigin;
	text: string;
	/** Server-authoritative prompt blocks, with heavy image/resource payloads removed. */
	content?: ConversationContentSummary[];
	/** False when the original prompt content cannot be reproduced safely. */
	editAvailable?: boolean;
	/** True while more deltas are expected. */
	streaming: boolean;
	delivery?: DeliveryState;
	/** Set when origin is a worker or automation, for the attribution line. */
	senderLabel?: string;
	createdAt: string;
}

export interface ConversationBranchPoint {
	turnId: string;
	position: number;
	total: number;
	previousBranchId?: string;
	nextBranchId?: string;
}

/** One choice the provider says is valid for a pending request. */
export interface DecisionOption {
	/** e.g. "accept", "acceptForSession", "acceptWithExecpolicyAmendment". */
	id: string;
	label: string;
	/** Provider-neutral consent meaning; IDs and labels may be opaque or localized. */
	kind?: "allow_once" | "allow_always" | "reject_once" | "reject_always";
}

/** Provider context retained before and after a human approval decision. */
export interface ApprovalDetail {
	/** The provider request shape, used to choose command or file-change copy. */
	method?: string;
	/** The provider decision id AO successfully returned. */
	decision?: string;
	/** Present when another connected client resolved the request. */
	resolvedBy?: string;
	/** The normalized activity the provider wants permission to perform. */
	subjectKind?: ActivityKind;
	/** ACP's tool category, retained for diagnostics and forward compatibility. */
	toolKind?: string;
}

export interface CommandDetail {
	/**
	 * Free text payload: a plan body, a reasoning summary, a message.
	 *
	 * For reasoning this is one field whether the item is still streaming or already
	 * settled — the provider's finished summary lands under the same key the deltas
	 * accumulate into, so a reader never has to know which state it is in.
	 */
	text?: string;
	/** The accumulated text stopped at the daemon's cap; the model wrote more. */
	textTruncated?: boolean;
	command?: string;
	rawCommand?: string;
	cwd?: string;
	output?: string;
	/**
	 * Provider output aggregation was observed to drop leading bytes even on tiny
	 * commands, so this display data is not an authoritative record of what ran.
	 *
	 * Set for BOTH output sources. Measured on codex-cli 0.146.0: a command
	 * printing tick-1..tick-8 lost tick-1 from the delta stream and from the
	 * aggregate alike, so accumulating deltas buys liveness, not completeness.
	 */
	outputMayBePartial?: boolean;
	/**
	 * Which channel the text came from.
	 *
	 * `stream` is accumulated output deltas: it exists while the command is still
	 * running, and it is the only source for a command that never completes.
	 * `aggregate` is the provider's own roll-up, which only appears on completion
	 * and is all a fast command produces.
	 */
	outputSource?: "stream" | "aggregate";
	/** Accumulation stopped at the daemon's cap; the command printed more. */
	outputTruncated?: boolean;
	exitCode?: number;
	durationMs?: number;
	reason?: string;
	/**
	 * What the agent typed into a running command's terminal — usually `^C` to abort
	 * one that will not finish on its own.
	 *
	 * Kept out of `output` on purpose: the PTY echoes keystrokes, so merging the two
	 * would print the abort twice and leave no way to tell what the command said from
	 * what was done to it.
	 */
	terminalInput?: string;
	terminalInputTruncated?: boolean;
	/** The provider's own process handle, when it reported one. */
	processId?: number;
	/** ACP transcript hierarchy and richer provider terminal metadata. */
	parentProviderItemId?: string;
	nestedAgent?: boolean;
	providerToolName?: string;
	providerTitle?: string;
	subagentType?: string;
	subagentRetry?: unknown;
	terminalId?: string;
	signal?: string;
}

/** ACP form/URL elicitation projected into a durable AO activity. */
export interface UserInputDetail {
	inputMode?: "form" | "url";
	message?: string;
	schema?: {
		title?: string;
		description?: string;
		type?: "object";
		required?: string[];
		properties?: Record<string, Record<string, unknown>>;
	};
	url?: string;
	elicitationId?: string;
}

/**
 * One changed file, as the daemon now normalizes it.
 *
 * Before, this carried a path and two counts and nothing else: the provider spells a
 * change's kind as an object, so no client could read a status at all. `status` is a
 * plain string now, and `patch` is this file's own unified diff — which is what lets
 * a change be read where it happened instead of only counted.
 *
 * `status` stays optional because rows written by earlier builds are still on disk
 * and have none; a missing status is drawn as a modification rather than dropping the
 * file.
 */
export interface FileChangeFile {
	path: string;
	/** Set only for a rename: where the file was before. */
	oldPath?: string;
	status?: DiffStatus;
	additions: number;
	deletions: number;
	patch?: string;
	/** The patch was cut at the daemon's cap, so it is not the whole change. */
	patchTruncated?: boolean;
}

export interface FileChangeDetail {
	/**
	 * Objects for a `file_change`; bare paths for an `auto_review`, whose action names
	 * the files it was allowed to touch. The key is shared on the wire, so the kind is
	 * what disambiguates — read it through `fileChangeFiles` or `reviewedPaths` rather
	 * than by hand.
	 */
	files?: FileChangeFile[] | string[];
	/** Accumulated patch text for a change still being written. */
	patchOutput?: string;
	patchOutputTruncated?: boolean;
}

/**
 * A call to a tool served by an MCP server.
 *
 * `arguments` and `result` are whatever JSON the tool takes and returns, so they are
 * `unknown` and rendered as JSON. When a payload exceeded the daemon's cap it is
 * replaced by `{ truncated, bytes, note }` — a stand-in the UI has to recognize
 * rather than print as the tool's actual answer.
 */
export interface McpToolDetail {
	server?: string;
	toolName?: string;
	/** The provider's grouping when the tool is namespaced rather than server-scoped. */
	namespace?: string;
	arguments?: unknown;
	result?: unknown;
	error?: string;
	success?: boolean;
	/** Progress notes streamed while a long call runs. */
	progress?: string;
	progressTruncated?: boolean;
}

/** How much of a risk the provider judged an action to be. */
export type ReviewRiskLevel = "low" | "medium" | "high" | "critical";

/**
 * A decision the provider made on the user's behalf.
 *
 * Not an approval: nobody was asked. The row exists so an action that ran unattended
 * is still on the record, with the reasoning that allowed it.
 */
export interface AutoReviewDetail {
	reviewId?: string;
	/** The item the review gated, so the decision can be tied to the command it let run. */
	targetItemId?: string;
	actionType?: string;
	riskLevel?: string;
	rationale?: string;
	/** Who decided: a policy rule matching, or a model making the call. */
	decisionSource?: string;
	/** The provider's own verdict — `approved`, `denied`, and so on. */
	status?: string;
	userAuthorization?: string;
	/** Set when the action was network access. */
	host?: string;
	commandSource?: string;
}

/**
 * A `system` activity's discriminator and the fields that belong to it.
 *
 * The kind is a general bucket, so the daemon stamps the event rather than leaving
 * the renderer to infer a compaction from the presence of token counts or a steer
 * from the presence of text. Read the discriminator, never the kind.
 */
export interface SystemEventDetail {
	event?:
		| "compaction"
		| "model.rerouted"
		| "auth.reauth_required"
		| "steer"
		| "plan"
		| "context.reset";
	/** model.rerouted */
	fromModel?: string;
	toModel?: string;
	/** steer: the user's own words, delivered into a turn already running. */
	origin?: string;
	clientMessageId?: string;
	/** steer: complete provider-neutral content copied from a promoted queue item. */
	content?: Array<{
		type: string;
		data?: string;
		mimeType?: string;
		uri?: string;
		name?: string;
		text?: string;
	}>;
}

/** A `plan` activity's payload, which is the same plan the turn carries. */
export interface PlanDetail {
	explanation?: string;
	steps?: PlanStep[];
}

export interface UsageDetail {
	inputTokens?: number;
	outputTokens?: number;
	totalTokens?: number;
}

/**
 * What a compaction reclaimed.
 *
 * Every field is optional because the provider reports none of them on its own
 * compaction event — the driver brackets them from the token-usage reports either
 * side of it, and omits what it could not observe. A compaction right after a
 * restart genuinely does not know what it saved, so the UI says nothing rather
 * than showing a zero.
 */
export interface CompactionDetail {
	tokensBefore?: number;
	tokensAfter?: number;
	tokensReclaimed?: number;
	contextWindow?: number;
}

/**
 * Whether a timeline item is the marker for a history compaction.
 *
 * Deliberately not a type predicate: it is used inside a boolean expression that
 * has already narrowed the item to an activity, and a predicate there would narrow
 * the negation to `never`.
 */
export function isCompaction(item: ConversationItem): boolean {
	return item.kind === "activity" && item.detail?.event === "compaction";
}

/**
 * Whether a timeline item is a steer: the user's own words, delivered into a turn
 * that was already running.
 *
 * Read by the `event` discriminator and not by the kind, the same convention a
 * compaction follows. A steer has to be an activity rather than a message because it
 * joins a turn in flight, and AO's only durable write that can attach to one is the
 * activity row — but it is still something a person said, and the timeline shows it
 * that way.
 */
export function isSteer(item: ConversationItem): boolean {
	return item.kind === "activity" && item.detail?.event === "steer";
}

/** The structured changed files on a `file_change`, or none. */
export function fileChangeFiles(activity: ConversationActivity): FileChangeFile[] {
	const files = activity.detail?.files;
	if (!Array.isArray(files)) return [];
	return files.filter((file): file is FileChangeFile => typeof file === "object" && file !== null);
}

/** The bare paths an `auto_review` action named, or none. */
export function reviewedPaths(activity: ConversationActivity): string[] {
	const files = activity.detail?.files;
	if (!Array.isArray(files)) return [];
	return files.filter((file): file is string => typeof file === "string");
}

/**
 * The plan an activity carries, when it is a plan row the turn did not absorb.
 *
 * The daemon writes both: the turn column answers "what is the plan now" and this
 * row answers "where in the conversation did the agent plan". They cannot disagree —
 * both are written from one event — so the surface prefers the turn's copy and only
 * falls back to this for a plan whose turn predates the controller.
 */
export function activityPlan(activity: ConversationActivity): ConversationPlan | undefined {
	const steps = activity.detail?.steps;
	if (!Array.isArray(steps) || steps.length === 0) return undefined;
	return { explanation: activity.detail?.explanation, steps };
}

export interface ConversationActivity {
	kind: "activity";
	id: string;
	turnId?: string;
	sequence: number;
	revision: number;
	activityKind: ActivityKind;
	status: ActivityStatus;
	/** The one-line label shown when collapsed. */
	summary: string;
	/**
	 * The typed payload for this kind. One intersection rather than a discriminated
	 * union because the daemon's `detail` is one JSON column read by every kind, and
	 * a union would force a cast at every access for no safety the fields do not
	 * already have — every one of them is optional, because every one of them is
	 * something a provider may not report.
	 */
	detail?: CommandDetail &
		ApprovalDetail &
		FileChangeDetail &
		UsageDetail &
		CompactionDetail &
		McpToolDetail &
		AutoReviewDetail &
		UserInputDetail &
		SystemEventDetail &
		PlanDetail;
	/**
	 * The provider's identifier for an approval. Resolving matches on this, so a
	 * card left on screen cannot answer a request that replaced it.
	 */
	requestId?: string;
	/** Provider correlation key used to attach nested agent activity to its parent. */
	providerItemId?: string;
	/**
	 * Authoritative for an approval. The provider varies what it offers per
	 * request and does not always include a decline, so buttons are rendered
	 * from this list and never from a fixed set.
	 */
	decisions?: DecisionOption[];
	createdAt: string;
}

/** One ordered entry in the timeline. */
export type ConversationItem = ConversationMessage | ConversationActivity;

/** AO's permission vocabulary, applied per turn in chat mode. */
export type ApprovalMode = "default" | "accept-edits" | "auto" | "bypass-permissions";

/**
 * The provider choices for the next turn.
 *
 * Every field is optional and empty means the provider's own default, so clearing
 * a choice and never making one are the same thing.
 */
export interface TurnSettings {
	model?: string;
	reasoningEffort?: string;
	approvalMode?: ApprovalMode;
}

/** One model the provider offers for this session. */
export interface ChatModel {
	id: string;
	displayName: string;
	description?: string;
	/** The model the provider would pick on its own. */
	default: boolean;
	/** Reasoning levels this model supports, in the provider's order. */
	efforts?: string[];
	defaultEffort?: string;
}

/** One provider-owned setting advertised for this live chat session. */
export interface ChatConfigOption {
	id: string;
	name: string;
	description?: string;
	/** ACP's semantic category, such as model, thought_level, or mode. */
	category?: string;
	type: "select" | "boolean";
	currentValue?: string;
	currentBoolean?: boolean;
	choices: ChatConfigChoice[];
}

/** One value offered by a select config option. */
export interface ChatConfigChoice {
	value: string;
	name: string;
	description?: string;
	/** Provider grouping is presentation data, not part of the value sent back. */
	group?: string;
	groupName?: string;
}

/** The two value shapes ACP session config supports. */
export type ChatConfigOptionValue = { value: string } | { enabled: boolean };

/**
 * One named skill the provider will let this session invoke.
 *
 * `name` is the invocable identifier and the only part the agent resolves;
 * `displayName` is a label and must never be what gets sent.
 */
export interface ChatSkill {
	name: string;
	displayName: string;
	description?: string;
	/** Short provider-authored placeholder for command arguments. */
	inputHint?: string;
	/** The provider's scope: user, repo, system, admin. */
	source?: string;
}

/** Health of the daemon's connection to the provider. */
export type ControllerState = "connecting" | "ready" | "busy" | "recovering" | "stopped";

/**
 * How full this conversation is.
 *
 * Current state, not a timeline entry: the provider reports it after every tool
 * call, and one row per report is what buried the conversation. What a reader
 * needs is the fraction, not the raw figure -- a token count with no window is a
 * number with no scale.
 */
export interface ConversationUsage {
	contextUsed: number;
	/** 0 when the provider would not state a window for this model. */
	contextWindow: number;
	/** The conversation's cumulative spend, which is a different question from
	 *  fullness: it grows without bound while context rises and falls. */
	inputTokens: number;
	outputTokens: number;
	cachedTokens: number;
	totalTokens: number;
	/** Cumulative provider-reported money, when the account reports it. */
	cost?: number | null;
	currency?: string;
}

/**
 * The account's quota position, which is why a turn can fail for reasons that
 * have nothing to do with what the user asked.
 */
export interface ConversationRateLimits {
	/** Percentages in 0..100. Negative means the provider did not report that
	 *  window, which is not the same as reporting it empty. */
	primaryUsedPercent: number;
	secondaryUsedPercent: number;
	/** Seconds remaining, not an absolute instant. */
	primaryResetsInSeconds?: number;
	secondaryResetsInSeconds?: number;
	planLabel?: string;
}

/**
 * The provider answering with a model other than the one that was asked for.
 *
 * Deliberately separate from `settings`: settings say what the user chose, this says
 * what actually replied. Without it the surface keeps advertising a model that is not
 * the one producing the answers.
 */
export interface ModelReroute {
	fromModel?: string;
	toModel: string;
	/** The provider's own word for why, carried verbatim rather than translated. */
	reason?: string;
	/** The turn it happened on, so the substitution can be pointed at an exchange. */
	providerTurnId?: string;
	at: string;
}

/** The provider account this conversation runs under. */
export interface ConversationAccount {
	authMode?: string;
	planLabel?: string;
	/**
	 * When the provider last demanded credentials AO does not hold. Present means the
	 * session has stopped working for a reason no retry will fix.
	 */
	reauthRequiredAt?: string;
	reauthReason?: string;
}

/** The provider's own lifecycle view of a thread. */
export type ThreadStatus = "active" | "idle" | "not_loaded" | "system_error" | "closed";

/**
 * The provider's thread lifecycle, which is NOT AO's controller state.
 *
 * The controller banner reports the daemon's connection to the agent process. This
 * reports what the provider says about the thread behind it, and the two disagree
 * routinely — a healthy controller can be attached to a thread the provider has put
 * into `system_error`.
 */
export interface ConversationThreadState {
	status?: ThreadStatus;
	/** The provider's active flags. A thread can be active AND blocked on a person. */
	waitingOn?: string[];
	archivedAt?: string;
	closedAt?: string;
}

/** One tool server's startup state. */
export interface McpServer {
	name: string;
	status: "starting" | "ready" | "failed" | "cancelled" | (string & {});
	/** The provider's failure text. */
	error?: string;
	/** Its classification of the failure, which is actionable in a way a message is not. */
	failureReason?: string;
}

export interface ConversationSnapshot {
	conversationId: string;
	sessionId: string;
	harness: string;
	mode: SessionMode;
	controller: { state: ControllerState; error?: string };
	turns: ConversationTurn[];
	/** Already ordered by sequence. The renderer does not re-sort. */
	items: ConversationItem[];
	latestSequence: number;
	oldestSequence: number;
	hasMoreBefore: boolean;
	/** The first provider-backed prompt in the active provider scope. */
	nativeForkAvailableAfterSequence?: number;
	activeBranchId?: string;
	branchedFromEarlierMessage?: boolean;
	branchPoints?: ConversationBranchPoint[];
	/** How the active historical branch acquired its provider context. */
	branchMaterialization?: ConversationBranchMaterialization;
	/** What the next turn will be sent with. Daemon-owned, so it survives a
	 *  restart and applies to turns AO dispatches on the user's behalf. */
	settings: TurnSettings;
	/** Undefined until the provider reports. Distinct from a conversation using
	 *  nothing, so the meter is withheld rather than drawn empty. */
	usage?: ConversationUsage;
	rateLimits?: ConversationRateLimits;
	/**
	 * When history was last summarized to reclaim context, or absent if never.
	 *
	 * Read from the snapshot rather than scanned out of the timeline: the timeline
	 * is unbounded and this is consulted on every render.
	 */
	compactedAt?: string;
	/**
	 * The name the provider gives this thread. Empty until something names it.
	 * The session's own label is set from this by the daemon, so this is here for
	 * the header rather than for the sidebar.
	 */
	title?: string;
	/** Present when the provider answered with a model other than the chosen one. */
	modelReroute?: ModelReroute;
	/** Absent until the provider says anything about the account. */
	account?: ConversationAccount;
	/** The provider's lifecycle view of the thread, which is not the controller's. */
	threadState?: ConversationThreadState;
	/**
	 * The tool servers this conversation can reach.
	 *
	 * It answers a question the timeline cannot: a tool call that never happened
	 * because its server failed to start reads, from the timeline alone, as the agent
	 * choosing not to use it.
	 */
	mcpServers?: McpServer[];
	/**
	 * What this session's provider can do, so a control is gated before it is drawn.
	 *
	 * Absent until a controller is live — which means "not known yet", NOT "cannot".
	 * Read it through `can()`, which encodes that distinction once.
	 */
	capabilities?: string[];
}

/** The provider-history fidelity of the active branch. */
export interface ConversationBranchMaterialization {
	strategy: "native" | "approximate_context" | (string & {});
	/** True when AO's bounded reconstructed context omitted some text. */
	replayTruncated: boolean;
}

/**
 * Whether this session's provider can do something.
 *
 * Absent capabilities mean the controller has not started, so nothing is known
 * yet; a control gated on this stays hidden until the daemon says otherwise. That
 * is deliberately not the same as offering the control and withdrawing it on the
 * refusal, which is what a client has to do without this and which reads as a bug
 * rather than as a difference between harnesses.
 */
export function can(snapshot: ConversationSnapshot, capability: string): boolean {
	return (snapshot.capabilities ?? []).includes(capability);
}

/**
 * Which model actually produced the answers.
 *
 * The reroute wins over the selection, because it is a correction to a claim the
 * selection already made.
 */
export function answeringModel(snapshot: ConversationSnapshot): string | undefined {
	return snapshot.modelReroute?.toModel ?? snapshot.settings.model;
}

/** Tool servers that will not answer, so the agent silently lacks their tools. */
export function brokenMcpServers(snapshot: ConversationSnapshot): McpServer[] {
	return (snapshot.mcpServers ?? []).filter(
		(server) => server.status === "failed" || server.status === "cancelled",
	);
}

/** Whether the provider is demanding credentials the daemon does not hold. */
export function needsReauth(snapshot: ConversationSnapshot): boolean {
	return Boolean(snapshot.account?.reauthRequiredAt);
}

/**
 * The turn currently in flight, if any.
 *
 * A running turn wins over a queued one: with a send queue both exist at once,
 * and the one the agent is actually working on is what the user is waiting on.
 */
export function activeTurn(snapshot: ConversationSnapshot): ConversationTurn | undefined {
	return (
		snapshot.turns.find((turn) => turn.state === "running") ??
		snapshot.turns.find((turn) => turn.state === "queued")
	);
}

/**
 * Turn ids for messages recorded but not yet sent.
 *
 * The daemon holds a mid-turn message instead of pushing it at a busy agent, so
 * the timeline has to distinguish "waiting to be sent" from "sent".
 */
export function queuedTurnIds(snapshot: ConversationSnapshot): Set<string> {
	return new Set(snapshot.turns.filter((turn) => turn.state === "queued").map((turn) => turn.id));
}

/** The pending approval a user must answer, if any. */
export function pendingApproval(snapshot: ConversationSnapshot): ConversationActivity | undefined {
	return snapshot.items.find(
		(item): item is ConversationActivity =>
			item.kind === "activity" && item.activityKind === "approval" && item.status === "pending",
	);
}

/** The structured question currently blocking the agent, if any. */
export function pendingUserInput(snapshot: ConversationSnapshot): ConversationActivity | undefined {
	return snapshot.items.find(
		(item): item is ConversationActivity =>
			item.kind === "activity" && item.activityKind === "user_input" && item.status === "pending",
	);
}
