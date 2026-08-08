import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentSwitch } from "../hooks/useAgentSwitches";
import type { SwitchAgentInput } from "../hooks/useSwitchAgent";
import type { WorkspaceSession } from "../types/workspace";
import { isMacPlatform } from "../lib/platform";
import { CenterPane } from "./CenterPane";
import { TooltipProvider } from "./ui/tooltip";

const shortcutMocks = vi.hoisted(() => ({
	closeListener: undefined as (() => void) | undefined,
	nextTabListener: undefined as (() => void) | undefined,
	previousTabListener: undefined as (() => void) | undefined,
	closeableStates: [] as boolean[],
}));

const agentSwitchMocks = vi.hoisted(() => ({
	refetch: vi.fn(),
	switches: [] as AgentSwitch[],
	mutation: {
		error: null as string | null,
		input: undefined as SwitchAgentInput | undefined,
		isPending: false,
	},
}));

vi.mock("../hooks/useAgentSwitches", () => ({
	findActiveAgentSwitch: (switches: AgentSwitch[]) =>
		switches.find((agentSwitch) => agentSwitch.state !== "completed" && agentSwitch.state !== "failed"),
	useAgentSwitches: () => ({ data: agentSwitchMocks.switches, refetch: agentSwitchMocks.refetch }),
}));

vi.mock("../hooks/useSwitchAgent", () => ({
	useSwitchAgentState: () => agentSwitchMocks.mutation,
}));

vi.mock("./TerminalSwitchAgentButton", () => ({
	TerminalSwitchAgentButton: ({ session }: { session: WorkspaceSession }) => (
		<button aria-label="Switch agent" data-testid="terminal-switch-agent" type="button">
			{session.provider}
		</button>
	),
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		app: {
			setCloseShellTerminalShortcutEnabled: (enabled: boolean) => shortcutMocks.closeableStates.push(enabled),
			onCloseShellTerminalShortcut: (listener: () => void) => {
				shortcutMocks.closeListener = listener;
				return () => {
					if (shortcutMocks.closeListener === listener) shortcutMocks.closeListener = undefined;
				};
		},
			onPreviousTabShortcut: (listener: () => void) => {
				shortcutMocks.previousTabListener = listener;
				return () => {
					if (shortcutMocks.previousTabListener === listener) shortcutMocks.previousTabListener = undefined;
				};
			},
			onNextTabShortcut: (listener: () => void) => {
				shortcutMocks.nextTabListener = listener;
				return () => {
					if (shortcutMocks.nextTabListener === listener) shortcutMocks.nextTabListener = undefined;
				};
			},
		},
	},
}));

// The terminal body pulls in xterm/SSE machinery irrelevant to the header under test.
vi.mock("./TerminalPane", () => ({
	TerminalPane: ({ focusRequested }: { focusRequested?: boolean }) => (
		<div data-focus-requested={focusRequested ? "true" : "false"}>terminal body</div>
	),
}));

const worker = {
	id: "sess-1",
	workspaceId: "proj-1",
	workspaceName: "my-app",
	title: "do the thing",
	provider: "claude-code",
	kind: "worker",
	branch: "ao/sess-1",
	status: "working",
	updatedAt: "2026-06-10T00:00:00Z",
	activity: { state: "active", lastActivityAt: "2026-06-10T00:00:00Z" },
	prs: [],
} satisfies WorkspaceSession;

function renderCenterPane(props: Partial<ComponentProps<typeof CenterPane>> = {}) {
	return render(
		<TooltipProvider>
			<CenterPane daemonReady theme="dark" {...props} />
		</TooltipProvider>,
	);
}

beforeEach(() => {
	shortcutMocks.closeListener = undefined;
	shortcutMocks.nextTabListener = undefined;
	shortcutMocks.previousTabListener = undefined;
	shortcutMocks.closeableStates.length = 0;
	agentSwitchMocks.switches.length = 0;
	agentSwitchMocks.refetch.mockReset();
	agentSwitchMocks.refetch.mockResolvedValue(undefined);
	agentSwitchMocks.mutation.error = null;
	agentSwitchMocks.mutation.input = undefined;
	agentSwitchMocks.mutation.isPending = false;
});

