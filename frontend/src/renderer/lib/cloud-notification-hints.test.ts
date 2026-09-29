import { describe, expect, it } from "vitest";
import { publishCloudNotificationHint, subscribeCloudNotificationHints } from "./cloud-notification-hints";

describe("cloud notification hints", () => {
	it("publishes the relay hint and stops after unsubscribe", () => {
		const received: string[] = [];
		const unsubscribe = subscribeCloudNotificationHints((hint) => received.push(hint.eventId));
		publishCloudNotificationHint({ source: "cloud", eventId: "evt-1", type: "needs_input", occurredAt: "2026-09-21T00:00:00Z", payload: {} });
		unsubscribe();
		publishCloudNotificationHint({ source: "cloud", eventId: "evt-2", type: "needs_input", occurredAt: "2026-09-21T00:00:00Z", payload: {} });
		expect(received).toEqual(["evt-1"]);
	});
});
