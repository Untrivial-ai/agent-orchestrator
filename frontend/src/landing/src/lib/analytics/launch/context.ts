/**
 * Launch attribution context: the normalized properties attached to every
 * event during the launch so any funnel step can be broken down by where the
 * visitor came from.
 *
 * `campaign.ts` already captures the raw `utm_*` params. This layer adds a
 * single normalized `source` (so `product_hunt`, a `producthunt.com` referrer,
 * and `utm_source=product-hunt` all collapse to one value), the visit's own
 * `utm_campaign` when present, and a coarse device class. These are registered
 * as PostHog super-properties from the init `loaded` callback (see
 * `instrumentation-client.ts`), so they ride on every event including the very
 * first pageview.
 *
 * The channel set is derived from `LAUNCH_CHANNELS` so the registry stays the
 * single source of truth: a channel added there is classifiable here, and
 * `context.test.ts` fails if the two drift apart.
 *
 * The functions are pure (they take the URL params, referrer, and user-agent as
 * arguments) so the classification rules are testable without a browser.
 */

import { campaignProperties, type CampaignParams } from "../campaign";
import { LAUNCH_CHANNELS, type LaunchSourceName } from "./utm";

export type LaunchSource = LaunchSourceName | "direct" | "other";

export type DeviceType = "mobile" | "tablet" | "desktop";

export type LaunchContext = {
	source: LaunchSource;
	/** The visit's own utm_campaign, when it carried one. Absent otherwise —
	 * never defaulted to LAUNCH_CAMPAIGN, so non-launch traffic (direct, Reddit
	 * ads, ...) is not silently relabeled. */
	campaign?: string;
	/** Always `anonymous` on the marketing site (no auth here). The app sets
	 * `signed_up` / `activated` for its own events. */
	user_type: "anonymous";
	device: DeviceType;
};

/** utm_source spellings that normalize onto a channel's canonical source. */
const UTM_ALIASES: Record<string, LaunchSourceName> = {
	producthunt: "product_hunt",
	"product-hunt": "product_hunt",
	ph: "product_hunt",
	twitter: "x",
	yt: "youtube",
};

/** Referrer hostnames that attribute onto a channel's canonical source. */
const REFERRER_ALIASES: Record<string, LaunchSourceName> = {
	"producthunt.com": "product_hunt",
	"ph.co": "product_hunt",
	"lnkd.in": "linkedin",
	"youtu.be": "youtube",
	"discord.gg": "discord",
	"t.co": "x",
	"twitter.com": "x",
	"reddit.com": "reddit",
};

/** utm_source value -> canonical source, one row per channel plus aliases. */
const UTM_SOURCE_MAP: Record<string, LaunchSourceName> = {
	...Object.fromEntries(
		LAUNCH_CHANNELS.map((channel) => [channel.source, channel.source]),
	),
	...UTM_ALIASES,
};

/** Referrer hostname -> canonical source. Needles never suffix-contain one
 * another, so order is irrelevant; matching is on domain boundaries. */
function referrerHostname(url: string): string {
	try {
		// Strip the leading "www." so the needle matches the apex and every
		// subdomain (www., m., news., ...) of it.
		return new URL(url).hostname.replace(/^www\./, "");
	} catch {
		return "";
	}
}

const REFERRER_RULES: Array<[string, LaunchSourceName]> = [
	...LAUNCH_CHANNELS.map((channel) => {
		return [referrerHostname(channel.profileUrl), channel.source] as const;
	}),
	...Object.entries(REFERRER_ALIASES),
].filter(([needle]) => needle !== "") as Array<[string, LaunchSourceName]>;

/** A campaign longer than this is treated as junk and dropped. */
const MAX_CAMPAIGN_LENGTH = 200;

/** True when `host` is `needle` or a subdomain of it — never a substring. */
function hostMatches(host: string, needle: string): boolean {
	return host === needle || host.endsWith("." + needle);
}

/**
 * Classifies the visit's source. A UTM source wins when present (it is the
 * intentional tag on our own links); otherwise the referrer hostname is
 * matched on domain boundaries (so `netflix.com` is not `x.com`); an empty
 * referrer is a direct visit; anything else is `other`.
 */
export function classifySource(
	utmSource: string | undefined,
	referrer: string | undefined,
): LaunchSource {
	const utm = utmSource?.trim().toLowerCase();
	if (utm && UTM_SOURCE_MAP[utm]) return UTM_SOURCE_MAP[utm];

	const ref = referrer?.trim().toLowerCase() ?? "";
	if (ref === "") return utm ? "other" : "direct";
	let host = ref;
	try {
		host = new URL(ref).hostname;
	} catch {
		// Not a full URL; match against the raw string below.
	}
	for (const [needle, source] of REFERRER_RULES) {
		if (hostMatches(host, needle)) return source;
	}
	return "other";
}

/**
 * Coarse device class from a user-agent string. `touchPoints` (from
 * `navigator.maxTouchPoints`) distinguishes iPadOS 13+, whose Safari presents
 * a desktop "Macintosh" user-agent, from an actual Mac.
 */
