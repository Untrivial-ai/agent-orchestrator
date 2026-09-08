
/**
 * Renderer-owned, session-scoped Chat drafts.
 *
 * Electron pins this origin's storage beneath AO's userData directory, so a
 * synchronous localStorage write survives Chat surface teardown, renderer
 * reload, and a supported app restart without putting daemon state in the UI.
 * Attachment bytes are deliberately absent: a descriptor is written only after
 * the daemon has durably staged those bytes in the session worktree.
 */

export const CHAT_DRAFT_SCHEMA_VERSION = 2 as const;
const CHAT_DRAFT_SCOPE_SCHEMA_VERSION = 1 as const;

/**
 * One immutable AO session incarnation. Session ids are human-stable handles and
 * may be reused after authoritative deletion; createdAt (or another daemon-owned
 * UUID) is what prevents an old renderer draft from entering the replacement.
 */
export interface ChatDraftScope {
	sessionId: string;
	incarnation: string;
}

type ChatDraftScopeInput = string | ChatDraftScope;

type ChatDraftScopeLease = {
	schemaVersion: typeof CHAT_DRAFT_SCOPE_SCHEMA_VERSION;
	sessionId: string;
	incarnation: string;
	state: "active" | "activating";
	previousIncarnation?: string;
};

export type ChatDraftScopeActivationResult =
	| { ok: true; replaced: boolean; previousIncarnation?: string }
	| { ok: false; reason: "obsolete" | "storage" };

export interface ChatDraftAttachment {
	id: string;
	path: string;
	name: string;
	mimeType: string;
	bytes: number;
}

export type ChatDraftDeliveryState = "dispatching" | "accepted";

export interface ChatComposerDelivery {
	/** Original request mode, kept across capability changes during recovery. */
	nativeImages?: boolean;
	kind: "send" | "steer";
	state: ChatDraftDeliveryState;
	/** Exact composer revision this delivery was created from. */
	revision: number;
	/** Stable daemon idempotency key. Reused for every recovery attempt. */
	clientMessageId: string;
	/** Exact API text, including durable attachment path references. */
	requestText: string;
}

export interface ChatSessionDraft {
	schemaVersion: typeof CHAT_DRAFT_SCHEMA_VERSION;
	sessionId: string;
	incarnation: string;
	composer: {
		/** Changes whenever text or attachments change. Used for accepted-send CAS. */
		revision: number;
		text: string;
		attachments: ChatDraftAttachment[];
		/** Durable delivery journal. Present until acceptance is durably cleared. */
		delivery?: ChatComposerDelivery;
	};
	queuedEdit?: ChatDraftQueuedEdit;
}

export type DraftStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

export type DraftWriteResult = { ok: true; draft: ChatSessionDraft } | { ok: false; draft: ChatSessionDraft };

export type DraftClearResult =
	| { ok: true; cleared: boolean; draft: ChatSessionDraft }
	| { ok: false; cleared: false; draft: ChatSessionDraft };

export type DraftDeliveryResult<Mutation> =
	| { ok: true; recovered: boolean; draft: ChatSessionDraft; mutation: Mutation }
	| { ok: false; recovered: boolean; draft: ChatSessionDraft; mutation?: Mutation };

export interface PrepareChatComposerDeliveryInput {
	nativeImages?: boolean;
	kind: ChatComposerDelivery["kind"];
	composerText: string;
	attachments: ChatDraftAttachment[];
	requestText: string;
	clientMessageId: string;
}

export type ChatDraftMutationToken = symbol;

export interface ChatDraftMutationSnapshot<Revision> {
	pending: boolean;
	accepted?: {
		sequence: number;
		revision: Revision;
		result: DraftClearResult;
	};
}

type DraftMutationKind = "composer";

type ChatDraftRuntime = {
	sessionId: string;
	listeners: Set<() => void>;
	composerToken?: ChatDraftMutationToken;
	composer: ChatDraftMutationSnapshot<number>;
};

const EMPTY_COMPOSER_MUTATION: ChatDraftMutationSnapshot<number> = { pending: false };
const draftRuntimes = new Map<string, ChatDraftRuntime>();
let draftMutationSequence = 0;

function normalizeScope(scope: ChatDraftScopeInput): ChatDraftScope {
	return typeof scope === "string"
		? { sessionId: scope, incarnation: scope }
		: scope;
}

export function chatDraftScopeKey(scope: ChatDraftScopeInput): string {
	const identity = normalizeScope(scope);
	return JSON.stringify([identity.sessionId, identity.incarnation]);
}

export function chatQueuedAttachmentScopeKey(scope: ChatDraftScope, editorId: string): string {
	return JSON.stringify([scope.sessionId, scope.incarnation, `queue:${editorId}`]);
}

