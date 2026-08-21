import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { randomUUID } from "node:crypto";
import {
	BROWSER_PROFILE_MAX_BINDINGS,
	BROWSER_PROFILE_MAX_COUNT,
	BROWSER_PROFILE_REGISTRY_FILE_NAME,
	BROWSER_PROFILE_REGISTRY_VERSION,
	browserProfilePartition,
	isBrowserProfileId,
	isValidBrowserProfileSessionId,
	normalizeBrowserProfileName,
	type BrowserProfile,
	type BrowserProfileBinding,
	type BrowserProfileListState,
	type BrowserProfileRegistry,
	type BrowserProfileStoreError as BrowserProfileStoreErrorInfo,
} from "../shared/browser-profiles";

export { BROWSER_PROFILE_REGISTRY_FILE_NAME };

export class BrowserProfileStoreError extends Error {
	readonly code: BrowserProfileStoreErrorInfo["code"] | "BROWSER_PROFILE_ACTIVE" | "BROWSER_PROFILE_NOT_FOUND" | "BROWSER_PROFILE_NAME_TAKEN" | "BROWSER_PROFILE_LIMIT" | "BROWSER_PROFILE_BINDING_LIMIT" | "BROWSER_PROFILE_OPERATION_IN_PROGRESS" | "INVALID_ARGUMENT";

	constructor(
		code: BrowserProfileStoreError["code"],
		message: string,
	) {
		super(message);
		this.name = "BrowserProfileStoreError";
		this.code = code;
	}
}

export type BrowserProfileStoreOptions = {
	stateDir: string;
	now?: () => Date;
};

type ProfileOperation<T> = () => Promise<T>;

const DEFAULT_REGISTRY: BrowserProfileRegistry = {
	version: BROWSER_PROFILE_REGISTRY_VERSION,
	profiles: [],
	bindings: {},
};

