import { describe, expect, it } from "vitest";
import {
	compareVersions,
	DAYS_FOR_FLEXIBLE_UPDATE,
	describePrompt,
	floorSignal,
	floorTarget,
	describeStoreRow,
	localeRegion,
	lookupUrl,
	MAX_DISMISSALS,
	nextSnooze,
	parseLookup,
	playStoreUrls,
	PROMPT_INTERVAL_MS,
	shouldPrompt,
	type Snooze,
	type StoreCheck,
	storeRowResult,
	tierOf,
} from "./storeUpdate";

function lookup(over: Record<string, unknown> = {}) {
	return { resultCount: 1, results: [{ version: "1.3.0", trackId: 123, trackViewUrl: "https://apps.apple.com/app/id123", ...over }] };
}

describe("compareVersions", () => {
	it("orders numerically, not lexically", () => {
		expect(compareVersions("1.10.0", "1.9.0")).toBeGreaterThan(0);
		expect(compareVersions("1.9.0", "1.10.0")).toBeLessThan(0);
		expect(compareVersions("2.0.0", "1.99.99")).toBeGreaterThan(0);
	});

	it("treats missing segments as zero", () => {
		expect(compareVersions("1.2", "1.2.0")).toBe(0);
		expect(compareVersions("1.2.1", "1.2")).toBeGreaterThan(0);
	});

	// A garbled store answer must never read as "newer", or the app nags forever.
	it("treats non-numeric segments as zero rather than NaN", () => {
		expect(compareVersions("1.2.0-beta", "1.2.0")).toBe(0);
		expect(compareVersions("oops", "1.2.0")).toBeLessThan(0);
	});
});

describe("lookupUrl", () => {
	it("queries by bundle id, so no App Store id has to be configured", () => {
		expect(lookupUrl("aoagents.ao")).toBe("https://itunes.apple.com/lookup?bundleId=aoagents.ao");
	});

	// Without a storefront the API answers for the US store only, so a user
	// elsewhere reads as "no update" while their own store has one.
	it("adds the storefront when one is known", () => {
		expect(lookupUrl("aoagents.ao", "GB")).toBe("https://itunes.apple.com/lookup?bundleId=aoagents.ao&country=gb");
	});

	it("drops a region it cannot use rather than guessing", () => {
		for (const bad of [undefined, null, "", "  ", "eng", "1"]) {
			expect(lookupUrl("aoagents.ao", bad)).toBe("https://itunes.apple.com/lookup?bundleId=aoagents.ao");
		}
	});
});

describe("parseLookup", () => {
	it("reports an update when the store is ahead", () => {
		expect(parseLookup(lookup(), "1.2.1")).toEqual({
			updateAvailable: true,
			storeVersion: "1.3.0",
			storeUrl: "https://apps.apple.com/app/id123",
		});
	});

	it("reports none when the installed build matches or leads the store", () => {
		expect(parseLookup(lookup(), "1.3.0").updateAvailable).toBe(false);
		expect(parseLookup(lookup(), "1.4.0").updateAvailable).toBe(false);
	});

	// This is today's real answer for aoagents.ao: not on the App Store yet.
	it("treats an empty result set as no update, not an error", () => {
		expect(parseLookup({ resultCount: 0, results: [] }, "1.2.1")).toEqual({ updateAvailable: false });
	});

	it("fails open on a body it cannot read", () => {
		expect(parseLookup(null, "1.2.1").updateAvailable).toBe(false);
		expect(parseLookup({ results: "nope" }, "1.2.1").updateAvailable).toBe(false);
		expect(parseLookup(lookup({ version: 13 }), "1.2.1").updateAvailable).toBe(false);
	});

	it("fails open when the installed version is unknown", () => {
		expect(parseLookup(lookup(), null).updateAvailable).toBe(false);
		expect(parseLookup(lookup(), undefined).updateAvailable).toBe(false);
	});

	it("rebuilds the store URL from trackId when trackViewUrl is missing", () => {
		expect(parseLookup(lookup({ trackViewUrl: undefined }), "1.2.1").storeUrl).toBe("https://apps.apple.com/app/id123");
		expect(parseLookup(lookup({ trackViewUrl: undefined, trackId: undefined }), "1.2.1").storeUrl).toBeUndefined();
	});
});

