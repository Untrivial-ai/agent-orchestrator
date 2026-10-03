import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import {
	clearConversationProviderCatalogs,
	conversationQueryKey,
	invalidateConversationProviderCatalogs,
} from "./useConversation";
import { sessionUiKey } from "../lib/hosts";

/**
 * Keeps Chat provider catalogs aligned with the controller epoch during agent switches.
 *
 * Admission clears and pauses catalog queries so a 202 refetch cannot repopulate
 * the outgoing provider's options. Durable success or failure then refetches once
 * the target or recovered source controller owns the session.
 *
 * Switch selection and observation belong to the Chat surface. This hook only
 * applies provider-cache side effects for that canonical lifecycle.
 */
export function useAgentSwitchProviderCatalogs({
	sessionId,
	hostId,
	agentSwitching,
	settledSwitchId,
}: {
	sessionId: string;
	hostId?: string;
	agentSwitching: boolean;
	settledSwitchId?: string;
}): boolean {
	const queryClient = useQueryClient();
	const stateSessionId = sessionUiKey(sessionId, hostId);
	const refreshedSwitchIdsRef = useRef(new Set<string>());
	const clearedWhileSwitchingRef = useRef(false);
	const mountedSessionIdRef = useRef(stateSessionId);
	const [reconciledSettlement, setReconciledSettlement] = useState<{
		sessionId: string;
		switchId: string;
	}>();

	if (mountedSessionIdRef.current !== stateSessionId) {
		mountedSessionIdRef.current = stateSessionId;
		refreshedSwitchIdsRef.current = new Set();
		clearedWhileSwitchingRef.current = false;
	}

	useEffect(() => {
		if (!agentSwitching) {
			clearedWhileSwitchingRef.current = false;
			return;
		}
		if (clearedWhileSwitchingRef.current) return;
		clearedWhileSwitchingRef.current = true;
		clearConversationProviderCatalogs(queryClient, sessionId, hostId);
	}, [agentSwitching, hostId, queryClient, sessionId]);

	useEffect(() => {
		if (!settledSwitchId) return;
		if (refreshedSwitchIdsRef.current.has(settledSwitchId)) {
			setReconciledSettlement((current) =>
				current?.sessionId === stateSessionId && current.switchId === settledSwitchId
					? current
					: { sessionId: stateSessionId, switchId: settledSwitchId },
			);
			return;
		}
		// The renderer may first learn about a fast switch after it has already
		// completed. Clear before invalidating so outgoing controls cannot remain
		// visible while active observers refetch from the live controller.
		clearConversationProviderCatalogs(queryClient, sessionId, hostId);
		refreshedSwitchIdsRef.current.add(settledSwitchId);
		invalidateConversationProviderCatalogs(queryClient, sessionId, hostId);
		void queryClient.invalidateQueries({ queryKey: conversationQueryKey(sessionId, hostId) });
		setReconciledSettlement({ sessionId: stateSessionId, switchId: settledSwitchId });
	}, [hostId, queryClient, sessionId, settledSwitchId, stateSessionId]);

	const settlementReconciled =
		!settledSwitchId ||
		(reconciledSettlement?.sessionId === stateSessionId &&
			reconciledSettlement.switchId === settledSwitchId);
	return !agentSwitching && settlementReconciled;
}
