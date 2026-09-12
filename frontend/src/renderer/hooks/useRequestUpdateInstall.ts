import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useRef } from "react";
import { aoBridge } from "../lib/bridge";
import { sessionsAtRiskFromInstall } from "../lib/update-install-risk";
import { useUiStore } from "../stores/ui-store";
import { workspaceQueryOptions } from "./useWorkspaceQuery";
import type { WorkspaceSummary } from "../types/workspace";

export function useRequestUpdateInstall(): () => void {
	const openPrompt = useUiStore((state) => state.openUpdateInstallPrompt);
	const queryClient = useQueryClient();
	const pending = useRef(false);

	return useCallback(() => {
		if (pending.current) return;
		pending.current = true;
		void (async () => {
			let timer: ReturnType<typeof setTimeout> | undefined;
			try {
				// Force a current local snapshot, even when the cache appears fresh.
				const projects = await Promise.race([
					queryClient.fetchQuery({ ...workspaceQueryOptions, staleTime: 0, retry: false }),
					new Promise<never>((_resolve, reject) => { timer = setTimeout(() => reject(new Error("Worker refresh timed out")), 10_000); }),
				]);
				const snapshot = queryClient.getQueryState<WorkspaceSummary[]>(workspaceQueryOptions.queryKey);
				if (snapshot?.isInvalidated || sessionsAtRiskFromInstall(projects.flatMap((project) => project.sessions)).length > 0) {
					openPrompt();
					return;
				}
				await aoBridge.updates.install();
			} catch {
				// Unknown worker state still requires explicit confirmation.
				openPrompt();
			} finally {
				clearTimeout(timer);
				pending.current = false;
			}
		})();
	}, [openPrompt, queryClient]);
}
