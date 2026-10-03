import { act, renderHook, waitFor } from "@testing-library/react";
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

	function widthVar() {
		return document.documentElement.style.getPropertyValue("--test-resizable-w");
	}

	function press(result: { current: ReturnType<typeof useResizable> }, clientX: number) {
		act(() => {
			result.current.onPointerDown({
				preventDefault: vi.fn(),
				clientX,
				pointerId: 1,
				currentTarget: { setPointerCapture: vi.fn(), hasPointerCapture: () => false, releasePointerCapture: vi.fn() },
			} as unknown as React.PointerEvent<HTMLElement>);
		});
	}

	function pointer(type: "pointermove" | "pointerup", clientX: number) {
		act(() => {
			window.dispatchEvent(new PointerEvent(type, { bubbles: true, clientX, pointerId: 1 }));
		});
	}

	// The live cap follows the window; 300 is a narrow window (or a zoomed one).
	let cap = 300;
	const max = () => cap;
	const min = () => Math.min(300, cap);

	function resizeWindow(width: number) {
		cap = width;
		act(() => {
			window.dispatchEvent(new Event("resize"));
		});
	}

	beforeEach(() => {
		cap = 300;
	});

	it("grows a width clamped in a narrow window back to the preference when the window widens", async () => {
		const { rerender } = renderHook(
			({ maxWidth }: { maxWidth: () => number }) =>
				useResizable({
					cssVar: "--test-resizable-w",
					storageKey: "test-resizable-w",
					defaultWidth: 900,
					min,
					max: maxWidth,
					edge: "left",
					reclampOnWindowResize: true,
				}),
			{ initialProps: { maxWidth: max } },
		);
		expect(widthVar()).toBe("300px");

		// A profile switch hands in new constraint callbacks and re-runs the restore.
		rerender({ maxWidth: () => cap });
		expect(widthVar()).toBe("300px");

		resizeWindow(744);
		await waitFor(() => expect(widthVar()).toBe("744px"));
		resizeWindow(1200);
		await waitFor(() => expect(widthVar()).toBe("900px"));
		expect(window.localStorage.getItem("test-resizable-w")).toBeNull();

		// Grow only: narrowing leaves the var to the consumer's CSS max-width.
		resizeWindow(300);
		await new Promise((resolve) => window.requestAnimationFrame(resolve));
		expect(widthVar()).toBe("900px");
	});

	it("leaves the width to an active drag when the window resizes", async () => {
		const { result } = renderHook(() =>
			useResizable({
				cssVar: "--test-resizable-w",
				storageKey: "test-resizable-w",
				defaultWidth: 900,
				min,
				max,
				edge: "left",
				reclampOnWindowResize: true,
			}),
		);
		expect(widthVar()).toBe("300px");

		press(result, 100);
		resizeWindow(1200);
		await new Promise((resolve) => window.requestAnimationFrame(resolve));
		expect(widthVar()).toBe("300px");
		pointer("pointerup", 100);

		resizeWindow(1200);
		await waitFor(() => expect(widthVar()).toBe("900px"));
	});

	it("ignores window resizes unless asked to re-clamp", async () => {
		renderHook(() =>
			useResizable({
				cssVar: "--test-resizable-w",
				storageKey: "test-resizable-w",
				defaultWidth: 900,
				min,
				max,
				edge: "right",
			}),
		);
		expect(widthVar()).toBe("300px");

		resizeWindow(1200);
		await new Promise((resolve) => window.requestAnimationFrame(resolve));
		expect(widthVar()).toBe("300px");
	});

	it("resets the preference on double-click even while the live max is narrow", async () => {
		window.localStorage.setItem("test-resizable-w", "700");
		const { result } = renderHook(() =>
			useResizable({
				cssVar: "--test-resizable-w",
				storageKey: "test-resizable-w",
				defaultWidth: 900,
				min,
				max,
				edge: "left",
				reclampOnWindowResize: true,
			}),
		);

		act(() => result.current.onDoubleClick());
		expect(widthVar()).toBe("300px");
		expect(window.localStorage.getItem("test-resizable-w")).toBe("900");

		resizeWindow(1200);
		await waitFor(() => expect(widthVar()).toBe("900px"));
	});

	it("does not write or persist a width for a press, or a drag that cannot change it", async () => {
		const { result } = renderHook(() =>
			useResizable({
				cssVar: "--test-resizable-w",
				storageKey: "test-resizable-w",
				defaultWidth: 900,
				min,
				max,
				edge: "left",
				reclampOnWindowResize: true,
			}),
		);

		press(result, 100);
		pointer("pointerup", 100);
		press(result, 100);
		pointer("pointermove", 40);
		pointer("pointerup", 40);
		expect(window.localStorage.getItem("test-resizable-w")).toBeNull();

		// Neither pinned the narrow width: the preference still wins once there is room.
		resizeWindow(1200);
		await waitFor(() => expect(widthVar()).toBe("900px"));
	});

	it("persists a drag that changes the width and keeps it as the preference", async () => {
		cap = 400;
		const { result } = renderHook(() =>
			useResizable({
				cssVar: "--test-resizable-w",
				storageKey: "test-resizable-w",
				defaultWidth: 200,
				min: 100,
				max,
				edge: "left",
				reclampOnWindowResize: true,
			}),
		);

		press(result, 100);
		pointer("pointermove", 60);
		pointer("pointerup", 60);
		expect(widthVar()).toBe("240px");
		expect(window.localStorage.getItem("test-resizable-w")).toBe("240");

		resizeWindow(1200);
		await new Promise((resolve) => window.requestAnimationFrame(resolve));
		expect(widthVar()).toBe("240px");
	});

	it("keeps clamping the restored width to a static max", () => {
		window.localStorage.setItem("test-resizable-w", "600");
		renderHook(() =>
			useResizable({
				cssVar: "--test-resizable-w",
				storageKey: "test-resizable-w",
				defaultWidth: 200,
				min: 100,
				max: 400,
				edge: "right",
			}),
		);
		expect(widthVar()).toBe("400px");
	});
});