describe("CenterPane toolbar session label", () => {
	const makeShells = (count: number) =>
		Array.from({ length: count }, (_, i) => ({
			handleId: `h-${i}`,
			title: `agent-orchestrator-${i}`,
			workingDir: "/tmp/ws",
			createdAt: "2026-07-22T00:00:00Z",
		}));

	it("shows the session display name for a worker", () => {
		renderCenterPane({ session: worker });
		expect(screen.getByText("do the thing")).toBeInTheDocument();
		expect(screen.queryByText("sess-1")).not.toBeInTheDocument();
		expect(screen.getByTestId("terminal-interaction-surface")).not.toHaveAttribute("inert");
		expect(screen.queryByTestId("agent-switch-terminal-overlay")).not.toBeInTheDocument();
	});

	it("locks only the terminal and shows the provider transfer as soon as a switch request starts", () => {
		agentSwitchMocks.mutation.input = {
			idempotencyKey: "switch-request-1",
			note: "",
			session: worker,
			targetHarness: "codex",
		};
		agentSwitchMocks.mutation.isPending = true;

		renderCenterPane({ session: worker });

		const overlay = screen.getByRole("status", { name: "Switching from Claude Code to Codex" });
		const terminalPanel = screen.getByRole("tabpanel", { name: "do the thing terminal" });
		expect(terminalPanel).toContainElement(overlay);
		expect(screen.getByTestId("terminal-interaction-surface")).toHaveAttribute("inert");
		expect(within(overlay).getByText("Claude Code")).toBeInTheDocument();
		expect(within(overlay).getByText("Codex")).toBeInTheDocument();
		expect(document.activeElement).toBe(screen.getByTestId("agent-switch-terminal-overlay"));
	});

	it("reopens terminal input when the source handoff needs a permission decision", () => {
		agentSwitchMocks.switches.push({
			agentHandoffStatus: "requested",
			fromHarness: "claude-code",
			id: "switch-2",
			requestedAt: "2026-06-10T00:00:00Z",
			sessionId: worker.id,
			state: "preparing_handoff",
			targetHarness: "codex",
			updatedAt: "2026-06-10T00:00:01Z",
		});

		renderCenterPane({
			session: {
				...worker,
				activity: { state: "waiting_input", lastActivityAt: "2026-06-10T00:00:02Z" },
			},
		});

		expect(screen.getByTestId("terminal-interaction-surface")).not.toHaveAttribute("inert");
		expect(screen.getByText("terminal body")).toHaveAttribute("data-focus-requested", "true");
		expect(
			screen.getByText(
				"The source agent requires a permission decision. Review the terminal prompt to continue the handoff.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Cancel switch" })).not.toBeInTheDocument();
	});

	it("renders only this session's own tab, never a sibling session", () => {
		renderCenterPane({ session: worker });

		const sessionTab = screen.getByRole("tab", { name: /^do the thing/ });
		expect(sessionTab).toHaveAttribute("aria-selected", "true");
		expect(sessionTab.parentElement).toHaveClass(
			"self-stretch",
			"bg-overlay",
			"after:h-0.5",
			"after:bg-foreground/80",
		);
		expect(sessionTab.parentElement).not.toHaveClass("session-primary-tab");
		expect(sessionTab.parentElement).not.toHaveClass("rounded-md");
		expect(sessionTab).toHaveAccessibleName("do the thing · Working");
		expect(sessionTab.querySelector('[title="Working"]')).toBeInTheDocument();
		expect(sessionTab.parentElement?.querySelector('img[aria-hidden="true"]')).toBeInTheDocument();
		expect(screen.queryByRole("tab", { name: "review the change" })).not.toBeInTheDocument();
	});

	it("keeps the main agent tab permanent, prominent, and solely branded by the harness", () => {
		const [shell] = makeShells(1);
		renderCenterPane({
			session: worker,
			shellTerminals: [shell],
			terminalTarget: {
				generation: shell.createdAt,
				kind: "shell",
				handleId: shell.handleId,
				title: shell.title,
			},
		});

		const mainTab = screen.getByRole("tab", { name: /^do the thing/ });
		const mainContainer = mainTab.parentElement;
		expect(mainContainer).toHaveAttribute("data-terminal-role", "primary");
		expect(mainContainer).toHaveClass("self-stretch", "bg-surface");
		expect(mainContainer).not.toHaveClass("session-primary-tab");
		expect(mainContainer).not.toHaveClass("rounded-md");
		expect(mainContainer).not.toHaveClass("before:bg-accent");
		expect(
			within(mainContainer as HTMLElement).queryByRole("button", {
				name: /close/i,
			}),
		).not.toBeInTheDocument();
		expect(mainContainer?.querySelector('img[aria-hidden="true"]')).toBeInTheDocument();

		const auxiliaryTab = screen.getByRole("tab", { name: shell.title });
		expect(auxiliaryTab.parentElement?.querySelector("img")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: `Close terminal ${shell.title}` })).toBeInTheDocument();
		expect(mainTab.querySelector('[title="Working"]')).toHaveClass("self-center");
		expect(mainTab.querySelector('[title="Working"]')).not.toHaveClass("-translate-y-px");
		expect(within(mainContainer as HTMLElement).getByTestId("terminal-switch-agent")).toBeInTheDocument();
	});

	it("closes only the selected auxiliary terminal from the application shortcut", () => {
		const [shell] = makeShells(1);
		const onCloseShellTerminal = vi.fn();
		renderCenterPane({
			session: worker,
			shellTerminals: [shell],
			terminalTarget: {
				generation: shell.createdAt,
				kind: "shell",
				handleId: shell.handleId,
				title: shell.title,
			},
			onCloseShellTerminal,
		});

		act(() => shortcutMocks.closeListener?.());
		expect(onCloseShellTerminal).toHaveBeenCalledWith(shell.handleId);
	});

	it("keeps the permanent main terminal open when the close shortcut fires", () => {
		const onCloseShellTerminal = vi.fn();
		renderCenterPane({ session: worker, onCloseShellTerminal });

		act(() => shortcutMocks.closeListener?.());
		expect(onCloseShellTerminal).not.toHaveBeenCalled();
	});

	it("cycles from the session terminal to its next shell tab", () => {
		const [shell] = makeShells(1);
		const onSelectShellTerminal = vi.fn();
		renderCenterPane({ session: worker, shellTerminals: [shell], onSelectShellTerminal });

		act(() => shortcutMocks.nextTabListener?.());
		expect(onSelectShellTerminal).toHaveBeenCalledWith(shell.handleId);
	});

	it("wraps from a shell tab to the session terminal", () => {
		const [shell] = makeShells(1);
		const onSelectSessionTerminal = vi.fn();
		renderCenterPane({
			session: worker,
			shellTerminals: [shell],
			terminalTarget: { generation: shell.createdAt, kind: "shell", handleId: shell.handleId, title: shell.title },
			onSelectSessionTerminal,
		});

		act(() => shortcutMocks.nextTabListener?.());
		expect(onSelectSessionTerminal).toHaveBeenCalledOnce();
	});

	it("enables the global close shortcut only while a closeable shell is active", () => {
		const [shell] = makeShells(1);
		const view = renderCenterPane({
			session: worker,
			shellTerminals: [shell],
			terminalTarget: {
				generation: shell.createdAt,
				kind: "shell",
				handleId: shell.handleId,
				title: shell.title,
			},
			onCloseShellTerminal: vi.fn(),
		});

		expect(shortcutMocks.closeableStates.at(-1)).toBe(true);
		view.unmount();
		expect(shortcutMocks.closeableStates.at(-1)).toBe(false);
	});

	it("shows reviewer as its own active harness tab", () => {
		renderCenterPane({
			session: worker,
			reviewerTerminal: { handleId: "review-sess-1", harness: "codex" },
			terminalTarget: { kind: "reviewer", handleId: "review-sess-1", harness: "codex", sessionId: worker.id },
		});

		expect(screen.getByRole("tab", { name: "Reviewer" })).toHaveAttribute("aria-current", "true");
		expect(screen.getByRole("tab", { name: /^do the thing/ })).not.toHaveAttribute("aria-current", "true");
		expect(screen.getByRole("tab", { name: "Reviewer" }).querySelector("img")).toHaveAttribute("src");
		expect(screen.queryByRole("button", { name: "Back to agent" })).not.toBeInTheDocument();
	});

	it("opens reviewer from the tab strip when a reviewer handle exists", () => {
		const onSelectReviewerTerminal = vi.fn();
		renderCenterPane({
			session: worker,
			reviewerTerminal: { handleId: "review-sess-1", harness: "codex" },
			onSelectReviewerTerminal,
		});

		fireEvent.click(screen.getByRole("tab", { name: "Reviewer" }));
		expect(onSelectReviewerTerminal).toHaveBeenCalledWith({ handleId: "review-sess-1", harness: "codex" });
	});

	// The button used to open a dropdown that also listed every session across
	// every project (#3208); it now only ever creates a terminal.
	it("opens a new terminal straight from the tab-strip button", () => {
		const onNewShellTerminal = vi.fn();
		renderCenterPane({ session: worker, onNewShellTerminal });

		const newTerminalButton = screen.getByRole("button", { name: "New terminal" });
		expect(newTerminalButton).toHaveClass("size-control-md", "border", "border-border");
		expect(screen.queryByText("New terminal")).not.toBeInTheDocument();
		fireEvent.click(newTerminalButton);
		expect(onNewShellTerminal).toHaveBeenCalledOnce();
		expect(screen.queryByRole("menu")).not.toBeInTheDocument();
	});

	it("explains compact controls with tooltips", async () => {
		const user = userEvent.setup();
		renderCenterPane({ session: worker, onNewShellTerminal: vi.fn() });

		await user.hover(screen.getByRole("button", { name: "New terminal" }));
		expect(await screen.findByRole("tooltip")).toHaveTextContent(
			isMacPlatform() ? "New terminal (⌘T)" : "New terminal (Ctrl+T)",
		);
	});

	it("shows 'Orchestrator' for an orchestrator session", () => {
		renderCenterPane({
			session: { ...worker, id: "sess-orch", kind: "orchestrator" },
		});
		expect(screen.getByText("Orchestrator")).toBeInTheDocument();
	});

	it("does not offer a new terminal for an orchestrator session", () => {
		renderCenterPane({
			session: { ...worker, id: "sess-orch", kind: "orchestrator" },
			onNewShellTerminal: vi.fn(),
		});

		expect(screen.queryByRole("button", { name: "New terminal" })).not.toBeInTheDocument();
	});

	it("shows 'No session' when there is no session", () => {
		renderCenterPane();
		expect(screen.getByText("No session")).toBeInTheDocument();
	});

	it("uses the inspector tab height for the terminal header", () => {
		renderCenterPane({ session: worker });

		const tablist = screen.getByRole("tablist", { name: "Open terminals" });
		const header = tablist.closest(".h-inspector-tabs");
		expect(header).toHaveClass("h-inspector-tabs");
		expect(tablist.parentElement).toHaveClass("h-full");
	});

	it("keeps terminal controls in the measured terminal region and session actions outside it", () => {
		renderCenterPane({
			session: worker,
			onNewShellTerminal: vi.fn(),
			topbarActions: <button type="button">Session action</button>,
		});

		const terminalRegion = screen.getByTestId("session-terminal-region");
		const workspaceTopbar = screen.getByTestId("session-workspace-topbar");
		expect(workspaceTopbar).toHaveClass("session-topbar-surface");
		expect(workspaceTopbar).toContainElement(terminalRegion);
		expect(terminalRegion).toContainElement(screen.getByRole("tablist", { name: "Open terminals" }));
		expect(terminalRegion).toContainElement(screen.getByRole("button", { name: "New terminal" }));
		expect(terminalRegion).toContainElement(screen.getByRole("toolbar", { name: "Terminal display controls" }));
		expect(terminalRegion).not.toContainElement(screen.getByTestId("session-action-region"));
		const actionRegion = screen.getByTestId("session-action-region");
		expect(actionRegion).not.toHaveClass("border-l");
		expect(actionRegion).toContainElement(
			screen.getByRole("button", { name: "Session action" }),
		);
	});

	it("hides session-level actions while the terminal is fullscreen", () => {
		const view = renderCenterPane({
			session: worker,
			topbarActions: <button type="button">Session action</button>,
		});
		const pane = view.container.querySelector(".terminal-pane-frame");

		Object.defineProperty(document, "fullscreenElement", { configurable: true, value: pane });
		act(() => document.dispatchEvent(new Event("fullscreenchange")));

		expect(screen.queryByTestId("session-action-region")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Exit fullscreen" })).toBeInTheDocument();
		Object.defineProperty(document, "fullscreenElement", { configurable: true, value: null });
	});

	it("does not reserve tab-strip space for unavailable overflow controls", () => {
		renderCenterPane({ session: worker });

		expect(screen.getByTestId("session-terminal-region")).not.toHaveClass("pl-0.5");
		expect(screen.getByRole("tablist", { name: "Open terminals" })).not.toHaveClass("pt-1.5");
		expect(screen.queryByRole("button", { name: "Scroll tabs left" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Scroll tabs right" })).not.toBeInTheDocument();
	});

	it("lets tabs shrink into a scrollable strip instead of overflowing onto the controls", () => {
		const shells = makeShells(8);
		renderCenterPane({ session: worker, shellTerminals: shells });

		const scrollRegion = document.querySelector(".overflow-x-auto");
		expect(scrollRegion).toHaveClass("scrollbar-none", "min-w-flex-min", "flex-1");
		for (const tab of screen.getAllByTitle(/^\/tmp\/ws/)) {
			expect(tab.parentElement).toHaveClass(
				"min-w-shell-tab-min",
				"w-shell-tab-connected",
			);
			expect(tab.parentElement).not.toHaveClass("min-w-16", "shrink-0");
			expect(tab).toHaveClass("min-w-0", "w-full");
		}
		// jsdom reports no overflow, so no unavailable control reserves header space.
		expect(screen.queryByRole("button", { name: "Scroll tabs right" })).not.toBeInTheDocument();

		// The display controls now complete the top bar, but stay outside the
		// flexible scroll region so a long tab list cannot overlap them.
		const tabList = screen.getByRole("tablist", { name: "Open terminals" });
		const toolbar = screen.getByRole("toolbar", {
			name: "Terminal display controls",
		});
		expect(tabList.contains(toolbar)).toBe(false);
		expect(toolbar).toContainElement(screen.getByRole("button", { name: "Fullscreen terminal" }));
	});

	it("reveals scroll chevrons only when the tab strip actually overflows", () => {
		const shells = makeShells(8);
		renderCenterPane({ session: worker, shellTerminals: shells });

		const scrollRegion = document.querySelector(".overflow-x-auto") as HTMLElement;
		Object.defineProperty(scrollRegion, "clientWidth", {
			value: 100,
			configurable: true,
		});
		Object.defineProperty(scrollRegion, "scrollWidth", {
			value: 500,
			configurable: true,
		});
		fireEvent.scroll(scrollRegion);

		expect(screen.getByRole("button", { name: "Scroll tabs right" })).toBeEnabled();
		expect(screen.queryByRole("button", { name: "Scroll tabs left" })).not.toBeInTheDocument();

		Object.defineProperty(scrollRegion, "scrollLeft", {
			value: 400,
			configurable: true,
		});
		fireEvent.scroll(scrollRegion);
		expect(screen.getByRole("button", { name: "Scroll tabs left" })).toBeEnabled();
		expect(screen.queryByRole("button", { name: "Scroll tabs right" })).not.toBeInTheDocument();
	});

	it("scrolls the tab strip horizontally with the mouse wheel", () => {
		const shells = makeShells(8);
		renderCenterPane({ session: worker, shellTerminals: shells });

		const scrollRegion = document.querySelector(".overflow-x-auto") as HTMLElement;
		Object.defineProperty(scrollRegion, "clientWidth", {
			value: 100,
			configurable: true,
		});
		Object.defineProperty(scrollRegion, "scrollWidth", {
			value: 500,
			configurable: true,
		});
		const scrollBy = vi.fn();
		Object.defineProperty(scrollRegion, "scrollBy", {
			value: scrollBy,
			configurable: true,
		});

		fireEvent.wheel(scrollRegion, { deltaY: 80 });
		expect(scrollBy).toHaveBeenCalledWith({ left: 80 });

		// Ctrl+wheel is terminal font zoom, not tab scrolling.
		scrollBy.mockClear();
		fireEvent.wheel(scrollRegion, { deltaY: 80, ctrlKey: true });
		expect(scrollBy).not.toHaveBeenCalled();
	});

	it("uses roving keyboard focus to select terminal tabs", () => {
		const shells = makeShells(2);
		const onSelectShellTerminal = vi.fn();
		const onSelectSessionTerminal = vi.fn();
		const onRenameShellTerminal = vi.fn();
		renderCenterPane({
			session: worker,
			shellTerminals: shells,
			onSelectSessionTerminal,
			onSelectShellTerminal,
			onRenameShellTerminal,
		});

		const sessionTab = screen.getByRole("tab", { name: /^do the thing/ });
		const firstShellTab = screen.getByRole("tab", {
			name: "agent-orchestrator-0",
		});
		expect(sessionTab).toHaveAttribute("tabindex", "0");
		expect(firstShellTab).toHaveAttribute("tabindex", "-1");

		sessionTab.focus();
		fireEvent.keyDown(sessionTab, { key: "ArrowRight" });
		expect(firstShellTab).toHaveFocus();
		expect(onSelectShellTerminal).toHaveBeenCalledWith("h-0");

		fireEvent.keyDown(firstShellTab, { key: "Home" });
		expect(sessionTab).toHaveFocus();
		expect(onSelectSessionTerminal).toHaveBeenCalledOnce();

		// Revisiting a tab quickly by keyboard must not count as a double-click
		// and enter rename mode.
		fireEvent.keyDown(sessionTab, { key: "ArrowRight" });
		expect(firstShellTab).toHaveFocus();
		expect(screen.queryByRole("textbox", { name: /rename terminal/i })).not.toBeInTheDocument();
	});
});
