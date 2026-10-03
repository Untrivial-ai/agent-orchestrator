import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useSyncExternalStore } from "react";
import { useCloudProjectsQuery, useCloudSessionsQuery, remoteWorkspaceQueryKey, useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSummary } from "../types/workspace";
import { LOCAL_HOST } from "./hosts";
import { useNavigateToSession } from "./navigate-to-session";
import { parseSessionLink, resolveSessionLink, type SessionLinkWorkspace } from "./session-links";

function useLocalLinkSource(_hostId: string): { ready: boolean; workspaces: SessionLinkWorkspace[] } {
	const query = useWorkspaceQuery();
	return { ready: query.isSuccess, workspaces: query.data ?? [] };
}

function useCloudLinkSource(_hostId: string): { ready: boolean; workspaces: SessionLinkWorkspace[] } {
	const projects = useCloudProjectsQuery();
	const sessions = useCloudSessionsQuery();
	return {
		ready: projects.isSuccess && sessions.isSuccess,
		workspaces: (projects.data ?? []).map((project) => ({
			id: project.id,
			sessions: (sessions.data ?? []).filter((session) => session.projectId === project.id),
		})),
	};
}

function useRemoteLinkSource(hostId: string): { ready: boolean; workspaces: SessionLinkWorkspace[] } {
	const queryClient = useQueryClient();
	const subscribe = useCallback((notify: () => void) => queryClient.getQueryCache().subscribe(notify), [queryClient]);
	const workspaces = useSyncExternalStore(
		subscribe,
		() => queryClient.getQueryData<WorkspaceSummary[]>(remoteWorkspaceQueryKey(hostId)),
	);
	return {
		ready: workspaces !== undefined,
		workspaces: workspaces ?? [],
	};
}

export function useSessionLinkNavigation(sourceHostId?: string, sourceKind?: "cloud"): (url: string) => boolean {
	const remoteHostId = sourceHostId && sourceHostId !== LOCAL_HOST ? sourceHostId : undefined;
	// Each caller's source is fixed for its mounted surface. Call only that
	// source's query so a remote terminal never probes the local daemon.
	const useSource = remoteHostId ? useRemoteLinkSource : sourceKind === "cloud" ? useCloudLinkSource : useLocalLinkSource;
	const source = useSource(remoteHostId ?? "");
	const navigateToSession = useNavigateToSession();
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	return useCallback((url: string) => {
		const target = parseSessionLink(url);
		if (!target) {
			showGlobalToast("This AO session link is malformed or unsupported.", undefined, {
				tone: "error",
				placement: "top-center",
				dismissible: true,
				dedupeKey: "session-link:error",
			});
			return false;
		}
		if (!source.ready) {
			showGlobalToast(`AO could not verify that session. Check the ${remoteHostId ? "host" : sourceKind === "cloud" ? "Cloud" : "daemon"} connection and try again.`, undefined, {
				tone: "error",
				placement: "top-center",
				dismissible: true,
				dedupeKey: "session-link:error",
			});
			return false;
		}
		const resolved = resolveSessionLink(url, source.workspaces);
		if (!resolved) {
			showGlobalToast("That session is missing or is not accessible in this AO workspace.", undefined, {
				tone: "error",
				placement: "top-center",
				dismissible: true,
				dedupeKey: "session-link:error",
			});
			return false;
		}
		if (resolved.isTerminated) {
			showGlobalToast(`Session ${resolved.sessionId} is terminated`, undefined, {
				placement: "top-center",
				dismissible: true,
				durationMs: 5_000,
				dedupeKey: `session-link:${remoteHostId ? `${remoteHostId}:` : ""}${resolved.projectId}:${resolved.sessionId}`,
			});
			return false;
		}
		if (remoteHostId) navigateToSession(resolved.projectId, resolved.sessionId, remoteHostId);
		else navigateToSession(resolved.projectId, resolved.sessionId);
		return true;
	}, [navigateToSession, remoteHostId, showGlobalToast, source, sourceKind]);
}
