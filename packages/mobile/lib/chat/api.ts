import { fetch as expoFetch } from "expo/fetch";
import { ApiError, apiRequest } from "../api";
import { authHeaders, httpBase, type ServerConfig } from "../config";
import type {
	ActivityDetail,
	ActivityKind,
	ActivityStatus,
	ApprovalMode,
	ChatConfigOption,
	ChatImage,
	ChatModel,
	ChatResource,
	ChatSkill,
	ControllerState,
	ConversationActivity,
	ConversationItem,
	ConversationMessage,
	ConversationSnapshot,
	ConversationTurn,
	DecisionOption,
	TurnSettings,
} from "./types";
import { parseSseFrame, takeSseFrames, type ConversationEvent } from "./sse";
import { mergeConversationPages, type ConversationPage } from "./snapshot";

export { parseSseFrame } from "./sse";
export type { ConversationEvent } from "./sse";
export { mergeConversationPages } from "./snapshot";
export type { ConversationPage } from "./snapshot";

const API = "/api/v1";
export const CHAT_INITIAL_PAGE_SIZE = 50;
export const CHAT_HISTORY_PAGE_SIZE = 200;

export type SessionInterfaceTransition = {
	id: string;
	sessionId: string;
	sourceMode: "chat" | "tui";
	targetMode: "chat" | "tui";
	policy: "drain" | "interrupt";
	phase:
		| "requested"
		| "preflighting"
		| "draining"
		| "source_stopping"
		| "source_stopped"
		| "target_starting"
		| "activating"
		| "completed"
		| "failed"
		| "cancelled"
		| "recovery_required";
	errorCode?: string;
	errorDetail?: string;
	createdAt: string;
	updatedAt: string;
	completedAt?: string;
	noticeAcknowledgedAt?: string;
};

export type SessionInterfaceTransitionStatus = {
	supported: boolean;
	targetMode: "chat" | "tui";
	reasonCode?: string;
	reason?: string;
	transition?: SessionInterfaceTransition;
};

export async function getSessionInterfaceTransition(
	cfg: ServerConfig,
	sessionId: string,
): Promise<SessionInterfaceTransitionStatus> {
	const res = await apiRequest(cfg, `${API}/sessions/${encodeURIComponent(sessionId)}/interface-transition`);
	return (await res.json()) as SessionInterfaceTransitionStatus;
}

export async function startSessionInterfaceTransition(
	cfg: ServerConfig,
	sessionId: string,
	targetMode: "chat" | "tui",
	policy: "drain" | "interrupt",
): Promise<SessionInterfaceTransition> {
	const res = await apiRequest(cfg, `${API}/sessions/${encodeURIComponent(sessionId)}/interface-transition`, {
		method: "POST",
		body: JSON.stringify({ targetMode, policy }),
	});
	const body = (await res.json()) as { transition: SessionInterfaceTransition };
	return body.transition;
}

export async function cancelSessionInterfaceTransition(
	cfg: ServerConfig,
	sessionId: string,
): Promise<void> {
	await apiRequest(cfg, `${API}/sessions/${encodeURIComponent(sessionId)}/interface-transition`, {
		method: "DELETE",
	});
}

export async function acknowledgeSessionInterfaceTransitionNotice(
	cfg: ServerConfig,
	sessionId: string,
	transitionId: string,
): Promise<SessionInterfaceTransition> {
	const res = await apiRequest(
		cfg,
		`${API}/sessions/${encodeURIComponent(sessionId)}/interface-transition/${encodeURIComponent(transitionId)}/notice-acknowledgement`,
		{ method: "PUT" },
	);
	const body = (await res.json()) as { transition: SessionInterfaceTransition };
	return body.transition;
}

type WireSnapshot = Omit<ConversationSnapshot, "items" | "controller"> & {
	controller: ControllerState;
	messages?: ConversationMessage[];
	activities?: Array<Omit<ConversationActivity, "detail" | "decisions"> & { detail?: Record<string, unknown> }>;
};

