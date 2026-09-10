/**
 * Live conversation data for a Chat session.
 *
 * The daemon serves the snapshot already ordered by sequence, so this hook maps
 * the wire shape onto the view model and does not re-sort, re-derive turn state,
 * or decide which approvals are actionable. Those are daemon decisions.
 *
 * Live changes arrive through the daemon event channel. Older history is fetched
 * in bounded pages only when the reader asks for it.
 */

import {
	type InfiniteData,
	type QueryClient,
	useInfiniteQuery,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useCallback, useState } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorCode, apiErrorMessage } from "../lib/api-client";
import { workspaceQueryKey } from "./useWorkspaceQuery";
import type {
	ActivityKind,
	ApprovalMode,
	ActivityStatus,
	ConversationActivity,
	ConversationItem,
	ConversationMessage,
	ConversationSnapshot,
	ControllerState,
	DecisionOption,
	DiffStatus,
	McpServer,
	MessageOrigin,
	MessageRole,
	ChatConfigOption,
	ChatConfigOptionValue,
	ChatModel,
	ChatSkill,
	PlanStep,
	PlanStepStatus,
	QueuedMessageEditOptions,
	SessionMode,
	ThreadStatus,
	TurnSettings,
	TurnState,
} from "../types/conversation";

type WireSnapshot = components["schemas"]["ConversationSnapshotResponse"];
type WireMessage = components["schemas"]["ConversationMessageResponse"];
type WireActivity = components["schemas"]["ConversationActivityResponse"];
type WireImageContent = components["schemas"]["ConversationImageContentRequest"];
type WireResourceContent = components["schemas"]["ConversationResourceContentRequest"];

export interface ConversationSendInput {
	text: string;
	attachments?: WireImageContent[];
	resources?: WireResourceContent[];
}

interface ConversationSendMutationInput {
	targetSessionId: string;
	clientMessageId: string;
	input: ConversationSendInput;
}

interface ConversationSessionMutationInput {
	targetSessionId: string;
}

interface ConversationRetryMutationInput extends ConversationSessionMutationInput {
	requestId: string;
	sourceTurnId: string;
}

interface ConversationEditMutationInput extends ConversationRetryMutationInput {
	text: string;
}

export const conversationQueryRoot = ["conversation"] as const;

export function conversationQueryKey(sessionId: string) {
	return [...conversationQueryRoot, sessionId] as const;
}

export function conversationModelsQueryKey(sessionId: string) {
	return ["conversation-models", sessionId] as const;
}

export function conversationConfigOptionsQueryKey(sessionId: string) {
	return ["conversation-config-options", sessionId] as const;
}

const conversationDispatchTrackingQueryKey = ["conversation-dispatch-tracking"] as const;
type ConversationDispatchOperation = "edit" | "retry" | "send";
interface ConversationDispatchDescriptor {
	operation: ConversationDispatchOperation;
	requestId: string;
	sourceTurnId?: string;
}
type ConversationDispatchTracking =
	| (ConversationDispatchDescriptor & { state: "pending" })
	| (ConversationDispatchDescriptor & { state: "accepted"; turnId: string });
type ConversationDispatchTrackingBySession = Record<string, ConversationDispatchTracking>;

function claimConversationDispatch(
	queryClient: QueryClient,
	targetSessionId: string,
	requestId: string,
	operation: ConversationDispatchOperation,
	sourceTurnId?: string,
): boolean {
	const current =
		queryClient.getQueryData<ConversationDispatchTrackingBySession>(
			conversationDispatchTrackingQueryKey,
		) ?? {};
	if (current[targetSessionId]?.state === "pending") return false;
	queryClient.setQueryData<ConversationDispatchTrackingBySession>(
		conversationDispatchTrackingQueryKey,
		{
			...current,
			[targetSessionId]: { operation, requestId, sourceTurnId, state: "pending" },
		},
	);
	return true;
}

function acceptConversationDispatch(
	queryClient: QueryClient,
	targetSessionId: string,
	requestId: string,
	turnId: string,
): void {
	queryClient.setQueryData<ConversationDispatchTrackingBySession>(
		conversationDispatchTrackingQueryKey,
		(current = {}) => {
			const tracked = current[targetSessionId];
			if (tracked?.requestId !== requestId) return current;
			return {
				...current,
				[targetSessionId]: { ...tracked, state: "accepted", turnId },
			};
		},
	);
}

function releaseConversationDispatch(
	queryClient: QueryClient,
	targetSessionId: string,
	requestId: string,
): void {
	queryClient.setQueryData<ConversationDispatchTrackingBySession>(
		conversationDispatchTrackingQueryKey,
		(current = {}) => {
			if (current[targetSessionId]?.requestId !== requestId) return current;
			const next = { ...current };
			delete next[targetSessionId];
			return next;
		},
	);
}

const CONVERSATION_PAGE_SIZE = 200;
const CONFIG_OPTIONS_POLL_INTERVAL_MS = 5_000;

/**
 * Answers that will never change on a retry. SESSION_MODE_MISMATCH is permanent
 * while the session's committed controller is TUI; the others describe a session or
 * controller state the client should explain rather than poll at.
 */
const PERMANENT_CODES = new Set([
	"SESSION_MODE_MISMATCH",
	"SESSION_NOT_FOUND",
	"SESSION_MODE_UNSUPPORTED",
	"CHAT_AUTH_REQUIRED",
]);

export interface ConversationQueryResult {
	snapshot?: ConversationSnapshot;
	isLoading: boolean;
	/** Set when the session exists but has no chat conversation to show. */
	unavailable?: { code: string; message: string };
	error?: string;
	hasOlder: boolean;
	isLoadingOlder: boolean;
	loadOlder: () => void;
}

