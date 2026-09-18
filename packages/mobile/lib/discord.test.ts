import { describe, expect, it } from "vitest";
import { discordFeatureRequestURL } from "./discord";

describe("discordFeatureRequestURL", () => {
	it("opens the official AO Discord invite until a feature-request channel is configured", () => {
		expect(discordFeatureRequestURL()).toBe("https://discord.com/invite/UZv7JjxbwG");
	});

	it("can target the feature-request channel directly when its Discord id is configured", () => {
		expect(discordFeatureRequestURL("12345")).toBe("https://discord.com/channels/1476302178913357958/12345");
	});
});