export type SendMessageInput = { text: string; clientMessageId: string; attachments?: ChatImage[]; resources?: ChatResource[] };
export type SendMessageResult = { turnId?: string; providerTurnId?: string; state?: string; duplicate: boolean };

export async function getConversationPage(
	cfg: ServerConfig,
	sessionId: string,
	beforeSequence?: number,
): Promise<ConversationPage> {
	const limit = beforeSequence === undefined ? CHAT_INITIAL_PAGE_SIZE : CHAT_HISTORY_PAGE_SIZE;
	const query = new URLSearchParams({ limit: String(limit) });
	if (beforeSequence !== undefined) query.set("beforeSequence", String(beforeSequence));
	const res = await apiRequest(
		cfg,
		`${API}/sessions/${encodeURIComponent(sessionId)}/conversation?${query.toString()}`,
	);
	return toSnapshot((await res.json()) as WireSnapshot);
}

export async function sendConversationMessage(
	cfg: ServerConfig,
	sessionId: string,
	input: SendMessageInput,
): Promise<SendMessageResult> {
	const res = await apiRequest(cfg, conversationPath(sessionId, "/messages"), {
		method: "POST",
		body: JSON.stringify(input),
	});
	return (await res.json()) as SendMessageResult;
}

export async function steerConversation(cfg: ServerConfig, sessionId: string, text: string, clientMessageId: string) {
	const res = await apiRequest(cfg, conversationPath(sessionId, "/steer"), {
		method: "POST",
		body: JSON.stringify({ text, clientMessageId }),
	});
	return res.json();
}

export async function interruptConversation(cfg: ServerConfig, sessionId: string): Promise<void> {
	await apiRequest(cfg, conversationPath(sessionId, "/interrupt"), { method: "POST" });
}

export async function compactConversation(cfg: ServerConfig, sessionId: string): Promise<void> {
	await apiRequest(cfg, conversationPath(sessionId, "/compact"), { method: "POST" });
}

export async function resolveApproval(
	cfg: ServerConfig,
	sessionId: string,
	requestId: string,
	decisionId: string,
): Promise<void> {
	await apiRequest(cfg, conversationPath(sessionId, `/approvals/${encodeURIComponent(requestId)}/resolve`), {
		method: "POST",
		body: JSON.stringify({ decisionId }),
	});
}

export async function resolveInput(
	cfg: ServerConfig,
	sessionId: string,
	requestId: string,
	action: "accept" | "decline" | "cancel",
	content?: Record<string, unknown>,
): Promise<void> {
	await apiRequest(cfg, conversationPath(sessionId, `/inputs/${encodeURIComponent(requestId)}/resolve`), {
		method: "POST",
		body: JSON.stringify({ action, content }),
	});
}

export async function rollbackConversation(cfg: ServerConfig, sessionId: string, turnId: string): Promise<number> {
	const res = await apiRequest(cfg, conversationPath(sessionId, `/turns/${encodeURIComponent(turnId)}/rollback`), {
		method: "POST",
	});
	const body = (await res.json()) as { turnsDiscarded?: number };
	return body.turnsDiscarded ?? 0;
}

export async function setConversationTitle(cfg: ServerConfig, sessionId: string, title: string): Promise<void> {
	await apiRequest(cfg, conversationPath(sessionId, "/title"), { method: "PUT", body: JSON.stringify({ title }) });
}

export async function getConversationModels(cfg: ServerConfig, sessionId: string): Promise<ChatModel[]> {
	const res = await apiRequest(cfg, conversationPath(sessionId, "/models"));
	const body = (await res.json()) as { models?: ChatModel[] };
	return body.models ?? [];
}

export async function setConversationSettings(cfg: ServerConfig, sessionId: string, settings: TurnSettings): Promise<void> {
	await apiRequest(cfg, conversationPath(sessionId, "/settings"), { method: "PATCH", body: JSON.stringify(settings) });
}

