import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	SIDEBAR_UPDATE_DISMISSAL_MS,
	SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY,
	useSidebarUpdateDismissal,
} from "./useSidebarUpdateDismissal";

describe("useSidebarUpdateDismissal", () => {
	beforeEach(() => {
		window.localStorage.clear();
		vi.useFakeTimers();
		vi.setSystemTime(new Date("2026-08-31T00:00:00Z"));
	});

	afterEach(() => vi.useRealTimers());

	it("hides the dismissed version and returns it after 24 hours", () => {
		const { result } = renderHook(() => useSidebarUpdateDismissal("9.9.9"));
		act(() => result.current.dismiss());
		expect(result.current.dismissed).toBe(true);
		expect(JSON.parse(localStorage.getItem(SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY)!)).toEqual({
			version: "9.9.9",
			dismissedUntil: Date.now() + SIDEBAR_UPDATE_DISMISSAL_MS,
		});

		act(() => vi.advanceTimersByTime(SIDEBAR_UPDATE_DISMISSAL_MS));
		expect(result.current.dismissed).toBe(false);
		expect(localStorage.getItem(SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY)).toBeNull();
	});

	it("shows a different version immediately", () => {
		const { result, rerender } = renderHook(
			({ version }) => useSidebarUpdateDismissal(version),
			{ initialProps: { version: "9.9.9" as string | undefined } },
		);
		act(() => result.current.dismiss());
		rerender({ version: "9.9.10" });
		expect(result.current.dismissed).toBe(false);
	});

	it("clears a dismissal that expired while the app was closed", () => {
		localStorage.setItem(SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY, JSON.stringify({
			version: "9.9.9",
			dismissedUntil: Date.now() - 1,
		}));

		const { result } = renderHook(() => useSidebarUpdateDismissal("9.9.9"));

		expect(result.current.dismissed).toBe(false);
		expect(localStorage.getItem(SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY)).toBeNull();
	});

	it("fails open for malformed storage", () => {
		localStorage.setItem(SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY, "not-json");
		const { result } = renderHook(() => useSidebarUpdateDismissal("9.9.9"));
		expect(result.current.dismissed).toBe(false);
		expect(localStorage.getItem(SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY)).toBeNull();
	});
});
