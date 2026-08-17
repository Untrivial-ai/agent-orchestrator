import { describe, expect, it } from "vitest";

import { buildUtmUrl, LAUNCH_CAMPAIGN, LAUNCH_CHANNELS } from "./utm";

describe("buildUtmUrl", () => {
	it("appends the params in standard order", () => {
		expect(
			buildUtmUrl("https://aoagents.dev", {
				source: "product_hunt",
				medium: "referral",
				campaign: "launch_day",
			}),
		).toBe(
			"https://aoagents.dev/?utm_source=product_hunt&utm_medium=referral&utm_campaign=launch_day",
		);
	});

	it("omits blank optional params rather than writing them empty", () => {
		const url = buildUtmUrl("https://aoagents.dev", {
			source: "x",
			medium: "social",
			campaign: "launch_day",
			term: "  ",
			content: "",
		});
		expect(url).not.toContain("utm_term");
		expect(url).not.toContain("utm_content");
	});

	it("preserves a query the base already has", () => {
		const url = buildUtmUrl("https://aoagents.dev/?ref=abc", {
			source: "github",
			medium: "referral",
			campaign: "launch_day",
		});
		expect(url).toContain("ref=abc");
		expect(url).toContain("utm_source=github");
	});
});

describe("LAUNCH_CHANNELS", () => {
	it("uses one canonical campaign across every channel", () => {
		for (const channel of LAUNCH_CHANNELS) {
			expect(channel.link).toContain(`utm_campaign=${LAUNCH_CAMPAIGN}`);
			expect(channel.link).toContain(`utm_source=${channel.source}`);
		}
	});

	it("has unique sources", () => {
		const sources = LAUNCH_CHANNELS.map((c) => c.source);
		expect(new Set(sources).size).toBe(sources.length);
	});

	it("marks Instagram as a not-live placeholder", () => {
		const ig = LAUNCH_CHANNELS.find((c) => c.source === "instagram");
		expect(ig?.todo).toBe(true);
		expect(ig?.profileUrl).toContain("TODO");
	});

	it("includes Product Hunt as a real, tagged link to the site", () => {
		const ph = LAUNCH_CHANNELS.find((c) => c.source === "product_hunt")?.link;
		expect(ph).toContain("https://aoagents.dev");
		expect(ph).toContain("utm_source=product_hunt");
	});
});