export async function getConversationConfigOptions(cfg: ServerConfig, sessionId: string): Promise<ChatConfigOption[]> {
	const res = await apiRequest(cfg, conversationPath(sessionId, "/config-options"));
	const body = (await res.json()) as { options?: ChatConfigOption[] };
	return body.options ?? [];
}

export async function setConversationConfigOption(
	cfg: ServerConfig,
	sessionId: string,
	optionId: string,
	value: { value: string } | { enabled: boolean },
): Promise<ChatConfigOption[]> {
	const res = await apiRequest(cfg, conversationPath(sessionId, `/config-options/${encodeURIComponent(optionId)}`), {
		method: "PATCH",
		body: JSON.stringify(value),
	});
	const body = (await res.json()) as { options?: ChatConfigOption[] };
	return body.options ?? [];
}

export async function getConversationSkills(cfg: ServerConfig, sessionId: string): Promise<ChatSkill[]> {
	const res = await apiRequest(cfg, conversationPath(sessionId, "/skills"));
	const body = (await res.json()) as { skills?: ChatSkill[] };
	return body.skills ?? [];
}

export async function reloadMcpServers(cfg: ServerConfig, sessionId: string): Promise<void> {
	await apiRequest(cfg, conversationPath(sessionId, "/mcp/reload"), { method: "POST" });
}

export async function stageConversationAttachments(
	cfg: ServerConfig,
	sessionId: string,
	attachments: ChatImage[],
): Promise<string[]> {
	const res = await apiRequest(cfg, `${API}/sessions/${encodeURIComponent(sessionId)}/attachments`, {
		method: "POST",
		body: JSON.stringify({ attachments }),
	}, 60_000);
	const body = (await res.json()) as { paths?: string[] };
	return body.paths ?? [];
}

export async function getWorkspacePaths(cfg: ServerConfig, sessionId: string): Promise<{ paths: string[]; truncated: boolean }> {
	const res = await apiRequest(cfg, `${API}/sessions/${encodeURIComponent(sessionId)}/workspace/files`);
	const body = (await res.json()) as { files?: Array<{ path: string; status?: string }>; truncated?: boolean };
	return { paths: (body.files ?? []).filter((file) => file.status !== "deleted").map((file) => file.path), truncated: Boolean(body.truncated) };
}

export type ShellTerminal = {
	handleId: string;
	projectId?: string;
	sessionId?: string;
	workingDir: string;
	title: string;
	createdAt: string;
};

export async function openSessionShell(
	cfg: ServerConfig,
	sessionId: string,
	projectId: string,
): Promise<ShellTerminal> {
	// Mobile presents the session shell as a route rather than a desktop tab strip.
	// Reuse the existing session-scoped handle so Back → Open shell returns to the
	// same process instead of leaking a new shell on every visit.
	const list = await apiRequest(cfg, `${API}/shell-terminals`);
	const listed = (await list.json()) as { shellTerminals?: ShellTerminal[] };
	const existing = (listed.shellTerminals ?? []).find((terminal) => terminal.sessionId === sessionId);
	if (existing) return existing;
	const res = await apiRequest(cfg, `${API}/shell-terminals`, {
		method: "POST",
		body: JSON.stringify({ projectId, sessionId }),
	});
	const body = (await res.json()) as { shellTerminal: ShellTerminal };
	return body.shellTerminal;
}

export async function closeShellTerminal(cfg: ServerConfig, handleId: string): Promise<void> {
	await apiRequest(cfg, `${API}/shell-terminals/${encodeURIComponent(handleId)}`, { method: "DELETE" });
}

/**
 * Authenticated durable CDC stream. The callback receives every event for the
 * session; the caller decides which caches to refresh. `expo/fetch` is used
 * deliberately because React Native's global fetch does not expose a streaming
 * response body consistently under Hermes.
 */
