import { describe, expect, it } from "vitest";

import { withHaptic } from "./hapticPress";

describe("withHaptic", () => {
	it("fires feedback before the action and preserves its contract", () => {
		const events: string[] = [];
		const wrapped = withHaptic(
			() => events.push("haptic"),
			(value: string) => {
				events.push(value);
				return value.length;
			},
		);

		expect(wrapped("action")).toBe(6);
		expect(events).toEqual(["haptic", "action"]);
	});
});
