import { useCallback, useEffect, useRef, useState } from "react";
import { chatDraftScopeSessionId } from "../lib/chat-drafts";

// Client-side mirror of the backend spawn caps in
// backend/internal/httpd/controllers/sessions.go (maxAttachments /
// maxAttachmentBytes / maxAttachmentsBytes). Enforced here too so the user gets
// inline feedback at paste/drop time instead of a late rejection after submit.
export const MAX_ATTACHMENTS = 8;
export const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024;
export const MAX_ATTACHMENTS_BYTES = 25 * 1024 * 1024;

const mb = (bytes: number) => Math.round(bytes / (1024 * 1024));

export type FileAttachmentStatus = "reading" | "ready" | "failed";

/** A single file staged for a task/orchestrator brief. */
export type FileAttachment = {
	/** Stable id for list keys and removal. */
	id: string;
	/** Browser-reported MIME type (e.g. "image/png", "text/plain"). */
	mimeType: string;
	/** Decoded byte size (from File.size), used to enforce the total-size cap. */
	bytes: number;
	/** File name for display. */
	name: string;
	/** FileReader lifecycle. Failed entries stay in the draft for retry/removal. */
	status?: FileAttachmentStatus;
	/** data: URL used to render the thumbnail preview (for images only). */
	dataUrl?: string;
	/** Base64 payload without the "data:...;base64," prefix, for upload. */
	data?: string;
	/** Durable worktree-relative path, present only after daemon staging succeeds. */
	stagedPath?: string;
};

/** Attachment payload shape accepted by the spawn API. */
export type FileAttachmentPayload = {
	mimeType: string;
	data: string;
	name?: string;
};

export type FileAttachmentOptions = {
	/** Previously staged descriptors restored for this owning surface. */
	initialAttachments?: FileAttachment[];
	/** Changes when the hook must replace its state with initialAttachments. */
	initialKey?: string;
	/** Reads the latest durable descriptors when a retained surface activates. */
	readPersistedAttachments?: () => FileAttachment[];
	/**
	 * Make newly read bytes durable before preparation settles. Reading and failed
	 * chips stay visible so the user can see, retry, or remove the exact file.
	 */
	prepareAttachments?: (attachments: FileAttachment[]) => Promise<FileAttachment[]>;
	/**
	 * Called after an accepted add, removal, or clear. Return false when the
	 * current staged descriptors could not be recorded in the local draft.
	 */
	onAttachmentsChange?: (attachments: FileAttachment[]) => boolean | void;
};

type SharedAttachmentUpdate = {
	pending: number;
	revision: number;
	persistedRevision: number;
	attachments?: FileAttachment[];
	error?: string | null;
};

type SharedAttachmentEntry = {
	pending: Map<symbol, Set<string>>;
	settlement?: { promise: Promise<void>; resolve: () => void };
	generation: number;
	cancellationRevision: number;
	revision: number;
	persistedRevision: number;
	listeners: Map<symbol, (update: SharedAttachmentUpdate) => void>;
	stagingQueue: Promise<void>;
	attachments?: FileAttachment[];
	descriptorVersions: Map<string, number>;
	/** Latest attachment work whose descriptor lineage owns each id. */
	descriptorWorkTokens: Map<string, symbol>;
	/** Exact descriptor versions proven written to the renderer's local draft. */
	persistedDescriptorVersions: Map<string, number>;
	removalTombstones: Map<
		string,
		{ attachment: FileAttachment; version: number; confirmed: boolean }
	>;
	error?: string | null;
	sources: Map<string, File>;
};

type SharedAttachmentWork = { token: symbol; generation: number };

type CapturedPendingFileAttachmentEntry = {
	key: string;
	generation: number;
	tokens: readonly symbol[];
	locallyUnpersisted: readonly { id: string; version: number }[];
	removals: readonly { id: string; version: number }[];
};

const pendingFileAttachmentCaptureEntries = Symbol("pending-file-attachment-capture");

/** Opaque handle for pending work and locally unpersisted drafts at an approved boundary. */
export type PendingFileAttachmentCapture = {
	readonly [pendingFileAttachmentCaptureEntries]: readonly CapturedPendingFileAttachmentEntry[];
};

function sharedAttachmentDescriptors(attachments: FileAttachment[]): FileAttachment[] {
	return attachments.map(({ id, mimeType, bytes, name, status, stagedPath }) => ({
		id,
		mimeType,
		bytes,
		name,
		status,
		...(stagedPath ? { stagedPath } : {}),
	}));
}

// Attachment preparation belongs to the AO session, not to one React mount. A
// controller or surface remount can happen while FileReader or the daemon is
// working; this registry lets the replacement hook await that exact lifecycle
// and receive the staged descriptors instead of racing a stale storage restore.
const sharedAttachmentEntries = new Map<string, SharedAttachmentEntry>();

function sharedEntry(key: string): SharedAttachmentEntry {
	let entry = sharedAttachmentEntries.get(key);
	if (!entry) {
		entry = {
			pending: new Map(),
			generation: 0,
			cancellationRevision: 0,
			revision: 0,
			persistedRevision: 0,
			listeners: new Map(),
			stagingQueue: Promise.resolve(),
			descriptorVersions: new Map(),
			descriptorWorkTokens: new Map(),
			persistedDescriptorVersions: new Map(),
			removalTombstones: new Map(),
			sources: new Map(),
		};
		sharedAttachmentEntries.set(key, entry);
	}
	return entry;
}