export function useConversation(sessionId: string | undefined): ConversationQueryResult {
	const query = useInfiniteQuery({
		queryKey: conversationQueryKey(sessionId ?? ""),
		enabled: Boolean(sessionId),
		initialPageParam: undefined as number | undefined,
		queryFn: async ({ pageParam }) => {
			const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/conversation", {
				params: {
					path: { sessionId: sessionId as string },
					query: {
						beforeSequence: pageParam,
						limit: CONVERSATION_PAGE_SIZE,
					},
				},
			});
			if (error) throw error;
			return toSnapshot(data as WireSnapshot);
		},
		getNextPageParam: (page) => (page.hasMoreBefore ? page.oldestSequence : undefined),
		select: (data) => mergeConversationPages(data.pages),
		// A mode mismatch is authoritative for this committed controller epoch, so
		// retrying the same request cannot help and would leave the surface loading
		// instead of explaining why there is no conversation. Only genuinely
		// transient failures are retried.
		retry: (attempt, error) => {
			const code = apiErrorCode(error);
			if (code && PERMANENT_CODES.has(code)) return false;
			return attempt < 2;
		},
	});

	if (query.error) {
		const code = apiErrorCode(query.error);
		// The session is currently owned by Terminal UI (or its Chat controller is
		// absent). A deliberate interface switch can change that later, but this
		// request cannot, so explain it rather than retrying.
		if (code === "SESSION_MODE_MISMATCH" || code === "CHAT_CONTROLLER_NOT_READY") {
			return {
				isLoading: false,
				unavailable: { code, message: apiErrorMessage(query.error) },
				hasOlder: false,
				isLoadingOlder: false,
				loadOlder: () => {},
			};
		}
		return {
			isLoading: false,
			error: apiErrorMessage(query.error),
			hasOlder: false,
			isLoadingOlder: false,
			loadOlder: () => {},
		};
	}

	return {
		snapshot: query.data,
		isLoading: query.isLoading,
		hasOlder: query.hasNextPage,
		isLoadingOlder: query.isFetchingNextPage,
		loadOlder: () => {
			void query.fetchNextPage();
		},
	};
}

