import { StrictMode, act, type PropsWithChildren } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { FleetBoardDemo } from "./FleetBoardDemo";

vi.mock("motion/react", () => ({
	AnimatePresence: ({ children }: PropsWithChildren) => children,
	LayoutGroup: ({ children }: PropsWithChildren) => children,
	motion: { div: ({ children }: PropsWithChildren) => <div>{children}</div> },
}));
vi.mock("../usePreviewScale", () => ({
	usePreviewScale: () => ({ viewportRef: null, viewportStyle: {}, canvasStyle: {} }),
}));

let container: HTMLDivElement;
let root: Root;
beforeEach(() => {
	vi.useFakeTimers();
	vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
	container = document.createElement("div");
	document.body.append(container);
	root = createRoot(container);
});
afterEach(() => {
	act(() => root.unmount());
	container.remove();
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
	vi.useRealTimers();
});

it("advances and spawns once per tick under StrictMode", () => {
	vi.spyOn(Math, "random").mockReturnValue(0);
	act(() => root.render(<StrictMode><FleetBoardDemo /></StrictMode>));
	expect(container.textContent).not.toContain("Add keyboard shortcut for session focus");
	act(() => vi.advanceTimersByTime(5000));
	expect(container.textContent).toContain("Add keyboard shortcut for session focus");
	expect(container.textContent).not.toContain("Lazy-load session terminal on first open");
	act(() => vi.advanceTimersByTime(5000));
	expect(container.textContent).toContain("Lazy-load session terminal on first open");
});

it("cancels pending card removal and animation ticks on unmount", () => {
	vi.spyOn(Math, "random").mockReturnValue(0.999);
	act(() => root.render(<StrictMode><FleetBoardDemo /></StrictMode>));
	act(() => vi.advanceTimersByTime(10994));
	// The selected merge card has a pending zero-delay removal timer.
	expect(vi.getTimerCount()).toBeGreaterThanOrEqual(2);
	act(() => root.unmount());
	expect(vi.getTimerCount()).toBe(0);
});
