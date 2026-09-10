// Store-update policy: whether to tell the user a newer native binary is on the
// store, and how hard to push. No React Native or Expo imports so vitest can run
// it; callers pass the store's answer in, the way `updates.ts` pairs with
// `UpdatesManager.tsx`.
//
// `updates.ts` is the JS half of updating and cannot cross the native boundary:
// a fingerprint runtime means a native change leaves old binaries with no
// updates and no signal. This is that signal.

/** What a store told us. Android fields come from Play, iOS from the iTunes lookup. */
export type StoreCheck = {
	updateAvailable: boolean;
	/** Play only: an update this app started is downloading or waiting to install. */
	updateInProgress?: boolean;
	/**
	 * iOS: the App Store's version string. Android: the available `versionCode`.
	 * The two are not the same kind of thing, so this is only ever compared to
	 * itself (to notice the store moved) and never parsed as a version here.
	 */
	storeVersion?: string;
	/** iOS: the app's own App Store page, straight from the lookup. */
	storeUrl?: string;
	/** Play only: whether the immediate (blocking) flow can start right now. */
	immediateAllowed?: boolean;
	/**
	 * Play only: days since THIS device's Play Store learned of the update — not
	 * days since release, so a staged rollout makes it per-device. Null when Play
	 * does not know.
	 */
	playStalenessDays?: number | null;
	/** Play only: "IMMEDIATE" once a release was published with inAppUpdatePriority >= 4. */
	serverUpdateType?: "FLEXIBLE" | "IMMEDIATE";
};

export type StoreTier = "none" | "recommended" | "required";

/** How often a dismissed nudge may come back, and how many times in total. */
export const PROMPT_INTERVAL_MS = 24 * 60 * 60 * 1000;
export const MAX_DISMISSALS = 3;

/**
 * A version we require (`min`) or would like people on (`latest`), carried in the
 * JS bundle so it can be moved with an `eas update` rather than a store release.
 */
export type Floor = { min?: string; latest?: string };

/** How long an update must have been on Play before the first nudge. */
export const DAYS_FOR_FLEXIBLE_UPDATE = 3;

/** Persisted nudge state. `version` is whichever version the count belongs to. */
export type Snooze = { version: string; dismissals: number; lastPromptAt: number };

/**
 * Compares dotted numeric versions: <0, 0, >0. Missing segments count as zero,
 * so "1.2" equals "1.2.0". A non-numeric segment counts as zero rather than NaN
 * so a malformed store answer can never read as "newer" and nag every launch.
 */
export function compareVersions(a: string, b: string): number {
	const left = a.split(".");
	const right = b.split(".");
	for (let i = 0; i < Math.max(left.length, right.length); i++) {
		const na = Number.parseInt(left[i] ?? "0", 10) || 0;
		const nb = Number.parseInt(right[i] ?? "0", 10) || 0;
		if (na !== nb) return na - nb;
	}
	return 0;
}

/**
 * A version string we are willing to compare, or null.
 *
 * `compareVersions` coerces non-numeric segments to 0, so it can never report
 * that a value was unparseable — it just answers. That is the wrong failure for
 * an operator-supplied floor: "v1.3.0" would silently become 0.3.0 and do
 * nothing, while a pasted versionCode like "10300" would sit above every version
 * that will ever exist. Anything that is not a plain dotted number is absent.
 */
function usableVersion(v: string | null | undefined): string | null {
	const trimmed = v?.trim();
	return trimmed && /^\d{1,4}(\.\d{1,4}){0,2}$/.test(trimmed) ? trimmed : null;
}

/**
 * The version a floor is pointing at, for copy and for keying the snooze. Same
 * `usableVersion` filter as `floorSignal`, so a value that cannot move the tier
 * cannot name the sheet or key the snooze either.
 */
export function floorTarget(floor: Floor): string | undefined {
	return usableVersion(floor.latest) ?? usableVersion(floor.min) ?? undefined;
}