/** Commands against a conversation. Each refetches the snapshot on success. */
export function useConversationCommands(sessionId: string | undefined) {
	const queryClient = useQueryClient();
	const trackedDispatches = useQuery({
		queryKey: conversationDispatchTrackingQueryKey,
		queryFn: async (): Promise<ConversationDispatchTrackingBySession> => ({}),
		initialData: {} as ConversationDispatchTrackingBySession,
		enabled: false,
		// Dispatched work is a safety boundary for Chat -> Terminal. Keep both the
		// in-flight request and its accepted turn until the exact durable row is
		// observed, even if the Chat surface is unmounted.
		//
		// This cache is intentionally renderer-lifetime, not persisted. A full reload
		// starts with work unknown (which already requires a policy choice) and then a
		// fresh daemon snapshot; any later direct switch is drain-only, whose daemon
		// gate atomically rejects new intake or drains work admitted before it. Without
		// a durable request-reconciliation API, persisting an unresolved HTTP sentinel
		// would instead leave sessions permanently and incorrectly busy.
		gcTime: Number.POSITIVE_INFINITY,
		staleTime: Number.POSITIVE_INFINITY,
	}).data;
	const trackedDispatch = sessionId ? trackedDispatches[sessionId] : undefined;
	const invalidateSession = useCallback(
		async (targetSessionId: string) => {
			await queryClient.invalidateQueries({ queryKey: conversationQueryKey(targetSessionId) });
		},
		[queryClient],
	);
	const invalidate = useCallback(() => {
		if (sessionId) {
			// Refresh in the background. Awaiting the refetch in mutation onSuccess kept
			// steer, queue, and approval mutations pending after the daemon had already
			// answered, which left the composer spinner stuck and blocked further sends.
			void invalidateSession(sessionId).catch(() => {});
		}
	}, [invalidateSession, sessionId]);
	const refreshSessionInBackground = useCallback(
		(targetSessionId: string) => {
			void invalidateSession(targetSessionId).catch(() => {});
		},
		[invalidateSession],
	);

	const send = useMutation({
		onMutate: (variables: ConversationSendMutationInput) => {
			queryClient.setQueryData<ConversationDispatchTrackingBySession>(
				conversationDispatchTrackingQueryKey,
				(current = {}) => {
					const tracked = current[variables.targetSessionId];
					if (tracked && tracked.requestId !== variables.clientMessageId) return current;
					return {
						...current,
						[variables.targetSessionId]: {
							operation: "send",
							requestId: variables.clientMessageId,
							state: "pending",
						},
					};
				},
			);
		},
		mutationFn: async ({
			targetSessionId,
			clientMessageId,
			input,
		}: ConversationSendMutationInput) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/messages",
				{
					params: { path: { sessionId: targetSessionId } },
					// A stable id per attempt makes a retry idempotent: the daemon
					// answers `duplicate` instead of opening a second provider turn.
					body: { ...input, clientMessageId },
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: (data, variables) => {
			const acceptedTurnId = data?.turnId;
			if (acceptedTurnId) {
				if (data.state === "queued") {
					// A queued row is already durable and did not start a new provider turn,
					// so keeping the accepted-turn safety marker would block the next queue
					// entry until a refetch observes this id — and the composer would swallow
					// further Enter presses with no error.
					releaseConversationDispatch(
						queryClient,
						variables.targetSessionId,
						variables.clientMessageId,
					);
				} else {
					// A refetch can briefly return the pre-send snapshot. Keep the accepted
					// turn locally visible as pending until that exact durable row arrives.
					acceptConversationDispatch(
						queryClient,
						variables.targetSessionId,
						variables.clientMessageId,
						acceptedTurnId,
					);
				}
			} else {
				// A duplicate response intentionally has no turn id: the daemon already
				// delivered this idempotency key, so there is no exact new row this
				// renderer can wait to observe. Release only this request's sentinel.
				releaseConversationDispatch(
					queryClient,
					variables.targetSessionId,
					variables.clientMessageId,
				);
			}
			// Delivery is already authoritative at this point. Refresh in the
			// background so a slow conversation refetch cannot keep send.isPending
			// true and leave the composer disabled or spinning after the daemon
			// accepted the message.
			void refreshSessionInBackground(variables.targetSessionId);
		},
		onError: (_error, variables) => {
			releaseConversationDispatch(
				queryClient,
				variables.targetSessionId,
				variables.clientMessageId,
			);
		},
	});

	const resolve = useMutation({
		mutationFn: async (input: { requestId: string; decisionId: string }) => {
			const { error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/approvals/{requestId}/resolve",
				{
					params: {
						path: {
							sessionId: sessionId as string,
							requestId: input.requestId,
						},
					},
					body: { decisionId: input.decisionId },
				},
			);
			if (error) throw error;
		},
		onSuccess: invalidate,
	});

	const resolveInput = useMutation({
		mutationFn: async (input: {
			requestId: string;
			action: "accept" | "decline" | "cancel";
			content?: Record<string, unknown>;
		}) => {
			const { error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/inputs/{requestId}/resolve",
				{
					params: {
						path: {
							sessionId: sessionId as string,
							requestId: input.requestId,
						},
					},
					body: { action: input.action, content: input.content },
				},
			);
			if (error) throw error;
		},
		onSuccess: invalidate,
	});

	const interrupt = useMutation({
		mutationFn: async ({ targetSessionId }: ConversationSessionMutationInput) => {
			const { error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/interrupt",
				{ params: { path: { sessionId: targetSessionId } } },
			);
			if (error) throw error;
		},
		onSuccess: (_data, variables) => refreshSessionInBackground(variables.targetSessionId),
		// A failed interrupt (e.g. CHAT_NO_ACTIVE_TURN) means the cached turn
		// state is wrong. Refetch so the UI discovers the real state instead of
		// keeping a Working bar the user cannot dismiss.
		onError: (_error, variables) => refreshSessionInBackground(variables.targetSessionId),
	});

	const resume = useMutation({
		mutationFn: async () => {
			const { data, error, response } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/resume-agent",
				{
					params: { path: { sessionId: sessionId as string } },
				},
			);
			if (error)
				throw new Error(apiErrorMessage(error, `Failed to resume agent (${response.status})`));
			return data;
		},
		onSuccess: () => {
			invalidate();
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});

	/**
	 * Summarize earlier history to reclaim context.
	 *
	 * Without this a long conversation eventually cannot accept another turn at all:
	 * every turn re-sends the history, so context fills on its own. It is the
	 * difference between a session that works for an hour and one that works for a
	 * day.
	 *
	 * The daemon answers as soon as the provider accepts, and the provider then does
	 * the work over the next several seconds. So there is nothing to show on success
	 * beyond the refetch — the reclaim arrives on the timeline as its own entry,
	 * which is where it belongs, since it is durable history rather than the answer
	 * to one request.
	 */
	const compact = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/compact",
				{
					params: { path: { sessionId: sessionId as string } },
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: invalidate,
	});

	const chooseSettings = useMutation({
		mutationFn: async ({ targetSessionId, settings }: { targetSessionId: string; settings: TurnSettings }) => {
			const { data, error } = await apiClient.PATCH(
				"/api/v1/sessions/{sessionId}/conversation/settings",
				{
					params: { path: { sessionId: targetSessionId } },
					body: settings,
				},
			);
			if (error) throw error;
			return data;
		},
		// Confirm from the daemon's response before enabling Remember. A background
		// snapshot refetch can be slow; it must not expose the previous permission.
		onSuccess: async (settings, { targetSessionId }) => {
			const queryKey = conversationQueryKey(targetSessionId);
			await queryClient.cancelQueries({ queryKey });
			if (settings) {
				queryClient.setQueryData<InfiniteData<ConversationSnapshot>>(queryKey, (current) =>
					current ? {
						...current,
						pages: current.pages.map((page, index) => index === 0
							? { ...page, settings: settings as TurnSettings } : page),
					} : current,
				);
			}
			refreshSessionInBackground(targetSessionId);
		},
	});

	/**
	 * Undo back to a turn. This asks the agent to forget, not the timeline to hide:
	 * the daemon discards the history provider-side and marks its own rows to match,
	 * so a success has to be followed by a refetch rather than an optimistic edit —
	 * how much was discarded is the daemon's answer, not the client's guess.
	 */
	/**
	 * Guidance delivered INTO the running turn.
	 *
	 * Not interrupt-then-resend. An interrupt throws the turn away — its reasoning,
	 * its half-finished tool calls, the command it has running — and the resend starts
	 * from a cold start. A steer leaves all of it in place: the turn keeps its id, its
	 * context and its in-flight work, and settles `completed`.
	 *
	 * Refusals are ordinary outcomes here, not failures, and they are kept apart
	 * because the advice differs: one means "send this as a new message instead", one
	 * means "wait and try again", and one means this harness cannot do it at all.
	 */
	const steer = useMutation({
		mutationFn: async (input: { text: string; attachments?: WireImageContent[] }) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/steer",
				{
					params: { path: { sessionId: sessionId as string } },
					body: { ...input, clientMessageId: crypto.randomUUID() },
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: invalidate,
	});

	const promoteQueuedTurn = useMutation({
		mutationFn: async (turnId: string) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/steer",
				{
					params: {
						path: { sessionId: sessionId as string, turnId },
					},
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: invalidate,
	});

	const cancelQueuedTurn = useMutation({
		mutationFn: async (turnId: string) => {
			const { error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/cancel",
				{
					params: {
						path: { sessionId: sessionId as string, turnId },
					},
				},
			);
			if (error) throw error;
		},
		onSuccess: invalidate,
	});

	const editQueuedTurn = useMutation({
		mutationFn: async ({ turnId, text, ...options }: { turnId: string; text: string } & QueuedMessageEditOptions) => {
			const { error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/queue/edit",
				{
					params: {
						path: { sessionId: sessionId as string, turnId },
					},
					body: { text, ...options },
				},
			);
			if (error) throw new Error(apiErrorMessage(error, "Could not save queued message edit"));
		},
		onSuccess: invalidate,
	});

	const reorderQueuedTurns = useMutation({
		mutationFn: async (turnIds: string[]) => {
			const { error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/queue/reorder",
				{
					params: {
						path: { sessionId: sessionId as string },
					},
					body: { turnIds },
				},
			);
			if (error) throw new Error(apiErrorMessage(error, "Could not reorder queued messages"));
		},
		onMutate: async (turnIds) => {
			if (!sessionId) return;
			const queryKey = conversationQueryKey(sessionId);
			await queryClient.cancelQueries({ queryKey });
			const previous = queryClient.getQueryData<InfiniteData<ConversationSnapshot>>(queryKey);
			if (previous) {
				queryClient.setQueryData<InfiniteData<ConversationSnapshot>>(
					queryKey,
					applyQueuedTurnOrderToPages(previous, turnIds),
				);
			}
			return { previous };
		},
		onError: (_error, _turnIds, context) => {
			if (!sessionId || !context?.previous) return;
			queryClient.setQueryData(conversationQueryKey(sessionId), context.previous);
		},
		onSettled: () => {
			invalidate();
		},
	});

	/**
	 * Restart the tool servers.
	 *
	 * Worth offering because a server that failed to start is not a transient blip the
	 * agent will retry: it will simply never call those tools, and nothing in the
	 * timeline says so. Refused mid-turn, which is why the control is disabled rather
	 * than allowed to fail.
	 */
	const reloadMcp = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/mcp/reload",
				{
					params: { path: { sessionId: sessionId as string } },
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: invalidate,
	});

	const rollback = useMutation({
		mutationFn: async (turnId: string) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/rollback",
				{
					params: {
						path: { sessionId: sessionId as string, turnId },
					},
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: invalidate,
	});

	const retryTurn = useMutation({
		mutationFn: async ({ targetSessionId, sourceTurnId }: ConversationRetryMutationInput) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/retry",
				{
					params: {
						path: { sessionId: targetSessionId, turnId: sourceTurnId },
					},
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: (data, variables) => {
			if (data?.turnId) {
				acceptConversationDispatch(
					queryClient,
					variables.targetSessionId,
					variables.requestId,
					data.turnId,
				);
			} else {
				releaseConversationDispatch(queryClient, variables.targetSessionId, variables.requestId);
			}
			void refreshSessionInBackground(variables.targetSessionId);
		},
		onError: (_error, variables) => {
			releaseConversationDispatch(queryClient, variables.targetSessionId, variables.requestId);
		},
	});

	const editMessage = useMutation({
		mutationFn: async ({
			targetSessionId,
			sourceTurnId,
			requestId,
			text,
		}: ConversationEditMutationInput) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/edit",
				{
					params: {
						path: { sessionId: targetSessionId, turnId: sourceTurnId },
					},
					body: { text, clientMessageId: requestId },
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: (data, variables) => {
			if (data?.turnId) {
				acceptConversationDispatch(
					queryClient,
					variables.targetSessionId,
					variables.requestId,
					data.turnId,
				);
			} else {
				releaseConversationDispatch(queryClient, variables.targetSessionId, variables.requestId);
			}
			void refreshSessionInBackground(variables.targetSessionId);
		},
		onError: (_error, variables) => {
			releaseConversationDispatch(queryClient, variables.targetSessionId, variables.requestId);
		},
	});

	const activateBranch = useMutation({
		mutationFn: async (branchId: string) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/branches/{branchId}/activate",
				{
					params: {
						path: { sessionId: sessionId as string, branchId },
					},
				},
			);
			if (error) throw error;
			return data;
		},
		onSettled: invalidate,
	});
	const acknowledgeAcceptedTurn = useCallback(
		(turnId: string) => {
			if (!sessionId) return;
			queryClient.setQueryData<ConversationDispatchTrackingBySession>(
				conversationDispatchTrackingQueryKey,
				(current = {}) => {
					const tracked = current[sessionId];
					if (tracked?.state !== "accepted" || tracked.turnId !== turnId) return current;
					const next = { ...current };
					delete next[sessionId];
					return next;
				},
			);
		},
		[queryClient, sessionId],
	);
	const sendTargetsCurrentSession = send.variables?.targetSessionId === sessionId;
	const interruptTargetsCurrentSession = interrupt.variables?.targetSessionId === sessionId;
	const retryTargetsCurrentSession = retryTurn.variables?.targetSessionId === sessionId;
	const editTargetsCurrentSession = editMessage.variables?.targetSessionId === sessionId;

	return {
		send: (input: string | ConversationSendInput) => {
			if (!sessionId) return Promise.reject(new Error("No conversation session is selected."));
			const clientMessageId = crypto.randomUUID();
			// React cannot disable the composer until its next render. Claim the
			// session in the shared registry synchronously so two Enter events in the
			// same tick cannot both cross the transport boundary.
			if (!claimConversationDispatch(queryClient, sessionId, clientMessageId, "send")) {
				return Promise.reject(new Error("Conversation work is already being sent for this session."));
			}
			return send.mutateAsync({
				targetSessionId: sessionId,
				clientMessageId,
				input: typeof input === "string" ? { text: input } : input,
			});
		},
		pendingAcceptedTurnId:
			trackedDispatch?.state === "accepted" ? trackedDispatch.turnId : undefined,
		acknowledgeAcceptedTurn,
		resolve: (requestId: string, decisionId: string) => resolve.mutate({ requestId, decisionId }),
		resolveInput: (
			requestId: string,
			action: "accept" | "decline" | "cancel",
			content?: Record<string, unknown>,
		) => resolveInput.mutateAsync({ requestId, action, content }),
		interrupt: () => interrupt.mutate({ targetSessionId: sessionId as string }),
		resumeAgent: () => resume.mutateAsync(),
		resumingAgent: resume.isPending,
		resumeError: resume.error ? apiErrorMessage(resume.error) : undefined,
		compact: () => compact.mutateAsync(),
		choosingSettings: chooseSettings.isPending && chooseSettings.variables?.targetSessionId === sessionId,
		chooseSettings: (settings: TurnSettings) => chooseSettings.mutate({ targetSessionId: sessionId as string, settings }),
		/** A compaction is in flight provider-side and takes seconds, so it reads as
		 *  its own state rather than folding into the generic busy flag, which also
		 *  gates the composer. */
		compacting: compact.isPending,
		/**
		 * The daemon refuses a compaction while a turn is running, because the
		 * provider would silently discard that turn to make room. Surfaced so the
		 * control can explain itself instead of appearing to do nothing.
		 */
		compactUnavailable:
			apiErrorCode(compact.error) === "CHAT_COMPACTION_UNSUPPORTED"
				? "This agent cannot compact its history"
				: apiErrorCode(compact.error) === "CHAT_COMPACTION_BUSY"
					? "Stop the current turn before compacting"
					: undefined,
		rollback: (turnId: string) => rollback.mutateAsync(turnId),
		rollbackPending: rollback.isPending,
		rollbackError: rollback.error ? apiErrorMessage(rollback.error) : undefined,
		retryControl: {
			retry: (turnId: string) => {
				if (!sessionId) return Promise.reject(new Error("No conversation session is selected."));
				const requestId = crypto.randomUUID();
				if (!claimConversationDispatch(queryClient, sessionId, requestId, "retry", turnId)) {
					return Promise.reject(new Error("Conversation work is already being sent for this session."));
				}
				return retryTurn.mutateAsync({
					requestId,
					sourceTurnId: turnId,
					targetSessionId: sessionId,
				});
			},
			pending:
				trackedDispatch?.operation === "retry" ||
				(retryTurn.isPending && retryTargetsCurrentSession),
			error:
				retryTargetsCurrentSession && retryTurn.error
					? apiErrorMessage(retryTurn.error)
					: undefined,
			turnId:
				trackedDispatch?.operation === "retry"
					? trackedDispatch.sourceTurnId
					: retryTargetsCurrentSession
						? retryTurn.variables?.sourceTurnId
						: undefined,
		},
		editMessage: (turnId: string, text: string) => {
			if (!sessionId) return Promise.reject(new Error("No conversation session is selected."));
			const requestId = crypto.randomUUID();
			if (!claimConversationDispatch(queryClient, sessionId, requestId, "edit", turnId)) {
				return Promise.reject(new Error("Conversation work is already being sent for this session."));
			}
			return editMessage.mutateAsync({
				requestId,
				sourceTurnId: turnId,
				targetSessionId: sessionId,
				text,
			});
		},
		editMessagePending:
			trackedDispatch?.operation === "edit" ||
			(editMessage.isPending && editTargetsCurrentSession),
		editMessageError:
			editTargetsCurrentSession && editMessage.error
				? apiErrorMessage(editMessage.error)
				: undefined,
		activateBranch: (branchId: string) => activateBranch.mutateAsync(branchId),
		activateBranchPending: activateBranch.isPending,
		activateBranchError: activateBranch.error ? apiErrorMessage(activateBranch.error) : undefined,
		steer: (text: string, attachments?: WireImageContent[]) =>
			steer.mutateAsync({
				text,
				...(attachments?.length ? { attachments } : {}),
			}),
		promoteQueuedTurn: (turnId: string) => promoteQueuedTurn.mutateAsync(turnId),
		cancelQueuedTurn: (turnId: string) => cancelQueuedTurn.mutateAsync(turnId),
		editQueuedTurn: (turnId: string, text: string, options?: QueuedMessageEditOptions) => {
			if (!sessionId) return Promise.reject(new Error("No conversation session is selected."));
			return editQueuedTurn.mutateAsync({ turnId, text, ...options });
		},
		reorderQueuedTurns: (turnIds: string[]) => {
			if (!sessionId) return Promise.reject(new Error("No conversation session is selected."));
			return reorderQueuedTurns.mutateAsync(turnIds);
		},
		promoteQueuedTurnPendingTurnId: promoteQueuedTurn.isPending
			? promoteQueuedTurn.variables
			: undefined,
		cancelQueuedTurnPendingTurnId: cancelQueuedTurn.isPending ? cancelQueuedTurn.variables : undefined,
		editQueuedTurnPendingTurnId: editQueuedTurn.isPending ? editQueuedTurn.variables?.turnId : undefined,
		sendPending: send.isPending && sendTargetsCurrentSession,
		steerPending: steer.isPending,
		/**
		 * Why the last steer was refused, or undefined. Only the retryable and
		 * send-instead cases produce copy: `CHAT_STEER_UNSUPPORTED` is answered by
		 * hiding the control entirely, since the harness will never accept one.
		 */
		steerRefusal: steerRefusal(steer.error),
		/**
		 * A harness that cannot steer at all. Learned from the daemon's typed refusal
		 * rather than from the harness name, and sticky for the life of the surface: the
		 * answer is a property of the driver, not of the moment.
		 */
		steerUnsupported: apiErrorCode(steer.error) === "CHAT_STEER_UNSUPPORTED",
		reloadMcpServers: () => reloadMcp.mutateAsync(),
		reloadingMcpServers: reloadMcp.isPending,
		mcpReloadUnsupported: apiErrorCode(reloadMcp.error) === "CHAT_MCP_RELOAD_UNSUPPORTED",
		mcpReloadError:
			reloadMcp.error && apiErrorCode(reloadMcp.error) !== "CHAT_MCP_RELOAD_UNSUPPORTED"
				? apiErrorMessage(reloadMcp.error)
				: undefined,
		busy:
			trackedDispatch?.state === "pending" ||
			(send.isPending && sendTargetsCurrentSession) ||
			resolve.isPending ||
			resolveInput.isPending ||
			(interrupt.isPending && interruptTargetsCurrentSession),
		error:
			(sendTargetsCurrentSession && send.error) ||
			resolve.error ||
			(interruptTargetsCurrentSession && interrupt.error) ||
			chooseSettings.error
				? apiErrorMessage(
							(sendTargetsCurrentSession ? send.error : undefined) ??
							resolve.error ??
							(interruptTargetsCurrentSession ? interrupt.error : undefined) ??
							chooseSettings.error,
					)
				: undefined,
	};
}

/**
 * What to tell the user about a refused steer.
 *
 * The daemon's own message is preferred for the retryable case because only it knows
 * which kind of turn refused — a compaction and a review read differently, and
 * "cannot be steered" alone leaves the user with nothing to do next.
 */
function steerRefusal(error: unknown): string | undefined {
	const code = apiErrorCode(error);
	if (!code) return undefined;
	switch (code) {
		case "CHAT_NO_ACTIVE_TURN":
			return "The turn finished before this landed. Send it as a message instead.";
		case "CHAT_TURN_NOT_STEERABLE":
			return `${apiErrorMessage(error)} Try again once it finishes.`;
		case "CHAT_STEER_UNSUPPORTED":
			// Answered by hiding the control, so there is nothing to say.
			return undefined;
		case "CHAT_STEER_TEXT_REQUIRED":
			return "Type something to steer with.";
		default:
			return apiErrorMessage(error);
	}
}

/**
 * The models the provider offers for this session.
 *
 * Fetched from the live conversation rather than a table in AO, and only while a
 * session is open: the catalog depends on the account's entitlements, which the
 * provider knows and AO does not.
 */
export function useConversationModels(sessionId: string | undefined, enabled: boolean) {
	const query = useQuery({
		queryKey: conversationModelsQueryKey(sessionId ?? ""),
		enabled: Boolean(sessionId) && enabled,
		// The catalog changes on the scale of provider releases, not turns.
		staleTime: 5 * 60 * 1000,
		retry: false,
		queryFn: async () => {
			const { data, error } = await apiClient.GET(
				"/api/v1/sessions/{sessionId}/conversation/models",
				{
					params: { path: { sessionId: sessionId as string } },
				},
			);
			if (error) throw error;
			return (data?.models ?? []) as ChatModel[];
		},
	});
	return {
		// An empty list is a real answer: this agent offers no choice, so the picker
		// hides itself rather than showing an error the user cannot act on.
		models: query.data ?? [],
		isLoading: query.isLoading,
	};
}

/**
 * Provider-owned controls advertised for this live session.
 *
 * Unlike AO's durable turn settings, these are an ACP catalog whose values and
 * available choices may change after any selection (choosing a model can replace
 * the effort choices, for example). The daemon therefore returns the complete
 * catalog after every mutation and that response replaces the cache atomically.
 */
export function useConversationConfigOptions(sessionId: string | undefined, enabled: boolean) {
	const queryClient = useQueryClient();
	const queryKey = conversationConfigOptionsQueryKey(sessionId ?? "");
	// Set for as long as a selection is being written. Cancelling in-flight reads
	// only closes half the race — without also holding the poll, the interval can
	// start a fresh read mid-write whose pre-change catalog lands after the
	// mutation's own result and reverts the picker the user just used.
	const [writing, setWriting] = useState(false);
	const query = useQuery({
		queryKey,
		enabled: Boolean(sessionId) && enabled,
		retry: false,
		// ACP can push a replacement catalog when one option changes. Until the
		// renderer consumes daemon change events, a light poll keeps those updates
		// visible without coupling them to conversation history polling.
		refetchInterval: writing ? false : CONFIG_OPTIONS_POLL_INTERVAL_MS,
		queryFn: async () => {
			const { data, error } = await apiClient.GET(
				"/api/v1/sessions/{sessionId}/conversation/config-options",
				{
					params: { path: { sessionId: sessionId as string } },
				},
			);
			if (error) throw error;
			return (data?.options ?? []) as ChatConfigOption[];
		},
	});
	const mutation = useMutation({
		// Held across the whole write, paired with the cancel below: `onMutate`
		// runs before the request and `onSettled` after the result is committed,
		// so no poll can start or land inside that window.
		onMutate: () => setWriting(true),
		onSettled: () => setWriting(false),
		mutationFn: async ({ optionId, value }: { optionId: string; value: ChatConfigOptionValue }) => {
			// A read already in flight when the user picked would otherwise land
			// after this mutation's setQueryData and put the pre-change catalog
			// back, reverting the picker to the old value until the next poll.
			await queryClient.cancelQueries({ queryKey });
			const { data, error } = await apiClient.PATCH(
				"/api/v1/sessions/{sessionId}/conversation/config-options/{configId}",
				{
					params: {
						path: {
							sessionId: sessionId as string,
							configId: optionId,
						},
					},
					body: value,
				},
			);
			if (error) throw error;
			return (data?.options ?? []) as ChatConfigOption[];
		},
		onSuccess: (options) => queryClient.setQueryData(queryKey, options),
	});

	return {
		options: query.data ?? [],
		loaded: query.isSuccess,
		setOption: (optionId: string, value: ChatConfigOptionValue) =>
			mutation.mutateAsync({ optionId, value }),
		pending: mutation.isPending,
		error: mutation.error || query.error ? apiErrorMessage(mutation.error ?? query.error) : undefined,
	};
}

/**
 * The named skills this session's provider will accept.
 *
 * Read from the live conversation for the same reason the model catalog is: skills
 * come from the user's own agent config and from the repo's own files, so a list AO
 * held would offer commands that no longer exist and hide ones just written.
 *
 * An empty list is a real answer and the composer depends on being able to tell it
 * from a failure — with no skills, `/` has to stay an ordinary character rather than
 * opening an empty menu.
 */
export function useConversationSkills(sessionId: string | undefined, enabled: boolean) {
	const query = useQuery({
		queryKey: ["conversation-skills", sessionId ?? ""],
		enabled: Boolean(sessionId) && enabled,
		// ACP agents publish this catalog asynchronously and may replace it later.
		// Polling also keeps Codex project skills current without introducing a
		// second renderer event channel solely for ephemeral provider metadata. The
		// catalog can be large and changes rarely, so it intentionally refreshes much
		// less often than conversation state.
		staleTime: 60 * 1000,
		refetchInterval: 60 * 1000,
		retry: false,
		queryFn: async () => {
			const { data, error } = await apiClient.GET(
				"/api/v1/sessions/{sessionId}/conversation/skills",
				{
					params: { path: { sessionId: sessionId as string } },
				},
			);
			if (error) throw error;
			return (data?.skills ?? []) as ChatSkill[];
		},
	});
	return { skills: query.data ?? [], isLoading: query.isLoading };
}

/**
 * Every path in the session worktree, for @-mention completion.
 *
 * The daemon already serves this list — tracked and untracked, non-ignored — so the
 * whole set is fetched once and filtered in the composer. That keeps a keystroke
 * from costing a round trip, which is what makes the menu feel like part of typing.
 * The endpoint caps itself, and `truncated` is respected rather than presented as a
 * complete list.
 */
export function useWorkspaceFilePaths(sessionId: string | undefined, enabled: boolean) {
	const query = useQuery({
		queryKey: ["workspace-file-paths", sessionId ?? ""],
		enabled: Boolean(sessionId) && enabled,
		// The agent edits files as it works, so this goes stale; refetched on demand
		// rather than polled, since a mention menu that is a minute out of date is
		// still useful and polling every session would not be.
		staleTime: 30 * 1000,
		retry: false,
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/files", {
				params: { path: { sessionId: sessionId as string } },
			});
			if (error) throw error;
			return {
				// A deleted path cannot be read, so offering it would insert a
				// reference the agent then fails to resolve.
				paths: (data?.files ?? [])
					.filter((file) => file.status !== "deleted")
					.map((file) => file.path),
				truncated: Boolean(data?.truncated),
			};
		},
	});
	return {
		paths: query.data?.paths ?? [],
		truncated: query.data?.truncated ?? false,
		isLoading: query.isLoading,
	};
}