function notifySharedAttachmentEntry(
	key: string,
	update: Omit<SharedAttachmentUpdate, "pending" | "revision" | "persistedRevision"> = {},
	originToken?: symbol,
): void {
	const entry = sharedAttachmentEntries.get(key);
	if (!entry) return;
	if (update.attachments !== undefined) {
		const previous = new Map(entry.attachments?.map((attachment) => [attachment.id, attachment]));
		const nextIDs = new Set<string>();
		for (const attachment of update.attachments) {
			nextIDs.add(attachment.id);
			const before = previous.get(attachment.id);
			if (
				!before ||
				before.mimeType !== attachment.mimeType ||
				before.bytes !== attachment.bytes ||
				before.name !== attachment.name ||
				before.status !== attachment.status ||
				before.stagedPath !== attachment.stagedPath
			) {
				entry.descriptorVersions.set(
					attachment.id,
					(entry.descriptorVersions.get(attachment.id) ?? 0) + 1,
				);
			}
		}
		for (const id of entry.persistedDescriptorVersions.keys()) {
			if (!nextIDs.has(id)) entry.persistedDescriptorVersions.delete(id);
		}
		for (const id of entry.descriptorWorkTokens.keys()) {
			if (!nextIDs.has(id)) entry.descriptorWorkTokens.delete(id);
		}
		entry.attachments = update.attachments;
		entry.revision += 1;
	}
	if (update.error !== undefined) entry.error = update.error;
	const notification = {
		pending: entry.pending.size,
		revision: entry.revision,
		persistedRevision: entry.persistedRevision,
		...update,
	};
	for (const [token, listener] of entry.listeners) {
		if (token !== originToken) listener(notification);
	}
}

function stageSharedAttachmentRemovals(
	key: string,
	previous: FileAttachment[],
	next: FileAttachment[],
): void {
	const entry = sharedEntry(key);
	const nextIDs = new Set(next.map(({ id }) => id));
	for (const attachment of sharedAttachmentDescriptors(previous)) {
		if (!attachment.stagedPath || nextIDs.has(attachment.id)) continue;
		let version = entry.descriptorVersions.get(attachment.id);
		if (version === undefined) {
			version = 1;
			entry.descriptorVersions.set(attachment.id, version);
		}
		const existing = entry.removalTombstones.get(attachment.id);
		if (!existing || existing.version <= version) {
			entry.removalTombstones.set(attachment.id, {
				attachment,
				version,
				confirmed: false,
			});
		}
	}
}

function recoverableInitialAttachments(
	key: string | undefined,
	attachments: FileAttachment[],
): FileAttachment[] {
	if (!key) return attachments;
	const entry = sharedAttachmentEntries.get(key);
	if (!entry || entry.removalTombstones.size === 0) return attachments;
	const currentIDs = new Set(entry.attachments?.map(({ id }) => id));
	return attachments.filter(({ id }) => {
		const tombstone = entry.removalTombstones.get(id);
		if (!tombstone) return true;
		return (
			currentIDs.has(id) &&
			(entry.descriptorVersions.get(id) ?? tombstone.version) > tombstone.version
		);
	});
}

function sharedDescriptorVersions(
	key: string,
	attachments: FileAttachment[],
): Map<string, number> {
	const entry = sharedAttachmentEntries.get(key);
	return new Map(
		attachments.flatMap(({ id }) => {
			const version = entry?.descriptorVersions.get(id);
			return version === undefined ? [] : [[id, version] as const];
		}),
	);
}

function recordPersistedSharedAttachmentDescriptors(
	key: string,
	attachments: FileAttachment[],
	versions: ReadonlyMap<string, number>,
	persisted: boolean | void,
): void {
	if (persisted === false) return;
	const entry = sharedAttachmentEntries.get(key);
	if (!entry) return;
	const currentByID = new Map(entry.attachments?.map((attachment) => [attachment.id, attachment]));
	let persistedDescriptorCount = 0;
	for (const attachment of attachments) {
		if (!attachment.stagedPath) continue;
		const version = versions.get(attachment.id);
		if (
			version === undefined ||
			entry.descriptorVersions.get(attachment.id) !== version ||
			currentByID.get(attachment.id)?.stagedPath !== attachment.stagedPath
		) {
			continue;
		}
		entry.persistedDescriptorVersions.set(attachment.id, version);
		persistedDescriptorCount += 1;
	}
	if (
		currentByID.size === attachments.length &&
		persistedDescriptorCount === attachments.length
	) {
		entry.persistedRevision = entry.revision;
	}
	for (const [id, tombstone] of entry.removalTombstones) {
		const current = currentByID.get(id);
		if (!current) {
			if (entry.descriptorVersions.get(id) === tombstone.version) {
				entry.removalTombstones.delete(id);
			}
			continue;
		}
		const currentVersion = versions.get(id);
		if (currentVersion !== undefined && currentVersion > tombstone.version) {
			entry.removalTombstones.delete(id);
		}
	}
}

function sharedAttachmentEntryCanEvict(entry: SharedAttachmentEntry): boolean {
	return (
		entry.pending.size === 0 &&
		entry.listeners.size === 0 &&
		(entry.attachments?.length ?? 0) === 0 &&
		entry.sources.size === 0 &&
		entry.removalTombstones.size === 0
	);
}

function scheduleSharedAttachmentEntryEviction(
	key: string,
	entry: SharedAttachmentEntry,
): void {
	// React disconnects effects while an Activity is hidden. During a same-commit
	// replacement, the old subscriber cleans up before the already-rendered
	// replacement subscribes. Keep the settled snapshot through that activation
	// boundary so the replacement cannot fall back to its stale render-time draft.
	queueMicrotask(() => {
		if (
			sharedAttachmentEntries.get(key) === entry &&
			sharedAttachmentEntryCanEvict(entry)
		) {
			sharedAttachmentEntries.delete(key);
		}
	});
}

function beginSharedAttachmentWork(key: string): SharedAttachmentWork {
	const entry = sharedEntry(key);
	if (entry.pending.size === 0) {
		let resolve!: () => void;
		const promise = new Promise<void>((settle) => {
			resolve = settle;
		});
		entry.settlement = { promise, resolve };
	}
	const work = { token: Symbol("chat-attachment-work"), generation: entry.generation };
	entry.pending.set(work.token, new Set());
	notifySharedAttachmentEntry(key);
	return work;
}

function settleSharedAttachmentWork(entry: SharedAttachmentEntry): void {
	if (entry.pending.size > 0 || !entry.settlement) return;
	entry.settlement.resolve();
	entry.settlement = undefined;
}