export function chatDraftScopeSessionId(key: string): string | undefined {
	try {
		const parsed: unknown = JSON.parse(key);
		return Array.isArray(parsed) &&
			(parsed.length === 2 || (parsed.length === 3 && typeof parsed[2] === "string" && parsed[2].startsWith("queue:"))) &&
			typeof parsed[0] === "string" &&
			typeof parsed[1] === "string"
			? parsed[0]
			: undefined;
	} catch {
		return undefined;
	}
}

function draftRuntimeKey(scope: ChatDraftScopeInput): string {
	return chatDraftScopeKey(scope);
}

function purgeDraftRuntimes(sessionId: string): void {
	for (const [key, runtime] of draftRuntimes) {
		if (runtime.sessionId !== sessionId) continue;
		runtime.composerToken = undefined;

		runtime.composer = EMPTY_COMPOSER_MUTATION;

		draftRuntimes.delete(key);
	}
}

function draftRuntime(scope: ChatDraftScopeInput): ChatDraftRuntime {
	const identity = normalizeScope(scope);
	const key = draftRuntimeKey(identity);
	let runtime = draftRuntimes.get(key);
	if (!runtime) {
		runtime = {
			sessionId: identity.sessionId,
			listeners: new Set(),
			composer: EMPTY_COMPOSER_MUTATION,
		};
		draftRuntimes.set(key, runtime);
	}
	return runtime;
}

function evictSettledDraftRuntime(key: string, runtime: ChatDraftRuntime): void {
	if (
		runtime.listeners.size > 0 ||
		runtime.composer.pending ||
		runtime.composer.accepted ||
		draftRuntimes.get(key) !== runtime
	) return;
	draftRuntimes.delete(key);
}

function clearDraftMutationReceipt(
	runtime: ChatDraftRuntime,
	kind: DraftMutationKind,
	pending?: boolean,
): void {
	if (kind === "composer") {
		runtime.composer = { pending: pending ?? runtime.composer.pending };
		return;
	}

}

function emitDraftRuntime(runtime: ChatDraftRuntime): void {
	for (const listener of runtime.listeners) listener();
}

export function subscribeChatDraftRuntime(scope: ChatDraftScopeInput, listener: () => void): () => void {
	if (!normalizeScope(scope).sessionId) return () => undefined;
	const key = draftRuntimeKey(scope);
	const runtime = draftRuntime(scope);
	runtime.listeners.add(listener);
	return () => {
		runtime.listeners.delete(listener);
		evictSettledDraftRuntime(key, runtime);
	};
}

export function getChatComposerMutation(
	scope: ChatDraftScopeInput,
): ChatDraftMutationSnapshot<number> {
	const runtime = draftRuntimes.get(draftRuntimeKey(scope));
	if (!runtime) return EMPTY_COMPOSER_MUTATION;
	return runtime.composer;
}

/** Async preparation may outlive authoritative replacement of this session. */
export function isChatComposerMutationCurrent(
	scope: ChatDraftScopeInput,
	token: ChatDraftMutationToken,
): boolean {
	return draftRuntimes.get(draftRuntimeKey(scope))?.composerToken === token;
}

function beginDraftMutation(
	scope: ChatDraftScopeInput,
	kind: DraftMutationKind,
): ChatDraftMutationToken | undefined {
	if (!normalizeScope(scope).sessionId) return undefined;
	const runtime = draftRuntime(scope);
	if (kind === "composer") {
		if (runtime.composer.pending || runtime.composer.accepted) return undefined;
		const token = Symbol("chat-composer-mutation");
		runtime.composerToken = token;
		runtime.composer = { pending: true };
		emitDraftRuntime(runtime);
		return token;
	}
	return undefined;
}

export function beginChatComposerMutation(scope: ChatDraftScopeInput): ChatDraftMutationToken | undefined {
	return beginDraftMutation(scope, "composer");
}

function cancelDraftMutation(
	scope: ChatDraftScopeInput,
	kind: DraftMutationKind,
	token: ChatDraftMutationToken,
): void {
	const key = draftRuntimeKey(scope);
	const runtime = draftRuntimes.get(key);
	if (!runtime) return;
	if (kind === "composer") {
		if (runtime.composerToken !== token) return;
		runtime.composerToken = undefined;
		clearDraftMutationReceipt(runtime, "composer", false);
	}
	emitDraftRuntime(runtime);
	evictSettledDraftRuntime(key, runtime);
}

export function cancelChatComposerMutation(
	scope: ChatDraftScopeInput,
	token: ChatDraftMutationToken,
): void {
	cancelDraftMutation(scope, "composer", token);
}