/**
 * Write staged images into the session worktree.
 *
 * Returns the worktree-relative paths the agent can open. The composer names those
 * paths in the message it sends, which is how an image reaches an agent whose
 * conversation carries text: the same thing spawn does for the opening brief.
 */
export function useStageAttachments(sessionId: string | undefined) {
	return useCallback(
		async (attachments: { mimeType: string; data: string }[]): Promise<string[]> => {
			if (!sessionId || attachments.length === 0) return [];
			const { data, error } = await apiClient.POST("/api/v1/sessions/{sessionId}/attachments", {
				params: { path: { sessionId } },
				body: { attachments },
			});
			if (error) throw error;
			return data?.paths ?? [];
		},
		[sessionId],
	);
}

/* -------------------------------------------------------------------------- */

/**
 * Merge messages and activities into one ordered timeline.
 *
 * They are separate tables because they have different write patterns, but the
 * reader sees one sequence — which is why sequence is conversation-scoped rather
 * than per-table.
 */
function toSnapshot(wire: WireSnapshot): ConversationSnapshot {
	const items: ConversationItem[] = [
		...(wire.messages ?? []).map(toMessage),
		...(wire.activities ?? []).map(toActivity),
	].sort((a, b) => a.sequence - b.sequence);

	return {
		conversationId: wire.conversationId,
		sessionId: wire.sessionId,
		harness: wire.harness ?? "",
		mode: wire.mode as SessionMode,
		controller: { state: wire.controller as ControllerState },
		latestSequence: wire.latestSequence,
		oldestSequence: wire.oldestSequence ?? wire.latestSequence + 1,
		hasMoreBefore: wire.hasMoreBefore ?? false,
		nativeForkAvailableAfterSequence: wire.nativeForkAvailableAfterSequence ?? 0,
		settings: {
			model: wire.settings?.model || undefined,
			reasoningEffort: wire.settings?.reasoningEffort || undefined,
			approvalMode: (wire.settings?.approvalMode as ApprovalMode | undefined) || undefined,
		},
		// Absent means the provider has not reported, which the meter renders as
		// nothing rather than as an empty bar.
		usage: wire.usage ? { ...wire.usage } : undefined,
		rateLimits: wire.rateLimits ? { ...wire.rateLimits } : undefined,
		compactedAt: wire.compactedAt ?? undefined,
		title: wire.title || undefined,
		// What actually answered, as opposed to what was asked for. Kept separate from
		// `settings` all the way through, because collapsing them would lose exactly the
		// fact worth showing.
		modelReroute: wire.modelReroute
			? {
					fromModel: wire.modelReroute.fromModel || undefined,
					toModel: wire.modelReroute.toModel,
					reason: wire.modelReroute.reason || undefined,
					providerTurnId: wire.modelReroute.providerTurnId || undefined,
					at: wire.modelReroute.at,
				}
			: undefined,
		account: wire.account
			? {
					authMode: wire.account.authMode || undefined,
					planLabel: wire.account.planLabel || undefined,
					reauthRequiredAt: wire.account.reauthRequiredAt ?? undefined,
					reauthReason: wire.account.reauthReason || undefined,
				}
			: undefined,
		// The provider's lifecycle view, kept apart from `controller` on purpose: they
		// answer different questions and routinely disagree.
		threadState: wire.threadState
			? {
					status: (wire.threadState.status as ThreadStatus | undefined) || undefined,
					waitingOn: wire.threadState.waitingOn?.length ? wire.threadState.waitingOn : undefined,
					archivedAt: wire.threadState.archivedAt ?? undefined,
					closedAt: wire.threadState.closedAt ?? undefined,
				}
			: undefined,
		mcpServers: wire.mcpServers?.length
			? wire.mcpServers.map(
					(server): McpServer => ({
						name: server.name,
						status: server.status as McpServer["status"],
						error: server.error || undefined,
						failureReason: server.failureReason || undefined,
					}),
				)
			: undefined,
		capabilities: wire.capabilities?.length ? wire.capabilities : undefined,
		activeBranchId: wire.activeBranchId || undefined,
		branchedFromEarlierMessage: wire.branchedFromEarlierMessage ?? undefined,
		branchMaterialization: wire.branchMaterialization
			? {
					strategy: wire.branchMaterialization.strategy,
					replayTruncated: wire.branchMaterialization.replayTruncated,
				}
			: undefined,
		branchPoints: (wire.branchPoints ?? []).map((point) => ({
			turnId: point.turnId,
			position: point.position,
			total: point.total,
			previousBranchId: point.previousBranchId || undefined,
			nextBranchId: point.nextBranchId || undefined,
		})),
		turns: (wire.turns ?? []).map((turn) => ({
			id: turn.id,
			state: turn.state as TurnState,
			providerTurnId: turn.providerTurnId,
			retryOfTurnId: turn.retryOfTurnId,
			hasRetryAttempt: turn.hasRetryAttempt,
			errorMessage: turn.errorMessage,
			requestedAt: turn.requestedAt,
			startedAt: turn.startedAt ?? undefined,
			completedAt: turn.completedAt ?? undefined,
			// Left undefined when the daemon reported none, so the surface can tell
			// "this turn changed nothing" from "this agent does not report diffs".
			diff: turn.diff
				? {
						files: (turn.diff.files ?? []).map((file) => ({
							path: file.path,
							additions: file.additions,
							deletions: file.deletions,
							status: file.status as DiffStatus,
							oldPath: file.oldPath || undefined,
						})),
						truncated: turn.diff.truncated,
					}
				: undefined,
			// Current state of the turn rather than history: the provider re-sends the
			// whole plan on every revision and the daemon overwrites this, which is why
			// the surface reads it here instead of walking the timeline for plan rows.
			plan: turn.plan
				? {
						explanation: turn.plan.explanation || undefined,
						steps: (turn.plan.steps ?? []).map(
							(step): PlanStep => ({
								text: step.text,
								status: step.status as PlanStepStatus,
							}),
						),
					}
				: undefined,
			rolledBack: turn.rolledBack ?? undefined,
		})),
		items,
	};
}

