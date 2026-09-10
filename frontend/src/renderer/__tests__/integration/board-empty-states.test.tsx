import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render as rtlRender, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { StrictMode, type ReactNode } from "react";
import { TooltipProvider } from "../../components/ui/tooltip";

function render(ui: ReactNode) {
	const result = rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
	return {
		...result,
		rerender: (nextUi: ReactNode) => result.rerender(<TooltipProvider>{nextUi}</TooltipProvider>),
	};
}

// Drives the real useWorkspaceQuery + SessionsBoard end to end for the two
// first-run states, mocking only the HTTP client, the router, and the native
// folder picker: an empty daemon shows the import chooser (no column shells), a
// fresh project shows the task invitation, and any session brings the columns back.
const { getMock, postMock, deleteMock, navigateMock, chooseDirectoryMock, clipboardWriteMock, spawnOrchestratorMock, terminalPanePropsMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	deleteMock: vi.fn(),
	navigateMock: vi.fn(),
	chooseDirectoryMock: vi.fn(),
	clipboardWriteMock: vi.fn(),
	spawnOrchestratorMock: vi.fn(),
	terminalPanePropsMock: vi.fn(),
}));

vi.mock("../../lib/spawn-orchestrator", () => ({
	isChatPreflightError: (error: unknown) =>
		error instanceof Error && (error as Error & { code?: string }).code === "CHAT_DRIVER_UNAVAILABLE",
	spawnOrchestrator: spawnOrchestratorMock,
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock, DELETE: deleteMock },
	apiErrorCode: () => undefined,
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
	hasTrustedApiBaseUrl: () => true,
}));

vi.mock("../../components/TerminalPane", () => ({
	TerminalPane: (props: { focusRequested?: boolean; onTerminalStateChange?: (state: "attached" | "exited") => void }) => {
		terminalPanePropsMock(props);
		return <div data-focus-requested={props.focusRequested ? "true" : "false"} data-testid="terminal-pane" />;
	},
}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: {
		app: { chooseDirectory: chooseDirectoryMock, openExternal: vi.fn() },
		clipboard: { writeText: clipboardWriteMock },
		// CreateProjectFlow reads the cloud session (Local | Cloud gating);
		// signed-out keeps these tests on the local-only flow.
		cloud: {
			getSession: async () => null,
			signIn: async () => undefined,
			signOut: async () => undefined,
			onSessionChanged: () => () => undefined,
		},
	},
}));

vi.mock("@tanstack/react-router", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@tanstack/react-router")>();
	return { ...actual, useNavigate: () => navigateMock };
});

import { SessionsBoard } from "../../components/SessionsBoard";
import { ShellProvider, type ShellContextValue } from "../../lib/shell-context";
import { useUiStore } from "../../stores/ui-store";

type Project = { id: string; name: string; path: string; orchestratorAgent?: string };
type Session = Record<string, unknown>;

function respondWith(
	projects: Project[],
	sessions: Session[],
	githubAuthenticated = true,
	githubCliSatisfied: boolean | null = true,
) {
	getMock.mockImplementation(async (url: string) => {
		if (url === "/api/v1/projects") return { data: { projects }, error: undefined };
		if (url === "/api/v1/sessions") return { data: { sessions }, error: undefined };
		if (url === "/api/v1/system/requirements") {
			return {
				data: {
					ready: true,
					requirements: [
						{ id: "git", label: "git", satisfied: true, required: true, detail: "/usr/bin/git" },
						{ id: "tmux", label: "tmux", satisfied: true, required: true, detail: "/usr/bin/tmux" },
						{ id: "harness", label: "agent harness", satisfied: true, required: true, detail: "Claude Code" },
						...(githubCliSatisfied === null
							? []
							: [{ id: "gh", label: "gh", satisfied: githubCliSatisfied, required: false, detail: githubCliSatisfied ? "/usr/bin/gh" : "Not found" }]),
					],
				},
				error: undefined,
			};
		}
		if (url === "/api/v1/system/github-auth") {
			return {
				data: {
					id: "github-auth",
					label: "GitHub access",
					satisfied: githubAuthenticated,
					required: false,
					detail: githubAuthenticated ? "GitHub CLI is signed in." : "Sign in with `gh auth login`.",
				},
				error: undefined,
			};
		}
		return { data: undefined, error: undefined };
	});
}

