import AsyncStorage from "@react-native-async-storage/async-storage";

export type PendingSendRecord = {
	id: string;
	draftText: string;
	text: string;
	hasAttachments: boolean;
	/** Absent on records saved by older builds, where every delivery was a send. */
	kind?: "send" | "steer";
};

export function pendingSendKey(host: string, conversation: string): string {
	return `ao.chat.pending.${encodeURIComponent(JSON.stringify([host, conversation]))}`;
}

export function needsAttachmentRecovery(restored: boolean, hasAttachments: boolean, payloadAvailable: boolean): boolean {
	return restored && hasAttachments && !payloadAvailable;
}

const draftWrites = new Map<string, Promise<void>>();

export function afterDraftWrites(key: string, write: () => Promise<void>): Promise<void> {
	const next = (draftWrites.get(key) ?? Promise.resolve()).catch(() => {}).then(write);
	draftWrites.set(key, next);
	void next.finally(() => { if (draftWrites.get(key) === next) draftWrites.delete(key); }).catch(() => {});
	return next;
}

export async function readPendingSend(key: string): Promise<PendingSendRecord | null> {
	const raw = await AsyncStorage.getItem(key);
	if (!raw) return null;
	const saved = JSON.parse(raw) as PendingSendRecord;
	if (!saved.id || typeof saved.draftText !== "string" || typeof saved.text !== "string" || typeof saved.hasAttachments !== "boolean" ||
		(saved.kind !== undefined && saved.kind !== "send" && saved.kind !== "steer")) {
		throw new Error("The previous message's retry record is damaged. Discard it before sending another message.");
	}
	return saved;
}

export async function reservePendingSend(key: string, candidate: PendingSendRecord): Promise<PendingSendRecord> {
	const existing = await readPendingSend(key);
	if (existing) {
		if ((existing.kind ?? "send") !== (candidate.kind ?? "send")) {
			throw new Error("Resolve the previous message before sending another one.");
		}
		if (existing.draftText !== candidate.draftText) {
			throw new Error("Resolve the previous message before sending another one.");
		}
		if (candidate.hasAttachments) {
			throw new Error("Resolve the previous message first. Retrying with new attachments could silently discard them.");
		}
		return existing;
	}
	await AsyncStorage.setItem(key, JSON.stringify(candidate));
	return candidate;
}

export async function clearPendingSend(key: string, id: string): Promise<void> {
	if ((await readPendingSend(key))?.id === id) await AsyncStorage.removeItem(key);
}