describe("tierOf", () => {
	const available: StoreCheck = { updateAvailable: true };

	it("says nothing when there is no update or no answer", () => {
		expect(tierOf(null, "android")).toBe("none");
		expect(tierOf({ updateAvailable: false }, "android")).toBe("none");
		expect(tierOf(null, "ios")).toBe("none");
	});

	it("blocks on Android only when Play itself asks for the immediate flow", () => {
		expect(tierOf({ ...available, serverUpdateType: "IMMEDIATE", immediateAllowed: true }, "android")).toBe("required");
		expect(tierOf({ ...available, serverUpdateType: "FLEXIBLE", immediateAllowed: true }, "android")).toBe("recommended");
		// Play asked, but the device will not allow it — degrade rather than dead-end.
		expect(tierOf({ ...available, serverUpdateType: "IMMEDIATE", immediateAllowed: false }, "android")).toBe("recommended");
	});

	// Apple offers no priority channel and discourages hard-blocking.
	it("never blocks on iOS", () => {
		expect(tierOf({ ...available, serverUpdateType: "IMMEDIATE", immediateAllowed: true }, "ios")).toBe("recommended");
	});
});

describe("shouldPrompt", () => {
	const now = 1_700_000_000_000;
	const snooze = (over: Partial<Snooze> = {}): Snooze => ({ version: "1.3.0", dismissals: 1, lastPromptAt: now, ...over });

	it("always prompts for a critical release, however often it was dismissed", () => {
		const args = { tier: "required" as const, version: "1.3.0", now };
		expect(shouldPrompt({ ...args, snooze: snooze({ dismissals: 99 }) })).toBe(true);
	});

	it("says nothing when there is no update", () => {
		expect(shouldPrompt({ tier: "none", version: "1.3.0", snooze: null, now })).toBe(false);
	});

	it("prompts the first time, then holds for a day", () => {
		const args = { tier: "recommended" as const, version: "1.3.0", now };
		expect(shouldPrompt({ ...args, snooze: null })).toBe(true);
		expect(shouldPrompt({ ...args, snooze: snooze() })).toBe(false);
		expect(shouldPrompt({ ...args, snooze: snooze({ lastPromptAt: now - PROMPT_INTERVAL_MS }) })).toBe(true);
	});

	it("gives up after enough dismissals of the same version", () => {
		const args = { tier: "recommended" as const, version: "1.3.0", now };
		const stale = { lastPromptAt: now - PROMPT_INTERVAL_MS };
		expect(shouldPrompt({ ...args, snooze: snooze({ ...stale, dismissals: MAX_DISMISSALS - 1 }) })).toBe(true);
		expect(shouldPrompt({ ...args, snooze: snooze({ ...stale, dismissals: MAX_DISMISSALS }) })).toBe(false);
	});

	// A newer build on the store is a new ask, so the count starts over.
	it("starts over when the store moves on", () => {
		const spent = snooze({ dismissals: MAX_DISMISSALS });
		expect(shouldPrompt({ tier: "recommended", version: "1.4.0", snooze: spent, now })).toBe(true);
	});
});

describe("nextSnooze", () => {
	const now = 1_700_000_000_000;

	it("counts dismissals of the same version", () => {
		expect(nextSnooze(null, "1.3.0", now)).toEqual({ version: "1.3.0", dismissals: 1, lastPromptAt: now });
		const once = nextSnooze(null, "1.3.0", now);
		expect(nextSnooze(once, "1.3.0", now + 1)).toEqual({ version: "1.3.0", dismissals: 2, lastPromptAt: now + 1 });
	});

	it("resets the count for a different version", () => {
		const spent: Snooze = { version: "1.3.0", dismissals: 3, lastPromptAt: now };
		expect(nextSnooze(spent, "1.4.0", now)).toEqual({ version: "1.4.0", dismissals: 1, lastPromptAt: now });
	});
});

describe("floorTarget", () => {
	it("points at latest, falls back to min, and is undefined when the floor is empty", () => {
		expect(floorTarget({ latest: "1.4.0", min: "1.2.0" })).toBe("1.4.0");
		expect(floorTarget({ min: "1.2.0" })).toBe("1.2.0");
		expect(floorTarget({ min: "", latest: "" })).toBeUndefined();
		expect(floorTarget({})).toBeUndefined();
	});

	// The same filter as floorSignal: a "v1.4.0" the tier ignores must not name
	// the sheet or key the snooze either — the two must agree on what a version is.
	it("skips a value floorSignal would reject", () => {
		expect(floorTarget({ latest: "v1.4.0", min: "1.3.0" })).toBe("1.3.0");
		expect(floorTarget({ latest: "v1.4.0" })).toBeUndefined();
	});
});

