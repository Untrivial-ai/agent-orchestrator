import { SidebarProvider } from "@/components/ui/sidebar";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// Disable motion animations so AnimatePresence unmounts children immediately
// (no exit-animation timer keeps them alive after conditional removal).
vi.mock("motion/react", async (importOriginal) => {
	const actual = await importOriginal<typeof import("motion/react")>();
	return {
		...actual,
		AnimatePresence: ({ children }: { children: React.ReactNode }) => children,
	};
});
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	Sidebar,
	SIDEBAR_DEFAULT_WIDTH,
	SIDEBAR_MIN_WIDTH,
} from "./Sidebar";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import { agentsQueryKey } from "../hooks/useAgentsQuery";
import { useUiStore } from "../stores/ui-store";

const { getMock, navigateMock, mockParams, renameSessionMock, spawnMock, updateStatusMock, commandPaletteEnabled } = vi.hoisted(
	() => ({
		getMock: vi.fn(),
		navigateMock: vi.fn(),
		mockParams: { projectId: undefined as string | undefined, sessionId: undefined as string | undefined },
		renameSessionMock: vi.fn().mockResolvedValue(undefined),
		spawnMock: vi.fn(),
		updateStatusMock: vi.fn(),
		commandPaletteEnabled: { current: true },
	}),
);

vi.mock("../lib/rename-session", () => ({ renameSession: renameSessionMock }));
vi.mock("../lib/spawn-orchestrator", () => ({ spawnOrchestrator: spawnMock }));

vi.mock("../hooks/useCommandPaletteEnabled", () => ({
	useCommandPaletteEnabled: () => commandPaletteEnabled.current,
}));

vi.mock("@tanstack/react-router", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@tanstack/react-router")>();
	return {
		...actual,
		useNavigate: () => navigateMock,
		useParams: () => ({ ...mockParams }),
		useRouterState: ({ select }: { select: (state: { location: { pathname: string } }) => unknown }) =>
			select({ location: { pathname: "/" } }),
	};
});

vi.mock("../lib/bridge", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../lib/bridge")>();
	return {
		aoBridge: {
			...actual.aoBridge,
			updates: { ...actual.aoBridge.updates, getStatus: updateStatusMock },
		},
	};
});

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	apiErrorMessage: (error: unknown) => {
		if (error instanceof Error) return error.message;
		if (typeof error === "object" && error !== null && "message" in error && typeof error.message === "string") {
			return error.message;
		}
		return "Request failed";
	},
}));

const workspace: WorkspaceSummary = {
	id: "proj-1",
	name: "Project One",
	path: "/repo/project-one",
	orchestratorAgent: "claude-code",
	sessions: [],
};

const session: WorkspaceSession = {
	id: "proj-1-1",
	workspaceId: "proj-1",
	workspaceName: "Project One",
	title: "fix login",
	provider: "claude-code",
	kind: "worker",
	branch: "session/proj-1-1",
	status: "working",
	updatedAt: "2026-06-30T00:00:00Z",
	prs: [],
};

function activeAgentSwitch(
	overrides: Partial<NonNullable<WorkspaceSession["activeAgentSwitch"]>> = {},
): NonNullable<WorkspaceSession["activeAgentSwitch"]> {
	return {
		agentHandoffStatus: "received",
		fromHarness: "claude-code",
		id: "switch-1",
		state: "starting_target",
		targetHarness: "codex",
		...overrides,
	};
}

function sidebarPR(overrides: Partial<WorkspaceSession["prs"][number]> = {}): WorkspaceSession["prs"][number] {
	return {
		url: "https://github.com/acme/project-one/pull/7",
		number: 7,
		state: "open",
		ci: "unknown",
		review: "none",
		mergeability: "unknown",
		reviewComments: false,
		updatedAt: "2026-06-30T00:00:00Z",
		...overrides,
	};
}

type CreateProjectInput = {
	path: string;
	workerAgent: string;
	orchestratorAgent: string;
	trackerIntake?: unknown;
	asWorkspace?: boolean;
};
type CreateProjectHandler = (input: CreateProjectInput) => Promise<void>;
type InitializeProjectHandler = (path: string) => Promise<void>;
type RemoveProjectHandler = (projectId: string) => Promise<void>;

function renderSidebar({
	onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler,
	onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler,
	onRemoveProject = vi.fn().mockResolvedValue(undefined) as RemoveProjectHandler,
	seedAgents = true,
	workspaces = [workspace],
	initialOpen = true,
}: {
	onCreateProject?: CreateProjectHandler;
	onInitializeProject?: InitializeProjectHandler;
	onRemoveProject?: RemoveProjectHandler;
	seedAgents?: boolean;
	workspaces?: WorkspaceSummary[];
	initialOpen?: boolean;
} = {}) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	if (seedAgents) {
		queryClient.setQueryData(agentsQueryKey, {
			supported: [
				{ id: "claude-code", label: "Claude Code" },
				{ id: "codex", label: "Codex" },
			],
			installed: [
				{ id: "claude-code", label: "Claude Code" },
				{ id: "codex", label: "Codex" },
			],
			authorized: [
				{ id: "claude-code", label: "Claude Code", authStatus: "authorized" },
				{ id: "codex", label: "Codex", authStatus: "authorized" },
			],
		});
	}
	render(
		<QueryClientProvider client={queryClient}>
			<SidebarProvider defaultOpen={initialOpen}>
				<Sidebar
					onCreateProject={onCreateProject}
					onInitializeProject={onInitializeProject}
					onRemoveProject={onRemoveProject}
					workspaces={workspaces}
				/>
			</SidebarProvider>
		</QueryClientProvider>,
	);
	return onRemoveProject;
}

/** Projects render collapsed; open one to list all of its sessions. */

async function chooseOption(trigger: HTMLElement, optionName: string) {
	await userEvent.click(trigger);
	await userEvent.click(await screen.findByRole("option", { name: optionName }));
}

function codedError(message: string, code: "NOT_A_GIT_REPO" | "PROJECT_UNBORN") {
	const error = new Error(message) as Error & { code: string };
	error.code = code;
	return error;
}

async function openCreateProjectDialog(
	path = "/repo/new-project",
	scan: {
		path: string;
		repos: Array<{
			name: string;
			path: string;
			relativePath: string;
			branch: string;
			remote: string;
			hasRemote: boolean;
			status?: "ok" | "error";
			reason?: string;
			needsGitInit?: boolean;
		}>;
	} = {
		path,
		repos: [
			{ name: "project", path, relativePath: ".", branch: "main", remote: "origin", hasRemote: true, status: "ok" },
		],
	},
) {
	const user = userEvent.setup();
	window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue(path);
	window.ao!.app.scanImportFolder = vi.fn().mockResolvedValue(scan);
	await user.click(screen.getByLabelText("New project"));
	await user.click(screen.getByRole("button", { name: /^Project/i }));
	await screen.findByText(path);
	await chooseOption(screen.getByRole("combobox", { name: "Worker agent" }), "Codex");
	await chooseOption(screen.getByRole("combobox", { name: "Orchestrator agent" }), "Claude Code");
	return user;
}

