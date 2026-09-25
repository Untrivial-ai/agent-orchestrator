import {
	bindCloudStartupAttempt,
	type CloudStartupAttempt,
} from "./cloud-startup-timing";

export type CloudSessionPreparationCommit = {
	displayName: string;
	prompt: string;
};

export type CloudSessionPreparationLease = {
	attachmentExpiresAt: string;
	expiresAt: string;
	generation: number;
	leaseSeconds: number;
};

export type CloudSessionPreparationRegistration = {
	attempt: CloudStartupAttempt;
	detach: (sessionId: string, clientInstanceId: string, generation: number) => Promise<void>;
	commit: (
		sessionId: string,
		input: CloudSessionPreparationCommit,
		idempotencyKey: string,
		clientInstanceId: string,
		generation: number,
	) => Promise<void>;
	compatibilityKey: string;
	create: (idempotencyKey: string, clientInstanceId: string) => Promise<{
		lease: CloudSessionPreparationLease;
		sessionId: string;
	}>;
	renew: (
		sessionId: string,
		clientInstanceId: string,
		generation: number,
	) => Promise<CloudSessionPreparationLease>;
	scopeKey: string;
	onEvent?: (event: string, properties?: Record<string, unknown>) => void;
};

export type CloudSessionPreparation = {
	attempt: CloudStartupAttempt;
	commit: (input: CloudSessionPreparationCommit) => Promise<string>;
	invalidate: () => void;
	recordActivity: () => void;
	release: () => void;
	retainForCommit: () => void;
};

type PreparationPhase =
	| "preparing"
	| "attached"
	| "detached"
	| "committing"
	| "committed"
	| "expired"
	| "invalidated"
	| "failed";

type PreparationEntry = {
	activityDirty: boolean;
	activityTimer?: ReturnType<typeof setTimeout>;
	attachments: number;
	attempt: CloudStartupAttempt;
	cleanupStarted: boolean;
	clientInstanceId: string;
	commitKey: string;
	compatibilityKey: string;
	createKey: string;
	durableSessionId?: string;
	detachInFlight?: Promise<void>;
	generation?: number;
	expiresAtMs?: number;
	expiryTimer?: ReturnType<typeof setTimeout>;
	key: string;
	lastRenewedAtMs: number;
	phase: PreparationPhase;
	ready?: Promise<string>;
	registration: CloudSessionPreparationRegistration;
	renewInFlight?: Promise<CloudSessionPreparationLease>;
	replacement?: PreparationEntry;
	scopeKey: string;
};

const ACTIVITY_RENEW_INTERVAL_MS = 45_000;
const ACTIVITY_RETRY_DELAY_MS = 5_000;
const entries = new Map<string, PreparationEntry>();

function registryKey(scopeKey: string, compatibilityKey: string): string {
	return `${scopeKey}\u0000${compatibilityKey}`;
}

function clearTimer(timer: ReturnType<typeof setTimeout> | undefined): void {
	if (timer !== undefined) clearTimeout(timer);
}

function removeEntry(entry: PreparationEntry): void {
	if (entries.get(entry.key) === entry) entries.delete(entry.key);
	clearTimer(entry.activityTimer);
	clearTimer(entry.expiryTimer);
	entry.activityTimer = undefined;
	entry.expiryTimer = undefined;
}

function emitEvent(entry: PreparationEntry, event: string, properties?: Record<string, unknown>): void {
	entry.registration.onEvent?.(event, {
		...(entry.durableSessionId ? { session_id: entry.durableSessionId } : {}),
		...properties,
	});
}

function expireEntry(entry: PreparationEntry): void {
	if (entry.phase === "committing" || entry.phase === "committed" || entry.phase === "expired" ||
		entry.phase === "invalidated") return;
	const previousPhase = entry.phase;
	entry.phase = "expired";
	removeEntry(entry);
	emitEvent(entry, "expired", { attachment_state: previousPhase });
}