/** What the floor alone would ask for, before the store gets a say. */
export function floorSignal(installed: string | null | undefined, floor: Floor): StoreTier {
	const have = usableVersion(installed);
	if (!have) return "none";
	const min = usableVersion(floor.min);
	if (min && compareVersions(have, min) < 0) return "required";
	const latest = usableVersion(floor.latest);
	if (latest && compareVersions(have, latest) < 0) return "recommended";
	return "none";
}

/**
 * The iTunes Search API lookup for a bundle id. Public, unauthenticated, no key.
 *
 * `region` is the two-letter storefront. Without it the API answers for the US
 * store only, so a user in a country the app has not launched in — or one where
 * the release rolls out later — reads as "no update" while their own store has
 * one. Unknown or malformed regions are dropped rather than guessed.
 */
export function lookupUrl(bundleId: string, region?: string | null): string {
	const url = `https://itunes.apple.com/lookup?bundleId=${encodeURIComponent(bundleId)}`;
	const country = region?.trim().toLowerCase();
	return country && /^[a-z]{2}$/.test(country) ? `${url}&country=${country}` : url;
}

/**
 * The region subtag of a BCP 47 locale, or null when it has none: the first
 * 2-alpha subtag after the language. Script subtags are 4 alpha and variants at
 * least 4 characters, so before the extensions nothing else is 2 letters — and
 * the scan stops at the first singleton, where `-u-ca-gregory` would otherwise
 * offer its calendar key "ca" up as a country.
 */
export function localeRegion(locale: string): string | null {
	for (const part of locale.split("-").slice(1)) {
		if (part.length === 1) break;
		if (/^[A-Za-z]{2}$/.test(part)) return part;
	}
	return null;
}

/**
 * Reads an iTunes lookup body. `resultCount: 0` is what the API returns for an
 * app that is not on the App Store, so it has to read as "no update" rather than
 * an error — as does a body we cannot parse. The check is advisory; failing open
 * is the only safe way to be wrong.
 */
export function parseLookup(body: unknown, installed: string | null | undefined): StoreCheck {
	if (!installed) return { updateAvailable: false };
	const results = (body as { results?: unknown })?.results;
	const first = Array.isArray(results) ? (results[0] as Record<string, unknown> | undefined) : undefined;
	const version = typeof first?.version === "string" ? first.version : null;
	if (!version) return { updateAvailable: false };
	// trackViewUrl is the app's own store page; trackId reconstructs it if the
	// field is ever missing. Either way the App Store id never has to be
	// configured in the app — the lookup carries it.
	const storeUrl =
		typeof first?.trackViewUrl === "string"
			? first.trackViewUrl
			: typeof first?.trackId === "number"
				? `https://apps.apple.com/app/id${first.trackId}`
				: undefined;
	return { updateAvailable: compareVersions(version, installed) > 0, storeVersion: version, storeUrl };
}

/**
 * How hard to push, from the store's answer and the floor together.
 *
 * **The interlock: `required` needs the store to independently confirm that an
 * update exists.** The floor can raise a confirmed update from dismissible to
 * blocking; it can never invent one. That is what makes a mistyped floor
 * harmless — the worst it can do is force an update already being offered — and
 * it is why iOS is nudge-only while unlisted and starts blocking by itself once
 * listed, with no platform special-case here.
 *
 * This is asserted directly in the tests rather than left to the order of the
 * statements below, because it is the property that stops the floor becoming a
 * unilateral kill switch.
 *
 * `recommended` deliberately does NOT need confirmation: the floor is the only
 * way to surface anything on iOS before the App Store listing exists.
 */
export function tierOf(check: StoreCheck | null, platform: string, floor: StoreTier = "none"): StoreTier {
	const confirmed = check?.updateAvailable === true;
	// Play's own policy channel: inAppUpdatePriority >= 4 on the release.
	if (confirmed && platform === "android" && check.serverUpdateType === "IMMEDIATE" && check.immediateAllowed) return "required";
	if (confirmed && floor === "required") return "required";
	if (confirmed || floor !== "none") return "recommended";
	return "none";
}

/**
 * The sheet's subtitle. Only claims the store has a build when the store said so
 * — a floor-driven nudge can fire before the store finishes processing a
 * release, and would otherwise point at a version nobody can install yet.
 */