function applyQueuedTurnOrder(
	snapshot: ConversationSnapshot,
	fifoTurnIds: readonly string[],
): ConversationSnapshot {
	const queuedTurns = snapshot.turns.filter((turn) => turn.state === "queued");
	if (queuedTurns.length !== fifoTurnIds.length) return snapshot;

	const queuedById = new Map(queuedTurns.map((turn) => [turn.id, turn]));
	const requestedAts = [...queuedTurns]
		.sort((left, right) => left.requestedAt.localeCompare(right.requestedAt))
		.map((turn) => turn.requestedAt);
	const reorderedQueued = fifoTurnIds.flatMap((turnId, index) => {
		const turn = queuedById.get(turnId);
		return turn
			? [{ ...turn, requestedAt: requestedAts[index] ?? turn.requestedAt }]
			: [];
	});
	if (reorderedQueued.length !== queuedTurns.length) return snapshot;

	const queuedIds = new Set(fifoTurnIds);
	const otherTurns = snapshot.turns.filter((turn) => !queuedIds.has(turn.id));
	return {
		...snapshot,
		turns: [...otherTurns, ...reorderedQueued].sort((left, right) =>
			left.requestedAt.localeCompare(right.requestedAt),
		),
	};
}

function applyQueuedTurnOrderToPages(
	data: InfiniteData<ConversationSnapshot>,
	fifoTurnIds: readonly string[],
): InfiniteData<ConversationSnapshot> {
	if (data.pages.length === 0) return data;
	return {
		...data,
		pages: data.pages.map((page, index) =>
			index === 0 ? applyQueuedTurnOrder(page, fifoTurnIds) : page,
		),
	};
}

