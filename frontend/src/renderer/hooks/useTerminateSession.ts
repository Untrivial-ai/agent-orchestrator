import { type QueryClient, useMutation, useMutationState, useQueryClient } from "@tanstack/react-query";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { cloudSessionsQueryKey, workspaceQueryKey } from "./useWorkspaceQuery";
import {
	applyTerminatedSession,
	clearOptimisticSessionKill,
	trackOptimisticSessionKill,
} from "./optimistic-session-kills";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { captureRendererEvent } from "../lib/telemetry";
import { createRendererCloudCpClient } from "./useCloudCp";
import { settingsQueryKey, type Settings } from "./useSettings";
import { useUiStore } from "../stores/ui-store";
import { appI18n } from "../i18n";

type TerminateSessionOptions = {
	/** Fires synchronously as the kill starts — before the cache drops the row. */
	onOptimistic?: (session: WorkspaceSession) => void;
};

export const terminateSessionMutationKey = ["terminate-session"] as const;

async function terminateSession(queryClient: QueryClient, session: WorkspaceSession): Promise<void> {
	if (session.cloud) {
		const settings = queryClient.getQueryData<Settings>(settingsQueryKey);
		const baseUrl = settings?.cloudControlPlaneUrl ?? "";
		if (baseUrl === "") throw new Error("The cloud control plane is not configured.");
		await createRendererCloudCpClient(baseUrl).deleteSession(session.cloud.orgId, session.id);
		return;
	}

	const { error, response } = await apiClient.POST("/api/v1/sessions/{sessionId}/kill", {
		params: { path: { sessionId: session.id } },
	});
	if (error) {
		const fallback = response ? `Failed to terminate session (${response.status})` : "Failed to terminate session";
		throw new Error(apiErrorMessage(error, fallback));
	}
}

type TerminateSessionMutationState = {
	error: unknown;
	session?: WorkspaceSession;
	status: "error" | "idle" | "pending" | "success";
	submittedAt: number;
};

function useTerminateSessionMutations() {
	return useMutationState<TerminateSessionMutationState>({
		filters: { mutationKey: terminateSessionMutationKey },
		select: (mutation) => ({
			error: mutation.state.error,
			session: mutation.state.variables as WorkspaceSession | undefined,
			status: mutation.state.status,
			submittedAt: mutation.state.submittedAt,
		}),
	});
}

function summarizeBySession(mutations: TerminateSessionMutationState[]) {
	const summaries = new Map<
		string,
		{ isPending: boolean; latest: TerminateSessionMutationState; session: WorkspaceSession }
	>();
	for (const mutation of mutations) {
		if (!mutation.session) continue;
		const current = summaries.get(mutation.session.id);
		if (!current) {
			summaries.set(mutation.session.id, {
				isPending: mutation.status === "pending",
				latest: mutation,
				session: mutation.session,
			});
			continue;
		}
		current.isPending ||= mutation.status === "pending";
		if (mutation.submittedAt >= current.latest.submittedAt) current.latest = mutation;
	}
	return [...summaries.values()];
}

export function useTerminateSession(options: TerminateSessionOptions = {}) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationKey: terminateSessionMutationKey,
		mutationFn: async (session: WorkspaceSession) => {
			void captureRendererEvent("ao.renderer.session_kill_requested", { project_id: session.workspaceId });
			const toastTitle = appI18n.t("shell.killingNamed", { title: session.branch || session.workspaceName || "Session" });
			useUiStore.getState().showGlobalToast(toastTitle, undefined, "info");

			await terminateSession(queryClient, session);
		},
		onMutate: async (session) => {
			// Navigate first while the row is still on screen / in closed-over lists.
			options.onOptimistic?.(session);
			// Drop in-flight workspace fetches so they cannot overwrite the optimistic
			// remove with a pre-kill snapshot (CDC + refetchInterval race).
			await queryClient.cancelQueries({ queryKey: workspaceQueryKey });
			const previous = queryClient.getQueryData<WorkspaceSummary[]>(workspaceQueryKey);
			trackOptimisticSessionKill(session.id);
			queryClient.setQueryData<WorkspaceSummary[]>(workspaceQueryKey, (workspaces) =>
				applyTerminatedSession(workspaces, session.id),
			);
			return { previous };
		},
		onSuccess: async (_data, session) => {
			void captureRendererEvent("ao.renderer.session_kill_succeeded", { project_id: session.workspaceId });
			// Reinforce before refresh; keep the optimistic id until this refetch finishes.
			queryClient.setQueryData<WorkspaceSummary[]>(workspaceQueryKey, (workspaces) =>
				applyTerminatedSession(workspaces, session.id),
			);
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			if (session.cloud) await queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey });
		},
		onError: (_error, session, context) => {
			void captureRendererEvent("ao.renderer.session_kill_failed", { project_id: session.workspaceId });
			clearOptimisticSessionKill(session.id);
			if (context?.previous) {
				queryClient.setQueryData(workspaceQueryKey, context.previous);
			}
		},
		onSettled: (_data, error, session) => {
			if (!error) clearOptimisticSessionKill(session.id);
		},
	});
}

export function useTerminateSessionState(sessionId: string) {
	const summary = summarizeBySession(useTerminateSessionMutations()).find(({ session }) => session.id === sessionId);

	return {
		error:
			!summary?.isPending && summary?.latest.status === "error" && summary.latest.error instanceof Error
				? summary.latest.error.message
				: null,
		isPending: summary?.isPending ?? false,
	};
}

export function useProjectTerminateSessionStates(workspaceId: string | undefined) {
	return summarizeBySession(useTerminateSessionMutations())
		.filter(({ isPending, latest, session }) => {
			return session.workspaceId === workspaceId && (isPending || latest.status === "error");
		})
		.sort((a, b) => b.latest.submittedAt - a.latest.submittedAt)
		.map(({ isPending, latest, session }) => ({
			error: !isPending && latest.error instanceof Error ? latest.error.message : null,
			isPending,
			session,
		}));
}

export function clearTerminateSessionState(queryClient: QueryClient, sessionId: string) {
	const mutationCache = queryClient.getMutationCache();
	for (const mutation of mutationCache.findAll({ mutationKey: terminateSessionMutationKey })) {
		const target = mutation.state.variables as WorkspaceSession | undefined;
		if (target?.id === sessionId && mutation.state.status !== "pending") {
			mutationCache.remove(mutation);
		}
	}
}
