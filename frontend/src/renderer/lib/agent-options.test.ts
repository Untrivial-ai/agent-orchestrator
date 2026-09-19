import { AGENT_OPTIONS, agentLabel } from "./agent-options";

describe("AGENT_OPTIONS", () => {
	it("contains only the opencode harness with no duplicates", () => {
		expect(AGENT_OPTIONS).toEqual(["opencode"]);
		expect(new Set(AGENT_OPTIONS).size).toBe(AGENT_OPTIONS.length);
	});
});

describe("agentLabel", () => {
	it("resolves the opencode display label", () => {
		expect(agentLabel("opencode")).toBe("OpenCode");
	});

	it("falls back to the raw provider for unknown harnesses", () => {
		expect(agentLabel("custom-agent")).toBe("custom-agent");
	});
});