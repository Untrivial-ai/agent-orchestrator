import { create } from "zustand";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import type { ImportableSession } from "../hooks/useImportableSessions";

// importLanes is how many folders are imported at once. Imports are dominated
// by waiting — scanning, git, starting an agent — so running several at once is
// most of the win, while a cap keeps a big history from opening hundreds of git
// operations and agent processes simultaneously.
const importLanes = 4;

export type ImportRunProgress = {
	done: number;
	total: number;
	imported: number;
	failed: number;
};

type ImportRunState = {
	progress: ImportRunProgress | null;
	running: boolean;
	/** start imports every pending conversation, off the component tree. */
	start: (sessions: ImportableSession[]) => Promise<void>;
	stop: () => void;
	/** dismiss clears a finished run's summary. */
	dismiss: () => void;
};

// The run deliberately lives in a store rather than in the dialog that starts
// it. A hundred imports take minutes, and nobody should have to sit and watch a
// modal for that long: closing the dialog, or navigating away, leaves the run
// going and the sidebar keeps showing where it has got to.
//
// cancelled is module state rather than store state because the running loop
// reads it on every step and must see a stop immediately, without waiting for a
// React update to propagate.
let cancelled = false;

async function importOne(session: ImportableSession): Promise<void> {
	const { error } = await apiClient.POST("/api/v1/sessions/import", {
		body: { provider: session.provider, nativeSessionId: session.nativeSessionId },
	});
	if (error) throw new Error(apiErrorMessage(error, "Import failed"));
}

export const useImportRunStore = create<ImportRunState>((set, get) => ({
	progress: null,
	running: false,

	start: async (sessions) => {
		if (get().running) return;

		const pending = sessions.filter((session) => !session.alreadyImported);
		if (pending.length === 0) return;

		cancelled = false;
		let done = 0;
		let imported = 0;
		let failed = 0;
		set({ running: true, progress: { done: 0, total: pending.length, imported: 0, failed: 0 } });

		// One lane per folder, so imports inside a repository stay ordered while
		// different repositories proceed at the same time. Creating a git worktree
		// takes a repository-wide lock, so two imports racing inside one
		// repository would contend for it and fail for no reason.
		const byFolder = new Map<string, ImportableSession[]>();
		for (const session of pending) {
			const folder = session.cwd || "";
			const bucket = byFolder.get(folder);
			if (bucket) bucket.push(session);
			else byFolder.set(folder, [session]);
		}
		const lanes = [...byFolder.values()];
		let nextLane = 0;

		const runLane = async () => {
			for (;;) {
				if (cancelled) return;
				const lane = lanes[nextLane++];
				if (!lane) return;
				for (const session of lane) {
					if (cancelled) return;
					try {
						await importOne(session);
						imported += 1;
					} catch {
						// Counted, not surfaced one by one: a run of a hundred cannot
						// stop to explain each failure. The summary reports the total.
						failed += 1;
					}
					done += 1;
					set({ progress: { done, total: pending.length, imported, failed } });
				}
			}
		};

		try {
			await Promise.all(Array.from({ length: Math.min(importLanes, lanes.length) }, runLane));
		} finally {
			// Whether the run finished or was stopped, it is no longer running.
			// Stopping still leaves a result worth reporting.
			set({ running: false });
		}
	},

	stop: () => {
		cancelled = true;
	},

	dismiss: () => set({ progress: null }),
}));