export async function streamGlobalConversationEvents(
	cfg: ServerConfig,
	after: number,
	signal: AbortSignal,
	onEvent: (event: ConversationEvent) => void,
	onCursorReset?: (cursor: number) => void,
): Promise<number> {
	const res = await expoFetch(`${httpBase(cfg)}${API}/events?after=${Math.max(0, after)}`, {
		headers: { ...authHeaders(cfg), Accept: "text/event-stream" },
		signal,
	});
	if (!res.ok) throw await streamError(res);
	if (!res.body) throw new Error("The mobile network stack did not provide an event stream");
	const advertisedAfterHeader = res.headers.get("X-AO-Event-After");
	const advertisedAfter = advertisedAfterHeader === null ? Number.NaN : Number(advertisedAfterHeader);
	const effectiveAfter = Number.isSafeInteger(advertisedAfter) && advertisedAfter >= 0
		? advertisedAfter
		: after;
	if (effectiveAfter !== after) onCursorReset?.(effectiveAfter);

	const decoder = new TextDecoder();
	const reader = res.body.getReader();
	let buffer = "";
	let cursor = effectiveAfter;
	try {
		while (!signal.aborted) {
			const { done, value } = await reader.read();
			if (done) break;
			buffer += decoder.decode(value, { stream: true });
			const split = takeSseFrames(buffer);
			buffer = split.remainder;
			for (const frame of split.frames) {
				const parsed = parseSseFrame(frame);
				if (parsed) {
					cursor = Math.max(cursor, parsed.seq);
					onEvent(parsed);
				}
			}
		}
	} finally {
		try {
			await reader.cancel();
		} catch {
			// Aborting the native request can close the reader before cancel runs.
		}
		reader.releaseLock();
	}
	return cursor;
}

function conversationPath(sessionId: string, suffix = ""): string {
	return `${API}/sessions/${encodeURIComponent(sessionId)}/conversation${suffix}`;
}

function toSnapshot(wire: WireSnapshot): ConversationSnapshot {
	const messages = (wire.messages ?? []).map((message) => ({ ...message, kind: "message" as const }));
	const activities = (wire.activities ?? []).map((activity): ConversationActivity => {
		const detail = (activity.detail ?? {}) as ActivityDetail;
		return {
			...activity,
			kind: "activity",
			activityKind: activity.activityKind as ActivityKind,
			status: activity.status as ActivityStatus,
			detail,
			decisions: readDecisions(detail),
		};
	});
	return {
		...wire,
		controller: { state: wire.controller },
		oldestSequence: wire.oldestSequence ?? wire.latestSequence + 1,
		hasMoreBefore: Boolean(wire.hasMoreBefore),
		settings: {
			model: wire.settings?.model || undefined,
			reasoningEffort: wire.settings?.reasoningEffort || undefined,
			approvalMode: (wire.settings?.approvalMode as ApprovalMode | undefined) || undefined,
		},
		turns: wire.turns ?? [],
		items: [...messages, ...activities].sort((a, b) => a.sequence - b.sequence),
	};
}

function readDecisions(detail: ActivityDetail): DecisionOption[] | undefined {
	if (!Array.isArray(detail.decisions)) return undefined;
	const decisions = detail.decisions.flatMap((option): DecisionOption[] => {
		if (!option || typeof option !== "object" || typeof option.id !== "string" || !option.id) return [];
		return [{ id: option.id, label: typeof option.label === "string" && option.label ? option.label : option.id }];
	});
	return decisions.length > 0 ? decisions : undefined;
}

async function streamError(res: Response): Promise<ApiError> {
	let message = `${res.status} ${res.statusText}`;
	let code: string | undefined;
	try {
		const body = (await res.json()) as { message?: string; error?: string; code?: string };
		const detail = body.message ?? body.error;
		if (detail) message += ` - ${detail}`;
		code = body.code;
	} catch {
		// A proxy may answer HTML; the status remains enough to classify it.
	}
	return new ApiError(res.status, message, code);
}
