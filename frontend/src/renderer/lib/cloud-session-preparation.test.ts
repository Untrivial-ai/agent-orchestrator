import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({ bind: vi.fn() }));

vi.mock("./cloud-startup-timing", () => ({
	bindCloudStartupAttempt: h.bind,
}));

import {
	isCloudSessionPreparationUnsupported,
	resetCloudSessionPreparationRegistryForTests,
	startCloudSessionPreparation,
	type CloudSessionPreparationRegistration,
} from "./cloud-session-preparation";

const attempt = { attemptId: "attempt-1", startedAtMs: 100 };

function lease(seconds = 120) {
	return {
		attachmentExpiresAt: new Date(Date.now() + seconds * 1_000).toISOString(),
		expiresAt: new Date(Date.now() + seconds * 1_000).toISOString(),
		generation: 1,
		leaseSeconds: seconds,
	};
}

function registration(
	overrides: Partial<CloudSessionPreparationRegistration> = {},
): CloudSessionPreparationRegistration {
	return {
		attempt,
		detach: vi.fn().mockResolvedValue(undefined),
		commit: vi.fn().mockResolvedValue(undefined),
		compatibilityKey: "project-1:codex:nodeops",
		create: vi.fn().mockImplementation(async () => ({ lease: lease(), sessionId: "session-1" })),
		renew: vi.fn().mockImplementation(async () => lease()),
		scopeKey: "org-1:project-1",
		...overrides,
	};
}

async function flushPromises(): Promise<void> {
	await Promise.resolve();
	await Promise.resolve();
	await Promise.resolve();
}

beforeEach(() => {
	vi.useFakeTimers();
	vi.setSystemTime(new Date("2026-09-23T12:00:00Z"));
	h.bind.mockReset();
});

afterEach(() => {
	resetCloudSessionPreparationRegistryForTests();
	vi.useRealTimers();
});

