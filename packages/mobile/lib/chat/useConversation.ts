import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AppState } from "react-native";
import type { ServerConfig } from "../config";
import {
	compactConversation,
	getConversationConfigOptions,
	getConversationModels,
	getConversationPage,
	getConversationSkills,
	mergeConversationPages,
	reloadMcpServers,
	resolveApproval,
	resolveInput,
	rollbackConversation,
	sendConversationMessage,
	setConversationConfigOption,
	setConversationSettings,
	setConversationTitle,
	stageConversationAttachments,
	steerConversation,
	interruptConversation,
	type ConversationPage,
} from "./api";
import type { ChatConfigOption, ChatImage, ChatModel, ChatResource, ChatSkill, ConversationSnapshot, TurnSettings } from "./types";
import { cachedConversationState, createMobileConversationPageCache, discardHistoricalPages } from "./snapshot";
import { conversationActionError, conversationErrorCode } from "./conversationErrors";
import { subscribeConversationEvents } from "./conversationEvents";
import { createAsyncValueCache } from "./asyncValueCache";
import { createRequestGate } from "./requestGate";
import { loadTurnOptionCatalog } from "./turnOptionsCatalog";

const REFRESH_DEBOUNCE_MS = 120;
const conversationPageCache = createMobileConversationPageCache();
const turnOptionsCache = createAsyncValueCache<string, { models: ChatModel[]; configOptions: ChatConfigOption[] }>(16, 30_000);
const skillsCache = createAsyncValueCache<string, ChatSkill[]>(16, 30_000);

export type PendingSend = {
	id: string;
	text: string;
	state: "sending" | "failed";
	error?: string;
	attachments?: ChatImage[];
	resources?: ChatResource[];
};

export type ConversationAction =
	| "steer"
	| "interrupt"
	| "approval"
	| "input"
	| "compact"
	| "rollback"
	| "settings"
	| "config"
	| "mcp"
	| "rename";

export type MobileConversation = {
	snapshot?: ConversationSnapshot;
	loading: boolean;
	refreshing: boolean;
	loadingOlder: boolean;
	error?: string;
	unavailable?: { code?: string; message: string };
	models: ChatModel[];
	configOptions: ChatConfigOption[];
	skills: ChatSkill[];
	pendingSends: PendingSend[];
	pendingActions: readonly ConversationAction[];
	actionError?: string;
	actionErrors: Partial<Record<ConversationAction, string>>;
	actionCodes: Partial<Record<ConversationAction, string>>;
	refresh(): Promise<void>;
	loadOlder(): Promise<void>;
	loadTurnOptions(options?: { refresh?: boolean }): Promise<{ models: ChatModel[]; configOptions: ChatConfigOption[] }>;
	loadSkills(): Promise<ChatSkill[]>;
	send(text: string, attachments?: ChatImage[], resources?: ChatResource[]): Promise<void>;
	retrySend(id: string): Promise<void>;
	discardSend(id: string): void;
	steer(text: string): Promise<void>;
	interrupt(): Promise<void>;
	resolveApproval(requestId: string, decisionId: string): Promise<void>;
	resolveInput(requestId: string, action: "accept" | "decline" | "cancel", content?: Record<string, unknown>): Promise<void>;
	compact(): Promise<void>;
	rollback(turnId: string): Promise<number>;
	chooseSettings(settings: TurnSettings): Promise<void>;
	setConfigOption(optionId: string, value: { value: string } | { enabled: boolean }): Promise<ChatConfigOption[]>;
	reloadMcp(): Promise<void>;
	rename(title: string): Promise<void>;
};

