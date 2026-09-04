import type { SessionMode } from "../api";

export type ControllerState = "connecting" | "ready" | "busy" | "recovering" | "stopped";
export type TurnState = "queued" | "running" | "completed" | "interrupted" | "failed";
export type ActivityStatus = "running" | "completed" | "failed" | "cancelled" | "pending" | "resolved";
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

export type ApprovalMode = "default" | "manual" | "accept-edits" | "auto" | "dont-ask" | "bypass-permissions";
export type TurnSettings = { model?: string; reasoningEffort?: string; approvalMode?: ApprovalMode };

export type PlanStep = { text: string; status: "pending" | "in_progress" | "completed" };
export type ConversationPlan = { explanation?: string; steps: PlanStep[] };
export type DiffFile = {
	path: string;
	oldPath?: string;
	status: "added" | "modified" | "deleted" | "renamed";
	additions: number;
	deletions: number;
};
export type TurnDiff = { files: DiffFile[]; truncated?: boolean };

export type ConversationTurn = {
	id: string;
	state: TurnState;
	providerTurnId?: string;
	errorMessage?: string;
	requestedAt: string;
	startedAt?: string;
	completedAt?: string;
	rolledBack?: boolean;
	diff?: TurnDiff;
	plan?: ConversationPlan;
};

export type DeliveryState = "queued" | "sending" | "accepted" | "uncertain" | "failed";
export type ConversationMessage = {
	kind: "message";
	id: string;
	turnId?: string;
	sequence: number;
	revision: number;
	role: "user" | "assistant";
	origin: "human" | "automation" | "daemon" | "provider";
	text: string;
	streaming: boolean;
	delivery?: DeliveryState;
	senderLabel?: string;
	createdAt: string;
};

export type DecisionOption = { id: string; label: string };
export type FileChange = DiffFile & { patch?: string; patchTruncated?: boolean };
export type InputProperty = {
	type?: "string" | "number" | "integer" | "boolean" | "array";
	title?: string;
	description?: string;
	default?: unknown;
	enum?: unknown[];
	oneOf?: Array<{ const?: unknown; title?: string; description?: string }>;
	items?: {
		type?: "string";
		anyOf?: Array<{ const?: unknown; title?: string; description?: string }>;
	};
	minimum?: number;
	maximum?: number;
	minLength?: number;
	maxLength?: number;
};

/**
 * Provider-neutral activity payload. Every field is optional because providers
 * report different subsets; the activity kind tells the renderer which fields
 * are meaningful without making the phone infer protocol-specific structures.
 */
export type ActivityDetail = {
	text?: string;
	textTruncated?: boolean;
	command?: string;
	rawCommand?: string;
	cwd?: string;
	output?: string | unknown;
	outputSource?: "stream" | "aggregate";
	outputMayBePartial?: boolean;
	outputTruncated?: boolean;
	exitCode?: number;
	durationMs?: number;
	reason?: string;
	terminalInput?: string;
	terminalInputTruncated?: boolean;
	processId?: number;
	parentProviderItemId?: string;
	nestedAgent?: boolean;
	providerToolName?: string;
	providerTitle?: string;
	subagentType?: string;
	terminalId?: string;
	signal?: string;
	files?: FileChange[] | string[];
	patchOutput?: string;
	patchOutputTruncated?: boolean;
	server?: string;
	toolName?: string;
	namespace?: string;
	arguments?: unknown;
	result?: unknown;
	error?: string;
	success?: boolean;
	progress?: string;
	progressTruncated?: boolean;
	reviewId?: string;
	targetItemId?: string;
	actionType?: string;
	riskLevel?: string;
	rationale?: string;
	decisionSource?: string;
	status?: string;
	userAuthorization?: string;
	host?: string;
	commandSource?: string;
	event?: "compaction" | "model.rerouted" | "auth.reauth_required" | "steer" | "plan" | string;
	fromModel?: string;
	toModel?: string;
	origin?: string;
	clientMessageId?: string;
	explanation?: string;
	steps?: PlanStep[];
	tokensBefore?: number;
	tokensAfter?: number;
	tokensReclaimed?: number;
	contextWindow?: number;
	inputMode?: "form" | "url";
	message?: string;
	schema?: {
		title?: string;
		description?: string;
		type?: "object";
		required?: string[];
		properties?: Record<string, InputProperty>;
	};
	url?: string;
	elicitationId?: string;
	decisions?: DecisionOption[];
	[key: string]: unknown;
};