export function deviceType(ua: string | undefined, touchPoints = 0): DeviceType {
	const s = (ua ?? "").toLowerCase();
	if (/ipad|tablet|kindle|playbook|silk/.test(s)) return "tablet";
	if (/macintosh/.test(s) && touchPoints > 1) return "tablet";
	if (/mobi|iphone|ipod|android.*mobile|windows phone/.test(s)) return "mobile";
	if (/android/.test(s)) return "tablet"; // Android without "mobile" is a tablet.
	return "desktop";
}

/** The visit's utm_campaign, normalized; undefined when absent or junk. */
function normalizeCampaign(utmCampaign: string | undefined): string | undefined {
	const value = utmCampaign?.trim();
	if (!value || value.length > MAX_CAMPAIGN_LENGTH) return undefined;
	return value;
}

/** The normalized launch context to register as PostHog super-properties. */
export function launchContext(input: {
	utmSource?: string;
	utmCampaign?: string;
	referrer?: string;
	ua?: string;
	touchPoints?: number;
}): LaunchContext {
	const context: LaunchContext = {
		source: classifySource(input.utmSource, input.referrer),
		user_type: "anonymous",
		device: deviceType(input.ua, input.touchPoints),
	};
	const campaign = normalizeCampaign(input.utmCampaign);
	if (campaign) context.campaign = campaign;
	return context;
}

/**
 * Referrer with same-site hosts removed. Navigating our own pages sets
 * `document.referrer` to our own origin, which must not reclassify a visit
 * (a reload of `/download` is not a referrer).
 */
export function externalReferrer(
	referrer: string,
	currentHostname: string,
): string {
	if (!referrer) return "";
	try {
		const host = new URL(referrer).hostname;
		return hostMatches(host, currentHostname) ? "" : referrer;
	} catch {
		return referrer;
	}
}

/** The browser inputs `launchContextFromBrowser` needs; injectable for tests. */
export type BrowserLaunchRead = {
	/** Persisted campaign params (campaign.ts captures once per tab session). */
	campaign: () => CampaignParams;
	referrer: string;
	hostname: string;
	userAgent: string;
	touchPoints: number;
};

/**
 * The launch context for the current browser visit.
 *
 * UTMs come from campaign.ts — capture-once and persisted for the tab session —
 * rather than the current URL: a visitor who arrived tagged and then navigated
 * or reloaded an untagged page keeps their attribution. The referrer is only
 * consulted when there is no persisted `utm_source`, and same-site referrers
 * are ignored.
 */
/**
 * sessionStorage key holding the referrer-inferred source of this tab session.
 *
 * Tagged arrivals keep their attribution through campaign.ts (it persists the
 * utm_* params for the tab). An untagged external arrival — a visitor whose
 * channel link carried no utm_* — can only be classified from document.referrer,
 * which is our own origin after the first reload. The inferred source is
 * therefore remembered here, with the same per-tab-session lifetime campaign.ts
 * uses: a new tab tomorrow from elsewhere starts clean.
 */
const SESSION_SOURCE_KEY = "ao.launch.source";

type SessionSourceStorage = Pick<Storage, "getItem" | "setItem">;

function sessionSourceStorage(): SessionSourceStorage | undefined {
	if (typeof window === "undefined") return undefined;
	try {
		return window.sessionStorage;
	} catch {
		return undefined;
	}
}

function rememberedSource(
	storage?: SessionSourceStorage,
): LaunchSource | undefined {
	try {
		const value = (storage ?? sessionSourceStorage())?.getItem(
			SESSION_SOURCE_KEY,
		);
		return value ? (value as LaunchSource) : undefined;
	} catch {
		return undefined;
	}
}

function rememberSource(
	source: LaunchSource,
	storage?: SessionSourceStorage,
): void {
	try {
		(storage ?? sessionSourceStorage())?.setItem(SESSION_SOURCE_KEY, source);
	} catch {
		// Storage blocked: the source simply is not persisted for the tab.
	}
}

export function launchContextFromBrowser(
	read?: BrowserLaunchRead,
	/** Where the inferred source is remembered; defaults to sessionStorage. */
	sessionSourceStore?: SessionSourceStorage,
): LaunchContext {
	if (typeof window === "undefined" && !read) return launchContext({});
	const r = read ?? {
		campaign: campaignProperties,
		referrer: document.referrer,
		hostname: window.location.hostname,
		userAgent: navigator.userAgent,
		touchPoints: navigator.maxTouchPoints,
	};
	const campaign = r.campaign();
	const referrer = externalReferrer(r.referrer, r.hostname);
	const base = {
		utmCampaign: campaign.utm_campaign,
		ua: r.userAgent,
		touchPoints: r.touchPoints,
	};
	// Tagged arrival: campaign.ts persists the utm_* for the tab, so this is
	// stable across untagged reloads by construction.
	if (campaign.utm_source) {
		return launchContext({ ...base, utmSource: campaign.utm_source, referrer });
	}
	// Untagged external arrival: classify from the referrer, and remember the
	// inferred source for the rest of the tab session.
	if (referrer) {
		const context = launchContext({ ...base, referrer });
		if (context.source !== "direct" && context.source !== "other") {
			rememberSource(context.source, sessionSourceStore);
		}
		return context;
	}
	// Same-site reload (or no referrer): fall back to what this tab session
	// arrived as, if anything was remembered.
	const remembered = rememberedSource(sessionSourceStore);
	const context = launchContext({ ...base, referrer: "" });
	return remembered ? { ...context, source: remembered } : context;
}