function emptyRegistry(): BrowserProfileRegistry {
	return { version: DEFAULT_REGISTRY.version, profiles: [], bindings: {} };
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validTimestamp(value: unknown): value is string {
	return typeof value === "string" && value.length > 0 && Number.isFinite(Date.parse(value));
}

function invalidRegistry(message: string): BrowserProfileStoreError {
	return new BrowserProfileStoreError("BROWSER_PROFILE_STORE_CORRUPT", message);
}

function parseRegistry(raw: unknown): BrowserProfileRegistry {
	if (!isRecord(raw) || raw.version !== BROWSER_PROFILE_REGISTRY_VERSION) {
		throw invalidRegistry("The browser profile registry has an unsupported version.");
	}
	if (!Array.isArray(raw.profiles) || raw.profiles.length > BROWSER_PROFILE_MAX_COUNT) {
		throw invalidRegistry("The browser profile registry has too many or invalid profiles.");
	}
	if (!isRecord(raw.bindings)) {
		throw invalidRegistry("The browser profile registry has invalid session bindings.");
	}

	const profiles: BrowserProfile[] = [];
	const profileIds = new Set<string>();
	const profileNames = new Set<string>();
	for (const value of raw.profiles) {
		if (!isRecord(value)) throw invalidRegistry("The browser profile registry contains an invalid profile.");
		const id = value.id;
		const name = value.name;
		const normalizedNameValue = normalizeBrowserProfileName(name);
		const createdAt = value.createdAt;
		const updatedAt = value.updatedAt;
		if (
			!isBrowserProfileId(id) ||
			id !== id.toLowerCase() ||
			normalizedNameValue === null ||
			normalizedNameValue !== name ||
			!validTimestamp(createdAt) ||
			!validTimestamp(updatedAt)
		) {
			throw invalidRegistry("The browser profile registry contains an invalid profile record.");
		}
		const normalizedName = normalizedNameValue.toLowerCase();
		if (profileIds.has(id) || profileNames.has(normalizedName)) {
			throw invalidRegistry("The browser profile registry contains duplicate profile IDs or names.");
		}
		profileIds.add(id);
		profileNames.add(normalizedName);
		profiles.push({ id, name, createdAt, updatedAt });
	}

	const bindings: Record<string, BrowserProfileBinding> = {};
	const bindingEntries = Object.entries(raw.bindings);
	if (bindingEntries.length > BROWSER_PROFILE_MAX_BINDINGS) {
		throw invalidRegistry("The browser profile registry has too many retained worker bindings.");
	}
	for (const [sessionId, value] of bindingEntries) {
		if (!isValidBrowserProfileSessionId(sessionId) || !isRecord(value)) {
			throw invalidRegistry("The browser profile registry contains an invalid worker binding.");
		}
		const profileId = value.profileId;
		if (!isBrowserProfileId(profileId) || profileId !== profileId.toLowerCase() || !profileIds.has(profileId)) {
			throw invalidRegistry("The browser profile registry contains a binding to an unknown profile.");
		}
		if (!validTimestamp(value.updatedAt)) {
			throw invalidRegistry("The browser profile registry contains an invalid binding timestamp.");
		}
		bindings[sessionId] = { profileId, updatedAt: value.updatedAt };
	}

	return { version: BROWSER_PROFILE_REGISTRY_VERSION, profiles, bindings };
}

function cloneRegistry(registry: BrowserProfileRegistry): BrowserProfileRegistry {
	return {
		version: registry.version,
		profiles: registry.profiles.map((profile) => ({ ...profile })),
		bindings: Object.fromEntries(
			Object.entries(registry.bindings).map(([sessionId, binding]) => [sessionId, { ...binding }]),
		),
	};
}

export class BrowserProfileStore {
	private readonly file: string;
	private readonly now: () => Date;
	private registry = emptyRegistry();
	private loaded = false;
	private loadError: BrowserProfileStoreErrorInfo | null = null;
	private mutationQueue: Promise<void> = Promise.resolve();
	private readonly profileOperationQueues = new Map<string, Promise<void>>();
	private readonly profileOperationsPending = new Set<string>();
	private readonly profileOperationsInProgress = new Set<string>();
	private loadPromise: Promise<void> | null = null;

	constructor(options: BrowserProfileStoreOptions) {
		this.file = path.join(options.stateDir, BROWSER_PROFILE_REGISTRY_FILE_NAME);
		this.now = options.now ?? (() => new Date());
	}

	async load(): Promise<BrowserProfileListState> {
		if (!this.loadPromise) {
			this.loadPromise = this.loadUnlocked();
		}
		await this.loadPromise;
		return this.listState();
	}

	private async loadUnlocked(): Promise<void> {
		if (this.loaded) return;
		this.loaded = true;
		let raw: string;
		try {
			raw = await readFile(this.file, "utf8");
		} catch (error) {
			if ((error as NodeJS.ErrnoException).code === "ENOENT") return;
			this.loadError = {
				code: "BROWSER_PROFILE_STORE_UNAVAILABLE",
				message: `Could not read the browser profile registry: ${(error as Error).message}`,
			};
			return;
		}
		try {
			this.registry = parseRegistry(JSON.parse(raw));
		} catch (error) {
			const normalized =
				error instanceof BrowserProfileStoreError
					? error
					: invalidRegistry("The browser profile registry is not valid JSON.");
			this.loadError = { code: normalized.code as BrowserProfileStoreErrorInfo["code"], message: normalized.message };
		}
	}

	private listState(): BrowserProfileListState {
		return {
			profiles: this.loadError ? [] : this.registry.profiles.map((profile) => ({ ...profile })),
			...(this.loadError ? { error: { ...this.loadError } } : {}),
		};
	}

	get error(): BrowserProfileStoreErrorInfo | null {
		return this.loadError ? { ...this.loadError } : null;
	}

	get profiles(): BrowserProfile[] {
		return this.loadError ? [] : this.registry.profiles.map((profile) => ({ ...profile }));
	}

	getProfile(profileId: string): BrowserProfile | undefined {
		if (this.loadError) return undefined;
		const profile = this.registry.profiles.find((candidate) => candidate.id === profileId);
		return profile ? { ...profile } : undefined;
	}

	getSessionProfileId(sessionId: string): string | undefined {
		if (this.loadError) return undefined;
		return this.registry.bindings[sessionId]?.profileId;
	}

	isProfileOperationInProgress(profileId: string): boolean {
		return this.profileOperationsPending.has(profileId) || this.profileOperationsInProgress.has(profileId);
	}

	waitForProfileOperation(profileId: string): Promise<void> {
		return this.profileOperationQueues.get(profileId) ?? Promise.resolve();
	}

	private ensureUsable(): void {
		if (this.loadError) {
			throw new BrowserProfileStoreError(this.loadError.code, this.loadError.message);
		}
	}

	private enqueueMutation<T>(operation: (current: BrowserProfileRegistry) => { registry: BrowserProfileRegistry; result: T }): Promise<T> {
		const queued = this.mutationQueue.then(async () => {
			await this.load();
			this.ensureUsable();
			const { registry, result } = operation(cloneRegistry(this.registry));
			await this.atomicWrite(registry);
			this.registry = registry;
			return result;
		});
		this.mutationQueue = queued.then(
			() => undefined,
			() => undefined,
		);
		return queued;
	}

	private async atomicWrite(registry: BrowserProfileRegistry): Promise<void> {
		const directory = path.dirname(this.file);
		await mkdir(directory, { recursive: true, mode: 0o750 });
		const temporaryFile = path.join(directory, `.${BROWSER_PROFILE_REGISTRY_FILE_NAME}.${process.pid}.${randomUUID()}.tmp`);
		try {
			await writeFile(temporaryFile, `${JSON.stringify(registry, null, 2)}\n`, { mode: 0o600 });
			await rename(temporaryFile, this.file);
		} catch (error) {
			await rm(temporaryFile, { force: true }).catch(() => undefined);
			throw new BrowserProfileStoreError(
				"BROWSER_PROFILE_STORE_UNAVAILABLE",
				`Could not save the browser profile registry: ${(error as Error).message}`,
			);
		}
	}

	async createProfile(rawName: unknown): Promise<BrowserProfile> {
		const name = normalizeBrowserProfileName(rawName);
		if (!name) throw new BrowserProfileStoreError("INVALID_ARGUMENT", "Profile name is invalid.");
		return this.enqueueMutation((current) => {
			if (current.profiles.length >= BROWSER_PROFILE_MAX_COUNT) {
				throw new BrowserProfileStoreError("BROWSER_PROFILE_LIMIT", "The browser profile limit has been reached.");
			}
			if (current.profiles.some((profile) => profile.name.toLowerCase() === name.toLowerCase())) {
				throw new BrowserProfileStoreError("BROWSER_PROFILE_NAME_TAKEN", "A browser profile with that name already exists.");
			}
			const now = this.now().toISOString();
			const profile: BrowserProfile = { id: randomUUID(), name, createdAt: now, updatedAt: now };
			current.profiles.push(profile);
			return { registry: current, result: { ...profile } };
		});
	}

	async renameProfile(profileId: string, rawName: unknown): Promise<BrowserProfile> {
		if (!isBrowserProfileId(profileId)) {
			throw new BrowserProfileStoreError("INVALID_ARGUMENT", "Profile ID is invalid.");
		}
		const name = normalizeBrowserProfileName(rawName);
		if (!name) throw new BrowserProfileStoreError("INVALID_ARGUMENT", "Profile name is invalid.");
		return this.enqueueMutation((current) => {
			const profile = current.profiles.find((candidate) => candidate.id === profileId);
			if (!profile) throw new BrowserProfileStoreError("BROWSER_PROFILE_NOT_FOUND", "Browser profile was not found.");
			if (
				current.profiles.some(
					(candidate) => candidate.id !== profileId && candidate.name.toLowerCase() === name.toLowerCase(),
				)
			) {
				throw new BrowserProfileStoreError("BROWSER_PROFILE_NAME_TAKEN", "A browser profile with that name already exists.");
			}
			profile.name = name;
			profile.updatedAt = this.now().toISOString();
			return { registry: current, result: { ...profile } };
		});
	}

	async bindSession(sessionId: string, profileId: string | null): Promise<void> {
		if (!isValidBrowserProfileSessionId(sessionId)) {
			throw new BrowserProfileStoreError("INVALID_ARGUMENT", "Session ID is invalid.");
		}
		if (profileId !== null && !isBrowserProfileId(profileId)) {
			throw new BrowserProfileStoreError("INVALID_ARGUMENT", "Profile ID is invalid.");
		}
		return this.enqueueMutation((current) => {
			if (profileId !== null && !current.profiles.some((profile) => profile.id === profileId)) {
				throw new BrowserProfileStoreError("BROWSER_PROFILE_NOT_FOUND", "Browser profile was not found.");
			}
			const nextBindings = { ...current.bindings };
			if (profileId === null) {
				delete nextBindings[sessionId];
			} else {
				if (!nextBindings[sessionId] && Object.keys(nextBindings).length >= BROWSER_PROFILE_MAX_BINDINGS) {
					throw new BrowserProfileStoreError(
						"BROWSER_PROFILE_BINDING_LIMIT",
						"The retained browser worker binding limit has been reached.",
					);
				}
				nextBindings[sessionId] = { profileId, updatedAt: this.now().toISOString() };
			}
			return { registry: { ...current, bindings: nextBindings }, result: undefined };
		});
	}

	async deleteProfile(profileId: string): Promise<void> {
		if (!isBrowserProfileId(profileId)) {
			throw new BrowserProfileStoreError("INVALID_ARGUMENT", "Profile ID is invalid.");
		}
		return this.enqueueMutation((current) => {
			if (!current.profiles.some((profile) => profile.id === profileId)) {
				throw new BrowserProfileStoreError("BROWSER_PROFILE_NOT_FOUND", "Browser profile was not found.");
			}
			const bindings = Object.fromEntries(
				Object.entries(current.bindings).filter(([, binding]) => binding.profileId !== profileId),
			);
			return {
				registry: {
					...current,
					profiles: current.profiles.filter((profile) => profile.id !== profileId),
					bindings,
				},
				result: undefined,
			};
		});
	}

	/**
	 * Serializes destructive storage work with registry mutation and blocks new
	 * live usage for the profile while the Electron Session APIs are running.
	 */
	async runProfileOperation<T>(profileId: string, isLive: () => boolean, operation: ProfileOperation<T>): Promise<T> {
		if (!isBrowserProfileId(profileId)) {
			throw new BrowserProfileStoreError("INVALID_ARGUMENT", "Profile ID is invalid.");
		}
		await this.load();
		this.ensureUsable();
		if (!this.getProfile(profileId)) {
			throw new BrowserProfileStoreError("BROWSER_PROFILE_NOT_FOUND", "Browser profile was not found.");
		}
		this.profileOperationsPending.add(profileId);
		const previous = this.profileOperationQueues.get(profileId) ?? Promise.resolve();
		const queued = previous.then(async () => {
			if (isLive()) throw new BrowserProfileStoreError("BROWSER_PROFILE_ACTIVE", "The browser profile is currently in use.");
			this.profileOperationsInProgress.add(profileId);
			try {
				const result = await operation();
				if (isLive()) {
					throw new BrowserProfileStoreError("BROWSER_PROFILE_ACTIVE", "The browser profile became active while it was being changed.");
				}
				return result;
			} finally {
				this.profileOperationsInProgress.delete(profileId);
			}
		});
		const tail = queued.then(
			() => undefined,
			() => undefined,
		);
		this.profileOperationQueues.set(profileId, tail);
		void tail.then(() => {
			if (this.profileOperationQueues.get(profileId) === tail) {
				this.profileOperationsPending.delete(profileId);
				this.profileOperationQueues.delete(profileId);
			}
		}, () => {
			if (this.profileOperationQueues.get(profileId) === tail) {
				this.profileOperationsPending.delete(profileId);
				this.profileOperationQueues.delete(profileId);
			}
		});
		return queued;
	}

	partitionForProfile(profileId: string): string {
		if (!this.getProfile(profileId)) {
			throw new BrowserProfileStoreError("BROWSER_PROFILE_NOT_FOUND", "Browser profile was not found.");
		}
		return browserProfilePartition(profileId);
	}
}

export async function createBrowserProfileStore(options: BrowserProfileStoreOptions): Promise<BrowserProfileStore> {
	const store = new BrowserProfileStore(options);
	await store.load();
	return store;
}
