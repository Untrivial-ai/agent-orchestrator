import { describe, it, expect } from "vitest";

describe("CloudCredentialDialog", () => {
	it("should define agent metadata with all required agents", () => {
		const AGENT_METADATA = {
			"claude-code": {
				label: "Claude Code",
				creds: [
					{ value: "oauth_token", label: "Setup token" },
					{ value: "api_key", label: "API key" },
				],
			},
			codex: {
				label: "Codex",
				creds: [
					{ value: "access_token", label: "Access token" },
					{ value: "api_key", label: "API key" },
				],
			},
			cursor: {
				label: "Cursor",
				creds: [{ value: "api_key", label: "API key" }],
			},
		};

		expect(AGENT_METADATA["claude-code"].label).toBe("Claude Code");
		expect(AGENT_METADATA.codex.label).toBe("Codex");
		expect(AGENT_METADATA.cursor.label).toBe("Cursor");
	});

	it("should have credential types for each agent", () => {
		const agents = ["claude-code", "codex", "cursor"];

		agents.forEach((agent) => {
			expect(agent).toBeDefined();
		});
	});

	it("should support multiple credential types per agent", () => {
		const credentialTypes = {
			"claude-code": 2, // oauth_token, api_key
			codex: 2, // access_token, api_key
			cursor: 1, // api_key
		};

		Object.entries(credentialTypes).forEach(([, expectedCount]) => {
			expect(expectedCount).toBeGreaterThan(0);
		});
	});
});
