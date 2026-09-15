import { AGENT_OPTIONS } from "./agent-options";

describe("AGENT_OPTIONS", () => {
	it("offers fx as a spawn harness exactly once", () => {
		expect(AGENT_OPTIONS.filter((agent: string) => agent === "fx")).toHaveLength(1);
	});
	it("contains Prime Agent and OMP exactly once and has no duplicate harness ids", () => {
		expect(AGENT_OPTIONS.filter((agent) => agent === "prime-agent")).toHaveLength(1);
		expect(AGENT_OPTIONS.filter((agent) => agent === "omp")).toHaveLength(1);
		expect(new Set(AGENT_OPTIONS).size).toBe(AGENT_OPTIONS.length);
	});
});
