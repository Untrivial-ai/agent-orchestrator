import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { RestoreUnavailableDialog } from "./RestoreUnavailableDialog";

const { spawnMock, remoteSpawnMock, workspaceQueryMock } = vi.hoisted(() => ({
	spawnMock: vi.fn(),
	remoteSpawnMock: vi.fn(),
	workspaceQueryMock: vi.fn(),
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceScope: (projectId: string, sessionId?: string, hostId?: string) => workspaceQueryMock(projectId, sessionId, hostId),
}));

vi.mock("../lib/spawn-orchestrator", () => ({
	spawnOrchestrator: spawnMock,
}));
vi.mock("../lib/remote-orchestrator", () => ({ openRemoteOrchestrator: remoteSpawnMock }));

const session: WorkspaceSession = {
	id: "orch-old",
	workspaceId: "proj-1",
	workspaceName: "Project One",
	title: "orchestrator",
	provider: "codex",
	kind: "orchestrator",
	status: "terminated",
	updatedAt: "2026-07-26T00:00:00Z",
	prs: [],
};

const workspace: WorkspaceSummary = {
	id: "proj-1",
	name: "Project One",
	path: "/repo/project-one",
	orchestratorAgent: "codex",
	sessions: [session],
};

beforeEach(() => {
	vi.clearAllMocks();
	useUiStore.setState({ settingsModal: null });
	workspaceQueryMock.mockReturnValue({ data: { project: workspace }, isLoading: false });
});

describe("RestoreUnavailableDialog", () => {
	it("opens project settings instead of recreating when no orchestrator agent is configured", async () => {
		const onOpenChange = vi.fn();
		const onRecreated = vi.fn();
		workspaceQueryMock.mockReturnValue({
		data: { project: { ...workspace, orchestratorAgent: undefined } },
			isLoading: false,
		});
		render(
			<RestoreUnavailableDialog
				open
				session={session}
				onOpenChange={onOpenChange}
				onRecreated={onRecreated}
			/>,
		);

		await userEvent.click(screen.getByRole("button", { name: "Configure orchestrator agent" }));

		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1" });
		expect(spawnMock).not.toHaveBeenCalled();
		expect(onRecreated).not.toHaveBeenCalled();
	});

	it("preserves clean recreation when an orchestrator agent is configured", async () => {
		const onOpenChange = vi.fn();
		const onRecreated = vi.fn();
		spawnMock.mockResolvedValue("orch-new");
		render(
			<RestoreUnavailableDialog
				open
				session={session}
				onOpenChange={onOpenChange}
				onRecreated={onRecreated}
			/>,
		);

		await userEvent.click(screen.getByRole("button", { name: "Create new orchestrator" }));

		await waitFor(() => expect(onRecreated).toHaveBeenCalledWith("orch-new"));
		expect(spawnMock).toHaveBeenCalledWith("proj-1", "restore_dialog", true);
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	it("recreates a remote orchestrator on its own host, not the local daemon", async () => {
		const onRecreated = vi.fn();
		remoteSpawnMock.mockResolvedValue("remote-new");
		render(<RestoreUnavailableDialog open hostId="box-b" session={session} onOpenChange={vi.fn()} onRecreated={onRecreated} />);

		await userEvent.click(screen.getByRole("button", { name: "Create new orchestrator" }));

		await waitFor(() => expect(onRecreated).toHaveBeenCalledWith("remote-new"));
		expect(workspaceQueryMock).toHaveBeenCalledWith("proj-1", undefined, "box-b");
		expect(remoteSpawnMock).toHaveBeenCalledWith("box-b", "proj-1", undefined, undefined, true, "restore_dialog");
		expect(spawnMock).not.toHaveBeenCalled();
	});

	it("opens host-qualified settings when a remote project lacks an orchestrator", async () => {
		workspaceQueryMock.mockReturnValue({ data: { project: { ...workspace, orchestratorAgent: undefined } }, isLoading: false });
		render(<RestoreUnavailableDialog open hostId="box-b" session={session} onOpenChange={vi.fn()} onRecreated={vi.fn()} />);

		await userEvent.click(screen.getByRole("button", { name: "Configure orchestrator agent" }));

		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1", hostId: "box-b" });
		expect(spawnMock).not.toHaveBeenCalled();
		expect(remoteSpawnMock).not.toHaveBeenCalled();
	});
});