describe("cloud session preparation", () => {
	it("distinguishes an absent preparation route from a missing resource", () => {
		expect(isCloudSessionPreparationUnsupported({ status: 404 })).toBe(true);
		expect(isCloudSessionPreparationUnsupported({ status: 404, code: "not_found" })).toBe(false);
		expect(isCloudSessionPreparationUnsupported({ status: 500 })).toBe(false);
	});

	it("starts immediately and commits the same durable session", async () => {
		const options = registration();
		const preparation = startCloudSessionPreparation(options);

		expect(options.create).toHaveBeenCalledOnce();
		const sessionId = await preparation.commit({ displayName: "Fix startup", prompt: "Do the work" });

		expect(sessionId).toBe("session-1");
		expect(h.bind).toHaveBeenCalledWith("session-1", attempt);
		expect(options.commit).toHaveBeenCalledWith(
			"session-1",
			{ displayName: "Fix startup", prompt: "Do the work" },
			expect.any(String),
			expect.any(String),
			1,
		);
		preparation.release();
		expect(options.detach).not.toHaveBeenCalled();
	});

	it("keeps a preparation during close grace and reuses it on reopen", async () => {
		const onEvent = vi.fn();
		const options = registration({ onEvent });
		const first = startCloudSessionPreparation(options);
		await flushPromises();

		first.release();
		await flushPromises();
		const second = startCloudSessionPreparation(options);
		await flushPromises();

		expect(options.create).toHaveBeenCalledTimes(2);
		expect(options.renew).not.toHaveBeenCalled();
		expect(options.detach).toHaveBeenCalledOnce();
		expect(onEvent).toHaveBeenCalledWith("acquired", { acquisition: "new" });
		expect(onEvent).toHaveBeenCalledWith("detached", { session_id: "session-1" });
		expect(onEvent).toHaveBeenCalledWith("acquired", {
			acquisition: "reused",
			session_id: "session-1",
		});
		expect(onEvent).toHaveBeenCalledWith("reattached", { session_id: "session-1" });
		await expect(second.commit({ displayName: "Reopen", prompt: "Continue" })).resolves.toBe("session-1");
	});

	it("retries an ambiguous server reattach with its reattach idempotency key", async () => {
		const create = vi.fn()
			.mockImplementationOnce(async () => ({ lease: lease(), sessionId: "session-1" }))
			.mockRejectedValueOnce(new Error("offline"))
			.mockImplementationOnce(async () => ({ lease: lease(), sessionId: "session-1" }));
		const options = registration({ create });
		const first = startCloudSessionPreparation(options);
		await flushPromises();
		first.release();
		await flushPromises();

		const reopened = startCloudSessionPreparation(options);
		await flushPromises();
		await expect(reopened.commit({ displayName: "Reopen", prompt: "Continue" })).resolves.toBe("session-1");

		expect(create).toHaveBeenCalledTimes(3);
		expect(create.mock.calls[1]?.[0]).toBe(create.mock.calls[2]?.[0]);
	});

	it("renews after a close that happens before create resolves", async () => {
		let resolveCreate!: (value: { lease: ReturnType<typeof lease>; sessionId: string }) => void;
		const create = vi.fn(() => new Promise<{ lease: ReturnType<typeof lease>; sessionId: string }>((resolve) => {
			resolveCreate = resolve;
		}));
		const renew = vi.fn().mockImplementation(async () => lease());
		const options = registration({ create, renew });
		const preparation = startCloudSessionPreparation(options);

		preparation.release();
		resolveCreate({ lease: lease(), sessionId: "session-2" });
		await flushPromises();

		expect(renew).not.toHaveBeenCalled();
		expect(options.detach).toHaveBeenCalledWith("session-2", expect.any(String), 1);
	});

	it("shares one preparation across compatible mounts", async () => {
		const options = registration();
		const first = startCloudSessionPreparation(options);
		const second = startCloudSessionPreparation(options);
		await flushPromises();

		expect(options.create).toHaveBeenCalledOnce();
		first.release();
		await flushPromises();
		expect(options.renew).not.toHaveBeenCalled();
		second.release();
		await flushPromises();
		expect(options.detach).toHaveBeenCalledOnce();
	});

	it("detaches an incompatible preparation", async () => {
		const firstOptions = registration();
		startCloudSessionPreparation(firstOptions);
		await flushPromises();

		startCloudSessionPreparation(registration({ compatibilityKey: "project-1:codex:coder" }));
		await flushPromises();

		expect(firstOptions.detach).toHaveBeenCalledWith("session-1", expect.any(String), 1);
	});

	it("coalesces meaningful activity into one trailing renewal", async () => {
		const options = registration();
		const preparation = startCloudSessionPreparation(options);
		await flushPromises();

		preparation.recordActivity();
		preparation.recordActivity();
		await vi.advanceTimersByTimeAsync(44_999);
		expect(options.renew).not.toHaveBeenCalled();
		await vi.advanceTimersByTimeAsync(1);

		expect(options.renew).toHaveBeenCalledOnce();
	});

	it("starts a fresh preparation on activity after local expiry", async () => {
		const create = vi.fn()
			.mockImplementationOnce(async () => ({ lease: lease(1), sessionId: "session-1" }))
			.mockImplementationOnce(async () => ({ lease: lease(), sessionId: "session-2" }));
		const preparation = startCloudSessionPreparation(registration({ create }));
		await flushPromises();

		await vi.advanceTimersByTimeAsync(1_000);
		preparation.recordActivity();
		await flushPromises();

		expect(create).toHaveBeenCalledTimes(2);
		await expect(preparation.commit({ displayName: "Fresh", prompt: "Continue" })).resolves.toBe("session-2");
	});

	it("does not renew an untouched composer and replaces it after expiry", async () => {
		const create = vi.fn()
			.mockImplementationOnce(async () => ({ lease: lease(2), sessionId: "session-1" }))
			.mockImplementationOnce(async () => ({ lease: lease(), sessionId: "session-2" }));
		const options = registration({ create });
		startCloudSessionPreparation(options);
		await flushPromises();

		await vi.advanceTimersByTimeAsync(2_000);
		const reopened = startCloudSessionPreparation(options);
		await flushPromises();

		expect(options.renew).not.toHaveBeenCalled();
		expect(create).toHaveBeenCalledTimes(2);
		await expect(reopened.commit({ displayName: "Reopen", prompt: "Continue" })).resolves.toBe("session-2");
	});

	it("retries a failed create with the same idempotency key", async () => {
		const create = vi.fn()
			.mockRejectedValueOnce(new Error("offline"))
			.mockImplementationOnce(async () => ({ lease: lease(), sessionId: "session-3" }));
		const preparation = startCloudSessionPreparation(registration({ create }));
		await flushPromises();

		await expect(preparation.commit({ displayName: "Retry", prompt: "Continue" })).resolves.toBe("session-3");

		expect(create).toHaveBeenCalledTimes(2);
		expect(create.mock.calls[0]?.[0]).toBe(create.mock.calls[1]?.[0]);
	});

	it("does not retry a commit after explicit expiry", async () => {
		const error = Object.assign(new Error("expired"), { code: "PREPARATION_EXPIRED" });
		const commit = vi.fn().mockRejectedValue(error);
		const preparation = startCloudSessionPreparation(registration({ commit }));

		await expect(preparation.commit({ displayName: "Expired", prompt: "Continue" })).rejects.toBe(error);
		expect(commit).toHaveBeenCalledOnce();
	});

	it("retries an ambiguous commit with the same idempotency key", async () => {
		const commit = vi.fn()
			.mockRejectedValueOnce(new Error("offline"))
			.mockResolvedValueOnce(undefined);
		const preparation = startCloudSessionPreparation(registration({ commit }));

		await expect(preparation.commit({ displayName: "Retry", prompt: "Continue" })).resolves.toBe("session-1");
		expect(commit).toHaveBeenCalledTimes(2);
		expect(commit.mock.calls[0]?.[2]).toBe(commit.mock.calls[1]?.[2]);
	});
});
