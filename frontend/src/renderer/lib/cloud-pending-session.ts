import { useSyncExternalStore } from "react";
import {
	bindCloudStartupAttempt,
	type CloudStartupAttempt,
} from "./cloud-startup-timing";

export type CloudPendingCreateState = "saving" | "accepted" | "failed" | "ready";
export type CloudPendingMessageState = "saving" | "sending" | "queued" | "failed";

export type CloudPendingMessage = {
	clientSequence: number;
	error?: string;
	id: string;
	idempotencyKey: string;
	state: CloudPendingMessageState;
	text: string;
};

export type CloudPendingSession = {
	attemptId: string;
	createError?: string;
	createIdempotencyKey: string;
	createState: CloudPendingCreateState;
	durableSessionId?: string;
	initialPrompt: string;
	messages: readonly CloudPendingMessage[];
	orgId: string;
	projectId: string;
	routeSessionId: string;
	startedAtMs: number;
};

export type CloudPendingSessionRegistration = {
	attempt: CloudStartupAttempt;
	create: (idempotencyKey: string) => Promise<string>;
	initialPrompt: string;
	orgId: string;
	onAccepted?: (sessionId: string) => void | Promise<void>;
	projectId: string;
	send: (
		sessionId: string,
		message: { clientSequence: number; text: string },
		idempotencyKey: string,
	) => Promise<void>;
};

type PendingRecord = {
	create: CloudPendingSessionRegistration["create"];
	creating: boolean;
	flush?: Promise<void>;
	onAccepted?: CloudPendingSessionRegistration["onAccepted"];
	send: CloudPendingSessionRegistration["send"];
	snapshot: CloudPendingSession;
	timingAttempt: CloudStartupAttempt;
};

const MAX_ATTEMPTS = 100;
const MAX_ATTEMPT_AGE_MS = 30 * 60_000;
const MAX_MESSAGES_PER_ATTEMPT = 64;
const MAX_PENDING_MESSAGE_BYTES = 256 * 1024;
const MAX_MESSAGE_BYTES = 65_536;
const ROUTE_PREFIX = "pending-cloud-";
const textEncoder = new TextEncoder();

const records = new Map<string, PendingRecord>();
const aliases = new Map<string, string>();
const listeners = new Set<() => void>();

function now(): number {
	return globalThis.performance?.now() ?? Date.now();
}

function boundedError(error: unknown): string {
	const message = error instanceof Error ? error.message : String(error);
	return message.trim().slice(0, 240) || "The request failed.";
}

function removeRecord(attemptId: string): void {
	const record = records.get(attemptId);
	if (!record) return;
	records.delete(attemptId);
	for (const [alias, target] of aliases) {
		if (target === attemptId) aliases.delete(alias);
	}
}

function prune(currentTime = now(), reserveCapacity = false): void {
	for (const [attemptId, record] of records) {
		if (currentTime - record.snapshot.startedAtMs > MAX_ATTEMPT_AGE_MS) {
			removeRecord(attemptId);
		}
	}
	while (reserveCapacity && records.size >= MAX_ATTEMPTS) {
		const oldest = records.keys().next().value;
		if (oldest === undefined) break;
		removeRecord(oldest);
	}
}

function emit(): void {
	for (const listener of listeners) listener();
}

function update(record: PendingRecord, change: Partial<CloudPendingSession>): void {
	record.snapshot = { ...record.snapshot, ...change };
	emit();
}

function replaceMessage(
	record: PendingRecord,
	messageId: string,
	change: Partial<CloudPendingMessage>,
): void {
	update(record, {
		messages: record.snapshot.messages.map((message) =>
			message.id === messageId ? { ...message, ...change } : message,
		),
	});
}

function resolveRecord(identifier: string): PendingRecord | undefined {
	prune();
	const attemptId = records.has(identifier) ? identifier : aliases.get(identifier);
	return attemptId ? records.get(attemptId) : undefined;
}

function pendingMessageBytes(record: PendingRecord): number {
	return record.snapshot.messages.reduce(
		(total, message) => total + textEncoder.encode(message.text).byteLength,
		0,
	);
}

async function flushMessages(record: PendingRecord): Promise<void> {
	const sessionId = record.snapshot.durableSessionId;
	if (!sessionId || !["accepted", "ready"].includes(record.snapshot.createState)) return;
	for (;;) {
		const next = record.snapshot.messages.find((message) => message.state !== "queued");
		if (!next || next.state === "failed" || next.state === "sending") return;
		replaceMessage(record, next.id, { state: "sending", error: undefined });
		try {
			await record.send(
				sessionId,
				{ clientSequence: next.clientSequence, text: next.text },
				next.idempotencyKey,
			);
			replaceMessage(record, next.id, { state: "queued", error: undefined });
		} catch (error) {
			replaceMessage(record, next.id, { state: "failed", error: boundedError(error) });
			return;
		}
	}
}