export function finishChatComposerMutation(
	scope: ChatDraftScopeInput,
	token: ChatDraftMutationToken,
	revision: number,
	result: DraftClearResult,
): void {
	const key = draftRuntimeKey(scope);
	const runtime = draftRuntimes.get(key);
	if (!runtime || runtime.composerToken !== token) return;
	runtime.composerToken = undefined;
	const sequence = ++draftMutationSequence;
	runtime.composer = {
		pending: false,
		accepted: { sequence, revision, result },
	};
	emitDraftRuntime(runtime);
	evictSettledDraftRuntime(key, runtime);
}

function acknowledgeDraftMutation(
	scope: ChatDraftScopeInput,
	kind: DraftMutationKind,
	sequence: number,
): void {
	const key = draftRuntimeKey(scope);
	const runtime = draftRuntimes.get(key);
	if (!runtime) return;
	if (kind === "composer") {
		if (runtime.composer.accepted?.sequence !== sequence) return;
		clearDraftMutationReceipt(runtime, "composer");
	}
	emitDraftRuntime(runtime);
	evictSettledDraftRuntime(key, runtime);
}

/**
 * Release a settled composer receipt only after a committed subscriber applied
 * it. No task duration or global pressure decides this lifetime. Each exact
 * session incarnation owns at most one composer receipt;
 * acknowledgement, a superseding durable edit, or authoritative incarnation
 * cleanup retires them.
 */
export function acknowledgeChatComposerMutation(
	scope: ChatDraftScopeInput,
	sequence: number,
): void {
	acknowledgeDraftMutation(scope, "composer", sequence);
}

function invalidateAcceptedDraftMutation(scope: ChatDraftScopeInput, kind: DraftMutationKind): void {
	const key = draftRuntimeKey(scope);
	const runtime = draftRuntimes.get(key);
	if (!runtime) return;
	if (kind === "composer") {
		if (!runtime.composer.accepted) return;
		clearDraftMutationReceipt(runtime, "composer");
	}
	emitDraftRuntime(runtime);
	evictSettledDraftRuntime(key, runtime);
}

function storageKey(sessionId: string): string {
	// The key remains stable across schema versions so a future decoder can find
	// and migrate the prior record rather than orphaning it under a v1-only key.
	return `ao.chat.draft:${encodeURIComponent(sessionId)}`;
}

function scopeLeaseStorageKey(sessionId: string): string {
	return `ao.chat.draft.active:${encodeURIComponent(sessionId)}`;
}

function isScopeLease(value: unknown, sessionId: string): value is ChatDraftScopeLease {
	if (!value || typeof value !== "object") return false;
	const lease = value as Partial<ChatDraftScopeLease>;
	return (
		lease.schemaVersion === CHAT_DRAFT_SCOPE_SCHEMA_VERSION &&
		lease.sessionId === sessionId &&
		typeof lease.incarnation === "string" &&
		lease.incarnation.length > 0 &&
		(lease.state === "active" || lease.state === "activating") &&
		(lease.previousIncarnation === undefined ||
			typeof lease.previousIncarnation === "string")
	);
}

type ScopeLeaseReadResult =
	| { ok: true; lease?: ChatDraftScopeLease }
	| { ok: false };

function readScopeLease(
	sessionId: string,
	storage: DraftStorage | undefined,
): ScopeLeaseReadResult {
	if (!sessionId || !storage) return { ok: false };
	let raw: string | null;
	try {
		raw = storage.getItem(scopeLeaseStorageKey(sessionId));
	} catch {
		return { ok: false };
	}
	if (raw === null) return { ok: true };
	try {
		const parsed: unknown = JSON.parse(raw);
		return isScopeLease(parsed, sessionId) ? { ok: true, lease: parsed } : { ok: false };
	} catch {
		return { ok: false };
	}
}

function storedDraftIncarnation(raw: string | null, sessionId: string): string | undefined {
	if (raw === null) return undefined;
	try {
		const parsed = JSON.parse(raw) as {
			sessionId?: unknown;
			incarnation?: unknown;
		};
		return parsed?.sessionId === sessionId && typeof parsed.incarnation === "string"
			? parsed.incarnation
			: undefined;
	} catch {
		return undefined;
	}
}

function isStrictlyNewerIncarnation(
	sessionId: string,
	candidate: string,
	current: string,
): boolean {
	if (candidate === current) return false;
	// The production incarnation is the daemon's createdAt. A legacy renderer used
	// the logical session id itself; an authoritative createdAt may replace that
	// sentinel, but the sentinel may never replace an already-dated incarnation.
	if (current === sessionId) return candidate !== sessionId;
	if (candidate === sessionId) return false;
	const candidateTime = Date.parse(candidate);
	const currentTime = Date.parse(current);
	return Number.isFinite(candidateTime) &&
		Number.isFinite(currentTime) &&
		candidateTime > currentTime;
}

