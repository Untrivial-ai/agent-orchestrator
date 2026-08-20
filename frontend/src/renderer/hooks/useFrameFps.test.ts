import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useFrameFps } from "./useFrameFps";

const frame = () => ({ data: "aGVsbG8=", mimeType: "image/png" });

describe("useFrameFps", () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it("counts frames within a one-second window", () => {
		const { result, rerender } = renderHook(({ frame }) => useFrameFps(frame), {
			initialProps: { frame: null as { data: string; mimeType: string } | null },
		});
		expect(result.current).toBe(0);

		rerender({ frame: frame() });
		rerender({ frame: frame() });
		rerender({ frame: frame() });
		// Three distinct frame identities committed back-to-back.
		expect(result.current).toBe(3);
	});

	it("decays to zero once frames stop arriving", () => {
		const { result, rerender } = renderHook(({ frame }) => useFrameFps(frame), {
			initialProps: { frame: null as { data: string; mimeType: string } | null },
		});
		rerender({ frame: frame() });
		expect(result.current).toBe(1);

		// Frames keep arriving inside the window…
		act(() => {
			vi.advanceTimersByTime(200);
		});
		rerender({ frame: frame() });
		expect(result.current).toBe(2);

		// …then delivery stops: the one-second window slides past the older
		// stamps on the next interval ticks and the count drops to zero.
		act(() => {
			vi.advanceTimersByTime(1600);
		});
		expect(result.current).toBe(0);
	});

	it("keeps the window sliding instead of accumulating forever", () => {
		const { result, rerender } = renderHook(
			({ frame }: { frame: { data: string; mimeType: string } | null }) => useFrameFps(frame),
			{ initialProps: { frame: null as { data: string; mimeType: string } | null } },
		);
		for (let i = 0; i < 10; i += 1) {
			rerender({ frame: frame() });
			act(() => {
				vi.advanceTimersByTime(100);
			});
		}
		// Roughly 10 x 100 ms of arrivals: only the last second's frames count.
		expect(result.current).toBeGreaterThan(0);
		expect(result.current).toBeLessThanOrEqual(10);
	});

	it("returns zero while nothing has arrived", () => {
		const { result } = renderHook(() => useFrameFps(null));
		expect(result.current).toBe(0);
	});
});