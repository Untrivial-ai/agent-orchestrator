import { useNavigate } from "@tanstack/react-router";
import { useCallback } from "react";
import { LOCAL_HOST, type HostId } from "./hosts";
import { STANDALONE_WORKSPACE_ID } from "../types/workspace";

export type SessionNavigateTarget =
	| { to: "/sessions/$sessionId"; params: { sessionId: string } }
	| { to: "/projects/$projectId/sessions/$sessionId"; params: { projectId: string; sessionId: string } }
	| { to: "/host/$hostId/project/$projectId/session/$sessionId"; params: { hostId: string; projectId: string; sessionId: string } }
	| { to: "/host/$hostId/session/$sessionId"; params: { hostId: string; sessionId: string } };

export function projectNavigateTarget(projectId: string, host: HostId = LOCAL_HOST) {
	return host === LOCAL_HOST
		? { to: "/projects/$projectId" as const, params: { projectId } }
		: { to: "/host/$hostId/project/$projectId" as const, params: { hostId: host, projectId } };
}

export function sessionNavigateTarget(projectId: string | undefined, sessionId: string, host: HostId = LOCAL_HOST): SessionNavigateTarget {
	if (host !== LOCAL_HOST) {
		if (!projectId || projectId === STANDALONE_WORKSPACE_ID) {
			return { to: "/host/$hostId/session/$sessionId", params: { hostId: host, sessionId } };
		}
		return { to: "/host/$hostId/project/$projectId/session/$sessionId", params: { hostId: host, projectId, sessionId } };
	}
	if (!projectId || projectId === STANDALONE_WORKSPACE_ID) {
		return { to: "/sessions/$sessionId", params: { sessionId } };
	}
	return {
		to: "/projects/$projectId/sessions/$sessionId",
		params: { projectId, sessionId },
	};
}

export function useNavigateToSession(): (projectId: string | undefined, sessionId: string, host?: HostId) => void {
	const navigate = useNavigate();
	return useCallback(
		(projectId: string | undefined, sessionId: string, host: HostId = LOCAL_HOST) => {
			if (!sessionId) return;
			void navigate(sessionNavigateTarget(projectId, sessionId, host));
		},
		[navigate],
	);
}

export function useNavigateToTerminals(): () => void {
	const navigate = useNavigate();
	return useCallback(() => {
		void navigate({ to: "/terminals" });
	}, [navigate]);
}