function writeScopeLeaseProven(
	lease: ChatDraftScopeLease,
	storage: DraftStorage,
): boolean {
	const serialized = JSON.stringify(lease);
	try {
		storage.setItem(scopeLeaseStorageKey(lease.sessionId), serialized);
		return storage.getItem(scopeLeaseStorageKey(lease.sessionId)) === serialized;
	} catch {
		return false;
	}
}

function finishScopeActivation(
	scope: ChatDraftScope,
	lease: ChatDraftScopeLease,
	storage: DraftStorage,
): ChatDraftScopeActivationResult {
	const key = storageKey(scope.sessionId);
	try {
		storage.removeItem(key);
		if (storage.getItem(key) !== null) return { ok: false, reason: "storage" };
	} catch {
		return { ok: false, reason: "storage" };
	}
	purgeDraftRuntimes(scope.sessionId);
	const active: ChatDraftScopeLease = {
		schemaVersion: CHAT_DRAFT_SCOPE_SCHEMA_VERSION,
		sessionId: scope.sessionId,
		incarnation: scope.incarnation,
		state: "active",
	};
	if (!writeScopeLeaseProven(active, storage)) return { ok: false, reason: "storage" };
	return {
		ok: true,
		replaced: lease.previousIncarnation !== undefined,
		...(lease.previousIncarnation !== undefined
			? { previousIncarnation: lease.previousIncarnation }
			: {}),
	};
}

/**
 * Establish the one daemon-authoritative incarnation allowed to own a logical
 * session's renderer draft. Replacement is a monotonic, recoverable transition:
 * the successor blocks every predecessor first, then proves the old record is
 * gone, and only then becomes writable.
 */
export function activateChatDraftScope(
	scopeInput: ChatDraftScopeInput,
	storage: DraftStorage | undefined = rendererStorage(),
): ChatDraftScopeActivationResult {
	const scope = normalizeScope(scopeInput);
	if (!scope.sessionId || !scope.incarnation || !storage) {
		return { ok: false, reason: "storage" };
	}
	const leaseRead = readScopeLease(scope.sessionId, storage);
	if (!leaseRead.ok) return { ok: false, reason: "storage" };
	const lease = leaseRead.lease;
	if (lease?.incarnation === scope.incarnation) {
		if (lease.state === "active") return { ok: true, replaced: false };
		return finishScopeActivation(scope, lease, storage);
	}

	let previousIncarnation = lease?.incarnation;
	if (!previousIncarnation) {
		let raw: string | null;
		try {
			raw = storage.getItem(storageKey(scope.sessionId));
		} catch {
			return { ok: false, reason: "storage" };
		}
		previousIncarnation = storedDraftIncarnation(raw, scope.sessionId);
		if (!previousIncarnation || previousIncarnation === scope.incarnation) {
			const active: ChatDraftScopeLease = {
				schemaVersion: CHAT_DRAFT_SCOPE_SCHEMA_VERSION,
				sessionId: scope.sessionId,
				incarnation: scope.incarnation,
				state: "active",
			};
			return writeScopeLeaseProven(active, storage)
				? { ok: true, replaced: false }
				: { ok: false, reason: "storage" };
		}
	}
	if (!isStrictlyNewerIncarnation(scope.sessionId, scope.incarnation, previousIncarnation)) {
		return { ok: false, reason: "obsolete" };
	}
	const activating: ChatDraftScopeLease = {
		schemaVersion: CHAT_DRAFT_SCOPE_SCHEMA_VERSION,
		sessionId: scope.sessionId,
		incarnation: scope.incarnation,
		state: "activating",
		previousIncarnation,
	};
	if (!writeScopeLeaseProven(activating, storage)) {
		return { ok: false, reason: "storage" };
	}
	return finishScopeActivation(scope, activating, storage);
}

function rendererStorage(): DraftStorage | undefined {
	if (typeof window === "undefined") return undefined;
	try {
		return window.localStorage;
	} catch {
		return undefined;
	}
}

function emptyDraft(scope: ChatDraftScopeInput): ChatSessionDraft {
	const identity = normalizeScope(scope);
	return {
		schemaVersion: CHAT_DRAFT_SCHEMA_VERSION,
		sessionId: identity.sessionId,
		incarnation: identity.incarnation,
		composer: { revision: 0, text: "", attachments: [] },
	};
}

function isAttachment(value: unknown): value is ChatDraftAttachment {
	if (!value || typeof value !== "object") return false;
	const attachment = value as Partial<ChatDraftAttachment>;
	return (
		typeof attachment.id === "string" &&
		attachment.id.length > 0 &&
		typeof attachment.path === "string" &&
		(attachment.path === "" || attachment.path.startsWith(".ao/attachments/")) &&
		typeof attachment.name === "string" &&
		typeof attachment.mimeType === "string" &&
		typeof attachment.bytes === "number" &&
		Number.isFinite(attachment.bytes) &&
		attachment.bytes >= 0
	);
}

