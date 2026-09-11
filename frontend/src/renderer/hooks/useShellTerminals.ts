// Standalone shell terminals: shells the user opens by hand from the topbar or
// ⌘T / Ctrl+T, with no agent session behind them. They are deliberately kept out of
// the workspaces query — they are not sessions, never appear on the board, and
// must not invalidate session state when they come and go.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { markTerminalHandleFresh } from "../lib/fresh-terminal-handles";
import type { components } from "../../api/schema";
import { apiClient, apiErrorCode, hasTrustedApiBaseUrl } from "../lib/api-client";
import { mockShellTerminals } from "../lib/mock-data";
import { isWindowsPlatform } from "../lib/platform";
import { terminalShellRequestValue, useTerminalShellStore } from "../stores/terminal-shell-store";

export type ShellTerminal = {
	/** Runtime handle the terminal mux attaches to, exactly like a session pane's. */
	handleId: string;
	projectId?: string;
	/** Agent session this shell is scoped to; absent for standalone shells. */
	sessionId?: string;
	workingDir: string;
	title: string;
	createdAt: string;
	/**
	 * Exists only in the renderer while the daemon is creating the PTY. It lets
	 * the tab strip respond to the click immediately without ever attempting to
	 * attach xterm to a handle that does not exist yet.
	 */
	optimistic?: true;
};

export const shellTerminalsQueryKey = ["shell-terminals"] as const;
const usePreviewData = import.meta.env.VITE_NO_ELECTRON === "1";

function isLegacyDirectoryTitle(title: string, workingDir: string): boolean {
	const parts = workingDir.split(/[\\/]/).filter(Boolean);
	return parts.at(-1) === title;
}

function toShellTerminal(t: components["schemas"]["ShellTerminalResponse"]): ShellTerminal {
	const title = isLegacyDirectoryTitle(t.title, t.workingDir) ? "Terminal" : t.title;
	return {
		handleId: t.handleId,
		projectId: t.projectId,
		sessionId: t.sessionId,
		workingDir: t.workingDir,
		// Shell tabs used to be named after their initial directory. Normalize
		// those persisted legacy labels so existing tabs adopt the new idle state.
		title: title === "Terminal" ? "Terminal 1" : title,
		createdAt: t.createdAt,
	};
}

// Preview-only shell list. The browser build has no daemon to spawn a PTY, so
// open/close mutate this array instead — keeping the tab strip fully
// interactive (open, select, close) without a backend, which is what the e2e
// suite drives.
let previewShellTerminals: ShellTerminal[] = [...mockShellTerminals];
let previewShellSeq = 0;

async function fetchShellTerminals(): Promise<ShellTerminal[]> {
	if (usePreviewData) {
		return previewShellTerminals;
	}
	if (!hasTrustedApiBaseUrl()) {
		return [];
	}
	const { data, error } = await apiClient.GET("/api/v1/shell-terminals");
	if (error) throw error;
	return (data?.shellTerminals ?? []).map(toShellTerminal);
}

// No refetchInterval: shell terminals only change when this client opens or
// closes one, and both mutations invalidate the query. Polling would spend a
// liveness probe per shell per interval for no new information.
export const shellTerminalsQueryOptions = {
	queryKey: shellTerminalsQueryKey,
	queryFn: fetchShellTerminals,
	retry: 1,
};

export function useShellTerminals() {
	return useQuery(shellTerminalsQueryOptions);
}

export type OpenShellTerminalInput = { projectId?: string; sessionId?: string; shell?: string };
type OpenShellTerminalMutationInput = OpenShellTerminalInput & { optimisticShell?: ShellTerminal };
type OpenShellTerminalCallbacks = { onSuccess?: (shell: ShellTerminal) => void };

function nextShellTerminalTitle(terminals: ShellTerminal[]): string {
	let maxNumber = 0;
	for (const terminal of terminals) {
		if (terminal.title === "Terminal") {
			maxNumber = Math.max(maxNumber, 1);
			continue;
		}
		const match = /^Terminal (\d+)$/.exec(terminal.title);
		if (match) maxNumber = Math.max(maxNumber, Number(match[1]));
	}
	return `Terminal ${maxNumber + 1}`;
}