async function waitForSharedAttachmentWork(key: string): Promise<void> {
	while (true) {
		const entry = sharedAttachmentEntries.get(key);
		if (!entry || entry.pending.size === 0) return;
		const settlement = entry.settlement;
		if (!settlement) return;
		await settlement.promise;
	}
}

function registerSharedAttachmentWorkIds(
	key: string,
	work: SharedAttachmentWork,
	ids: Iterable<string>,
): void {
	const entry = sharedAttachmentEntries.get(key);
	if (!entry || entry.generation !== work.generation) return;
	const owned = entry.pending.get(work.token);
	if (!owned) return;
	for (const id of ids) {
		owned.add(id);
		entry.descriptorWorkTokens.set(id, work.token);
	}
}

function discardSharedAttachmentTokens(
	entry: SharedAttachmentEntry,
	tokens: Iterable<symbol>,
): boolean {
	const discardedIds = new Set<string>();
	let changed = false;
	for (const token of tokens) {
		const owned = entry.pending.get(token);
		if (!owned) continue;
		for (const id of owned) discardedIds.add(id);
		entry.pending.delete(token);
		changed = true;
	}
	if (discardedIds.size > 0) {
		const stillOwned = new Set<string>();
		for (const owned of entry.pending.values()) {
			for (const id of owned) stillOwned.add(id);
		}
		const abandonedIds = new Set([...discardedIds].filter((id) => !stillOwned.has(id)));
		entry.attachments = entry.attachments?.filter(({ id }) => !abandonedIds.has(id)) ?? [];
		for (const id of abandonedIds) {
			entry.descriptorWorkTokens.delete(id);
			entry.persistedDescriptorVersions.delete(id);
			entry.sources.delete(id);
		}
	}
	settleSharedAttachmentWork(entry);
	return changed;
}

function endSharedAttachmentWork(key: string, token: symbol): void {
	const entry = sharedAttachmentEntries.get(key);
	if (!entry) return;
	entry.pending.delete(token);
	settleSharedAttachmentWork(entry);
	notifySharedAttachmentEntry(key);
	if (sharedAttachmentEntryCanEvict(entry)) {
		scheduleSharedAttachmentEntryEviction(key, entry);
	}
}

function subscribeSharedAttachmentWork(
	key: string,
	token: symbol,
	listener: (update: SharedAttachmentUpdate) => void,
): () => void {
	const entry = sharedEntry(key);
	entry.listeners.set(token, listener);
	listener({
		pending: entry.pending.size,
		revision: entry.revision,
		persistedRevision: entry.persistedRevision,
		...(entry.attachments !== undefined ? { attachments: entry.attachments } : {}),
		...(entry.error !== undefined ? { error: entry.error } : {}),
	});
	return () => {
		entry.listeners.delete(token);
		if (sharedAttachmentEntryCanEvict(entry)) {
			scheduleSharedAttachmentEntryEviction(key, entry);
		}
	};
}

function sharedAttachmentPending(key: string | undefined): boolean {
	return Boolean(key && (sharedAttachmentEntries.get(key)?.pending.size ?? 0) > 0);
}

function sharedAttachmentWorkIsCurrent(key: string, work: SharedAttachmentWork): boolean {
	const entry = sharedAttachmentEntries.get(key);
	return Boolean(entry && entry.generation === work.generation && entry.pending.has(work.token));
}

export function discardPendingFileAttachments(key: string): void {
	const entry = sharedAttachmentEntries.get(key);
	if (!entry) return;
	entry.generation += 1;
	entry.cancellationRevision += 1;
	discardSharedAttachmentTokens(entry, [...entry.pending.keys()]);
	notifySharedAttachmentEntry(key, {
		attachments: entry.attachments ?? [],
		error: null,
	});
}

function attachmentKeyBelongsToSession(key: string, sessionId: string): boolean {
	return key === sessionId || chatDraftScopeSessionId(key) === sessionId;
}

/** Capture pending work and locally unpersisted descriptors when leaving is confirmed. */
export function capturePendingFileAttachmentsForSession(
	sessionId: string,
): PendingFileAttachmentCapture {
	const entries: CapturedPendingFileAttachmentEntry[] = [];
	for (const [key, entry] of sharedAttachmentEntries) {
		if (!attachmentKeyBelongsToSession(key, sessionId)) continue;
		const locallyUnpersisted =
			entry.attachments?.flatMap((attachment) =>
				!attachment.stagedPath ||
				entry.persistedDescriptorVersions.get(attachment.id) !==
					entry.descriptorVersions.get(attachment.id)
					? [{ id: attachment.id, version: entry.descriptorVersions.get(attachment.id) ?? 0 }]
					: [],
			) ?? [];
		const removals = [...entry.removalTombstones.values()].map(({ attachment, version }) => ({
			id: attachment.id,
			version,
		}));
		if (
			entry.pending.size === 0 &&
			locallyUnpersisted.length === 0 &&
			removals.length === 0
		) continue;
		entries.push({
			key,
			generation: entry.generation,
			tokens: [...entry.pending.keys()],
			locallyUnpersisted,
			removals,
		});
	}
	return { [pendingFileAttachmentCaptureEntries]: entries };
}

