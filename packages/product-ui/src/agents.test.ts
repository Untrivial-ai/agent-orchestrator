import { describe, expect, it } from "vitest";
import { AGENT_LABELS, AGENT_OPTIONS } from "./agents";

describe("Qoder agent identity", () => {
	it("is listed exactly once with its product label", () => {
		expect(AGENT_OPTIONS.filter((id) => id === "qoder")).toHaveLength(1);
		expect(AGENT_LABELS.qoder).toBe("Qoder");
	});
});