function scheduleExpiry(entry: PreparationEntry): void {
	clearTimer(entry.expiryTimer);
	entry.expiryTimer = undefined;
	if (entry.expiresAtMs === undefined) return;
	entry.expiryTimer = setTimeout(
		() => expireEntry(entry),
		Math.max(0, entry.expiresAtMs - Date.now()),
	);
}

function setLease(entry: PreparationEntry, lease: CloudSessionPreparationLease): void {
	const serverExpiresAtMs = Date.parse(lease.expiresAt);
	const leaseDurationMs = lease.leaseSeconds * 1_000;
	if (!Number.isFinite(serverExpiresAtMs) || !Number.isFinite(leaseDurationMs) || leaseDurationMs <= 0 ||
		!Number.isSafeInteger(lease.generation) || lease.generation < 1) {
		throw new Error("The control plane returned an invalid preparation expiry.");
	}
	const expiresAtMs = Date.now() + leaseDurationMs;
	entry.expiresAtMs = expiresAtMs;
	entry.lastRenewedAtMs = Date.now();
	scheduleExpiry(entry);
}

function detachEntry(entry: PreparationEntry): Promise<void> {
	if (entry.detachInFlight) return entry.detachInFlight;
	entry.detachInFlight = ensureReady(entry).then((sessionId) => {
		if (entry.generation === undefined || (entry.attachments > 0 && entry.phase !== "invalidated")) return;
		return entry.registration.detach(sessionId, entry.clientInstanceId, entry.generation);
	}).finally(() => {
		entry.detachInFlight = undefined;
	});
	return entry.detachInFlight;
}

function cleanupInvalidated(entry: PreparationEntry): void {
	if (entry.cleanupStarted) return;
	entry.cleanupStarted = true;
	void detachEntry(entry).catch(() => undefined);
}

function ensureReady(entry: PreparationEntry): Promise<string> {
	if (entry.ready) return entry.ready;
	entry.ready = entry.registration.create(entry.createKey, entry.clientInstanceId).then(({ lease, sessionId }) => {
		if (!sessionId.trim()) throw new Error("The control plane returned no session identifier.");
		entry.durableSessionId = sessionId;
		entry.generation = lease.generation;
		setLease(entry, lease);
		bindCloudStartupAttempt(sessionId, entry.attempt);
		if (entry.phase === "invalidated") cleanupInvalidated(entry);
		return sessionId;
	}).catch((error) => {
		entry.ready = undefined;
		if (entry.phase !== "invalidated") entry.phase = "failed";
		throw error;
	});
	void entry.ready.catch(() => undefined);
	return entry.ready;
}

function createEntry(registration: CloudSessionPreparationRegistration): PreparationEntry {
	const key = registryKey(registration.scopeKey, registration.compatibilityKey);
	const entry: PreparationEntry = {
		activityDirty: false,
		attachments: 0,
		attempt: registration.attempt,
		cleanupStarted: false,
		clientInstanceId: globalThis.crypto.randomUUID(),
		commitKey: globalThis.crypto.randomUUID(),
		compatibilityKey: registration.compatibilityKey,
		createKey: globalThis.crypto.randomUUID(),
		key,
		lastRenewedAtMs: Date.now(),
		phase: "preparing",
		registration,
		scopeKey: registration.scopeKey,
	};
	entries.set(key, entry);
	emitEvent(entry, "acquired", { acquisition: "new" });
	void ensureReady(entry);
	return entry;
}

function invalidateEntry(entry: PreparationEntry): void {
	if (entry.phase === "committing" || entry.phase === "committed" || entry.phase === "invalidated") return;
	entry.phase = "invalidated";
	removeEntry(entry);
	cleanupInvalidated(entry);
}

function invalidateIncompatible(registration: CloudSessionPreparationRegistration): void {
	for (const entry of entries.values()) {
		if (entry.scopeKey === registration.scopeKey && entry.compatibilityKey !== registration.compatibilityKey) {
			invalidateEntry(entry);
		}
	}
}

