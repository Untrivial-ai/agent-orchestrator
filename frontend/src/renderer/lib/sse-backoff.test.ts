import { describe, expect, it } from "vitest";
import { SSE_BACKOFF_INIT_MS, SSE_BACKOFF_MAX_MS, computeSseRetryDelayMs } from "./sse-backoff";

/** Jitter at its top of range, so the delay equals the un-jittered step. */
const noJitter = () => 0.999_999;
/** Jitter at its floor: half the step. */
const fullJitter = () => 0;

describe("computeSseRetryDelayMs", () => {
	it("starts at the initial delay on the first failure", () => {
		expect(computeSseRetryDelayMs(1, noJitter)).toBe(SSE_BACKOFF_INIT_MS);
		expect(computeSseRetryDelayMs(1, fullJitter)).toBe(SSE_BACKOFF_INIT_MS / 2);
	});

	it("doubles the step per consecutive failure until the ceiling", () => {
		expect(computeSseRetryDelayMs(2, noJitter)).toBe(10_000);
		expect(computeSseRetryDelayMs(3, noJitter)).toBe(20_000);
		expect(computeSseRetryDelayMs(4, noJitter)).toBe(40_000);
		expect(computeSseRetryDelayMs(5, noJitter)).toBe(SSE_BACKOFF_MAX_MS);
	});

	it("never exceeds the ceiling, however long the stream stays broken", () => {
		for (const failures of [5, 12, 40, 5_000, Number.MAX_SAFE_INTEGER]) {
			expect(computeSseRetryDelayMs(failures, noJitter)).toBeLessThanOrEqual(SSE_BACKOFF_MAX_MS);
		}
	});

	it("keeps every delay finite and positive across the whole range", () => {
		for (let failures = 1; failures <= 64; failures += 1) {
			const delay = computeSseRetryDelayMs(failures);
			expect(Number.isFinite(delay)).toBe(true);
			expect(delay).toBeGreaterThanOrEqual(SSE_BACKOFF_INIT_MS / 2);
			expect(delay).toBeLessThanOrEqual(SSE_BACKOFF_MAX_MS);
		}
	});

	it("holds the jitter inside [0.5, 1) of the step so retries spread out", () => {
		const step = SSE_BACKOFF_MAX_MS;
		const delays = new Set<number>();
		for (let i = 0; i < 500; i += 1) {
			const delay = computeSseRetryDelayMs(9);
			expect(delay).toBeGreaterThanOrEqual(step * 0.5);
			expect(delay).toBeLessThanOrEqual(step);
			delays.add(delay);
		}
		// Real randomness, not a constant: concurrent streams must desynchronise.
		expect(delays.size).toBeGreaterThan(1);
	});

	it("treats a non-positive or non-finite failure count as the first failure", () => {
		for (const failures of [0, -1, -1_000, Number.NaN, Number.POSITIVE_INFINITY]) {
			expect(computeSseRetryDelayMs(failures, noJitter)).toBe(SSE_BACKOFF_INIT_MS);
		}
	});

	it("ignores a jitter source that strays outside [0, 1)", () => {
		expect(computeSseRetryDelayMs(1, () => 5)).toBe(SSE_BACKOFF_INIT_MS);
		expect(computeSseRetryDelayMs(1, () => -5)).toBe(SSE_BACKOFF_INIT_MS / 2);
	});
});
