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
	/**
	 * Make newly accepted bytes durable before their chips become visible. A
	 * rejection leaves the existing attachment list untouched.
	 */
	prepareAttachments?: (attachments: FileAttachment[]) => Promise<FileAttachment[]>;
	/** Called after an accepted add, removal, or clear. */
	onAttachmentsChange?: (attachments: FileAttachment[]) => void;
};

type SharedAttachmentUpdate = {
	pending: number;
	attachments?: FileAttachment[];
	error?: string | null;
};

type SharedAttachmentEntry = {
	pending: Set<symbol>;
	generation: number;
	listeners: Map<symbol, (update: SharedAttachmentUpdate) => void>;
	attachments?: FileAttachment[];
	error?: string | null;
};

type SharedAttachmentWork = { token: symbol; generation: number };

type CapturedPendingFileAttachmentEntry = {
	key: string;
	generation: number;
	tokens: readonly symbol[];
};

const pendingFileAttachmentCaptureEntries = Symbol("pending-file-attachment-capture");

/** Opaque handle for attachment work that was pending at a user-approved boundary. */
export type PendingFileAttachmentCapture = {
	readonly [pendingFileAttachmentCaptureEntries]: readonly CapturedPendingFileAttachmentEntry[];
};

function sharedAttachmentDescriptors(attachments: FileAttachment[]): FileAttachment[] {
	return attachments.map(({ id, mimeType, bytes, name, stagedPath }) => ({
		id,
		mimeType,
		bytes,
		name,
		...(stagedPath ? { stagedPath } : {}),
	}));
}

// Staging belongs to the AO session, not to one React mount. A controller or
// surface remount can happen while the daemon is writing bytes; keeping this
// tiny registry lets the replacement hook remain pending and receive the staged
// descriptors instead of restoring storage before the write and missing them.
const sharedAttachmentEntries = new Map<string, SharedAttachmentEntry>();

function sharedEntry(key: string): SharedAttachmentEntry {
	let entry = sharedAttachmentEntries.get(key);
	if (!entry) {
		entry = { pending: new Set(), generation: 0, listeners: new Map() };
		sharedAttachmentEntries.set(key, entry);
	}
	return entry;
}

function notifySharedAttachmentEntry(
	key: string,
	update: Omit<SharedAttachmentUpdate, "pending"> = {},
	originToken?: symbol,
): void {
	const entry = sharedAttachmentEntries.get(key);
	if (!entry) return;
	if (update.attachments !== undefined) entry.attachments = update.attachments;
	if (update.error !== undefined) entry.error = update.error;
	const notification = { pending: entry.pending.size, ...update };
	for (const [token, listener] of entry.listeners) {
		if (token !== originToken) listener(notification);
	}
}

function beginSharedAttachmentWork(key: string): SharedAttachmentWork {
	const entry = sharedEntry(key);
	const work = { token: Symbol("chat-attachment-work"), generation: entry.generation };
	entry.pending.add(work.token);
	notifySharedAttachmentEntry(key);
	return work;
}

