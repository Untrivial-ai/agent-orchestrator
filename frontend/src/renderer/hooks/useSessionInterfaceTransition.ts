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
import { useCloudCp } from "./useCloudCp";
import { cloudSessionsQueryKey, workspaceQueryKey } from "./useWorkspaceQuery";
import type { CloudCpInterfaceTransition } from "../lib/cloud-cp";

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

// Cloud and the local daemon deliberately expose the same state-machine
// vocabulary. Keep this conversion at the control-plane boundary rather than
// dropping the Cloud transition: without it, the first status refetch after a
// successful POST erased the optimistic active state, re-enabled the action,
// and let a second click race the still-running handoff.
function toSessionInterfaceTransition(
	transition: CloudCpInterfaceTransition | undefined,
): SessionInterfaceTransition | undefined {
	return transition as SessionInterfaceTransition | undefined;
}

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
	return {
		error:
			!pending && latest?.status === "error"
				? apiErrorMessage(latest.error)
				: undefined,
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
export function useSessionInterfaceTransition(
	sessionId: string | undefined,
	// undefined: session row is still resolving; null: resolved local session.
	// This prevents unresolved Cloud tabs from probing the local daemon.
	cloud?: { orgId: string } | null,
) {
	const queryClient = useQueryClient();
	const cloudCp = useCloudCp();
	// Keep the two-argument call site backwards-compatible for local sessions.
	// SessionView deliberately passes `undefined` while a tab is unresolved, so
	// distinguish an omitted context from that explicit unresolved value.
	const hasSessionContext = arguments.length >= 2;
	const isCloud = Boolean(cloud && cloudCp.ready);
	const isLocal = cloud === null || !hasSessionContext;
	const settledRef = useRef<string>("");
	const refreshAttemptRef = useRef(0);
	const [refreshingTransition, setRefreshingTransition] = useState<{
		attempt: number;
		key: string;
	}>();
	const query = useQuery({
		queryKey: sessionInterfaceTransitionQueryKey(sessionId ?? ""),
		enabled: Boolean(sessionId && (isCloud || (isLocal && hasTrustedApiBaseUrl()))),
		queryFn: async () => {
			if (isCloud && cloud) {
				const [sessionResponse, transitionStatus] = await Promise.all([
					cloudCp.client.getSession(cloud.orgId, sessionId as string),
					cloudCp.client.getInterfaceTransition(cloud.orgId, sessionId as string),
				]);
				return {
					supported: transitionStatus.supported,
					targetMode: transitionStatus.targetMode,
					reasonCode: transitionStatus.reasonCode,
					reason: transitionStatus.reason,
					transition: toSessionInterfaceTransition(transitionStatus.transition),
					currentMode: sessionResponse.session.interfaceMode,
				} as SessionInterfaceTransitionStatus & { currentMode?: SessionInterfaceMode };
			}
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
			if (isCloud && cloud) {
				const response = await cloudCp.client.startInterfaceTransition(
					cloud.orgId,
					targetSessionId,
					input,
				);
				return {
					transition: toSessionInterfaceTransition(response.transition),
				};
			}
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
			const refreshes = [
				queryClient.invalidateQueries({
					queryKey: sessionInterfaceTransitionQueryKey(variables.targetSessionId),
				}),
			];
			if (isCloud) {
				// Cloud's session projection owns interfaceMode. Refetch it as soon
				// as POST accepts the handoff rather than leaving the source TUI
				// mounted until the ordinary five-second Cloud polling interval.
				refreshes.push(
					queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey }),
					queryClient.invalidateQueries({ queryKey: ["cloud-session"] }),
				);
			}
			return Promise.all(refreshes).catch(() => undefined);
		},
	});

	const cancel = useMutation({
		mutationKey: cancelInterfaceTransitionMutationKey,
		mutationFn: async ({ targetSessionId }: InterfaceTransitionMutationTarget) => {
			if (isCloud) return;
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
			if (isCloud) return undefined;
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
			if (!response) return;
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
		const refreshes = [
			queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
			queryClient.invalidateQueries({ queryKey: conversationQueryKey(sessionId) }),
		];
		if (isCloud) {
			refreshes.push(
				queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey }),
				queryClient.invalidateQueries({ queryKey: ["cloud-session"] }),
			);
		}
		void Promise.all(refreshes).finally(() => {
			setRefreshingTransition((refreshing) =>
				refreshing?.key === transitionKey && refreshing.attempt === attempt
					? undefined
					: refreshing,
			);
		});
	}, [isCloud, queryClient, sessionId, transitionActive, transitionKey]);
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
		startError: startState.error,
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
