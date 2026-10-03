import { beforeEach, expect, it, vi } from "vitest";
import { openRemoteOrchestrator } from "./remote-orchestrator";
import type { WorkspaceSession } from "../types/workspace";

const mocks = vi.hoisted(() => ({ post: vi.fn(), selectedHosts: [] as string[] }));
vi.mock("./host-clients", () => ({
	clientForSessionHost: (hostId: string) => {
		mocks.selectedHosts.push(hostId);
		return { POST: mocks.post };
	},
}));

beforeEach(() => {
	mocks.post.mockReset();
	mocks.selectedHosts.length = 0;
});

it("deduplicates concurrent spawns for one host and project", async () => {
	let finish!: (value: unknown) => void;
	mocks.post.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
	const first = openRemoteOrchestrator("box-b", "shared");
	const second = openRemoteOrchestrator("box-b", "shared");
	expect(first).toBe(second);
	expect(mocks.selectedHosts).toEqual(["box-b"]);
	expect(mocks.post).toHaveBeenCalledWith("/api/v1/orchestrators", { body: { projectId: "shared", clean: false } });
	finish({ data: { orchestrator: { id: "orch-b" } } });
	await expect(first).resolves.toBe("orch-b");
});

it("resumes an exited orchestrator on its owning host", async () => {
	const orchestrator: WorkspaceSession = {
		hostId: "box-a", id: "orch-a", workspaceId: "shared", workspaceName: "Todo App",
		title: "Orchestrator", provider: "opencode", kind: "orchestrator", status: "working",
		updatedAt: "2026-09-28T00:00:00Z", prs: [], activity: { state: "exited", lastActivityAt: "2026-09-28T00:00:00Z" },
	};
	mocks.post.mockResolvedValue({ data: { resumeMode: "native" } });
	await expect(openRemoteOrchestrator("box-a", "shared", orchestrator)).resolves.toBe("orch-a");
	expect(mocks.selectedHosts).toEqual(["box-a"]);
	expect(mocks.post).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/resume-agent", {
		params: { path: { sessionId: "orch-a" } },
	});
});

it("replaces an active orchestrator when the project agent changed", async () => {
	const orchestrator: WorkspaceSession = {
		hostId: "box-a", id: "orch-old", workspaceId: "shared", workspaceName: "Todo App",
		title: "Orchestrator", provider: "codex", kind: "orchestrator", status: "working",
		updatedAt: "2026-09-28T00:00:00Z", prs: [],
	};
	mocks.post.mockResolvedValue({ data: { orchestrator: { id: "orch-new" } } });
	await expect(openRemoteOrchestrator("box-a", "shared", orchestrator, undefined, true)).resolves.toBe("orch-new");
	expect(mocks.post).toHaveBeenCalledWith("/api/v1/orchestrators", { body: { projectId: "shared", clean: true } });
});