function isDeliveryState(value: unknown): value is ChatDraftDeliveryState {
	return value === "dispatching" || value === "accepted";
}

function isComposerDelivery(value: unknown): value is ChatComposerDelivery {
	if (!value || typeof value !== "object") return false;
	const delivery = value as Partial<ChatComposerDelivery>;
	return (
		(delivery.kind === "send" || delivery.kind === "steer") &&
		isDeliveryState(delivery.state) &&
		typeof delivery.revision === "number" &&
		Number.isInteger(delivery.revision) &&
		delivery.revision >= 0 &&
		typeof delivery.clientMessageId === "string" &&
		delivery.clientMessageId.length > 0 &&
		typeof delivery.requestText === "string" &&
		(!("nativeImages" in delivery) || typeof delivery.nativeImages === "boolean")
	);
}

function isChatSessionDraft(value: unknown, scope: ChatDraftScope): value is ChatSessionDraft {
	if (!value || typeof value !== "object") return false;
	const draft = value as Partial<ChatSessionDraft>;
	const composer = draft.composer as Partial<ChatSessionDraft["composer"]> | undefined;
	return (
		draft.schemaVersion === CHAT_DRAFT_SCHEMA_VERSION &&
		draft.sessionId === scope.sessionId &&
		draft.incarnation === scope.incarnation &&
		Boolean(composer) &&
		typeof composer?.revision === "number" &&
		Number.isInteger(composer.revision) &&
		composer.revision >= 0 &&
		typeof composer.text === "string" &&
		Array.isArray(composer.attachments) &&
		composer.attachments.every(isAttachment) &&
		(composer.delivery === undefined || isComposerDelivery(composer.delivery)) &&
		(draft.queuedEdit === undefined || (draft.queuedEdit !== null &&
			typeof draft.queuedEdit.revision === "string" && draft.queuedEdit.revision.length > 0 &&
			typeof draft.queuedEdit.turnId === "string" && draft.queuedEdit.turnId.length > 0 &&
			typeof draft.queuedEdit.text === "string" &&
			(draft.queuedEdit.nativeImages === undefined || typeof draft.queuedEdit.nativeImages === "boolean") &&
			(draft.queuedEdit.clientMessageId === undefined || typeof draft.queuedEdit.clientMessageId === "string") &&
			(draft.queuedEdit.ownerId === undefined || typeof draft.queuedEdit.ownerId === "string") &&
			(draft.queuedEdit.expectedRevision === undefined || (Number.isSafeInteger(draft.queuedEdit.expectedRevision) && draft.queuedEdit.expectedRevision >= 0)) &&
			(draft.queuedEdit.stagedAttachments === undefined || (Array.isArray(draft.queuedEdit.stagedAttachments) && draft.queuedEdit.stagedAttachments.every(isAttachment))) &&
			(draft.queuedEdit.attachments === undefined || (Array.isArray(draft.queuedEdit.attachments) && draft.queuedEdit.attachments.every((item) =>
				item !== null && typeof item === "object" && typeof item.id === "string" && typeof item.name === "string" &&
				(item.path === undefined || typeof item.path === "string") &&
				(item.contentIndex === undefined || (Number.isSafeInteger(item.contentIndex) && item.contentIndex >= 0)) &&
				(item.contentType === undefined || typeof item.contentType === "string")))) &&
			(draft.queuedEdit.saving === undefined || typeof draft.queuedEdit.saving === "boolean")
		))
	);
}

type DraftReadResult = { ok: true; draft: ChatSessionDraft } | { ok: false; draft: ChatSessionDraft };

function loadChatSessionDraft(scopeInput: ChatDraftScopeInput, storage: DraftStorage | undefined): DraftReadResult {
	const scope = normalizeScope(scopeInput);
	const empty = emptyDraft(scope);
	if (!scope.sessionId || !scope.incarnation || !storage) return { ok: false, draft: empty };
	const leaseRead = readScopeLease(scope.sessionId, storage);
	if (!leaseRead.ok) return { ok: false, draft: empty };
	if (
		leaseRead.lease &&
		(leaseRead.lease.incarnation !== scope.incarnation || leaseRead.lease.state !== "active")
	) {
		return { ok: false, draft: empty };
	}
	let raw: string | null;
	try {
		raw = storage.getItem(storageKey(scope.sessionId));
	} catch {
		return { ok: false, draft: empty };
	}
	if (!raw) return { ok: true, draft: empty };
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		// Corrupt renderer data is recoverable; the next proven write replaces it.
		return { ok: true, draft: empty };
	}
	if (
		parsed &&
		typeof parsed === "object" &&
		"sessionId" in parsed &&
		(parsed as { sessionId?: unknown }).sessionId === scope.sessionId &&
		"incarnation" in parsed &&
		(parsed as { incarnation?: unknown }).incarnation !== scope.incarnation
	) return { ok: false, draft: empty };
	return {
		ok: true,
		draft: isChatSessionDraft(parsed, scope) ? parsed : empty,
	};
}

