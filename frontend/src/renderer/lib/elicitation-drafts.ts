import type { DraftStorage } from "./chat-drafts";

/**
 * Renderer-owned drafts for a pending agent question (elicitation).
 *
 * The question docks above the composer, so switching sessions unmounts it and
 * takes any half-typed "Other" answer with it. The answer is not sent anywhere
 * until the human presses Continue, so the draft stays in this renderer's
 * localStorage — pinned beneath AO's userData directory — keyed by conversation
 * and request id, and is removed once the request is resolved.
 *
 * The daemon itself identifies a pending input by `(conversation_id,
 * request_id)`, so the draft is scoped the same way, not by session id. A
 * reviewer-chat overlay reports the underlying worker's session id in its
 * snapshot (`review.SessionID`) while reading from its own, separate
 * conversation, so session id alone is not a precise scope for two chats that
 * can be open on the same session at once. Question ids themselves do not
 * repeat (they come from `uuid.NewString()` or a per-relay counter), so this
 * is about matching the daemon's own identity model, not guarding against a
 * collision.
 *
 * The key carries no schema version: `schemaVersion` inside the stored value
 * does that job, and both the read path and the sweep drop any entry whose
 * version they don't recognize. A version in the key (as this file used to
 * have) would need the sweep to also know every past prefix, or old entries
 * would sit outside its `startsWith` filter forever.
 */

export type ElicitationDraftValue = string | number | boolean | string[];

export interface ElicitationDraft {
	values: Record<string, ElicitationDraftValue>;
	activeQuestion: number;
}

interface StoredElicitationDraft extends ElicitationDraft {
	schemaVersion: typeof ELICITATION_DRAFT_SCHEMA_VERSION;
	updatedAt: number;
}

export const ELICITATION_DRAFT_SCHEMA_VERSION = 1 as const;

const KEY_PREFIX = "ao.elicitation-draft:";
const LAST_SWEEP_KEY = "ao.elicitation-draft-sweep:last";

/** Drafts for questions this old are abandoned; the request is long gone. */
const MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;

/**
 * A locally-written draft can look future-dated if the system clock steps
 * backward after the write (a manual fix, a VM or dual-boot RTC correction, a
 * large NTP step). This tolerance keeps that draft instead of destroying the
 * exact thing this module exists to protect; it is far too small to hide a
 * genuinely corrupt or forged far-future value.
 */
const MAX_CLOCK_SKEW_MS = 5 * 60 * 1000;

/** How often a mounting question re-checks for abandoned drafts, so a window left open for days still gets swept. */
const SWEEP_INTERVAL_MS = 60 * 60 * 1000;

export type ElicitationDraftStorage = DraftStorage & Partial<Pick<Storage, "key" | "length">>;

export interface ElicitationDraftWriteResult {
	ok: boolean;
}

export function elicitationDraftKey(conversationId: string, requestId: string): string {
	return `${KEY_PREFIX}${conversationId}:${requestId}`;
}

export function readElicitationDraft(
	conversationId: string,
	requestId: string,
	storage: ElicitationDraftStorage | undefined = rendererStorage(),
): ElicitationDraft | undefined {
	if (!storage) return undefined;
	const key = elicitationDraftKey(conversationId, requestId);
	let raw: string | null;
	try {
		raw = storage.getItem(key);
	} catch {
		return undefined;
	}
	const decoded = decodeStoredDraft(raw, Date.now());
	if (decoded.kind === "missing") return undefined;
	if (decoded.kind === "valid") return { values: decoded.values, activeQuestion: decoded.activeQuestion };
	// Malformed, an unsupported schema version, or expired: dead weight either
	// way, and removing it here means a later read never has to decide again.
	try {
		storage.removeItem(key);
	} catch {
		// A leftover entry that cannot be removed still fails this same check on the next read.
	}
	return undefined;
}

export function writeElicitationDraft(
	conversationId: string,
	requestId: string,
	draft: ElicitationDraft,
	storage: ElicitationDraftStorage | undefined = rendererStorage(),
): ElicitationDraftWriteResult {
	if (!storage) return { ok: false };
	const stored: StoredElicitationDraft = {
		schemaVersion: ELICITATION_DRAFT_SCHEMA_VERSION,
		values: draft.values,
		activeQuestion: draft.activeQuestion,
		updatedAt: Date.now(),
	};
	try {
		storage.setItem(elicitationDraftKey(conversationId, requestId), JSON.stringify(stored));
		return { ok: true };
	} catch {
		// The caller reports this through setChatDraftBoundary, the same way a
		// failed composer or queued-edit write does — switching sessions would
		// otherwise silently drop the answer, which is the bug this module exists
		// to fix.
		return { ok: false };
	}
}

export function clearElicitationDraft(
	conversationId: string,
	requestId: string,
	storage: ElicitationDraftStorage | undefined = rendererStorage(),
): void {
	if (!storage) return;
	try {
		storage.removeItem(elicitationDraftKey(conversationId, requestId));
	} catch {
		// A draft that cannot be cleared expires on its own.
	}
}

/**
 * Removes every draft this conversation is holding except the one for
 * `currentRequestId` (if any question is pending). A ChatWorkspace mount only
 * ever sees the requests that come and go while it stays mounted; a question
 * that resolves elsewhere, times out, or is answered from another window
 * while this conversation's Chat surface is unmounted never fires that
 * transition here. Reconciling directly against the loaded snapshot instead —
 * on every mount, not only on a live change — catches that case too, well
 * inside the sweep's 7-day window rather than only at its end.
 */