/** Merge the newest live page with any older pages loaded on demand. */
function mergeConversationPages(pages: ConversationSnapshot[]): ConversationSnapshot | undefined {
	const live = pages[0];
	if (!live) return undefined;

	const items = new Map<string, ConversationItem>();
	const turns = new Map<string, ConversationSnapshot["turns"][number]>();
	// Walk oldest to newest so the live page wins when a row changed after an older
	// page was fetched (for example, a streamed message settling).
	for (const page of [...pages].reverse()) {
		for (const item of page.items) items.set(`${item.kind}:${item.id}`, item);
		for (const turn of page.turns) turns.set(turn.id, turn);
	}

	const oldest = pages[pages.length - 1] ?? live;
	return {
		...live,
		oldestSequence: oldest.oldestSequence,
		hasMoreBefore: oldest.hasMoreBefore,
		items: [...items.values()].sort((a, b) => a.sequence - b.sequence),
		turns: [...turns.values()].sort((a, b) => a.requestedAt.localeCompare(b.requestedAt)),
	};
}

function toMessage(wire: WireMessage): ConversationMessage {
	return {
		kind: "message",
		id: wire.id,
		turnId: wire.turnId,
		sequence: wire.sequence,
		revision: wire.revision,
		role: wire.role as MessageRole,
		origin: wire.origin as MessageOrigin,
		text: wire.text,
		content: (wire.content ?? []).map((item) => ({
			type: item.type,
			mimeType: item.mimeType || undefined,
			uri: item.uri || undefined,
			name: item.name || undefined,
		})),
		editAvailable: wire.editAvailable ?? undefined,
		streaming: wire.streaming,
		createdAt: wire.createdAt,
	};
}