export function useMobileConversation(
	cfg: ServerConfig | null,
	sessionId: string,
): MobileConversation {
	const cacheKey = cfg ? conversationPageCacheKey(cfg, sessionId) : "";
	const [initialState] = useState(() => cacheKey
		? cachedConversationState(conversationPageCache, cacheKey)
		: { pages: [] as ConversationPage[], loading: true });
	const [pages, setPages] = useState<ConversationPage[]>(initialState.pages);
	const [loading, setLoading] = useState(initialState.loading);
	const [refreshing, setRefreshing] = useState(false);
	const [loadingOlder, setLoadingOlder] = useState(false);
	const [error, setError] = useState<string>();
	const [unavailable, setUnavailable] = useState<{ code?: string; message: string }>();
	const [models, setModels] = useState<ChatModel[]>([]);
	const [configOptions, setConfigOptions] = useState<ChatConfigOption[]>([]);
	const [skills, setSkills] = useState<ChatSkill[]>([]);
	const [pendingSends, setPendingSends] = useState<PendingSend[]>([]);
	const [pendingActions, setPendingActions] = useState<ConversationAction[]>([]);
	const [actionError, setActionError] = useState<string>();
	const [actionErrors, setActionErrors] = useState<Partial<Record<ConversationAction, string>>>({});
	const [actionCodes, setActionCodes] = useState<Partial<Record<ConversationAction, string>>>({});
	const mounted = useRef(true);
	const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
	const refreshGate = useRef(createRequestGate()).current;

	const snapshot = useMemo(() => mergeConversationPages(pages), [pages]);
	const hasConversation = Boolean(snapshot);
	const hasProviderConfig = Boolean(snapshot?.capabilities?.includes("config_options"));

	const refresh = useCallback(async () => {
		if (!cfg) return;
		const request = refreshGate.begin();
		setRefreshing(true);
		try {
			const live = await getConversationPage(cfg, sessionId);
			if (!mounted.current || !refreshGate.isCurrent(request)) return;
			setPages((old) => {
				const next = old[0]?.conversationId && old[0].conversationId !== live.conversationId
					? [live]
					: [live, ...old.slice(1)];
				conversationPageCache.set(cacheKey, next);
				return next;
			});
			setUnavailable(undefined);
			setError(undefined);
		} catch (cause) {
			if (!mounted.current || !refreshGate.isCurrent(request)) return;
			const classified = classifyConversationError(cause);
			if (classified.permanent) setUnavailable({ code: classified.code, message: classified.message });
			else setError(classified.message);
		} finally {
			if (mounted.current && refreshGate.isCurrent(request)) {
				setLoading(false);
				setRefreshing(false);
			}
		}
	}, [cacheKey, cfg, refreshGate, sessionId]);

	const scheduleRefresh = useCallback(() => {
		if (refreshTimer.current) clearTimeout(refreshTimer.current);
		refreshTimer.current = setTimeout(() => void refresh(), REFRESH_DEBOUNCE_MS);
	}, [refresh]);

	const loadOlder = useCallback(async () => {
		if (!cfg || !snapshot?.hasMoreBefore || loadingOlder) return;
		setLoadingOlder(true);
		try {
			const older = await getConversationPage(cfg, sessionId, snapshot.oldestSequence);
			if (mounted.current) setPages((old) => [...old, older]);
		} catch (cause) {
			if (mounted.current) setActionError(conversationActionError(cause));
		} finally {
			if (mounted.current) setLoadingOlder(false);
		}
	}, [cfg, sessionId, snapshot?.hasMoreBefore, snapshot?.oldestSequence, loadingOlder]);

	useEffect(() => {
		mounted.current = true;
		const cached = cacheKey
			? cachedConversationState(conversationPageCache, cacheKey)
			: { pages: [] as ConversationPage[], loading: true };
		setPages(cached.pages);
		setLoading(cached.loading);
		setUnavailable(undefined);
		void refresh();
		return () => {
			mounted.current = false;
			refreshGate.invalidate();
			if (refreshTimer.current) clearTimeout(refreshTimer.current);
		};
	}, [cacheKey, refresh, refreshGate]);

	const loadTurnOptions = useCallback(async (options?: { refresh?: boolean }) => {
		if (!cfg || unavailable || !hasConversation) return { models, configOptions };
		if (options?.refresh) turnOptionsCache.delete(cacheKey);
		const catalog = await turnOptionsCache.load(cacheKey, async () => {
			return loadTurnOptionCatalog({
				hasProviderConfig,
				loadModels: () => getConversationModels(cfg, sessionId),
				loadConfigOptions: () => getConversationConfigOptions(cfg, sessionId),
			});
		});
		if (mounted.current) {
			setModels(catalog.models);
			setConfigOptions(catalog.configOptions);
		}
		return catalog;
	}, [cacheKey, cfg, configOptions, hasConversation, hasProviderConfig, models, sessionId, unavailable]);

	const loadSkills = useCallback(async () => {
		if (!cfg || unavailable || !hasConversation) return skills;
		try {
			const next = await skillsCache.load(cacheKey, () => getConversationSkills(cfg, sessionId));
			if (mounted.current) setSkills(next);
			return next;
		} catch {
			return skills;
		}
	}, [cacheKey, cfg, hasConversation, sessionId, skills, unavailable]);

	useEffect(() => {
		if (!cfg || unavailable) return;
		return subscribeConversationEvents(sessionId, (event) => {
			if (event.payload?.conversationId) scheduleRefresh();
		});
	}, [cfg, sessionId, scheduleRefresh, unavailable]);

	useEffect(() => {
		const subscription = AppState.addEventListener("change", (state) => {
			if (state === "active") void refresh();
		});
		return () => subscription.remove();
	}, [refresh]);

	const runAction = useCallback(
		async <T,>(kind: ConversationAction, action: () => Promise<T>, resetHistoricalPages = false): Promise<T> => {
			setPendingActions((old) => old.includes(kind) ? old : [...old, kind]);
			setActionError(undefined);
			setActionErrors((old) => ({ ...old, [kind]: undefined }));
			setActionCodes((old) => ({ ...old, [kind]: undefined }));
			try {
				const result = await action();
				if (resetHistoricalPages) setPages(discardHistoricalPages);
				await refresh();
				return result;
			} catch (cause) {
				const message = conversationActionError(cause);
				setActionError(message);
				setActionErrors((old) => ({ ...old, [kind]: message }));
				setActionCodes((old) => ({ ...old, [kind]: conversationErrorCode(cause) }));
				throw new Error(message);
			} finally {
				setPendingActions((old) => old.filter((candidate) => candidate !== kind));
			}
		},
		[refresh],
	);

	const deliver = useCallback(
		async (pending: PendingSend) => {
			if (!cfg) throw new Error("No AO server configured");
			setPendingSends((old) => upsertPending(old, { ...pending, state: "sending", error: undefined }));
			try {
				await sendConversationMessage(cfg, sessionId, {
					text: pending.text,
					clientMessageId: pending.id,
					attachments: pending.attachments,
					resources: pending.resources,
				});
				setPendingSends((old) => old.filter((item) => item.id !== pending.id));
				await refresh();
			} catch (cause) {
				const message = conversationActionError(cause);
				setPendingSends((old) => upsertPending(old, { ...pending, state: "failed", error: message }));
				throw new Error(message);
			}
		},
		[cfg, sessionId, refresh],
	);

	const send = useCallback(
		async (text: string, attachments?: ChatImage[], resources?: ChatResource[]) => {
			const id = clientMessageId();
			let message = text;
			let nativeAttachments = attachments;
			if (attachments?.length) {
				const paths = await requireConfig(cfg, (c) => stageConversationAttachments(c, sessionId, attachments));
				message = withAttachmentReferences(message, paths);
				if (!snapshot?.capabilities?.includes("images")) nativeAttachments = undefined;
			}
			await deliver({ id, text: message, state: "sending", attachments: nativeAttachments, resources });
		},
		[cfg, sessionId, snapshot?.capabilities, deliver],
	);

	const retrySend = useCallback(
		async (id: string) => {
			const pending = pendingSends.find((item) => item.id === id);
			if (pending) await deliver(pending);
		},
		[pendingSends, deliver],
	);

	const discardSend = useCallback((id: string) => {
		setPendingSends((old) => old.filter((item) => item.id !== id));
	}, []);
	const steer = useCallback(
		(text: string) => runAction("steer", () => requireConfig(cfg, (c) => steerConversation(c, sessionId, text, clientMessageId()))),
		[cfg, runAction, sessionId],
	);
	const interrupt = useCallback(
		() => runAction("interrupt", () => requireConfig(cfg, (c) => interruptConversation(c, sessionId))),
		[cfg, runAction, sessionId],
	);
	const resolveApprovalAction = useCallback(
		(requestId: string, decisionId: string) =>
			runAction("approval", () => requireConfig(cfg, (c) => resolveApproval(c, sessionId, requestId, decisionId))),
		[cfg, runAction, sessionId],
	);
	const resolveInputAction = useCallback(
		(requestId: string, action: "accept" | "decline" | "cancel", content?: Record<string, unknown>) =>
			runAction("input", () => requireConfig(cfg, (c) => resolveInput(c, sessionId, requestId, action, content))),
		[cfg, runAction, sessionId],
	);
	const compact = useCallback(
		() => runAction("compact", () => requireConfig(cfg, (c) => compactConversation(c, sessionId))),
		[cfg, runAction, sessionId],
	);
	const rollback = useCallback(
		(turnId: string) => runAction("rollback", () => requireConfig(cfg, (c) => rollbackConversation(c, sessionId, turnId)), true),
		[cfg, runAction, sessionId],
	);
	const chooseSettings = useCallback(
		(settings: TurnSettings) => runAction("settings", () => requireConfig(cfg, (c) => setConversationSettings(c, sessionId, settings))),
		[cfg, runAction, sessionId],
	);
	const setConfigOption = useCallback(
		(optionId: string, value: { value: string } | { enabled: boolean }) =>
			runAction("config", async () => {
				const options = await requireConfig(cfg, (c) => setConversationConfigOption(c, sessionId, optionId, value));
				setConfigOptions(options);
				turnOptionsCache.set(cacheKey, { models, configOptions: options });
				return options;
			}),
		[cacheKey, cfg, models, runAction, sessionId],
	);
	const reloadMcp = useCallback(
		async () => {
			await runAction("mcp", () => requireConfig(cfg, (c) => reloadMcpServers(c, sessionId)));
			skillsCache.delete(cacheKey);
			setSkills([]);
		},
		[cacheKey, cfg, runAction, sessionId],
	);
	const rename = useCallback(
		(title: string) => runAction("rename", () => requireConfig(cfg, (c) => setConversationTitle(c, sessionId, title))),
		[cfg, runAction, sessionId],
	);

	return {
		snapshot,
		loading,
		refreshing,
		loadingOlder,
		error,
		unavailable,
		models,
		configOptions,
		skills,
		pendingSends,
		pendingActions,
		actionError,
		actionErrors,
		actionCodes,
		refresh,
		loadOlder,
		loadTurnOptions,
		loadSkills,
		send,
		retrySend,
		discardSend,
		steer,
		interrupt,
		resolveApproval: resolveApprovalAction,
		resolveInput: resolveInputAction,
		compact,
		rollback,
		chooseSettings,
		setConfigOption,
		reloadMcp,
		rename,
	};
}