function discardCapturedUnpersistedAttachments(
	entry: SharedAttachmentEntry,
	captured: CapturedPendingFileAttachmentEntry,
): boolean {
	const stillOwned = new Set<string>();
	for (const owned of entry.pending.values()) {
		for (const id of owned) stillOwned.add(id);
	}
	const capturedVersions = new Map(
		captured.locallyUnpersisted.map(({ id, version }) => [id, version]),
	);
	const capturedTokens = new Set(captured.tokens);
	const abandoned = new Set<string>();
	for (const attachment of entry.attachments ?? []) {
		const capturedVersion = capturedVersions.get(attachment.id);
		const currentVersion = entry.descriptorVersions.get(attachment.id);
		const persistedVersion = entry.persistedDescriptorVersions.get(attachment.id);
		const descriptorWorkToken = entry.descriptorWorkTokens.get(attachment.id);
		const ownedByCapturedWork =
			descriptorWorkToken !== undefined && capturedTokens.has(descriptorWorkToken);
		if (
			capturedVersion === undefined ||
			stillOwned.has(attachment.id) ||
			(currentVersion !== undefined && persistedVersion === currentVersion) ||
			(currentVersion !== capturedVersion && !ownedByCapturedWork)
		) {
			continue;
		}
		abandoned.add(attachment.id);
	}
	if (abandoned.size === 0) return false;
	entry.attachments = entry.attachments?.filter(({ id }) => !abandoned.has(id)) ?? [];
	for (const id of abandoned) {
		entry.descriptorWorkTokens.delete(id);
		entry.persistedDescriptorVersions.delete(id);
		entry.sources.delete(id);
	}
	return true;
}

function confirmCapturedAttachmentRemovals(
	entry: SharedAttachmentEntry,
	captured: CapturedPendingFileAttachmentEntry,
): boolean {
	let changed = false;
	const currentIDs = new Set(entry.attachments?.map(({ id }) => id));
	for (const removal of captured.removals) {
		const tombstone = entry.removalTombstones.get(removal.id);
		if (!tombstone || tombstone.version !== removal.version) continue;
		if (
			currentIDs.has(removal.id) &&
			(entry.descriptorVersions.get(removal.id) ?? removal.version) > removal.version
		) {
			continue;
		}
		if (!tombstone.confirmed) {
			tombstone.confirmed = true;
			changed = true;
		}
	}
	return changed;
}

/**
 * Abandon exactly the pending work and unchanged locally unpersisted descriptors from a
 * prior confirmation. Work begun afterward remains recoverable.
 */
export function discardCapturedPendingFileAttachments(
	capture: PendingFileAttachmentCapture,
): void {
	for (const captured of capture[pendingFileAttachmentCaptureEntries]) {
		const entry = sharedAttachmentEntries.get(captured.key);
		if (!entry || entry.generation !== captured.generation) continue;
		const discardedPending = discardSharedAttachmentTokens(entry, captured.tokens);
		const discardedUnpersisted = discardCapturedUnpersistedAttachments(entry, captured);
		const confirmedRemovals = confirmCapturedAttachmentRemovals(entry, captured);
		if (!discardedPending && !discardedUnpersisted && !confirmedRemovals) continue;
		entry.cancellationRevision += 1;
		notifySharedAttachmentEntry(captured.key, {
			attachments: entry.attachments ?? [],
			...(discardedUnpersisted ? { error: null } : {}),
		});
		if (sharedAttachmentEntryCanEvict(entry)) {
			scheduleSharedAttachmentEntryEviction(captured.key, entry);
		}
	}
}

/** Cancel every in-flight renderer generation owned by one logical AO session. */
export function discardPendingFileAttachmentsForSession(sessionId: string): void {
	for (const key of [...sharedAttachmentEntries.keys()]) {
		if (attachmentKeyBelongsToSession(key, sessionId)) discardPendingFileAttachments(key);
	}
}

/**
 * Remove only renderer descriptors/work for a deleted session incarnation. The
 * durable worktree bytes are intentionally outside this registry and untouched.
 */
export function purgeFileAttachmentsForSession(sessionId: string): void {
	for (const key of [...sharedAttachmentEntries.keys()]) {
		if (attachmentKeyBelongsToSession(key, sessionId)) purgeFileAttachments(key);
	}
}

/** Retire one completed owner without touching other drafts or staged bytes. */
export function purgeFileAttachments(key: string): void {
	const entry = sharedAttachmentEntries.get(key);
	if (!entry) return;
	entry.generation += 1;
	entry.cancellationRevision += 1;
	entry.pending.clear();
	settleSharedAttachmentWork(entry);
	entry.sources.clear();
	entry.descriptorWorkTokens.clear();
	entry.persistedDescriptorVersions.clear();
	entry.removalTombstones.clear();
	notifySharedAttachmentEntry(key, { attachments: [], error: null });
	if (entry.listeners.size === 0) sharedAttachmentEntries.delete(key);
}

// Client-side mirror of the backend image-preview allowlist. Non-image files can
// still be attached; they render with the generic file icon.
const SUPPORTED_IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "image/bmp"]);

export const isSupportedImageAttachment = (type: string) =>
	SUPPORTED_IMAGE_TYPES.has(type.toLowerCase().trim());

const readFileAsBase64 = (file: File): Promise<{ dataUrl: string; data: string }> =>
	new Promise((resolve, reject) => {
		const reader = new FileReader();
		reader.onerror = () => reject(reader.error ?? new Error("Failed to read file"));
		reader.onload = () => {
			const dataUrl = typeof reader.result === "string" ? reader.result : "";
			const comma = dataUrl.indexOf(",");
			if (!dataUrl || comma < 0) {
				reject(new Error("Unreadable file data"));
				return;
			}
			resolve({
				dataUrl,
				data: dataUrl.slice(comma + 1),
			});
		};
		reader.readAsDataURL(file);
	});

/**
 * useFileAttachments stages files pasted, dropped, or picked into a brief and
 * exposes them as upload-ready payloads. Count and size caps (mirroring the backend)
 * are enforced here, and rejections / unreadable files surface through `error` for
 * inline feedback.
 *
 * Supports all file types except SVG (blocked for security). Images show thumbnails,
 * other files show a generic file icon.
 */
