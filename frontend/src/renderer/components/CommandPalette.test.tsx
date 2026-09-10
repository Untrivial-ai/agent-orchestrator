import { useState, type ReactNode } from "react";
import * as Popover from "@radix-ui/react-popover";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSummary } from "../types/workspace";
import { useUiStore } from "../stores/ui-store";

const navigateMock = vi.hoisted(() => vi.fn());
const spawnMock = vi.hoisted(() => vi.fn());
const choosePathMock = vi.hoisted(() => vi.fn());
const getMock = vi.hoisted(() => vi.fn());
const postMock = vi.hoisted(() => vi.fn());
const openExternalMock = vi.hoisted(() => vi.fn());
const writeTextMock = vi.hoisted(() => vi.fn());
const restoreMock = vi.hoisted(() => vi.fn());
const workspaceSubscriptionMock = vi.hoisted(() => vi.fn());
const createProjectFlowMock = vi.hoisted(() => ({
	props: null as null | {
		existingProjectPaths?: readonly string[];
		onOpenExistingProject?: (path: string) => void | Promise<void>;
	},
}));

const ctx = vi.hoisted(() => {
	const workspaces: WorkspaceSummary[] = [
		{
			id: "proj-1",
			name: "app",
			path: "/repos/app",
			type: "main",
			orchestratorAgent: "codex",
			sessions: [
				{
					id: "w-merge",
					workspaceId: "proj-1",
					workspaceName: "app",
					title: "ship banner",
					provider: "codex",
					kind: "worker",
					branch: "feature/ship",
					status: "mergeable",
					updatedAt: "2026-06-10T00:00:00Z",
					prs: [],
				},
				{
					id: "w-fix",
					workspaceId: "proj-1",
					workspaceName: "app",
					title: "fix flake",
					provider: "codex",
					kind: "worker",
					branch: "feature/fix",
					status: "working",
					updatedAt: "2026-06-10T00:00:00Z",
					prs: [],
				},
				{
					id: "w-archived",
					workspaceId: "proj-1",
					workspaceName: "app",
					title: "archived cleanup",
					provider: "codex",
					kind: "worker",
					branch: "feature/archived",
					status: "terminated",
					updatedAt: "2026-06-10T00:00:00Z",
					prs: [],
				},
				{
					id: "orch",
					workspaceId: "proj-1",
					workspaceName: "app",
					title: "orchestrate",
					provider: "codex",
					kind: "orchestrator",
					branch: "main",
					status: "working",
					updatedAt: "2026-06-10T00:00:00Z",
					prs: [],
				},
			],
		},
		{
			id: "proj-2",
			name: "lib",
			path: "/repos/lib",
			type: "main",
			orchestratorAgent: "codex",
			sessions: [],
		},
	];
	return {
		params: {} as { projectId?: string; sessionId?: string },
		enabled: true,
		workspaces,
	};
});

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
	useParams: () => ctx.params,
}));

vi.mock("../hooks/useCommandPaletteEnabled", () => ({
	useCommandPaletteEnabled: () => ctx.enabled,
	useAppVersion: () => "0.0.0-test",
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: (options: { subscribed?: boolean }) => {
		workspaceSubscriptionMock(options);
		return { data: ctx.workspaces };
	},
	workspaceQueryKey: ["workspaces"],
}));

vi.mock("../lib/shell-context", () => ({
	useShell: () => ({ cloneProject: vi.fn(), createProject: vi.fn(), initializeProjectRepository: vi.fn(), daemonStatus: {} }),
}));

vi.mock("../lib/spawn-orchestrator", () => ({ spawnOrchestrator: spawnMock }));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: getMock,
		POST: postMock,
	},
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		if (typeof error === "object" && error !== null && "message" in error) {
			return String((error as { message: unknown }).message);
		}
		return fallback;
	},
}));


vi.mock("../lib/bridge", () => ({
	aoBridge: {
		app: { openExternal: openExternalMock },
		clipboard: { writeText: writeTextMock },
	},
}));

vi.mock("../hooks/useRestoreSession", () => ({ useRestoreSession: () => restoreMock }));

