import type { DownloadItem, Session } from "electron";
import { randomUUID } from "node:crypto";
import { existsSync, lstatSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import path from "node:path";
import type {
	BrowserDownload,
	BrowserDownloadActionInput,
	BrowserDownloadsState,
} from "../shared/browser-downloads";

type DownloadItemLike = Pick<
	DownloadItem,
	| "cancel"
	| "getFilename"
	| "getReceivedBytes"
	| "getTotalBytes"
	| "isPaused"
	| "on"
	| "once"
	| "removeListener"
	| "pause"
	| "canResume"
	| "resume"
	| "setSavePath"
>;

type DownloadSessionLike = Pick<Session, "on" | "removeListener">;

type DownloadShell = {
	openPath: (filePath: string) => Promise<string>;
	showItemInFolder: (filePath: string) => void;
	trashItem: (filePath: string) => Promise<void>;
};

type StoredDownload = BrowserDownload & { savePath: string };

type BrowserDownloadManagerOptions = {
	downloadsDirectory: string;
	historyPath: string;
	shell: DownloadShell;
	notify: (state: BrowserDownloadsState) => void;
	now?: () => number;
	createId?: () => string;
};

const MAX_DOWNLOAD_HISTORY = 200;
const DOWNLOAD_DESTINATION_ERROR = "Could not prepare the Downloads folder.";
const DOWNLOAD_DELETE_ERROR = "Could not delete the downloaded file.";

function publicDownload(download: StoredDownload): BrowserDownload {
	const { savePath: _savePath, ...safe } = download;
	return safe;
}

function safeFilename(value: string): string {
	const fileName = path.basename(value.trim()).replace(/[. ]+$/u, "");
	return fileName && fileName !== "." && fileName !== ".." ? fileName : "download";
}

function collisionSafePath(directory: string, fileName: string, unavailable: Set<string>): string {
	const parsed = path.parse(fileName);
	let candidate = path.join(directory, fileName);
	let suffix = 1;
	while (existsSync(candidate) || unavailable.has(candidate.toLowerCase())) {
		candidate = path.join(directory, `${parsed.name} (${suffix})${parsed.ext}`);
		suffix += 1;
	}
	return candidate;
}

function isInsideDirectory(directory: string, candidate: string): boolean {
	const relative = path.relative(path.resolve(directory), path.resolve(candidate));
	return relative !== "" && relative !== ".." && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function isStoredDownload(value: unknown): value is StoredDownload {
	if (!value || typeof value !== "object" || Array.isArray(value)) return false;
	const item = value as Partial<StoredDownload>;
	return (
		typeof item.id === "string" &&
		typeof item.fileName === "string" &&
		typeof item.savePath === "string" &&
		typeof item.receivedBytes === "number" &&
		typeof item.totalBytes === "number" &&
		typeof item.startedAt === "number" &&
		typeof item.updatedAt === "number" &&
		["progressing", "paused", "completed", "cancelled", "interrupted"].includes(item.status ?? "")
	);
}

export type BrowserDownloadManager = ReturnType<typeof createBrowserDownloadManager>;

export function createBrowserDownloadManager(options: BrowserDownloadManagerOptions) {
	const attachedSessions = new Map<DownloadSessionLike, (event: unknown, item: DownloadItem) => void>();
	const activeItems = new Map<string, DownloadItemLike>();
	const activeItemListeners = new Map<string, {
		item: DownloadItemLike;
		updated: (event: unknown, state: "progressing" | "interrupted") => void;
		done: (event: unknown, state: "completed" | "cancelled" | "interrupted") => void;
	}>();
	const reservedPaths = new Set<string>();
	let disposed = false;
	let downloads: StoredDownload[] = [];
	let error = "";

	try {
		const parsed = JSON.parse(readFileSync(options.historyPath, "utf8")) as unknown;
		if (Array.isArray(parsed)) {
			downloads = parsed
				.filter((download): download is StoredDownload =>
					isStoredDownload(download) && isInsideDirectory(options.downloadsDirectory, download.savePath))
				.map((download) => ({
					...download,
					fileName: path.basename(download.savePath),
					status: download.status === "progressing" || download.status === "paused" ? "interrupted" : download.status,
					active: false,
					resumable: false,
				}));
		}
	} catch {
		// A missing or malformed optional history file starts with an empty list.
	}

	const state = (): BrowserDownloadsState => ({
		downloads: downloads.map(publicDownload),
		...(error ? { error } : {}),
	});
	const persist = (): void => {
		mkdirSync(path.dirname(options.historyPath), { recursive: true });
		const temporaryPath = `${options.historyPath}.tmp`;
		writeFileSync(temporaryPath, JSON.stringify(downloads), { encoding: "utf8", mode: 0o600 });
		renameSync(temporaryPath, options.historyPath);
	};
	const publish = (persistState = false): BrowserDownloadsState => {
		if (persistState) {
			try {
				persist();
			} catch {
				// A history write failure must not abort Chromium's file transfer.
			}
		}
		const next = state();
		if (!disposed) {
			try {
				options.notify(next);
			} catch {
				// A window can disappear while an Electron download event is being
				// dispatched. Notification failure must not block other listeners.
			}
		}
		return next;
	};
	const updateItem = (id: string, item: DownloadItemLike, status?: StoredDownload["status"]): void => {
		downloads = downloads.map<StoredDownload>((download) =>
			download.id === id
				? {
						...download,
						receivedBytes: Math.max(0, item.getReceivedBytes()),
						totalBytes: Math.max(0, item.getTotalBytes()),
						status: status ?? (item.isPaused() ? "paused" : "progressing"),
						active: activeItems.has(id),
						resumable: status === "interrupted" && activeItems.has(id) && item.canResume(),
						updatedAt: (options.now ?? Date.now)(),
					}
				: download,
		);
	};

	const begin = (item: DownloadItemLike): void => {
		let savePath: string;
		try {
			mkdirSync(options.downloadsDirectory, { recursive: true });
			savePath = collisionSafePath(options.downloadsDirectory, safeFilename(item.getFilename()), reservedPaths);
			item.setSavePath(savePath);
		} catch {
			try {
				item.cancel();
			} catch {
				// The destination failure remains the useful error for the user.
			}
			error = DOWNLOAD_DESTINATION_ERROR;
			publish();
			return;
		}
		error = "";
		const id = (options.createId ?? randomUUID)();
		const now = (options.now ?? Date.now)();
		reservedPaths.add(savePath.toLowerCase());
		activeItems.set(id, item);
		const download: StoredDownload = {
			id,
			fileName: path.basename(savePath),
			savePath,
			receivedBytes: Math.max(0, item.getReceivedBytes()),
			totalBytes: Math.max(0, item.getTotalBytes()),
			status: "progressing",
			active: true,
			resumable: false,
			startedAt: now,
			updatedAt: now,
		};
		downloads = [download, ...downloads].slice(0, MAX_DOWNLOAD_HISTORY);
		publish(true);
		const updated = (_event: unknown, updateState: "progressing" | "interrupted") => {
			updateItem(id, item, updateState === "interrupted" ? "interrupted" : undefined);
			publish();
		};
		const done = (_event: unknown, doneState: "completed" | "cancelled" | "interrupted") => {
			activeItems.delete(id);
			activeItemListeners.delete(id);
			reservedPaths.delete(savePath.toLowerCase());
			updateItem(id, item, doneState);
			publish(true);
		};
		activeItemListeners.set(id, { item, updated, done });
		item.on("updated", updated);
		item.once("done", done);
	};

	return {
		attach(session: DownloadSessionLike | undefined): void {
			if (!session || disposed || attachedSessions.has(session)) return;
			const listener = (_event: unknown, item: DownloadItem) => begin(item);
			attachedSessions.set(session, listener);
			session.on("will-download", listener);
		},
		dispose(): void {
			if (disposed) return;
			disposed = true;
			for (const [session, listener] of attachedSessions) {
				session.removeListener("will-download", listener);
			}
			attachedSessions.clear();
			for (const { item, updated, done } of activeItemListeners.values()) {
				item.removeListener("updated", updated);
				item.removeListener("done", done);
			}
			activeItemListeners.clear();
			activeItems.clear();
			reservedPaths.clear();
		},
		list: state,
		async action(input: BrowserDownloadActionInput): Promise<BrowserDownloadsState> {
			if (!input || typeof input.id !== "string") throw new Error("Invalid download action");
			const download = downloads.find((candidate) => candidate.id === input.id);
			if (!download) throw new Error("Download not found");
			const item = activeItems.get(input.id);
			switch (input.action) {
				case "pause":
					if (!item) throw new Error("Download is no longer active");
					item.pause();
					updateItem(input.id, item, "paused");
					return publish(true);
				case "resume":
					if (!item) throw new Error("Download is no longer active");
					item.resume();
					updateItem(input.id, item, "progressing");
					return publish(true);
				case "cancel":
					if (!item) throw new Error("Download is no longer active");
					item.cancel();
					return state();
				case "open": {
					if (download.status !== "completed" || !existsSync(download.savePath)) throw new Error("Downloaded file is unavailable");
					const error = await options.shell.openPath(download.savePath);
					if (error) throw new Error(error);
					return state();
				}
				case "show":
					if (download.status !== "completed" || !existsSync(download.savePath)) throw new Error("Downloaded file is unavailable");
					options.shell.showItemInFolder(download.savePath);
					return state();
				case "remove": {
					if (item) throw new Error("Active downloads cannot be removed");
					try {
						if (existsSync(download.savePath)) {
							if (!isInsideDirectory(options.downloadsDirectory, download.savePath)) {
								throw new Error(DOWNLOAD_DELETE_ERROR);
							}
							const file = lstatSync(download.savePath);
							if (!file.isFile() && !file.isSymbolicLink()) throw new Error(DOWNLOAD_DELETE_ERROR);
							await options.shell.trashItem(download.savePath);
						}
					} catch {
						throw new Error(DOWNLOAD_DELETE_ERROR);
					}
					downloads = downloads.filter((candidate) => candidate.id !== input.id);
					return publish(true);
				}
				default:
					throw new Error("Unsupported download action");
			}
		},
		clear(): BrowserDownloadsState {
			downloads = downloads.filter((download) => activeItems.has(download.id));
			return publish(true);
		},
	};
}
