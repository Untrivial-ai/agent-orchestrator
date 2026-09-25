import { STANDALONE_WORKSPACE_ID } from "../types/workspace";

/**
 * The session the user was looking at when the window last closed. The record
 * lives in renderer localStorage, under Electron's ~/.ao userData, so the next
 * launch can show it before the daemon finishes starting. It is not a session
 * row and it is not a send.
 */
export interface LastSessionRecord {
	sessionId: string;
	projectId?: string;
	incarnation: string;
	title: string;
}

const LAST_SESSION_KEY = "ao.lastSession";

export type LastSessionTarget =
	| { to: "/sessions/$sessionId"; params: { sessionId: string } }
	| {
			to: "/projects/$projectId/sessions/$sessionId";
			params: { projectId: string; sessionId: string };
	  };

export function rememberLastSession(
	record: LastSessionRecord,
	storage: Pick<Storage, "setItem" | "removeItem"> | null = localStorageOrNull(),
): void {
	if (!storage || !record.sessionId || !record.incarnation) return;
	try {
		storage.setItem(LAST_SESSION_KEY, JSON.stringify(record));
	} catch {
		// A full or blocked store must not block the session the user is already in.
	}
}

export function readLastSession(
	storage: Pick<Storage, "getItem"> | null = localStorageOrNull(),
): LastSessionRecord | null {
	if (!storage) return null;
	let raw: string | null;
	try {
		raw = storage.getItem(LAST_SESSION_KEY);
	} catch {
		return null;
	}
	if (!raw) return null;
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return null;
	}
	if (!parsed || typeof parsed !== "object") return null;
	const record = parsed as Partial<LastSessionRecord>;
	if (typeof record.sessionId !== "string" || record.sessionId === "") return null;
	if (typeof record.incarnation !== "string" || record.incarnation === "") return null;
	if (typeof record.title !== "string") return null;
	if (record.projectId !== undefined && typeof record.projectId !== "string") return null;
	return {
		sessionId: record.sessionId,
		projectId: record.projectId || undefined,
		incarnation: record.incarnation,
		title: record.title,
	};
}

export function forgetLastSession(
	storage: Pick<Storage, "removeItem"> | null = localStorageOrNull(),
): void {
	if (!storage) return;
	try {
		storage.removeItem(LAST_SESSION_KEY);
	} catch {
		// Leaving a stale record is safer than throwing through the shell.
	}
}

export function lastSessionTarget(record: LastSessionRecord): LastSessionTarget {
	if (!record.projectId || record.projectId === STANDALONE_WORKSPACE_ID) {
		return { to: "/sessions/$sessionId", params: { sessionId: record.sessionId } };
	}
	return {
		to: "/projects/$projectId/sessions/$sessionId",
		params: { projectId: record.projectId, sessionId: record.sessionId },
	};
}

function localStorageOrNull(): Storage | null {
	try {
		return globalThis.localStorage ?? null;
	} catch {
		return null;
	}
}
