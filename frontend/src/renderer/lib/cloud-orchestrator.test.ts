import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { settingsQueryKey, type Settings } from "../hooks/useSettings";
import type { CloudCpProviderConnection } from "./cloud-cp";
import { selectCloudOrchestratorHarness, spawnCloudOrchestrator } from "./cloud-orchestrator";

const cloudMocks = vi.hoisted(() => ({
	me: vi.fn(),
	listProviderConnections: vi.fn(),
	listUserProviderConnections: vi.fn(),
	createSession: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	createRendererCloudCpClient: () => ({
		me: cloudMocks.me,
		listProviderConnections: cloudMocks.listProviderConnections,
		listUserProviderConnections: cloudMocks.listUserProviderConnections,
		createSession: cloudMocks.createSession,
	}),
}));

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
		expect(selectCloudOrchestratorHarness([connection("claude-code")])).toBe("claude-code");
	});

	it("preserves Codex as the preference when several supported agents are connected", () => {
		expect(selectCloudOrchestratorHarness([connection("cursor"), connection("codex")])).toBe("codex");
	});

	it("ignores invalid, non-default, and non-agent provider connections", () => {
		const invalid = connection("codex", "invalid");
		const nonDefault = { ...connection("claude-code"), label: "secondary" };
		expect(selectCloudOrchestratorHarness([invalid, nonDefault, connection("github")])).toBeUndefined();
	});
});

describe("spawnCloudOrchestrator", () => {
	beforeEach(() => {
		cloudMocks.me.mockReset();
		cloudMocks.listProviderConnections.mockReset();
		cloudMocks.listUserProviderConnections.mockReset();
		cloudMocks.createSession.mockReset();
	});

	it("starts without a user kickoff prompt so the role comes only from the system prompt", async () => {
		const queryClient = new QueryClient();
		queryClient.setQueryData<Settings>(settingsQueryKey, {
			cloudControlPlaneUrl: "https://cloud.example.com",
		} as Settings);
		cloudMocks.me.mockResolvedValue({ organizations: [{ id: "org-1" }] });
		cloudMocks.listProviderConnections.mockResolvedValue({
			providerConnections: [connection("claude-code")],
		});
		cloudMocks.listUserProviderConnections.mockResolvedValue({ providerConnections: [] });
		cloudMocks.createSession.mockResolvedValue({ session: { id: "session-1" } });

		await expect(spawnCloudOrchestrator(queryClient, "project-1")).resolves.toBe("session-1");
		expect(cloudMocks.createSession).toHaveBeenCalledWith("org-1", {
			projectId: "project-1",
			kind: "orchestrator",
			harness: "claude-code",
			displayName: "Orchestrator",
			prompt: "",
		});
	});
});
