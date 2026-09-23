import { describe, expect, it, vi } from "vitest";
import { resetHeaderRightForSwap } from "./headerRightSwap";

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
