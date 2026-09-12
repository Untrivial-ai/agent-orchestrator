import { randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import {
	BROWSER_IMPORT_MAX_HISTORY_ENTRIES,
	type BrowserHistorySuggestion,
} from "../shared/browser-profile-import";
import { isBrowserProfileId } from "../shared/browser-profiles";

const HISTORY_VERSION = 1 as const;
const HISTORY_DIRECTORY = "browser-history";
const HISTORY_MAX_FILE_BYTES = 4 * 1024 * 1024;
const HISTORY_MAX_URL_LENGTH = 4_096;
const HISTORY_MAX_TITLE_LENGTH = 512;
const HISTORY_MAX_SUGGESTIONS = 8;

export type BrowserHistoryEntry = {
	url: string;
	title?: string;
	lastVisited: string;
	visitCount: number;
};

type BrowserHistoryFile = {
	version: typeof HISTORY_VERSION;
	entries: BrowserHistoryEntry[];
};

export type BrowserHistoryStoreOptions = {
	stateDir: string;
	now?: () => Date;
};

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function normalizeEntry(value: unknown): BrowserHistoryEntry | null {
	if (!isRecord(value) || typeof value.url !== "string" || value.url.length > HISTORY_MAX_URL_LENGTH) return null;
	let parsed: URL;
	try {
		parsed = new URL(value.url);
	} catch {
		return null;
	}
	if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return null;
	if (
		typeof value.lastVisited !== "string" ||
		!Number.isFinite(Date.parse(value.lastVisited)) ||
		typeof value.visitCount !== "number" ||
		!Number.isSafeInteger(value.visitCount) ||
		value.visitCount < 1
	) {
		return null;
	}
	const title =
		typeof value.title === "string" && value.title.trim().length > 0
			? value.title.trim().slice(0, HISTORY_MAX_TITLE_LENGTH)
			: undefined;
	return {
		url: parsed.href,
		...(title ? { title } : {}),
		lastVisited: value.lastVisited,
		visitCount: value.visitCount,
	};
}

function parseHistory(raw: unknown): BrowserHistoryFile {
	if (!isRecord(raw) || raw.version !== HISTORY_VERSION || !Array.isArray(raw.entries)) {
		throw new Error("The browser history file has an unsupported format.");
	}
	if (raw.entries.length > BROWSER_IMPORT_MAX_HISTORY_ENTRIES) {
		throw new Error("The browser history file exceeds the entry limit.");
	}
	const entries = raw.entries.map(normalizeEntry);
	if (entries.some((entry) => entry === null)) throw new Error("The browser history file is invalid.");
	return { version: HISTORY_VERSION, entries: entries as BrowserHistoryEntry[] };
}

function compact(entries: BrowserHistoryEntry[]): BrowserHistoryEntry[] {
	const byURL = new Map<string, BrowserHistoryEntry>();
	for (const entry of entries) {
		const normalized = normalizeEntry(entry);
		if (!normalized) continue;
		const existing = byURL.get(normalized.url);
		if (!existing) {
			byURL.set(normalized.url, normalized);
			continue;
		}
		const newer = Date.parse(normalized.lastVisited) >= Date.parse(existing.lastVisited) ? normalized : existing;
		byURL.set(normalized.url, {
			...newer,
			visitCount: Math.min(Number.MAX_SAFE_INTEGER, existing.visitCount + normalized.visitCount),
		});
	}
	return [...byURL.values()].sort(
		(a, b) => Date.parse(b.lastVisited) - Date.parse(a.lastVisited) || a.url.localeCompare(b.url),
	);
}

function fittedHistory(entries: BrowserHistoryEntry[]): { entries: BrowserHistoryEntry[]; serialized: string } {
	const retained = [...entries];
	while (retained.length > 0) {
		const serialized = `${JSON.stringify({ version: HISTORY_VERSION, entries: retained }, null, 2)}\n`;
		if (Buffer.byteLength(serialized) <= HISTORY_MAX_FILE_BYTES) return { entries: retained, serialized };
		retained.pop();
	}
	return { entries: [], serialized: `${JSON.stringify({ version: HISTORY_VERSION, entries: [] }, null, 2)}\n` };
}

export class BrowserHistoryStore {
	private readonly directory: string;
	private readonly now: () => Date;
	private readonly cache = new Map<string, BrowserHistoryEntry[]>();
	private readonly queues = new Map<string, Promise<void>>();

	constructor(options: BrowserHistoryStoreOptions) {
		this.directory = path.join(options.stateDir, HISTORY_DIRECTORY);
		this.now = options.now ?? (() => new Date());
	}

	private file(profileId: string): string {
		if (!isBrowserProfileId(profileId)) throw new Error("Browser profile ID is invalid.");
		return path.join(this.directory, `${profileId}.json`);
	}

	private async load(profileId: string): Promise<BrowserHistoryEntry[]> {
		const cached = this.cache.get(profileId);
		if (cached) return cached.map((entry) => ({ ...entry }));
		const file = this.file(profileId);
		try {
			const metadata = await stat(file);
			if (!metadata.isFile() || metadata.size > HISTORY_MAX_FILE_BYTES) {
				throw new Error("The browser history file exceeds the size limit.");
			}
			const parsed = parseHistory(JSON.parse(await readFile(file, "utf8")));
			this.cache.set(profileId, parsed.entries);
			return parsed.entries.map((entry) => ({ ...entry }));
		} catch (error) {
			if ((error as NodeJS.ErrnoException).code === "ENOENT") {
				this.cache.set(profileId, []);
				return [];
			}
			throw error;
		}
	}

	private async write(profileId: string, entries: BrowserHistoryEntry[]): Promise<BrowserHistoryEntry[]> {
		await mkdir(this.directory, { recursive: true, mode: 0o750 });
		const file = this.file(profileId);
		const temporary = path.join(this.directory, `.${profileId}.${process.pid}.${randomUUID()}.tmp`);
		const fitted = fittedHistory(entries);
		try {
			await writeFile(temporary, fitted.serialized, { mode: 0o600 });
			await rename(temporary, file);
			return fitted.entries;
		} catch (error) {
			await rm(temporary, { force: true }).catch(() => undefined);
			throw error;
		}
	}

	private enqueue(profileId: string, operation: () => Promise<void>): Promise<void> {
		const previous = this.queues.get(profileId) ?? Promise.resolve();
		const queued = previous.then(operation, operation);
		const tail = queued.then(
			() => undefined,
			() => undefined,
		);
		this.queues.set(profileId, tail);
		void tail.then(() => {
			if (this.queues.get(profileId) === tail) this.queues.delete(profileId);
		});
		return queued;
	}

	async drain(): Promise<void> {
		while (this.queues.size > 0) {
			await Promise.all([...this.queues.values()]);
		}
	}

	async mergeImportedEntries(
		profileId: string,
		imported: BrowserHistoryEntry[],
	): Promise<{ imported: number; truncated: number }> {
		let importedCount = 0;
		let truncated = 0;
		await this.enqueue(profileId, async () => {
			const normalized = compact(imported);
			const combined = compact([...(await this.load(profileId)), ...normalized])
				.slice(0, BROWSER_IMPORT_MAX_HISTORY_ENTRIES);
			const entries = await this.write(profileId, combined);
			const retainedURLs = new Set(entries.map((entry) => entry.url));
			importedCount = normalized.filter((entry) => retainedURLs.has(entry.url)).length;
			truncated = normalized.length - importedCount;
			this.cache.set(profileId, entries);
		});
		return { imported: importedCount, truncated };
	}

	async record(profileId: string, rawURL: string, rawTitle: string, incrementVisit: boolean): Promise<void> {
		const normalized = normalizeEntry({
			url: rawURL,
			title: rawTitle,
			lastVisited: this.now().toISOString(),
			visitCount: 1,
		});
		if (!normalized) return;
		await this.enqueue(profileId, async () => {
			const current = await this.load(profileId);
			const existing = current.find((entry) => entry.url === normalized.url);
			const nextEntry = existing
				? {
						...existing,
						...(normalized.title ? { title: normalized.title } : {}),
						lastVisited: normalized.lastVisited,
						visitCount: incrementVisit ? existing.visitCount + 1 : existing.visitCount,
					}
				: normalized;
			const entries = await this.write(
				profileId,
				compact([nextEntry, ...current.filter((entry) => entry.url !== normalized.url)])
					.slice(0, BROWSER_IMPORT_MAX_HISTORY_ENTRIES),
			);
			this.cache.set(profileId, entries);
		});
	}

	async suggest(profileId: string, rawQuery: string): Promise<BrowserHistorySuggestion[]> {
		const query = rawQuery.trim().toLowerCase().slice(0, 512);
		if (!query) return [];
		const entries = await this.load(profileId);
		return entries
			.map((entry) => {
				const title = entry.title?.toLowerCase() ?? "";
				const url = entry.url.toLowerCase();
				const score = url.startsWith(query) ? 0 : title.startsWith(query) ? 1 : url.includes(query) ? 2 : title.includes(query) ? 3 : -1;
				return { entry, score };
			})
			.filter((candidate) => candidate.score >= 0)
			.sort((a, b) => a.score - b.score || Date.parse(b.entry.lastVisited) - Date.parse(a.entry.lastVisited))
			.slice(0, HISTORY_MAX_SUGGESTIONS)
			.map(({ entry }) => ({ url: entry.url, ...(entry.title ? { title: entry.title } : {}) }));
	}

	async clear(profileId: string): Promise<void> {
		await this.enqueue(profileId, async () => {
			await rm(this.file(profileId), { force: true });
			this.cache.delete(profileId);
		});
	}
}
