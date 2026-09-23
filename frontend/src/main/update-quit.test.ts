// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { finishUpdateQuit } from "./update-quit";
afterEach(() => vi.useRealTimers());
const actions = () => ({ quit: vi.fn(), exit: vi.fn(), log: vi.fn() });
it("allows cleanup to finish before a normal update quit", async () => {
	const a = actions();
	await finishUpdateQuit(Promise.resolve(), a);
	expect(a.quit).toHaveBeenCalledOnce();
	expect(a.exit).not.toHaveBeenCalled();
});
it("releases a hung cleanup and ignores its late completion", async () => {
	vi.useFakeTimers();
	let complete!: () => void;
	const cleanup = new Promise<void>((resolve) => { complete = resolve; });
	const a = actions();
	const done = finishUpdateQuit(cleanup, a);
	await vi.advanceTimersByTimeAsync(14_999);
	expect(a.exit).not.toHaveBeenCalled();
	await vi.advanceTimersByTimeAsync(1);
	await done;
	expect(a.exit).toHaveBeenCalledOnce();
	complete();
	await Promise.resolve();
	expect(a.quit).not.toHaveBeenCalled();
});
it("does not let a rejected cleanup prevent the prepared restart", async () => {
	const a = actions();
	const error = new Error("telemetry shutdown failed");
	await finishUpdateQuit(Promise.reject(error), a);
	expect(a.log).toHaveBeenCalledWith(error);
	expect(a.quit).toHaveBeenCalledOnce();
	expect(a.exit).not.toHaveBeenCalled();
});
