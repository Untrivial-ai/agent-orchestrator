import { describe, expect, it } from "vitest";
import { isCommandPaletteEnabled, isNightlyBuild, parseNightlyVersion } from "./build-channel";

describe("isNightlyBuild", () => {
	it("detects -nightly. stamps and rejects everything else", () => {
		expect(isNightlyBuild("0.10.4-nightly.202607071200+abc123")).toBe(true);
		expect(isNightlyBuild("0.10.3")).toBe(false);
		expect(isNightlyBuild(undefined)).toBe(false);
		expect(isNightlyBuild("0.0.0-preview")).toBe(false);
		expect(isNightlyBuild("0.0.0-test")).toBe(false);
	});
});

describe("isCommandPaletteEnabled", () => {
	it("is on in dev or nightly, off otherwise", () => {
		expect(isCommandPaletteEnabled("0.10.3", true)).toBe(true);
		expect(isCommandPaletteEnabled(undefined, true)).toBe(true);
		expect(isCommandPaletteEnabled("0.10.4-nightly.202607071200+abc123", false)).toBe(true);
		expect(isCommandPaletteEnabled("0.10.3", false)).toBe(false);
		expect(isCommandPaletteEnabled(undefined, false)).toBe(false);
	});
});

describe("parseNightlyVersion", () => {
	it("splits a nightly stamp into base version and UTC build instant", () => {
		const parsed = parseNightlyVersion("0.12.11-nightly.202609021713");
		expect(parsed?.base).toBe("0.12.11");
		// The stamp encodes the build time in UTC. Assert the absolute instant,
		// not local getters: the value must not depend on the timezone of the
		// machine running the tests.
		expect(parsed?.builtAt.toISOString()).toBe("2026-09-02T17:13:00.000Z");
		expect(parsed?.builtAt.getTime()).toBe(Date.UTC(2026, 8, 2, 17, 13));
	});

	it("renders the same instant in positive and negative offsets", () => {
		const parsed = parseNightlyVersion("0.12.11-nightly.202609071035");
		if (!parsed) throw new Error("expected a valid nightly stamp to parse");
		const time = new Intl.DateTimeFormat("en-US", {
			timeZone: "Asia/Kolkata",
			month: "short",
			day: "numeric",
			hour: "numeric",
			minute: "2-digit",
			hour12: true,
		}).format(parsed.builtAt);
		expect(time).toBe("Sep 7, 4:05 PM");
		const pacificTime = new Intl.DateTimeFormat("en-US", {
			timeZone: "America/Los_Angeles",
			month: "short",
			day: "numeric",
			hour: "numeric",
			minute: "2-digit",
			hour12: true,
		}).format(parsed.builtAt);
		expect(pacificTime).toBe("Sep 7, 3:35 AM");
	});

	it("lands on the correct local calendar day across the UTC day boundary", () => {
		// 03:00 UTC is already the next calendar day in Kolkata but still the
		// previous evening in Los Angeles; date-only surfaces must show Sep 6
		// to a user in Los Angeles.
		const parsed = parseNightlyVersion("0.12.11-nightly.202609070300");
		if (!parsed) throw new Error("expected a valid nightly stamp to parse");
		expect(parsed.builtAt.toISOString()).toBe("2026-09-07T03:00:00.000Z");
		expect(
			new Intl.DateTimeFormat("en-US", { timeZone: "Asia/Kolkata", month: "short", day: "numeric" }).format(
				parsed.builtAt,
			),
		).toBe("Sep 7");
		expect(
			new Intl.DateTimeFormat("en-US", { timeZone: "America/Los_Angeles", month: "short", day: "numeric" }).format(
				parsed.builtAt,
			),
		).toBe("Sep 6");
	});

	it("tolerates a trailing commit stamp", () => {
		expect(parseNightlyVersion("0.10.4-nightly.202607071200+abc123")?.base).toBe("0.10.4");
	});

	it("returns null for stable, feature and malformed versions", () => {
		expect(parseNightlyVersion("0.12.10")).toBeNull();
		expect(parseNightlyVersion("0.12.0-pr4473.202608271542")).toBeNull();
		expect(parseNightlyVersion(undefined)).toBeNull();
		expect(parseNightlyVersion("0.12.11-nightly.2026")).toBeNull();
		expect(parseNightlyVersion("0.12.11-nightly.202699021713")).toBeNull();
	});
});
