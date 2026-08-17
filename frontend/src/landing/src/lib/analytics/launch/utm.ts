/**
 * Canonical UTM link registry for the Product Hunt launch and the other
 * channels we point at the site.
 *
 * A UTM link is an *inbound* link: it lives on an external channel (the Product
 * Hunt page, an X bio, a LinkedIn post) and points at the marketing site with
 * `utm_*` query params so PostHog attributes the resulting pageview to that
 * source. This module is the single source of truth for those links, so the
 * team pastes consistent, tagged URLs everywhere and the breakdown in PostHog
 * stays clean (one `utm_source` per channel, one campaign for the launch).
 *
 * PostHog already reads `utm_source/medium/campaign/term/content` off the
 * landing URL automatically, and `campaign.ts` persists them across the tab, so
 * no client code is needed to *record* these — you only need to use the links.
 */

import { COMPANY } from "@ao/shared/constants";

/** The single campaign name for the launch. Keep it stable across channels. */
export const LAUNCH_CAMPAIGN = "launch_day";

/** The live Product Hunt page (outbound target for upvote / comment CTAs). */
export const PRODUCT_HUNT_URL =
	"https://www.producthunt.com/products/agent-orchestrator?launch=agent-orchestrator";

export type UtmParams = {
	source: string;
	medium: string;
	campaign: string;
	content?: string;
	term?: string;
};

/**
 * Appends UTM params to a base URL without clobbering any query it already has.
 * Keys are written in the standard order so generated links are stable and
 * diff-friendly. Blank optional values are omitted rather than written empty.
 */
export function buildUtmUrl(base: string, params: UtmParams): string {
	const url = new URL(base);
	const ordered: Array<[string, string | undefined]> = [
		["utm_source", params.source],
		["utm_medium", params.medium],
		["utm_campaign", params.campaign],
		["utm_term", params.term],
		["utm_content", params.content],
	];
	for (const [key, value] of ordered) {
		if (value && value.trim() !== "") url.searchParams.set(key, value.trim());
	}
	return url.toString();
}

/** Canonical launch sources. One per registry channel, plus the sources the
 * site already buys/links from but that have no inbound-link row (reddit). */
export type LaunchSourceName =
	| "product_hunt"
	| "x"
	| "linkedin"
	| "youtube"
	| "discord"
	| "github"
	| "instagram"
	| "reddit";

export type LaunchChannel = {
	/** `utm_source` value. Canonical and lowercase; never changes per channel. */
	source: LaunchSourceName;
	/** Human label for docs/dashboards. */
	label: string;
	/** `utm_medium` value. */
	medium: string;
	/** Where this channel's profile/page lives (where you place the link). */
	profileUrl: string;
	/** The tagged inbound link to paste on that channel. */
	link: string;
	/** True when the account/page is not live yet: `profileUrl` is a placeholder. */
	todo?: boolean;
};

function inbound(source: LaunchSourceName, medium: string): string {
	return buildUtmUrl(COMPANY.MARKETING_URL, {
		source,
		medium,
		campaign: LAUNCH_CAMPAIGN,
	});
}

/**
 * One row per channel. `link` is the tagged URL you paste on that channel;
 * `profileUrl` is where it goes. Product Hunt is the launch's primary source.
 * Instagram has no account yet, so it is a clearly-marked placeholder to fill
 * in once the handle exists (kept in the registry so the structure is ready).
 */
export const LAUNCH_CHANNELS: LaunchChannel[] = [
	{
		source: "product_hunt",
		label: "Product Hunt",
		medium: "referral",
		profileUrl: PRODUCT_HUNT_URL,
		link: inbound("product_hunt", "referral"),
	},
	{
		source: "x",
		label: "X / Twitter",
		medium: "social",
		profileUrl: COMPANY.X_URL,
		link: inbound("x", "social"),
	},
	{
		source: "linkedin",
		label: "LinkedIn",
		medium: "social",
		profileUrl: COMPANY.LINKEDIN_URL,
		link: inbound("linkedin", "social"),
	},
	{
		source: "youtube",
		label: "YouTube",
		medium: "social",
		profileUrl: COMPANY.YOUTUBE_URL,
		link: inbound("youtube", "social"),
	},
	{
		source: "discord",
		label: "Discord",
		medium: "community",
		profileUrl: COMPANY.DISCORD_URL,
		link: inbound("discord", "community"),
	},
	{
		source: "github",
		label: "GitHub",
		medium: "referral",
		profileUrl: COMPANY.GITHUB_URL,
		link: inbound("github", "referral"),
	},
	{
		// TODO: no Instagram account exists yet. Replace the placeholder profile
		// URL with the real handle when it is live; the tagged link is already
		// correct and needs no change.
		source: "instagram",
		label: "Instagram",
		medium: "social",
		profileUrl: "https://instagram.com/TODO",
		link: inbound("instagram", "social"),
		todo: true,
	},
];