export function readChatSessionDraft(
	scope: ChatDraftScopeInput,
	storage: DraftStorage | undefined = rendererStorage(),
): ChatSessionDraft {
	return loadChatSessionDraft(scope, storage).draft;
}

function hasContent(draft: ChatSessionDraft): boolean {
	return (
		draft.composer.text !== "" ||
		draft.composer.attachments.length > 0 ||
		Boolean(draft.composer.delivery) || Boolean(draft.queuedEdit)
	);
}

function claimOrProveDraftScope(
	draft: ChatSessionDraft,
	storage: DraftStorage,
): boolean {
	const leaseRead = readScopeLease(draft.sessionId, storage);
	if (!leaseRead.ok) return false;
	if (leaseRead.lease) {
		return (
			leaseRead.lease.state === "active" &&
			leaseRead.lease.incarnation === draft.incarnation
		);
	}
	let raw: string | null;
	try {
		raw = storage.getItem(storageKey(draft.sessionId));
	} catch {
		return false;
	}
	const existingIncarnation = storedDraftIncarnation(raw, draft.sessionId);
	if (existingIncarnation && existingIncarnation !== draft.incarnation) return false;
	return writeScopeLeaseProven(
		{
			schemaVersion: CHAT_DRAFT_SCOPE_SCHEMA_VERSION,
			sessionId: draft.sessionId,
			incarnation: draft.incarnation,
			state: "active",
		},
		storage,
	);
}

function persistDraft(draft: ChatSessionDraft, storage: DraftStorage | undefined): DraftWriteResult {
	if (!draft.sessionId || !storage) return { ok: false, draft };
	if (!claimOrProveDraftScope(draft, storage)) return { ok: false, draft };
	try {
		if (hasContent(draft)) storage.setItem(storageKey(draft.sessionId), JSON.stringify(draft));
		else storage.removeItem(storageKey(draft.sessionId));
		return { ok: true, draft };
	} catch {
		return { ok: false, draft };
	}
}

/**
 * Persist and immediately read back the exact record. Dispatch callers use this
 * stronger boundary so a silent/no-op storage adapter cannot be mistaken for a
 * durable draft or idempotency key.
 */
function persistDraftProven(
	draft: ChatSessionDraft,
	storage: DraftStorage | undefined,
): DraftWriteResult {
	if (!draft.sessionId || !storage) return { ok: false, draft };
	if (!claimOrProveDraftScope(draft, storage)) return { ok: false, draft };
	const key = storageKey(draft.sessionId);
	try {
		if (hasContent(draft)) {
			const serialized = JSON.stringify(draft);
			storage.setItem(key, serialized);
			if (storage.getItem(key) !== serialized) return { ok: false, draft };
		} else {
			storage.removeItem(key);
			if (storage.getItem(key) !== null) return { ok: false, draft };
		}
		return { ok: true, draft };
	} catch {
		return { ok: false, draft };
	}
}

function attachmentsEqual(
	current: ChatDraftAttachment[],
	next: ChatDraftAttachment[],
): boolean {
	return (
		current.length === next.length &&
		current.every((attachment, index) => {
			const candidate = next[index];
			return (
				candidate !== undefined &&
				attachment.id === candidate.id &&
				attachment.path === candidate.path &&
				attachment.name === candidate.name &&
				attachment.mimeType === candidate.mimeType &&
				attachment.bytes === candidate.bytes
			);
		})
	);
}

/**
 * Atomically proves the exact composer snapshot and its stable delivery id
 * before the renderer is allowed to call the daemon.
 */
export function prepareChatComposerDelivery(
	scope: ChatDraftScopeInput,
	input: PrepareChatComposerDeliveryInput,
	storage: DraftStorage | undefined = rendererStorage(),
): DraftDeliveryResult<ChatComposerDelivery> {
	const loaded = loadChatSessionDraft(scope, storage);
	if (!loaded.ok) return { ok: false, recovered: false, draft: loaded.draft };
	const existing = loaded.draft.composer.delivery;
	if (existing) {
		const proof = persistDraftProven(loaded.draft, storage);
		return proof.ok
			? { ok: true, recovered: true, draft: proof.draft, mutation: existing }
			: { ok: false, recovered: true, draft: loaded.draft };
	}
	const exact =
		loaded.draft.composer.text === input.composerText &&
		attachmentsEqual(loaded.draft.composer.attachments, input.attachments);
	const revision = exact
		? loaded.draft.composer.revision
		: loaded.draft.composer.revision + 1;
	const mutation: ChatComposerDelivery = {
		...(input.nativeImages === undefined ? {} : { nativeImages: input.nativeImages }),
		kind: input.kind,
		state: "dispatching",
		revision,
		clientMessageId: input.clientMessageId,
		requestText: input.requestText,
	};
	const next: ChatSessionDraft = {
		...loaded.draft,
		composer: {
			revision,
			text: input.composerText,
			attachments: input.attachments,
			delivery: mutation,
		},
	};
	const result = persistDraftProven(next, storage);
	return result.ok
		? { ok: true, recovered: false, draft: result.draft, mutation }
		: { ok: false, recovered: false, draft: loaded.draft };
}