describe("playStoreUrls", () => {
	it("prefers the Play app and keeps a browsable fallback", () => {
		expect(playStoreUrls("aoagents.dev")).toEqual({
			app: "market://details?id=aoagents.dev",
			web: "https://play.google.com/store/apps/details?id=aoagents.dev",
		});
	});
});

describe("describeStoreRow", () => {
	const idle = { enabled: true, checking: false, last: null };

	it("goes inert in development builds", () => {
		expect(describeStoreRow({ ...idle, enabled: false })).toEqual({
			value: "Off in development builds",
			tone: "default",
			busy: false,
			action: null,
		});
	});

	it("shows progress while checking", () => {
		expect(describeStoreRow({ ...idle, checking: true })).toEqual({
			value: "Checking…",
			tone: "default",
			busy: true,
			action: null,
		});
	});

	it("offers the store when an update is waiting", () => {
		expect(describeStoreRow({ ...idle, last: { kind: "available" } })).toEqual({
			value: "Update available",
			tone: "good",
			busy: false,
			action: "open",
		});
	});

	it("reports a failed or successful check, and stays re-checkable", () => {
		expect(describeStoreRow({ ...idle, last: { kind: "error" } })).toEqual({
			value: "Couldn't check",
			tone: "bad",
			busy: false,
			action: "check",
		});
		expect(describeStoreRow({ ...idle, last: { kind: "up-to-date" } })).toEqual({
			value: "Up to date",
			tone: "good",
			busy: false,
			action: "check",
		});
	});

	it("invites a first check before anything has run", () => {
		expect(describeStoreRow(idle)).toEqual({ value: "Check now", tone: "default", busy: false, action: "check" });
	});
});

// The Settings row and the launch nudge read the same tier, so they cannot
// disagree: during the unlisted-iOS window the sheet says a version is
// available, and this row must not answer "Up to date" to the user who opened
// Settings to act on it.
describe("storeRowResult", () => {
	it("reports available whenever the launch nudge would fire, floor included", () => {
		expect(storeRowResult({ updateAvailable: true }, "recommended")).toEqual({ kind: "available" });
		expect(storeRowResult({ updateAvailable: true }, "required")).toEqual({ kind: "available" });
		// The store says nothing is listed; the floor still knows better.
		expect(storeRowResult({ updateAvailable: false }, "recommended")).toEqual({ kind: "available" });
	});

	it("reports an unreachable store as an error only when the floor is silent too", () => {
		expect(storeRowResult(null, "none")).toEqual({ kind: "error" });
		expect(storeRowResult(null, "recommended")).toEqual({ kind: "available" });
	});

	it("is up to date only when the store and the floor both are", () => {
		expect(storeRowResult({ updateAvailable: false }, "none")).toEqual({ kind: "up-to-date" });
	});
});

describe("localeRegion", () => {
	it("finds the region wherever it sits", () => {
		expect(localeRegion("en-US")).toBe("US");
		expect(localeRegion("zh-Hans-CN")).toBe("CN");
		expect(localeRegion("ca-ES-valencia")).toBe("ES");
		expect(localeRegion("en-US-u-ca-gregory")).toBe("US");
	});

	it("answers null rather than guessing", () => {
		expect(localeRegion("en")).toBeNull();
		// "ca" here is the calendar extension key, not Canada.
		expect(localeRegion("en-u-ca-gregory")).toBeNull();
		// A UN M.49 numeric region; the iTunes lookup only takes ISO alpha-2.
		expect(localeRegion("es-419")).toBeNull();
	});
});


