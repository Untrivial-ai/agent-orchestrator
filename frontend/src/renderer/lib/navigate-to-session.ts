import { useNavigate } from "@tanstack/react-router";
import { useCallback } from "react";
import { STANDALONE_WORKSPACE_ID } from "../types/workspace";

export type SessionNavigateTarget =
	| { to: "/sessions/$sessionId"; params: { sessionId: string } }
	| { to: "/projects/$projectId/sessions/$sessionId"; params: { projectId: string; sessionId: string } };

export function sessionNavigateTarget(projectId: string | undefined, sessionId: string): SessionNavigateTarget {
	if (!projectId || projectId === STANDALONE_WORKSPACE_ID) {
		return { to: "/sessions/$sessionId", params: { sessionId } };
	}
	return {
		to: "/projects/$projectId/sessions/$sessionId",
		params: { projectId, sessionId },
	};
}

export function useNavigateToSession(): (projectId: string | undefined, sessionId: string) => void {
	const navigate = useNavigate();
	return useCallback(
		(projectId: string | undefined, sessionId: string) => {
			if (!sessionId) return;
			void navigate(sessionNavigateTarget(projectId, sessionId));
		},
		[navigate],
	);
}