/** Persist the daemon's acceptance before attempting to remove the draft. */
export function markChatComposerDeliveryAccepted(
	scope: ChatDraftScopeInput,
	clientMessageId: string,
	revision: number,
	storage: DraftStorage | undefined = rendererStorage(),
): DraftWriteResult {
	const loaded = loadChatSessionDraft(scope, storage);
	const delivery = loaded.draft.composer.delivery;
	if (
		!loaded.ok ||
		!delivery ||
		delivery.clientMessageId !== clientMessageId ||
		delivery.revision !== revision
	) {
		return { ok: false, draft: loaded.draft };
	}
	if (delivery.state === "accepted") return { ok: true, draft: loaded.draft };
	const result = persistDraftProven(
		{
			...loaded.draft,
			composer: {
				...loaded.draft.composer,
				delivery: { ...delivery, state: "accepted" },
			},
		},
		storage,
	);
	return result.ok ? result : { ok: false, draft: loaded.draft };
}

/**
 * Remove a definitively refused delivery journal without consuming its text. The
 * daemon proved that this first attempt was not accepted, so the same composer
 * revision becomes an ordinary editable draft again.
 */
export function clearRejectedChatComposerDelivery(
	scope: ChatDraftScopeInput,
	clientMessageId: string,
	revision: number,
	storage: DraftStorage | undefined = rendererStorage(),
): DraftWriteResult {
	const loaded = loadChatSessionDraft(scope, storage);
	const delivery = loaded.draft.composer.delivery;
	if (
		!loaded.ok ||
		!delivery ||
		delivery.state !== "dispatching" ||
		delivery.clientMessageId !== clientMessageId ||
		delivery.revision !== revision
	) {
		return { ok: false, draft: loaded.draft };
	}
	const next: ChatSessionDraft = {
		...loaded.draft,
		composer: {
			revision: loaded.draft.composer.revision,
			text: loaded.draft.composer.text,
			attachments: loaded.draft.composer.attachments,
		},
	};
	const result = persistDraftProven(next, storage);
	return result.ok ? result : { ok: false, draft: loaded.draft };
}

/**
 * Explicitly abandon a steer whose acceptance is unknowable. This is deliberately
 * separate from definitive refusal: the provider may already have received it.
 * Only the renderer journal is removed; text and staged descriptors remain so the
 * person can decide whether a later submit is worth the duplicate-delivery risk.
 */
export function clearUncertainChatComposerDelivery(
	scope: ChatDraftScopeInput,
	clientMessageId: string,
	revision: number,
	storage: DraftStorage | undefined = rendererStorage(),
): DraftWriteResult {
	const loaded = loadChatSessionDraft(scope, storage);
	const delivery = loaded.draft.composer.delivery;
	if (
		!loaded.ok ||
		!delivery ||
		delivery.kind !== "steer" ||
		delivery.state !== "dispatching" ||
		delivery.clientMessageId !== clientMessageId ||
		delivery.revision !== revision
	) {
		return { ok: false, draft: loaded.draft };
	}
	const next: ChatSessionDraft = {
		...loaded.draft,
		composer: {
			revision: loaded.draft.composer.revision,
			text: loaded.draft.composer.text,
			attachments: loaded.draft.composer.attachments,
		},
	};
	const result = persistDraftProven(next, storage);
	return result.ok ? result : { ok: false, draft: loaded.draft };
}

function mutateDraft(
	scope: ChatDraftScopeInput,
	mutate: (draft: ChatSessionDraft) => ChatSessionDraft,
	storage: DraftStorage | undefined,
	loaded: DraftReadResult = loadChatSessionDraft(scope, storage),
): DraftWriteResult {
	const next = mutate(loaded.draft);
	if (!loaded.ok) return { ok: false, draft: next };
	return persistDraft(next, storage);
}

export function writeChatComposerText(
	scope: ChatDraftScopeInput,
	text: string,
	storage: DraftStorage | undefined = rendererStorage(),
): DraftWriteResult {
	const loaded = loadChatSessionDraft(scope, storage);
	if (loaded.ok && loaded.draft.composer.text === text) {
		return { ok: true, draft: loaded.draft };
	}
	const result = mutateDraft(
		scope,
		(draft) => ({
			...draft,
			composer: {
				...draft.composer,
				revision: draft.composer.revision + 1,
				text,
			},
		}),
		storage,
		loaded,
	);
	if (result.ok) invalidateAcceptedDraftMutation(scope, "composer");
	return result;
}

