import { describe, expect, it } from "vitest";
import { createRequestGate } from "./requestGate";

describe("request gate", () => {
	it("allows only the newest request to publish", () => {
		const gate = createRequestGate();
		const first = gate.begin();
		const second = gate.begin();
		expect(gate.isCurrent(first)).toBe(false);
		expect(gate.isCurrent(second)).toBe(true);
	});

	it("invalidates an in-flight request when its owner is dismissed", () => {
		const gate = createRequestGate();
		const request = gate.begin();
		gate.invalidate();
		expect(gate.isCurrent(request)).toBe(false);
	});
});
