import { SidebarProvider } from "@/components/ui/sidebar";
import { TooltipProvider } from "@/components/ui/tooltip";
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
import { agentReadinessQueryKey } from "../hooks/useAgentReadinessQuery";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import { useUiStore } from "../stores/ui-store";

type DragOverTestEvent = {
	active: {
		id: string;
		rect: { current: { initial: null; translated: null } };
	};
	activatorEvent: null;
	delta: { x: number; y: number };
	over: { id: string; rect: { height: number; top: number } } | null;
};

const {
	checkUpdateMock,
	cloudGateState,
	cloudSessionState,
	dragEnds,
	dragOvers,
	dragStarts,
	downloadUpdateMock,
	getMock,
	navigateMock,
	mockParams,
	postMock,
	renameSessionMock,
	spawnMock,
	updateStatusMock,
	commandPaletteEnabled,
} = vi.hoisted(
	() => ({
		cloudGateState: { cloudEnabled: true, localEnabled: true, client: "" },
		cloudSessionState: {
			configured: false,
			session: null as null | { user: { email: string } },
			status: "unauthenticated" as "authenticated" | "loading" | "unauthenticated",
			signIn: vi.fn(),
			signOut: vi.fn().mockResolvedValue(undefined),
		},
		dragEnds: new Map<string, (event: { active: { id: string }; over: { id: string } | null }) => void>(),
		dragOvers: new Map<string, (event: DragOverTestEvent) => void>(),
		dragStarts: new Map<string, (event: { active: { id: string } }) => void>(),
		getMock: vi.fn(),
		postMock: vi.fn(),
		navigateMock: vi.fn(),
		mockParams: { projectId: undefined as string | undefined, sessionId: undefined as string | undefined },
		renameSessionMock: vi.fn().mockResolvedValue(undefined),
		spawnMock: vi.fn(),
		updateStatusMock: vi.fn(),
		downloadUpdateMock: vi.fn(),
		checkUpdateMock: vi.fn(),
		commandPaletteEnabled: { current: true },
	}),
);

vi.mock("@dnd-kit/core", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@dnd-kit/core")>();
	return {
		...actual,
		DndContext: ({ children, id, onDragEnd, onDragOver, onDragStart }: {
			children: React.ReactNode;
			id?: string;
			onDragEnd?: (event: { active: { id: string }; over: { id: string } | null }) => void;
			onDragOver?: (event: DragOverTestEvent) => void;
			onDragStart?: (event: { active: { id: string } }) => void;
		}) => {
			if (id && onDragEnd) dragEnds.set(id, onDragEnd);
			if (id && onDragOver) dragOvers.set(id, onDragOver);
			if (id && onDragStart) dragStarts.set(id, onDragStart);
			return <div data-dnd-context={id}>{children}</div>;
		},
		DragOverlay: ({ children }: { children: React.ReactNode }) => children,
	};
});

vi.mock("../lib/rename-session", () => ({ renameSession: renameSessionMock }));
vi.mock("../lib/spawn-orchestrator", () => ({ spawnOrchestrator: spawnMock }));
vi.mock("../lib/cloud-session", () => ({ useCloudSession: () => cloudSessionState }));
vi.mock("../hooks/useCloudGate", () => ({ useCloudGate: () => cloudGateState }));
// Local (dev-only) cloud sign-in is off in these tests; mock it like its cloud
// siblings so the sign-in row never fires a real settings fetch.
vi.mock("../hooks/useCloudLocalAuth", () => ({
	useCloudLocalAuth: () => ({
		available: false,
		cpUrl: "",
		register: async () => {
			throw new Error("useCloudLocalAuth is mocked");
		},
		login: async () => {
			throw new Error("useCloudLocalAuth is mocked");
		},
	}),
}));
vi.mock("../hooks/useCommandPaletteEnabled", () => ({
	useCommandPaletteEnabled: () => commandPaletteEnabled.current,
}));

vi.mock("../lib/platform", () => ({
	isLinuxPlatform: () => false,
	isMacPlatform: () => true,
	isWindowsPlatform: () => false,
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
			updates: {
				...actual.aoBridge.updates,
				getStatus: updateStatusMock,
				download: downloadUpdateMock,
				check: checkUpdateMock,
			},
		},
	};
});

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
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
type CloneProjectHandler = (input: {
	remoteUrl: string;
	destinationParent: string;
	workerAgent: string;
	orchestratorAgent: string;
	trackerIntake?: unknown;
}) => Promise<void>;
type InitializeProjectHandler = (path: string) => Promise<void>;
type RemoveProjectHandler = (projectId: string) => Promise<void>;

function projectValidation(
	path: string,
	overrides: Partial<{
		isValid: boolean;
		blockingErrors: string[];
		nextStep: "error" | "choose_import_kind" | "prepare_git" | "continue";
		warning: string;
		root: Partial<{
			repoPath: string;
			isRepo: boolean;
			hasCommit: boolean;
			hasOrigin: boolean;
			isEmptyFolder: boolean;
			needsGitInit: boolean;
			requiredActions: string[];
			blockingErrors: string[];
		}>;
	}> = {},
) {
	return {
		importKind: "project",
		isValid: overrides.isValid ?? true,
		blockingErrors: overrides.blockingErrors ?? [],
		root: {
			repoPath: overrides.root?.repoPath ?? path,
			isRepo: overrides.root?.isRepo ?? true,
			hasCommit: overrides.root?.hasCommit ?? true,
			hasOrigin: overrides.root?.hasOrigin ?? true,
			isEmptyFolder: overrides.root?.isEmptyFolder ?? false,
			needsGitInit: overrides.root?.needsGitInit ?? false,
			requiredActions: overrides.root?.requiredActions ?? [],
			blockingErrors: overrides.root?.blockingErrors ?? [],
		},
		childRepos: [],
		nextStep: overrides.nextStep ?? "continue",
		warning: overrides.warning,
	};
}

function workspaceValidation(path: string) {
	return {
		...projectValidation(path),
		importKind: "workspace",
		root: { ...projectValidation(path).root, isRepo: false, hasCommit: false, hasOrigin: false, needsGitInit: true },
		childRepos: [{
			repoPath: `${path}/project`, isRepo: true, hasCommit: true, hasOrigin: true,
			isEmptyFolder: false, needsGitInit: false, requiredActions: [], blockingErrors: [],
		}],
	};
}

