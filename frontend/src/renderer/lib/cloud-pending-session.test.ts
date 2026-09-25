import { beforeEach, describe, expect, it, vi } from "vitest";
import { beginCloudStartupAttempt } from "./cloud-startup-timing";
import {
	createCloudPendingSession,
	getCloudPendingSession,
	markCloudPendingSessionReady,
	queueCloudPendingMessage,
	registerCloudPendingSession,
	resetCloudPendingSessionsForTests,
	retryCloudPendingMessage,
} from "./cloud-pending-session";

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: unknown) => void;
	const promise = new Promise<T>((resolvePromise, rejectPromise) => {
		resolve = resolvePromise;
		reject = rejectPromise;
	});
	return { promise, reject, resolve };
}

describe("cloud pending session", () => {
	beforeEach(() => {
		resetCloudPendingSessionsForTests();
		let sequence = 0;
		vi.stubGlobal("crypto", { randomUUID: () => `key-${++sequence}` });
	});

	it("binds one create and flushes messages exactly once in client order", async () => {
		const creation = deferred<string>();
		const sends: Array<{ key: string; sequence: number; text: string }> = [];
		const firstSend = deferred<void>();
		const accepted = vi.fn();
		const pending = registerCloudPendingSession({
			attempt: beginCloudStartupAttempt("attempt-1", 100),
			orgId: "org-1",
			projectId: "project-1",
			initialPrompt: "Initial task",
			create: () => creation.promise,
			onAccepted: accepted,
			send: async (_sessionId, message, key) => {
				sends.push({ key, sequence: message.clientSequence, text: message.text });
				if (message.clientSequence === 1) await firstSend.promise;
			},
		});
		const creating = createCloudPendingSession(pending.attemptId);
		queueCloudPendingMessage(pending.routeSessionId, "First follow-up");
		queueCloudPendingMessage(pending.attemptId, "Second follow-up");

		expect(getCloudPendingSession(pending.attemptId)?.messages.map((message) => message.state)).toEqual([
			"saving",
			"saving",
		]);
		creation.resolve("session-1");
		await creating;
		await vi.waitFor(() => expect(sends).toHaveLength(1));
		expect(sends[0]).toMatchObject({ sequence: 1, text: "First follow-up" });
		expect(accepted).toHaveBeenCalledOnce();

		firstSend.resolve();
		await vi.waitFor(() => expect(sends).toHaveLength(2));
		expect(sends.map(({ sequence, text }) => ({ sequence, text }))).toEqual([
			{ sequence: 1, text: "First follow-up" },
			{ sequence: 2, text: "Second follow-up" },
		]);
		await vi.waitFor(() =>
			expect(getCloudPendingSession("session-1")?.messages.map((message) => message.state)).toEqual([
				"queued",
				"queued",
			]),
		);
	});

	it("retries an ambiguous create with the original idempotency key", async () => {
		const keys: string[] = [];
		let attempts = 0;
		const pending = registerCloudPendingSession({
			attempt: beginCloudStartupAttempt("stable-create-key", 100),
			orgId: "org-1",
			projectId: "project-1",
			initialPrompt: "Initial task",
			create: async (key) => {
				keys.push(key);
				attempts += 1;
				if (attempts === 1) throw new Error("response lost");
				return "session-recovered";
			},
			send: async () => undefined,
		});

		expect(await createCloudPendingSession(pending.attemptId)).toBeUndefined();
		expect(getCloudPendingSession(pending.attemptId)).toMatchObject({
			createState: "failed",
			createError: "response lost",
		});
		expect(await createCloudPendingSession(pending.attemptId)).toBe("session-recovered");
		expect(keys).toEqual(["stable-create-key", "stable-create-key"]);
	});

	it("holds later messages behind a failed send and retries with the same key", async () => {
		const calls: string[] = [];
		let fail = true;
		const pending = registerCloudPendingSession({
			attempt: beginCloudStartupAttempt("attempt-failure", 100),
			orgId: "org-1",
			projectId: "project-1",
			initialPrompt: "Initial task",
			create: async () => "session-1",
			send: async (_sessionId, message, key) => {
				calls.push(`${message.clientSequence}:${key}`);
				if (message.clientSequence === 1 && fail) throw new Error("temporarily unavailable");
			},
		});
		await createCloudPendingSession(pending.attemptId);
		const firstId = queueCloudPendingMessage(pending.attemptId, "First");
		queueCloudPendingMessage(pending.attemptId, "Second");

		await vi.waitFor(() =>
			expect(getCloudPendingSession(pending.attemptId)?.messages.map((message) => message.state)).toEqual([
				"failed",
				"saving",
			]),
		);
		const firstKey = getCloudPendingSession(pending.attemptId)?.messages[0]?.idempotencyKey;
		fail = false;
		markCloudPendingSessionReady("session-1");
		retryCloudPendingMessage(pending.attemptId, firstId);
		await vi.waitFor(() =>
			expect(getCloudPendingSession(pending.attemptId)?.messages.map((message) => message.state)).toEqual([
				"queued",
				"queued",
			]),
		);
		expect(calls).toEqual([`1:${firstKey}`, `1:${firstKey}`, expect.stringMatching(/^2:/)]);
	});

	it("delivers a draft submitted after the terminal becomes ready", async () => {
		const send = vi.fn(async () => undefined);
		const pending = registerCloudPendingSession({
			attempt: beginCloudStartupAttempt("ready-draft", 100),
			orgId: "org-1", projectId: "project-1", initialPrompt: "Initial task",
			create: async () => "session-1", send,
		});
		await createCloudPendingSession(pending.attemptId);
		markCloudPendingSessionReady("session-1");
		queueCloudPendingMessage(pending.attemptId, "Finish this draft");
		await vi.waitFor(() => expect(send).toHaveBeenCalledOnce());
		expect(getCloudPendingSession("session-1")?.messages[0]?.state).toBe("queued");
	});

	it("bounds local input and releases the startup layer only once", async () => {
		const pending = registerCloudPendingSession({
			attempt: beginCloudStartupAttempt("attempt-limits", 100),
			orgId: "org-1",
			projectId: "project-1",
			initialPrompt: "Initial task",
			create: async () => "session-1",
			send: async () => undefined,
		});
		expect(() => queueCloudPendingMessage(pending.attemptId, " ")).toThrow("between 1 and 65536 bytes");
		expect(() => queueCloudPendingMessage(pending.attemptId, "x".repeat(65_537))).toThrow(
			"between 1 and 65536 bytes",
		);
		await createCloudPendingSession(pending.attemptId);
		markCloudPendingSessionReady("session-1");
		markCloudPendingSessionReady("session-1");
		expect(getCloudPendingSession(pending.routeSessionId)?.createState).toBe("ready");
	});
});
