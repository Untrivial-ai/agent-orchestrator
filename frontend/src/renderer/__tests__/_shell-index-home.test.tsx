import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSummary } from "../types/workspace";
import { TooltipProvider } from "../components/ui/tooltip";

const routeMocks = vi.hoisted(() => ({
	createProjectFlowProps: null as null | {
		existingProjectPaths?: readonly string[];
		onOpenExistingProject?: (path: string) => void | Promise<void>;
	},
	navigate: vi.fn(),
	workspaces: [] as WorkspaceSummary[],
	requirements: [] as Array<{ id: string; label: string; satisfied: boolean; required: boolean; detail: string }>,
	authRequirement: undefined as { id: string; label: string; satisfied: boolean; required: boolean; detail: string } | undefined,
	authTerminal: null as null | { handleId: string; title: string },
	deviceCode: "",
	startGitHubAuth: vi.fn(),
	markAutoLoginOffered: vi.fn(),
	closeTerminal: vi.fn(),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	useNavigate: () => routeMocks.navigate,
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: () => ({ data: routeMocks.workspaces, isSuccess: true }),
}));

vi.mock("../hooks/useSystemRequirementsGate", () => ({
	useSystemRequirementsGate: () => ({ blocked: false, requirements: routeMocks.requirements, query: { refetch: vi.fn() } }),
	useGitHubAuthRequirement: () => ({ data: routeMocks.authRequirement, isFetching: false, refetch: vi.fn() }),
	useGitHubAuthAutoLoginOffered: () => ({ offered: false, markOffered: routeMocks.markAutoLoginOffered }),
	useGitHubAuthTerminal: () => ({ data: routeMocks.authTerminal, deviceCode: routeMocks.deviceCode, clear: vi.fn() }),
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

vi.mock("../components/BoardEmptyStates", () => ({
	BoardWelcome: () => <div data-testid="board-welcome" />,
}));

import { HomePage } from "../components/HomePage";

beforeEach(() => {
	routeMocks.navigate.mockReset();
	routeMocks.workspaces = [];
	routeMocks.createProjectFlowProps = null;
	routeMocks.requirements = [];
	routeMocks.authRequirement = undefined;
	routeMocks.authTerminal = null;
	routeMocks.deviceCode = "";
	routeMocks.startGitHubAuth.mockReset();
	routeMocks.markAutoLoginOffered.mockReset();
	routeMocks.closeTerminal.mockReset();
});

describe("shell index route", () => {
	it("restores first-run onboarding when no projects exist", async () => {
		render(<HomePage />);

		expect(screen.getByTestId("board-welcome")).toBeInTheDocument();
		expect(screen.queryByText("Jump back right in")).not.toBeInTheDocument();
		expect(routeMocks.navigate).not.toHaveBeenCalled();
	});

	it("renders the home page instead of redirecting to a scratch board when projects exist", async () => {
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
		expect(routeMocks.navigate).not.toHaveBeenCalled();
	});

	it("surfaces optional GitHub authentication on the Home page without auto-opening it", async () => {
		routeMocks.workspaces = [
			{ id: "scratch", name: "Scratch", kind: "scratch", path: "/scratch", sessions: [] },
		];
		routeMocks.requirements = [
			{ id: "gh", label: "gh", satisfied: true, required: false, detail: "/usr/bin/gh" },
		];
		routeMocks.authRequirement = { id: "github-auth", label: "GitHub access", satisfied: false, required: false, detail: "Sign in." };

		render(<HomePage />);

		expect(screen.getByText("GitHub isn't connected.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Connect GitHub" })).toBeInTheDocument();
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(routeMocks.startGitHubAuth).not.toHaveBeenCalled();
		expect(
			screen.getByRole("button", { name: /Scratch/ }).compareDocumentPosition(
				screen.getByTestId("github-onboarding-notice"),
			) & Node.DOCUMENT_POSITION_PRECEDING,
		).toBeTruthy();

		await fireEvent.click(screen.getByRole("button", { name: "Connect GitHub" }));
		expect(await screen.findByRole("dialog")).toBeInTheDocument();
	});

	it("shows the device code with a user-triggered browser open once sign-in starts", async () => {
		routeMocks.workspaces = [
			{ id: "scratch", name: "Scratch", kind: "scratch", path: "/scratch", sessions: [] },
		];
		routeMocks.requirements = [
			{ id: "gh", label: "gh", satisfied: true, required: false, detail: "/usr/bin/gh" },
		];
		routeMocks.authRequirement = { id: "github-auth", label: "GitHub access", satisfied: false, required: false, detail: "Sign in." };
		routeMocks.authTerminal = { handleId: "shellterm-github", title: "Connect GitHub" };
		routeMocks.deviceCode = "C6D9-51C4";

		render(<TooltipProvider><HomePage /></TooltipProvider>);

		await fireEvent.click(screen.getByRole("button", { name: "Connect GitHub" }));
		expect(await screen.findByRole("dialog")).toBeInTheDocument();
		expect(screen.getByText("C6D9-51C4")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Open GitHub" })).toBeInTheDocument();
		expect(routeMocks.startGitHubAuth).not.toHaveBeenCalled();
	});

	it("hides the onboarding notice once GitHub is connected", () => {
		routeMocks.workspaces = [
			{ id: "scratch", name: "Scratch", kind: "scratch", path: "/scratch", sessions: [] },
		];
		routeMocks.requirements = [
			{ id: "gh", label: "gh", satisfied: true, required: false, detail: "/usr/bin/gh" },
		];
		routeMocks.authRequirement = { id: "github-auth", label: "GitHub access", satisfied: true, required: false, detail: "GitHub CLI is signed in." };

		render(<HomePage />);

		expect(screen.queryByTestId("github-onboarding-notice")).not.toBeInTheDocument();
		expect(screen.queryByText("GitHub isn't connected.")).not.toBeInTheDocument();
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
