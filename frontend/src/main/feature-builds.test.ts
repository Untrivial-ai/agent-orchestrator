// @vitest-environment node
import { describe, it, expect, vi, afterEach } from "vitest";
import { listFeatureBuilds, parseFeatureBuild, reconcileFeaturePin } from "./feature-builds";
import type { UpdateSettings } from "./update-settings";

// Mock the electron module so `app.getVersion()` works outside Electron.
vi.mock("electron", () => ({
	app: { getVersion: () => "0.2.0" },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const DAY_MS = 24 * 60 * 60 * 1000;

/** Build a mock GitHub releases API response entry. */
const DEFAULT_MARKER = '<!-- ao-feature-build: {"pr":2270,"base":"main","sha":"abc1234","slug":"pr2270"} -->';

function makeRelease(
	overrides: {
		tag_name?: string;
		name?: string;
		prerelease?: boolean;
		published_at?: string;
		body?: string | null;
	} = {},
) {
	return {
		tag_name: overrides.tag_name ?? "v0.2.0-pr2270.202607061200",
		name: overrides.name ?? "Feature build pr2270",
		prerelease: overrides.prerelease ?? true,
		published_at: overrides.published_at ?? new Date(Date.now() - DAY_MS).toISOString(),
		// Use "body" key with explicit null handling — ?? passes null through as null.
		body: "body" in overrides ? overrides.body : DEFAULT_MARKER,
	};
}

/** Stub fetch with a series of responses. The first call returns releases; subsequent calls return PR state. */
function stubFetch(releasesPayload: unknown, prStates: Record<number, { state: string }> = {}) {
	let callIndex = 0;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (url: string) => {
			// First call is always the /releases endpoint.
			if (callIndex === 0) {
				callIndex++;
				return {
					ok: true,
					json: async () => releasesPayload,
				};
			}
			// Subsequent calls are /pulls/<n> checks.
			callIndex++;
			const match = String(url).match(/\/pulls\/(\d+)$/);
			const pr = match ? parseInt(match[1], 10) : -1;
			if (pr in prStates) {
				return { ok: true, json: async () => prStates[pr] };
			}
			// Default: open.
			return { ok: true, json: async () => ({ state: "open" }) };
		}),
	);
}

// ---------------------------------------------------------------------------
// parseFeatureBuild
// ---------------------------------------------------------------------------

describe("parseFeatureBuild", () => {
	it("returns { pr } for a feature-build version", () => {
		expect(parseFeatureBuild("0.2.0-pr2270.202607061200+abc1234")).toEqual({ pr: 2270 });
	});

	it("strips a leading v", () => {
		expect(parseFeatureBuild("v0.2.0-pr2270.202607061200")).toEqual({ pr: 2270 });
	});

	it("returns null for a plain stable version", () => {
		expect(parseFeatureBuild("0.2.0")).toBeNull();
	});

	it("returns null for a nightly version (no -pr<N>. segment)", () => {
		expect(parseFeatureBuild("0.3.0-nightly.202607060000")).toBeNull();
		expect(parseFeatureBuild("0.3.0-nightly.202607060000+abc1234")).toBeNull();
	});

	it("returns null for an empty string", () => {
		expect(parseFeatureBuild("")).toBeNull();
	});
});

// ---------------------------------------------------------------------------
// listFeatureBuilds
// ---------------------------------------------------------------------------

describe("listFeatureBuilds", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("returns [] when the releases fetch throws", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				throw new Error("network error");
			}),
		);
		const result = await listFeatureBuilds();
		expect(result).toEqual([]);
	});

	it("returns [] when the releases fetch returns a non-ok response", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => ({ ok: false, status: 403, json: async () => ({}) })),
		);
		const result = await listFeatureBuilds();
		expect(result).toEqual([]);
	});

	it("excludes non-prerelease releases", async () => {
		stubFetch([makeRelease({ prerelease: false })]);
		const result = await listFeatureBuilds();
		expect(result).toEqual([]);
	});

	it("excludes releases with no ao-feature-build marker", async () => {
		stubFetch([makeRelease({ body: "Just a normal release description." })]);
		const result = await listFeatureBuilds();
		expect(result).toEqual([]);
	});

	it("excludes releases with a null body", async () => {
		stubFetch([makeRelease({ body: null })]);
		const result = await listFeatureBuilds();
		expect(result).toEqual([]);
	});

	it("excludes releases with a malformed marker JSON", async () => {
		stubFetch([makeRelease({ body: "<!-- ao-feature-build: {bad json} -->" })]);
		const result = await listFeatureBuilds();
		expect(result).toEqual([]);
	});

	it("excludes releases published more than 7 days ago", async () => {
		const old = new Date(Date.now() - 8 * DAY_MS).toISOString();
		stubFetch([makeRelease({ published_at: old })], { 2270: { state: "open" } });
		const result = await listFeatureBuilds();
		expect(result).toEqual([]);
	});

	it("keeps a release published exactly within the 7-day window", async () => {
		const recent = new Date(Date.now() - 6 * DAY_MS).toISOString();
		stubFetch([makeRelease({ published_at: recent })], { 2270: { state: "open" } });
		const result = await listFeatureBuilds();
		expect(result).toHaveLength(1);
		expect(result[0].pr).toBe(2270);
	});

	it("excludes builds whose PR is closed", async () => {
		stubFetch([makeRelease()], { 2270: { state: "closed" } });
		const result = await listFeatureBuilds();
		expect(result).toEqual([]);
	});

	it("excludes builds whose PR is merged", async () => {
		stubFetch([makeRelease()], { 2270: { state: "merged" } });
		const result = await listFeatureBuilds();
		expect(result).toEqual([]);
	});

	it("KEEPS a build when the /pulls/<n> check errors (resilience)", async () => {
		let callIndex = 0;
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				if (callIndex === 0) {
					callIndex++;
					return { ok: true, json: async () => [makeRelease()] };
				}
				// PR check: throw a network error.
				throw new Error("rate limited");
			}),
		);
		const result = await listFeatureBuilds();
		// On error, isPrOpen returns true so the build is kept.
		expect(result).toHaveLength(1);
		expect(result[0].pr).toBe(2270);
	});

	it("strips the workflow's [feature] PR #N: title prefix so UI labels do not double-prefix", async () => {
		const publishedAt = new Date(Date.now() - DAY_MS).toISOString();
		stubFetch(
			[
				makeRelease({
					tag_name: "v0.2.0-pr2270.202607061200",
					name: "[feature] PR #2270: feat: per-PR installable builds",
					published_at: publishedAt,
				}),
			],
			{ 2270: { state: "open" } },
		);
		const result = await listFeatureBuilds();
		expect(result).toHaveLength(1);
		expect(result[0].title).toBe("feat: per-PR installable builds");
	});

	it("keeps a release title without the authored prefix unchanged", async () => {
		const publishedAt = new Date(Date.now() - DAY_MS).toISOString();
		stubFetch(
			[
				makeRelease({
					tag_name: "v0.2.0-pr2270.202607061200",
					name: "Feature build pr2270",
					published_at: publishedAt,
				}),
			],
			{ 2270: { state: "open" } },
		);
		const result = await listFeatureBuilds();
		expect(result).toHaveLength(1);
		expect(result[0].title).toBe("Feature build pr2270");
	});

	it("returns a FeatureBuild with the expected shape fields", async () => {
		const publishedAt = new Date(Date.now() - DAY_MS).toISOString();
		stubFetch(
			[
				makeRelease({
					tag_name: "v0.2.0-pr2270.202607061200",
					name: "Feature build pr2270",
					published_at: publishedAt,
				}),
			],
			{ 2270: { state: "open" } },
		);
		const result = await listFeatureBuilds();
		expect(result).toHaveLength(1);
		const build = result[0];
		expect(build.pr).toBe(2270);
		expect(build.title).toBe("Feature build pr2270");
		expect(build.base).toBe("main");
		expect(build.sha).toBe("abc1234");
		expect(build.slug).toBe("pr2270");
		expect(build.buildId).toBe("v0.2.0-pr2270.202607061200");
		expect(build.publishedAt).toBe(publishedAt);
		// publishedMs must NOT be present (internal field stripped).
		expect((build as unknown as Record<string, unknown>).publishedMs).toBeUndefined();
	});

	it("groups multiple builds of the same PR, keeping only the newest", async () => {
		const older = new Date(Date.now() - 2 * DAY_MS).toISOString();
		const newer = new Date(Date.now() - DAY_MS).toISOString();
		stubFetch(
			[
				makeRelease({
					tag_name: "v0.2.0-pr2270.202607050000",
					name: "Feature build pr2270 old",
					published_at: older,
				}),
				makeRelease({
					tag_name: "v0.2.0-pr2270.202607061200",
					name: "Feature build pr2270 new",
					published_at: newer,
				}),
			],
			{ 2270: { state: "open" } },
		);
		const result = await listFeatureBuilds();
		expect(result).toHaveLength(1);
		expect(result[0].buildId).toBe("v0.2.0-pr2270.202607061200");
		expect(result[0].title).toBe("Feature build pr2270 new");
	});

	it("sorts results newest-first across multiple PRs", async () => {
		const t1 = new Date(Date.now() - 3 * DAY_MS).toISOString();
		const t2 = new Date(Date.now() - DAY_MS).toISOString();
		const t3 = new Date(Date.now() - 2 * DAY_MS).toISOString();
		stubFetch(
			[
				makeRelease({
					tag_name: "v0.2.0-pr2271.202607040000",
					name: "pr2271 build",
					published_at: t1,
					body: '<!-- ao-feature-build: {"pr":2271,"base":"main","sha":"def5678","slug":"pr2271"} -->',
				}),
				makeRelease({
					tag_name: "v0.2.0-pr2272.202607060000",
					name: "pr2272 build",
					published_at: t2,
					body: '<!-- ao-feature-build: {"pr":2272,"base":"main","sha":"ghi9012","slug":"pr2272"} -->',
				}),
				makeRelease({
					tag_name: "v0.2.0-pr2270.202607050000",
					name: "pr2270 build",
					published_at: t3,
					body: '<!-- ao-feature-build: {"pr":2270,"base":"main","sha":"abc1234","slug":"pr2270"} -->',
				}),
			],
			{ 2271: { state: "open" }, 2272: { state: "open" }, 2270: { state: "open" } },
		);
		const result = await listFeatureBuilds();
		expect(result).toHaveLength(3);
		// Should be sorted newest first: t2 > t3 > t1
		expect(result[0].pr).toBe(2272);
		expect(result[1].pr).toBe(2270);
		expect(result[2].pr).toBe(2271);
	});

	it("dedupes the PR open-state check (calls /pulls/<n> only once per PR)", async () => {
		const fetchMock = vi.fn(async (url: string) => {
			if (String(url).includes("/releases")) {
				return {
					ok: true,
					json: async () => [
						makeRelease({
							tag_name: "v0.2.0-pr2270.202607050000",
							published_at: new Date(Date.now() - 2 * DAY_MS).toISOString(),
						}),
						makeRelease({
							tag_name: "v0.2.0-pr2270.202607061200",
							published_at: new Date(Date.now() - DAY_MS).toISOString(),
						}),
					],
				};
			}
			return { ok: true, json: async () => ({ state: "open" }) };
		});
		vi.stubGlobal("fetch", fetchMock);
		await listFeatureBuilds();
		const pullsCalls = (fetchMock.mock.calls as [string][]).filter(([url]) => String(url).includes("/pulls/"));
		expect(pullsCalls).toHaveLength(1);
	});
});