function renderSidebar({
	onCloneProject = vi.fn().mockResolvedValue(undefined) as CloneProjectHandler,
	onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler,
	onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler,
	onRemoveProject = vi.fn().mockResolvedValue(undefined) as RemoveProjectHandler,
	seedAgents = true,
	workspaces = [workspace],
	initialOpen = true,
	topbarOffset = "toolbar",
	expandedProjectIds,
}: {
	onCloneProject?: CloneProjectHandler;
	onCreateProject?: CreateProjectHandler;
	onInitializeProject?: InitializeProjectHandler;
	onRemoveProject?: RemoveProjectHandler;
	seedAgents?: boolean;
	workspaces?: WorkspaceSummary[];
	initialOpen?: boolean;
	topbarOffset?: "toolbar" | "titlebar" | "trafficLights" | "session";
	expandedProjectIds?: string[];
} = {}) {
	// Most legacy sidebar tests exercise session rows and assume their fixture
	// project was previously open. Tests for the empty-store behavior opt out.
	window.localStorage.setItem(
		"ao.sidebar.expanded-projects",
		JSON.stringify(expandedProjectIds ?? workspaces.map(({ id }) => id)),
	);
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	if (seedAgents) {
		queryClient.setQueryData(agentReadinessQueryKey, {
			agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex")],
		});
	}
	render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<SidebarProvider defaultOpen={initialOpen}>
					<Sidebar
						topbarOffset={topbarOffset}
						onCloneProject={onCloneProject}
						onCreateProject={onCreateProject}
						onInitializeProject={onInitializeProject}
						onRemoveProject={onRemoveProject}
						workspaces={workspaces}
					/>
				</SidebarProvider>
			</TooltipProvider>
		</QueryClientProvider>,
	);
	return onRemoveProject;
}

/** Projects restore their persisted disclosure state. */

async function chooseOption(trigger: HTMLElement, optionName: string) {
	await userEvent.click(trigger);
	await userEvent.click(await screen.findByRole("option", { name: optionName }));
}

function codedError(message: string, code: "NOT_A_GIT_REPO" | "PROJECT_UNBORN") {
	const error = new Error(message) as Error & { code: string };
	error.code = code;
	return error;
}

beforeEach(() => {
	window.localStorage.clear();
	dragEnds.clear();
	dragOvers.clear();
	dragStarts.clear();
	document.documentElement.style.removeProperty("--ao-sidebar-w");
	commandPaletteEnabled.current = true;
	cloudGateState.cloudEnabled = true;
	cloudGateState.localEnabled = true;
	cloudGateState.client = "";
	cloudSessionState.configured = false;
	cloudSessionState.session = null;
	cloudSessionState.status = "unauthenticated";
	cloudSessionState.signIn.mockReset();
	cloudSessionState.signOut.mockReset().mockResolvedValue(undefined);
	useUiStore.setState({ isCommandPaletteOpen: false, settingsModal: null });
	getMock.mockReset();
	postMock.mockReset();
	getMock.mockResolvedValue({
		data: {
			agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex")],
		},
		error: undefined,
	});
	postMock.mockImplementation(async (route: string, { body }: { body?: { importKind?: string; path?: string } }) => {
		if (route === "/api/v1/imports/validate") {
			if (body?.importKind === "workspace") return { data: workspaceValidation(body.path ?? "/repo/workspace"), error: undefined };
			return { data: projectValidation(body?.path ?? "/repo/new-project"), error: undefined };
		}
		if (route === "/api/v1/imports/prepare-git") {
			return {
				data: {
					events: [],
					validation: projectValidation(body?.path ?? "/repo/new-project"),
				},
				error: undefined,
			};
		}
		if (route === "/api/v1/projects/clone/prepare") {
			return { data: { path: "/repo/web-app", remoteUrl: "git@github.com:acme/web-app.git" }, error: undefined };
		}
		if (route === "/api/v1/projects/clone/cleanup") return { data: undefined, error: undefined };
		return { data: undefined, error: new Error(`Unhandled POST ${route}`) };
	});
	window.ao!.app.scanImportFolder = vi.fn().mockImplementation(async ({ path }: { path: string }) => ({
		path,
		repos: [],
	}));
	navigateMock.mockReset();
	renameSessionMock.mockReset().mockResolvedValue(undefined);
	spawnMock.mockReset();
	updateStatusMock.mockReset().mockResolvedValue({ state: "idle" });
	downloadUpdateMock.mockReset().mockResolvedValue(undefined);
	checkUpdateMock.mockReset().mockResolvedValue(undefined);
	mockParams.projectId = undefined;
	mockParams.sessionId = undefined;
});

afterEach(() => {
	vi.restoreAllMocks();
});