describe("floorSignal", () => {
	it("requires below min, recommends below latest, and is quiet above both", () => {
		const floor = { min: "1.2.0", latest: "1.4.0" };
		expect(floorSignal("1.1.0", floor)).toBe("required");
		expect(floorSignal("1.3.0", floor)).toBe("recommended");
		expect(floorSignal("1.4.0", floor)).toBe("none");
	});

	it("works with either value alone", () => {
		expect(floorSignal("1.1.0", { min: "1.2.0" })).toBe("required");
		expect(floorSignal("1.1.0", { latest: "1.2.0" })).toBe("recommended");
		expect(floorSignal("1.1.0", {})).toBe("none");
	});

	// A floor an operator typed wrong must do nothing, never something.
	it("treats anything that is not a plain dotted number as absent", () => {
		for (const bad of ["", "  ", "v1.3.0", "1.3.0-rc.1", "1.2.3.4", "abc", "10300"]) {
			expect(floorSignal("1.2.1", { min: bad, latest: bad })).toBe("none");
		}
	});

	it("does nothing when the installed version is unknown or unusable", () => {
		expect(floorSignal(null, { min: "9.9.9" })).toBe("none");
		expect(floorSignal("v1.2.1", { min: "9.9.9" })).toBe("none");
	});
});

// THE INTERLOCK. Asserted directly rather than left to the order of statements
// inside tierOf: this is the property that stops the floor from becoming a
// unilateral kill switch, and it must survive any refactor of that function.
describe("tierOf — the floor may only escalate a store-confirmed update", () => {
	const confirmed: StoreCheck = { updateAvailable: true };

	// Degrades to a nudge rather than going silent: the floor is still a real
	// opinion about this build, it just may not lock anyone out on its own.
	it("never blocks on a floor the store has not corroborated", () => {
		expect(tierOf(null, "android", "required")).toBe("recommended");
		expect(tierOf({ updateAvailable: false }, "android", "required")).toBe("recommended");
		expect(tierOf(null, "ios", "required")).toBe("recommended");
	});

	it("blocks only once the store confirms the update exists", () => {
		expect(tierOf(confirmed, "android", "required")).toBe("required");
		expect(tierOf(confirmed, "ios", "required")).toBe("required");
	});

	// The soft tier deliberately does NOT need corroboration — it is the only way
	// to surface anything on iOS before the App Store listing exists.
	it("still nudges from the floor alone", () => {
		expect(tierOf(null, "ios", "recommended")).toBe("recommended");
		expect(tierOf({ updateAvailable: false }, "ios", "recommended")).toBe("recommended");
	});

	it("leaves the no-floor behaviour exactly as it was", () => {
		expect(tierOf(confirmed, "android")).toBe("recommended");
		expect(tierOf(null, "android")).toBe("none");
	});
});

describe("shouldPrompt — staleness", () => {
	const now = 1_700_000_000_000;
	const args = { tier: "recommended" as const, version: "1.3.0", snooze: null, now };

	it("holds the first ask until the update has been on Play a few days", () => {
		expect(shouldPrompt({ ...args, stalenessDays: 0 })).toBe(false);
		expect(shouldPrompt({ ...args, stalenessDays: DAYS_FOR_FLEXIBLE_UPDATE - 1 })).toBe(false);
		expect(shouldPrompt({ ...args, stalenessDays: DAYS_FOR_FLEXIBLE_UPDATE })).toBe(true);
	});

	// Play not knowing must never read as "too fresh" — iOS never sets this.
	it("does not suppress when staleness is unknown", () => {
		expect(shouldPrompt({ ...args, stalenessDays: null })).toBe(true);
		expect(shouldPrompt(args)).toBe(true);
	});

	it("only gates the first ask; after that the snooze governs", () => {
		const asked = { version: "1.3.0", dismissals: 1, lastPromptAt: now - PROMPT_INTERVAL_MS };
		expect(shouldPrompt({ ...args, snooze: asked, stalenessDays: 0 })).toBe(true);
	});

	it("never delays a required update", () => {
		expect(shouldPrompt({ ...args, tier: "required", stalenessDays: 0 })).toBe(true);
	});
});

describe("describePrompt", () => {
	it("names the store only when the store actually answered", () => {
		expect(describePrompt({ version: "1.3.0", storeConfirmed: true, storeName: "App Store" })).toBe(
			"Version 1.3.0 is on the App Store.",
		);
		expect(describePrompt({ storeConfirmed: true, storeName: "Play Store" })).toBe("A newer version is on the Play Store.");
	});

	// During the TestFlight window the floor is the only signal, and there is no
	// App Store page to point at — the copy must not imply one.
	it("stays vague when only the floor fired", () => {
		expect(describePrompt({ version: "1.3.0", storeConfirmed: false, storeName: "App Store" })).toBe("Version 1.3.0 is available.");
		expect(describePrompt({ storeConfirmed: false, storeName: "App Store" })).toBe("A newer version is available.");
	});
});