export function useFileAttachments(options: FileAttachmentOptions = {}) {
	const {
		initialAttachments = [],
		initialKey,
		readPersistedAttachments,
		prepareAttachments,
		onAttachmentsChange,
	} = options;
	const normalizeInitial = useCallback(
		(items: FileAttachment[]): FileAttachment[] =>
			items.map((attachment) => ({ ...attachment, status: attachment.status ?? "ready" })),
		[],
	);
	const restoreInitial = useCallback(
		(items: FileAttachment[]): FileAttachment[] =>
			normalizeInitial(recoverableInitialAttachments(initialKey, items)),
		[initialKey, normalizeInitial],
	);
	const [attachments, setAttachments] = useState<FileAttachment[]>(() =>
		restoreInitial(initialAttachments),
	);
	const [validationError, setValidationError] = useState<string | null>(null);
	const [preparing, setPreparing] = useState(() => sharedAttachmentPending(initialKey));
	const attachmentsRef = useRef<FileAttachment[]>(restoreInitial(initialAttachments));
	const initialKeyRef = useRef(initialKey);
	const generationRef = useRef(0);
	const listenerTokenRef = useRef(Symbol("file-attachment-listener"));
	const renderedSharedRevisionRef = useRef({
		key: initialKey,
		revision: initialKey ? sharedEntry(initialKey).revision : 0,
	});
	if (renderedSharedRevisionRef.current.key !== initialKey) {
		renderedSharedRevisionRef.current = {
			key: initialKey,
			revision: initialKey ? sharedEntry(initialKey).revision : 0,
		};
	}
	const sourceFilesRef = useRef<Map<string, File>>(
		initialKey ? sharedEntry(initialKey).sources : new Map(),
	);
	const pendingReadsRef = useRef<Map<string, Promise<void>>>(new Map());
	const pendingRetriesRef = useRef<Set<Promise<boolean>>>(new Set());
	const addQueueRef = useRef<Promise<void>>(Promise.resolve());
	const stagingQueueRef = useRef<Promise<void>>(Promise.resolve());
	const queuedAddsRef = useRef(0);
	const reportAttachmentsChange = useCallback(
		(next: FileAttachment[]) => {
			if (!onAttachmentsChange) return;
			const versions = initialKey ? sharedDescriptorVersions(initialKey, next) : undefined;
			const persisted = onAttachmentsChange(next);
			if (initialKey && versions) {
				recordPersistedSharedAttachmentDescriptors(initialKey, next, versions, persisted);
			}
		},
		[initialKey, onAttachmentsChange],
	);

	const commitAttachments = useCallback(
		(next: FileAttachment[]) => {
			if (initialKey) {
				stageSharedAttachmentRemovals(initialKey, attachmentsRef.current, next);
			}
			attachmentsRef.current = next;
			setAttachments(next);
			if (initialKey) {
				notifySharedAttachmentEntry(
					initialKey,
					{ attachments: sharedAttachmentDescriptors(next) },
					listenerTokenRef.current,
				);
			}
			reportAttachmentsChange(next);
		},
		[initialKey, reportAttachmentsChange],
	);

	const commitError = useCallback(
		(next: string | null) => {
			setValidationError(next);
			if (initialKey) notifySharedAttachmentEntry(initialKey, { error: next });
		},
		[initialKey],
	);

	const updateAttachment = useCallback(
		(id: string, update: (attachment: FileAttachment) => FileAttachment) => {
			const index = attachmentsRef.current.findIndex((attachment) => attachment.id === id);
			if (index < 0) return;
			const next = [...attachmentsRef.current];
			next[index] = update(next[index]);
			commitAttachments(next);
		},
		[commitAttachments],
	);

	useEffect(() => {
		if (initialKeyRef.current === initialKey) return;
		generationRef.current++;
		initialKeyRef.current = initialKey;
		const restored = restoreInitial(initialAttachments);
		attachmentsRef.current = restored;
		setAttachments(restored);
		sourceFilesRef.current = initialKey ? sharedEntry(initialKey).sources : new Map();
		setValidationError(null);
		setPreparing(sharedAttachmentPending(initialKey));
	}, [initialAttachments, initialKey, restoreInitial]);

	useEffect(() => {
		if (!initialKey) return;
		let activated = false;
		return subscribeSharedAttachmentWork(initialKey, listenerTokenRef.current, (update) => {
			const activating = !activated;
			const renderedRevision =
				renderedSharedRevisionRef.current.key === initialKey
					? renderedSharedRevisionRef.current.revision
					: -1;
			const shouldApplyAttachments =
				activated ||
				update.revision > renderedRevision ||
				update.persistedRevision !== update.revision;
			activated = true;
			if (activating && readPersistedAttachments) {
				// Activity can retain a render longer than the shared registry's empty
				// snapshot. Re-read the durable draft at commit so an evicted receipt
				// cannot leave stale render-time attachments active and resendable.
				const restored = restoreInitial(readPersistedAttachments());
				attachmentsRef.current = restored;
				setAttachments(restored);
			}
			if (update.attachments !== undefined && shouldApplyAttachments) {
				const restored = normalizeInitial(update.attachments);
				attachmentsRef.current = restored;
				setAttachments(restored);
				reportAttachmentsChange(restored);
			}
			if (update.error !== undefined) setValidationError(update.error);
			setPreparing(update.pending > 0);
		});
	}, [initialKey, normalizeInitial, readPersistedAttachments, reportAttachmentsChange, restoreInitial]);

	const readAttachment = useCallback(
		(id: string, file: File, sharedWork?: SharedAttachmentWork): Promise<void> => {
			const generation = generationRef.current;
			let pending: Promise<void>;
			pending = readFileAsBase64(file)
				.then((result) => {
					if (generationRef.current !== generation) return;
					if (
						initialKey &&
						sharedWork &&
						!sharedAttachmentWorkIsCurrent(initialKey, sharedWork)
					) {
						return;
					}
					const isImage = file.type.startsWith("image/") && isSupportedImageAttachment(file.type);
					updateAttachment(id, (attachment) => ({
						...attachment,
						status: "ready",
						dataUrl: isImage ? result.dataUrl : undefined,
						data: result.data,
					}));
				})
				.catch(() => {
					if (generationRef.current !== generation) return;
					if (
						initialKey &&
						sharedWork &&
						!sharedAttachmentWorkIsCurrent(initialKey, sharedWork)
					) {
						return;
					}
					updateAttachment(id, (attachment) => ({
						...attachment,
						status: "failed",
						dataUrl: undefined,
						data: undefined,
						stagedPath: undefined,
					}));
				})
				.finally(() => {
					if (pendingReadsRef.current.get(id) === pending) pendingReadsRef.current.delete(id);
				});
			pendingReadsRef.current.set(id, pending);
			return pending;
		},
		[initialKey, updateAttachment],
	);

	const stageReadyAttachments = useCallback(
		(ids?: ReadonlySet<string>, sharedWork?: SharedAttachmentWork): Promise<void> => {
			if (!prepareAttachments) return Promise.resolve();
			const generation = generationRef.current;
			const shared = initialKey ? sharedEntry(initialKey) : undefined;
			const queue = shared?.stagingQueue ?? stagingQueueRef.current;
			const run = queue.then(async () => {
				if (generationRef.current !== generation) return;
				if (initialKey && sharedWork && !sharedAttachmentWorkIsCurrent(initialKey, sharedWork)) return;
				// Select work only after earlier staging settles. A retry can otherwise
				// overlap its original add and stage the same attachment twice.
				const ready = attachmentsRef.current.filter(
					(attachment) =>
						(ids === undefined || ids.has(attachment.id)) &&
						attachment.status === "ready" &&
						!attachment.stagedPath &&
						attachment.data !== undefined,
				);
				if (ready.length === 0) return;
				const prepared = await prepareAttachments(ready);
				if (generationRef.current !== generation) return;
				if (initialKey && sharedWork && !sharedAttachmentWorkIsCurrent(initialKey, sharedWork)) return;
				if (prepared.length !== ready.length) {
					throw new Error("Attachment staging returned an incomplete result");
				}
				const preparedByID = new Map(prepared.map((attachment) => [attachment.id, attachment]));
				commitAttachments(
					attachmentsRef.current.map((attachment) => preparedByID.get(attachment.id) ?? attachment),
				);
				// The staged descriptor is now the durable retry boundary. Keep the
				// decoded bytes on the fresh attachment for native delivery, but release
				// the original File so a session-scoped registry cannot pin its Blob.
				for (const attachment of prepared) {
					if (!attachment.stagedPath) continue;
					sourceFilesRef.current.delete(attachment.id);
					shared?.sources.delete(attachment.id);
				}
			});
			const nextQueue = run.then(
				() => undefined,
				() => undefined,
			);
			if (shared) shared.stagingQueue = nextQueue;
			else stagingQueueRef.current = nextQueue;
			return run;
		},
		[commitAttachments, initialKey, prepareAttachments],
	);

	const processFiles = useCallback(async (files: File[], generation: number, sharedWork?: SharedAttachmentWork) => {
		if (generationRef.current !== generation) return;
		if (initialKey && sharedWork && !sharedAttachmentWorkIsCurrent(initialKey, sharedWork)) return;
		// Filter out directories - they have type "" and size 0 in most browsers
		const validFiles = files.filter((file) => {
			// Exclude directories (they typically have no type and size 0)
			// Also exclude items that might be folders based on common patterns
			if (file.type === "" && file.name.endsWith("/")) return false;
			// Some browsers report directories as size 0 with empty type
			if (file.type === "" && file.size === 0) return false;
			return true;
		});

		if (validFiles.length === 0) return;

		const errors = new Set<string>();
		// Block SVG files for security (active content)
		const blockedFiles = validFiles.filter(
			(file) => file.type.toLowerCase().trim() === "image/svg+xml",
		);
		if (blockedFiles.length > 0) {
			errors.add("SVG files are not supported for security reasons.");
		}
		const valid = validFiles.filter(
			(file) => file.type.toLowerCase().trim() !== "image/svg+xml",
		);

		// Reject oversized files before the (async) read.
		const readable = valid.filter((file) => {
			if (file.size > MAX_ATTACHMENT_BYTES) {
				errors.add(`Each file must be under ${mb(MAX_ATTACHMENT_BYTES)} MB.`);
				return false;
			}
			return true;
		});

		const accepted = [...attachmentsRef.current];
		let total = accepted.reduce((sum, attachment) => sum + attachment.bytes, 0);
		const fresh: Array<{ attachment: FileAttachment; file: File }> = [];
		for (const file of readable) {
			if (accepted.length + fresh.length >= MAX_ATTACHMENTS) {
				errors.add(`You can attach up to ${MAX_ATTACHMENTS} files.`);
				break;
			}
			if (total + file.size > MAX_ATTACHMENTS_BYTES) {
				// Only this file is refused: the remaining budget cannot absorb it,
				// but a later file in the same batch still can.
				errors.add(`Attachments must total under ${mb(MAX_ATTACHMENTS_BYTES)} MB.`);
				continue;
			}
			const id =
				typeof crypto !== "undefined" && "randomUUID" in crypto
					? crypto.randomUUID()
					: `${Date.now()}-${Math.random().toString(16).slice(2)}`;
			const attachment: FileAttachment = {
				id,
				mimeType: file.type || "application/octet-stream",
				bytes: file.size,
				name: file.name,
				status: "reading",
			};
			fresh.push({ attachment, file });
			sourceFilesRef.current.set(id, file);
			if (initialKey) sharedEntry(initialKey).sources.set(id, file);
			total += file.size;
		}
		if (fresh.length > 0) {
			if (initialKey && sharedWork) {
				registerSharedAttachmentWorkIds(
					initialKey,
					sharedWork,
					fresh.map(({ attachment }) => attachment.id),
				);
			}
			commitAttachments([...accepted, ...fresh.map(({ attachment }) => attachment)]);
		}
		commitError(errors.size > 0 ? Array.from(errors).join(" ") : null);
		await Promise.all(
			fresh.map(({ attachment, file }) => readAttachment(attachment.id, file, sharedWork)),
		);
		if (generationRef.current !== generation) return;
		if (initialKey && sharedWork && !sharedAttachmentWorkIsCurrent(initialKey, sharedWork)) return;
		try {
			await stageReadyAttachments(
				new Set(fresh.map(({ attachment }) => attachment.id)),
				sharedWork,
			);
		} catch {
			if (generationRef.current !== generation) return;
		if (initialKey && sharedWork && !sharedAttachmentWorkIsCurrent(initialKey, sharedWork)) return;
			commitError("Files couldn’t be saved. Retry sending to save them again.");
		}
	}, [commitAttachments, commitError, initialKey, readAttachment, stageReadyAttachments]);

	const addFiles = useCallback((files: Iterable<File>): Promise<void> => {
		// Serialize batches. Two paste/drop events can arrive before React publishes
		// `preparing`; processing both against the same attachment snapshot could
		// otherwise exceed count/byte caps or overwrite one batch with the other.
		const batch = Array.from(files);
		if (batch.length === 0) return Promise.resolve();
		const sharedKey = initialKey;
		const generation = generationRef.current;
		const sharedWork = sharedKey ? beginSharedAttachmentWork(sharedKey) : undefined;
		queuedAddsRef.current += 1;
		setPreparing(true);
		// Preserve the hook's existing contract: the first FileReader starts in the
		// same turn as addFiles. Only a later overlapping batch needs to wait for the
		// current queue, otherwise callers cannot observe the pending read immediately.
		const run =
			queuedAddsRef.current === 1
				? processFiles(batch, generation, sharedWork)
				: addQueueRef.current.then(() => processFiles(batch, generation, sharedWork));
		const settled = run
			.catch(() => {
				if (generationRef.current !== generation) return;
				commitError("Some files couldn’t be prepared and were skipped.");
			})
			.finally(() => {
				queuedAddsRef.current = Math.max(0, queuedAddsRef.current - 1);
				if (sharedKey && sharedWork) endSharedAttachmentWork(sharedKey, sharedWork.token);
				else if (queuedAddsRef.current === 0 && pendingRetriesRef.current.size === 0) {
					setPreparing(false);
				}
			});
		addQueueRef.current = settled;
		return settled;
	}, [commitError, initialKey, processFiles]);

	const performRetry = useCallback(
		async (id: string): Promise<boolean> => {
			const file = sourceFilesRef.current.get(id);
			if (!file) return false;
			const existing = pendingReadsRef.current.get(id);
			if (existing) {
				await existing;
				const current = attachmentsRef.current.find((attachment) => attachment.id === id);
				return Boolean(current?.status === "ready" && (!prepareAttachments || current.stagedPath));
			}
			const sharedKey = initialKey;
			const sharedWork = sharedKey ? beginSharedAttachmentWork(sharedKey) : undefined;
			if (sharedKey && sharedWork) registerSharedAttachmentWorkIds(sharedKey, sharedWork, [id]);
			setPreparing(true);
			commitError(null);
			updateAttachment(id, (attachment) => ({
				...attachment,
				status: "reading",
				dataUrl: undefined,
				data: undefined,
				stagedPath: undefined,
			}));
			try {
				await readAttachment(id, file, sharedWork);
				if (sharedKey && sharedWork && !sharedAttachmentWorkIsCurrent(sharedKey, sharedWork)) {
					return false;
				}
				if (attachmentsRef.current.find((attachment) => attachment.id === id)?.status === "ready") {
					try {
						await stageReadyAttachments(new Set([id]), sharedWork);
					} catch {
						commitError("Files couldn’t be saved. Retry sending to save them again.");
					}
				}
				const current = attachmentsRef.current.find((attachment) => attachment.id === id);
				return Boolean(current?.status === "ready" && (!prepareAttachments || current.stagedPath));
			} finally {
				if (sharedKey && sharedWork) endSharedAttachmentWork(sharedKey, sharedWork.token);
			}
		},
		[
			commitError,
			initialKey,
			prepareAttachments,
			readAttachment,
			stageReadyAttachments,
			updateAttachment,
		],
	);
	const retry = useCallback(
		(id: string): Promise<boolean> => {
			let pending: Promise<boolean>;
			pending = performRetry(id).finally(() => {
				pendingRetriesRef.current.delete(pending);
				if (!initialKey && queuedAddsRef.current === 0 && pendingRetriesRef.current.size === 0) {
					setPreparing(false);
				}
			});
			pendingRetriesRef.current.add(pending);
			return pending;
		},
		[initialKey, performRetry],
	);

	const remove = useCallback((id: string) => {
		const next = attachmentsRef.current.filter((a) => a.id !== id);
		sourceFilesRef.current.delete(id);
		pendingReadsRef.current.delete(id);
		if (initialKey) sharedEntry(initialKey).sources.delete(id);
		commitAttachments(next);
		commitError(null);
	}, [commitAttachments, commitError, initialKey]);

	const clear = useCallback(() => {
		generationRef.current++;
		sourceFilesRef.current.clear();
		pendingReadsRef.current.clear();
		if (initialKey) sharedEntry(initialKey).sources.clear();
		commitAttachments([]);
		commitError(null);
	}, [commitAttachments, commitError, initialKey]);

	const releaseStagedPayloadData = useCallback(() => {
		let changed = false;
		const next = attachmentsRef.current.map((attachment) => {
			if (
				!attachment.stagedPath ||
				(attachment.data === undefined && attachment.dataUrl === undefined)
			) {
				return attachment;
			}
			changed = true;
			return { ...attachment, data: undefined, dataUrl: undefined };
		});
		if (changed) commitAttachments(next);
	}, [commitAttachments]);

	const payloadsFor = useCallback(
		(current: FileAttachment[]): FileAttachmentPayload[] =>
			current.flatMap(({ mimeType, data, name, status }) =>
				status === "ready" && data !== undefined ? [{ mimeType, data, name }] : [],
			),
		[],
	);

	const reconcilePersistedAttachments = useCallback((persisted: FileAttachment[]) => {
		// A hidden React Activity renders before its effects subscribe. By the time it
		// reconnects, another same-scope surface may have accepted and cleared the
		// durable draft. Prefer a live shared descriptor snapshot when one exists (it
		// owns pending work and failed-persistence recovery); otherwise re-seed from
		// storage at the commit boundary instead of reviving render-time descriptors.
		const shared = initialKey
			? sharedAttachmentEntries.get(initialKey)?.attachments
			: undefined;
		const next = restoreInitial(shared ?? persisted);
		attachmentsRef.current = next;
		setAttachments(next);
	}, [initialKey, restoreInitial]);

	const toPayload = useCallback(
		(): FileAttachmentPayload[] => payloadsFor(attachments),
		[attachments, payloadsFor],
	);

	const toSettledPayload = useCallback(async (): Promise<FileAttachmentPayload[]> => {
		const generation = generationRef.current;
		const entry = initialKey ? sharedAttachmentEntries.get(initialKey) : undefined;
		const cancellationRevision = entry?.cancellationRevision;
		const assertStillOwned = () => {
			if (generationRef.current !== generation || entry?.cancellationRevision !== cancellationRevision) {
				throw new Error("Attachments were discarded before delivery. Review the draft and send again.");
			}
		};
		if (initialKey) await waitForSharedAttachmentWork(initialKey);
		while (queuedAddsRef.current > 0) {
			const queued = addQueueRef.current;
			await queued;
			if (queued === addQueueRef.current && queuedAddsRef.current === 0) break;
		}
		while (pendingReadsRef.current.size > 0) {
			await Promise.allSettled(Array.from(pendingReadsRef.current.values()));
		}
		while (pendingRetriesRef.current.size > 0) {
			const pending = [...pendingRetriesRef.current];
			await Promise.allSettled(pending);
			if (pendingRetriesRef.current.size === 0) break;
		}
		assertStillOwned();
		const currentBeforeStage = attachmentsRef.current;
		if (currentBeforeStage.some(({ status }) => status === "failed")) {
			throw new Error("Some files couldn't be read. Retry or remove them before sending.");
		}
		if (currentBeforeStage.some(({ status }) => status !== "ready")) {
			throw new Error("Some files are still being read. Wait before sending.");
		}
		if (prepareAttachments && currentBeforeStage.some(({ stagedPath }) => !stagedPath)) {
			const sharedKey = initialKey;
			const sharedWork = sharedKey ? beginSharedAttachmentWork(sharedKey) : undefined;
			const unstaged = currentBeforeStage.filter(({ stagedPath }) => !stagedPath);
			if (sharedKey && sharedWork) {
				registerSharedAttachmentWorkIds(
					sharedKey,
					sharedWork,
					unstaged.map(({ id }) => id),
				);
			}
			setPreparing(true);
			const rehydratedIDs = new Set<string>();
			try {
				// A failed staging attempt keeps the source for retry, while shared
				// descriptors intentionally omit native bytes. Rehydrate those bytes at
				// the next approved send boundary before trying durable staging again.
				await Promise.all(
					unstaged.flatMap((attachment) => {
						if (attachment.data !== undefined) return [];
						const source = sourceFilesRef.current.get(attachment.id);
						if (!source) return [];
						rehydratedIDs.add(attachment.id);
						updateAttachment(attachment.id, (current) => ({
							...current,
							status: "reading",
							dataUrl: undefined,
							data: undefined,
						}));
						return [readAttachment(attachment.id, source, sharedWork)];
					}),
				);
				if (attachmentsRef.current.some(({ status }) => status === "failed")) {
					throw new Error("Attachment source could not be reread");
				}
				await stageReadyAttachments(undefined, sharedWork);
				if (attachmentsRef.current.some(({ stagedPath }) => !stagedPath)) {
					throw new Error("Attachment staging did not produce durable paths");
				}
				commitError(null);
			} catch {
				const message = attachmentsRef.current.some(({ status }) => status === "failed")
					? "Some files couldn't be read. Retry or remove them before sending."
					: "The files could not be attached. Nothing was sent.";
				commitError(message);
				throw new Error(message);
			} finally {
				if (rehydratedIDs.size > 0) {
					// Bytes recovered solely for warm-remount staging never become a
					// provider payload, even when this staging attempt fails again.
					commitAttachments(
						attachmentsRef.current.map((attachment) =>
							rehydratedIDs.has(attachment.id)
								? { ...attachment, data: undefined, dataUrl: undefined }
								: attachment,
						),
					);
				}
				if (sharedKey && sharedWork) endSharedAttachmentWork(sharedKey, sharedWork.token);
				else {
					setPreparing(queuedAddsRef.current > 0 || pendingRetriesRef.current.size > 0);
				}
			}
		}
		if (prepareAttachments && attachmentsRef.current.some(({ stagedPath }) => !stagedPath)) {
			throw new Error("The files are not durably available. Nothing was sent.");
		}
		assertStillOwned();
		return payloadsFor(attachmentsRef.current);
	}, [
		commitAttachments,
		commitError,
		initialKey,
		payloadsFor,
		prepareAttachments,
		readAttachment,
		stageReadyAttachments,
		updateAttachment,
	]);

	const hasAttachments = useCallback(() => attachmentsRef.current.length > 0, []);
	const currentAttachments = useCallback(() => attachmentsRef.current, []);
	const signature = useCallback(
		() => attachmentsRef.current.map((attachment) => attachment.id).join(":"),
		[],
	);
	const hasUndurableAttachments = attachments.some(({ stagedPath }) => !stagedPath);
	const failed = attachments.filter(({ status }) => status === "failed");
	const readError =
		failed.length === 0
			? null
			: failed.length === 1
				? `${failed[0].name} couldn't be read. Retry or remove it.`
				: `${failed.length} files couldn't be read. Retry or remove them.`;
	const error = readError ?? validationError;

	return {
		attachments,
		error,
		preparing,
		addFiles,
		retry,
		remove,
		clear,
		reconcilePersistedAttachments,
		releaseStagedPayloadData,
		getAttachments: currentAttachments,
		attachmentSignature: signature,
		hasPendingReads: () => pendingReadsRef.current.size > 0 || sharedAttachmentPending(initialKey),
		toPayload,
		toSettledPayload,
		hasAttachments,
		currentAttachments,
		signature,
		hasUndurableAttachments,
	};
}