describe("Sidebar", () => {
	it("shows the cloud sign-in entry point while signed out", () => {
		cloudSessionState.configured = true;
		renderSidebar();

		const signInControls = screen.getAllByLabelText("Sign in to AO Cloud");
		expect(signInControls).toHaveLength(2);
		const activeControl = signInControls.find((control) => control.tabIndex === 0);
		expect(activeControl).toBeDefined();
		fireEvent.click(activeControl!);
		expect(cloudSessionState.signIn).toHaveBeenCalledOnce();
	});

	it("keeps cloud account controls visible while signed in", () => {
		cloudSessionState.configured = true;
		cloudSessionState.status = "authenticated";
		cloudSessionState.session = { user: { email: "user@example.com" } };
		renderSidebar();

		expect(screen.getAllByLabelText("Signed in as user@example.com")).toHaveLength(2);
	});

	it("hides cloud account controls when the daemon reports the cloud offering off", () => {
		cloudGateState.cloudEnabled = false;
		cloudSessionState.configured = true;
		cloudSessionState.status = "authenticated";
		cloudSessionState.session = { user: { email: "user@example.com" } };
		renderSidebar();

		expect(screen.queryByLabelText("Signed in as user@example.com")).not.toBeInTheDocument();
	});

	it("suppresses focus chrome without removing keyboard focusability", () => {
		renderSidebar();

		expect(document.querySelector('[data-slot="sidebar-container"]')).toHaveClass("sidebar-focusless");
		expect(screen.getAllByRole("button", { name: "Settings" })[0]).toHaveAttribute("tabindex", "0");
	});

	it("keeps the Settings footer flush with the bottom edge", () => {
		renderSidebar();

		const footer = document.querySelector('[data-sidebar="footer"]');
		expect(footer).toHaveClass("border-t", "border-border-strong", "!py-2");
		expect(screen.getAllByRole("button", { name: "Settings" })[0]).toHaveClass("h-[42px]");
		expect(footer?.className).not.toContain("--size-center-panel-bottom-inset");
		expect(footer?.className).not.toContain("--size-center-panel-inset-mac");
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

		expect(await screen.findByRole("dialog", { name: "Add a project" })).toBeInTheDocument();
	});

	it("keeps the create-project shortcut available when there are no projects", async () => {
		renderSidebar({ workspaces: [] });

		act(() => {
			useUiStore.getState().requestCreateProject();
		});

		expect(await screen.findByRole("dialog", { name: "Add a project" })).toBeInTheDocument();
	});

	it("reveals orchestrator and kebab buttons on the project row (no dashboard button)", () => {
		renderSidebar();

		expect(screen.queryByLabelText("Open Project One dashboard")).not.toBeInTheDocument();
		expect(screen.getByLabelText("Spawn Project One orchestrator")).toBeInTheDocument();
		expect(screen.getByLabelText("Project actions for Project One")).toBeInTheDocument();
	});

	it("keeps project disclosure and row actions in the keyboard tab order", () => {
		renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });

		const disclosure = screen.getByRole("button", { name: "Toggle Project One sessions" });
		expect(disclosure.tagName).toBe("BUTTON");
		expect(disclosure).toHaveProperty("tabIndex", 0);
		expect(screen.getByLabelText("Spawn Project One orchestrator")).toHaveProperty("tabIndex", 0);
		expect(screen.getByLabelText("Project actions for Project One")).toHaveProperty("tabIndex", 0);
		expect(screen.getByLabelText("Pin session")).toHaveProperty("tabIndex", 0);
		expect(screen.queryByRole("button", { name: "Rename fix login" })).not.toBeInTheDocument();
		expect(screen.getByLabelText("Kill session")).toHaveProperty("tabIndex", 0);
	});

	it("fades the message age out in favor of the overlaid hover actions", () => {
		const lastUserMessageAt = "2026-06-29T23:55:00Z";
		renderSidebar({
			workspaces: [{ ...workspace, sessions: [{ ...session, lastUserMessageAt }] }],
		});

		const openSession = screen.getByLabelText("Open fix login");
		const label = within(openSession).getByText("fix login");
		const actions = screen.getByLabelText("Pin session").closest("[data-session-actions]");
		const actionButtons = screen.getByLabelText("Pin session").parentElement;
		const time = actions?.querySelector("time");

		expect(openSession).toHaveClass("pr-[36px]");
		expect(openSession).toHaveClass(
			"group-hover/session-row:pr-[50px]",
			"group-focus-within/session-row:pr-[50px]",
		);
		expect(label).toHaveClass("min-w-0", "flex-1", "truncate");
		expect(actions).toHaveAttribute("data-session-actions");
		expect(actionButtons).toHaveClass(
			"absolute",
			"right-0.5",
			"opacity-0",
			"group-hover/session-row:pointer-events-auto",
			"group-hover/session-row:opacity-100",
			"group-focus-within/session-row:pointer-events-auto",
			"group-focus-within/session-row:opacity-100",
		);
		expect(time).toHaveAttribute("datetime", lastUserMessageAt);
		expect(time).toHaveClass(
			"absolute",
			"right-1.5",
			"opacity-100",
			"group-hover/session-row:opacity-0",
			"group-focus-within/session-row:opacity-0",
		);
		expect(openSession).toHaveClass("pl-1.5");
		expect(openSession.closest("li")).toHaveClass("pl-0.5");
	});

	it("keeps session status and actions stable when an action receives keyboard focus", async () => {
		const user = userEvent.setup();
		renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });

		const openSession = screen.getByLabelText("Open fix login");
		const row = openSession.closest<HTMLElement>("[data-session-row]");
		const status = openSession.querySelector("[data-session-status]");

		if (!row) throw new Error("Session row not found");
		expect(status).toBeInTheDocument();
		openSession.focus();
		await user.tab();

		expect(screen.getByLabelText("Pin session")).toHaveFocus();
		expect(row).toContainElement(openSession);
		expect(row).toContainElement(status as HTMLElement);
		expect(row).toContainElement(screen.getByLabelText("Pin session"));
	});

	it("keeps action pointer presses from triggering the session press surface", () => {
		renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });

		const openSession = screen.getByLabelText("Open fix login");
		const row = openSession.closest<HTMLElement>("[data-session-row]");
		if (!row) throw new Error("Session row not found");

		fireEvent.pointerDown(openSession);
		expect(row).toHaveClass("scale-[0.97]");
		fireEvent.pointerUp(openSession);
		expect(row).not.toHaveClass("scale-[0.97]");

		fireEvent.pointerDown(screen.getByLabelText("Pin session"));
		expect(row).not.toHaveClass("scale-[0.97]");
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

	it("lists worker sessions by updated time, newest first", () => {
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
			"Open invalid activity",
			"Open no activity",
			"Open old task",
			"Open new task",
			"Open created fallback",
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
		expect(screen.getByRole("dialog", { name: "Add a project" })).toBeInTheDocument();
		expect(window.ao!.app.chooseDirectory).not.toHaveBeenCalled();
		await user.click(screen.getByRole("button", { name: /^Import an existing project$/i }));

		expect(window.ao!.app.chooseDirectory).toHaveBeenCalledWith("Choose a project repository");
		const dialog = await screen.findByRole("dialog", { name: "Set up project" });
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

	it("prepares a Git clone before starting agents, then registers the checkout", async () => {
		const user = userEvent.setup();
		const onCloneProject = vi.fn().mockResolvedValue(undefined) as CloneProjectHandler;
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo");
		renderSidebar({ onCloneProject, onCreateProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: "Clone from Git" }));
		expect(await screen.findByRole("dialog", { name: "Clone a Git repository" })).toBeInTheDocument();

		await user.type(
			await screen.findByRole("textbox", { name: "Repository URL" }),
			"git@github.com:acme/web-app.git",
		);
		await user.click(screen.getByRole("button", { name: "Choose" }));
		expect(window.ao!.app.chooseDirectory).toHaveBeenCalledWith("Choose where to clone the repository");
		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(await screen.findByRole("dialog", { name: "Set up project" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Clone" }));
		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith({
				path: "/repo/web-app",
				workerAgent: "claude-code",
				orchestratorAgent: "claude-code",
			}),
		);
		expect(onCloneProject).not.toHaveBeenCalled();
	});

	it("creates the selected local repository after backing out of a clone", async () => {
		const user = userEvent.setup();
		const onCloneProject = vi.fn().mockResolvedValue(undefined) as CloneProjectHandler;
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi
			.fn()
			.mockResolvedValueOnce("/repo")
			.mockResolvedValueOnce("/repo/local-project");
		renderSidebar({ onCloneProject, onCreateProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: "Clone from Git" }));
		await user.type(
			await screen.findByRole("textbox", { name: "Repository URL" }),
			"git@github.com:acme/web-app.git",
		);
		await user.click(screen.getByRole("button", { name: "Choose" }));
		await user.click(await screen.findByRole("button", { name: "Continue" }));

		await user.click(await screen.findByRole("button", { name: "Back to clone details" }));
		await user.click(await screen.findByRole("button", { name: "Back to code source" }));
		await user.click(await screen.findByRole("button", { name: /^Import an existing project$/i }));

		expect(await screen.findByRole("dialog", { name: "Set up project" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create and start" }));

		await waitFor(() =>
			expect(onCreateProject).toHaveBeenCalledWith(
				expect.objectContaining({
					path: "/repo/local-project",
					workerAgent: "claude-code",
					orchestratorAgent: "claude-code",
				}),
			),
		);
		expect(onCloneProject).not.toHaveBeenCalled();
	});

	it("prioritizes authorized project agents by preferred agent order", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/new-project");
		getMock.mockResolvedValueOnce({
			data: {
				agents: [
					agentReadiness("goose", "Goose"),
					agentReadiness("devin", "Devin"),
					agentReadiness("aider", "Aider"),
					agentReadiness("opencode", "OpenCode"),
					agentReadiness("cursor", "Cursor"),
				],
			},
			error: undefined,
		});
		renderSidebar({ onCreateProject, seedAgents: false });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import an existing project$/i }));
		await screen.findByRole("dialog", { name: "Set up project" });
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

	it("prepares a non-git project before creating it", async () => {
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		postMock.mockResolvedValueOnce({
			data: projectValidation("/repo/new-project", {
				nextStep: "prepare_git",
				root: {
					isRepo: false,
					hasCommit: false,
					hasOrigin: true,
					needsGitInit: true,
					requiredActions: ["git_init", "git_commit"],
				},
			}),
			error: undefined,
		});
		postMock.mockResolvedValueOnce({
			data: {
				events: [
					{ repoPath: "/repo/new-project", action: "git_init", state: "success" },
					{ repoPath: "/repo/new-project", action: "git_commit", state: "success" },
				],
				validation: projectValidation("/repo/new-project"),
			},
			error: undefined,
		});
		renderSidebar({ onCreateProject, onInitializeProject });
		const user = userEvent.setup();
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/new-project");

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import an existing project$/i }));
		expect(await screen.findByRole("dialog", { name: "Prepare project" })).toBeInTheDocument();
		expect(screen.getByText("Project setup")).toBeInTheDocument();
		expect(onInitializeProject).not.toHaveBeenCalled();
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(await screen.findByRole("dialog", { name: "Set up project" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create and start" }));
		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
		expect(onInitializeProject).not.toHaveBeenCalled();
	});

	it("shows the validation warning before preparing a nested plain project folder", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/parent/universe");
		postMock.mockResolvedValueOnce({
			data: projectValidation("/repo/parent/universe", {
				nextStep: "prepare_git",
				warning:
				"Selected folder is inside an existing Git repository at /repo/parent. AO will initialize this folder as a separate repository.",
				root: {
					isRepo: false,
					hasCommit: false,
					hasOrigin: true,
					needsGitInit: true,
					requiredActions: ["git_init", "git_commit"],
				},
			}),
			error: undefined,
		});
		postMock.mockResolvedValueOnce({
			data: {
				events: [
					{ repoPath: "/repo/parent/universe", action: "git_init", state: "success" },
					{ repoPath: "/repo/parent/universe", action: "git_commit", state: "success" },
				],
				validation: projectValidation("/repo/parent/universe"),
			},
			error: undefined,
		});
		renderSidebar({ onCreateProject, onInitializeProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import an existing project$/i }));

		expect(await screen.findByRole("dialog", { name: "Prepare project" })).toBeInTheDocument();
		expect(screen.getByText(/inside an existing Git repository at \/repo\/parent/i)).toBeInTheDocument();
		expect(onInitializeProject).not.toHaveBeenCalled();
		expect(onCreateProject).not.toHaveBeenCalled();

		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(await screen.findByRole("dialog", { name: "Set up project" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create and start" }));
		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
		expect(onInitializeProject).not.toHaveBeenCalled();
	});

	it("prepares repositories with no commits before opening agent selection", async () => {
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		postMock.mockResolvedValueOnce({
			data: projectValidation("/repo/unborn", {
				nextStep: "prepare_git",
				root: {
					isRepo: true,
					hasCommit: false,
					hasOrigin: true,
					requiredActions: ["git_commit"],
				},
			}),
			error: undefined,
		});
		postMock.mockResolvedValueOnce({
			data: {
				events: [{ repoPath: "/repo/unborn", action: "git_commit", state: "success" }],
				validation: projectValidation("/repo/unborn"),
			},
			error: undefined,
		});
		renderSidebar({ onCreateProject, onInitializeProject });
		const user = userEvent.setup();
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/unborn");
		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import an existing project$/i }));
		expect(await screen.findByRole("dialog", { name: "Prepare project" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/imports/prepare-git", expect.anything()));
		expect(await screen.findByRole("dialog", { name: "Set up project" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create and start" }));
		await waitFor(() => expect(onCreateProject).toHaveBeenCalledTimes(1));
		expect(onInitializeProject).not.toHaveBeenCalled();
	});

	it("does not create the project when agent selection is cancelled after preparation", async () => {
		const onCreateProject = vi
			.fn()
			.mockRejectedValueOnce(
				codedError("This folder is not a Git repository.", "NOT_A_GIT_REPO"),
			) as unknown as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockResolvedValue(undefined) as InitializeProjectHandler;
		postMock.mockResolvedValueOnce({
			data: projectValidation("/repo/new-project", {
				nextStep: "prepare_git",
				root: {
					isRepo: false,
					hasCommit: false,
					hasOrigin: true,
					needsGitInit: true,
					requiredActions: ["git_init", "git_commit"],
				},
			}),
			error: undefined,
		});
		postMock.mockResolvedValueOnce({
			data: {
				events: [
					{ repoPath: "/repo/new-project", action: "git_init", state: "success" },
					{ repoPath: "/repo/new-project", action: "git_commit", state: "success" },
				],
				validation: projectValidation("/repo/new-project"),
			},
			error: undefined,
		});
		renderSidebar({ onCreateProject, onInitializeProject });
		const user = userEvent.setup();
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/new-project");
		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import an existing project$/i }));
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		await screen.findByRole("dialog", { name: "Set up project" });
		await user.click(screen.getByRole("button", { name: "Close project agents dialog" }));
		expect(onInitializeProject).not.toHaveBeenCalled();
		expect(onCreateProject).not.toHaveBeenCalled();
		expect(screen.queryByRole("dialog", { name: "Set up project" })).not.toBeInTheDocument();
	});

	it("surfaces project preparation failures", async () => {
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		const onInitializeProject = vi.fn().mockRejectedValue(new Error("git init failed")) as InitializeProjectHandler;
		postMock.mockResolvedValueOnce({
			data: projectValidation("/repo/new-project", {
				nextStep: "prepare_git",
				root: {
					isRepo: false,
					hasCommit: false,
					hasOrigin: true,
					needsGitInit: true,
					requiredActions: ["git_init"],
				},
			}),
			error: undefined,
		});
		postMock.mockResolvedValueOnce({
			data: {
				events: [{ repoPath: "/repo/new-project", action: "git_init", state: "error", error: "git init failed" }],
				validation: projectValidation("/repo/new-project", {
					nextStep: "prepare_git",
					root: {
						isRepo: false,
						hasCommit: false,
						hasOrigin: true,
						needsGitInit: true,
						requiredActions: ["git_init"],
					},
				}),
			},
			error: undefined,
		});
		renderSidebar({ onCreateProject, onInitializeProject });
		const user = userEvent.setup();
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/new-project");
		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import an existing project$/i }));
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		expect(await screen.findByText(/failed while running Git initialization/i)).toBeInTheDocument();
		expect(onCreateProject).not.toHaveBeenCalled();
		expect(onInitializeProject).not.toHaveBeenCalled();
	});

	it("can create a workspace project from the project add flow", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockResolvedValue(undefined) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/workspace");
		renderSidebar({ onCreateProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import a workspace folder$/i }));

		expect(await screen.findByText("/repo/workspace")).toBeInTheDocument();
		expect(window.ao!.app.chooseDirectory).toHaveBeenCalledWith("Choose a workspace folder");
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(screen.getByRole("dialog", { name: "Set up workspace" })).toBeInTheDocument();
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
		await user.click(screen.getByRole("button", { name: /^Import a workspace folder$/i }));
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		await screen.findByRole("dialog", { name: "Set up workspace" });
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

	it("does not rescan folders for non-validation create failures", async () => {
		const user = userEvent.setup();
		const onCreateProject = vi.fn().mockRejectedValue(new Error("AO daemon is not ready.")) as CreateProjectHandler;
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/repo/workspace");
		window.ao!.app.checkAncestorRepo = vi.fn().mockResolvedValue(undefined);
		window.ao!.app.scanImportFolder = vi.fn().mockResolvedValue({ path: "/repo/workspace", repos: [] });
		renderSidebar({ onCreateProject });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import a workspace folder$/i }));
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		await screen.findByRole("dialog", { name: "Set up workspace" });
		await chooseOption(screen.getByRole("combobox", { name: "Orchestrator agent" }), "Claude Code");
		await user.click(screen.getByRole("button", { name: "Create workspace and start" }));

		expect(await screen.findByText("AO daemon is not ready.")).toBeInTheDocument();
		// The initial folder validation is required by the import step. The
		// non-validation create failure must not trigger a second scan.
		expect(window.ao!.app.checkAncestorRepo).toHaveBeenCalledWith("/repo/workspace");
		expect(window.ao!.app.scanImportFolder).toHaveBeenCalledTimes(1);
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
		await user.click(screen.getByRole("button", { name: /^Import a workspace folder$/i }));
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		await screen.findByRole("dialog", { name: "Set up workspace" });
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
				agents: [
					agentReadiness("claude-code", "Claude Code"),
					agentReadiness("cursor", "Cursor", { authentication: "unauthorized" }),
					agentReadiness("aider", "Aider", { installation: "not_installed", authentication: "unknown" }),
				],
			},
			error: undefined,
		});
		renderSidebar({ onCreateProject, seedAgents: false });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import an existing project$/i }));
		await screen.findByRole("dialog", { name: "Set up project" });

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
			data: { agents: ReturnType<typeof agentReadiness>[] };
			error: undefined;
		}) => void;
		getMock.mockReturnValueOnce(
			new Promise((resolve) => {
				resolveAgents = resolve;
			}),
		);
		renderSidebar({ onCreateProject, seedAgents: false });

		await user.click(screen.getByLabelText("New project"));
		await user.click(screen.getByRole("button", { name: /^Import an existing project$/i }));
		await screen.findByRole("dialog", { name: "Set up project" });
		expect(screen.getByRole("button", { name: "Create and start" })).toBeDisabled();

		resolveAgents({
			data: {
				agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex")],
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

	it("opens the Mobile settings page from the footer", async () => {
		const user = userEvent.setup();
		renderSidebar();
		await user.click((await screen.findAllByRole("button", { name: "Connect Mobile" }))[0]);
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "global", section: "mobile" });
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("always shows Connect Mobile", () => {
		renderSidebar();

		expect(screen.getByRole("button", { name: "Connect Mobile" })).toBeVisible();
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

	it("renames a session inline by double-clicking its full navigation target", async () => {
		const user = userEvent.setup();
		const workspaceWithSession = { ...workspace, sessions: [session] };
		renderSidebar({ workspaces: [workspaceWithSession] });

		await user.dblClick(screen.getByRole("button", { name: "Open fix login" }));
		expect(navigateMock).not.toHaveBeenCalled();
		const input = screen.getByLabelText("Rename fix login");
		await user.clear(input);
		await user.type(input, "  polish login  {Enter}");

		await waitFor(() => expect(renameSessionMock).toHaveBeenCalledWith("proj-1-1", "polish login"));
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("still opens a session after an unpaired single click", async () => {
		renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });

		fireEvent.click(screen.getByRole("button", { name: "Open fix login" }), { detail: 1 });
		expect(navigateMock).not.toHaveBeenCalled();

		await waitFor(
			() =>
				expect(navigateMock).toHaveBeenCalledWith({
					to: "/projects/$projectId/sessions/$sessionId",
					params: { projectId: "proj-1", sessionId: "proj-1-1" },
				}),
			{ timeout: 1_000 },
		);
	});

	it("starts the same inline rename from the session context menu", async () => {
		const user = userEvent.setup();
		renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });

		fireEvent.contextMenu(screen.getByRole("button", { name: "Open fix login" }));
		const renameItem = await screen.findByRole("menuitem", { name: "Rename fix login" });
		const menu = renameItem.closest('[role="menu"]');
		if (!menu) throw new Error("Session context menu not found");
		expect(within(menu as HTMLElement).getAllByRole("menuitem").map((item) => item.textContent)).toEqual(["Rename"]);
		expect(renameItem).toHaveTextContent(/^Rename$/);
		expect(renameItem.querySelector("svg")).toBeInTheDocument();
		await user.click(renameItem);

		expect(screen.getByRole("textbox", { name: "Rename fix login" })).toHaveFocus();
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("caps the inline rename input at 20 characters", async () => {
		const user = userEvent.setup();
		const workspaceWithSession = { ...workspace, sessions: [session] };
		renderSidebar({ workspaces: [workspaceWithSession] });

		await user.dblClick(screen.getByText("fix login"));
		expect(screen.getByLabelText("Rename fix login")).toHaveAttribute("maxlength", "20");
	});

	it("renders rename as an unboxed inline label editor", async () => {
		const user = userEvent.setup();
		const lastUserMessageAt = "2026-06-29T23:55:00Z";
		mockParams.sessionId = session.id;
		renderSidebar({
			workspaces: [{ ...workspace, sessions: [{ ...session, lastUserMessageAt }] }],
		});

		await user.dblClick(screen.getByText("fix login"));
		const input = screen.getByLabelText("Rename fix login");
		const time = input.parentElement?.querySelector("time");

		expect(input).toHaveAttribute("data-session-inline-editor");
		expect(input).toHaveClass("border-0", "bg-transparent!", "p-0", "ring-0");
		expect(input).not.toHaveClass("rounded-xs", "border-accent", "px-1", "focus-visible:ring-1");
		expect(input.parentElement).toHaveAttribute("data-session-row");
		expect(input.parentElement).toHaveClass("bg-interactive-active", "text-foreground", "pr-1");
		expect(time).toHaveAttribute("data-session-message-age", "");
		expect(time).toHaveAttribute("datetime", lastUserMessageAt);
	});

	it("offers F2 as a keyboard rename path", async () => {
		const user = userEvent.setup();
		renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });

		const openSession = screen.getByLabelText("Open fix login");
		expect(openSession).toHaveAttribute("aria-keyshortcuts", "F2");
		openSession.focus();
		await user.keyboard("{F2}");

		expect(screen.getByLabelText("Rename fix login")).toHaveFocus();
	});

	it("retains a double-tap rename path for touch", () => {
		renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });

		const label = screen.getByText("fix login");
		fireEvent.pointerUp(label, { pointerType: "touch" });
		fireEvent.pointerUp(label, { pointerType: "touch" });

		expect(screen.getByLabelText("Rename fix login")).toBeInTheDocument();
	});

	it("cancels the inline rename on Escape without calling the daemon", async () => {
		const user = userEvent.setup();
		const workspaceWithSession = { ...workspace, sessions: [session] };
		renderSidebar({ workspaces: [workspaceWithSession] });

		await user.dblClick(screen.getByText("fix login"));
		const input = screen.getByLabelText("Rename fix login");
		await user.clear(input);
		await user.type(input, "discard me{Escape}");

		expect(renameSessionMock).not.toHaveBeenCalled();
		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();
	});

	it.each(["", "fix login"])("does not persist the no-op rename %j", async (nextName) => {
		const user = userEvent.setup();
		renderSidebar({ workspaces: [{ ...workspace, sessions: [session] }] });

		await user.dblClick(screen.getByRole("button", { name: "Open fix login" }));
		const input = screen.getByLabelText("Rename fix login");
		await user.clear(input);
		if (nextName) await user.type(input, nextName);
		await user.keyboard("{Enter}");

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

	it("paints the dot from its board section while activity drives the pulse", () => {
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

		// No pull request: the dot falls back to runtime status.
		expect(sessionDot("idle task")).toHaveClass("bg-status-idle");
		expect(sessionDot("idle task")).not.toHaveClass("animate-status-pulse");

		const workingDot = sessionDot("working task");
		expect(workingDot).toHaveClass("bg-status-working");
		expect(workingDot).toHaveClass("animate-status-pulse");

		// The board-section tone stays visible while the pulse says the agent is busy.
		expect(sessionDot("ci failed task")).toHaveClass("bg-status-needs-you", "animate-status-pulse");
		expect(sessionDot("review task")).toHaveClass("bg-status-in-review", "animate-status-pulse");
		expect(sessionDot("ready task")).toHaveClass("bg-status-ready", "animate-status-pulse");
		expect(sessionDot("merged task")).toHaveClass("bg-status-merged", "animate-status-pulse");
	});

	it("blinks blue when an idle-section session has working activity", () => {
		renderSidebar({
			workspaces: [
				{
					...workspace,
					sessions: [
						{
							...session,
							id: "proj-1-idle-working",
							title: "idle task receiving work",
							status: "idle",
							activity: { state: "active", lastActivityAt: "2026-06-30T00:00:00Z" },
						},
					],
				},
			],
		});

		const dot = screen
			.getByLabelText("Open idle task receiving work")
			.querySelector<HTMLElement>("[data-session-status]");
		expect(dot).toHaveClass("bg-status-working", "animate-status-pulse");
	});

	it("holds the dot still for idle activity and keeps its PR tone", async () => {
		renderSidebar({
			workspaces: [
				{
					...workspace,
					sessions: [
						{
							...session,
							id: "proj-1-idle-activity",
							title: "idle activity task",
							status: "idle",
							activity: { state: "idle", lastActivityAt: "2026-06-30T00:00:00Z" },
						},
						{
							...session,
							id: "proj-1-idle-draft",
							title: "idle draft task",
							status: "draft",
							scmStatus: "draft",
							activity: { state: "idle", lastActivityAt: "2026-06-30T00:00:00Z" },
							prs: [sidebarPR({ state: "draft" })],
						},
					],
				},
			],
		});


		const idleActivityDot = screen
			.getByLabelText("Open idle activity task")
			.querySelector<HTMLElement>("span.rounded-full");
		const idleDraftDot = screen.getByLabelText("Open idle draft task").querySelector<HTMLElement>("span.rounded-full");

		// An idle session with no pull request stays gray; a parked draft keeps
		// the in-review tone the board gives it, without any motion.
		expect(idleActivityDot).toHaveClass("bg-status-idle");
		expect(idleDraftDot).toHaveClass("bg-status-in-review");
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
		expect(dot).toHaveClass("bg-status-needs-you");
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

	it("starts every project collapsed when the expanded-project store is empty", () => {
		renderSidebar({
			expandedProjectIds: [],
			workspaces: [{ ...workspace, sessions: [session] }],
		});

		expect(screen.queryByLabelText("Open fix login")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Toggle Project One sessions" })).toHaveAttribute(
			"aria-expanded",
			"false",
		);
	});

	it("reveals the active project when opening a worker-session deep link", async () => {
		const user = userEvent.setup();
		mockParams.projectId = workspace.id;
		mockParams.sessionId = session.id;
		renderSidebar({
			expandedProjectIds: [],
			workspaces: [{ ...workspace, sessions: [session] }],
		});

		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Toggle Project One sessions" })).toHaveAttribute(
			"aria-expanded",
			"true",
		);

		await user.click(screen.getByRole("button", { name: "Toggle Project One sessions" }));
		expect(screen.queryByLabelText("Open fix login")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Toggle Project One sessions" })).toHaveAttribute(
			"aria-expanded",
			"false",
		);
	});

	it("restores only the projects saved as expanded and persists toggles", async () => {
		const user = userEvent.setup();
		const secondWorkspace = {
			...workspace,
			id: "proj-2",
			name: "Project Two",
			sessions: [{ ...session, id: "proj-2-1", title: "second task" }],
		};
		renderSidebar({
			expandedProjectIds: [workspace.id],
			workspaces: [{ ...workspace, sessions: [session] }, secondWorkspace],
		});

		expect(screen.getByLabelText("Open fix login")).toBeInTheDocument();
		expect(screen.queryByLabelText("Open second task")).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Toggle Project One sessions" }));
		expect(JSON.parse(window.localStorage.getItem("ao.sidebar.expanded-projects") ?? "null")).toEqual([]);
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

	it("downloads the update when the available row is clicked", async () => {
		updateStatusMock.mockResolvedValue({ state: "available", version: "9.9.9" });
		renderSidebar();

		// Both footer variants (expanded row and collapsed rail icon) are mounted.
		const buttons = await screen.findAllByLabelText("Download update v9.9.9");
		expect(buttons.length).toBeGreaterThan(0);
		expect(screen.getByText("Update available")).toBeInTheDocument();
		const availableRow = screen.getByTestId("sidebar-update-available");
		expect(within(availableRow).getByText("v9.9.9")).toBeVisible();
		expect(availableRow.querySelector(".rounded-full")).toBeNull();
		expect(screen.getByRole("button", { name: "Hide update v9.9.9 for 24 hours" })).not.toHaveClass(
			"bg-interactive-hover",
		);
		// Nothing is staged yet, so the restart action must not be offered.
		expect(screen.queryByLabelText(/Restart to install update/)).not.toBeInTheDocument();

		await userEvent.click(buttons[0]);
		expect(downloadUpdateMock).toHaveBeenCalledTimes(1);
	});

	it("dismisses the current available update without downloading it", async () => {
		updateStatusMock.mockResolvedValue({ state: "available", version: "9.9.9" });
		renderSidebar();

		await userEvent.click(await screen.findByRole("button", {
			name: "Hide update v9.9.9 for 24 hours",
		}));

		expect(screen.queryByText("Update available")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Download update v9.9.9")).not.toBeInTheDocument();
		expect(downloadUpdateMock).not.toHaveBeenCalled();
	});

	it("keeps showing update activity while the automatic download is in progress", async () => {
		updateStatusMock.mockResolvedValue({ state: "downloading", version: "9.9.9", percent: 42 });
		renderSidebar();

		await waitFor(() => expect(updateStatusMock).toHaveBeenCalled());
		expect(screen.getByText("Downloading… 42%")).toBeInTheDocument();
		const downloadingRow = screen.getByTestId("sidebar-update-downloading");
		expect(downloadingRow).not.toHaveClass("border");
		expect(downloadingRow.querySelector("svg circle")).toBeNull();
		expect(screen.queryByLabelText(/Restart to install update/)).not.toBeInTheDocument();
		// A download already in flight must not offer a second one.
		expect(screen.queryByLabelText(/Download update/)).not.toBeInTheDocument();
		expect(screen.queryByLabelText(/Hide update/)).not.toBeInTheDocument();
	});

	it("offers a retry when automatic update checks keep failing", async () => {
		// The state stays truthful (the suppressed automatic failure never
		// replaced it); the flag is what makes the dead end visible.
		updateStatusMock.mockResolvedValue({ state: "idle", checksFailing: true });
		renderSidebar();

		// Both footer variants (expanded row and collapsed rail icon) are mounted.
		const buttons = await screen.findAllByLabelText("Retry update check");
		expect(buttons.length).toBeGreaterThan(0);
		expect(screen.getByText("Update check failed")).toBeInTheDocument();
		const failedRow = screen.getByTestId("sidebar-update-failed");
		expect(failedRow).toHaveClass("border", "border-warning/35", "bg-warning/12", "text-warning");
		expect(within(failedRow).getByText("Retry update check")).toBeVisible();
		expect(failedRow.querySelector(".rounded-full")).toBeNull();

		await userEvent.click(buttons[0]);
		expect(checkUpdateMock).toHaveBeenCalledTimes(1);
	});

	it("keeps a staged build's restart action ahead of the failing-checks retry", async () => {
		updateStatusMock.mockResolvedValue({
			state: "downloaded",
			version: "9.9.9",
			stagedAt: Date.now(),
			checksFailing: true,
		});
		renderSidebar();

		// A build ready to install is more actionable than "checks are failing".
		expect(await screen.findAllByLabelText("Restart to install update v9.9.9")).not.toHaveLength(0);
		const readyRow = screen.getByTestId("sidebar-update-ready");
		expect(readyRow).toHaveClass("border", "border-primary/35", "bg-primary/12", "text-primary");
		expect(within(readyRow).getByText("v9.9.9 ready")).toBeVisible();
		expect(readyRow.querySelector(".rounded-full")).toBeNull();
		expect(screen.queryByLabelText("Retry update check")).not.toBeInTheDocument();
		expect(screen.queryByLabelText(/Hide update/)).not.toBeInTheDocument();
	});

	it("keeps the staged restart row up while a background check runs", async () => {
		// Regression: the row keyed off `state`, which a routine check drives
		// through checking/available/not-available while the staged build is
		// untouched, so the row blinked out of existence every 15 minutes on
		// nightly. `staged` is stamped on every status for exactly this reason.
		const stagedAt = Date.now();
		updateStatusMock.mockResolvedValue({
			state: "checking",
			staged: { version: "9.9.9", stagedAt, escalated: false },
		});
		renderSidebar();

		expect(await screen.findAllByLabelText("Restart to install update v9.9.9")).not.toHaveLength(0);
		expect(screen.getByTestId("sidebar-update-ready")).toBeVisible();
	});

	it("names the channel and build date for a staged nightly", async () => {
		// A raw nightly string truncates to noise in the sidebar, and two
		// consecutive nightlies differ only in the trailing digits.
		updateStatusMock.mockResolvedValue({
			state: "downloaded",
			version: "0.12.11-nightly.202609021713",
			stagedAt: Date.now(),
		});
		renderSidebar();

		const readyRow = await screen.findByTestId("sidebar-update-ready");
		expect(within(readyRow).getByText("Nightly 0.12.11 · Sep 2")).toBeVisible();
	});

	it("stays quiet for a one-off update failure that has not become a streak", async () => {
		updateStatusMock.mockResolvedValue({ state: "idle" });
		renderSidebar();

		await waitFor(() => expect(updateStatusMock).toHaveBeenCalled());
		expect(screen.queryByLabelText("Retry update check")).not.toBeInTheDocument();
		expect(screen.queryByText("Update check failed")).not.toBeInTheDocument();
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
		expect(button).toHaveClass("text-working");
		}
		expect(screen.getByText("v9.9.9 ready")).toBeInTheDocument();
	});

	it("commits a project drop", () => {
		renderSidebar({
			workspaces: [
				{ ...workspace, id: "alpha", name: "Alpha" },
				{ ...workspace, id: "bravo", name: "Bravo" },
			],
		});

		act(() => dragEnds.get("sidebar-projects")?.({ active: { id: "bravo" }, over: { id: "alpha" } }));

		expect(Array.from(document.querySelectorAll("[data-project-label]"), (node) => node.textContent)).toEqual(["Bravo", "Alpha"]);
	});

	it("pauses nested session drag contexts during a project drag", async () => {
		renderSidebar({
			workspaces: [
				{ ...workspace, id: "alpha", name: "Alpha", sessions: [{ ...session, id: "alpha-session", workspaceId: "alpha" }] },
				{ ...workspace, id: "bravo", name: "Bravo", sessions: [{ ...session, id: "bravo-session", workspaceId: "bravo" }] },
			],
		});

		expect(document.querySelectorAll('[data-dnd-context^="sidebar-sessions-"]')).toHaveLength(2);

		act(() => dragStarts.get("sidebar-projects")?.({ active: { id: "alpha" } }));

		await waitFor(() => expect(document.querySelectorAll('[data-dnd-context^="sidebar-sessions-"]')).toHaveLength(0));
		expect(screen.getAllByRole("button", { name: "Open fix login" })).toHaveLength(2);
	});

	it("commits a session drop within its project", () => {
		renderSidebar({
			workspaces: [{
				...workspace,
				sessions: [
					{ ...session, id: "first", title: "First", updatedAt: "2026-06-30T01:00:00Z" },
					{ ...session, id: "second", title: "Second", updatedAt: "2026-06-30T00:00:00Z" },
				],
			}],
		});

		act(() => dragEnds.get("sidebar-sessions-proj-1")?.({ active: { id: "second" }, over: { id: "first" } }));

		expect(Array.from(document.querySelectorAll('[data-testid="session-list-proj-1"] button[aria-label^="Open "]'), (node) => node.getAttribute("aria-label"))).toEqual([
			"Open Second",
			"Open First",
		]);
	});

	it("does not toggle disclosure from the click synthesized after a folder drag", () => {
		vi.useFakeTimers();
		try {
			renderSidebar({ workspaces: [{ ...workspace, id: "alpha", name: "Alpha" }] });
			const projectRow = screen.getByText("Alpha").closest("button");
			const initialDisclosure = projectRow?.getAttribute("aria-expanded");

			act(() => dragEnds.get("sidebar-projects")?.({ active: { id: "alpha" }, over: null }));
			act(() => fireEvent.click(screen.getByRole("button", { name: "Toggle Alpha sessions" })));

			expect(projectRow).toHaveAttribute("aria-expanded", initialDisclosure ?? "false");
		} finally {
			vi.useRealTimers();
		}
	});

	it("keeps reordered sessions in an expanded project drag preview", () => {
		renderSidebar({
			workspaces: [{
				...workspace,
				sessions: [
					{ ...session, id: "first", title: "First", updatedAt: "2026-06-30T01:00:00Z" },
					{ ...session, id: "second", title: "Second", updatedAt: "2026-06-30T00:00:00Z" },
				],
			}],
		});

		act(() => dragEnds.get("sidebar-sessions-proj-1")?.({ active: { id: "second" }, over: { id: "first" } }));
		act(() => dragStarts.get("sidebar-projects")?.({ active: { id: "proj-1" } }));

		const overlay = document.querySelector("[data-project-drag-overlay]");
		expect(overlay).toHaveTextContent(/Project One.*Second.*First/);
		expect(overlay?.querySelector("[data-project-drag-preview-session]")).toHaveClass("pl-0.5");
	});

	it("keeps hidden sessions out of compact project drag previews", () => {
		renderSidebar({
			initialOpen: false,
			workspaces: [{ ...workspace, sessions: [session] }],
		});

		act(() => dragStarts.get("sidebar-projects")?.({ active: { id: "proj-1" } }));

		const overlay = document.querySelector("[data-project-drag-overlay]");
		expect(overlay).toHaveTextContent("Project One");
		expect(overlay).not.toHaveTextContent("fix login");
	});

	it.each(["light", "dark"] as const)("uses a visible project drop indicator in the %s theme", (theme) => {
		document.documentElement.classList.toggle("dark", theme === "dark");
		try {
			renderSidebar({
				workspaces: [
					{ ...workspace, id: "alpha", name: "Alpha" },
					{ ...workspace, id: "bravo", name: "Bravo" },
				],
			});

			act(() => dragStarts.get("sidebar-projects")?.({ active: { id: "bravo" } }));
			act(() => dragOvers.get("sidebar-projects")?.({
				active: {
					id: "bravo",
					rect: { current: { initial: null, translated: null } },
				},
				activatorEvent: null,
				delta: { x: 0, y: 0 },
				over: { id: "alpha", rect: { height: 32, top: 0 } },
			}));

			const target = document.querySelector('[data-project-id="alpha"]');
			expect(target).toHaveAttribute("data-drop-indicator", "before");
			const indicator = target?.querySelector('[data-project-drop-indicator="before"]');
			expect(indicator).toHaveClass("bg-foreground");
			expect(indicator).not.toHaveClass("bg-white");
		} finally {
			document.documentElement.classList.remove("dark");
		}
	});
});
