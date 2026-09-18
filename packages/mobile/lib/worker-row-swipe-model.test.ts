import { describe, expect, it } from "vitest";
import { boundWorkerActionTranslation, resolveWorkerActionRail } from "./worker-row-swipe-model";

describe("resolveWorkerActionRail", () => {
	it("opens the action rail after a deliberate left swipe", () => {
		expect(resolveWorkerActionRail({ translationX: -65, velocityX: 0 })).toBe(-128);
	});

	it("opens the action rail for a quick left flick before crossing the distance threshold", () => {
		expect(resolveWorkerActionRail({ translationX: -18, velocityX: -520 })).toBe(-128);
	});

	it("closes the action rail for a short or rightward release", () => {
		expect(resolveWorkerActionRail({ translationX: -35, velocityX: 0 })).toBe(0);
		expect(resolveWorkerActionRail({ translationX: -90, velocityX: 520 })).toBe(0);
	});

	it("tracks a finger only within the action rail's reveal bounds", () => {
		expect(boundWorkerActionTranslation(-40)).toBe(-40);
		expect(boundWorkerActionTranslation(18)).toBe(0);
		expect(boundWorkerActionTranslation(-190)).toBe(-128);
	});
});
