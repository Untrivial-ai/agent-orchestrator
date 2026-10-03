import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../lib/bridge";
import { useWindowZoomFactor } from "./useWindowZoomFactor";

vi.mock("../lib/platform", () => ({ isMacPlatform: () => true }));

afterEach(() => vi.restoreAllMocks());

describe("macOS window zoom chrome", () => {
	it("refreshes after native menu zoom and restores root tokens on unmount", async () => {
		let zoom = 1;
		vi.spyOn(aoBridge.window, "getZoomFactor").mockImplementation(async () => zoom);
		const off = vi.fn();
		vi.spyOn(aoBridge.window, "onZoomFactor").mockReturnValue(off);
		const { result, unmount } = renderHook(useWindowZoomFactor);
		zoom = 0.75;
		act(() => window.dispatchEvent(new Event("resize")));
		await waitFor(() => expect(result.current).toBe(0.75));
		expect(document.documentElement.style.getPropertyValue("--ao-window-zoom")).toBe("0.75");
		expect(document.documentElement.style.getPropertyValue("--size-topbar-primary")).toBe("36px");
		unmount();
		expect(off).toHaveBeenCalledOnce();
		expect(document.documentElement.style.getPropertyValue("--ao-window-zoom")).toBe("");
	});

	it("does not let a stale initial read overwrite a newer native zoom event", async () => {
		let resolve!: (value: number) => void;
		vi.spyOn(aoBridge.window, "getZoomFactor").mockImplementation(() => new Promise<number>((done) => { resolve = done; }));
		let publish!: (value: number) => void;
		vi.spyOn(aoBridge.window, "onZoomFactor").mockImplementation((listener) => {
			publish = listener;
			return () => undefined;
		});
		const { result } = renderHook(useWindowZoomFactor);
		act(() => publish(1.25));
		await act(async () => resolve(1));
		expect(result.current).toBe(1.25);
	});
});