function createOptimisticShellTerminal(
	{ projectId, sessionId }: OpenShellTerminalInput,
	terminals: ShellTerminal[],
): ShellTerminal {
	const id = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
	return {
		handleId: `pending-shell:${id}`,
		projectId,
		sessionId,
		workingDir: "",
		title: nextShellTerminalTitle(terminals),
		createdAt: new Date().toISOString(),
		optimistic: true,
	};
}

function addOptimisticShell(queryClient: ReturnType<typeof useQueryClient>, shell: ShellTerminal) {
	queryClient.setQueryData<ShellTerminal[]>(shellTerminalsQueryKey, (current) =>
		current?.some((candidate) => candidate.handleId === shell.handleId) ? current : [...(current ?? []), shell],
	);
}

/**
 * Opens a shell in the given project's root (or the daemon data dir when
 * omitted). When sessionId is set the shell is scoped to that session and only
 * appears in its tab strip; otherwise it is a standalone shell on /terminals.
 */
export function useOpenShellTerminal() {
	const queryClient = useQueryClient();
	const mutation = useMutation({
		mutationFn: async ({ projectId, sessionId, shell, optimisticShell }: OpenShellTerminalMutationInput = {}): Promise<ShellTerminal> => {
			if (usePreviewData) {
				previewShellSeq += 1;
				const shell: ShellTerminal = {
					handleId: `shellterm-preview-${previewShellSeq}`,
					projectId,
					sessionId,
					workingDir: `/Users/demo/Projects/${projectId ?? "ao"}`,
					title: optimisticShell?.title ?? `Terminal ${previewShellSeq}`,
					createdAt: new Date().toISOString(),
				};
				previewShellTerminals = [...previewShellTerminals, shell];
				return shell;
			}
			const body: OpenShellTerminalInput = {};
			if (projectId) body.projectId = projectId;
			if (sessionId) body.sessionId = sessionId;
			if (isWindowsPlatform()) {
				await useTerminalShellStore.getState().load();
				body.shell = shell ?? terminalShellRequestValue(useTerminalShellStore.getState().preference);
			}
			const { data, error } = await apiClient.POST("/api/v1/shell-terminals", { body });
			if (error) throw error;
			if (!data) throw new Error("Daemon returned no shell terminal");
			markTerminalHandleFresh(data.shellTerminal.handleId);
			return toShellTerminal(data.shellTerminal);
		},
		onMutate: (input) => {
			const optimisticShell =
				input.optimisticShell ??
				createOptimisticShellTerminal(input, queryClient.getQueryData<ShellTerminal[]>(shellTerminalsQueryKey) ?? []);
			addOptimisticShell(queryClient, optimisticShell);
			return { optimisticHandleId: optimisticShell.handleId };
		},
		onSuccess: (shell, _input, context) => {
			// Replace, rather than append to, the tab that was visible while the POST
			// ran. This preserves selection and prevents a duplicate tab flash.
			queryClient.setQueryData<ShellTerminal[]>(shellTerminalsQueryKey, (current) => {
				if (current?.some((candidate) => candidate.handleId === shell.handleId)) return current;
				const optimisticHandleId = context?.optimisticHandleId;
				const index = current?.findIndex((candidate) => candidate.handleId === optimisticHandleId) ?? -1;
				if (index < 0) return [...(current ?? []), shell];
				return current?.map((candidate, candidateIndex) => (candidateIndex === index ? shell : candidate)) ?? [shell];
			});
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
		},
		onError: (error, _input, context) => {
			queryClient.setQueryData<ShellTerminal[]>(shellTerminalsQueryKey, (current) =>
				current?.filter((shell) => shell.handleId !== context?.optimisticHandleId),
			);
			console.error("Failed to open shell terminal:", error);
			if (isWindowsPlatform() && apiErrorCode(error) === "SHELL_TERMINAL_SHELL_UNAVAILABLE") {
				void useTerminalShellStore.getState().setPreference({ kind: "auto" });
			}
		},
		onSettled: () => {
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
		},
	});

	// Session topbars need the pending shell synchronously so they can select
	// it in the same click event. Other callers can keep using mutation.mutate;
	// onMutate supplies an optimistic entry for them too.
	const open = (input: OpenShellTerminalInput = {}, callbacks?: OpenShellTerminalCallbacks) => {
		const optimisticShell = createOptimisticShellTerminal(
			input,
			queryClient.getQueryData<ShellTerminal[]>(shellTerminalsQueryKey) ?? [],
		);
		addOptimisticShell(queryClient, optimisticShell);
		mutation.mutate({ ...input, optimisticShell }, callbacks);
		return optimisticShell;
	};

	return { ...mutation, open };
}

