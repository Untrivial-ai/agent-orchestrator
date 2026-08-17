import { describe, expect, it } from "vitest";

import {
	classifySource,
	deviceType,
	externalReferrer,
	launchContext,
	launchContextFromBrowser,
	type BrowserLaunchRead,
} from "./context";
import { LAUNCH_CHANNELS } from "./utm";

describe("classifySource", () => {
	it("prefers an explicit utm_source and normalizes its spellings", () => {
		for (const s of ["product_hunt", "producthunt", "product-hunt", "PH"]) {
			expect(classifySource(s, "https://google.com")).toBe("product_hunt");
		}
		expect(classifySource("twitter", undefined)).toBe("x");
		expect(classifySource("yt", undefined)).toBe("youtube");
		expect(classifySource("instagram", undefined)).toBe("instagram");
	});

	it("falls back to the referrer hostname when there is no utm_source", () => {
		expect(classifySource(undefined, "https://www.producthunt.com/posts/x")).toBe("product_hunt");
		expect(classifySource(undefined, "https://t.co/abc")).toBe("x");
		expect(classifySource(undefined, "https://lnkd.in/abc")).toBe("linkedin");
		expect(classifySource(undefined, "https://m.youtube.com/watch?v=x")).toBe("youtube");
	});

	it("matches referrer hostnames on domain boundaries, never substrings", () => {
		// Regression: naive includes() matched these — netflix/dropbox contain
		// "x.com", graph.company contains "ph.co".
		expect(classifySource(undefined, "https://netflix.com/title/1")).toBe("other");
		expect(classifySource(undefined, "https://dropbox.com/home")).toBe("other");
		expect(classifySource(undefined, "https://graph.company/")).toBe("other");
		// ...while real subdomains still match.
		expect(classifySource(undefined, "https://news.x.com/aoagents")).toBe("x");
		expect(classifySource(undefined, "https://api.producthunt.com/")).toBe("product_hunt");
	});

	it("treats an empty referrer with no utm as a direct visit", () => {
		expect(classifySource(undefined, "")).toBe("direct");
		expect(classifySource("", "")).toBe("direct");
	});

	it("returns other for an unknown source", () => {
		expect(classifySource("newsletter", undefined)).toBe("other");
		expect(classifySource(undefined, "https://example.com")).toBe("other");
	});

	it("keeps the referrer rules in sync with the channel registry", () => {
		// Drift guard: every channel in LAUNCH_CHANNELS must be classifiable
		// both by its own utm_source and by its profile hostname, so adding a
		// channel there cannot silently produce an unattributable source here.
		for (const channel of LAUNCH_CHANNELS) {
			expect(classifySource(channel.source, undefined)).toBe(channel.source);
			expect(classifySource(undefined, channel.profileUrl)).toBe(channel.source);
		}
	});
});

describe("deviceType", () => {
	it("classifies mobile, tablet, and desktop", () => {
		expect(deviceType("iPhone; CPU iPhone OS 17_0 like Mac OS X Mobile")).toBe("mobile");
		expect(deviceType("iPad; CPU OS 17_0 like Mac OS X")).toBe("tablet");
		expect(deviceType("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15)")).toBe("desktop");
	});

	it("treats Android without 'mobile' as a tablet", () => {
		expect(deviceType("Mozilla/5.0 (Linux; Android 14; Tab)")).toBe("tablet");
		expect(deviceType("Mozilla/5.0 (Linux; Android 14; Pixel Mobile)")).toBe("mobile");
	});

	it("counts an iPadOS 13+ desktop-style UA as a tablet when touch is reported", () => {
		const iPadOS =
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15";
		expect(deviceType(iPadOS, 5)).toBe("tablet");
		expect(deviceType(iPadOS, 0)).toBe("desktop");
	});
});

describe("launchContext", () => {
	it("assembles the normalized super-properties", () => {
		expect(
			launchContext({
				utmSource: "product_hunt",
				utmCampaign: "launch_day",
				referrer: "https://www.producthunt.com/",
				ua: "iPhone Mobile",
			}),
		).toEqual({
			source: "product_hunt",
			campaign: "launch_day",
			user_type: "anonymous",
			device: "mobile",
		});
	});

	it("passes the visit's own utm_campaign through instead of assuming the launch one", () => {
		expect(
			launchContext({ utmSource: "x", utmCampaign: "spring_share", ua: "Mac" }).campaign,
		).toBe("spring_share");
	});

	it("omits campaign entirely when the visit carried none", () => {
		// Direct and untagged traffic must not be relabeled launch_day: the
		// property is unregistered, not defaulted.
		expect(launchContext({ referrer: "", ua: "Mac" })).toEqual({
			source: "direct",
			user_type: "anonymous",
			device: "desktop",
		});
		expect("campaign" in launchContext({ referrer: "", ua: "Mac" })).toBe(false);
		expect(launchContext({ utmCampaign: "   ", ua: "Mac" }).campaign).toBeUndefined();
	});
});