export type ConversationActivity = {
	kind: "activity";
	id: string;
	turnId?: string;
	sequence: number;
	revision: number;
	activityKind: ActivityKind;
	status: ActivityStatus;
	summary: string;
	detail?: ActivityDetail;
	requestId?: string;
	providerItemId?: string;
	decisions?: DecisionOption[];
	createdAt: string;
};

export type ConversationItem = ConversationMessage | ConversationActivity;
export type ConversationUsage = {
	contextUsed: number;
	contextWindow: number;
	inputTokens: number;
	outputTokens: number;
	cachedTokens: number;
	totalTokens: number;
	cost?: number | null;
	currency?: string;
};
export type ConversationRateLimits = {
	primaryUsedPercent: number;
	secondaryUsedPercent: number;
	primaryResetsInSeconds?: number;
	secondaryResetsInSeconds?: number;
	planLabel?: string;
};
export type ConversationAccount = {
	authMode?: string;
	planLabel?: string;
	reauthRequiredAt?: string;
	reauthReason?: string;
};
export type ConversationThreadState = {
	status?: "active" | "idle" | "not_loaded" | "system_error" | "closed";
	waitingOn?: string[];
	archivedAt?: string;
	closedAt?: string;
};
export type McpServer = { name: string; status: string; error?: string; failureReason?: string };

export type ConversationSnapshot = {
	conversationId: string;
	sessionId: string;
	harness: string;
	mode: SessionMode;
	controller: { state: ControllerState; error?: string };
	latestSequence: number;
	oldestSequence: number;
	hasMoreBefore: boolean;
	turns: ConversationTurn[];
	items: ConversationItem[];
	settings: TurnSettings;
	title?: string;
	usage?: ConversationUsage;
	rateLimits?: ConversationRateLimits;
	compactedAt?: string;
	modelReroute?: { fromModel?: string; toModel: string; reason?: string; providerTurnId?: string; at: string };
	account?: ConversationAccount;
	threadState?: ConversationThreadState;
	mcpServers?: McpServer[];
	capabilities?: string[];
};

export type ChatModel = {
	id: string;
	displayName: string;
	description?: string;
	default: boolean;
	efforts?: string[];
	defaultEffort?: string;
};
export type ChatConfigChoice = {
	value: string;
	name: string;
	description?: string;
	group?: string;
	groupName?: string;
};
export type ChatConfigOption = {
	id: string;
	name: string;
	description?: string;
	category?: string;
	type: "select" | "boolean";
	currentValue?: string;
	currentBoolean?: boolean;
	choices: ChatConfigChoice[];
};
export type ChatSkill = {
	name: string;
	displayName: string;
	description?: string;
	inputHint?: string;
	source?: string;
};
export type ChatImage = { mimeType: string; data: string };
export type ChatResource = { uri: string; name: string; mimeType?: string; text?: string };

export function activeTurn(snapshot: ConversationSnapshot): ConversationTurn | undefined {
	return snapshot.turns.find((turn) => turn.state === "running") ?? snapshot.turns.find((turn) => turn.state === "queued");
}

export function can(snapshot: ConversationSnapshot | undefined, capability: string): boolean {
	return Boolean(snapshot?.capabilities?.includes(capability));
}

export function pendingApproval(snapshot: ConversationSnapshot): ConversationActivity | undefined {
	return snapshot.items.find(
		(item): item is ConversationActivity =>
			item.kind === "activity" && item.activityKind === "approval" && item.status === "pending",
	);
}

export function pendingInput(snapshot: ConversationSnapshot): ConversationActivity | undefined {
	return snapshot.items.find(
		(item): item is ConversationActivity =>
			item.kind === "activity" && item.activityKind === "user_input" && item.status === "pending",
	);
}

export function brokenMcpServers(snapshot: ConversationSnapshot): McpServer[] {
	return (snapshot.mcpServers ?? []).filter((server) => server.status === "failed" || server.status === "cancelled");
}

export function turnForItem(snapshot: ConversationSnapshot, item: ConversationItem): ConversationTurn | undefined {
	return item.turnId ? snapshot.turns.find((turn) => turn.id === item.turnId) : undefined;
}
