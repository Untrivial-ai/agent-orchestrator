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
 * Scoped by conversation, not session: the daemon identifies a pending input by
 * `(conversation_id, request_id)`, and a reviewer-chat overlay can report the
 * same session id as its underlying worker chat while reading a different
 * conversation.
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

/** A timestamp up to this far in the future is trusted as-is, no re-stamp needed (a small NTP nudge). */
const MAX_CLOCK_SKEW_MS = 5 * 60 * 1000;

/**
 * A timestamp beyond `MAX_CLOCK_SKEW_MS` but within this is still trusted — a
 * dual-boot RTC offset is typically a whole timezone, hours rather than
 * minutes — but gets re-stamped to the current time on the next read, so it
 * stops relying on a clock that was wrong when it was written. Past this, a
 * value is treated as corrupt rather than as a real clock error.
 */
const MAX_FUTURE_TOLERANCE_MS = 24 * 60 * 60 * 1000;

/** How often a sweep call actually walks the store; see `pruneExpiredElicitationDraftsOnce`. */
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
	const now = Date.now();
	const decoded = decodeStoredDraft(raw, now);
	if (decoded.kind === "missing") return undefined;
	if (decoded.kind !== "valid") {
		// Malformed, an unsupported schema version, or expired: dead weight either
		// way, and removing it here means a later read never has to decide again.
		try {
			storage.removeItem(key);
		} catch {
			// A leftover entry that cannot be removed still fails this same check on the next read.
		}
		return undefined;
	}
	if (decoded.restamp) {
		writeElicitationDraft(conversationId, requestId, { values: decoded.values, activeQuestion: decoded.activeQuestion }, storage);
	}
	return { values: decoded.values, activeQuestion: decoded.activeQuestion };
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
 * Removes drafts this conversation is holding that are safe to remove:
 * anything in `resolvedRequestIds` always is, since it was seen in the loaded
 * page and is not pending. When `fullyLoaded` is true (nothing older is left
 * unloaded), every other draft not in `pendingRequestIds` is safe too. When it
 * is false, a draft whose request id was not seen at all is left alone — it
 * may belong to a still-open question old enough to sit outside the loaded
 * page, and deleting it would only be a guess.
 *
 * `pendingRequestIds` has to list every open request on the conversation, not
 * just the one on screen: the daemon can have more than one `user_input` open
 * at once, and the UI shows only the newest.
 */
export function reconcileElicitationDraftsForConversation(
	conversationId: string,
	pendingRequestIds: Iterable<string>,
	resolvedRequestIds: Iterable<string>,
	fullyLoaded: boolean,
	storage: ElicitationDraftStorage | undefined = rendererStorage(),
): void {
	if (!storage || typeof storage.key !== "function" || typeof storage.length !== "number") return;
	const prefix = `${KEY_PREFIX}${conversationId}:`;
	const keep = new Set([...pendingRequestIds].map((requestId) => elicitationDraftKey(conversationId, requestId)));
	const resolved = new Set([...resolvedRequestIds].map((requestId) => elicitationDraftKey(conversationId, requestId)));
	const stale: string[] = [];
	try {
		for (let index = 0; index < storage.length; index += 1) {
			const key = storage.key(index);
			if (!key || !key.startsWith(prefix) || keep.has(key)) continue;
			if (fullyLoaded || resolved.has(key)) stale.push(key);
		}
		for (const key of stale) storage.removeItem(key);
	} catch {
		// Reconciliation is opportunistic, same as the sweep.
	}
}

/**
 * Sweeps expired drafts, but only if it has been at least `SWEEP_INTERVAL_MS`
 * since the last sweep that ran — tracked in storage itself, so a renderer
 * left open for days does not skip every call after the first. This still
 * only runs when something calls it, not on a timer of its own: expiry is
 * opportunistic, paid by whatever triggers a call (a question appearing, a
 * conversation switching), not guaranteed on a schedule.
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
	// Any future marker counts as no marker: it's ours, an early sweep costs
	// nothing, and trusting one written while the clock was ahead would block
	// sweeps until real time caught up.
	if (typeof last === "number" && Number.isFinite(last) && last <= now && now - last < SWEEP_INTERVAL_MS) {
		return;
	}
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
	| {
			kind: "valid";
			values: Record<string, ElicitationDraftValue>;
			activeQuestion: number;
			/** True if `updatedAt` was past the tight tolerance and should be refreshed. */
			restamp: boolean;
	  };

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
	const age = timestampAge(parsed.updatedAt, now);
	if (age === undefined) return { kind: "expired" };
	const values: Record<string, ElicitationDraftValue> = {};
	for (const [name, value] of Object.entries(parsed.values)) {
		if (isDraftValue(value)) values[name] = value;
	}
	return {
		kind: "valid",
		values,
		activeQuestion: typeof parsed.activeQuestion === "number" && parsed.activeQuestion >= 0 ? parsed.activeQuestion : 0,
		restamp: age > MAX_CLOCK_SKEW_MS,
	};
}

/** How far in the future `value` is, or undefined if it is not a usable timestamp at all. */
function timestampAge(value: unknown, now: number): number | undefined {
	if (typeof value !== "number" || !Number.isFinite(value)) return undefined;
	if (now - value > MAX_AGE_MS) return undefined;
	const future = value - now;
	if (future > MAX_FUTURE_TOLERANCE_MS) return undefined;
	return future;
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
