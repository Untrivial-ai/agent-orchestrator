import { createDecipheriv, createHash, pbkdf2Sync, randomUUID } from "node:crypto";
import { constants } from "node:fs";
import {
	chmod,
	lstat,
	mkdir,
	open,
	readdir,
	realpath,
	rm,
	stat,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import Database from "better-sqlite3";
import type { CookiesSetDetails } from "electron";
import {
	BROWSER_IMPORT_MAX_COOKIES,
	BROWSER_IMPORT_MAX_HISTORY_ENTRIES,
	BROWSER_IMPORT_MAX_SOURCE_PROFILES,
	type BrowserImportCookieSupportReason,
	type BrowserImportDiscovery,
	type BrowserImportProgress,
	type BrowserImportRequest,
	type BrowserImportResult,
	type BrowserImportResultEntry,
	type BrowserImportSource,
	type BrowserImportWarning,
} from "../shared/browser-profile-import";
import {
	browserProfilePartition,
	BROWSER_PROFILE_MAX_COUNT,
	isBrowserProfileId,
	normalizeBrowserProfileName,
	type BrowserProfile,
} from "../shared/browser-profiles";
import { BrowserHistoryStore, type BrowserHistoryEntry } from "./browser-history-store";
import { BrowserProfileStore } from "./browser-profile-store";

const execFileAsync = promisify(execFile);
const SOURCE_FILE_MAX_BYTES = 256 * 1024 * 1024;
const SOURCE_SIDECAR_MAX_BYTES = 64 * 1024 * 1024;
const IMPORT_TOTAL_MAX_BYTES = 512 * 1024 * 1024;
const LOCAL_STATE_MAX_BYTES = 4 * 1024 * 1024;
const SOURCE_ID_PATTERN = /^[0-9a-f]{32}$/;

type BrowserFamily = "chromium" | "firefox";

type BrowserDescriptor = {
	id: string;
	name: string;
	family: BrowserFamily;
	roots: (context: DiscoveryContext) => string[];
	chromiumKeychainNames?: string[];
};

type DiscoveryContext = {
	platform: NodeJS.Platform;
	homeDir: string;
	env: NodeJS.ProcessEnv;
};

type InternalSourceProfile = {
	id: string;
	name: string;
	default: boolean;
	root: string;
};

type InternalSource = {
	public: BrowserImportSource;
	descriptor: BrowserDescriptor;
	root: string;
	profiles: InternalSourceProfile[];
};

type ImportedCookie = CookiesSetDetails & {
	dedupeKey: string;
};

type ReadProfileData = {
	profile: InternalSourceProfile;
	cookies: ImportedCookie[];
	history: BrowserHistoryEntry[];
	warnings: BrowserImportWarning[];
	skippedCookies: number;
};

type ImportSession = {
	cookies: { set: (details: CookiesSetDetails) => Promise<void> };
	clearStorageData: () => Promise<void>;
	clearCache: () => Promise<void>;
};

export type BrowserProfileImportOptions = {
	stateDir: string;
	profileStore: BrowserProfileStore;
	historyStore: BrowserHistoryStore;
	fromPartition: (partition: string) => ImportSession;
	platform?: NodeJS.Platform;
	homeDir?: string;
	env?: NodeJS.ProcessEnv;
	now?: () => Date;
};

class SourceBudget {
	private used = 0;

	consume(bytes: number): void {
		if (!Number.isSafeInteger(bytes) || bytes < 0 || this.used + bytes > IMPORT_TOTAL_MAX_BYTES) {
			throw new Error("Selected browser data exceeds the import size limit.");
		}
		this.used += bytes;
	}
}

function throwIfImportAborted(signal: AbortSignal): void {
	if (signal.aborted) throw new Error("Browser import was canceled because the app is closing.");
}

const DESCRIPTORS: BrowserDescriptor[] = [
	{
		id: "chrome",
		name: "Google Chrome",
		family: "chromium",
		chromiumKeychainNames: ["Chrome", "Google Chrome"],
		roots: (c) => platformPaths(c, {
			win32: [joinEnv(c.env.LOCALAPPDATA, "Google", "Chrome", "User Data")],
			darwin: [path.join(c.homeDir, "Library", "Application Support", "Google", "Chrome")],
			linux: [path.join(configHome(c), "google-chrome")],
		}),
	},
	{
		id: "edge",
		name: "Microsoft Edge",
		family: "chromium",
		chromiumKeychainNames: ["Microsoft Edge"],
		roots: (c) => platformPaths(c, {
			win32: [joinEnv(c.env.LOCALAPPDATA, "Microsoft", "Edge", "User Data")],
			darwin: [path.join(c.homeDir, "Library", "Application Support", "Microsoft Edge")],
			linux: [path.join(configHome(c), "microsoft-edge")],
		}),
	},
	{
		id: "brave",
		name: "Brave",
		family: "chromium",
		chromiumKeychainNames: ["Brave", "Brave Browser"],
		roots: (c) => platformPaths(c, {
			win32: [joinEnv(c.env.LOCALAPPDATA, "BraveSoftware", "Brave-Browser", "User Data")],
			darwin: [path.join(c.homeDir, "Library", "Application Support", "BraveSoftware", "Brave-Browser")],
			linux: [path.join(configHome(c), "BraveSoftware", "Brave-Browser")],
		}),
	},
	{
		id: "chromium",
		name: "Chromium",
		family: "chromium",
		chromiumKeychainNames: ["Chromium"],
		roots: (c) => platformPaths(c, {
			win32: [joinEnv(c.env.LOCALAPPDATA, "Chromium", "User Data")],
			darwin: [path.join(c.homeDir, "Library", "Application Support", "Chromium")],
			linux: [path.join(configHome(c), "chromium")],
		}),
	},
	{
		id: "vivaldi",
		name: "Vivaldi",
		family: "chromium",
		chromiumKeychainNames: ["Vivaldi"],
		roots: (c) => platformPaths(c, {
			win32: [joinEnv(c.env.LOCALAPPDATA, "Vivaldi", "User Data")],
			darwin: [path.join(c.homeDir, "Library", "Application Support", "Vivaldi")],
			linux: [path.join(configHome(c), "vivaldi")],
		}),
	},
	{
		id: "arc",
		name: "Arc",
		family: "chromium",
		chromiumKeychainNames: ["Arc"],
		roots: (c) => platformPaths(c, {
			win32: [],
			darwin: [path.join(c.homeDir, "Library", "Application Support", "Arc", "User Data")],
			linux: [],
		}),
	},
	{
		id: "firefox",
		name: "Firefox",
		family: "firefox",
		roots: (c) => platformPaths(c, {
			win32: [joinEnv(c.env.APPDATA, "Mozilla", "Firefox")],
			darwin: [path.join(c.homeDir, "Library", "Application Support", "Firefox")],
			linux: [path.join(c.homeDir, ".mozilla", "firefox")],
		}),
	},
	{
		id: "zen",
		name: "Zen",
		family: "firefox",
		roots: (c) => platformPaths(c, {
			win32: [joinEnv(c.env.APPDATA, "zen")],
			darwin: [path.join(c.homeDir, "Library", "Application Support", "zen")],
			linux: [path.join(c.homeDir, ".zen")],
		}),
	},
];

function configHome(context: DiscoveryContext): string {
	return context.env.XDG_CONFIG_HOME || path.join(context.homeDir, ".config");
}

function joinEnv(root: string | undefined, ...parts: string[]): string {
	return root ? path.join(root, ...parts) : "";
}

function platformPaths(
	context: DiscoveryContext,
	paths: Record<"win32" | "darwin" | "linux", string[]>,
): string[] {
	if (context.platform === "win32") return paths.win32.filter(Boolean);
	if (context.platform === "darwin") return paths.darwin.filter(Boolean);
	return paths.linux.filter(Boolean);
}

function opaqueSourceId(kind: string, canonicalPath: string): string {
	return createHash("sha256").update(kind).update("\0").update(canonicalPath).digest("hex").slice(0, 32);
}

function contained(root: string, candidate: string): boolean {
	const relative = path.relative(root, candidate);
	return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
}

async function existingRealDirectory(candidate: string): Promise<string | null> {
	if (!candidate) return null;
	try {
		const metadata = await lstat(candidate);
		if (!metadata.isDirectory() || metadata.isSymbolicLink()) return null;
		return await realpath(candidate);
	} catch {
		return null;
	}
}

async function readSmallJSON(file: string, maxBytes: number): Promise<unknown> {
	const metadata = await lstat(file);
	if (!metadata.isFile() || metadata.isSymbolicLink() || metadata.size > maxBytes) {
		throw new Error("Browser metadata exceeds the size limit.");
	}
	const noFollow = "O_NOFOLLOW" in constants ? constants.O_NOFOLLOW : 0;
	const handle = await open(file, constants.O_RDONLY | noFollow);
	try {
		const opened = await handle.stat();
		if (!opened.isFile() || opened.size > maxBytes) throw new Error("Browser metadata exceeds the size limit.");
		return JSON.parse(await handle.readFile("utf8"));
	} finally {
		await handle.close().catch(() => undefined);
	}
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

async function discoverChromiumProfiles(descriptor: BrowserDescriptor, root: string): Promise<InternalSourceProfile[]> {
	const names = new Map<string, string>();
	try {
		const localState = await readSmallJSON(path.join(root, "Local State"), LOCAL_STATE_MAX_BYTES);
		if (isRecord(localState) && isRecord(localState.profile) && isRecord(localState.profile.info_cache)) {
			for (const [directory, raw] of Object.entries(localState.profile.info_cache)) {
				if (!isRecord(raw)) continue;
				const name = typeof raw.name === "string" ? raw.name.trim() : "";
				if (name) names.set(directory, name);
			}
		}
	} catch {
		// A missing or malformed Local State does not hide otherwise valid profiles.
	}
	const entries = await readdir(root, { withFileTypes: true }).catch(() => []);
	const directories = new Set<string>([
		...names.keys(),
		...entries
			.filter((entry) => entry.isDirectory() && (entry.name === "Default" || /^Profile \d+$/.test(entry.name)))
			.map((entry) => entry.name),
	]);
	const profiles: InternalSourceProfile[] = [];
	for (const directory of directories) {
		const profileRoot = await existingRealDirectory(path.join(root, directory));
		if (!profileRoot || !contained(root, profileRoot) || !(await hasImportableDatabase(profileRoot, "chromium"))) continue;
		profiles.push({
			id: opaqueSourceId(`${descriptor.id}:profile`, profileRoot),
			name: names.get(directory) || (directory === "Default" ? "Default" : directory),
			default: directory === "Default",
			root: profileRoot,
		});
	}
	return profiles.sort((a, b) => Number(b.default) - Number(a.default) || a.name.localeCompare(b.name));
}

async function discoverFirefoxProfiles(descriptor: BrowserDescriptor, root: string): Promise<InternalSourceProfile[]> {
	const profileParent = (await existingRealDirectory(path.join(root, "Profiles"))) ?? root;
	const entries = await readdir(profileParent, { withFileTypes: true }).catch(() => []);
	const profiles: InternalSourceProfile[] = [];
	for (const entry of entries) {
		if (!entry.isDirectory()) continue;
		const profileRoot = await existingRealDirectory(path.join(profileParent, entry.name));
		if (!profileRoot || !contained(root, profileRoot) || !(await hasImportableDatabase(profileRoot, "firefox"))) continue;
		const dot = entry.name.indexOf(".");
		const suffix = dot >= 0 ? entry.name.slice(dot + 1) : entry.name;
		profiles.push({
			id: opaqueSourceId(`${descriptor.id}:profile`, profileRoot),
			name: suffix || entry.name,
			default: /default|release/i.test(suffix),
			root: profileRoot,
		});
	}
	return profiles.sort((a, b) => Number(b.default) - Number(a.default) || a.name.localeCompare(b.name));
}

async function hasImportableDatabase(profileRoot: string, family: BrowserFamily): Promise<boolean> {
	const candidates =
		family === "chromium"
			? ["History", path.join("Network", "Cookies"), "Cookies"]
			: ["places.sqlite", "cookies.sqlite"];
	for (const relative of candidates) {
		try {
			const metadata = await lstat(path.join(profileRoot, relative));
			if (metadata.isFile() && !metadata.isSymbolicLink()) return true;
		} catch {
			// Continue looking for another supported database.
		}
	}
	return false;
}

function cookieCapability(
	family: BrowserFamily,
	platform: NodeJS.Platform,
): { support: BrowserImportSource["cookieSupport"]; reason: BrowserImportCookieSupportReason } {
	if (family === "firefox") return { support: "supported", reason: "firefox-plaintext" };
	return {
		support: "partial",
		reason: platform === "linux" ? "chromium-encryption-unsupported" : "chromium-encryption-partial",
	};
}

export class BrowserProfileImportService {
	private readonly context: DiscoveryContext;
	private readonly now: () => Date;
	private activeImport: Promise<BrowserImportResult> | null = null;
	private activeController: AbortController | null = null;
	private disposePromise: Promise<void> | null = null;
	private disposed = false;

	constructor(private readonly options: BrowserProfileImportOptions) {
		this.context = {
			platform: options.platform ?? process.platform,
			homeDir: options.homeDir ?? os.homedir(),
			env: options.env ?? process.env,
		};
		this.now = options.now ?? (() => new Date());
	}

	async initialize(): Promise<void> {
		if (this.disposed) throw new Error("Browser profile import is unavailable.");
		if (this.activeImport) throw new Error("Another browser import is already running.");
		await rm(this.stagingRoot(), { recursive: true, force: true });
	}

	dispose(): Promise<void> {
		if (this.disposePromise) return this.disposePromise;
		this.disposed = true;
		this.activeController?.abort();
		const activeImport = this.activeImport;
		this.disposePromise = (async () => {
			if (activeImport) await activeImport.catch(() => undefined);
			await rm(this.stagingRoot(), { recursive: true, force: true }).catch(() => undefined);
		})();
		return this.disposePromise;
	}

	private stagingRoot(): string {
		return path.join(this.options.stateDir, "browser-import-staging");
	}

	async discover(): Promise<BrowserImportDiscovery> {
		return { sources: (await this.discoverInternal()).map((source) => source.public) };
	}

	private async discoverInternal(): Promise<InternalSource[]> {
		const sources: InternalSource[] = [];
		for (const descriptor of DESCRIPTORS) {
			for (const candidate of descriptor.roots(this.context)) {
				const root = await existingRealDirectory(candidate);
				if (!root) continue;
				const profiles =
					descriptor.family === "chromium"
						? await discoverChromiumProfiles(descriptor, root)
						: await discoverFirefoxProfiles(descriptor, root);
				if (profiles.length === 0) continue;
				const capability = cookieCapability(descriptor.family, this.context.platform);
				sources.push({
					descriptor,
					root,
					profiles,
					public: {
						id: opaqueSourceId(`${descriptor.id}:source`, root),
						name: descriptor.name,
						family: descriptor.family,
						profiles: profiles.map(({ id, name, default: isDefault }) => ({ id, name, default: isDefault })),
						cookieSupport: capability.support,
						cookieSupportReason: capability.reason,
						historySupport: true,
					},
				});
				break;
			}
		}
		return sources;
	}

	async import(
		rawRequest: BrowserImportRequest,
		onProgress: (progress: BrowserImportProgress) => void,
	): Promise<BrowserImportResult> {
		if (this.disposed) throw new Error("Browser profile import is unavailable.");
		if (this.activeImport) throw new Error("Another browser import is already running.");
		const controller = new AbortController();
		const pending = this.runImport(rawRequest, onProgress, controller.signal);
		this.activeController = controller;
		this.activeImport = pending;
		try {
			return await pending;
		} finally {
			if (this.activeImport === pending) {
				this.activeImport = null;
				this.activeController = null;
			}
		}
	}

	private async runImport(
		rawRequest: BrowserImportRequest,
		onProgress: (progress: BrowserImportProgress) => void,
		signal: AbortSignal,
	): Promise<BrowserImportResult> {
		const staging = path.join(this.stagingRoot(), randomUUID());
		let sourceRoots: string[] = [];
		try {
			throwIfImportAborted(signal);
			const sources = await this.discoverInternal();
			throwIfImportAborted(signal);
			sourceRoots = sources.map((source) => source.root);
			const request = validateRequest(rawRequest, sources, this.options.profileStore.profiles);
			const source = sources.find((candidate) => candidate.public.id === request.sourceId)!;
			const selected = request.profileIds.map((id) => source.profiles.find((profile) => profile.id === id)!);
			onProgress({ requestId: request.requestId, phase: "preparing", completed: 0, total: selected.length });
			await mkdir(staging, { recursive: true, mode: 0o700 });
			const budget = new SourceBudget();
			const decryptor = await ChromiumCookieDecryptor.create(source, this.context.platform);
			const readData: ReadProfileData[] = [];
			for (const [index, profile] of selected.entries()) {
				throwIfImportAborted(signal);
				readData.push(await readProfileData(source, profile, request, staging, budget, decryptor, this.now()));
				throwIfImportAborted(signal);
				onProgress({ requestId: request.requestId, phase: "reading", completed: index + 1, total: selected.length });
			}
			return await this.commitImport(source, request, readData, onProgress, signal);
		} catch (error) {
			throw redactSourcePaths(error, sourceRoots);
		} finally {
			await rm(staging, { recursive: true, force: true }).catch(() => undefined);
		}
	}

	private async commitImport(
		source: InternalSource,
		request: BrowserImportRequest,
		readData: ReadProfileData[],
		onProgress: (progress: BrowserImportProgress) => void,
		signal: AbortSignal,
	): Promise<BrowserImportResult> {
		const destination = request.destination;
		const groups =
			destination.mode === "merge"
				? [{ name: destination.name, data: readData }]
				: readData.map((data) => ({ name: destination.names[data.profile.id]!, data: [data] }));
		const created: BrowserProfile[] = [];
		const results: BrowserImportResultEntry[] = [];
		try {
			for (const [index, group] of groups.entries()) {
				throwIfImportAborted(signal);
				const profile = await this.options.profileStore.createProfile(group.name);
				created.push(profile);
				throwIfImportAborted(signal);
				const history = group.data.flatMap((data) => data.history);
				const cookieSelection = dedupeCookies(group.data.flatMap((data) => data.cookies));
				const historyOutcome = request.includeHistory
					? await this.options.historyStore.mergeImportedEntries(profile.id, history)
					: { imported: 0, truncated: 0 };
				throwIfImportAborted(signal);
				const cookieOutcome = request.includeCookies
					? await setCookies(this.options.fromPartition(browserProfilePartition(profile.id)), cookieSelection.cookies, signal)
					: { imported: 0, skipped: 0, warnings: [] };
				throwIfImportAborted(signal);
				const limitWarnings: BrowserImportWarning[] = [
					...(cookieSelection.truncated > 0
						? [{ code: "cookie-limit-truncated" as const, count: cookieSelection.truncated }]
						: []),
					...(historyOutcome.truncated > 0
						? [{ code: "history-limit-truncated" as const, count: historyOutcome.truncated }]
						: []),
				];
				results.push({
					sourceProfileNames: group.data.map((data) => data.profile.name),
					destinationProfile: profile,
					importedCookies: cookieOutcome.imported,
					skippedCookies: group.data.reduce((total, data) => total + data.skippedCookies, 0)
						+ cookieSelection.truncated
						+ cookieOutcome.skipped,
					importedHistoryEntries: historyOutcome.imported,
					warnings: mergeWarnings([
						...group.data.flatMap((data) => data.warnings),
						...limitWarnings,
						...cookieOutcome.warnings,
					]),
				});
				onProgress({ requestId: request.requestId, phase: "importing", completed: index + 1, total: groups.length });
			}
			return { sourceName: source.public.name, entries: results };
		} catch (error) {
			const cleanupFailures: string[] = [];
			for (const profile of created.reverse()) {
				let importedSession: ImportSession | undefined;
				try {
					importedSession = this.options.fromPartition(browserProfilePartition(profile.id));
				} catch {
					cleanupFailures.push(`${profile.name}: browser storage cleanup could not start`);
				}
				const cleanup = await Promise.allSettled([
					this.options.historyStore.clear(profile.id),
					...(importedSession ? [importedSession.clearStorageData(), importedSession.clearCache()] : []),
				]);
				if (cleanup.some((outcome) => outcome.status === "rejected")) {
					cleanupFailures.push(`${profile.name}: imported data could not be fully removed`);
					continue;
				}
				if (!importedSession) continue;
				try {
					await this.options.profileStore.deleteProfile(profile.id);
				} catch {
					cleanupFailures.push(`${profile.name}: profile record could not be removed`);
				}
			}
			if (cleanupFailures.length > 0) {
				const original = error instanceof Error ? error.message : "Browser data could not be imported.";
				throw new Error(
					`${original} Cleanup was incomplete (${cleanupFailures.join("; ")}). `
					+ "The affected profiles remain in Browser settings so you can retry deleting them.",
				);
			}
			throw error;
		}
	}
}

function redactSourcePaths(error: unknown, sourceRoots: string[]): Error {
	let message = error instanceof Error ? error.message : "Browser data could not be imported.";
	for (const root of sourceRoots.sort((a, b) => b.length - a.length)) {
		for (const variant of new Set([root, root.replaceAll("\\", "/")])) {
			message = message.replaceAll(variant, "<browser source>");
		}
	}
	return new Error(message || "Browser data could not be imported.");
}

function validateRequest(
	request: BrowserImportRequest,
	sources: InternalSource[],
	existingProfiles: BrowserProfile[],
): BrowserImportRequest {
	if (!isRecord(request) || !isBrowserProfileId(request.requestId)) throw new Error("Browser import request ID is invalid.");
	if (typeof request.sourceId !== "string" || !SOURCE_ID_PATTERN.test(request.sourceId)) throw new Error("Browser import source is invalid.");
	const source = sources.find((candidate) => candidate.public.id === request.sourceId);
	if (!source) throw new Error("The selected browser source is no longer available.");
	if (
		!Array.isArray(request.profileIds) ||
		request.profileIds.length === 0 ||
		request.profileIds.length > BROWSER_IMPORT_MAX_SOURCE_PROFILES ||
		new Set(request.profileIds).size !== request.profileIds.length ||
		request.profileIds.some((id) => typeof id !== "string" || !source.profiles.some((profile) => profile.id === id))
	) {
		throw new Error("Selected browser profiles are invalid.");
	}
	if (request.includeCookies !== true && request.includeHistory !== true) throw new Error("Select cookies, history, or both.");
	const destination = request.destination;
	if (!isRecord(destination) || (destination.mode !== "separate" && destination.mode !== "merge")) {
		throw new Error("Browser import destination is invalid.");
	}
	let names: string[];
	if (destination.mode === "merge") {
		const name = normalizeBrowserProfileName(destination.name);
		if (!name || name !== destination.name) throw new Error("Destination profile name is invalid.");
		names = [name];
	} else {
		if (!isRecord(destination.names)) throw new Error("Destination profile names are invalid.");
		names = request.profileIds.map((id) => {
			if (!Object.hasOwn(destination.names, id)) throw new Error("A destination profile name is missing.");
			const name = normalizeBrowserProfileName(destination.names[id]);
			if (!name || name !== destination.names[id]) throw new Error("Destination profile name is invalid.");
			return name;
		});
	}
	const normalizedNames = names.map((name) => name.toLowerCase());
	if (new Set(normalizedNames).size !== normalizedNames.length) throw new Error("Destination profile names must be unique.");
	if (existingProfiles.length + names.length > BROWSER_PROFILE_MAX_COUNT) {
		throw new Error("Not enough browser profile slots are available for this import.");
	}
	const existing = new Set(existingProfiles.map((profile) => profile.name.toLowerCase()));
	if (normalizedNames.some((name) => existing.has(name))) throw new Error("A destination browser profile already exists.");
	return request;
}

async function readProfileData(
	source: InternalSource,
	profile: InternalSourceProfile,
	request: BrowserImportRequest,
	staging: string,
	budget: SourceBudget,
	decryptor: ChromiumCookieDecryptor,
	now: Date,
): Promise<ReadProfileData> {
	const warnings: BrowserImportWarning[] = [];
	let cookies: ImportedCookie[] = [];
	let history: BrowserHistoryEntry[] = [];
	let skippedCookies = 0;
	if (request.includeCookies) {
		const cookieDatabase = await findDatabase(profile.root, source.descriptor.family === "chromium"
			? [path.join("Network", "Cookies"), "Cookies"]
			: ["cookies.sqlite"]);
		if (!cookieDatabase) {
			warnings.push({ code: "cookie-database-missing" });
		} else {
			const snapshot = await snapshotSQLite(cookieDatabase, profile.root, staging, budget);
			const outcome = source.descriptor.family === "chromium"
				? readChromiumCookies(snapshot, decryptor, now)
				: readFirefoxCookies(snapshot, now);
			if (!outcome) {
				warnings.push({ code: "cookie-database-missing" });
			} else {
				cookies = outcome.cookies;
				skippedCookies += outcome.skipped;
				warnings.push(...outcome.warnings);
			}
		}
	}
	if (request.includeHistory) {
		const historyDatabase = await findDatabase(profile.root, source.descriptor.family === "chromium" ? ["History"] : ["places.sqlite"]);
		if (!historyDatabase) {
			warnings.push({ code: "history-database-missing" });
		} else {
			const snapshot = await snapshotSQLite(historyDatabase, profile.root, staging, budget);
			const outcome = source.descriptor.family === "chromium"
				? readChromiumHistory(snapshot)
				: readFirefoxHistory(snapshot);
			history = outcome.history;
			warnings.push(...outcome.warnings);
		}
	}
	return { profile, cookies, history, warnings: mergeWarnings(warnings), skippedCookies };
}

async function findDatabase(profileRoot: string, relatives: string[]): Promise<string | null> {
	for (const relative of relatives) {
		const candidate = path.join(profileRoot, relative);
		try {
			const metadata = await lstat(candidate);
			if (!metadata.isFile() || metadata.isSymbolicLink()) continue;
			const canonical = await realpath(candidate);
			if (contained(profileRoot, canonical)) return canonical;
		} catch {
			// Try the next known location.
		}
	}
	return null;
}

async function snapshotSQLite(
	database: string,
	profileRoot: string,
	staging: string,
	budget: SourceBudget,
): Promise<string> {
	const destination = path.join(staging, `${randomUUID()}-${path.basename(database)}`);
	const canonical = await preflightContainedFile(database, profileRoot, SOURCE_FILE_MAX_BYTES, budget);
	for (const suffix of ["-wal", "-shm"]) {
		try {
			await preflightContainedFile(`${database}${suffix}`, profileRoot, SOURCE_SIDECAR_MAX_BYTES, budget);
		} catch (error) {
			if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
		}
	}
	const source = new Database(canonical, { readonly: true, fileMustExist: true, timeout: 5_000 });
	try {
		source.pragma("query_only = ON");
		await source.backup(destination);
		const output = await stat(destination);
		if (!output.isFile() || output.size > SOURCE_FILE_MAX_BYTES + SOURCE_SIDECAR_MAX_BYTES) {
			throw new Error("Browser source database exceeds the snapshot size limit.");
		}
		await chmod(destination, 0o600);
		return destination;
	} catch (error) {
		await rm(destination, { force: true }).catch(() => undefined);
		throw error;
	} finally {
		source.close();
	}
}

async function preflightContainedFile(
	source: string,
	root: string,
	maxBytes: number,
	budget: SourceBudget,
): Promise<string> {
	const metadata = await lstat(source);
	if (!metadata.isFile() || metadata.isSymbolicLink() || metadata.size > maxBytes) {
		throw new Error("Browser source database is invalid or exceeds the size limit.");
	}
	const canonical = await realpath(source);
	if (!contained(root, canonical)) throw new Error("Browser source database is outside its profile directory.");
	const canonicalMetadata = await lstat(canonical);
	if (!canonicalMetadata.isFile() || canonicalMetadata.isSymbolicLink() || canonicalMetadata.size > maxBytes) {
		throw new Error("Browser source database exceeds the size limit.");
	}
	budget.consume(canonicalMetadata.size);
	return canonical;
}

function withReadOnlyDatabase<T>(file: string, read: (database: Database) => T): T {
	const database = new Database(file, { readonly: true, fileMustExist: true, timeout: 5_000 });
	try {
		database.pragma("query_only = ON");
		return read(database);
	} finally {
		database.close();
	}
}

function readFirefoxCookies(file: string, now: Date): { cookies: ImportedCookie[]; skipped: number; warnings: BrowserImportWarning[] } | null {
	return withReadOnlyDatabase(file, (database) => {
		if (!hasTable(database, "moz_cookies")) return null;
		const columns = tableColumns(database, "moz_cookies");
		const hasOriginAttributes = columns.has("originAttributes");
		const hasSameSite = columns.has("sameSite");
		const isolationWhere = hasOriginAttributes ? "WHERE COALESCE(originAttributes, '') = ''" : "";
		const total = countRows(database, "moz_cookies");
		const eligible = countRows(database, "moz_cookies", isolationWhere);
		const isolatedSkipped = total - eligible;
		const truncated = Math.max(0, eligible - BROWSER_IMPORT_MAX_COOKIES);
		const orderBy = cookieOrder(columns, ["lastAccessed", "creationTime"]);
		const rows = database.prepare(`
			SELECT host, name, value, path, expiry, isSecure, isHttpOnly, ${hasSameSite ? "sameSite" : "-1 AS sameSite"}
			FROM moz_cookies
			${isolationWhere}
			ORDER BY ${orderBy}
			LIMIT ${BROWSER_IMPORT_MAX_COOKIES}
		`).all() as Record<string, unknown>[];
		const normalized = normalizeCookieRows(rows.map((row) => ({
			domain: row.host,
			name: row.name,
			value: row.value,
			path: row.path,
			expires: numberValue(row.expiry),
			secure: booleanValue(row.isSecure),
			httpOnly: booleanValue(row.isHttpOnly),
			sameSite: firefoxSameSite(numberValue(row.sameSite)),
		})), now);
		if (isolatedSkipped > 0) normalized.warnings.push({ code: "isolated-cookies-skipped", count: isolatedSkipped });
		if (truncated > 0) normalized.warnings.push({ code: "cookie-limit-truncated", count: truncated });
		if (!hasSameSite && rows.length > 0) normalized.warnings.push({ code: "cookie-attributes-defaulted", count: rows.length });
		normalized.skipped += isolatedSkipped + truncated;
		return normalized;
	});
}

function readChromiumCookies(
	file: string,
	decryptor: ChromiumCookieDecryptor,
	now: Date,
): { cookies: ImportedCookie[]; skipped: number; warnings: BrowserImportWarning[] } {
	return withReadOnlyDatabase(file, (database) => {
		requireTable(database, "cookies", "The selected Chromium profile does not contain supported cookie data.");
		const columns = tableColumns(database, "cookies");
		const hasPartitionKey = columns.has("top_frame_site_key");
		const isolationWhere = hasPartitionKey ? "WHERE COALESCE(top_frame_site_key, '') = ''" : "";
		const total = countRows(database, "cookies");
		const eligible = countRows(database, "cookies", isolationWhere);
		const isolatedSkipped = total - eligible;
		const truncated = Math.max(0, eligible - BROWSER_IMPORT_MAX_COOKIES);
		const orderBy = cookieOrder(columns, ["last_access_utc", "creation_utc"]);
		const rows = database.prepare(`
			SELECT host_key, name, value, encrypted_value, path, expires_utc, is_secure, is_httponly, samesite
			FROM cookies
			${isolationWhere}
			ORDER BY ${orderBy}
			LIMIT ${BROWSER_IMPORT_MAX_COOKIES}
		`).all() as Record<string, unknown>[];
		let encryptedSkipped = 0;
		const raw: Array<Record<string, unknown>> = [];
		for (const row of rows) {
			const domain = stringValue(row.host_key);
			let value = stringValue(row.value);
			if (!value) {
				const encrypted = Buffer.isBuffer(row.encrypted_value) ? row.encrypted_value : Buffer.alloc(0);
				value = decryptor.decrypt(encrypted, domain) ?? "";
				if (!value && encrypted.length > 0) {
					encryptedSkipped += 1;
					continue;
				}
			}
			raw.push({
				domain,
				name: row.name,
				value,
				path: row.path,
				expires: chromiumTimestamp(numberValue(row.expires_utc)),
				secure: booleanValue(row.is_secure),
				httpOnly: booleanValue(row.is_httponly),
				sameSite: chromiumSameSite(numberValue(row.samesite)),
			});
		}
		const normalized = normalizeCookieRows(raw, now);
		if (encryptedSkipped > 0) normalized.warnings.push({ code: "encrypted-cookies-skipped", count: encryptedSkipped });
		if (isolatedSkipped > 0) normalized.warnings.push({ code: "isolated-cookies-skipped", count: isolatedSkipped });
		if (truncated > 0) normalized.warnings.push({ code: "cookie-limit-truncated", count: truncated });
		normalized.skipped += encryptedSkipped + isolatedSkipped + truncated;
		return normalized;
	});
}

function normalizeCookieRows(
	rows: Array<Record<string, unknown>>,
	now: Date,
): { cookies: ImportedCookie[]; skipped: number; warnings: BrowserImportWarning[] } {
	const cookies: ImportedCookie[] = [];
	let expired = 0;
	let invalid = 0;
	for (const row of rows) {
		const domain = stringValue(row.domain).trim().toLowerCase();
		const hostname = domain.replace(/^\.+/, "");
		if (!hostname) {
			invalid += 1;
			continue;
		}
		const name = stringValue(row.name);
		const value = stringValue(row.value);
		if (!name) {
			invalid += 1;
			continue;
		}
		const expirationDate = typeof row.expires === "number" && Number.isFinite(row.expires) && row.expires > 0 ? row.expires : undefined;
		if (expirationDate !== undefined && expirationDate <= now.getTime() / 1_000) {
			expired += 1;
			continue;
		}
		const cookiePath = stringValue(row.path) || "/";
		const secure = row.secure === true;
		const sameSite = row.sameSite as CookiesSetDetails["sameSite"];
		try {
			const url = new URL(`${secure ? "https" : "http"}://${hostname}${cookiePath.startsWith("/") ? cookiePath : `/${cookiePath}`}`).href;
			cookies.push({
				url,
				name,
				value,
				domain,
				path: cookiePath,
				secure,
				httpOnly: row.httpOnly === true,
				...(sameSite ? { sameSite } : {}),
				...(expirationDate !== undefined ? { expirationDate } : {}),
				dedupeKey: `${name}\0${domain}\0${cookiePath}`,
			});
		} catch {
			invalid += 1;
		}
	}
	const warnings: BrowserImportWarning[] = [];
	if (expired > 0) warnings.push({ code: "expired-cookies-skipped", count: expired });
	if (invalid > 0) warnings.push({ code: "invalid-cookies-skipped", count: invalid });
	return { cookies, skipped: expired + invalid, warnings };
}

function readFirefoxHistory(file: string): { history: BrowserHistoryEntry[]; warnings: BrowserImportWarning[] } {
	return withReadOnlyDatabase(file, (database) => {
		requireTable(database, "moz_places", "The selected Firefox profile does not contain supported history data.");
		const eligible = countRows(database, "moz_places", "WHERE url LIKE 'http%'");
		const truncated = Math.max(0, eligible - BROWSER_IMPORT_MAX_HISTORY_ENTRIES);
		const history = (database.prepare(`
			SELECT url, title, visit_count, last_visit_date
			FROM moz_places
			WHERE url LIKE 'http%'
			ORDER BY last_visit_date DESC, rowid DESC
			LIMIT ${BROWSER_IMPORT_MAX_HISTORY_ENTRIES}
		`).all() as Record<string, unknown>[]).flatMap((row) => {
			const timestamp = numberValue(row.last_visit_date) / 1_000;
			return normalizeHistoryRow(row.url, row.title, row.visit_count, timestamp);
		});
		return {
			history,
			warnings: truncated > 0 ? [{ code: "history-limit-truncated", count: truncated }] : [],
		};
	});
}

function readChromiumHistory(file: string): { history: BrowserHistoryEntry[]; warnings: BrowserImportWarning[] } {
	return withReadOnlyDatabase(file, (database) => {
		requireTable(database, "urls", "The selected Chromium profile does not contain supported history data.");
		const eligible = countRows(database, "urls", "WHERE url LIKE 'http%'");
		const truncated = Math.max(0, eligible - BROWSER_IMPORT_MAX_HISTORY_ENTRIES);
		const history = (database.prepare(`
			SELECT url, title, visit_count, last_visit_time
			FROM urls
			WHERE url LIKE 'http%'
			ORDER BY last_visit_time DESC, rowid DESC
			LIMIT ${BROWSER_IMPORT_MAX_HISTORY_ENTRIES}
		`).all() as Record<string, unknown>[]).flatMap((row) =>
			normalizeHistoryRow(row.url, row.title, row.visit_count, chromiumTimestamp(numberValue(row.last_visit_time)) * 1_000),
		);
		return {
			history,
			warnings: truncated > 0 ? [{ code: "history-limit-truncated", count: truncated }] : [],
		};
	});
}

function requireTable(database: Database, table: string, message: string): void {
	if (!hasTable(database, table)) throw new Error(message);
}

function hasTable(database: Database, table: string): boolean {
	return Boolean(database.prepare("SELECT 1 AS present FROM sqlite_master WHERE type = 'table' AND name = ?").get(table));
}

function tableColumns(database: Database, table: "cookies" | "moz_cookies"): Set<string> {
	return new Set(
		(database.prepare(`PRAGMA table_info(${table})`).all() as Array<{ name?: unknown }>)
			.map((row) => stringValue(row.name))
			.filter(Boolean),
	);
}

function countRows(
	database: Database,
	table: "cookies" | "moz_cookies" | "urls" | "moz_places",
	where = "",
): number {
	const row = database.prepare(`SELECT COUNT(*) AS count FROM ${table} ${where}`).get() as { count?: unknown };
	return numberValue(row.count);
}

function cookieOrder(columns: Set<string>, preferredNewestColumns: string[]): string {
	const newest = preferredNewestColumns.filter((column) => columns.has(column));
	return [...newest.map((column) => `${column} DESC`), "rowid DESC"].join(", ");
}

function normalizeHistoryRow(
	rawURL: unknown,
	rawTitle: unknown,
	rawVisitCount: unknown,
	timestampMs: number,
): BrowserHistoryEntry[] {
	try {
		const url = new URL(stringValue(rawURL));
		if (url.protocol !== "http:" && url.protocol !== "https:") return [];
		const title = stringValue(rawTitle).trim().slice(0, 512);
		const lastVisited = Number.isFinite(timestampMs) && timestampMs > 0 ? new Date(timestampMs).toISOString() : new Date(0).toISOString();
		return [{
			url: url.href,
			...(title ? { title } : {}),
			lastVisited,
			visitCount: Math.max(1, Math.trunc(numberValue(rawVisitCount)) || 1),
		}];
	} catch {
		return [];
	}
}

function chromiumTimestamp(rawMicroseconds: number): number {
	if (!Number.isFinite(rawMicroseconds) || rawMicroseconds <= 0) return 0;
	return rawMicroseconds / 1_000_000 - 11_644_473_600;
}

function firefoxSameSite(value: number): CookiesSetDetails["sameSite"] {
	if (value === 2) return "strict";
	if (value === 1) return "lax";
	if (value === 0) return "no_restriction";
	return "unspecified";
}

function chromiumSameSite(value: number): CookiesSetDetails["sameSite"] {
	if (value === 2) return "strict";
	if (value === 1) return "lax";
	if (value === 0) return "no_restriction";
	return "unspecified";
}

function stringValue(value: unknown): string {
	return typeof value === "string" ? value : "";
}

function numberValue(value: unknown): number {
	return typeof value === "number" ? value : typeof value === "bigint" ? Number(value) : 0;
}

function booleanValue(value: unknown): boolean {
	return value === true || value === 1 || value === 1n;
}

function dedupeCookies(cookies: ImportedCookie[]): { cookies: ImportedCookie[]; truncated: number } {
	const byKey = new Map<string, ImportedCookie>();
	for (const cookie of cookies) {
		// profileIds order is user-selected order. For an identity collision,
		// retain the first selected profile's cookie rather than silently mixing
		// account state based on expiration timestamps.
		if (!byKey.has(cookie.dedupeKey)) byKey.set(cookie.dedupeKey, cookie);
	}
	const unique = [...byKey.values()].sort(
		(a, b) => (b.expirationDate ?? 0) - (a.expirationDate ?? 0) || a.dedupeKey.localeCompare(b.dedupeKey),
	);
	return {
		cookies: unique.slice(0, BROWSER_IMPORT_MAX_COOKIES),
		truncated: Math.max(0, unique.length - BROWSER_IMPORT_MAX_COOKIES),
	};
}

async function setCookies(
	session: ImportSession,
	cookies: ImportedCookie[],
	signal: AbortSignal,
): Promise<{ imported: number; skipped: number; warnings: BrowserImportWarning[] }> {
	let imported = 0;
	let skipped = 0;
	for (const cookie of cookies) {
		throwIfImportAborted(signal);
		const { dedupeKey: _dedupeKey, ...details } = cookie;
		try {
			await session.cookies.set(details);
			throwIfImportAborted(signal);
			imported += 1;
		} catch {
			skipped += 1;
		}
	}
	return {
		imported,
		skipped,
		warnings: skipped > 0 ? [{ code: "cookie-write-failed", count: skipped }] : [],
	};
}

function mergeWarnings(warnings: BrowserImportWarning[]): BrowserImportWarning[] {
	const counts = new Map<BrowserImportWarning["code"], number | undefined>();
	for (const warning of warnings) {
		const current = counts.get(warning.code);
		counts.set(warning.code, warning.count === undefined ? current : (current ?? 0) + warning.count);
	}
	return [...counts].map(([code, count]) => ({ code, ...(count === undefined ? {} : { count }) }));
}

class ChromiumCookieDecryptor {
	private constructor(
		private readonly platform: NodeJS.Platform,
		private readonly key: Buffer | null,
	) {}

	static async create(source: InternalSource, platform: NodeJS.Platform): Promise<ChromiumCookieDecryptor> {
		if (source.descriptor.family !== "chromium") return new ChromiumCookieDecryptor(platform, null);
		if (platform === "win32") {
			try {
				const localState = await readSmallJSON(path.join(source.root, "Local State"), LOCAL_STATE_MAX_BYTES);
				if (!isRecord(localState) || !isRecord(localState.os_crypt) || typeof localState.os_crypt.encrypted_key !== "string") {
					return new ChromiumCookieDecryptor(platform, null);
				}
				const wrapped = Buffer.from(localState.os_crypt.encrypted_key, "base64");
				if (!wrapped.subarray(0, 5).equals(Buffer.from("DPAPI"))) return new ChromiumCookieDecryptor(platform, null);
				return new ChromiumCookieDecryptor(platform, await windowsDPAPIUnprotect(wrapped.subarray(5)));
			} catch {
				return new ChromiumCookieDecryptor(platform, null);
			}
		}
		if (platform === "darwin") {
			for (const name of source.descriptor.chromiumKeychainNames ?? []) {
				try {
					const { stdout } = await execFileAsync(
						"security",
						["find-generic-password", "-w", "-s", `${name} Safe Storage`],
						{ timeout: 10_000, maxBuffer: 16 * 1024 },
					);
					const password = stdout.trim();
					if (password) return new ChromiumCookieDecryptor(platform, Buffer.from(password));
				} catch {
					// Try the next legitimate Keychain service name.
				}
			}
		}
		return new ChromiumCookieDecryptor(platform, null);
	}

	decrypt(encrypted: Buffer, host: string): string | null {
		if (encrypted.length === 0 || !this.key) return null;
		if (this.platform === "win32") return decryptWindowsChromiumCookie(encrypted, this.key, host);
		if (this.platform === "darwin") return decryptMacChromiumCookie(encrypted, this.key, host);
		return null;
	}
}

export function decryptWindowsChromiumCookie(encrypted: Buffer, key: Buffer, host: string): string | null {
	if (encrypted.subarray(0, 3).toString() !== "v10" && encrypted.subarray(0, 3).toString() !== "v11") return null;
	if (encrypted.length < 3 + 12 + 16) return null;
	try {
		const nonce = encrypted.subarray(3, 15);
		const payload = encrypted.subarray(15);
		const ciphertext = payload.subarray(0, -16);
		const authTag = payload.subarray(-16);
		const decipher = createDecipheriv("aes-256-gcm", key, nonce);
		decipher.setAuthTag(authTag);
		return stripChromiumCookieHostDigest(
			Buffer.concat([decipher.update(ciphertext), decipher.final()]),
			host,
		).toString("utf8");
	} catch {
		return null;
	}
}

function decryptMacChromiumCookie(encrypted: Buffer, password: Buffer, host: string): string | null {
	const prefix = encrypted.subarray(0, 3).toString();
	if (prefix !== "v10" && prefix !== "v11") return null;
	try {
		const key = pbkdf2Sync(password, "saltysalt", 1_003, 16, "sha1");
		const decipher = createDecipheriv("aes-128-cbc", key, Buffer.alloc(16, 0x20));
		const plaintext = Buffer.concat([decipher.update(encrypted.subarray(3)), decipher.final()]);
		return stripChromiumCookieHostDigest(plaintext, host).toString("utf8");
	} catch {
		return null;
	}
}

function stripChromiumCookieHostDigest(plaintext: Buffer, host: string): Buffer {
	// Chromium cookie DB v24 prefixes encrypted values with SHA-256(host_key)
	// and verifies/removes it after platform decryption. Older databases do not,
	// so only strip an exact digest match.
	const hostDigest = createHash("sha256").update(host).digest();
	return plaintext.subarray(0, hostDigest.length).equals(hostDigest)
		? plaintext.subarray(hostDigest.length)
		: plaintext;
}

async function windowsDPAPIUnprotect(input: Buffer): Promise<Buffer | null> {
	const script = [
		"Add-Type -AssemblyName System.Security",
		"$raw = [Console]::In.ReadToEnd()",
		"$bytes = [Convert]::FromBase64String($raw)",
		"$plain = [Security.Cryptography.ProtectedData]::Unprotect($bytes, $null, [Security.Cryptography.DataProtectionScope]::CurrentUser)",
		"[Console]::Out.Write([Convert]::ToBase64String($plain))",
	].join("; ");
	return new Promise((resolve) => {
		const child = spawn("powershell.exe", ["-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script], {
			stdio: ["pipe", "pipe", "ignore"],
			windowsHide: true,
		});
		let stdout = "";
		const timer = setTimeout(() => child.kill(), 10_000);
		child.stdout.setEncoding("utf8");
		child.stdout.on("data", (chunk: string) => {
			if (stdout.length < 32 * 1024) stdout += chunk;
		});
		child.once("error", () => {
			clearTimeout(timer);
			resolve(null);
		});
		child.once("close", (code) => {
			clearTimeout(timer);
			if (code !== 0) return resolve(null);
			try {
				resolve(Buffer.from(stdout.trim(), "base64"));
			} catch {
				resolve(null);
			}
		});
		child.stdin.end(input.toString("base64"));
	});
}