export function reconcileElicitationDraftsForConversation(
	conversationId: string,
	currentRequestId: string | undefined,
	storage: ElicitationDraftStorage | undefined = rendererStorage(),
): void {
	if (!storage || typeof storage.key !== "function" || typeof storage.length !== "number") return;
	const prefix = `${KEY_PREFIX}${conversationId}:`;
	const currentKey = currentRequestId ? elicitationDraftKey(conversationId, currentRequestId) : undefined;
	const stale: string[] = [];
	try {
		for (let index = 0; index < storage.length; index += 1) {
			const key = storage.key(index);
			if (!key || !key.startsWith(prefix) || key === currentKey) continue;
			stale.push(key);
		}
		for (const key of stale) storage.removeItem(key);
	} catch {
		// Reconciliation is opportunistic, same as the sweep.
	}
}

/**
 * Sweeps expired drafts at most once per `SWEEP_INTERVAL_MS`, tracked in
 * storage itself rather than in memory: a once-per-process guard never runs
 * again in a window left open for days, which is exactly when an abandoned
 * draft has had the most time to accumulate. Pruning walks every stored key,
 * so it belongs on a question appearing, not on a keystroke.
 */
export function pruneExpiredElicitationDraftsOnce(
	storage: ElicitationDraftStorage | undefined = rendererStorage(),
): void {
	if (!storage) return;
	const now = Date.now();
	let last: number | undefined;
	try {
		const raw = storage.getItem(LAST_SWEEP_KEY);
		last = raw === null ? undefined : Number(raw);
	} catch {
		last = undefined;
	}
	if (typeof last === "number" && Number.isFinite(last) && now - last < SWEEP_INTERVAL_MS) return;
	pruneExpiredElicitationDrafts(storage, now);
	try {
		storage.setItem(LAST_SWEEP_KEY, String(now));
	} catch {
		// Best effort; the next mount just sweeps again.
	}
}

/** Test seam: clears the recorded sweep time so the next call sweeps again. */
export function resetElicitationDraftPruning(storage: ElicitationDraftStorage | undefined = rendererStorage()): void {
	if (!storage) return;
	try {
		storage.removeItem(LAST_SWEEP_KEY);
	} catch {
		// Nothing to reset if this fails; the interval just runs out on its own.
	}
}

/**
 * Drops drafts whose question was never resolved in this renderer — the app was
 * quit while a question was open, or the session was deleted underneath it.
 */
export function pruneExpiredElicitationDrafts(
	storage: ElicitationDraftStorage | undefined = rendererStorage(),
	now = Date.now(),
): void {
	if (!storage || typeof storage.key !== "function" || typeof storage.length !== "number") return;
	const expired: string[] = [];
	try {
		for (let index = 0; index < storage.length; index += 1) {
			const key = storage.key(index);
			if (!key || !key.startsWith(KEY_PREFIX)) continue;
			let raw: string | null;
			try {
				raw = storage.getItem(key);
			} catch {
				continue;
			}
			// Anything that doesn't decode as a current, fresh draft is removed:
			// malformed JSON, an unsupported schema version, and a stale timestamp
			// are all treated the same way the read path treats them.
			if (decodeStoredDraft(raw, now).kind !== "valid") expired.push(key);
		}
		for (const key of expired) storage.removeItem(key);
	} catch {
		// Pruning is opportunistic.
	}
}

type DecodedStoredDraft =
	| { kind: "missing" }
	| { kind: "invalid" }
	| { kind: "expired" }
	| { kind: "valid"; values: Record<string, ElicitationDraftValue>; activeQuestion: number };

/** Single source of truth for what counts as a usable stored draft, shared by every read path. */
function decodeStoredDraft(raw: string | null, now: number): DecodedStoredDraft {
	if (!raw) return { kind: "missing" };
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return { kind: "invalid" };
	}
	if (!isRecord(parsed)) return { kind: "invalid" };
	if (parsed.schemaVersion !== ELICITATION_DRAFT_SCHEMA_VERSION) return { kind: "invalid" };
	if (!isRecord(parsed.values)) return { kind: "invalid" };
	if (!isFreshTimestamp(parsed.updatedAt, now)) return { kind: "expired" };
	const values: Record<string, ElicitationDraftValue> = {};
	for (const [name, value] of Object.entries(parsed.values)) {
		if (isDraftValue(value)) values[name] = value;
	}
	return {
		kind: "valid",
		values,
		activeQuestion: typeof parsed.activeQuestion === "number" && parsed.activeQuestion >= 0 ? parsed.activeQuestion : 0,
	};
}

/** A timestamp counts as fresh if it isn't further in the future than clock skew allows, and not older than MAX_AGE_MS. */
function isFreshTimestamp(value: unknown, now: number): boolean {
	return (
		typeof value === "number" &&
		Number.isFinite(value) &&
		value - now <= MAX_CLOCK_SKEW_MS &&
		now - value <= MAX_AGE_MS
	);
}

function rendererStorage(): ElicitationDraftStorage | undefined {
	if (typeof window === "undefined") return undefined;
	try {
		return window.localStorage;
	} catch {
		return undefined;
	}
}

function isDraftValue(value: unknown): value is ElicitationDraftValue {
	if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return true;
	return Array.isArray(value) && value.every((entry) => typeof entry === "string");
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null;
}
