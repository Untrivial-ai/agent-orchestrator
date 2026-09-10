import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import {
	clearConversationProviderCatalogs,
	conversationQueryKey,
	invalidateConversationProviderCatalogs,
} from "./useConversation";

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
	agentSwitching,
	settledSwitchId,
}: {
	sessionId: string;
	agentSwitching: boolean;
	settledSwitchId?: string;
}): boolean {
	const queryClient = useQueryClient();
	const refreshedSwitchIdsRef = useRef(new Set<string>());
	const clearedWhileSwitchingRef = useRef(false);
	const mountedSessionIdRef = useRef(sessionId);
	const [reconciledSettlement, setReconciledSettlement] = useState<{
		sessionId: string;
		switchId: string;
	}>();

	if (mountedSessionIdRef.current !== sessionId) {
		mountedSessionIdRef.current = sessionId;
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
		clearConversationProviderCatalogs(queryClient, sessionId);
	}, [agentSwitching, queryClient, sessionId]);

	useEffect(() => {
		if (!settledSwitchId) return;
		if (refreshedSwitchIdsRef.current.has(settledSwitchId)) {
			setReconciledSettlement((current) =>
				current?.sessionId === sessionId && current.switchId === settledSwitchId
					? current
					: { sessionId, switchId: settledSwitchId },
			);
			return;
		}
		// The renderer may first learn about a fast switch after it has already
		// completed. Clear before invalidating so outgoing controls cannot remain
		// visible while active observers refetch from the live controller.
		clearConversationProviderCatalogs(queryClient, sessionId);
		refreshedSwitchIdsRef.current.add(settledSwitchId);
		invalidateConversationProviderCatalogs(queryClient, sessionId);
		void queryClient.invalidateQueries({ queryKey: conversationQueryKey(sessionId) });
		setReconciledSettlement({ sessionId, switchId: settledSwitchId });
	}, [queryClient, sessionId, settledSwitchId]);

	const settlementReconciled =
		!settledSwitchId ||
		(reconciledSettlement?.sessionId === sessionId &&
			reconciledSettlement.switchId === settledSwitchId);
	return !agentSwitching && settlementReconciled;
}
