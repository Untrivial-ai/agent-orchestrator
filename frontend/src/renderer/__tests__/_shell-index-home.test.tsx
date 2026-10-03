import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import {
	STANDALONE_PROJECT_KIND,
	STANDALONE_WORKSPACE_ID,
	type WorkspaceSession,
	type WorkspaceSummary,
} from "../types/workspace";

const routeMocks = vi.hoisted(() => ({
	createProjectFlowProps: null as null | {
		existingProjectPaths?: readonly string[];
		onOpenExistingProject?: (path: string) => void | Promise<void>;
		sourceSignal?: { source: string; nonce: number } | null;
	},
	navigate: vi.fn(),
	workspaces: [] as WorkspaceSummary[],
	requirements: [] as Array<{ id: string; label: string; satisfied: boolean; required: boolean; detail: string }>,
	authRequirement: undefined as { id: string; label: string; satisfied: boolean; required: boolean; detail: string } | undefined,
	startGitHubAuth: vi.fn(),
	markAutoLoginOffered: vi.fn(),
	closeTerminal: vi.fn(),
	cloudEnabled: false,
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	useNavigate: () => routeMocks.navigate,
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: () => ({ data: routeMocks.workspaces, isSuccess: true }),
}));

vi.mock("../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ cloudEnabled: routeMocks.cloudEnabled }),
}));

vi.mock("../hooks/useSystemRequirementsGate", () => ({
	useSystemRequirementsGate: () => ({ blocked: false, requirements: routeMocks.requirements, query: { refetch: vi.fn() } }),
	useGitHubAuthRequirement: () => ({ data: routeMocks.authRequirement, isFetching: false, refetch: vi.fn() }),
	useGitHubAuthAutoLoginOffered: () => ({ offered: false, markOffered: routeMocks.markAutoLoginOffered }),
	useGitHubAuthTerminal: () => ({ data: null, clear: vi.fn() }),
	useStartGitHubAuthTerminal: () => ({ mutate: routeMocks.startGitHubAuth, isPending: false, isError: false }),
}));

vi.mock("../hooks/useShellTerminals", () => ({
	useCloseShellTerminal: () => ({ mutate: routeMocks.closeTerminal }),
}));

vi.mock("../lib/shell-context", () => ({
	useShell: () => ({
		daemonStatus: { state: "ready" },
		workspaceStartupState: "ready",
		cloneProject: vi.fn(),
		createProject: vi.fn(),
		initializeProjectRepository: vi.fn(),
	}),
	useShellMaybe: () => ({ daemonStatus: { state: "ready" } }),
}));

vi.mock("../components/CreateProjectFlow", () => ({
	CreateProjectFlow: (props: NonNullable<typeof routeMocks.createProjectFlowProps>) => {
		routeMocks.createProjectFlowProps = props;
		return null;
	},
}));

import { HomePage } from "../components/HomePage";

const standaloneSession = (overrides: Partial<WorkspaceSession>): WorkspaceSession => ({
	id: "standalone-1",
	workspaceId: STANDALONE_WORKSPACE_ID,
	workspaceName: "Scratchpad",
	title: "Ad hoc task",
	provider: "codex",
	kind: "worker",
	status: "idle",
	updatedAt: "2026-06-15T00:00:00Z",
	prs: [],
	...overrides,
});

beforeEach(() => {
	useUiStore.setState({ developerMode: false, newTaskRequest: null });
	routeMocks.navigate.mockReset();
	routeMocks.workspaces = [];
	routeMocks.createProjectFlowProps = null;
	routeMocks.requirements = [];
	routeMocks.authRequirement = undefined;
	routeMocks.startGitHubAuth.mockReset();
	routeMocks.markAutoLoginOffered.mockReset();
	routeMocks.closeTerminal.mockReset();
	routeMocks.cloudEnabled = false;
});

