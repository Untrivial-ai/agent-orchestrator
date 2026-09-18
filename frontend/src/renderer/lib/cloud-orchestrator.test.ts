import { describe, expect, it } from "vitest";
import type { CloudCpProviderConnection } from "./cloud-cp";
import { selectCloudOrchestratorHarness } from "./cloud-orchestrator";

function connection(provider: string, validationState = "valid"): CloudCpProviderConnection {
	return {
		id: provider,
		provider,
		label: "default",
		config: {},
		validationState,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
	};
}

describe("selectCloudOrchestratorHarness", () => {
	it("uses the connected harness when it is the only Cloud option", () => {
		expect(selectCloudOrchestratorHarness([connection("opencode")])).toBe("opencode");
	});

	it("ignores invalid, non-default, and non-agent provider connections", () => {
		const invalid = connection("opencode", "invalid");
		const nonDefault = { ...connection("opencode"), label: "secondary" };
		expect(selectCloudOrchestratorHarness([invalid, nonDefault, connection("github")])).toBeUndefined();
	});
});
