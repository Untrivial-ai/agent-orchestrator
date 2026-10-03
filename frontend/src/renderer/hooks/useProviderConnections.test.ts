import { describe, expect, it } from "vitest";
import type { CloudCpProviderConnection } from "../lib/cloud-cp";
import { hasValidAgentConnection } from "./useProviderConnections";

const connection = (provider: string, validationState: string) =>
	({ id: `${provider}-${validationState}`, provider, validationState }) as unknown as CloudCpProviderConnection;

describe("hasValidAgentConnection", () => {
	it("accepts a validated coding-agent connection", () => {
		expect(hasValidAgentConnection([connection("claude-code", "valid")])).toBe(true);
	});

	it("ignores a validated GitHub token from the personal list", () => {
		expect(hasValidAgentConnection([connection("github", "valid")])).toBe(false);
	});

	it("ignores connections the control plane has not validated", () => {
		expect(hasValidAgentConnection([connection("codex", "invalid")])).toBe(false);
		expect(hasValidAgentConnection(undefined)).toBe(false);
	});
});
