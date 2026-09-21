import { describe, expect, it } from "vitest";
import { addCloudNotificationHint, applyCloudNotificationEvent, emptyCloudNotificationState } from "./cloud-notifications";

describe("cloud notification reconciliation", () => {
	it("replaces a fast hint when its durable confirmation arrives", () => {
		const pending = addCloudNotificationHint(emptyCloudNotificationState(), { source: "cloud", eventId: "evt-1", type: "needs_input", occurredAt: "2026-09-21T00:00:00Z", payload: {} });
		const next = applyCloudNotificationEvent(pending, { sequence: 1, orgId: "org", recipientUserId: "user", kind: "notification_created", createdAt: "2026-09-21T00:00:01Z", notification: { id: "n-1", source: "cloud", eventId: "evt-1", orgId: "org", type: "needs_input", title: "Input needed", body: "Choose", status: "unread", createdAt: "2026-09-21T00:00:01Z", updatedAt: "2026-09-21T00:00:01Z" } });
		expect(next.pending).toEqual([]);
		expect(next.durable.map((item) => item.id)).toEqual(["n-1"]);
	});
});