async function requireConfig<T>(cfg: ServerConfig | null, action: (cfg: ServerConfig) => Promise<T>): Promise<T> {
	if (!cfg) throw new Error("No AO server configured");
	return action(cfg);
}

function clientMessageId(): string {
	return `mobile-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}

function upsertPending(items: PendingSend[], next: PendingSend): PendingSend[] {
	const index = items.findIndex((item) => item.id === next.id);
	if (index < 0) return [...items, next];
	return items.map((item, at) => (at === index ? next : item));
}

function classifyConversationError(error: unknown): { permanent: boolean; code?: string; message: string } {
	const code = typeof error === "object" && error !== null && "code" in error ? String(error.code ?? "") : undefined;
	const permanentCodes = new Set([
		"SESSION_MODE_MISMATCH",
		"SESSION_NOT_FOUND",
		"SESSION_MODE_UNSUPPORTED",
		"CHAT_DRIVER_UNAVAILABLE",
		"CHAT_DRIVER_INCOMPATIBLE",
		"CHAT_AUTH_REQUIRED",
		"CHAT_RESUME_FAILED",
		"CHAT_CONTROLLER_NOT_READY",
	]);
	return {
		permanent: Boolean(code && permanentCodes.has(code)),
		code,
		message: conversationActionError(error),
	};
}

function conversationPageCacheKey(cfg: ServerConfig, sessionId: string): string {
	return `${cfg.secure ? "https" : "http"}://${cfg.host}:${cfg.httpPort}/${cfg.password}/${sessionId}`;
}

function withAttachmentReferences(text: string, paths: string[]): string {
	if (paths.length === 0) return text;
	const references = paths.map((path) => `- ${path}`).join("\n");
	return `${text.trim()}${text.trim() ? "\n\n" : ""}Attached files are available in the worktree:\n${references}`;
}
