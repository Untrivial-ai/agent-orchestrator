import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useResizable } from "./useResizable";

describe("useResizable", () => {
	beforeEach(() => {
		document.body.classList.remove("is-resizing-x");
		document.documentElement.style.removeProperty("--test-resizable-w");
		window.localStorage.clear();
	});

	it("captures the pointer and clears resize state when the drag ends", () => {
		const setPointerCapture = vi.fn();
		const releasePointerCapture = vi.fn();
		const { result } = renderHook(() =>
			useResizable({
				cssVar: "--test-resizable-w",
				storageKey: "test-resizable-w",
				defaultWidth: 200,
				min: 100,
				max: 400,
				edge: "left",
			}),
		);

		act(() => {
			result.current.onPointerDown({
				preventDefault: vi.fn(),
				clientX: 100,
				pointerId: 7,
				currentTarget: {
					setPointerCapture,
					hasPointerCapture: () => true,
					releasePointerCapture,
				},
			} as unknown as React.PointerEvent<HTMLElement>);
		});

		expect(document.body.classList.contains("is-resizing-x")).toBe(true);
		expect(setPointerCapture).toHaveBeenCalledWith(7);

		act(() => {
			window.dispatchEvent(new PointerEvent("pointercancel", { bubbles: true, pointerId: 7 }));
		});

		expect(document.body.classList.contains("is-resizing-x")).toBe(false);
		expect(releasePointerCapture).toHaveBeenCalledWith(7);
	});
});