const project: Project = {
	id: "proj-1",
	name: "my-app",
	path: "/repo/my-app",
	orchestratorAgent: "claude-code",
};

const workerSession: Session = {
	id: "sess-1",
	projectId: "proj-1",
	displayName: "fix the bug",
	harness: "claude-code",
	kind: "worker",
	status: "working",
	isTerminated: false,
	updatedAt: "2026-07-04T10:00:00Z",
	prs: [],
};

const orchestratorSession: Session = {
	id: "proj-1-orchestrator",
	projectId: "proj-1",
	displayName: "orchestrator",
	harness: "claude-code",
	kind: "orchestrator",
	status: "working",
	isTerminated: false,
	updatedAt: "2026-07-04T10:00:00Z",
	prs: [],
};

const createProjectMock = vi.fn().mockResolvedValue(undefined);
const cloneProjectMock = vi.fn().mockResolvedValue(undefined);
const initializeProjectRepositoryMock = vi.fn().mockResolvedValue(undefined);

// Kept from the latest renderBoard call so tests can rerender with the same
// providers (e.g. simulating a projectId route-param change on a mounted board).
let lastQueryClient: QueryClient | null = null;
let lastShell: ShellContextValue | null = null;

function renderBoard(ui: ReactNode) {
	lastQueryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	lastShell = {
		daemonStatus: { state: "ready" } as ShellContextValue["daemonStatus"],
		workspaceStartupState: "ready",
		cloneProject: cloneProjectMock,
		createProject: createProjectMock,
		initializeProjectRepository: initializeProjectRepositoryMock,
	};
	return render(
		<QueryClientProvider client={lastQueryClient}>
			<ShellProvider value={lastShell}>{ui}</ShellProvider>
		</QueryClientProvider>,
	);
}

// The kanban columns render as <section> elements; the empty states render none.
const columnCount = () => document.querySelectorAll("section").length;

beforeEach(() => {
	vi.clearAllMocks();
	cloneProjectMock.mockResolvedValue(undefined);
	createProjectMock.mockResolvedValue(undefined);
	initializeProjectRepositoryMock.mockResolvedValue(undefined);
	clipboardWriteMock.mockResolvedValue(undefined);
	postMock.mockResolvedValue({
		data: {
			shellTerminal: {
				handleId: "shellterm-github",
				workingDir: "/tmp/auth",
				title: "Connect GitHub",
				createdAt: "2026-07-04T10:00:00Z",
			},
		},
		error: undefined,
	});
	deleteMock.mockResolvedValue({ error: undefined });
	useUiStore.setState({
		orchestratorReplacementErrors: {},
		orchestratorStartupErrors: {},
		provisioningProjectIds: new Set(),
		restartingProjectIds: new Set(),
		settingsModal: null,
	});
});

