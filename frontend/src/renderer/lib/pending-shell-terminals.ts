// A new shell tab exists in the renderer before its PTY does. The tab's xterm
// mounts first, measures its grid, and only then does the daemon create the
// PTY at that grid, so the shell's first output is laid out for the width the
// user actually sees. (A PTY created at a guessed width makes zsh's partial-line
// marker wrap and leave a stray "%" above the first prompt.) The same xterm then
// carries on under the daemon's handle instead of being replaced.
//
// This module is the rendezvous between the three parties involved: the open
// mutation (waits for the grid, then announces the created shell), the pending
// tab's terminal (reports its measured grid), and the terminal cache (re-keys the
// pending tab's retained terminal to the created handle).

export const PENDING_SHELL_HANDLE_PREFIX = "pending-shell:";

export type TerminalGrid = { cols: number; rows: number };

type PendingGrid = {
	promise: Promise<TerminalGrid>;
	resolve: (grid: TerminalGrid) => void;
	reject: (error: Error) => void;
};

type PendingShellCache = {
	/** Hand the pending tab's retained terminal over to the created shell. */
	adopt: (pendingHandleId: string, shell: CreatedShell) => void;
	/** Dispose the pending tab's retained terminal; creation failed. */
	discard: (pendingHandleId: string) => void;
};

export type CreatedShell = {
	handleId: string;
	createdAt: string;
	title: string;
};

const pendingGrids = new Map<string, PendingGrid>();
let cache: PendingShellCache | null = null;

export function isPendingShellHandle(handleId: string): boolean {
	return handleId.startsWith(PENDING_SHELL_HANDLE_PREFIX);
}

/** Resolves with the grid the pending tab's terminal measured. */
export function pendingShellGrid(pendingHandleId: string): Promise<TerminalGrid> {
	return pendingGridEntry(pendingHandleId).promise;
}

/** Called by the pending tab's terminal once it has measured a real grid. */
export function reportPendingShellGrid(pendingHandleId: string, grid: TerminalGrid): void {
	pendingGridEntry(pendingHandleId).resolve(grid);
}

/** Fails a pending creation whose terminal can never measure (renderer init failed). */
export function failPendingShell(pendingHandleId: string, error: Error): void {
	pendingGridEntry(pendingHandleId).reject(error);
}

/** Announces the created shell so the cache can re-key the pending terminal. */
export function adoptPendingShell(pendingHandleId: string, shell: CreatedShell): void {
	pendingGrids.delete(pendingHandleId);
	cache?.adopt(pendingHandleId, shell);
}

/** Drops all pending state for a creation that failed. */
export function discardPendingShell(pendingHandleId: string): void {
	pendingGrids.delete(pendingHandleId);
	cache?.discard(pendingHandleId);
}

export function registerPendingShellCache(next: PendingShellCache): () => void {
	cache = next;
	return () => {
		if (cache === next) cache = null;
	};
}

function pendingGridEntry(pendingHandleId: string): PendingGrid {
	let entry = pendingGrids.get(pendingHandleId);
	if (!entry) {
		let resolve!: (grid: TerminalGrid) => void;
		let reject!: (error: Error) => void;
		const promise = new Promise<TerminalGrid>((res, rej) => {
			resolve = res;
			reject = rej;
		});
		// The terminal can fail before the mutation starts waiting.
		promise.catch(() => undefined);
		entry = { promise, resolve, reject };
		pendingGrids.set(pendingHandleId, entry);
	}
	return entry;
}
