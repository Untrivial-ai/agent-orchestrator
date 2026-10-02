import { describe, expect, it } from "vitest";
import type { CloudCpProviderConnection } from "./cloud-cp";
import { cloudAgentInfos, connectedCredentialType, credentialModelScope, isCloudHarnessConnected } from "./cloud-agents";

function connection(
	provider: string,
	overrides: Partial<CloudCpProviderConnection> = {},
): CloudCpProviderConnection {
	return {
		id: provider,
		provider,
		label: "default",
		config: {},
		validationState: "valid",
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		...overrides,
	};
}

describe("credentialModelScope", () => {
	it("prefixes the credential type so the daemon can tell it from a project id", () => {
		expect(credentialModelScope("anthropic_api_key")).toBe("@cred:anthropic_api_key");
	});
});

describe("connectedCredentialType", () => {
	it("returns the credential type of the valid default opencode connection", () => {
		const connections = [
			connection("opencode", { config: { credentialType: "anthropic_api_key" } }),
		];
		expect(connectedCredentialType(connections, "opencode")).toBe("anthropic_api_key");
	});

	it("ignores connections for other providers", () => {
		const connections = [
			connection("codex", { config: { credentialType: "auth_json" } }),
			connection("opencode", { config: { credentialType: "openai_api_key" } }),
		];
		expect(connectedCredentialType(connections, "opencode")).toBe("openai_api_key");
	});

	it("prefers the default label over other labels", () => {
		const connections = [
			connection("opencode", { label: "secondary", config: { credentialType: "openrouter_api_key" } }),
			connection("opencode", { config: { credentialType: "anthropic_api_key" } }),
		];
		expect(connectedCredentialType(connections, "opencode")).toBe("anthropic_api_key");
	});

	it("skips invalid connections", () => {
		const connections = [
			connection("opencode", { validationState: "invalid", config: { credentialType: "openai_api_key" } }),
		];
		expect(connectedCredentialType(connections, "opencode")).toBe("");
	});

	it("returns empty when the control plane recorded no credential type", () => {
		expect(connectedCredentialType([connection("opencode")], "opencode")).toBe("");
	});

	it("returns empty for undefined connections", () => {
		expect(connectedCredentialType(undefined, "opencode")).toBe("");
	});
});

describe("cloudAgentInfos", () => {
	it("offers Claude Code, Codex, Cursor, and OpenCode on cloud", () => {
		expect(cloudAgentInfos([]).map((agent) => agent.id)).toEqual(["claude-code", "codex", "cursor", "opencode"]);
	});

	it("marks an agent ready only when its connection is valid", () => {
		const agents = cloudAgentInfos([
			connection("claude-code"),
			connection("codex", { validationState: "invalid" }),
			connection("github"),
		]);
		expect(agents.map((agent) => [agent.id, agent.effectiveReadiness])).toEqual([
			["claude-code", "ready"],
			["codex", "not_ready"],
			["cursor", "not_ready"],
			["opencode", "not_ready"],
		]);
	});
});

describe("isCloudHarnessConnected", () => {
	it("uses the valid default connection, not whichever connection comes last", () => {
		const connections = [
			connection("codex"),
			connection("codex", { id: "codex-2", label: "secondary", validationState: "invalid" }),
		];
		expect(isCloudHarnessConnected(connections, "codex")).toBe(true);
		expect(cloudAgentInfos(connections).find((agent) => agent.id === "codex")?.effectiveReadiness).toBe("ready");
	});

	it("does not count a valid non-default connection", () => {
		const connections = [
			connection("claude-code", { validationState: "invalid" }),
			connection("claude-code", { id: "claude-2", label: "secondary" }),
		];
		expect(isCloudHarnessConnected(connections, "claude-code")).toBe(false);
		expect(cloudAgentInfos(connections).find((agent) => agent.id === "claude-code")?.effectiveReadiness).toBe("not_ready");
	});

	it("is false without connections", () => {
		expect(isCloudHarnessConnected(undefined, "codex")).toBe(false);
	});
});
