import {
	type QueryClient,
	useMutation,
	useMutationState,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { conversationQueryKey } from "./useConversation";
import { workspaceQueryKey } from "./useWorkspaceQuery";

export type SessionInterfaceTransition = components["schemas"]["SessionInterfaceTransition"];
export type SessionInterfaceTransitionStatus =
	components["schemas"]["SessionInterfaceTransitionStatusResponse"];
export type SessionInterfaceTransitionPolicy = "drain" | "interrupt";
export type SessionInterfaceMode = "chat" | "tui";

type StartInterfaceTransitionInput = {
	targetMode: SessionInterfaceMode;
	policy: SessionInterfaceTransitionPolicy;
};

type StartInterfaceTransitionMutationInput = StartInterfaceTransitionInput & {
	targetSessionId: string;
};

type InterfaceTransitionMutationTarget = {
	targetSessionId: string;
};

type AcknowledgeInterfaceTransitionNoticeMutationInput =
	InterfaceTransitionMutationTarget & {
		transitionId: string;
	};

const startInterfaceTransitionMutationKey = ["start-session-interface-transition"] as const;
const cancelInterfaceTransitionMutationKey = ["cancel-session-interface-transition"] as const;
const acknowledgeInterfaceTransitionNoticeMutationKey = [
	"acknowledge-session-interface-transition-notice",
] as const;

type InterfaceTransitionMutationState<TInput> = {
	error: unknown;
	input?: TInput;
	status: "error" | "idle" | "pending" | "success";
	submittedAt: number;
};

function useInterfaceTransitionMutations<TInput>(mutationKey: readonly unknown[]) {
	return useMutationState<InterfaceTransitionMutationState<TInput>>({
		filters: { mutationKey },
		select: (mutation) => ({
			error: mutation.state.error,
			input: mutation.state.variables as TInput | undefined,
			status: mutation.state.status,
			submittedAt: mutation.state.submittedAt,
		}),
	});
}

function summarizeInterfaceTransitionMutations<
	TInput extends InterfaceTransitionMutationTarget,
>(mutations: InterfaceTransitionMutationState<TInput>[], sessionId: string | undefined) {
	let latest: InterfaceTransitionMutationState<TInput> | undefined;
	let pending: InterfaceTransitionMutationState<TInput> | undefined;
	for (const mutation of mutations) {
		if (mutation.input?.targetSessionId !== sessionId) continue;
		if (!latest || mutation.submittedAt >= latest.submittedAt) latest = mutation;
		if (
			mutation.status === "pending" &&
			(!pending || mutation.submittedAt >= pending.submittedAt)
		) {
			pending = mutation;
		}
	}
	const errored = !pending && latest?.status === "error" ? latest : undefined;
	return {
		error: errored ? apiErrorMessage(errored.error) : undefined,
		errorAt: errored?.submittedAt,
		isPending: Boolean(pending),
	};
}

function clearInterfaceTransitionMutationState(
	queryClient: QueryClient,
	mutationKey: readonly unknown[],
	sessionId: string | undefined,
) {
	if (!sessionId) return;
	const mutationCache = queryClient.getMutationCache();
	for (const mutation of mutationCache.findAll({ mutationKey })) {
		const input = mutation.state.variables as InterfaceTransitionMutationTarget | undefined;
		if (input?.targetSessionId === sessionId && mutation.state.status !== "pending") {
			mutationCache.remove(mutation);
		}
	}
}

const activePhases = new Set<SessionInterfaceTransition["phase"]>([
	"requested",
	"preflighting",
	"draining",
	"source_stopping",
	"source_stopped",
	"target_starting",
	"activating",
]);

const cancellablePhases = new Set<SessionInterfaceTransition["phase"]>([
	"requested",
	"preflighting",
	"draining",
]);

const nativeSessionReadinessPoll = 1_000;

export function interfaceTransitionIsActive(transition?: SessionInterfaceTransition): boolean {
	return Boolean(transition && activePhases.has(transition.phase));
}

export function interfaceTransitionIsCancellable(transition?: SessionInterfaceTransition): boolean {
	return Boolean(transition && cancellablePhases.has(transition.phase));
}

export function interfaceTransitionHasUnacknowledgedNotice(
	transition?: SessionInterfaceTransition,
): boolean {
	return Boolean(
		transition &&
			!transition.noticeAcknowledgedAt &&
			(transition.phase === "failed" || transition.phase === "recovery_required"),
	);
}

export function sessionInterfaceTransitionQueryKey(sessionId: string) {
	return ["session-interface-transition", sessionId] as const;
}

/**
 * One bounded durable row drives every client. Polling is intentionally only
 * eager while a handoff is active; idle sessions do not create background
 * traffic and the existing session CDC stream still refreshes the committed
 * mode in the workspace model.
 */
export function useSessionInterfaceTransition(sessionId: string | undefined) {
	const queryClient = useQueryClient();
	const settledRef = useRef<string>("");
	const refreshAttemptRef = useRef(0);
	const [refreshingTransition, setRefreshingTransition] = useState<{
		attempt: number;
		key: string;
	}>();
	const query = useQuery({
		queryKey: sessionInterfaceTransitionQueryKey(sessionId ?? ""),
		enabled: Boolean(sessionId && hasTrustedApiBaseUrl()),
		queryFn: async () => {
			const { data, error } = await apiClient.GET(
				"/api/v1/sessions/{sessionId}/interface-transition",
				{ params: { path: { sessionId: sessionId as string } } },
			);
			if (error) throw error;
			return data as SessionInterfaceTransitionStatus;
		},
		refetchInterval: (state) => {
			const status = state.state.data;
			if (interfaceTransitionIsActive(status?.transition)) return 250;
			// A missing or not-yet-current native identity is transient while the
			// terminal's session-start hook is arriving. Recheck only those readiness
			// states so supported switches enable without polling permanently
			// unsupported harnesses or ordinary idle sessions.
			return status?.reasonCode === "NATIVE_SESSION_MISSING" ||
				status?.reasonCode === "NATIVE_SESSION_UNVERIFIED"
				? nativeSessionReadinessPoll
				: false;
		},
		retry: 1,
	});

	const start = useMutation({
		mutationKey: startInterfaceTransitionMutationKey,
		mutationFn: async ({
			targetSessionId,
			...input
		}: StartInterfaceTransitionMutationInput) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/interface-transition",
				{
					params: { path: { sessionId: targetSessionId } },
					body: input,
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: (response, variables) => {
			// The POST response is the durable acceptance boundary. Refreshing may
			// be a no-op for an inactive query or fail transiently, but neither case
			// may turn an accepted handoff back into an available Chat composer.
			if (response?.transition) {
				queryClient.setQueryData<SessionInterfaceTransitionStatus>(
					sessionInterfaceTransitionQueryKey(variables.targetSessionId),
					{
						supported: true,
						targetMode: response.transition.targetMode,
						transition: response.transition,
					},
				);
			}
			return queryClient
				.invalidateQueries({
					queryKey: sessionInterfaceTransitionQueryKey(variables.targetSessionId),
				})
				.catch(() => undefined);
		},
	});

	const cancel = useMutation({
		mutationKey: cancelInterfaceTransitionMutationKey,
		mutationFn: async ({ targetSessionId }: InterfaceTransitionMutationTarget) => {
			const { error } = await apiClient.DELETE(
				"/api/v1/sessions/{sessionId}/interface-transition",
				{ params: { path: { sessionId: targetSessionId } } },
			);
			if (error) throw error;
		},
		onSuccess: (_data, variables) => {
			void queryClient.invalidateQueries({
				queryKey: sessionInterfaceTransitionQueryKey(variables.targetSessionId),
			});
		},
	});

	const acknowledgeNotice = useMutation({
		mutationKey: acknowledgeInterfaceTransitionNoticeMutationKey,
		mutationFn: async ({
			targetSessionId,
			transitionId,
		}: AcknowledgeInterfaceTransitionNoticeMutationInput) => {
			const { data, error } = await apiClient.PUT(
				"/api/v1/sessions/{sessionId}/interface-transition/{transitionId}/notice-acknowledgement",
				{
					params: {
						path: { sessionId: targetSessionId, transitionId },
					},
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: (response, variables) => {
			queryClient.setQueryData<SessionInterfaceTransitionStatus>(
				sessionInterfaceTransitionQueryKey(variables.targetSessionId),
				(current) =>
					current?.transition?.id === response.transition.id
						? { ...current, transition: response.transition }
						: current,
			);
		},
		onSettled: (_data, _error, variables) => {
			void queryClient.invalidateQueries({
				queryKey: sessionInterfaceTransitionQueryKey(variables.targetSessionId),
			});
		},
	});
	const startState = summarizeInterfaceTransitionMutations(
		useInterfaceTransitionMutations<StartInterfaceTransitionMutationInput>(
			startInterfaceTransitionMutationKey,
		),
		sessionId,
	);
	const cancelState = summarizeInterfaceTransitionMutations(
		useInterfaceTransitionMutations<InterfaceTransitionMutationTarget>(
			cancelInterfaceTransitionMutationKey,
		),
		sessionId,
	);
	const acknowledgeNoticeState = summarizeInterfaceTransitionMutations(
		useInterfaceTransitionMutations<AcknowledgeInterfaceTransitionNoticeMutationInput>(
			acknowledgeInterfaceTransitionNoticeMutationKey,
		),
		sessionId,
	);

	const transition = query.data?.transition;
	const transitionActive = interfaceTransitionIsActive(transition);
	const transitionID = transition?.id;
	const transitionKey =
		sessionId && transitionID ? JSON.stringify([sessionId, transitionID]) : "";
	// A completed handoff is not visually settled until the queries invalidated by
	// it have returned. In particular, switching TUI -> Chat necessarily has a
	// small interval after mode=chat commits and before the Chat controller is in
	// the registry. A snapshot fetched in that interval truthfully says "stopped",
	// but rendering it as a controller failure is a lie: the transition worker is
	// still starting the target. Keep that state distinct through the final refetch.
	const settling = Boolean(
		transitionKey &&
			!transitionActive &&
			(settledRef.current !== transitionKey ||
				refreshingTransition?.key === transitionKey),
	);
	useEffect(() => {
		if (!sessionId || !transitionKey || transitionActive) return;
		if (settledRef.current === transitionKey) return;
		settledRef.current = transitionKey;
		const attempt = ++refreshAttemptRef.current;
		setRefreshingTransition({ attempt, key: transitionKey });
		void Promise.all([
			queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
			queryClient.invalidateQueries({ queryKey: conversationQueryKey(sessionId) }),
		]).finally(() => {
			setRefreshingTransition((refreshing) =>
				refreshing?.key === transitionKey && refreshing.attempt === attempt
					? undefined
					: refreshing,
			);
		});
	}, [queryClient, sessionId, transitionActive, transitionKey]);
	// A local start refusal records a durable-free error. If any client then opens
	// a real transition, that newer durable row supersedes the stale refusal: the
	// switch is running or done, so the "could not switch" notice must not linger.
	const transitionCreatedAt = transition ? Date.parse(transition.createdAt) : NaN;
	const startErrorSuperseded = Boolean(
		startState.errorAt !== undefined &&
			Number.isFinite(transitionCreatedAt) &&
			transitionCreatedAt > startState.errorAt,
	);
	useEffect(() => {
		if (!startErrorSuperseded) return;
		clearInterfaceTransitionMutationState(
			queryClient,
			startInterfaceTransitionMutationKey,
			sessionId,
		);
	}, [queryClient, sessionId, startErrorSuperseded]);
	return {
		status: query.data,
		transition,
		settling,
		isLoading: query.isLoading,
		statusError: query.error ? apiErrorMessage(query.error) : undefined,
		start: (input: StartInterfaceTransitionInput) => {
			if (!sessionId) return Promise.reject(new Error("No session is selected."));
			return start.mutateAsync({ ...input, targetSessionId: sessionId });
		},
		starting: startState.isPending,
		startError: startErrorSuperseded ? undefined : startState.error,
		resetStartError: () => {
			clearInterfaceTransitionMutationState(
				queryClient,
				startInterfaceTransitionMutationKey,
				sessionId,
			);
		},
		cancel: () => {
			if (!sessionId) return Promise.reject(new Error("No session is selected."));
			return cancel.mutateAsync({ targetSessionId: sessionId });
		},
		cancelling: cancelState.isPending,
		cancelError: cancelState.error,
		acknowledgeNotice: (transitionId: string) => {
			if (!sessionId) return Promise.reject(new Error("No session is selected."));
			return acknowledgeNotice.mutateAsync({ targetSessionId: sessionId, transitionId });
		},
		acknowledgingNotice: acknowledgeNoticeState.isPending,
		acknowledgeNoticeError: acknowledgeNoticeState.error,
	};
}