describe("externalReferrer", () => {
	it("drops same-site referrers, including subdomains, and keeps external ones", () => {
		expect(externalReferrer("https://aoagents.dev/", "aoagents.dev")).toBe("");
		expect(externalReferrer("https://www.aoagents.dev/download", "aoagents.dev")).toBe("");
		expect(externalReferrer("https://www.producthunt.com/", "aoagents.dev")).toBe(
			"https://www.producthunt.com/",
		);
		expect(externalReferrer("", "aoagents.dev")).toBe("");
	});
});

describe("launchContextFromBrowser", () => {
	const read = (over: Partial<BrowserLaunchRead>): BrowserLaunchRead => ({
		campaign: () => ({}),
		referrer: "",
		hostname: "aoagents.dev",
		userAgent: "Macintosh",
		touchPoints: 0,
		...over,
	});

	it("keeps a tagged arrival attributed across an untagged reload", () => {
		// Regression: the visitor arrived via the tagged Product Hunt link,
		// then client-navigated to /download and reloaded — the URL no longer
		// carries utm_*, but campaign.ts persists them for the tab session,
		// and document.referrer is now our own origin.
		const context = launchContextFromBrowser(
			read({
				campaign: () => ({
					utm_source: "product_hunt",
					utm_campaign: "launch_day",
				}),
				referrer: "https://aoagents.dev/download",
			}),
		);
		expect(context.source).toBe("product_hunt");
		expect(context.campaign).toBe("launch_day");
	});

	it("classifies an untagged same-site reload as direct with no campaign", () => {
		const context = launchContextFromBrowser(
			read({ referrer: "https://aoagents.dev/" }),
		);
		expect(context.source).toBe("direct");
		expect(context.campaign).toBeUndefined();
	});

	it("falls back to the external referrer when no persisted utm exists", () => {
		const context = launchContextFromBrowser(
			read({ referrer: "https://t.co/abc" }),
		);
		expect(context.source).toBe("x");
	});

	it("passes touch detection through to the device class", () => {
		const iPadOS =
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15";
		expect(
			launchContextFromBrowser(
				read({ userAgent: iPadOS, touchPoints: 5 }),
			).device,
		).toBe("tablet");
	});

	it("remembers an untagged external arrival's source across a same-site reload", () => {
		// Regression: an untagged Product Hunt link (no utm_*) classified
		// correctly on arrival, but after the reload — referrer now our own
		// origin, campaign.ts holding nothing — the source became "direct" and
		// ph_referral_visit was lost for a visitor who consented late.
		const session = new Map<string, string>();
		const sessionStorage = {
			getItem: (k: string) => session.get(k) ?? null,
			setItem: (k: string, v: string) => void session.set(k, v),
		};
		const arrival = launchContextFromBrowser(
			read({ referrer: "https://www.producthunt.com/posts/ao" }),
			sessionStorage,
		);
		expect(arrival.source).toBe("product_hunt");
		expect(session.get("ao.launch.source")).toBe("product_hunt");

		const reload = launchContextFromBrowser(
			read({ referrer: "https://aoagents.dev/" }),
			sessionStorage,
		);
		expect(reload.source).toBe("product_hunt");
	});

	it("does not remember direct or unrecognized sources", () => {
		const session = new Map<string, string>();
		const sessionStorage = {
			getItem: (k: string) => session.get(k) ?? null,
			setItem: (k: string, v: string) => void session.set(k, v),
		};
		launchContextFromBrowser(read({ referrer: "https://example.com/" }), sessionStorage);
		launchContextFromBrowser(read({ referrer: "" }), sessionStorage);
		expect(session.has("ao.launch.source")).toBe(false);
		// A later same-site load with nothing remembered is direct.
		expect(
			launchContextFromBrowser(read({ referrer: "https://aoagents.dev/" }), sessionStorage).source,
		).toBe("direct");
	});

	it("prefers a tagged arrival over any remembered source", () => {
		const session = new Map<string, string>([["ao.launch.source", "x"]]);
		const sessionStorage = {
			getItem: (k: string) => session.get(k) ?? null,
			setItem: (k: string, v: string) => void session.set(k, v),
		};
		const context = launchContextFromBrowser(
			read({
				campaign: () => ({ utm_source: "product_hunt", utm_campaign: "launch_day" }),
			}),
			sessionStorage,
		);
		expect(context.source).toBe("product_hunt");
	});
});