function StubNestedLayer() {
	const [open, setOpen] = useState(false);
	return (
		<Popover.Root open={open} onOpenChange={setOpen}>
			<Popover.Trigger>stub-open-layer</Popover.Trigger>
			<Popover.Portal>
				<Popover.Content>nested layer</Popover.Content>
			</Popover.Portal>
		</Popover.Root>
	);
}

vi.mock("./TaskComposer", () => ({
	TaskComposer: (props: {
		projectId?: string;
		onCreated: (id: string) => void;
		onDirtyChange?: (dirty: boolean) => void;
		onSubmittingChange?: (submitting: boolean) => void;
	}) => (
		<div data-testid="task-composer">
			<span>composer {props.projectId}</span>
			<StubNestedLayer />
			<button type="button" onClick={() => props.onDirtyChange?.(true)}>
				stub-dirty
			</button>
			<button type="button" onClick={() => props.onSubmittingChange?.(true)}>
				stub-submit-start
			</button>
			<button type="button" onClick={() => props.onCreated("new-session")}>
				stub-create
			</button>
		</div>
	),
}));

vi.mock("./CreateProjectFlow", () => ({
	CreateProjectFlow: (props: {
		children: (state: { choosePath: () => void }) => ReactNode;
		existingProjectPaths?: readonly string[];
		onOpenExistingProject?: (path: string) => void | Promise<void>;
	}) => {
		createProjectFlowMock.props = props;
		return props.children({ choosePath: choosePathMock });
	},
}));

import { CommandPalette } from "./CommandPalette";

function renderPalette() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const wrap = () => (
		<QueryClientProvider client={queryClient}>
			<CommandPalette />
		</QueryClientProvider>
	);
	const result = render(wrap());
	return { ...result, rerenderPalette: () => result.rerender(wrap()) };
}

function pressKey(init: KeyboardEventInit): KeyboardEvent {
	const event = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
	act(() => {
		window.dispatchEvent(event);
	});
	return event;
}

function pressEscape(init: KeyboardEventInit = {}): KeyboardEvent {
	const event = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true, ...init });
	act(() => {
		document.body.dispatchEvent(event);
	});
	return event;
}

function focusTerminal() {
	const xterm = document.createElement("div");
	xterm.className = "xterm";
	const input = document.createElement("textarea");
	xterm.appendChild(input);
	document.body.appendChild(xterm);
	input.focus();
	return xterm;
}

const paletteInput = () => screen.queryByPlaceholderText(/search projects/i);

beforeEach(() => {
	ctx.params = {};
	ctx.enabled = true;
	ctx.workspaces[0].orchestratorAgent = "codex";
	ctx.workspaces[1].orchestratorAgent = "codex";
	ctx.workspaces[0].sessions[0].prs = [];
	navigateMock.mockReset();
	spawnMock.mockReset();
	choosePathMock.mockReset();
	getMock.mockReset();
	postMock.mockReset();
	openExternalMock.mockReset();
	writeTextMock.mockReset();
	restoreMock.mockReset();
	workspaceSubscriptionMock.mockReset();
	createProjectFlowMock.props = null;
	restoreMock.mockResolvedValue({ status: "success" });
	act(() => {
		useUiStore.setState({
			isCommandPaletteOpen: false,
			themePreference: "dark",
			resolvedTheme: "dark",
			restartingProjectIds: new Set(),
			settingsModal: null,
		});
	});
});

afterEach(() => {
	document.querySelectorAll(".xterm, [data-fake-modal]").forEach((node) => node.remove());
});

describe("CommandPalette gating", () => {
	it("subscribes to workspace updates only while open", () => {
		renderPalette();
		expect(workspaceSubscriptionMock).toHaveBeenLastCalledWith({ subscribed: false });

		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		expect(workspaceSubscriptionMock).toHaveBeenLastCalledWith({ subscribed: true });
	});

	it("renders nothing and binds no shortcut on a disabled (stable) build", () => {
		ctx.enabled = false;
		renderPalette();
		pressKey({ key: "k", metaKey: true });
		expect(paletteInput()).toBeNull();
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(false);
	});
});