export function describePrompt(input: { version?: string; storeConfirmed: boolean; storeName: string }): string {
	const where = input.storeConfirmed ? `on the ${input.storeName}` : "available";
	return input.version ? `Version ${input.version} is ${where}.` : `A newer version is ${where}.`;
}

/**
 * Whether to interrupt the user now. A critical release ignores the snooze —
 * that is the whole point of the tier. Everything else is rate limited, and a
 * different version on the store is a different ask, so the count starts over.
 */
export function shouldPrompt({
	tier,
	version,
	snooze,
	now,
	stalenessDays,
}: {
	tier: StoreTier;
	/** What this ask is keyed on — the floor's version when one is set, else the store's. */
	version: string | undefined;
	snooze: Snooze | null;
	now: number;
	/** Play's `clientVersionStalenessDays`, when it knows. */
	stalenessDays?: number | null;
}): boolean {
	if (tier === "required") return true;
	if (tier !== "recommended") return false;
	// Don't nag on day zero of a release: Play may be minutes from installing it
	// by itself. Only the first ask waits — once we have prompted, the snooze
	// below governs. A null staleness means Play does not know, so never suppress.
	if (!snooze && typeof stalenessDays === "number" && stalenessDays < DAYS_FOR_FLEXIBLE_UPDATE) return false;
	if (!snooze || snooze.version !== (version ?? "")) return true;
	if (snooze.dismissals >= MAX_DISMISSALS) return false;
	return now - snooze.lastPromptAt >= PROMPT_INTERVAL_MS;
}

/** The snooze record after a nudge for `target` went nowhere — dismissed, or "Update" opened nothing. */
export function nextSnooze(prev: Snooze | null, target: string | undefined, now: number): Snooze {
	const version = target ?? "";
	const carried = prev && prev.version === version ? prev.dismissals : 0;
	return { version, dismissals: carried + 1, lastPromptAt: now };
}

/**
 * Play Store links. `market://` opens the Play app directly; the https form is
 * the fallback for a device without it (and the one a browser can handle).
 */
export function playStoreUrls(packageName: string): { app: string; web: string } {
	return {
		app: `market://details?id=${packageName}`,
		web: `https://play.google.com/store/apps/details?id=${packageName}`,
	};
}

export type StoreRowResult = { kind: "error" } | { kind: "up-to-date" } | { kind: "available" };

export type StoreRowInput = {
	/** False in dev builds, where there is no store install to compare against. */
	enabled: boolean;
	checking: boolean;
	last: StoreRowResult | null;
};

export type StoreRow = {
	value: string;
	tone: "default" | "good" | "bad";
	busy: boolean;
	action: "check" | "open" | null;
};

/**
 * One manual check's conclusion. Takes the tier rather than the raw check so
 * the floor weighs in exactly as it does at launch — otherwise a floor-driven
 * nudge coexists with a green "Up to date" in Settings, each calling the other
 * a liar. An unreachable store reads as an error only when the floor is silent
 * too: the floor knowing of an update is an answer, not a failed check.
 */
export function storeRowResult(check: StoreCheck | null, tier: StoreTier): StoreRowResult {
	if (tier !== "none") return { kind: "available" };
	if (check === null) return { kind: "error" };
	return { kind: "up-to-date" };
}

/**
 * What the Settings store row shows and does. Worded to match the OTA row right
 * above it (`describeUpdateRow`): two adjacent rows reporting the same kind of
 * state should say it the same way.
 */
export function describeStoreRow(input: StoreRowInput): StoreRow {
	if (!input.enabled) return { value: "Off in development builds", tone: "default", busy: false, action: null };
	if (input.checking) return { value: "Checking…", tone: "default", busy: true, action: null };
	if (input.last?.kind === "available") return { value: "Update available", tone: "good", busy: false, action: "open" };
	if (input.last?.kind === "error") return { value: "Couldn't check", tone: "bad", busy: false, action: "check" };
	if (input.last?.kind === "up-to-date") return { value: "Up to date", tone: "good", busy: false, action: "check" };
	return { value: "Check now", tone: "default", busy: false, action: "check" };
}
