import { describe, expect, it, vi } from "vitest";
import { deferRouteContent, resetHeaderRightForSwap } from "./headerRightSwap";

describe("resetHeaderRightForSwap", () => {
	it("clears the previous native header item before installing the next one", () => {
		const setOptions = vi.fn();
		let scheduled: (() => void) | undefined;
		const cancel = vi.fn();
		const ready = vi.fn();

		resetHeaderRightForSwap(
			() => setOptions({ headerRight: undefined }),
			ready,
			(callback) => {
				scheduled = callback;
				return cancel;
			},
		);

		expect(setOptions).toHaveBeenCalledTimes(1);
		expect(setOptions).toHaveBeenLastCalledWith({ headerRight: undefined });
		expect(ready).not.toHaveBeenCalled();

		scheduled?.();
		expect(ready).toHaveBeenCalledTimes(1);
	});

	it("cancels the deferred install and clears the item during a mode change", () => {
		const setOptions = vi.fn();
		const cancel = vi.fn();
		const ready = vi.fn();
		const cleanup = resetHeaderRightForSwap(() => setOptions({ headerRight: undefined }), ready, () => cancel);

		cleanup();

		expect(cancel).toHaveBeenCalledTimes(1);
		expect(setOptions).toHaveBeenLastCalledWith({ headerRight: undefined });
		expect(ready).not.toHaveBeenCalled();
	});
});

describe("deferRouteContent", () => {
	it("waits for navigation interactions and cancels an abandoned route", () => {
		let scheduled: (() => void) | undefined;
		const ready = vi.fn();
		const cancel = vi.fn();
		const cleanup = deferRouteContent(ready, (callback) => {
			scheduled = callback;
			return cancel;
		});

		expect(ready).not.toHaveBeenCalled();
		scheduled?.();
		expect(ready).toHaveBeenCalledOnce();
		cleanup();
		expect(cancel).toHaveBeenCalledOnce();
	});
});
