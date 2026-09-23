import { describe, expect, it } from "vitest";

import {
	STALE_AFTER_MS,
	screenStateFor,
	showsList,
	showsStaleBanner,
	staleAgeLabel,
} from "./screenState";

const input = (over: Partial<Parameters<typeof screenStateFor>[0]> = {}) => ({
	configured: true,
	loading: false,
	itemCount: 3,
	error: false,
	...over,
});

describe("screenStateFor", () => {
	it("puts pairing ahead of everything else", () => {
		expect(screenStateFor(input({ configured: false, loading: true, error: true, itemCount: 0 })))
			.toEqual({ kind: "unconfigured" });
	});

	it("shows a spinner only while there is nothing cached", () => {
		expect(screenStateFor(input({ loading: true, itemCount: 0 }))).toEqual({ kind: "loading" });
	});

	// Regression: a refresh over existing rows must not blank the list — pull to
	// refresh already shows that something is happening.
	it("keeps showing rows during a refresh", () => {
		expect(screenStateFor(input({ loading: true, itemCount: 3 }))).toEqual({ kind: "ready" });
	});

	it("lets the failure own the screen when there is nothing to fall back on", () => {
		expect(screenStateFor(input({ error: true, itemCount: 0 }))).toEqual({ kind: "error" });
	});

	// The bug this module exists for: today all three screens put failure copy in
	// ListEmptyComponent, so a populated list with a dead poll looks live.
	it("reports a failed poll over cached rows as stale, not as an error", () => {
		expect(screenStateFor(input({ error: true, itemCount: 3, staleForMs: 90_000 })))
			.toEqual({ kind: "stale", ageMs: 90_000 });
	});

	it("reports stale with a zero age when the failure age is unknown", () => {
		expect(screenStateFor(input({ error: true, itemCount: 3 })))
			.toEqual({ kind: "stale", ageMs: 0 });
	});

	it("distinguishes an empty success from a failure", () => {
		expect(screenStateFor(input({ itemCount: 0 }))).toEqual({ kind: "empty" });
	});

	it("goes stale on age alone, because a silently stopped poll never errors", () => {
		expect(screenStateFor(input({ staleForMs: STALE_AFTER_MS }))).toEqual({
			kind: "stale",
			ageMs: STALE_AFTER_MS,
		});
	});

	it("does not flicker on a single dropped request", () => {
		expect(screenStateFor(input({ staleForMs: STALE_AFTER_MS - 1 }))).toEqual({ kind: "ready" });
	});

	it("honours a caller-supplied threshold", () => {
		expect(screenStateFor(input({ staleForMs: 5_000, staleAfterMs: 4_000 })))
			.toEqual({ kind: "stale", ageMs: 5_000 });
	});

	it("treats a never-fetched screen with rows as current", () => {
		expect(screenStateFor(input({ staleForMs: null }))).toEqual({ kind: "ready" });
	});
});

describe("staleAgeLabel", () => {
	// A banner that says the data is stale and "0m ago" in the same breath reads
	// as a bug, so the sub-minute case gets words rather than a number.
	it("says moments rather than zero below a minute", () => {
		expect(staleAgeLabel(0)).toBe("moments ago");
		expect(staleAgeLabel(59_000)).toBe("moments ago");
	});

	it("uses the same m/h/d vocabulary as the row timestamps", () => {
		expect(staleAgeLabel(60_000)).toBe("1m ago");
		expect(staleAgeLabel(45 * 60_000)).toBe("45m ago");
		expect(staleAgeLabel(60 * 60_000)).toBe("1h ago");
		expect(staleAgeLabel(23 * 60 * 60_000)).toBe("23h ago");
		expect(staleAgeLabel(24 * 60 * 60_000)).toBe("1d ago");
	});

	it("never reports a negative age when the device clock moves backwards", () => {
		expect(staleAgeLabel(-5_000)).toBe("moments ago");
	});
});

describe("presentation helpers", () => {
	it("renders the list for ready and stale only", () => {
		expect(showsList({ kind: "ready" })).toBe(true);
		expect(showsList({ kind: "stale", ageMs: 1 })).toBe(true);
		expect(showsList({ kind: "empty" })).toBe(false);
		expect(showsList({ kind: "error" })).toBe(false);
		expect(showsList({ kind: "loading" })).toBe(false);
		expect(showsList({ kind: "unconfigured" })).toBe(false);
	});

	it("banners staleness and nothing else", () => {
		expect(showsStaleBanner({ kind: "stale", ageMs: 1 })).toBe(true);
		expect(showsStaleBanner({ kind: "ready" })).toBe(false);
		expect(showsStaleBanner({ kind: "error" })).toBe(false);
	});
});