function scheduleFlush(record: PendingRecord): void {
	if (record.flush) return;
	record.flush = flushMessages(record).finally(() => {
		record.flush = undefined;
	});
}

export function registerCloudPendingSession(
	registration: CloudPendingSessionRegistration,
): CloudPendingSession {
	prune(now(), true);
	const attemptId = registration.attempt.attemptId;
	const existing = records.get(attemptId);
	if (existing) return existing.snapshot;
	const routeSessionId = `${ROUTE_PREFIX}${attemptId}`;
	const snapshot: CloudPendingSession = {
		attemptId,
		createIdempotencyKey: attemptId,
		createState: "saving",
		initialPrompt: registration.initialPrompt,
		messages: [],
		orgId: registration.orgId,
		projectId: registration.projectId,
		routeSessionId,
		startedAtMs: registration.attempt.startedAtMs,
	};
	records.set(attemptId, {
		create: registration.create,
		creating: false,
		onAccepted: registration.onAccepted,
		send: registration.send,
		snapshot,
		timingAttempt: registration.attempt,
	});
	aliases.set(routeSessionId, attemptId);
	emit();
	return snapshot;
}

export async function createCloudPendingSession(identifier: string): Promise<string | undefined> {
	const record = resolveRecord(identifier);
	if (!record || record.creating) return record?.snapshot.durableSessionId;
	if (record.snapshot.durableSessionId) return record.snapshot.durableSessionId;
	record.creating = true;
	update(record, { createState: "saving", createError: undefined });
	try {
		const sessionId = await record.create(record.snapshot.createIdempotencyKey);
		if (!sessionId.trim()) throw new Error("The control plane returned no session identifier.");
		aliases.set(sessionId, record.snapshot.attemptId);
		bindCloudStartupAttempt(sessionId, record.timingAttempt);
		update(record, {
			createState: "accepted",
			createError: undefined,
			durableSessionId: sessionId,
		});
		scheduleFlush(record);
		try {
			await record.onAccepted?.(sessionId);
		} catch {
			// The durable session remains authoritative. Navigation can recover from
			// the alias without repeating the create request.
		}
		return sessionId;
	} catch (error) {
		update(record, { createState: "failed", createError: boundedError(error) });
		return undefined;
	} finally {
		record.creating = false;
	}
}

export function queueCloudPendingMessage(identifier: string, text: string): string {
	const record = resolveRecord(identifier);
	if (!record) throw new Error("The pending Cloud session is unavailable.");
	const bytes = textEncoder.encode(text).byteLength;
	if (text.trim() === "" || bytes > MAX_MESSAGE_BYTES) {
		throw new Error("Message text must be between 1 and 65536 bytes.");
	}
	if (record.snapshot.messages.length >= MAX_MESSAGES_PER_ATTEMPT ||
		pendingMessageBytes(record) + bytes > MAX_PENDING_MESSAGE_BYTES) {
		throw new Error("The pending message limit was reached. Wait for delivery before sending more.");
	}
	const message: CloudPendingMessage = {
		clientSequence: record.snapshot.messages.length + 1,
		id: globalThis.crypto.randomUUID(),
		idempotencyKey: globalThis.crypto.randomUUID(),
		state: "saving",
		text,
	};
	update(record, { messages: [...record.snapshot.messages, message] });
	if (record.snapshot.durableSessionId) {
		scheduleFlush(record);
	}
	return message.id;
}

export function retryCloudPendingMessage(identifier: string, messageId: string): void {
	const record = resolveRecord(identifier);
	const message = record?.snapshot.messages.find((candidate) => candidate.id === messageId);
	if (!record || !message || message.state !== "failed") return;
	replaceMessage(record, messageId, { state: "saving", error: undefined });
	scheduleFlush(record);
}

export function markCloudPendingSessionReady(identifier: string): void {
	const record = resolveRecord(identifier);
	if (!record || record.snapshot.createState === "ready") return;
	update(record, { createState: "ready" });
}

export function getCloudPendingSession(identifier: string): CloudPendingSession | undefined {
	return resolveRecord(identifier)?.snapshot;
}

export function isCloudPendingSessionRoute(identifier: string): boolean {
	return identifier.startsWith(ROUTE_PREFIX);
}

export function subscribeCloudPendingSessions(listener: () => void): () => void {
	listeners.add(listener);
	return () => listeners.delete(listener);
}

export function useCloudPendingSession(identifier: string): CloudPendingSession | undefined {
	return useSyncExternalStore(
		subscribeCloudPendingSessions,
		() => getCloudPendingSession(identifier),
		() => getCloudPendingSession(identifier),
	);
}

export function resetCloudPendingSessionsForTests(): void {
	records.clear();
	aliases.clear();
}