describe("CommandPalette shortcut (Windows/Linux)", () => {
	it("opens on Ctrl+K and toggles closed on a second Ctrl+K", async () => {
		renderPalette();
		pressKey({ key: "k", ctrlKey: true });
		expect(await screen.findByPlaceholderText(/search projects/i)).toBeInTheDocument();
		pressKey({ key: "k", ctrlKey: true });
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("ignores Cmd+K off macOS (the opening modifier is platform-gated)", () => {
		renderPalette();
		const event = pressKey({ key: "k", metaKey: true });
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(false);
		expect(event.defaultPrevented).toBe(false);
	});

	it("yields Ctrl+K to a focused terminal (does not open, does not preventDefault)", () => {
		renderPalette();
		focusTerminal();
		const event = pressKey({ key: "k", ctrlKey: true });
		expect(event.defaultPrevented).toBe(false);
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(false);
	});

	it("does not open over an already-open modal", () => {
		renderPalette();
		const modal = document.createElement("div");
		modal.setAttribute("role", "dialog");
		modal.setAttribute("data-state", "open");
		modal.setAttribute("data-fake-modal", "");
		document.body.appendChild(modal);
		pressKey({ key: "k", ctrlKey: true });
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(false);
	});

	it.each([
		{ label: "Cmd", init: { key: "1", metaKey: true } satisfies KeyboardEventInit },
		{ label: "Ctrl", init: { key: "1", ctrlKey: true } satisfies KeyboardEventInit },
	])("swallows $label+digit project-switch while the palette is open", async ({ init }) => {
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		await screen.findByPlaceholderText(/search projects/i);
		expect(pressKey(init).defaultPrevented).toBe(true);
	});

	it("does not swallow Cmd+digit while the palette is closed (project switch still works)", () => {
		renderPalette();
		const event = pressKey({ key: "1", metaKey: true });
		expect(event.defaultPrevented).toBe(false);
	});
});

describe("CommandPalette shortcut (macOS)", () => {
	beforeEach(() => {
		vi.stubGlobal("navigator", {
			...globalThis.navigator,
			userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
		});
	});

	afterEach(() => vi.unstubAllGlobals());

	it("opens on Cmd+K", async () => {
		renderPalette();
		pressKey({ key: "k", metaKey: true });
		expect(await screen.findByPlaceholderText(/search projects/i)).toBeInTheDocument();
	});

	it("opens on Cmd+K even when a terminal is focused", async () => {
		renderPalette();
		focusTerminal();
		pressKey({ key: "k", metaKey: true });
		expect(await screen.findByPlaceholderText(/search projects/i)).toBeInTheDocument();
	});

	it("ignores Ctrl+K on macOS, leaving the system kill-to-end-of-line binding alone", () => {
		renderPalette();
		const event = pressKey({ key: "k", ctrlKey: true });
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(false);
		expect(event.defaultPrevented).toBe(false);
	});
});

describe("CommandPalette drill-in + Enter", () => {
	it("opens a session's actions panel, then jumps with a second Enter", async () => {
		ctx.params = {};
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);

		fireEvent.change(input, { target: { value: "ship" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("ship banner");
		});
		fireEvent.keyDown(input, { key: "Enter" });

		const actionsInput = await screen.findByPlaceholderText(/search actions/i);
		expect(navigateMock).not.toHaveBeenCalled();
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("Jump to session");
		});
		fireEvent.keyDown(actionsInput, { key: "Enter" });

		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId: "w-merge" },
		});
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("opens the highest-scoring match, not the first category", async () => {
		ctx.params = {};
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);

		// "app" is the exact project title but also a keyword hit on the Needs
		// attention rows, which render above Projects by default.
		fireEvent.change(input, { target: { value: "app" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.getAttribute("data-value")).toBe("project:proj-1");
		});
		fireEvent.keyDown(input, { key: "Enter" });

		expect(navigateMock).toHaveBeenCalledWith({ to: "/projects/$projectId", params: { projectId: "proj-1" } });
	});

	it("jumps to an archived (terminated) session via search + Enter", async () => {
		ctx.params = {};
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);

		fireEvent.change(input, { target: { value: "archived" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("archived cleanup");
		});
		fireEvent.keyDown(input, { key: "Enter" });

		await screen.findByPlaceholderText(/search actions/i);
		expect(screen.getByText("Resume agent")).toBeInTheDocument();
		expect(screen.getByText("Jump to session")).toBeInTheDocument();

		pressEscape();
		expect(await screen.findByPlaceholderText(/search projects/i)).toBeInTheDocument();
	});

	it("does not offer Resume for a live worker", async () => {
		ctx.params = {};
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);

		fireEvent.change(input, { target: { value: "flake" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("fix flake");
		});
		fireEvent.keyDown(input, { key: "Enter" });

		await screen.findByPlaceholderText(/search actions/i);
		expect(screen.queryByText("Resume agent")).toBeNull();
		expect(screen.getByText("Copy branch name")).toBeInTheDocument();
	});

	it("dispatches restore when Resume is selected and closes the palette on success", async () => {
		restoreMock.mockResolvedValue({ status: "success" });
		ctx.params = {};
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);
		fireEvent.change(input, { target: { value: "archived" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("archived cleanup");
		});
		fireEvent.keyDown(input, { key: "Enter" });
		await screen.findByPlaceholderText(/search actions/i);

		fireEvent.click(screen.getByText("Resume agent"));
		await waitFor(() => expect(restoreMock).toHaveBeenCalledWith("w-archived"));
		await waitFor(() =>
			expect(navigateMock).toHaveBeenCalledWith({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId: "proj-1", sessionId: "w-archived" },
			}),
		);
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("restates a not-resumable refusal instead of echoing the daemon envelope", async () => {
		restoreMock.mockResolvedValue({ status: "not_resumable", message: "SESSION_NOT_RESUMABLE" });
		ctx.params = {};
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);
		fireEvent.change(input, { target: { value: "archived" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("archived cleanup");
		});
		fireEvent.keyDown(input, { key: "Enter" });
		await screen.findByPlaceholderText(/search actions/i);

		fireEvent.click(screen.getByText("Resume agent"));
		expect(await screen.findByRole("alert")).toHaveTextContent(/no saved agent session or prompt/i);
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("surfaces an inline error and stays open when resume is rejected", async () => {
		restoreMock.mockResolvedValue({ status: "error", message: "Session is not restorable" });
		ctx.params = {};
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);
		fireEvent.change(input, { target: { value: "archived" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("archived cleanup");
		});
		fireEvent.keyDown(input, { key: "Enter" });
		await screen.findByPlaceholderText(/search actions/i);

		fireEvent.click(screen.getByText("Resume agent"));
		expect(await screen.findByRole("alert")).toHaveTextContent("not restorable");
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(true);
	});
});

describe("CommandPalette actions", () => {
	it("shows disabled New task reason, skips it for selection, and ignores clicks", async () => {
		ctx.params = {};
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		await screen.findByPlaceholderText(/search projects/i);
		expect(screen.getByText("No current project")).toBeInTheDocument();
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).not.toContain("No current project");
			expect(selected?.getAttribute("aria-disabled")).not.toBe("true");
		});
		fireEvent.click(screen.getByText("New task"));
		expect(navigateMock).not.toHaveBeenCalled();
		expect(spawnMock).not.toHaveBeenCalled();
	});

	it("does not spawn Open orchestrator while the project is restarting", async () => {
		ctx.params = { projectId: "proj-1" };
		act(() => useUiStore.setState({ restartingProjectIds: new Set(["proj-1"]) }));
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		await screen.findByPlaceholderText(/search projects/i);
		fireEvent.click(screen.getByText("Open orchestrator"));
		expect(spawnMock).not.toHaveBeenCalled();
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("opens project settings instead of spawning when no orchestrator agent is configured", async () => {
		ctx.params = { projectId: "proj-2" };
		ctx.workspaces[1].orchestratorAgent = undefined;
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		await screen.findByPlaceholderText(/search projects/i);
		fireEvent.click(screen.getByText("Open orchestrator"));

		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-2" });
		expect(navigateMock).not.toHaveBeenCalled();
		expect(spawnMock).not.toHaveBeenCalled();
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("navigates and closes when selecting a project", async () => {
		ctx.params = {};
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		await screen.findByPlaceholderText(/search projects/i);
		fireEvent.click(screen.getByText("lib"));
		expect(navigateMock).toHaveBeenCalledWith({ to: "/projects/$projectId", params: { projectId: "proj-2" } });
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("re-highlights the first result after the query changes", async () => {
		ctx.params = { projectId: "proj-1" };
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);
		fireEvent.change(input, { target: { value: "ship" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("ship banner");
		});
		fireEvent.change(input, { target: { value: "fix" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("fix flake");
		});
	});

	it("spawns only once when Open orchestrator is selected twice (in-flight guard)", async () => {
		ctx.params = { projectId: "proj-2" };
		spawnMock.mockReturnValueOnce(new Promise<string>(() => {}));
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		await screen.findByPlaceholderText(/search projects/i);
		const item = screen.getByText("Open orchestrator");
		fireEvent.click(item);
		fireEvent.click(item);
		expect(spawnMock).toHaveBeenCalledTimes(1);
	});

	it("keeps the palette open and shows an error when spawning an orchestrator fails", async () => {
		ctx.params = { projectId: "proj-2" };
		spawnMock.mockRejectedValueOnce(new Error("daemon down"));
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		await screen.findByPlaceholderText(/search projects/i);
		fireEvent.click(screen.getByText("Open orchestrator"));
		expect(await screen.findByRole("alert")).toHaveTextContent("daemon down");
		expect(spawnMock).toHaveBeenCalledWith("proj-2", "command_palette");
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(true);
	});

	it("toggles the theme and closes", async () => {
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);
		fireEvent.change(input, { target: { value: "toggle theme" } });
		fireEvent.keyDown(input, { key: "Enter" });
		expect(useUiStore.getState().resolvedTheme).toBe("light");
		expect(input).toHaveValue("toggle theme");
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("opens the new-project path picker and closes the palette", async () => {
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		await screen.findByPlaceholderText(/search projects/i);
		fireEvent.click(screen.getByText("New project"));
		await waitFor(() => expect(choosePathMock).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("opens an already registered project selected by the import flow", async () => {
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));

		expect(createProjectFlowMock.props?.existingProjectPaths).toEqual(["/repos/app", "/repos/lib"]);
		await act(async () => createProjectFlowMock.props?.onOpenExistingProject?.("/repos/lib"));

		expect(useUiStore.getState().isCommandPaletteOpen).toBe(false);
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId",
			params: { projectId: "proj-2" },
		});
	});
});

describe("CommandPalette PR and review actions", () => {
	const openPr = {
		url: "https://github.com/o/r/pull/7",
		number: 7,
		state: "open" as const,
		ci: "passing",
		review: "pending",
		mergeability: "clean",
		reviewComments: false,
		updatedAt: "2026-06-10T00:00:00Z",
	};

	const reviewState = (status: string) => ({
		prNumber: 7,
		prUrl: openPr.url,
		status,
		targetSha: "sha",
		title: "PR 7",
	});

	const mockReviews = (reviews: unknown[]) => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/reviews") {
				return { data: { reviewerHandleId: "", reviews } };
			}
			return { data: undefined };
		});
	};

	beforeEach(() => {
		ctx.workspaces[0].sessions[0].prs = [openPr];
		mockReviews([]);
		postMock.mockResolvedValue({ data: { reviewerHandleId: "", reviews: [] } });
	});

	async function openPaletteWithQuery(value: string) {
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);
		fireEvent.change(input, { target: { value } });
		return input;
	}

	it("opens the PR URL externally and closes", async () => {
		await openPaletteWithQuery("open pr");
		fireEvent.click(await screen.findByText("Open PR #7"));
		await waitFor(() => expect(openExternalMock).toHaveBeenCalledWith("https://github.com/o/r/pull/7"));
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("copies the PR URL to the clipboard and closes", async () => {
		await openPaletteWithQuery("copy pr");
		fireEvent.click(await screen.findByText("Copy PR URL #7"));
		await waitFor(() => expect(writeTextMock).toHaveBeenCalledWith("https://github.com/o/r/pull/7"));
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("triggers a review for the PR's session and closes", async () => {
		mockReviews([reviewState("needs_review")]);
		await openPaletteWithQuery("review");
		fireEvent.click(await screen.findByText("Review latest commit #7"));
		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/reviews/trigger", {
				params: { path: { sessionId: "w-merge" } },
			}),
		);
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("does not post when the review item is not eligible to run", async () => {
		await openPaletteWithQuery("review");
		expect(await screen.findByText("Not eligible for review")).toBeInTheDocument();
		fireEvent.click(screen.getByText("Run review #7"));
		expect(postMock).not.toHaveBeenCalled();
	});

	it("shows Not eligible for review once the session review state loads", async () => {
		await openPaletteWithQuery("review");
		expect(await screen.findByText("Not eligible for review")).toBeInTheDocument();
		fireEvent.click(screen.getByText("Run review #7"));
		expect(postMock).not.toHaveBeenCalled();
	});

	it("disables the review action with Review already running once the session review state loads", async () => {
		mockReviews([reviewState("running")]);
		await openPaletteWithQuery("review");
		expect(await screen.findByText("Review already running")).toBeInTheDocument();
		fireEvent.click(screen.getByText("Reviewing... #7"));
		expect(postMock).not.toHaveBeenCalled();
		expect(useUiStore.getState().isCommandPaletteOpen).toBe(true);
	});

	it("hides the review action until the session's review state loads, then keeps it stable for the rest of the open", async () => {
		let resolveReviews: (value: { data: { reviewerHandleId: string; reviews: unknown[] } }) => void = () => {};
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/reviews") {
				return new Promise((resolve) => {
					resolveReviews = resolve;
				});
			}
			return { data: undefined };
		});
		await openPaletteWithQuery("#7");
		// Open PR / Copy PR URL don't depend on review state; the mutating
		// review action must not render until we actually know it's safe.
		expect(await screen.findByText("Open PR #7")).toBeInTheDocument();
		expect(screen.queryByText(/run review|re-run review|reviewing/i)).toBeNull();
		await act(async () => {
			resolveReviews({ data: { reviewerHandleId: "", reviews: [reviewState("running")] } });
		});
		expect(await screen.findByText("Review already running")).toBeInTheDocument();
		// A later poll reflecting a finished review must not retitle the row —
		// this session is frozen for the remainder of this open.
		mockReviews([reviewState("up_to_date")]);
		await act(async () => {
			await Promise.resolve();
		});
		expect(screen.getByText("Reviewing... #7")).toBeInTheDocument();
		expect(screen.queryByText("Re-run review #7")).toBeNull();
	});

	it("does not expose Run review from stale cached data while a background refetch is settling", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		queryClient.setQueryData(
			["session-reviews", "w-merge"],
			{ reviewerHandleId: "", reviews: [reviewState("needs_review")] },
			// Force this cached value to look well past the palette's 60s
			// staleTime so opening the palette triggers a background refetch
			// while still returning this (now-outdated) data synchronously.
			{ updatedAt: Date.now() - 120_000 },
		);

		let resolveReviews: (value: { data: { reviewerHandleId: string; reviews: unknown[] } }) => void = () => {};
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/reviews") {
				return new Promise((resolve) => {
					resolveReviews = resolve;
				});
			}
			return { data: undefined };
		});

		render(
			<QueryClientProvider client={queryClient}>
				<CommandPalette />
			</QueryClientProvider>,
		);
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);
		fireEvent.change(input, { target: { value: "#7" } });

		// The stale cached "needs_review" data must not surface as an enabled
		// Run review action while the background refetch is still settling.
		expect(await screen.findByText("Open PR #7")).toBeInTheDocument();
		expect(screen.queryByText(/run review|re-run review/i)).toBeNull();

		await act(async () => {
			resolveReviews({ data: { reviewerHandleId: "", reviews: [reviewState("running")] } });
		});

		// Once the refetch settles with the true state, the row must reflect it.
		expect(await screen.findByText("Review already running")).toBeInTheDocument();
		fireEvent.click(screen.getByText("Reviewing... #7"));
		expect(postMock).not.toHaveBeenCalled();
	});

	it("does not expose Run review from stale cached data when a background refetch fails", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		queryClient.setQueryData(
			["session-reviews", "w-merge"],
			{ reviewerHandleId: "", reviews: [reviewState("needs_review")] },
			// Force this cached value to look well past the palette's 60s
			// staleTime so opening the palette triggers a background refetch
			// while still returning this (now-outdated) data synchronously.
			{ updatedAt: Date.now() - 120_000 },
		);
		let rejectReviews: (error: Error) => void = () => {};
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/sessions/{sessionId}/reviews") {
				return new Promise((_resolve, reject) => {
					rejectReviews = reject;
				});
			}
			return { data: undefined };
		});
		render(
			<QueryClientProvider client={queryClient}>
				<CommandPalette />
			</QueryClientProvider>,
		);
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);
		fireEvent.change(input, { target: { value: "#7" } });
		expect(await screen.findByText("Open PR #7")).toBeInTheDocument();
		expect(screen.queryByText(/run review|re-run review/i)).toBeNull();
		await act(async () => {
			rejectReviews(new Error("network error"));
		});
		// TanStack Query keeps the stale cached data and flips isFetching back
		// to false on a failed refetch — the stale "needs_review" value must
		// still not be treated as resolved, so the row must stay hidden rather
		// than reappearing as an enabled Run review.
		await waitFor(() => expect(getMock).toHaveBeenCalled());
		expect(screen.queryByText(/run review|re-run review/i)).toBeNull();
	});
});