function endSharedAttachmentWork(key: string, token: symbol): void {
	const entry = sharedAttachmentEntries.get(key);
	if (!entry) return;
	entry.pending.delete(token);
	notifySharedAttachmentEntry(key);
	if (
		entry.pending.size === 0 &&
		entry.listeners.size === 0 &&
		(entry.attachments?.length ?? 0) === 0
	) {
		sharedAttachmentEntries.delete(key);
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
		...(entry.attachments !== undefined ? { attachments: entry.attachments } : {}),
		...(entry.error !== undefined ? { error: entry.error } : {}),
	});
	return () => {
		entry.listeners.delete(token);
		if (
			entry.pending.size === 0 &&
			entry.listeners.size === 0 &&
			(entry.attachments?.length ?? 0) === 0
		) {
			sharedAttachmentEntries.delete(key);
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
	entry.pending.clear();
	notifySharedAttachmentEntry(key, { error: null });
}

function attachmentKeyBelongsToSession(key: string, sessionId: string): boolean {
	return key === sessionId || chatDraftScopeSessionId(key) === sessionId;
}

/** Capture only the work that is pending when the user confirms leaving Chat. */
export function capturePendingFileAttachmentsForSession(
	sessionId: string,
): PendingFileAttachmentCapture {
	const entries: CapturedPendingFileAttachmentEntry[] = [];
	for (const [key, entry] of sharedAttachmentEntries) {
		if (!attachmentKeyBelongsToSession(key, sessionId) || entry.pending.size === 0) continue;
		entries.push({ key, generation: entry.generation, tokens: [...entry.pending] });
	}
	return { [pendingFileAttachmentCaptureEntries]: entries };
}

/**
 * Cancel exactly the pending work represented by a prior confirmation. Work
 * begun afterward remains current and can finish into the recoverable draft.
 */
export function discardCapturedPendingFileAttachments(
	capture: PendingFileAttachmentCapture,
): void {
	for (const captured of capture[pendingFileAttachmentCaptureEntries]) {
		const entry = sharedAttachmentEntries.get(captured.key);
		if (!entry || entry.generation !== captured.generation) continue;
		let changed = false;
		for (const token of captured.tokens) {
			changed = entry.pending.delete(token) || changed;
		}
		if (!changed) continue;
		notifySharedAttachmentEntry(captured.key);
		if (
			entry.pending.size === 0 &&
			entry.listeners.size === 0 &&
			(entry.attachments?.length ?? 0) === 0
		) {
			sharedAttachmentEntries.delete(captured.key);
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
	entry.pending.clear();
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
		prepareAttachments,
		onAttachmentsChange,
	} = options;
	const [attachments, setAttachments] = useState<FileAttachment[]>(() => initialAttachments);
	const [error, setError] = useState<string | null>(null);
	const [preparing, setPreparing] = useState(() => sharedAttachmentPending(initialKey));
	const attachmentsRef = useRef<FileAttachment[]>(initialAttachments);
	const initialKeyRef = useRef(initialKey);
	const listenerTokenRef = useRef(Symbol("file-attachment-listener"));
	const pendingReadsRef = useRef<Set<Promise<unknown>>>(new Set());
	const addQueueRef = useRef<Promise<void>>(Promise.resolve());
	const queuedAddsRef = useRef(0);
	const generationRef = useRef(0);

	useEffect(() => {
		if (initialKeyRef.current === initialKey) return;
		generationRef.current++;
		initialKeyRef.current = initialKey;
		attachmentsRef.current = initialAttachments;
		setAttachments(initialAttachments);
		setError(null);
		setPreparing(sharedAttachmentPending(initialKey));
	}, [initialAttachments, initialKey]);

	useEffect(() => {
		if (!initialKey) return;
		return subscribeSharedAttachmentWork(initialKey, listenerTokenRef.current, (update) => {
			if (update.attachments) {
				attachmentsRef.current = update.attachments;
				setAttachments(update.attachments);
				onAttachmentsChange?.(update.attachments);
			}
			if (update.error !== undefined) setError(update.error);
			setPreparing(update.pending > 0);
		});
	}, [initialKey, onAttachmentsChange]);

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

		const pendingReads = Promise.all(
			readable.map((file) =>
				readFileAsBase64(file).catch(() => null).then((result) => ({ file, result })),
			),
		);
		pendingReadsRef.current.add(pendingReads);
		const results = await pendingReads;
		pendingReadsRef.current.delete(pendingReads);
		if (generationRef.current !== generation) return;
		if (initialKey && sharedWork && !sharedAttachmentWorkIsCurrent(initialKey, sharedWork)) return;

		const fresh: FileAttachment[] = [];
		for (const { file, result } of results) {
			if (!result) {
				errors.add(`Some files couldn't be read and were skipped.`);
				continue;
			}
			const isImage = file.type.startsWith("image/") && isSupportedImageAttachment(file.type);
			fresh.push({
				id:
					typeof crypto !== "undefined" && "randomUUID" in crypto
						? crypto.randomUUID()
						: `${Date.now()}-${Math.random().toString(16).slice(2)}`,
				mimeType: file.type || "application/octet-stream",
				bytes: file.size,
				name: file.name,
				dataUrl: isImage ? result.dataUrl : undefined,
				data: result.data,
			});
		}

		const accepted = [...attachmentsRef.current];
		const acceptedFresh: FileAttachment[] = [];
		let total = accepted.reduce((sum, a) => sum + a.bytes, 0);
		for (const a of fresh) {
			if (accepted.length >= MAX_ATTACHMENTS) {
				errors.add(`You can attach up to ${MAX_ATTACHMENTS} files.`);
				break;
			}
			if (total + a.bytes > MAX_ATTACHMENTS_BYTES) {
				// Only this file is refused: the remaining budget cannot absorb it,
				// but a later file in the same batch still can. Aborting here (break)
				// would silently drop every smaller file staged after it.
				errors.add(`Attachments must total under ${mb(MAX_ATTACHMENTS_BYTES)} MB.`);
				continue;
			}
			accepted.push(a);
			acceptedFresh.push(a);
			total += a.bytes;
		}
		if (acceptedFresh.length > 0) {
			let prepared = acceptedFresh;
			if (prepareAttachments) {
				try {
					prepared = await prepareAttachments(acceptedFresh);
					if (generationRef.current !== generation) return;
					if (initialKey && sharedWork && !sharedAttachmentWorkIsCurrent(initialKey, sharedWork)) return;
					if (prepared.length !== acceptedFresh.length) {
						throw new Error("Attachment staging returned an incomplete result");
					}
				} catch {
					if (generationRef.current !== generation) return;
					if (initialKey && sharedWork && !sharedAttachmentWorkIsCurrent(initialKey, sharedWork)) return;
					errors.add("Files couldn’t be saved. Nothing was attached.");
					const message = Array.from(errors).join(" ");
					setError(message);
					if (initialKey) notifySharedAttachmentEntry(initialKey, { error: message });
					return;
				}
			}
			// Read the live list after async preparation. A removal that happened while
			// bytes were being staged must not resurrect an older attachment snapshot.
			const next = [...attachmentsRef.current, ...prepared];
			attachmentsRef.current = next;
			setAttachments(next);
			onAttachmentsChange?.(next);
			if (initialKey) {
				notifySharedAttachmentEntry(
					initialKey,
					{ attachments: sharedAttachmentDescriptors(next) },
					listenerTokenRef.current,
				);
			}
		}
		const nextError = errors.size > 0 ? Array.from(errors).join(" ") : null;
		setError(nextError);
		if (initialKey) notifySharedAttachmentEntry(initialKey, { error: nextError });
	}, [initialKey, onAttachmentsChange, prepareAttachments]);

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
				setError("Some files couldn’t be prepared and were skipped.");
			})
			.finally(() => {
				queuedAddsRef.current = Math.max(0, queuedAddsRef.current - 1);
				if (sharedKey && sharedWork) endSharedAttachmentWork(sharedKey, sharedWork.token);
				else if (queuedAddsRef.current === 0) setPreparing(false);
			});
		addQueueRef.current = settled;
		return settled;
	}, [initialKey, processFiles]);

	const remove = useCallback((id: string) => {
		const next = attachmentsRef.current.filter((a) => a.id !== id);
		attachmentsRef.current = next;
		setAttachments(next);
		onAttachmentsChange?.(next);
		if (initialKey) {
			notifySharedAttachmentEntry(
				initialKey,
				{ attachments: sharedAttachmentDescriptors(next), error: null },
				listenerTokenRef.current,
			);
		}
		setError(null);
	}, [initialKey, onAttachmentsChange]);

	const clear = useCallback(() => {
		generationRef.current++;
		pendingReadsRef.current.clear();
		attachmentsRef.current = [];
		setAttachments([]);
		onAttachmentsChange?.([]);
		if (initialKey) {
			notifySharedAttachmentEntry(
				initialKey,
				{ attachments: [], error: null },
				listenerTokenRef.current,
			);
		}
		setError(null);
	}, [initialKey, onAttachmentsChange]);

	const reconcilePersistedAttachments = useCallback((persisted: FileAttachment[]) => {
		// A hidden React Activity renders before its effects subscribe. By the time it
		// reconnects, another same-scope surface may have accepted and cleared the
		// durable draft. Prefer a live shared descriptor snapshot when one exists (it
		// owns pending work and failed-persistence recovery); otherwise re-seed from
		// storage at the commit boundary instead of reviving render-time descriptors.
		const shared = initialKey
			? sharedAttachmentEntries.get(initialKey)?.attachments
			: undefined;
		const next = shared ?? persisted;
		attachmentsRef.current = next;
		setAttachments(next);
	}, [initialKey]);

	const toPayload = useCallback(
		(): FileAttachmentPayload[] =>
			attachments.flatMap(({ mimeType, data }) =>
				data ? [{ mimeType, data }] : [],
			),
		[attachments],
	);

	const toSettledPayload = useCallback(async (): Promise<FileAttachmentPayload[]> => {
		while (queuedAddsRef.current > 0) {
			const queued = addQueueRef.current;
			await queued;
			if (queued === addQueueRef.current && queuedAddsRef.current === 0) break;
		}
		while (pendingReadsRef.current.size > 0) {
			await Promise.allSettled(Array.from(pendingReadsRef.current));
		}
		return attachmentsRef.current.flatMap(({ mimeType, data }) =>
			data ? [{ mimeType, data }] : [],
		);
	}, []);
	// Read from the same ref as toSettledPayload so a submit resumed after FileReader
	// completion can cache staged paths without waiting for another React render.
	const attachmentSignature = useCallback(
		() => attachmentsRef.current.map((attachment) => attachment.id).join(":"),
		[],
	);
	const hasPendingReads = useCallback(
		() => pendingReadsRef.current.size > 0 || sharedAttachmentPending(initialKey),
		[initialKey],
	);

	return {
		attachments,
		error,
		preparing,
		addFiles,
		remove,
		clear,
		reconcilePersistedAttachments,
		getAttachments: () => attachmentsRef.current,
		toPayload,
		toSettledPayload,
		attachmentSignature,
		hasPendingReads,
	};
}
