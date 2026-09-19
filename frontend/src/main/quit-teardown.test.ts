import { describe, expect, it, vi } from "vitest";
import { teardownSessionsForQuit, type QuitTeardownDeps } from "./quit-teardown";

const appRunFile = () => JSON.stringify({ pid: 123, port: 3001, owner: "app" });

function mocks() {
	return {
		postShutdown: vi.fn<(port: number, body: unknown) => Promise<void>>(async () => {}),
		log: vi.fn<(message: string) => void>(() => {}),
	};
}

function deps(overrides: Partial<QuitTeardownDeps> = {}): QuitTeardownDeps {
	return {
		keepDaemon: false,
		readRunFile: async () => appRunFile(),
		postShutdown: async () => {},
		log: () => {},
		...overrides,
	};
}

describe("teardownSessionsForQuit", () => {
	it("skips entirely when the daemon is kept alive", async () => {
		const m = mocks();
		const readRunFile = vi.fn<() => Promise<string | null>>();
		await teardownSessionsForQuit(deps({ keepDaemon: true, readRunFile, ...m }));
		expect(readRunFile).not.toHaveBeenCalled();
		expect(m.postShutdown).not.toHaveBeenCalled();
	});

	it("quits silently when the run-file is missing", async () => {
		const m = mocks();
		await expect(
			teardownSessionsForQuit(deps({ readRunFile: async () => null, ...m })),
		).resolves.toBeUndefined();
		expect(m.postShutdown).not.toHaveBeenCalled();
	});

	it.each([
		["headless daemon", undefined],
		["persistent daemon", "persistent"],
	])("leaves a %s alone", async (_label, owner) => {
		const m = mocks();
		await teardownSessionsForQuit(deps({
			readRunFile: async () => JSON.stringify({ pid: 123, port: 3001, owner }),
			...m,
		}));
		expect(m.postShutdown).not.toHaveBeenCalled();
		expect(m.log).toHaveBeenCalledWith(expect.stringContaining("not app-owned"));
	});

	it("posts teardownSessions to an app-owned daemon", async () => {
		const m = mocks();
		await teardownSessionsForQuit(deps(m));
		expect(m.postShutdown).toHaveBeenCalledTimes(1);
		expect(m.postShutdown).toHaveBeenCalledWith(3001, { teardownSessions: true });
	});

	it("never rejects when the daemon is unreachable", async () => {
		const m = mocks();
		m.postShutdown.mockRejectedValueOnce(new Error("connection refused"));
		await expect(teardownSessionsForQuit(deps(m))).resolves.toBeUndefined();
		expect(m.log).toHaveBeenCalled();
	});

	it("never blocks quit past the timeout", async () => {
		const m = mocks();
		m.postShutdown.mockImplementationOnce(() => new Promise<void>(() => {}));
		await expect(teardownSessionsForQuit(deps(m), 20)).resolves.toBeUndefined();
		expect(m.log).toHaveBeenCalled();
	});
});
