import { describe, expect, it } from "vitest";
import { cloudNotificationsQueryKey } from "./cloud-notifications";

describe("cloudNotificationsQueryKey", () => {
	it("normalizes the base URL so a trailing slash yields the same key", () => {
		expect(cloudNotificationsQueryKey("https://cp.example/", "org-1", "all")).toEqual(
			cloudNotificationsQueryKey("https://cp.example", "org-1", "all"),
		);
	});

	it("strips repeated trailing slashes and keeps status distinct", () => {
		expect(cloudNotificationsQueryKey("https://cp.example///", "org-1", "unread")).toEqual([
			"cloud-notifications",
			"https://cp.example",
			"org-1",
			"unread",
		]);
	});
});