// ---------------------------------------------------------------------------
// reconcileFeaturePin
// ---------------------------------------------------------------------------

describe("reconcileFeaturePin", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	const pinned = (pr: number): UpdateSettings => ({
		enabled: true,
		channel: "latest",
		nightlyAck: false,
		feature: { pr },
	});

	it("returns unchanged when there is no pin", async () => {
		const settings: UpdateSettings = { enabled: true, channel: "nightly", nightlyAck: true, feature: null };
		const r = await reconcileFeaturePin(settings);
		expect(r).toEqual({ settings, cleared: false });
	});

	it("keeps the pin when the PR still has a live build", async () => {
		stubFetch([makeRelease()], { 2270: { state: "open" } });
		const r = await reconcileFeaturePin(pinned(2270));
		expect(r.cleared).toBe(false);
		expect(r.settings.feature).toEqual({ pr: 2270 });
	});

	it("clears the pin (preserving the home channel) when the PR has no live build", async () => {
		stubFetch([]); // no live feature builds at all -> pin is retired
		const r = await reconcileFeaturePin(pinned(2270));
		expect(r.cleared).toBe(true);
		expect(r.settings.feature).toBeNull();
		expect(r.settings.channel).toBe("latest");
	});

	it("clears the pin when a different PR is live but the pinned one is not", async () => {
		stubFetch(
			[
				makeRelease({
					tag_name: "v0.2.0-pr999.202607061200",
					body: '<!-- ao-feature-build: {"pr":999,"base":"main","sha":"x","slug":"y"} -->',
				}),
			],
			{ 999: { state: "open" } },
		);
		const r = await reconcileFeaturePin(pinned(2270));
		expect(r.cleared).toBe(true);
		expect(r.settings.feature).toBeNull();
	});

	it("keeps the pin on a fetch error (never strands the user on a transient failure)", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				throw new Error("network error");
			}),
		);
		const r = await reconcileFeaturePin(pinned(2270));
		expect(r.cleared).toBe(false);
		expect(r.settings.feature).toEqual({ pr: 2270 });
	});
});