function resolveEntry(entry: PreparationEntry): PreparationEntry {
	let current = entry;
	while (current.replacement) current = current.replacement;
	return current;
}

function replaceExpiredEntry(entry: PreparationEntry): PreparationEntry {
	const current = resolveEntry(entry);
	expireEntry(current);
	const existing = entries.get(current.key);
	if (existing && existing !== current && !isExpired(existing)) {
		existing.attachments += current.attachments;
		current.attachments = 0;
		current.replacement = existing;
		return existing;
	}
	const replacement = createEntry(current.registration);
	replacement.attachments = current.attachments;
	replacement.phase = replacement.attachments > 0 ? "attached" : "detached";
	current.attachments = 0;
	current.replacement = replacement;
	return replacement;
}

function isExpired(entry: PreparationEntry): boolean {
	return entry.phase === "expired" || (entry.expiresAtMs !== undefined && entry.expiresAtMs <= Date.now());
}

export function isCloudSessionPreparationExpired(error: unknown): boolean {
	return typeof error === "object" && error !== null && "code" in error &&
		(error as { code?: unknown }).code === "PREPARATION_EXPIRED";
}

export function isCloudSessionPreparationUnsupported(error: unknown): boolean {
	if (typeof error !== "object" || error === null) return false;
	const failure = error as { code?: unknown; status?: unknown };
	return failure.status === 404 && failure.code === undefined;
}

async function renewEntry(entry: PreparationEntry): Promise<CloudSessionPreparationLease> {
	if (entry.renewInFlight) return entry.renewInFlight;
	emitEvent(entry, "renewal_attempted");
	entry.renewInFlight = ensureReady(entry).then((sessionId) => {
		if (entry.generation === undefined) throw new Error("The control plane returned no preparation generation.");
		clearTimer(entry.expiryTimer);
		entry.expiryTimer = undefined;
		return entry.registration.renew(sessionId, entry.clientInstanceId, entry.generation);
	}).then((lease) => {
		entry.generation = lease.generation;
		setLease(entry, lease);
		emitEvent(entry, "renewal_succeeded");
		return lease;
	}).catch((error) => {
		emitEvent(entry, "renewal_failed", {
			failure_category: isCloudSessionPreparationExpired(error) ? "expired" : "transport_or_server",
		});
		if (isCloudSessionPreparationExpired(error) ||
			(entry.expiresAtMs !== undefined && entry.expiresAtMs <= Date.now())) {
			expireEntry(entry);
		} else {
			scheduleExpiry(entry);
		}
		throw error;
	}).finally(() => {
		entry.renewInFlight = undefined;
	});
	return entry.renewInFlight;
}

async function reattachEntry(entry: PreparationEntry): Promise<void> {
	const previousSessionId = entry.durableSessionId;
	const detaching = entry.detachInFlight;
	const ready = (async () => {
		if (detaching) await detaching.catch(() => undefined);
		entry.createKey = globalThis.crypto.randomUUID();
		return entry.registration.create(entry.createKey, entry.clientInstanceId);
	})().then(({ lease, sessionId }) => {
		entry.durableSessionId = sessionId;
		entry.generation = lease.generation;
		setLease(entry, lease);
		if (sessionId !== previousSessionId) bindCloudStartupAttempt(sessionId, entry.attempt);
		return sessionId;
	}).catch((error) => {
		entry.ready = undefined;
		throw error;
	});
	entry.ready = ready;
	await ready;
}

function scheduleActivityRenewal(entry: PreparationEntry, delay?: number): void {
	if (entry.activityTimer !== undefined || entry.attachments < 1 || !entry.activityDirty) return;
	const dueIn = delay ?? Math.max(0, entry.lastRenewedAtMs + ACTIVITY_RENEW_INTERVAL_MS - Date.now());
	entry.activityTimer = setTimeout(() => {
		entry.activityTimer = undefined;
		if (entry.attachments < 1 || !entry.activityDirty) return;
		entry.activityDirty = false;
		void renewEntry(entry).then(() => {
			if (entry.activityDirty) scheduleActivityRenewal(entry);
		}).catch((error) => {
			if (isCloudSessionPreparationExpired(error)) {
				if (entry.attachments > 0) replaceExpiredEntry(entry);
				return;
			}
			entry.activityDirty = true;
			if (!isExpired(entry)) scheduleActivityRenewal(entry, ACTIVITY_RETRY_DELAY_MS);
		});
	}, dueIn);
}

