import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { settingsQueryKey, type Settings } from "../hooks/useSettings";
import { spawnCloudOrchestrator } from "./cloud-orchestrator";

const cloudMocks = vi.hoisted(() => ({
	me: vi.fn(),
	createSession: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	createRendererCloudCpClient: () => ({
		me: cloudMocks.me,
		createSession: cloudMocks.createSession,
	}),
}));

describe("spawnCloudOrchestrator", () => {
	beforeEach(() => {
		cloudMocks.me.mockReset();
		cloudMocks.createSession.mockReset();
	});

	it("starts without a user kickoff prompt so the role comes only from the system prompt", async () => {
		const queryClient = new QueryClient();
		queryClient.setQueryData<Settings>(settingsQueryKey, {
			cloudControlPlaneUrl: "https://cloud.example.com",
		} as Settings);
		cloudMocks.me.mockResolvedValue({ organizations: [{ id: "org-1" }] });
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