/** Closes a shell and destroys its PTY. */
export async function closeShellTerminal(handleId: string): Promise<void> {
	if (usePreviewData) {
		previewShellTerminals = previewShellTerminals.filter((shell) => shell.handleId !== handleId);
		return;
	}
	const { error } = await apiClient.DELETE("/api/v1/shell-terminals/{handleId}", {
		params: { path: { handleId } },
	});
	// The desired postcondition is already true when the daemon no longer owns
	// the record. Treat this as confirmed cleanup, not a failed cancellation.
	if (error && apiErrorCode(error) !== "SHELL_TERMINAL_NOT_FOUND") throw error;
}

export function useCloseShellTerminal() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: closeShellTerminal,
		onMutate: async (handleId) => {
			const previous = queryClient.getQueryData<ShellTerminal[]>(shellTerminalsQueryKey);
			const removeClosedShell = () => {
				queryClient.setQueryData<ShellTerminal[]>(shellTerminalsQueryKey, (current) =>
					current?.filter((shell) => shell.handleId !== handleId),
				);
			};
			// Remove the pill synchronously. Waiting for cancellation first leaves the
			// closed tab visible for the duration of an in-flight list request.
			removeClosedShell();
			await queryClient.cancelQueries({ queryKey: shellTerminalsQueryKey });
			// A request that resolved while cancellation was being scheduled may have
			// restored its stale snapshot; make the optimistic state authoritative.
			removeClosedShell();
			return { previous };
		},
		onError: (error, _handleId, context) => {
			// A 404 means the daemon has already removed the shell, so restoring its
			// stale tab would be misleading. Other failures put the tab back so the
			// user can retry instead of losing access to a still-live PTY.
			if (apiErrorCode(error) !== "SHELL_TERMINAL_NOT_FOUND" && context?.previous) {
				queryClient.setQueryData(shellTerminalsQueryKey, context.previous);
			}
		},
		// Settled, not success: a close that 404s means the daemon already lost
		// the shell, and the stale tab still needs to disappear.
		onSettled: () => {
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
		},
	});
}

export type RenameShellTerminalInput = { handleId: string; title: string };

/** Renames a shell terminal's tab. The new title persists on the daemon. */
export function useRenameShellTerminal() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async ({ handleId, title }: RenameShellTerminalInput): Promise<ShellTerminal> => {
			if (usePreviewData) {
				previewShellTerminals = previewShellTerminals.map((s) => (s.handleId === handleId ? { ...s, title } : s));
				const shell = previewShellTerminals.find((s) => s.handleId === handleId);
				if (!shell) throw new Error("No such shell terminal");
				return shell;
			}
			const { data, error } = await apiClient.PATCH("/api/v1/shell-terminals/{handleId}", {
				params: { path: { handleId } },
				body: { title },
			});
			if (error) throw error;
			if (!data) throw new Error("Daemon returned no shell terminal");
			return toShellTerminal(data.shellTerminal);
		},
		onMutate: async ({ handleId, title }) => {
			await queryClient.cancelQueries({ queryKey: shellTerminalsQueryKey });
			const previous = queryClient.getQueryData<ShellTerminal[]>(shellTerminalsQueryKey);
			queryClient.setQueryData<ShellTerminal[]>(shellTerminalsQueryKey, (current) =>
				current?.map((shell) => (shell.handleId === handleId ? { ...shell, title } : shell)),
			);
			return { previous };
		},
		onError: (_error, _input, context) => {
			if (context?.previous) queryClient.setQueryData(shellTerminalsQueryKey, context.previous);
		},
		onSuccess: (shell) => {
			queryClient.setQueryData<ShellTerminal[]>(shellTerminalsQueryKey, (current) =>
				current?.map((candidate) => (candidate.handleId === shell.handleId ? shell : candidate)),
			);
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
		},
	});
}