describe("shell index route", () => {
	it("shows the home actions when no projects exist", () => {
		render(<HomePage />);

		expect(screen.getByText("Get started")).toBeInTheDocument();
		expect(screen.queryByText("Jump back right in")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Clone from Git" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import an existing project" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import a workspace folder" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "New standalone agent" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "New cloud project" })).not.toBeInTheDocument();
		expect(screen.queryByText("Recent projects")).not.toBeInTheDocument();
		expect(routeMocks.navigate).not.toHaveBeenCalled();
	});

	it("opens the clone flow from the empty home page", () => {
		render(<HomePage />);

		fireEvent.click(screen.getByRole("button", { name: "Clone from Git" }));
		expect(routeMocks.createProjectFlowProps?.sourceSignal?.source).toBe("clone");
	});

	it("adds cloud project creation beside standalone when Developer Mode and Cloud are enabled", () => {
		useUiStore.setState({ developerMode: true });
		routeMocks.cloudEnabled = true;
		render(<HomePage />);

		fireEvent.click(screen.getByRole("button", { name: "New cloud project" }));
		expect(routeMocks.createProjectFlowProps?.sourceSignal?.source).toBe("cloud");
		fireEvent.click(screen.getByRole("button", { name: "New standalone agent" }));
		expect(useUiStore.getState().newTaskRequest?.projectId).toBe(STANDALONE_WORKSPACE_ID);
	});

	it.each([
		{ developerMode: false, cloudEnabled: true },
		{ developerMode: true, cloudEnabled: false },
	])("keeps standalone creation when either toggle is off: %j", ({ developerMode, cloudEnabled }) => {
		useUiStore.setState({ developerMode });
		routeMocks.cloudEnabled = cloudEnabled;
		render(<HomePage />);

		fireEvent.click(screen.getByRole("button", { name: "New standalone agent" }));
		expect(useUiStore.getState().newTaskRequest?.projectId).toBe(STANDALONE_WORKSPACE_ID);
		expect(screen.queryByRole("button", { name: "New cloud project" })).not.toBeInTheDocument();
	});

	it("shows cloud creation when standalone sessions exist without a registered project", () => {
		useUiStore.setState({ developerMode: true });
		routeMocks.cloudEnabled = true;
		routeMocks.workspaces = [{
			id: STANDALONE_WORKSPACE_ID,
			name: "Scratchpad",
			kind: STANDALONE_PROJECT_KIND,
			path: "Not attached to a project",
			sessions: [standaloneSession({})],
		}];
		render(<HomePage />);

		fireEvent.click(screen.getByRole("button", { name: "New cloud project" }));
		expect(routeMocks.createProjectFlowProps?.sourceSignal?.source).toBe("cloud");
	});

	it("renders the home page instead of redirecting to a scratch board when projects exist", async () => {
		useUiStore.setState({ developerMode: true });
		routeMocks.cloudEnabled = true;
		routeMocks.workspaces = [
			{
				id: "scratch",
				name: "Scratch",
				kind: "scratch",
				path: "/home/me/.ao/scratch/default",
				sessions: [],
			},
		];

		render(<HomePage />);

		expect(screen.getByText("Jump back right in")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "New cloud project" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "New standalone agent" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Connect mobile" })).not.toBeInTheDocument();
		expect(routeMocks.navigate).not.toHaveBeenCalled();
	});

	it("surfaces missing GitHub authentication on the seeded first-run home page", () => {
		routeMocks.workspaces = [
			{ id: "scratch", name: "Scratch", kind: "scratch", path: "/scratch", sessions: [] },
		];
		routeMocks.requirements = [
			{ id: "gh", label: "gh", satisfied: true, required: false, detail: "/usr/bin/gh" },
		];
		routeMocks.authRequirement = { id: "github-auth", label: "GitHub access", satisfied: false, required: false, detail: "Sign in." };

		render(<HomePage />);

		expect(screen.getByText("Connect GitHub for pull requests")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Sign in with GitHub" })).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /Scratch/ }).compareDocumentPosition(
				screen.getByText("Connect GitHub for pull requests"),
			) & Node.DOCUMENT_POSITION_FOLLOWING,
		).toBeTruthy();
	});

	it("opens a project from the recent-project list", async () => {
		routeMocks.workspaces = [
			{ id: "scratch", name: "Scratch", kind: "scratch", path: "/scratch", sessions: [] },
			{ id: "proj-1", name: "Project One", kind: "single_repo", path: "/repo/project-one", sessions: [] },
		];

		render(<HomePage />);

		fireEvent.click(screen.getByRole("button", { name: /Project One/ }));
		expect(routeMocks.navigate).toHaveBeenCalledWith({
			to: "/projects/$projectId",
			params: { projectId: "proj-1" },
		});
	});

	it("keeps the Scratchpad out of recent projects and the heading count", () => {
		routeMocks.workspaces = [
			{
				id: STANDALONE_WORKSPACE_ID,
				name: "Scratchpad",
				kind: STANDALONE_PROJECT_KIND,
				path: "Not attached to a project",
				sessions: [standaloneSession({ status: "terminated", isTerminated: true })],
			},
		];

		render(<HomePage />);

		expect(screen.getByText("Get started")).toBeInTheDocument();
		expect(screen.queryByText("Recent projects")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /Scratchpad/ })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "New standalone agent" })).toBeInTheDocument();
	});

	it("lists real projects without the Scratchpad", () => {
		routeMocks.workspaces = [
			{
				id: STANDALONE_WORKSPACE_ID,
				name: "Scratchpad",
				kind: STANDALONE_PROJECT_KIND,
				path: "Not attached to a project",
				sessions: [standaloneSession({})],
			},
			{ id: "proj-1", name: "Project One", kind: "single_repo", path: "/repo/project-one", sessions: [] },
		];

		render(<HomePage />);

		expect(screen.getByText("Jump back right in")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /Project One/ })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /Scratchpad/ })).not.toBeInTheDocument();
	});

	it("opens an already registered path from the import flow", async () => {
		routeMocks.workspaces = [
			{ id: "proj-1", name: "Project One", kind: "single_repo", path: "/repo/project-one", sessions: [] },
		];

		render(<HomePage />);

		expect(routeMocks.createProjectFlowProps?.existingProjectPaths).toEqual(["/repo/project-one"]);
		await act(async () => routeMocks.createProjectFlowProps?.onOpenExistingProject?.("/repo/project-one"));
		expect(routeMocks.navigate).toHaveBeenCalledWith({
			to: "/projects/$projectId",
			params: { projectId: "proj-1" },
		});
	});

	it("shows only the first three projects", () => {
		routeMocks.workspaces = [
			{ id: "proj-1", name: "Project One", kind: "single_repo", path: "/repo/project-one", sessions: [] },
			{ id: "proj-2", name: "Project Two", kind: "single_repo", path: "/repo/project-two", sessions: [] },
			{ id: "proj-3", name: "Project Three", kind: "single_repo", path: "/repo/project-three", sessions: [] },
			{ id: "proj-4", name: "Project Four", kind: "single_repo", path: "/repo/project-four", sessions: [] },
		];

		render(<HomePage />);

		expect(screen.getByRole("button", { name: /Project One/ })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /Project Three/ })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /Project Four/ })).not.toBeInTheDocument();
	});
});