function acquireEntry(registration: CloudSessionPreparationRegistration): PreparationEntry {
	invalidateIncompatible(registration);
	const key = registryKey(registration.scopeKey, registration.compatibilityKey);
	let entry = entries.get(key);
	if (entry && isExpired(entry)) {
		expireEntry(entry);
		entry = undefined;
	}
	if (!entry) return createEntry(registration);
	entry.registration = registration;
	emitEvent(entry, "acquired", { acquisition: "reused" });
	return entry;
}

export function startCloudSessionPreparation(
	registration: CloudSessionPreparationRegistration,
): CloudSessionPreparation {
	let entry = acquireEntry(registration);
	const reused = entry.attachments === 0 && entry.durableSessionId !== undefined;
	entry.attachments += 1;
	if (entry.phase !== "preparing") entry.phase = "attached";
	let released = false;

	if (reused) {
		emitEvent(entry, "reattached");
		void reattachEntry(entry).catch((error) => {
			if (isCloudSessionPreparationExpired(error)) entry = replaceExpiredEntry(entry);
		});
	}

	const currentEntry = (replaceExpired: boolean): PreparationEntry => {
		entry = resolveEntry(entry);
		if (replaceExpired && isExpired(entry)) entry = replaceExpiredEntry(entry);
		return entry;
	};

	return {
		attempt: entry.attempt,
		commit: async (input) => {
			const active = currentEntry(true);
			active.phase = "committing";
			clearTimer(active.activityTimer);
			clearTimer(active.expiryTimer);
			active.activityTimer = undefined;
			active.expiryTimer = undefined;
			let sessionId: string;
			try {
				sessionId = await ensureReady(active);
			} catch {
				sessionId = await ensureReady(active);
			}
			try {
				if (active.generation === undefined) throw new Error("The control plane returned no preparation generation.");
				await active.registration.commit(
					sessionId, input, active.commitKey, active.clientInstanceId, active.generation,
				);
			} catch (error) {
				if (isCloudSessionPreparationExpired(error)) {
					active.phase = "expired";
					removeEntry(active);
					emitEvent(active, "expired", { attachment_state: "committing" });
					throw error;
				}
				if (active.generation === undefined) throw error;
				await active.registration.commit(
					sessionId, input, active.commitKey, active.clientInstanceId, active.generation,
				);
			}
			active.phase = "committed";
			removeEntry(active);
			return sessionId;
		},
		invalidate: () => invalidateEntry(currentEntry(false)),
		recordActivity: () => {
			if (released) return;
			const active = currentEntry(true);
			if (active.phase === "committing" || active.phase === "committed" || active.phase === "invalidated") return;
			active.activityDirty = true;
			scheduleActivityRenewal(active);
		},
		release: () => {
			if (released) return;
			released = true;
			const active = currentEntry(false);
			active.attachments = Math.max(0, active.attachments - 1);
			if (active.attachments > 0 || active.phase === "committing" || active.phase === "committed" ||
				active.phase === "expired" || active.phase === "invalidated") return;
			active.phase = "detached";
			emitEvent(active, "detached");
			active.activityDirty = false;
			clearTimer(active.activityTimer);
			active.activityTimer = undefined;
			void detachEntry(active).catch(() => undefined);
		},
		retainForCommit: () => {
			const active = currentEntry(true);
			if (active.phase !== "committed" && active.phase !== "invalidated") active.phase = "committing";
		},
	};
}

export function resetCloudSessionPreparationRegistryForTests(): void {
	for (const entry of entries.values()) removeEntry(entry);
	entries.clear();
}