describe("CommandPalette back navigation", () => {
	async function drillIntoTest() {
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		const input = await screen.findByPlaceholderText(/search projects/i);
		fireEvent.change(input, { target: { value: "archived" } });
		await waitFor(() => {
			const selected = document.querySelector('[cmdk-item][data-selected="true"]');
			expect(selected?.textContent).toContain("archived cleanup");
		});
		fireEvent.keyDown(input, { key: "Enter" });
		return screen.findByPlaceholderText(/search actions/i);
	}

	it("pops to root on Backspace at an empty query", async () => {
		ctx.params = {};
		const actionsInput = await drillIntoTest();
		fireEvent.keyDown(actionsInput, { key: "Backspace" });
		expect(await screen.findByPlaceholderText(/search projects/i)).toBeInTheDocument();
	});

	it("blocks a duplicate resume while one is in flight", async () => {
		restoreMock.mockReturnValueOnce(new Promise<never>(() => {}));
		ctx.params = {};
		await drillIntoTest();
		fireEvent.click(screen.getByText("Resume agent"));
		expect(restoreMock).toHaveBeenCalledTimes(1);
		fireEvent.click(screen.getByText("Resume agent"));
		expect(restoreMock).toHaveBeenCalledTimes(1);
	});

	it("still dismisses while a command hangs, and drops the late result", async () => {
		let reject: (err: Error) => void = () => undefined;
		restoreMock.mockReturnValueOnce(new Promise<never>((_resolve, r) => (reject = r)));
		ctx.params = {};
		await drillIntoTest();
		fireEvent.click(screen.getByText("Resume agent"));

		pressEscape();
		expect(await screen.findByPlaceholderText(/search projects/i)).toBeInTheDocument();

		await act(async () => {
			reject(new Error("daemon went away"));
			await Promise.resolve();
		});
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it("lets an IME consume Escape instead of popping the view", async () => {
		ctx.params = {};
		await drillIntoTest();
		const composing = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
		Object.defineProperty(composing, "isComposing", { value: true });
		act(() => {
			document.body.dispatchEvent(composing);
		});
		expect(screen.getByPlaceholderText(/search actions/i)).toBeInTheDocument();
		pressEscape();
		expect(await screen.findByPlaceholderText(/search projects/i)).toBeInTheDocument();
	});

	it("auto-pops to root when the scoped session disappears", async () => {
		ctx.params = {};
		const original = ctx.workspaces;
		try {
			const { rerenderPalette } = renderPalette();
			act(() => useUiStore.getState().setCommandPaletteOpen(true));
			const input = await screen.findByPlaceholderText(/search projects/i);
			fireEvent.change(input, { target: { value: "archived" } });
			await waitFor(() => {
				const selected = document.querySelector('[cmdk-item][data-selected="true"]');
				expect(selected?.textContent).toContain("archived cleanup");
			});
			fireEvent.keyDown(input, { key: "Enter" });
			await screen.findByPlaceholderText(/search actions/i);

			ctx.workspaces = original.map((w) => ({ ...w, sessions: w.sessions.filter((s) => s.id !== "w-archived") }));
			act(() => rerenderPalette());

			expect(await screen.findByPlaceholderText(/search projects/i)).toBeInTheDocument();
			expect(await screen.findByRole("alert")).toHaveTextContent(/no longer available/i);
		} finally {
			ctx.workspaces = original;
		}
	});
});

describe("CommandPalette inline task composer", () => {
	async function openComposer() {
		ctx.params = { projectId: "proj-1" };
		renderPalette();
		act(() => useUiStore.getState().setCommandPaletteOpen(true));
		await screen.findByPlaceholderText(/search projects/i);
		fireEvent.click(screen.getByText("New task"));
		return screen.findByTestId("task-composer");
	}

	it("opens the composer inline instead of a dialog", async () => {
		await openComposer();
		expect(screen.getByTestId("task-composer")).toHaveTextContent("proj-1");
		expect(screen.queryByTestId("new-task-dialog")).toBeNull();
	});

	it("navigates to the created session and closes on success", async () => {
		await openComposer();
		fireEvent.click(screen.getByText("stub-create"));
		await waitFor(() =>
			expect(navigateMock).toHaveBeenCalledWith({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId: "proj-1", sessionId: "new-session" },
			}),
		);
		await waitFor(() => expect(paletteInput()).toBeNull());
	});

	it("lets a layer opened inside the composer take Escape first", async () => {
		await openComposer();
		fireEvent.click(screen.getByText("stub-dirty"));
		fireEvent.click(screen.getByText("stub-open-layer"));
		expect(await screen.findByText("nested layer")).toBeInTheDocument();

		pressEscape();
		await waitFor(() => expect(screen.queryByText("nested layer")).toBeNull());
		expect(screen.queryByText(/discard this draft/i)).toBeNull();
		expect(screen.getByTestId("task-composer")).toBeInTheDocument();

		pressEscape();
		expect(await screen.findByText(/discard this draft/i)).toBeInTheDocument();
	});

	it("confirms before discarding a dirty draft", async () => {
		await openComposer();
		fireEvent.click(screen.getByText("stub-dirty"));
		pressEscape();
		expect(await screen.findByText(/discard this draft/i)).toBeInTheDocument();
		expect(screen.getByTestId("task-composer")).toBeInTheDocument();
	});

	it("blocks dismissal while a create is in flight, then navigates on completion", async () => {
		await openComposer();
		fireEvent.click(screen.getByText("stub-dirty"));
		fireEvent.click(screen.getByText("stub-submit-start"));
		pressEscape();
		expect(screen.queryByText(/discard this draft/i)).toBeNull();
		expect(screen.getByTestId("task-composer")).toBeInTheDocument();
		expect(navigateMock).not.toHaveBeenCalled();
		fireEvent.click(screen.getByText("stub-create"));
		await waitFor(() =>
			expect(navigateMock).toHaveBeenCalledWith({
				to: "/projects/$projectId/sessions/$sessionId",
				params: { projectId: "proj-1", sessionId: "new-session" },
			}),
		);
	});
});
