import { create } from "zustand";
import { apiClient, apiErrorCode, apiErrorMessage } from "../lib/api-client";
import { queryClient } from "../lib/query-client";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import {
	importableSessionsQueryKey,
	type ImportableSession,
} from "../hooks/useImportableSessions";

export const IMPORT_REQUEST_TIMEOUT_MS = 120_000;
export type ImportRunProgress = {
	done: number;
	total: number;
	imported: number;
	failed: number;
};
export type ImportRun = {
	projectId: string;
	running: boolean;
	stopped: boolean;
	progress: ImportRunProgress;
	errors: { title: string; message: string }[];
	currentId?: string;
	elapsedMs?: number;
};
type ImportRunState = {
	runs: Record<string, ImportRun>;
	start: (projectId: string, sessions: ImportableSession[]) => Promise<void>;
	stop: (projectId: string) => void;
	dismiss: (projectId: string) => void;
};
const controllers = new Map<string, AbortController>();

// Runs survive dialog unmounts. Each project has one sequential queue shared by
// single and bulk imports, so navigating cannot create competing mutations.
export const useImportRunStore = create<ImportRunState>((set, get) => ({
	runs: {},
	start: async (projectId, sessions) => {
		if (!projectId || get().runs[projectId]?.running) return;
		const seen = new Set<string>();
		const pending = sessions.filter((s) => {
			const key = `${s.provider}:${s.nativeSessionId}`;
			if (s.alreadyImported || seen.has(key)) return false;
			seen.add(key);
			return true;
		});
		if (!pending.length) return;
		const startedAt = performance.now();
		const controller = new AbortController();
		controllers.set(projectId, controller);
		let run: ImportRun = {
			projectId,
			running: true,
			stopped: false,
			progress: { done: 0, total: pending.length, imported: 0, failed: 0 },
			errors: [],
		};
		const publish = () =>
			set((s) => ({
				runs: {
					...s.runs,
					[projectId]: {
						...run,
						progress: { ...run.progress },
						errors: [...run.errors],
					},
				},
			}));
		publish();
		let lastRefresh = Date.now();
		let batchResults: Map<string, { error?: string }> | undefined;
		const imported = new Set<string>();
		try {
			for (const session of pending) {
				if (controller.signal.aborted) break;
				run.currentId = `${session.provider}:${session.nativeSessionId}`;
				if (!batchResults) publish();
				const request = new AbortController();
				const abort = () =>
					request.abort(
						new Error(
							"Import stopped. Refresh the list before retrying; the current session may have completed.",
						),
					);
				controller.signal.addEventListener("abort", abort, { once: true });
				// A stalled daemon must not leave the project locked forever. The timeout
				// only bounds network work; it never delays local navigation or feedback.
				const timer = setTimeout(
					() =>
						request.abort(
							new Error(
								"Import timed out. Refresh the list before retrying; the session may have completed.",
							),
						),
					IMPORT_REQUEST_TIMEOUT_MS,
				);
				let rejectAbort: (() => void) | undefined;
				try {
					const aborted = new Promise<never>((_, reject) => {
						rejectAbort = () => reject(request.signal.reason);
						request.signal.addEventListener("abort", rejectAbort, {
							once: true,
						});
					});
					const importRequest = async () => {
						if (pending.length === 1) return apiClient.POST("/api/v1/sessions/import", {
							body: { provider: session.provider, nativeSessionId: session.nativeSessionId, projectId }, signal: request.signal,
						});
						if (!batchResults) {
							const batch = await apiClient.POST("/api/v1/sessions/import/batch", {
								body: { projectId, sessions: pending.map(({ provider, nativeSessionId }) => ({ provider, nativeSessionId })) }, signal: request.signal,
							});
							if (batch.error) { run.stopped = true; return { error: batch.error }; }
							batchResults = new Map((batch.data?.results ?? []).map((r) => [`${r.provider}:${r.nativeSessionId}`, r]));
						}
						const result = batchResults.get(`${session.provider}:${session.nativeSessionId}`);
						return { error: !result ? new Error("Import did not complete. Refresh before retrying.") : result.error ? new Error(result.error) : undefined };
					};
					const result = await Promise.race([importRequest(), aborted]);
					if (result.error) throw result.error;
					if (controller.signal.aborted) break;
					run.progress.imported++;
					imported.add(`${session.provider}:${session.nativeSessionId}`);
				} catch (error) {
					run.errors.push({
						title: session.title || session.nativeSessionId,
						message: apiErrorMessage(error, "Import failed"),
					});
					if (!controller.signal.aborted) run.progress.failed++;
					// These prerequisites affect the whole queue. Preserve the daemon
					// envelope so one setup failure stops further import attempts.
					const code = apiErrorCode(error);
					if (
						code === "CODEX_ACCOUNT_AUTH_UNVERIFIED" ||
						code === "CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE" ||
						code === "AGENT_BINARY_NOT_FOUND"
					) run.stopped = true;
					// An uncertain timed-out result must not launch further operations.
					if (request.signal.aborted) run.stopped = true;
				} finally {
					clearTimeout(timer);
					controller.signal.removeEventListener("abort", abort);
					if (rejectAbort)
						request.signal.removeEventListener("abort", rejectAbort);
				}
				if (!controller.signal.aborted) run.progress.done++;
				if (!batchResults) publish();
				if (run.stopped) break;
				if (Date.now() - lastRefresh >= 5_000) {
					void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
					lastRefresh = Date.now();
				}
			}
		} finally {
   if (imported.size) queryClient.setQueryData<ImportableSession[]>(
    [...importableSessionsQueryKey, projectId],
    (old) => old?.map((s) => imported.has(`${s.provider}:${s.nativeSessionId}`) ? { ...s, alreadyImported: true } : s),
   );
			run = {
				...run,
				running: false,
				stopped: run.stopped || controller.signal.aborted,
				currentId: undefined,
				elapsedMs: performance.now() - startedAt,
			};
			publish();
			controllers.delete(projectId);
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			// One discovery refresh at completion, never one disk scan per session.
			void queryClient.invalidateQueries({
				queryKey: [...importableSessionsQueryKey, projectId],
			});
		}
	},
	stop: (projectId) => controllers.get(projectId)?.abort(),
	dismiss: (projectId) => {
		if (get().runs[projectId]?.running) return;
		set((s) => {
			const runs = { ...s.runs };
			delete runs[projectId];
			return { runs };
		});
	},
}));
