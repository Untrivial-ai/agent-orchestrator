import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
	const [refreshingTransitionID, setRefreshingTransitionID] = useState("");
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
		mutationFn: async (input: {
			targetMode: SessionInterfaceMode;
			policy: SessionInterfaceTransitionPolicy;
		}) => {
			const { data, error } = await apiClient.POST(
				"/api/v1/sessions/{sessionId}/interface-transition",
				{
					params: { path: { sessionId: sessionId as string } },
					body: input,
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: () => {
			if (sessionId) {
				void queryClient.invalidateQueries({
					queryKey: sessionInterfaceTransitionQueryKey(sessionId),
				});
			}
		},
	});

	const cancel = useMutation({
		mutationFn: async () => {
			const { error } = await apiClient.DELETE(
				"/api/v1/sessions/{sessionId}/interface-transition",
				{ params: { path: { sessionId: sessionId as string } } },
			);
			if (error) throw error;
		},
		onSuccess: () => {
			if (sessionId) {
				void queryClient.invalidateQueries({
					queryKey: sessionInterfaceTransitionQueryKey(sessionId),
				});
			}
		},
	});

	const acknowledgeNotice = useMutation({
		mutationFn: async (transitionId: string) => {
			const { data, error } = await apiClient.PUT(
				"/api/v1/sessions/{sessionId}/interface-transition/{transitionId}/notice-acknowledgement",
				{
					params: {
						path: { sessionId: sessionId as string, transitionId },
					},
				},
			);
			if (error) throw error;
			return data;
		},
		onSuccess: (response) => {
			if (!sessionId) return;
			queryClient.setQueryData<SessionInterfaceTransitionStatus>(
				sessionInterfaceTransitionQueryKey(sessionId),
				(current) =>
					current?.transition?.id === response.transition.id
						? { ...current, transition: response.transition }
						: current,
			);
		},
		onSettled: () => {
			if (sessionId) {
				void queryClient.invalidateQueries({
					queryKey: sessionInterfaceTransitionQueryKey(sessionId),
				});
			}
		},
	});

	const transition = query.data?.transition;
	const transitionActive = interfaceTransitionIsActive(transition);
	const transitionID = transition?.id;
	// A completed handoff is not visually settled until the queries invalidated by
	// it have returned. In particular, switching TUI -> Chat necessarily has a
	// small interval after mode=chat commits and before the Chat controller is in
	// the registry. A snapshot fetched in that interval truthfully says "stopped",
	// but rendering it as a controller failure is a lie: the transition worker is
	// still starting the target. Keep that state distinct through the final refetch.
	const settling = Boolean(
		transitionID &&
			!transitionActive &&
			(settledRef.current !== transitionID || refreshingTransitionID === transitionID),
	);
	useEffect(() => {
		if (!sessionId || !transitionID || transitionActive) return;
		if (settledRef.current === transitionID) return;
		settledRef.current = transitionID;
		setRefreshingTransitionID(transitionID);
		let current = true;
		void Promise.all([
			queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
			queryClient.invalidateQueries({ queryKey: conversationQueryKey(sessionId) }),
		]).finally(() => {
			if (!current) return;
			setRefreshingTransitionID((refreshing) =>
				refreshing === transitionID ? "" : refreshing,
			);
		});
		return () => {
			current = false;
		};
	}, [queryClient, sessionId, transitionActive, transitionID]);

	return {
		status: query.data,
		transition,
		settling,
		isLoading: query.isLoading,
		statusError: query.error ? apiErrorMessage(query.error) : undefined,
		start: start.mutateAsync,
		starting: start.isPending,
		startError: start.error ? apiErrorMessage(start.error) : undefined,
		resetStartError: start.reset,
		cancel: cancel.mutateAsync,
		cancelling: cancel.isPending,
		cancelError: cancel.error ? apiErrorMessage(cancel.error) : undefined,
		acknowledgeNotice: acknowledgeNotice.mutateAsync,
		acknowledgingNotice: acknowledgeNotice.isPending,
		acknowledgeNoticeError: acknowledgeNotice.error
			? apiErrorMessage(acknowledgeNotice.error)
			: undefined,
	};
}