function toActivity(wire: WireActivity): ConversationActivity {
	const detail = (wire.detail ?? {}) as Record<string, unknown>;
	return {
		kind: "activity",
		id: wire.id,
		turnId: wire.turnId,
		sequence: wire.sequence,
		revision: wire.revision,
		activityKind: wire.activityKind as ActivityKind,
		status: wire.status as ActivityStatus,
		summary: wire.summary,
		requestId: wire.requestId,
		providerItemId: wire.providerItemId,
		// The provider's own offered decisions, carried through the detail payload.
		// Rendering from this is what keeps the card from drawing a button the
		// provider will reject.
		decisions: readDecisions(detail),
		detail: detail as ConversationActivity["detail"],
		createdAt: wire.createdAt,
	};
}

function readDecisions(detail: Record<string, unknown>): DecisionOption[] | undefined {
	const raw = detail.decisions;
	if (!Array.isArray(raw)) return undefined;
	const options: DecisionOption[] = [];
	for (const entry of raw) {
		if (entry && typeof entry === "object" && "id" in entry) {
			const option = entry as {
				id?: unknown;
				label?: unknown;
				kind?: unknown;
			};
			if (typeof option.id === "string" && option.id !== "") {
				const kind = isDecisionKind(option.kind) ? option.kind : undefined;
				options.push({
					id: option.id,
					label: typeof option.label === "string" && option.label ? option.label : option.id,
					kind,
				});
			}
		}
	}
	return options.length > 0 ? options : undefined;
}

function isDecisionKind(value: unknown): value is NonNullable<DecisionOption["kind"]> {
	return (
		value === "allow_once" ||
		value === "allow_always" ||
		value === "reject_once" ||
		value === "reject_always"
	);
}