beforeEach(() => {
	window.localStorage.clear();
	document.documentElement.style.removeProperty("--ao-sidebar-w");
	commandPaletteEnabled.current = true;
	useUiStore.setState({ isCommandPaletteOpen: false, settingsModal: null });
	getMock.mockReset();
	getMock.mockResolvedValue({
		data: {
			supported: [
				{ id: "claude-code", label: "Claude Code" },
				{ id: "codex", label: "Codex" },
			],
			installed: [
				{ id: "claude-code", label: "Claude Code" },
				{ id: "codex", label: "Codex" },
			],
			authorized: [
				{ id: "claude-code", label: "Claude Code", authStatus: "authorized" },
				{ id: "codex", label: "Codex", authStatus: "authorized" },
			],
		},
		error: undefined,
	});
	navigateMock.mockReset();
	renameSessionMock.mockReset().mockResolvedValue(undefined);
	spawnMock.mockReset();
	updateStatusMock.mockReset().mockResolvedValue({ state: "idle" });
	mockParams.projectId = undefined;
	mockParams.sessionId = undefined;
});

afterEach(() => {
	vi.restoreAllMocks();
});

describe("Sidebar", () => {
	it("suppresses focus chrome without removing keyboard focusability", () => {
		renderSidebar();

		expect(document.querySelector('[data-slot="sidebar-container"]')).toHaveClass("sidebar-focusless");
		expect(screen.getAllByRole("button", { name: "Settings" })[0]).toHaveAttribute("tabindex", "0");
	});

	it("aligns the Settings footer hairline and row height with the board Archive bar", () => {
		renderSidebar();

		const footer = document.querySelector('[data-sidebar="footer"]');
		expect(footer).toHaveClass("border-t", "border-border-strong", "!py-2");
		expect(screen.getAllByRole("button", { name: "Settings" })[0]).toHaveClass("h-[42px]");
		// Windowed: lift the hairline by the framed panel inset + 1px surface
		// border. macOS also collapses that inset in native fullscreen.
		if (footer?.className.includes("--size-center-panel-inset-mac")) {
			expect(footer).toHaveClass(
				"mb-[calc(var(--size-center-panel-inset-mac)+1px)]",
				"in-[.native-fullscreen]:mb-px",
			);
		} else {
			expect(footer).toHaveClass("mb-[calc(var(--size-center-panel-bottom-inset)+1px)]");
		}
	});

	it("keeps only the expanded Settings control keyboard-accessible while expanded", () => {
		renderSidebar();

		const settingsButtons = Array.from(document.querySelectorAll<HTMLButtonElement>('button[aria-label="Settings"]'));
		const expandedButton = settingsButtons.find((button) => button.textContent?.includes("Settings"));
		const collapsedButton = settingsButtons.find((button) => !button.textContent?.includes("Settings"));

		expect(settingsButtons).toHaveLength(2);
		expect(expandedButton).toHaveAttribute("tabindex", "0");
		expect(expandedButton?.parentElement).not.toHaveAttribute("aria-hidden");
		expect(collapsedButton).toHaveAttribute("tabindex", "-1");
		expect(collapsedButton?.closest('[aria-hidden="true"]')).toBeInTheDocument();
	});

	it("keeps only the collapsed Settings control keyboard-accessible while collapsed", () => {
		renderSidebar({ initialOpen: false });

		const settingsButtons = Array.from(document.querySelectorAll<HTMLButtonElement>('button[aria-label="Settings"]'));
		const expandedButton = settingsButtons.find((button) => button.textContent?.includes("Settings"));
		const collapsedButton = settingsButtons.find((button) => !button.textContent?.includes("Settings"));

		expect(settingsButtons).toHaveLength(2);
		expect(expandedButton).toHaveAttribute("tabindex", "-1");
		expect(expandedButton?.closest('[aria-hidden="true"]')).toBeInTheDocument();
		expect(collapsedButton).toHaveAttribute("tabindex", "0");
		expect(collapsedButton?.closest('[aria-hidden="true"]')).toBeNull();
	});

	it("keeps sidebar scrolling functional with overflow-y-auto", () => {
		renderSidebar();

		const content = document.querySelector('[data-sidebar="content"]');
		expect(content).toHaveClass("overflow-y-auto", "project-sidebar-scrollbar");
		expect(content).not.toHaveClass("scrollbar-none");
		expect(content).not.toContainElement(screen.getByText("Projects"));
	});

	it("opens project settings instead of spawning when no orchestrator agent is configured", async () => {
		const user = userEvent.setup();
		renderSidebar({ workspaces: [{ ...workspace, orchestratorAgent: undefined }] });

		await user.click(screen.getByRole("button", { name: "Spawn Project One orchestrator" }));

		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1" });
		expect(navigateMock).not.toHaveBeenCalled();
		expect(spawnMock).not.toHaveBeenCalled();
	});

	it("shows a ConfirmDialog and calls onRemoveProject when confirmed", async () => {
		const user = userEvent.setup();
		const onRemoveProject = renderSidebar();

		await user.click(screen.getByLabelText("Project actions for Project One"));
		await user.click(await screen.findByRole("menuitem", { name: "Remove project" }));

		// The ConfirmDialog renders via Radix Portal — find it by role
		const dialog = await screen.findByRole("dialog", { name: "Remove project" });
		expect(dialog).toBeInTheDocument();
		expect(dialog).toHaveTextContent("Project One");

		await user.click(screen.getByRole("button", { name: "Remove" }));
		await waitFor(() => expect(onRemoveProject).toHaveBeenCalledTimes(1));
		expect(navigateMock).toHaveBeenCalledWith({ to: "/" });
	});

	it("dismisses project removal immediately and shows progress outside the modal", async () => {
		let finishRemoval!: () => void;
		const onRemoveProject = vi.fn(
			() =>
				new Promise<void>((resolve) => {
					finishRemoval = resolve;
				}),
		) as RemoveProjectHandler;
		const user = userEvent.setup();
		renderSidebar({ onRemoveProject });

		await user.click(screen.getByLabelText("Project actions for Project One"));
		await user.click(await screen.findByRole("menuitem", { name: "Remove project" }));
		await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove" }));

		expect(screen.queryByRole("dialog", { name: "Remove project" })).not.toBeInTheDocument();
		expect(navigateMock).toHaveBeenCalledWith({ to: "/" });
		expect(screen.getByRole("status")).toHaveTextContent("Removing Project One");

		finishRemoval();
		await waitFor(() => expect(screen.queryByRole("status")).not.toBeInTheDocument());
	});

	it("does not remove the project when cancellation is clicked in the ConfirmDialog", async () => {
		const user = userEvent.setup();
		const onRemoveProject = renderSidebar();

		await user.click(screen.getByLabelText("Project actions for Project One"));
		await user.click(await screen.findByRole("menuitem", { name: "Remove project" }));

		await screen.findByRole("dialog", { name: "Remove project" });
		await user.click(screen.getByRole("button", { name: "Cancel" }));

		// Dialog should close and the handler must not have fired
		await waitFor(() => expect(screen.queryByRole("dialog", { name: "Remove project" })).not.toBeInTheDocument());
		expect(onRemoveProject).not.toHaveBeenCalled();
	});

	it("keeps the removal dialog dismissed and surfaces failures in the sidebar", async () => {
		const user = userEvent.setup();
		const onRemoveProject = vi
			.fn()
			.mockRejectedValueOnce(new Error("Failed to remove project")) as RemoveProjectHandler;
		renderSidebar({ onRemoveProject });

		await user.click(screen.getByLabelText("Project actions for Project One"));
		await user.click(await screen.findByRole("menuitem", { name: "Remove project" }));
		await screen.findByRole("dialog", { name: "Remove project" });
		await user.click(screen.getByRole("button", { name: "Remove" }));

		expect(await screen.findByText("Failed to remove project")).toBeInTheDocument();
		expect(screen.queryByRole("dialog", { name: "Remove project" })).not.toBeInTheDocument();
		expect(navigateMock).toHaveBeenCalledWith({ to: "/" });
	});

	it("requests a new task for the project from the kebab menu", async () => {
		const user = userEvent.setup();
		renderSidebar();
		const before = useUiStore.getState().newTaskRequest?.nonce ?? 0;

		await user.click(screen.getByLabelText("Project actions for Project One"));
		await user.click(await screen.findByRole("menuitem", { name: /New session/ }));

		const request = useUiStore.getState().newTaskRequest;
		expect(request?.projectId).toBe("proj-1");
		expect(request?.nonce ?? 0).toBeGreaterThan(before);
	});

	it("opens the create-project flow when the no-project shortcut signal arrives", async () => {
		renderSidebar();

		act(() => {
			useUiStore.getState().requestCreateProject();
		});

		expect(await screen.findByRole("dialog", { name: "Import to Agent Orchestrator" })).toBeInTheDocument();
	});

	it("keeps the create-project shortcut available when there are no projects", async () => {
		renderSidebar({ workspaces: [] });

		act(() => {
			useUiStore.getState().requestCreateProject();
		});

		expect(await screen.findByRole("dialog", { name: "Import to Agent Orchestrator" })).toBeInTheDocument();
	});

	it("reveals orchestrator and kebab buttons on the project row (no dashboard button)", () => {
		renderSidebar();

		expect(screen.queryByLabelText("Open Project One dashboard")).not.toBeInTheDocument();
		expect(screen.getByLabelText("Spawn Project One orchestrator")).toBeInTheDocument();
		expect(screen.getByLabelText("Project actions for Project One")).toBeInTheDocument();
	});

	it("toggles project sessions from the folder icon without selecting the project first", async () => {
		const user = userEvent.setup();
		const other: WorkspaceSummary = {
			id: "proj-2",
			name: "Project Two",
			path: "/repo/project-two",
			orchestratorAgent: "claude-code",
			sessions: [{ ...session, id: "proj-2-1", workspaceId: "proj-2", workspaceName: "Project Two", title: "other task" }],
		};
		renderSidebar({
			workspaces: [{ ...workspace, sessions: [session] }, other],
		});

		expect(screen.getByText("fix login")).toBeInTheDocument();
		expect(screen.getByText("other task")).toBeInTheDocument();

		const folder = screen.getByRole("button", { name: "Toggle Project Two sessions" });
		expect(folder).toBeTruthy();
		await user.click(folder);

		expect(screen.queryByText("other task")).not.toBeInTheDocument();
		expect(screen.getByText("fix login")).toBeInTheDocument();
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("lists worker sessions by last activity, newest first", () => {
		const oldest: WorkspaceSession = {
			...session,
			id: "proj-1-old",
			title: "old task",
			createdAt: "2026-06-29T00:00:00Z",
			updatedAt: "2026-07-02T00:00:00Z",
			activity: { state: "idle", lastActivityAt: "2026-07-01T00:00:00Z" },
		};
		const newest: WorkspaceSession = {
			...session,
			id: "proj-1-new",
			title: "new task",
			createdAt: "2026-07-01T00:00:00Z",
			updatedAt: "2026-07-01T00:00:00Z",
			activity: { state: "active", lastActivityAt: "2026-07-02T00:00:00Z" },
		};
		const noActivity: WorkspaceSession = {
			...session,
			id: "proj-1-no-activity",
			title: "no activity",
			createdAt: "2026-06-29T00:00:00Z",
			updatedAt: "2026-07-03T00:00:00Z",
		};
		const invalidActivity: WorkspaceSession = {
			...session,
			id: "proj-1-invalid-activity",
			title: "invalid activity",
			createdAt: "2026-06-29T00:00:00Z",
			updatedAt: "2026-07-04T00:00:00Z",
			activity: { state: "idle", lastActivityAt: "not-a-timestamp" },
		};
		const createdFallback: WorkspaceSession = {
			...session,
			id: "proj-1-created-fallback",
			title: "created fallback",
			createdAt: "2026-07-05T00:00:00Z",
			updatedAt: "not-a-timestamp",
			activity: { state: "idle", lastActivityAt: "also-not-a-timestamp" },
		};
		renderSidebar({ workspaces: [{ ...workspace, sessions: [oldest, newest, noActivity, invalidActivity, createdFallback] }] });

		const sessionButtons = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-session-row] button[aria-label^="Open "]'));
		expect(sessionButtons.map((button) => button.getAttribute("aria-label"))).toEqual([
			"Open created fallback",
			"Open invalid activity",
			"Open no activity",
			"Open new task",
			"Open old task",
		]);
	});

	it("navigates to the project board when the project row button is clicked", async () => {
		const user = userEvent.setup();
		renderSidebar();

		// Click the project name text — it's inside SidebarMenuButton and bubbles up to onProjectClick.
		await user.click(screen.getByText("Project One"));

		expect(navigateMock).toHaveBeenCalledWith({ to: "/projects/$projectId", params: { projectId: "proj-1" } });
	});

	it("returns to the project board from an orchestrator session without collapsing", async () => {
		const user = userEvent.setup();
		const orchestrator: WorkspaceSession = {
			...session,
			id: "proj-1-orc",
			title: "Orchestrator",
			kind: "orchestrator",
		};
		mockParams.projectId = "proj-1";
		mockParams.sessionId = "proj-1-orc";
		renderSidebar({
			workspaces: [{ ...workspace, sessions: [orchestrator, session] }],
		});

		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();

		await user.click(screen.getByText("Project One"));

		expect(navigateMock).toHaveBeenCalledWith({ to: "/projects/$projectId", params: { projectId: "proj-1" } });
		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();
		expect(screen.getByText("Project One").closest("button")).toHaveAttribute("aria-expanded", "true");
	});

	it("collapses an expanded project when its board is already active", async () => {
		const user = userEvent.setup();
		mockParams.projectId = "proj-1";
		mockParams.sessionId = undefined;
		renderSidebar({
			workspaces: [{ ...workspace, sessions: [session] }],
		});

		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();

		await user.click(screen.getByText("Project One"));

		expect(navigateMock).not.toHaveBeenCalled();
		expect(screen.queryByLabelText("Open fix login")).not.toBeInTheDocument();
		expect(screen.getByText("Project One").closest("button")).toHaveAttribute("aria-expanded", "false");
	});

	it("expands a collapsed project when opening its orchestrator", async () => {
		const user = userEvent.setup();
		const orchestrator: WorkspaceSession = {
			...session,
			id: "proj-1-orc",
			title: "Orchestrator",
			kind: "orchestrator",
		};
		renderSidebar({
			workspaces: [{ ...workspace, sessions: [orchestrator, session] }],
		});

		await user.click(screen.getByRole("button", { name: "Toggle Project One sessions" }));
		expect(screen.queryByLabelText("Open fix login")).not.toBeInTheDocument();
		expect(screen.getByText("Project One").closest("button")).toHaveAttribute("aria-expanded", "false");

		await user.click(screen.getByRole("button", { name: "Open Project One orchestrator" }));

		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "proj-1-orc" },
		});
		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();
		expect(screen.getByText("Project One").closest("button")).toHaveAttribute("aria-expanded", "true");
	});

	it("defaults worker and orchestrator agents when creating a project", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/new-project");
		renderSidebar({ onCreateProject });

		await user.click(screen.getByLabelText("New project"));
		expect(screen.getByRole("dialog", { name: "Import to Agent Orchestrator" })).toBeInTheDocument();
		expect(window.ao!.app.chooseDirectory).not.toHaveBeenCalled();
		await user.click(screen.getByRole("button", { name: /^Project/i }));

		expect(await screen.findByText("/repo/new-project")).toBeInTheDocument();
		expect(window.ao!.app.chooseDirectory).toHaveBeenCalledWith("Choose a project repository");
		const dialog = screen.getByRole("dialog", { name: "Project agents" });
		expect(dialog).toHaveClass("left-1/2", "top-1/2", "-translate-x-1/2", "-translate-y-1/2");
		await user.click(screen.getByRole("button", { name: "Create and start" }));

		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith(
				expect.objectContaining({
					path: "/repo/new-project",
					workerAgent: "claude-code",
					orchestratorAgent: "claude-code",
				}),
			),
		);
	});

	it("prioritizes authorized project agents by preferred agent order", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/new-project");
		getMock.mockResolvedValueOnce({
			data: {
				supported: [
					{ id: "goose", label: "Goose" },
					{ id: "devin", label: "Devin" },
					{ id: "aider", label: "Aider" },
					{ id: "opencode", label: "OpenCode" },
					{ id: "cursor", label: "Cursor" },
				],
				installed: [
					{ id: "goose", label: "Goose", authStatus: "authorized" },
					{ id: "devin", label: "Devin", authStatus: "authorized" },
					{ id: "aider", label: "Aider", authStatus: "authorized" },
					{ id: "opencode", label: "OpenCode", authStatus: "authorized" },
					{ id: "cursor", label: "Cursor", authStatus: "authorized" },
				],
				authorized: [
					{ id: "goose", label: "Goose", authStatus: "authorized" },
					{ id: "devin", label: "Devin", authStatus: "authorized" },
					{ id: "aider", label: "Aider", authStatus: "authorized" },
					{ id: "opencode", label: "OpenCode", authStatus: "authorized" },
					{ id: "cursor", label: "Cursor", authStatus: "authorized" },
				],
			},
			error: undefined,
		});
		renderSidebar({ onCreateProject, seedAgents: false });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Project/i }));
		expect(await screen.findByText("/repo/new-project")).toBeInTheDocument();
		expect(screen.getByRole("combobox", { name: "Worker agent" })).toHaveTextContent(/cursor/i);
		expect(screen.getByRole("combobox", { name: "Orchestrator agent" })).toHaveTextContent(/cursor/i);

		await user.click(screen.getByRole("combobox", { name: "Worker agent" }));
		expect((await screen.findAllByRole("option")).map((option) => option.textContent)).toEqual([
			"Cursor",
			"OpenCode",
			"Aider",
			"Devin",
			"Goose",
		]);
		await user.keyboard("{Escape}");

		await user.click(screen.getByRole("button", { name: "Create and start" }));
		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith(
				expect.objectContaining({
					workerAgent: "cursor",
					orchestratorAgent: "cursor",
				}),
			),
		);
	});

	it("explains Git setup before creating a non-git project", async () => {
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		renderSidebar({ onCreateProject, onInitializeProject });
		const user = await openCreateProjectDialog("/repo/new-project", { path: "/repo/new-project", repos: [] });

		expect(await screen.findByText(/If this folder needs Git setup/i)).toBeInTheDocument();
		expect(onInitializeProject).not.toHaveBeenCalled();
		await user.click(screen.getByRole("button", { name: "Create and start" }));
		await waitFor(() => expect(onInitializeProject).toHaveBeenCalledWith("/repo/new-project"));
		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
	});

	it("warns before initializing a plain project folder nested inside a parent repo", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/parent/universe");
		window.ao!.app.scanImportFolder = vi.fn().mockResolvedValue({
			path: "/repo/parent/universe",
			repos: [],
			setupWarning:
				"Selected folder is inside an existing Git repository at /repo/parent. AO will initialize this folder as a separate repository.",
		});
		renderSidebar({ onCreateProject, onInitializeProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Project/i }));

		expect(await screen.findByRole("dialog", { name: "Project agents" })).toBeInTheDocument();
		expect(screen.getByText(/If this folder needs Git setup/i)).toBeInTheDocument();
		expect(screen.getByText(/inside an existing Git repository at \/repo\/parent/i)).toBeInTheDocument();
		expect(onInitializeProject).not.toHaveBeenCalled();
		expect(onCreateProject).not.toHaveBeenCalled();

		await user.click(screen.getByRole("button", { name: "Create and start" }));
		await waitFor(() => expect(onInitializeProject).toHaveBeenCalledWith("/repo/parent/universe"));
		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
	});

	it("shows repository initialization recovery for git repos with no commits", async () => {
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		renderSidebar({ onCreateProject, onInitializeProject });
		const user = await openCreateProjectDialog("/repo/unborn", {
			path: "/repo/unborn",
			repos: [
				{
					name: "unborn",
					path: "/repo/unborn",
					relativePath: ".",
					branch: "HEAD",
					remote: "",
					hasRemote: false,
					status: "error",
					reason: "Repository must have at least one commit.",
				},
			],
		});
		expect(await screen.findByText(/If this folder needs Git setup/i)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create and start" }));
		await waitFor(() => expect(onInitializeProject).toHaveBeenCalledWith("/repo/unborn"));
		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
	});

	it("does not initialize Git when the project creation is cancelled", async () => {
		const onCreateProject = vi
			.fn()
			.mockRejectedValueOnce(
				codedError("This folder is not a Git repository.", "NOT_A_GIT_REPO"),
			) as unknown as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		renderSidebar({ onCreateProject, onInitializeProject });
		const user = await openCreateProjectDialog("/repo/new-project", { path: "/repo/new-project", repos: [] });
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onInitializeProject).not.toHaveBeenCalled();
		expect(screen.queryByRole("dialog", { name: "Project agents" })).not.toBeInTheDocument();
	});

	it("surfaces repository initialization failures", async () => {
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockRejectedValue(new Error("git init failed")) as InitializeProjectHandler;
		renderSidebar({ onCreateProject, onInitializeProject });
		const user = await openCreateProjectDialog("/repo/new-project", { path: "/repo/new-project", repos: [] });
		await user.click(screen.getByRole("button", { name: "Create and start" }));
		await waitFor(() => expect(onInitializeProject).toHaveBeenCalledWith("/repo/new-project"));
		expect(onCreateProject).not.toHaveBeenCalled();
	});

	it("can create a workspace project from the project add flow", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/workspace");
		renderSidebar({ onCreateProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Workspace/i }));

		expect(await screen.findByText("/repo/workspace")).toBeInTheDocument();
		expect(window.ao!.app.chooseDirectory).toHaveBeenCalledWith("Choose a workspace folder");
		expect(screen.getByRole("dialog", { name: "Workspace agents" })).toBeInTheDocument();
		await chooseOption(screen.getByRole("combobox", { name: "Worker agent" }), "Codex");
		await chooseOption(screen.getByRole("combobox", { name: "Orchestrator agent" }), "Claude Code");
		await user.click(screen.getByRole("button", { name: "Create workspace and start" }));

		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith({
				path: "/repo/workspace",
				workerAgent: "codex",
				orchestratorAgent: "claude-code",
				asWorkspace: true,
			}),
		);
	});

	it("does not run single-repo Git setup recovery for workspace imports", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi
			.fn()
			.mockRejectedValueOnce(
				codedError("This folder is not a Git repository.", "NOT_A_GIT_REPO"),
			) as unknown as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/workspace");
		window.ao!.app.checkAncestorRepo = vi.fn().mockResolvedValue(undefined);
		window.ao!.app.scanImportFolder = vi.fn().mockResolvedValue({ path: "/repo/workspace", repos: [] });
		renderSidebar({ onCreateProject, onInitializeProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Workspace/i }));
		await screen.findByRole("dialog", { name: "Workspace agents" });
		await chooseOption(screen.getByRole("combobox", { name: "Orchestrator agent" }), "Claude Code");
		await user.click(screen.getByRole("button", { name: "Create workspace and start" }));

		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
		expect(onInitializeProject).not.toHaveBeenCalled();
		expect(await screen.findByText(/Import failed · workspace not registered/i)).toBeInTheDocument();
		expect(screen.getByText("Review the error above or choose a different folder")).toBeInTheDocument();
		expect(window.ao!.app.checkAncestorRepo).toHaveBeenCalledWith("/repo/workspace");
		expect(window.ao!.app.scanImportFolder).toHaveBeenCalledWith({
			path: "/repo/workspace",
			mode: "workspace",
		});
	});

	it("shows detected repository validation when workspace import fails", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockRejectedValue(new Error("workspace not registered")) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/Users/test/dev/acme");
		window.ao!.app.checkAncestorRepo = vi.fn().mockResolvedValue(undefined);
		window.ao!.app.scanImportFolder = vi.fn().mockResolvedValue({
			path: "/Users/test/dev/acme",
			repos: [
				{
					name: "web",
					path: "/Users/test/dev/acme/web",
					relativePath: "web",
					branch: "HEAD",
					remote: "",
					hasRemote: false,
					status: "error",
					reason: "Repository name is reserved by AO.",
				},
				{
					name: "api",
					path: "/Users/test/dev/acme/api",
					relativePath: "api",
					branch: "main",
					remote: "git@github.com:acme/api.git",
					hasRemote: true,
					status: "ok",
				},
			],
		});
		renderSidebar({ onCreateProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Workspace/i }));
		await screen.findByRole("dialog", { name: "Workspace agents" });
		await chooseOption(screen.getByRole("combobox", { name: "Orchestrator agent" }), "Claude Code");
		await user.click(screen.getByRole("button", { name: "Create workspace and start" }));

		expect(await screen.findByText(/Import failed · workspace not registered/i)).toBeInTheDocument();
		expect(screen.getByText("workspace not registered")).toBeInTheDocument();
		expect(screen.getByText("web")).toBeInTheDocument();
		expect(screen.getByText("Repository name is reserved by AO.")).toBeInTheDocument();
		expect(screen.getByText("api")).toBeInTheDocument();
		expect(screen.getByText("main github.com/acme/api")).toBeInTheDocument();
		expect(screen.getByText("Resolve 1 failed repository to continue")).toBeInTheDocument();
		expect(window.ao!.app.checkAncestorRepo).toHaveBeenCalledWith("/Users/test/dev/acme");
		expect(window.ao!.app.scanImportFolder).toHaveBeenCalledWith({
			path: "/Users/test/dev/acme",
			mode: "workspace",
		});
	});

	it("shows non-git child repos as needs git init in the valid list", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockRejectedValue(new Error("workspace not registered")) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/workspace");
		window.ao!.app.checkAncestorRepo = vi.fn().mockResolvedValue(undefined);
		window.ao!.app.scanImportFolder = vi.fn().mockResolvedValue({
			path: "/repo/workspace",
			repos: [
				{
					name: "api",
					path: "/repo/workspace/api",
					relativePath: "api",
					branch: "main",
					remote: "git@github.com:acme/api.git",
					hasRemote: true,
					status: "ok",
				},
				{
					name: "docs",
					path: "/repo/workspace/docs",
					relativePath: "docs",
					branch: "",
					remote: "",
					hasRemote: false,
					status: "ok",
					needsGitInit: true,
				},
			],
		});
		renderSidebar({ onCreateProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Workspace/i }));
		await screen.findByRole("dialog", { name: "Workspace agents" });
		await chooseOption(screen.getByRole("combobox", { name: "Orchestrator agent" }), "Claude Code");
		await user.click(screen.getByRole("button", { name: "Create workspace and start" }));

		expect(await screen.findByText(/Import failed · workspace not registered/i)).toBeInTheDocument();
		expect(screen.getByText("api")).toBeInTheDocument();
		expect(screen.getByText("main github.com/acme/api")).toBeInTheDocument();
		expect(screen.getByText("docs")).toBeInTheDocument();
		expect(screen.getByText("Needs git init")).toBeInTheDocument();
		expect(screen.queryByText(/Origin remote is required/)).not.toBeInTheDocument();
	});

	it("does not rescan folders for non-validation create failures", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockRejectedValue(new Error("AO daemon is not ready.")) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/workspace");
		window.ao!.app.checkAncestorRepo = vi.fn().mockResolvedValue(undefined);
		window.ao!.app.scanImportFolder = vi.fn();
		renderSidebar({ onCreateProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Workspace/i }));
		await screen.findByRole("dialog", { name: "Workspace agents" });
		await chooseOption(screen.getByRole("combobox", { name: "Orchestrator agent" }), "Claude Code");
		await user.click(screen.getByRole("button", { name: "Create workspace and start" }));

		expect(await screen.findByText("AO daemon is not ready.")).toBeInTheDocument();
		// checkAncestorRepo is called once during the preflight (chooseDirectory),
		// but scanImportFolder is never called (shouldScanCreateFailure returns false for this error)
		expect(window.ao!.app.checkAncestorRepo).toHaveBeenCalledWith("/repo/workspace");
		expect(window.ao!.app.scanImportFolder).not.toHaveBeenCalled();
	});

	it("shows ancestor repo warning in agent sheet for workspace inside existing repo", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue({
			data: { project: { id: "ws-1", name: "My Workspace", kind: "workspace", path: "/repo/inner" } },
			error: null,
		}) as unknown as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/inner");
		window.ao!.app.checkAncestorRepo = vi
			.fn()
			.mockResolvedValue(
				"Selected folder is inside an existing Git repository at /repo. AO will initialize this folder as a separate repository.",
			);
		renderSidebar({ onCreateProject, onInitializeProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Workspace/i }));
		await screen.findByRole("dialog", { name: "Workspace agents" });
		expect(
			screen.getByText(
				"Selected folder is inside an existing Git repository at /repo. AO will initialize this folder as a separate repository.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"If this folder needs Git setup, AO will initialize it and create the first commit before starting.",
			),
		).toBeInTheDocument();
		await chooseOption(screen.getByRole("combobox", { name: "Orchestrator agent" }), "Claude Code");
		await user.click(screen.getByRole("button", { name: "Create workspace and start" }));

		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
		expect(onCreateProject).toHaveBeenCalledWith(
			expect.objectContaining({ path: "/repo/inner", asWorkspace: true }),
		);
		expect(onInitializeProject).not.toHaveBeenCalled();
		expect(window.ao!.app.checkAncestorRepo).toHaveBeenCalledWith("/repo/inner");
	});

	it("opens global settings from the footer menu when no project is selected", async () => {
		const user = userEvent.setup();
		renderSidebar();

		await user.click(screen.getByRole("button", { name: /project actions/i }));

		expect(await screen.findByRole("menuitem", { name: /settings/i })).toBeInTheDocument();
	});

	it("shows needs-auth agents as unavailable while keeping authorized agents selectable", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/new-project");
		getMock.mockResolvedValueOnce({
			data: {
				supported: [
					{ id: "claude-code", label: "Claude Code" },
					{ id: "cursor", label: "Cursor" },
					{ id: "aider", label: "Aider" },
				],
				installed: [
					{ id: "claude-code", label: "Claude Code", authStatus: "authorized" },
					{ id: "cursor", label: "Cursor", authStatus: "unauthorized" },
				],
				authorized: [{ id: "claude-code", label: "Claude Code", authStatus: "authorized" }],
			},
			error: undefined,
		});
		renderSidebar({ onCreateProject, seedAgents: false });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Project/i }));
		expect(await screen.findByText("/repo/new-project")).toBeInTheDocument();

		await user.click(screen.getByRole("combobox", { name: "Orchestrator agent" }));
		const options = await screen.findAllByRole("option");
		expect(options.map((option) => option.textContent)).toEqual([
			"Claude Code",
			"CursorNeeds auth",
			"AiderNeeds install",
		]);
		expect(options[1]).toHaveAttribute("aria-disabled", "true");
		expect(options[2]).toHaveAttribute("aria-disabled", "true");
		await user.keyboard("{Escape}");

		await user.click(screen.getByRole("button", { name: "Create and start" }));

		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith(expect.objectContaining({ orchestratorAgent: "claude-code" })),
		);
	});

	it("updates project agent options when the catalog loads after the dialog opens", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/new-project");
		let resolveAgents!: (value: {
			data: {
				supported: { id: string; label: string }[];
				installed: { id: string; label: string }[];
				authorized: { id: string; label: string; authStatus: "authorized" }[];
			};
			error: undefined;
		}) => void;
		getMock.mockReturnValueOnce(
			new Promise((resolve) => {
				resolveAgents = resolve;
			}),
		);
		renderSidebar({ onCreateProject, seedAgents: false });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Project/i }));
		expect(await screen.findByText("/repo/new-project")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Create and start" })).toBeDisabled();

		resolveAgents({
			data: {
				supported: [
					{ id: "claude-code", label: "Claude Code" },
					{ id: "codex", label: "Codex" },
				],
				installed: [
					{ id: "claude-code", label: "Claude Code" },
					{ id: "codex", label: "Codex" },
				],
				authorized: [
					{ id: "claude-code", label: "Claude Code", authStatus: "authorized" },
					{ id: "codex", label: "Codex", authStatus: "authorized" },
				],
			},
			error: undefined,
		});

		await chooseOption(screen.getByRole("combobox", { name: "Orchestrator agent" }), "Claude Code");
		await user.click(screen.getByRole("button", { name: "Create and start" }));

		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith({
				path: "/repo/new-project",
				workerAgent: "claude-code",
				orchestratorAgent: "claude-code",
				trackerIntake: undefined,
				asWorkspace: false,
			}),
		);
	});

	it("opens settings when the footer Settings button is clicked", async () => {
		const user = userEvent.setup();
		renderSidebar();
		await user.click(screen.getAllByRole("button", { name: "Settings" })[0]);
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "global" });
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("opens the command palette when Search is clicked", async () => {
		const user = userEvent.setup();
		renderSidebar();
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(false);
		await user.click(screen.getByRole("button", { name: /Search/ }));
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(true);
		expect(screen.getByRole("button", { name: /Search/ })).toHaveTextContent(/(?:⌘ |Ctrl\+)K/);
	});

	it("defers opening the palette until the Search click has been dispatched", async () => {
		renderSidebar();
		fireEvent.click(screen.getByRole("button", { name: /Search/ }));
		// Still closed inside the click's task: the palette dialog must not mount
		// while the pointer sequence that opened it is still being handled.
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(false);
		await act(async () => {});
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(true);
	});

	it("hides Search when the command palette feature is disabled", () => {
		commandPaletteEnabled.current = false;
		renderSidebar();
		expect(screen.queryByRole("button", { name: /Search/ })).not.toBeInTheDocument();
	});

	it("shows the project name and context in the ConfirmDialog description", async () => {
		const user = userEvent.setup();
		renderSidebar();

		await user.click(screen.getByLabelText("Project actions for Project One"));
		await user.click(await screen.findByRole("menuitem", { name: "Remove project" }));

		const dialog = await screen.findByRole("dialog", { name: "Remove project" });
		expect(dialog).toHaveTextContent("Project One");
		expect(dialog).toHaveTextContent("live sessions");
		expect(dialog).toHaveTextContent("repository folder");
	});

	it("renames a session inline and persists via the daemon", async () => {
		const user = userEvent.setup();
		const workspaceWithSession = { ...workspace, sessions: [session] };
		renderSidebar({ workspaces: [workspaceWithSession] });

		await user.click(screen.getByLabelText("Rename fix login"));
		const input = screen.getByLabelText("Rename fix login");
		await user.clear(input);
		await user.type(input, "polish login{Enter}");

		await waitFor(() => expect(renameSessionMock).toHaveBeenCalledWith("proj-1-1", "polish login"));
	});

	it("caps the inline rename input at 20 characters", async () => {
		const user = userEvent.setup();
		const workspaceWithSession = { ...workspace, sessions: [session] };
		renderSidebar({ workspaces: [workspaceWithSession] });

		await user.click(screen.getByLabelText("Rename fix login"));
		expect(screen.getByLabelText("Rename fix login")).toHaveAttribute("maxlength", "20");
	});

	it("cancels the inline rename on Escape without calling the daemon", async () => {
		const user = userEvent.setup();
		const workspaceWithSession = { ...workspace, sessions: [session] };
		renderSidebar({ workspaces: [workspaceWithSession] });

		await user.click(screen.getByLabelText("Rename fix login"));
		const input = screen.getByLabelText("Rename fix login");
		await user.clear(input);
		await user.type(input, "discard me{Escape}");

		expect(renameSessionMock).not.toHaveBeenCalled();
		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();
	});

	it("always shows action icons and reserves padding for them", () => {
		renderSidebar();

		const projectRow = screen.getByText("Project One").closest('button, [role="button"]');
		const actionCluster = screen.getByLabelText("Project actions for Project One").parentElement;

		if (!projectRow) throw new Error("Project row button not found");
		expect(projectRow).toHaveClass("pr-sidebar-project-actions");
		expect(actionCluster).toHaveAttribute("data-project-actions");
		expect(actionCluster).toHaveClass("right-0.5", "gap-px");
		expect(within(actionCluster as HTMLElement).getAllByRole("button")).toHaveLength(2);
		expect(screen.getByLabelText("Project actions for Project One")).not.toHaveClass("opacity-0");
	});

	it("scales project actions with the row without scaling for action-button presses", () => {
		renderSidebar();

		const projectRow = screen.getByText("Project One").closest('button, [role="button"]');
		const pressSurface = projectRow?.closest<HTMLElement>("[data-project-press]");
		const projectActions = screen.getByLabelText("Project actions for Project One");

		if (!projectRow || !pressSurface) throw new Error("Project press surface not found");
		expect(pressSurface).toContainElement(projectActions);

		fireEvent.pointerDown(projectRow);
		expect(pressSurface).toHaveClass("scale-[0.98]");
		fireEvent.pointerUp(projectRow);
		expect(pressSurface).not.toHaveClass("scale-[0.98]");

		fireEvent.pointerDown(projectActions);
		expect(pressSurface).not.toHaveClass("scale-[0.98]");
	});

	it("optically aligns the project folder and label with its action icons", () => {
		renderSidebar();

		const projectRow = screen.getByText("Project One").closest('button, [role="button"]');
		expect(projectRow?.querySelector("[data-project-folder-visual]")).toHaveClass("translate-y-px");
		expect(projectRow?.querySelector("[data-project-label]")).toHaveClass("translate-y-px");
	});

	it("clamps width at minimum when dragged past the resize floor (no auto-collapse)", async () => {
		renderSidebar();

		const resizeHandle = screen.getByTestId("resize-handle");
		expect(resizeHandle).toBeInTheDocument();
		expect(document.querySelector('[data-slot="sidebar"][data-state="expanded"]')).toBeInTheDocument();

		fireEvent.pointerDown(resizeHandle, { clientX: SIDEBAR_DEFAULT_WIDTH });
		// Drag well past minimum — sidebar should stay expanded and clamp at min.
		fireEvent.pointerMove(window, { clientX: SIDEBAR_MIN_WIDTH - 50 });
		fireEvent.pointerUp(window);

		// Sidebar stays expanded; dragging no longer collapses it.
		expect(document.querySelector('[data-slot="sidebar"][data-state="expanded"]')).toBeInTheDocument();
		expect(document.documentElement.style.getPropertyValue("--ao-sidebar-w")).toBe(`${SIDEBAR_MIN_WIDTH}px`);
	});

	it("flushes any queued rAF frame on pointer-up and persists the clamped width", async () => {
		let queuedFrame: FrameRequestCallback | undefined;
		const requestAnimationFrameSpy = vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
			queuedFrame = callback;
			return 1;
		});
		const cancelAnimationFrameSpy = vi.spyOn(window, "cancelAnimationFrame").mockImplementation(() => undefined);

		try {
			renderSidebar();

			const resizeHandle = screen.getByTestId("resize-handle");

			fireEvent.pointerDown(resizeHandle, { clientX: SIDEBAR_DEFAULT_WIDTH });
			fireEvent.pointerMove(window, { clientX: SIDEBAR_MIN_WIDTH + 5 });
			fireEvent.pointerUp(window);

			// rAF was queued; pointerUp should flush it via cancelAnimationFrame.
			expect(cancelAnimationFrameSpy).toHaveBeenCalledWith(1);
			expect(window.localStorage.getItem("ao-sidebar-w")).toBe(String(SIDEBAR_MIN_WIDTH + 5));

			// Firing the stale frame after cancellation should not overwrite width.
			queuedFrame?.(performance.now());
			expect(window.localStorage.getItem("ao-sidebar-w")).toBe(String(SIDEBAR_MIN_WIDTH + 5));
		} finally {
			requestAnimationFrameSpy.mockRestore();
			cancelAnimationFrameSpy.mockRestore();
		}
	});

	it("renders active activity as pulsing blue regardless of PR context", () => {
		renderSidebar({
			workspaces: [
				{
					...workspace,
					sessions: [
						{
							...session,
							id: "proj-1-idle",
							title: "idle task",
							status: "idle",
							activity: { state: "idle", lastActivityAt: "2026-06-30T00:00:00Z" },
						},
						{
							...session,
							id: "proj-1-work",
							title: "working task",
							status: "working",
							activity: { state: "active", lastActivityAt: "2026-06-30T00:00:00Z" },
						},
						{
							...session,
							id: "proj-1-ci",
							title: "ci failed task",
							status: "working",
							scmStatus: "ci_failed",
							activity: { state: "active", lastActivityAt: "2026-06-30T00:00:00Z" },
							prs: [sidebarPR({ ci: "failing" })],
						},
						{
							...session,
							id: "proj-1-review",
							title: "review task",
							status: "working",
							scmStatus: "pr_open",
							activity: { state: "active", lastActivityAt: "2026-06-30T00:00:00Z" },
							prs: [sidebarPR()],
						},
						{
							...session,
							id: "proj-1-ready",
							title: "ready task",
							status: "working",
							scmStatus: "mergeable",
							activity: { state: "active", lastActivityAt: "2026-06-30T00:00:00Z" },
							prs: [sidebarPR({ mergeability: "mergeable" })],
						},
						{
							...session,
							id: "proj-1-merged",
							title: "merged task",
							status: "working",
							scmStatus: "merged",
							activity: { state: "active", lastActivityAt: "2026-06-30T00:00:00Z" },
							prs: [sidebarPR({ state: "merged" })],
						},
					],
				},
			],
		});
		const sessionDot = (title: string) =>
			screen.getByLabelText(`Open ${title}`).querySelector<HTMLElement>("[data-session-status]");

		expect(sessionDot("idle task")).toHaveClass("bg-status-idle");
		expect(sessionDot("idle task")).not.toHaveClass("animate-status-pulse");

		const workingDot = sessionDot("working task");
		expect(workingDot).toHaveClass("bg-status-working");
		expect(workingDot).toHaveClass("animate-status-pulse");

		const ciFailedDot = sessionDot("ci failed task");
		expect(ciFailedDot).toHaveClass("bg-status-working");
		expect(ciFailedDot).toHaveClass("animate-status-pulse");

		expect(sessionDot("review task")).toHaveClass("bg-status-working", "animate-status-pulse");
		expect(sessionDot("ready task")).toHaveClass("bg-status-working", "animate-status-pulse");
		expect(sessionDot("merged task")).toHaveClass("bg-status-working", "animate-status-pulse");
	});

	it("renders a static gray dot for idle activity across session statuses", async () => {
		renderSidebar({
			workspaces: [
				{
					...workspace,
					sessions: [
						{
							...session,
							id: "proj-1-idle-activity",
							title: "idle activity task",
							status: "working",
							activity: { state: "idle", lastActivityAt: "2026-06-30T00:00:00Z" },
						},
						{
							...session,
							id: "proj-1-idle-draft",
							title: "idle draft task",
							status: "draft",
							activity: { state: "idle", lastActivityAt: "2026-06-30T00:00:00Z" },
						},
					],
				},
			],
		});


		const idleActivityDot = screen
			.getByLabelText("Open idle activity task")
			.querySelector<HTMLElement>("span.rounded-full");
		const idleDraftDot = screen.getByLabelText("Open idle draft task").querySelector<HTMLElement>("span.rounded-full");

		expect(idleActivityDot).toHaveClass("bg-status-idle");
		expect(idleDraftDot).toHaveClass("bg-status-idle");
		expect(idleActivityDot).not.toHaveClass("animate-status-pulse");
		expect(idleDraftDot).not.toHaveClass("animate-status-pulse");
	});

	it("keeps runtime activity on the dot while showing switch progress separately", () => {
		renderSidebar({
			workspaces: [{
				...workspace,
				sessions: [{
					...session,
					status: "exited",
					activity: { state: "exited", lastActivityAt: "2026-06-30T00:00:00Z" },
					activeAgentSwitch: activeAgentSwitch(),
				}],
			}],
		});

		const row = screen.getByLabelText("Open fix login");
		expect(row).toHaveAccessibleDescription("Switching to Codex");
		expect(within(row).getByText("Switching to Codex")).toBeInTheDocument();
		const dot = row.querySelector<HTMLElement>("[data-session-status]");
		expect(dot).toHaveClass("bg-status-exited");
		expect(dot).not.toHaveClass("animate-status-pulse");
	});

	it("shows sessions on load and hides them once collapsed", async () => {
		const user = userEvent.setup();
		const workspaceWithSessions = {
			...workspace,
			sessions: [session, { ...session, id: "proj-1-2", title: "second task" }],
		};
		renderSidebar({ workspaces: [workspaceWithSessions] });

		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();
		expect(screen.getByLabelText("Open second task")).toBeInTheDocument();

		// Collapse via folder icon
		const folder = screen.getByRole("button", { name: "Toggle Project One sessions" });
		expect(folder).toBeTruthy();
		await user.click(folder);

		expect(screen.queryByLabelText("Open fix login")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Open second task")).not.toBeInTheDocument();
	});

	it("hides all sessions when project is collapsed via folder icon", async () => {
		const user = userEvent.setup();
		mockParams.projectId = "proj-1";
		mockParams.sessionId = "proj-1-2";
		renderSidebar({
			workspaces: [
				{
					...workspace,
					sessions: [session, { ...session, id: "proj-1-2", title: "second task" }],
				},
			],
		});

		const projectRow = screen.getByText("Project One").closest('button, [role="button"]')!;
		// Project starts expanded — sessions visible
		expect(screen.getByLabelText("Open second task")).toBeInTheDocument();
		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();
		expect(projectRow).toHaveAttribute("aria-expanded", "true");

		// Collapse via folder icon
		const folder = screen.getByRole("button", { name: "Toggle Project One sessions" });
		expect(folder).toBeTruthy();
		await user.click(folder);

		expect(projectRow).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByLabelText("Open second task")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Open fix login")).not.toBeInTheDocument();
	});

	it("keeps merged sessions in the list until they are terminated", async () => {
		renderSidebar({
			workspaces: [
				{
					...workspace,
					sessions: [
						{ ...session, id: "merged-live", title: "merged live task", status: "merged", isTerminated: false },
						{ ...session, id: "merged-done", title: "merged terminated task", status: "merged", isTerminated: true },
					],
				},
			],
		});


		expect(screen.getByLabelText("Open merged live task")).toBeInTheDocument();
		expect(screen.queryByLabelText("Open merged terminated task")).not.toBeInTheDocument();
	});

	it("does not render the restart-to-update row unless an update is downloaded", async () => {
		updateStatusMock.mockResolvedValue({ state: "available", version: "9.9.9" });
		renderSidebar();

		await waitFor(() => expect(updateStatusMock).toHaveBeenCalled());
		expect(screen.queryByLabelText(/Restart to install update/)).not.toBeInTheDocument();
	});

	it("renders the restart-to-update row with the working-orange treatment when escalated", async () => {
		updateStatusMock.mockResolvedValue({
			state: "downloaded",
			version: "9.9.9",
			stagedAt: Date.now(),
			escalated: true,
		});
		renderSidebar();

		// Both footer variants (expanded row and collapsed rail icon) are mounted.
		const buttons = await screen.findAllByLabelText("Restart to install update v9.9.9");
		expect(buttons.length).toBeGreaterThan(0);
		for (const button of buttons) {
			expect(button).toHaveClass("text-working", "bg-working/12");
		}
		expect(screen.getByText("v9.9.9 ready")).toBeInTheDocument();
	});
});
