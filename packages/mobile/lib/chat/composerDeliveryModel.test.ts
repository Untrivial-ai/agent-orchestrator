import { describe, expect, it } from "vitest";

import { composerDeliveryPresentation, composerDeliveryRoute, composerPrimaryAction } from "./composerDeliveryModel";

describe("mobile chat delivery", () => {
	it("queues ordinary sends while a turn is active", () => {
		expect(composerDeliveryRoute("send", true)).toBe("queue");
		expect(composerDeliveryPresentation({ active: true, canSteer: true, hasDraft: true, hasAttachments: false, hasQueued: false })).toEqual({
			placeholder: "Send after this…",
			showQueueNote: true,
			showSteerAction: true,
		});
	});

	it("steers only an explicit eligible text-only draft", () => {
		expect(composerDeliveryRoute("steer", true)).toBe("steer");
		expect(composerDeliveryRoute("steer", false)).toBe("queue");
		expect(composerDeliveryPresentation({ active: true, canSteer: true, hasDraft: true, hasAttachments: true, hasQueued: false }).showSteerAction).toBe(false);
		expect(composerDeliveryPresentation({ active: true, canSteer: true, hasDraft: true, hasAttachments: false, hasQueued: true }).showSteerAction).toBe(false);
	});

	it("keeps the idle composer free of queue controls", () => {
		expect(composerDeliveryPresentation({ active: false, canSteer: true, hasDraft: true, hasAttachments: false, hasQueued: false })).toEqual({
			placeholder: "Message this worker…",
			showQueueNote: false,
			showSteerAction: false,
		});
	});

	it("uses the primary composer control to stop only when active and empty", () => {
		expect(composerPrimaryAction({ active: true, hasDraft: false, hasAttachments: false })).toBe("stop");
		expect(composerPrimaryAction({ active: true, hasDraft: true, hasAttachments: false })).toBe("send");
		expect(composerPrimaryAction({ active: true, hasDraft: false, hasAttachments: true })).toBe("send");
		expect(composerPrimaryAction({ active: false, hasDraft: false, hasAttachments: false })).toBe("send");
	});
});