export function writeChatAttachments(
	scope: ChatDraftScopeInput,
	attachments: ChatDraftAttachment[],
	storage: DraftStorage | undefined = rendererStorage(),
): DraftWriteResult {
	const loaded = loadChatSessionDraft(scope, storage);
	const current = loaded.draft;
	if (loaded.ok && attachmentsEqual(current.composer.attachments, attachments)) {
		return { ok: true, draft: current };
	}
	const result = mutateDraft(
		scope,
		(draft) => ({
			...draft,
			composer: {
				...draft.composer,
				revision: draft.composer.revision + 1,
				attachments,
			},
		}),
		storage,
		loaded,
	);
	if (result.ok) invalidateAcceptedDraftMutation(scope, "composer");
	return result;
}

/**
 * Clear the composer accepted by the daemon only if it is still the same
 * revision. A keystroke or attachment change that happened while send awaited
 * acceptance must remain both on screen and in durable storage.
 */
export function clearAcceptedChatComposer(
	scope: ChatDraftScopeInput,
	acceptedRevision: number,
	storage: DraftStorage | undefined = rendererStorage(),
): DraftClearResult {
	const loaded = loadChatSessionDraft(scope, storage);
	const current = loaded.draft;
	if (!loaded.ok) return { ok: false, cleared: false, draft: current };
	if (current.composer.revision !== acceptedRevision) {
		if (current.composer.delivery?.revision !== acceptedRevision) {
			return { ok: true, cleared: false, draft: current };
		}
		const next: ChatSessionDraft = {
			...current,
			composer: {
				revision: current.composer.revision,
				text: current.composer.text,
				attachments: current.composer.attachments,
			},
		};
		const result = persistDraftProven(next, storage);
		return result.ok
			? { ok: true, cleared: false, draft: result.draft }
			: { ok: false, cleared: false, draft: current };
	}
	const next: ChatSessionDraft = {
		...current,
		composer: {
			revision: current.composer.revision + 1,
			text: "",
			attachments: [],
		},
	};
	const result = persistDraftProven(next, storage);
	return result.ok
		? { ok: true, cleared: true, draft: result.draft }
		: { ok: false, cleared: false, draft: current };
}




export interface ChatDraftQueuedEdit {
	/** Original request mode, independent of current controller capabilities. */
	nativeImages?: boolean;
	/** Stable daemon receipt key for the exact pending save. */
	clientMessageId?: string;
	/** Stable for one editing attempt, including remounts and staged reads. */
	ownerId?: string;
	revision: string;
	turnId: string;
	text: string;
	/** Daemon message revision, held fixed across retries of this edit. */
	expectedRevision?: number;
	attachments?: ChatDraftRetainedAttachment[];
	stagedAttachments?: ChatDraftAttachment[];
	/** The daemon may have saved this exact edit before the response was lost. */
	saving?: boolean;
}




/** Server-owned content selected for retention; never stores attachment bytes. */
export interface ChatDraftRetainedAttachment {
	id: string;
	name: string;
	path?: string;
	contentIndex?: number;
	contentType?: string;
}




/** Queue edits are independent from both the ordinary prompt and history edits. */
export function writeChatQueuedEdit(
	scope: ChatDraftScopeInput,
	edit: Omit<ChatDraftQueuedEdit, "revision"> | undefined,
	expectedRevision?: string,
	storage: DraftStorage | undefined = rendererStorage(),
	previousUnprovenWrite?: { revisions: (string | undefined)[] },
): DraftWriteResult & { attempted?: { revisions: (string | undefined)[] } } {
	const loaded = loadChatSessionDraft(scope, storage);
	if (!loaded.ok || (expectedRevision !== undefined && loaded.draft.queuedEdit?.revision !== expectedRevision &&
		(!previousUnprovenWrite || !previousUnprovenWrite.revisions.includes(loaded.draft.queuedEdit?.revision)))) {
		return { ok: false, draft: loaded.draft };
	}
	const next = { ...loaded.draft };
	if (edit) next.queuedEdit = { ...edit, revision: draftEditRevision() };
	else delete next.queuedEdit;
	const result = persistDraftProven(next, storage);
	// A write can commit before its readback fails. Permit a later attempt to
	// recognize only the loaded or attempted revision, never another editor's
	// changes. Retain both in case the write itself failed before changing storage.
	return { ...result, attempted: { revisions: [loaded.draft.queuedEdit?.revision, next.queuedEdit?.revision] } };
}



function draftEditRevision(): string {
	if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
	return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}