describe("global board first launch", () => {
	it("runs the lightweight requirements preflight while loading the board", async () => {
		respondWith([], []);
		renderBoard(<SessionsBoard />);

		await waitFor(() => {
			expect(getMock.mock.calls.some(([url]) => url === "/api/v1/projects")).toBe(true);
			expect(getMock.mock.calls.some(([url]) => url === "/api/v1/sessions")).toBe(true);
		});
		expect(await screen.findByText("Add a project")).toBeInTheDocument();
		expect(getMock.mock.calls.some(([url]) => url === "/api/v1/system/requirements")).toBe(true);
	});

	it("renders the board shell while the daemon is booting", async () => {
		respondWith([], []);
		lastQueryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		lastShell = {
			daemonStatus: { state: "starting" } as ShellContextValue["daemonStatus"],
			workspaceStartupState: "loading",
			cloneProject: cloneProjectMock,
			createProject: createProjectMock,
			initializeProjectRepository: initializeProjectRepositoryMock,
		};
		render(
			<QueryClientProvider client={lastQueryClient}>
				<ShellProvider value={lastShell}>
					<SessionsBoard />
				</ShellProvider>
			</QueryClientProvider>,
		);

		expect(await screen.findByTestId("board")).toBeInTheDocument();
		expect(screen.getByTestId("daemon-startup-loader")).toBeInTheDocument();
	});

	it("shows the import chooser instead of empty columns when no projects exist", async () => {
		respondWith([], []);
		renderBoard(<SessionsBoard />);

		expect(await screen.findByText("Add a project")).toBeInTheDocument();
		expect(screen.getByText("Choose where your code is located")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Clone from Git" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import a workspace folder" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import an existing project" })).toBeInTheDocument();
		expect(columnCount()).toBe(0);
		// The welcome carries its own orientation — no dangling "Board" header.
		expect(screen.queryByText("Board")).not.toBeInTheDocument();
	});

	it("automatically opens GitHub sign-in without requesting focus when gh is signed out", async () => {
		respondWith([], [], false);
		renderBoard(
			<StrictMode>
				<SessionsBoard />
			</StrictMode>,
		);

		expect(await screen.findByText("Connect GitHub for pull requests")).toBeInTheDocument();
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/system/github-auth/terminal");
		expect(await screen.findByTestId("github-auth-terminal")).toBeInTheDocument();
		expect(screen.getByTestId("terminal-pane")).toHaveAttribute("data-focus-requested", "false");
		act(() => terminalPanePropsMock.mock.lastCall?.[0].onTerminalStateChange?.("attached"));
		expect(screen.queryByRole("button", { name: "Check again" })).not.toBeInTheDocument();
		expect(screen.getByTestId("terminal-pane")).toHaveAttribute("data-focus-requested", "false");
	});

	it("adopts a daemon-owned login after renderer state is lost", async () => {
		respondWith([], [], false);
		const originalGet = getMock.getMockImplementation()!;
		getMock.mockImplementation(async (url: string) => url === "/api/v1/shell-terminals"
			? { data: { shellTerminals: [{ handleId: "surviving-login", title: "Connect GitHub", workingDir: "/tmp/auth", createdAt: "2026-07-04T10:00:00Z" }] } }
			: originalGet(url));
		const first = renderBoard(<SessionsBoard />);
		await waitFor(() => expect(terminalPanePropsMock).toHaveBeenCalledWith(expect.objectContaining({ terminalTarget: expect.objectContaining({ handleId: "surviving-login" }) })));
		first.unmount();
		terminalPanePropsMock.mockClear();
		renderBoard(<SessionsBoard />);
		await waitFor(() => expect(terminalPanePropsMock).toHaveBeenCalledWith(expect.objectContaining({ terminalTarget: expect.objectContaining({ handleId: "surviving-login" }) })));
		expect(postMock).not.toHaveBeenCalled();
	});

	it("does not spawn when daemon terminal reconciliation fails", async () => {
		respondWith([], [], false);
		const originalGet = getMock.getMockImplementation()!;
		getMock.mockImplementation(async (url: string) => url === "/api/v1/shell-terminals"
			? { error: new Error("Terminal list unavailable") }
			: originalGet(url));
		renderBoard(<SessionsBoard />);
		expect(await screen.findByRole("alert", {}, { timeout: 5000 })).toHaveTextContent("Terminal list unavailable");
		expect(postMock).not.toHaveBeenCalled();
	});

	it("does not open GitHub sign-in when the initial auth check succeeds", async () => {
		respondWith([], []);
		renderBoard(<SessionsBoard />);

		expect(await screen.findByText("Add a project")).toBeInTheDocument();
		expect(postMock).not.toHaveBeenCalledWith("/api/v1/system/github-auth/terminal");
	});

	it("respects a dismissed automatic login after the notice remounts", async () => {
		respondWith([], [], false);
		const view = renderBoard(<SessionsBoard />);
		await screen.findByTestId("github-auth-terminal");
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));

		await userEvent.click(screen.getByRole("button", { name: "Close" }));
		await waitFor(() => expect(screen.queryByTestId("github-auth-terminal")).not.toBeInTheDocument());
		view.unmount();
		render(
			<QueryClientProvider client={lastQueryClient!}>
				<ShellProvider value={lastShell!}><SessionsBoard /></ShellProvider>
			</QueryClientProvider>,
		);

		expect(await screen.findByText("Connect GitHub for pull requests")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledTimes(1);
	});

	it.each(["Close", "Try again"])("retains the login handle when %s cannot close its PTY", async (action) => {
		respondWith([], [], false);
		renderBoard(<SessionsBoard />);
		await screen.findByTestId("github-auth-terminal");
		if (action === "Try again") {
			act(() => terminalPanePropsMock.mock.lastCall?.[0].onTerminalStateChange?.("exited"));
		}
		deleteMock.mockResolvedValue({ error: new Error("Close failed") });
		await userEvent.click(await screen.findByRole("button", { name: action }));
		await waitFor(() => {
			expect(deleteMock).toHaveBeenCalledTimes(1);
			expect(lastQueryClient!.isMutating()).toBe(0);
		});
		expect(screen.getByTestId("github-auth-terminal")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledTimes(1);
	});

	it("offers recovery instead of treating an exited login terminal as active", async () => {
		respondWith([], [], false);
		renderBoard(<SessionsBoard />);
		await screen.findByTestId("github-auth-terminal");

		act(() => terminalPanePropsMock.mock.lastCall?.[0].onTerminalStateChange?.("attached"));
		act(() => terminalPanePropsMock.mock.lastCall?.[0].onTerminalStateChange?.("exited"));

		expect(await screen.findByText("Sign-in stopped before GitHub was connected")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Try again" })).toBeEnabled();
		expect(screen.getByRole("button", { name: "Check again" })).toBeEnabled();

		await userEvent.click(screen.getByRole("button", { name: "Try again" }));
		await waitFor(() => expect(deleteMock).toHaveBeenCalledWith("/api/v1/shell-terminals/{handleId}", {
			params: { path: { handleId: "shellterm-github" } },
		}));
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(2));
		act(() => terminalPanePropsMock.mock.lastCall?.[0].onTerminalStateChange?.("attached"));
		expect(screen.getByTestId("terminal-pane")).toHaveAttribute("data-focus-requested", "true");
	});

	it("keeps sign-in available when GitHub CLI readiness is unknown", async () => {
		respondWith([], [], false, null);
		renderBoard(<SessionsBoard />);

		expect(await screen.findByText("Connect GitHub for pull requests")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Sign in with GitHub" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Check again" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Get GitHub CLI" })).not.toBeInTheDocument();
		expect(postMock).not.toHaveBeenCalledWith("/api/v1/system/github-auth/terminal");

		await userEvent.click(screen.getByRole("button", { name: "Sign in with GitHub" }));
		await screen.findByTestId("github-auth-terminal");
		act(() => terminalPanePropsMock.mock.lastCall?.[0].onTerminalStateChange?.("attached"));
		expect(screen.getByTestId("terminal-pane")).toHaveAttribute("data-focus-requested", "true");
	});

	it("opens the native folder picker from the Project card", async () => {
		respondWith([], []);
		chooseDirectoryMock.mockResolvedValue(null);
		renderBoard(<SessionsBoard />);

		await userEvent.click(await screen.findByRole("button", { name: "Import an existing project" }));
		expect(chooseDirectoryMock).toHaveBeenCalledTimes(1);
		expect(chooseDirectoryMock).toHaveBeenCalledWith("Choose a project repository");
	});

	it("opens the native folder picker from the Workspace card", async () => {
		respondWith([], []);
		chooseDirectoryMock.mockResolvedValue(null);
		renderBoard(<SessionsBoard />);

		await userEvent.click(await screen.findByRole("button", { name: "Import a workspace folder" }));
		expect(chooseDirectoryMock).toHaveBeenCalledTimes(1);
		expect(chooseDirectoryMock).toHaveBeenCalledWith("Choose a workspace folder");
	});

	it("shows a visible error when the folder picker fails", async () => {
		respondWith([], []);
		chooseDirectoryMock.mockRejectedValue(new Error("dialog unavailable"));
		renderBoard(<SessionsBoard />);

		await userEvent.click(await screen.findByRole("button", { name: "Import an existing project" }));
		const messages = await screen.findAllByText("dialog unavailable");
		expect(messages.some((el) => !el.classList.contains("sr-only"))).toBe(true);
	});

	it("keeps the columns once a project exists", async () => {
		respondWith([project], [workerSession]);
		renderBoard(<SessionsBoard />);

		expect(await screen.findByText("fix the bug")).toBeInTheDocument();
		expect(screen.queryByText("Add a project")).not.toBeInTheDocument();
		expect(columnCount()).toBe(4);
	});

	it("keeps populated columns visible after the daemon reports a startup failure", async () => {
		respondWith([project], [workerSession]);
		lastQueryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		lastShell = {
			daemonStatus: { state: "stopped", code: "exited" } as ShellContextValue["daemonStatus"],
			workspaceStartupState: "loading",
			cloneProject: cloneProjectMock,
			createProject: createProjectMock,
			initializeProjectRepository: initializeProjectRepositoryMock,
		};
		render(
			<QueryClientProvider client={lastQueryClient}>
				<ShellProvider value={lastShell}>
					<SessionsBoard />
				</ShellProvider>
			</QueryClientProvider>,
		);

		expect(await screen.findByText("fix the bug")).toBeInTheDocument();
		expect(screen.queryByTestId("daemon-startup-loader")).not.toBeInTheDocument();
		expect(columnCount()).toBe(4);
	});
});

describe("project board with no sessions", () => {
	it("shows the task invitation instead of empty columns", async () => {
		respondWith([project], []);
		renderBoard(<SessionsBoard projectId="proj-1" />);

		expect(await screen.findByText("No worker sessions yet")).toBeInTheDocument();
		// Board header + empty state each offer the pair; the orchestrator is primary in both.
		expect(screen.getAllByRole("button", { name: "Spawn Orchestrator" }).length).toBeGreaterThan(0);
		expect(screen.getAllByRole("button", { name: "New task" }).length).toBeGreaterThan(0);
		expect(screen.queryByText("Add a project")).not.toBeInTheDocument();
		expect(columnCount()).toBe(0);
	});

	it("surfaces the daemon error when spawning the orchestrator fails", async () => {
		respondWith([project], []);
		spawnOrchestratorMock.mockRejectedValue(new Error("branch is already checked out in another worktree"));
		renderBoard(<SessionsBoard projectId="proj-1" />);

		await screen.findByText("No worker sessions yet");
		const [spawnButton] = screen.getAllByRole("button", { name: "Spawn Orchestrator" });
		await userEvent.click(spawnButton);

		expect(await screen.findByText(/branch is already checked out/)).toBeInTheDocument();
	});

	it("offers an explicit Terminal UI fallback when Chat preflight fails", async () => {
		respondWith([project], []);
		const preflightError = Object.assign(new Error("Claude Code is unavailable"), {
			code: "CHAT_DRIVER_UNAVAILABLE",
		});
		spawnOrchestratorMock.mockRejectedValueOnce(preflightError).mockResolvedValueOnce("proj-1-orchestrator");
		renderBoard(<SessionsBoard projectId="proj-1" />);

		await screen.findByText("No worker sessions yet");
		const [spawnButton] = screen.getAllByRole("button", { name: "Spawn Orchestrator" });
		await userEvent.click(spawnButton);
		await userEvent.click(await screen.findByRole("button", { name: "Create as Terminal UI" }));

		expect(spawnOrchestratorMock).toHaveBeenNthCalledWith(1, "proj-1", "board", false, undefined);
		expect(spawnOrchestratorMock).toHaveBeenNthCalledWith(2, "proj-1", "board", false, "tui");
	});

	it("opens project settings instead of spawning when no orchestrator agent is configured", async () => {
		const unconfiguredProject = { ...project, orchestratorAgent: undefined };
		respondWith([unconfiguredProject], []);
		renderBoard(<SessionsBoard projectId="proj-1" />);

		await screen.findByText("No worker sessions yet");
		const [spawnButton] = screen.getAllByRole("button", { name: "Spawn Orchestrator" });
		await userEvent.click(spawnButton);

		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1" });
		expect(navigateMock).not.toHaveBeenCalled();
		expect(spawnOrchestratorMock).not.toHaveBeenCalled();
	});

	it("shows the project creation startup error after navigating to the project board", async () => {
		respondWith([project], []);
		useUiStore
			.getState()
			.setOrchestratorStartupError(
				"proj-1",
				"Project added, but orchestrator did not start: branch is already checked out in another worktree",
			);
		renderBoard(<SessionsBoard projectId="proj-1" />);

		expect(await screen.findByText(/Project added, but orchestrator did not start/)).toBeInTheDocument();
		expect(screen.getByText(/branch is already checked out/)).toBeInTheDocument();
	});

	it("shows a provisioning banner and gates actions while the orchestrator starts in the background", async () => {
		respondWith([project], []);
		useUiStore.getState().setProjectProvisioning("proj-1", true);
		renderBoard(<SessionsBoard projectId="proj-1" />);

		expect(await screen.findByRole("status")).toHaveTextContent(/Setting up the project/);
		for (const button of screen.getAllByRole("button", { name: "Spawn Orchestrator" })) {
			expect(button).toBeDisabled();
		}
		useUiStore.getState().setProjectProvisioning("proj-1", false);
		await waitFor(() => expect(screen.queryByRole("status")).not.toBeInTheDocument());
	});

	it("clears the project creation startup error when retrying orchestrator spawn", async () => {
		respondWith([project], []);
		useUiStore
			.getState()
			.setOrchestratorStartupError(
				"proj-1",
				"Project added, but orchestrator did not start: branch is already checked out in another worktree",
			);
		spawnOrchestratorMock.mockResolvedValue("proj-1-orchestrator");
		renderBoard(<SessionsBoard projectId="proj-1" />);

		await screen.findByText(/Project added, but orchestrator did not start/);
		const [spawnButton] = screen.getAllByRole("button", { name: "Spawn Orchestrator" });
		await userEvent.click(spawnButton);

		await waitFor(() =>
			expect(screen.queryByText(/Project added, but orchestrator did not start/)).not.toBeInTheDocument(),
		);
		expect(useUiStore.getState().orchestratorStartupErrors["proj-1"]).toBeUndefined();
	});

	it("clears a project creation startup error when switching projects", async () => {
		const otherProject: Project = { id: "proj-2", name: "other-app", path: "/repo/other-app" };
		respondWith([project, otherProject], []);
		useUiStore
			.getState()
			.setOrchestratorStartupError(
				"proj-1",
				"Project added, but orchestrator did not start: branch is already checked out in another worktree",
			);
		const { rerender } = renderBoard(<SessionsBoard projectId="proj-1" />);

		await screen.findByText(/Project added, but orchestrator did not start/);
		rerender(
			<QueryClientProvider client={lastQueryClient!}>
				<ShellProvider value={lastShell!}>
					<SessionsBoard projectId="proj-2" />
				</ShellProvider>
			</QueryClientProvider>,
		);

		await screen.findByText("No worker sessions yet");
		await waitFor(() => expect(useUiStore.getState().orchestratorStartupErrors["proj-1"]).toBeUndefined());
		expect(screen.queryByText(/Project added, but orchestrator did not start/)).not.toBeInTheDocument();
	});

	it("clears a project creation startup error once an orchestrator exists", async () => {
		respondWith([project], [orchestratorSession]);
		useUiStore
			.getState()
			.setOrchestratorStartupError(
				"proj-1",
				"Project added, but orchestrator did not start: branch is already checked out in another worktree",
			);
		renderBoard(<SessionsBoard projectId="proj-1" />);

		await screen.findByText("No worker sessions yet");
		await waitFor(() => expect(useUiStore.getState().orchestratorStartupErrors["proj-1"]).toBeUndefined());
		expect(screen.queryByText(/Project added, but orchestrator did not start/)).not.toBeInTheDocument();
	});

	it("clears a stale spawn error when switching projects", async () => {
		const otherProject: Project = { id: "proj-2", name: "other-app", path: "/repo/other-app" };
		respondWith([project, otherProject], []);
		spawnOrchestratorMock.mockRejectedValue(new Error("branch is already checked out in another worktree"));
		const { rerender } = renderBoard(<SessionsBoard projectId="proj-1" />);

		await screen.findByText("No worker sessions yet");
		const [spawnButton] = screen.getAllByRole("button", { name: "Spawn Orchestrator" });
		await userEvent.click(spawnButton);
		await screen.findByText(/branch is already checked out/);

		rerender(
			<QueryClientProvider client={lastQueryClient!}>
				<ShellProvider value={lastShell!}>
					<SessionsBoard projectId="proj-2" />
				</ShellProvider>
			</QueryClientProvider>,
		);
		await screen.findByText("No worker sessions yet");
		expect(screen.queryByText(/branch is already checked out/)).not.toBeInTheDocument();
	});

	it("keeps the columns once the project has a session", async () => {
		respondWith([project], [workerSession]);
		renderBoard(<SessionsBoard projectId="proj-1" />);

		expect(await screen.findByText("fix the bug")).toBeInTheDocument();
		expect(screen.queryByText("No worker sessions yet")).not.toBeInTheDocument();
		expect(columnCount()).toBe(4);
	});
});
