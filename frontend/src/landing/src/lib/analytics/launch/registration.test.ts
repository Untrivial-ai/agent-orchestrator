import { describe, expect, it } from "vitest";

import { launchContext } from "./context";
import { launchSuperProperties } from "./registration";

describe("launchSuperProperties", () => {
	it("registers the full context when the visit carried a campaign", () => {
		const context = launchContext({
			utmSource: "product_hunt",
			utmCampaign: "launch_day",
			ua: "Macintosh",
		});
		expect(launchSuperProperties(context)).toEqual({
			register: {
				source: "product_hunt",
				campaign: "launch_day",
				user_type: "anonymous",
				device: "desktop",
			},
			unregister: [],
		});
	});

	it("unregisters a stale campaign when the visit carried none", () => {
		// Registered super-properties persist in the PostHog cookie; without the
		// explicit unregister, a previous visit's campaign rides along forever.
		const { register, unregister } = launchSuperProperties(
			launchContext({ ua: "Macintosh" }),
		);
		expect(register).not.toHaveProperty("campaign");
		expect(unregister).toEqual(["campaign"]);
	});
});
