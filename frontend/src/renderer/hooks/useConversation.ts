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

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
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

export function conversationQueryKey(sessionId: string) {
	return ["conversation", sessionId] as const;
}

export function conversationModelsQueryKey(sessionId: string) {
	return ["conversation-models", sessionId] as const;
}

export function conversationConfigOptionsQueryKey(sessionId: string) {
	return ["conversation-config-options", sessionId] as const;
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
					query: { beforeSequence: pageParam, limit: CONVERSATION_PAGE_SIZE },
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
	const invalidate = useCallback(async () => {
		if (sessionId) {
			// Keep the mutation pending until the active conversation has refreshed. A
			// queued message or landed steer should be visible before the composer clears,
			// otherwise the action looks dropped even though the daemon accepted it.
			await queryClient.invalidateQueries({ queryKey: conversationQueryKey(sessionId) });
		}
	}, [queryClient, sessionId]);

	const send = useMutation({
		mutationFn: async (input: ConversationSendInput) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/messages",
				{
					params: { path: { sessionId: sessionId as string } },
					// A stable id per attempt makes a retry idempotent: the daemon
					// answers `duplicate` instead of opening a second provider turn.
					body: { ...input, clientMessageId: crypto.randomUUID() },
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: invalidate,
	});

	const resolve = useMutation({
		mutationFn: async (input: { requestId: string; decisionId: string }) => {
			const { error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/approvals/{requestId}/resolve",
				{
					params: { path: { sessionId: sessionId as string, requestId: input.requestId } },
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
					params: { path: { sessionId: sessionId as string, requestId: input.requestId } },
					body: { action: input.action, content: input.content },
				},
			);
			if (error) throw error;
		},
		onSuccess: invalidate,
	});

	const interrupt = useMutation({
		mutationFn: async () => {
			const { error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/interrupt",
				{ params: { path: { sessionId: sessionId as string } } },
			);
			if (error) throw error;
		},
		onSuccess: invalidate,
		// A failed interrupt (e.g. CHAT_NO_ACTIVE_TURN) means the cached turn
		// state is wrong. Refetch so the UI discovers the real state instead of
		// keeping a Working bar the user cannot dismiss.
		onError: invalidate,
	});

	const resume = useMutation({
		mutationFn: async () => {
			const { data, error, response } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/resume-agent",
				{ params: { path: { sessionId: sessionId as string } } },
			);
			if (error) throw new Error(apiErrorMessage(error, `Failed to resume agent (${response.status})`));
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
				{ params: { path: { sessionId: sessionId as string } } },
			);
			if (error) throw error;
			return data;
		},
		onSuccess: invalidate,
	});

	const chooseSettings = useMutation({
		mutationFn: async (settings: TurnSettings) => {
			const { error } = await apiClient.PATCH(
				"/api/v1/sessions/{sessionId}/conversation/settings",
				{ params: { path: { sessionId: sessionId as string } }, body: settings },
			);
			if (error) throw error;
		},
		// The snapshot carries the selection, so refetching is what confirms it took
		// rather than the composer trusting its own optimistic state.
		onSuccess: invalidate,
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
		mutationFn: async (text: string) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/steer",
				{
					params: { path: { sessionId: sessionId as string } },
					body: { text, clientMessageId: crypto.randomUUID() },
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
				{ params: { path: { sessionId: sessionId as string, turnId } } },
			);
			if (error) throw error;
			return data;
		},
		onSuccess: invalidate,
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
				{ params: { path: { sessionId: sessionId as string } } },
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
				{ params: { path: { sessionId: sessionId as string, turnId } } },
			);
			if (error) throw error;
			return data;
		},
		onSuccess: invalidate,
	});

	const editMessage = useMutation({
		mutationFn: async ({ turnId, text }: { turnId: string; text: string }) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/edit",
				{
					params: { path: { sessionId: sessionId as string, turnId } },
					body: { text, clientMessageId: crypto.randomUUID() },
				},
			);
			if (error) throw error;
			return data;
		},
		onSettled: invalidate,
	});

	const activateBranch = useMutation({
		mutationFn: async (branchId: string) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/conversation/branches/{branchId}/activate",
				{ params: { path: { sessionId: sessionId as string, branchId } } },
			);
			if (error) throw error;
			return data;
		},
		onSettled: invalidate,
	});

	return {
		send: (input: string | ConversationSendInput) =>
			send.mutateAsync(typeof input === "string" ? { text: input } : input),
		resolve: (requestId: string, decisionId: string) => resolve.mutate({ requestId, decisionId }),
		resolveInput: (
			requestId: string,
			action: "accept" | "decline" | "cancel",
			content?: Record<string, unknown>,
		) => resolveInput.mutateAsync({ requestId, action, content }),
		interrupt: () => interrupt.mutate(),
		resumeAgent: () => resume.mutateAsync(),
		resumingAgent: resume.isPending,
		resumeError: resume.error ? apiErrorMessage(resume.error) : undefined,
		compact: () => compact.mutateAsync(),
		chooseSettings: (settings: TurnSettings) => chooseSettings.mutate(settings),
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
		editMessage: (turnId: string, text: string) => editMessage.mutateAsync({ turnId, text }),
		editMessagePending: editMessage.isPending,
		editMessageError: editMessage.error ? apiErrorMessage(editMessage.error) : undefined,
		activateBranch: (branchId: string) => activateBranch.mutateAsync(branchId),
		activateBranchPending: activateBranch.isPending,
		activateBranchError: activateBranch.error
			? apiErrorMessage(activateBranch.error)
			: undefined,
		steer: (text: string) => steer.mutateAsync(text),
		promoteQueuedTurn: (turnId: string) => promoteQueuedTurn.mutateAsync(turnId),
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
		mcpReloadUnsupported:
			apiErrorCode(reloadMcp.error) === "CHAT_MCP_RELOAD_UNSUPPORTED",
		mcpReloadError:
			reloadMcp.error && apiErrorCode(reloadMcp.error) !== "CHAT_MCP_RELOAD_UNSUPPORTED"
				? apiErrorMessage(reloadMcp.error)
				: undefined,
		busy: send.isPending || resolve.isPending || resolveInput.isPending || interrupt.isPending,
		error:
			send.error || resolve.error || interrupt.error || chooseSettings.error
				? apiErrorMessage(
						send.error ?? resolve.error ?? interrupt.error ?? chooseSettings.error,
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
				{ params: { path: { sessionId: sessionId as string } } },
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
	const query = useQuery({
		queryKey,
		enabled: Boolean(sessionId) && enabled,
		retry: false,
		// ACP can push a replacement catalog when one option changes. Until the
		// renderer consumes daemon change events, a light poll keeps those updates
		// visible without coupling them to conversation history polling.
		refetchInterval: CONFIG_OPTIONS_POLL_INTERVAL_MS,
		queryFn: async () => {
			const { data, error } = await apiClient.GET(
				"/api/v1/sessions/{sessionId}/conversation/config-options",
				{ params: { path: { sessionId: sessionId as string } } },
			);
			if (error) throw error;
			return (data?.options ?? []) as ChatConfigOption[];
		},
	});
	const mutation = useMutation({
		mutationFn: async ({
			optionId,
			value,
		}: {
			optionId: string;
			value: ChatConfigOptionValue;
		}) => {
			const { data, error } = await apiClient.PATCH(
				"/api/v1/sessions/{sessionId}/conversation/config-options/{configId}",
				{
					params: {
						path: { sessionId: sessionId as string, configId: optionId },
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
		setOption: (optionId: string, value: ChatConfigOptionValue) =>
			mutation.mutateAsync({ optionId, value }),
		pending: mutation.isPending,
		error: mutation.error ? apiErrorMessage(mutation.error) : undefined,
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
				{ params: { path: { sessionId: sessionId as string } } },
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
			const { data, error } = await apiClient.GET(
				"/api/v1/sessions/{sessionId}/workspace/files",
				{ params: { path: { sessionId: sessionId as string } } },
			);
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
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/attachments",
				{ params: { path: { sessionId } }, body: { attachments } },
			);
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

/** Merge the newest live page with any older pages loaded on demand. */
function mergeConversationPages(
	pages: ConversationSnapshot[],
): ConversationSnapshot | undefined {
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
			const option = entry as { id?: unknown; label?: unknown };
			if (typeof option.id === "string" && option.id !== "") {
				options.push({
					id: option.id,
					label: typeof option.label === "string" && option.label ? option.label : option.id,
				});
			}
		}
	}
	return options.length > 0 ? options : undefined;
}
