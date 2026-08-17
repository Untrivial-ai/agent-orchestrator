import { describe, expect, it } from "vitest";
import { interfaceTransitionPollInterval } from "./interfaceTransition";

describe("mobile interface transition polling", () => {
	it("polls quickly while a transition is active", () => {
		expect(interfaceTransitionPollInterval({ phase: "draining" })).toBe(300);
	});

	it("does not poll while idle or after a transition settles", () => {
		expect(interfaceTransitionPollInterval()).toBeUndefined();
		expect(interfaceTransitionPollInterval({ phase: "completed" })).toBeUndefined();
	});
});
