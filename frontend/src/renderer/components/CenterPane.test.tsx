import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps, ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";
import { CenterPane } from "./CenterPane";
import { TooltipProvider } from "./ui/tooltip";

const shortcutMocks = vi.hoisted(() => ({
	closeListener: undefined as (() => void) | undefined,
	nextTabListener: undefined as (() => void) | undefined,
	previousTabListener: undefined as (() => void) | undefined,
	closeableStates: [] as boolean[],
}));

const reorderMocks = vi.hoisted(() => ({
	onReorder: undefined as ((values: string[]) => void) | undefined,
}));

const renameSessionMock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));

vi.mock("../lib/rename-session", () => ({ renameSession: renameSessionMock }));

vi.mock("motion/react", () => ({
	Reorder: {
		Group: ({ children, onReorder }: { children: ReactNode; onReorder: (values: string[]) => void }) => {
			reorderMocks.onReorder = onReorder;
			return <div data-testid="reorderable-terminal-tabs">{children}</div>;
		},
		Item: ({ children, value }: { children: ReactNode; value: string }) => (
			<div data-terminal-tab-key={value}>{children}</div>
		),
	},
	useDragControls: () => ({ start: vi.fn() }),
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
	TerminalPane: (props: {
		focusRequested?: boolean;
		inputDisabled?: boolean;
	}) => {
		return (
			<div
				data-focus-requested={props.focusRequested ? "true" : "false"}
				data-input-disabled={props.inputDisabled ? "true" : "false"}
			>
				terminal body
			</div>
		);
	},
}));

const worker = {
	id: "sess-1",
	workspaceId: "proj-1",
	workspaceName: "my-app",
	title: "do the thing",
	provider: "opencode",
	kind: "worker",
	branch: "ao/sess-1",
	status: "working",
	updatedAt: "2026-06-10T00:00:00Z",
	activity: { state: "active", lastActivityAt: "2026-06-10T00:00:00Z" },
	prs: [],
} satisfies WorkspaceSession;

function renderCenterPane(props: Partial<ComponentProps<typeof CenterPane>> = {}) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<TooltipProvider>
			<CenterPane daemonReady theme="dark" {...props} />
		</TooltipProvider>,
		{
			wrapper: ({ children }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>,
		},
	);
}

beforeEach(() => {
	shortcutMocks.closeListener = undefined;
	shortcutMocks.nextTabListener = undefined;
	shortcutMocks.previousTabListener = undefined;
	shortcutMocks.closeableStates.length = 0;
	reorderMocks.onReorder = undefined;
	renameSessionMock.mockReset().mockResolvedValue(undefined);
});

describe("CenterPane toolbar session label", () => {
	const makeShells = (count: number) =>
		Array.from({ length: count }, (_, i) => ({
			handleId: `h-${i}`,
			title: `agent-orchestrator-${i}`,
			workingDir: "/tmp/ws",
			createdAt: "2026-07-22T00:00:00Z",
		}));

	it("shows the session display name while naming the harness accessibly", () => {
		renderCenterPane({ session: worker });
		expect(screen.getByText("do the thing")).toBeInTheDocument();
		expect(screen.queryByText("Claude Code")).not.toBeInTheDocument();
		expect(screen.queryByText("sess-1")).not.toBeInTheDocument();
		expect(screen.getByTestId("terminal-interaction-surface")).not.toHaveAttribute("inert");
		expect(screen.queryByTestId("agent-switch-terminal-overlay")).not.toBeInTheDocument();
	});

	it("renames the owning session from a double-click on its terminal tab", async () => {
		const user = userEvent.setup();
		const onSelectSessionTerminal = vi.fn();
		renderCenterPane({ session: worker, onSelectSessionTerminal });

		await user.dblClick(screen.getByRole("tab", { name: /^do the thing/ }));
		const input = screen.getByRole("textbox", { name: "Rename do the thing" });
		await user.clear(input);
		await user.type(input, "  clearer task  {Enter}");

		await waitFor(() => expect(renameSessionMock).toHaveBeenCalledWith("sess-1", "clearer task"));
		expect(onSelectSessionTerminal).toHaveBeenCalledOnce();
	});

	it("starts owning-session rename from its context menu", async () => {
		const user = userEvent.setup();
		const onSelectSessionTerminal = vi.fn();
		renderCenterPane({ session: worker, onSelectSessionTerminal });

		fireEvent.contextMenu(screen.getByRole("tab", { name: /^do the thing/ }));
		const renameItem = await screen.findByRole("menuitem", { name: "Rename do the thing" });
		expect(renameItem).toHaveTextContent(/^Rename$/);
		expect(renameItem.querySelector("svg")).toBeInTheDocument();
		await user.click(renameItem);

		expect(screen.getByRole("textbox", { name: "Rename do the thing" })).toHaveFocus();
		expect(onSelectSessionTerminal).not.toHaveBeenCalled();
	});

	it("cancels owning-session rename with Escape", async () => {
		const user = userEvent.setup();
		renderCenterPane({ session: worker });

		const tab = screen.getByRole("tab", { name: /^do the thing/ });
		tab.focus();
		await user.keyboard("{F2}");
		const input = screen.getByRole("textbox", { name: "Rename do the thing" });
		await user.clear(input);
		await user.type(input, "discard this{Escape}");

		expect(renameSessionMock).not.toHaveBeenCalled();
		expect(screen.getByRole("tab", { name: /^do the thing/ })).toBeInTheDocument();
	});

	it.each(["", "do the thing"])("does not persist the no-op terminal-tab rename %j", async (nextName) => {
		const user = userEvent.setup();
		renderCenterPane({ session: worker });

		await user.dblClick(screen.getByRole("tab", { name: /^do the thing/ }));
		const input = screen.getByRole("textbox", { name: "Rename do the thing" });
		expect(input).toHaveAttribute("maxlength", "20");
		await user.clear(input);
		if (nextName) await user.type(input, nextName);
		await user.keyboard("{Enter}");

		expect(renameSessionMock).not.toHaveBeenCalled();
		expect(screen.getByRole("tab", { name: /^do the thing/ })).toBeInTheDocument();
	});

	it("renders only this session's own tab, never a sibling session", () => {
		renderCenterPane({ session: worker });

		const sessionTab = screen.getByRole("tab", { name: /^do the thing/ });
		const sessionFrame = sessionTab.closest("[data-terminal-tab-frame]");
		expect(sessionTab).toHaveAttribute("aria-selected", "true");
		expect(sessionFrame).toHaveClass(
			"self-stretch",
			"border-border",
			"bg-overlay",
		);
		expect(sessionFrame).not.toHaveClass("session-primary-tab", "rounded-md");
		expect(sessionTab).toHaveAccessibleName("do the thing · OpenCode · Working");
		expect(sessionTab.querySelector('[title="Working"]')).not.toBeInTheDocument();
		expect(sessionTab.querySelector('img[aria-hidden="true"]')).toBeInTheDocument();
		expect(screen.queryByRole("tab", { name: "review the change" })).not.toBeInTheDocument();
	});

	it("places the active session indicator along the bottom edge", () => {
		renderCenterPane({ session: worker });

		const indicator = screen.getByTestId("active-terminal-tab-indicator");
		expect(indicator).toHaveClass("bottom-0", "h-0.5");
	});

	it("keeps the main agent tab permanent while the avatar visually signals the harness", () => {
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
		const mainContainer = mainTab.closest("[data-terminal-tab-frame]");
		expect(mainContainer).toHaveAttribute("data-terminal-role", "primary");
		expect(mainContainer).toHaveClass("self-stretch", "w-shell-tab-connected");
		expect(mainContainer).not.toHaveClass("bg-surface");
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
		expect(mainTab.querySelector('[title="Working"]')).not.toBeInTheDocument();
		expect(within(mainContainer as HTMLElement).queryByTestId("terminal-switch-agent")).toBeNull();
	});

	it("keeps the owner tab in the scrollable terminal list", () => {
		const [shell] = makeShells(1);
		renderCenterPane({ session: worker, shellTerminals: [shell] });

		const ownerTab = screen.getByRole("tab", { name: /^do the thing/ });
		const ownerCard = ownerTab.closest("[data-terminal-tab-frame]");
		const scrollRegion = document.querySelector(".session-tab-scroll-region");
		const avatar = ownerCard?.querySelector('img[aria-hidden="true"]');

		expect(ownerCard).toHaveClass("min-w-shell-tab-min", "shrink-0");
		expect(ownerCard).not.toHaveClass("w-full", "max-w-full");
		expect(ownerTab).not.toHaveClass("min-w-flex-min");
		expect(scrollRegion?.contains(ownerCard)).toBe(true);
		expect(avatar?.classList.contains("size-terminal-agent-icon")).toBe(true);
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

	it("closes the selected workspace file from the application shortcut", () => {
		const onClose = vi.fn();
		renderCenterPane({
			session: worker,
			workspaceActiveTabKey: "file:README.md",
			workspaceTabs: [
				{
					key: "file:README.md",
					content: <button role="tab">README.md</button>,
					onSelect: vi.fn(),
					onClose,
				},
			],
		});

		expect(shortcutMocks.closeableStates.at(-1)).toBe(true);
		act(() => shortcutMocks.closeListener?.());
		expect(onClose).toHaveBeenCalledOnce();
	});

	it("removes the active highlight from a shell while a workspace file is selected", () => {
		const [shell] = makeShells(1);
		renderCenterPane({
			session: worker,
			shellTerminals: [shell],
			terminalTarget: { generation: shell.createdAt, kind: "shell", handleId: shell.handleId, title: shell.title },
			workspaceActiveTabKey: "file:README.md",
			workspaceTabs: [
				{
					key: "file:README.md",
					content: <button aria-selected="true" role="tab">README.md</button>,
					onSelect: vi.fn(),
				},
			],
		});

		expect(screen.getByRole("tab", { name: shell.title })).toHaveAttribute("aria-selected", "false");
		expect(screen.getByRole("tab", { name: "README.md" })).toHaveAttribute("aria-selected", "true");
	});

	it("removes the active highlight from the reviewer while a workspace file is selected", () => {
		renderCenterPane({
			session: worker,
			reviewerTerminal: { handleId: "review-sess-1", harness: "opencode" },
			terminalTarget: { kind: "reviewer", handleId: "review-sess-1", harness: "opencode", sessionId: worker.id },
			workspaceActiveTabKey: "file:README.md",
			workspaceTabs: [
				{
					key: "file:README.md",
					content: <button aria-selected="true" role="tab">README.md</button>,
					onSelect: vi.fn(),
				},
			],
		});

		expect(screen.getByRole("tab", { name: "Reviewer" })).toHaveAttribute("aria-selected", "false");
		expect(screen.getByRole("tab", { name: "README.md" })).toHaveAttribute("aria-selected", "true");
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
		const [shell] = makeShells(1);
		renderCenterPane({
			session: worker,
			reviewerTerminal: { handleId: "review-sess-1", harness: "opencode" },
			shellTerminals: [shell],
			terminalTarget: { kind: "reviewer", handleId: "review-sess-1", harness: "opencode", sessionId: worker.id },
		});

		const reviewerTab = screen.getByRole("tab", { name: "Reviewer" });
		const shellTab = screen.getByRole("tab", { name: shell.title });
		expect(reviewerTab).toHaveAttribute("aria-current", "true");
		expect(reviewerTab.querySelector("img")).toHaveClass("size-terminal-agent-icon");
		expect(reviewerTab.closest("[data-terminal-tab-frame]")).toHaveClass(
			"min-w-shell-tab-min",
			"shrink-0",
			"self-stretch",
			"w-shell-tab-connected",
			"border-r",
			"border-border",
			"bg-overlay",
		);
		expect(shellTab.closest("[data-terminal-tab-frame]")).toHaveClass(
			"shrink-0",
			"self-stretch",
			"max-w-shell-tab-max",
		);
		expect(shellTab.closest("[data-terminal-tab-frame]")).not.toHaveClass("w-shell-tab-connected");
		expect(reviewerTab.closest("[data-terminal-tab-frame]")).not.toHaveAttribute("data-terminal-role", "primary");
		expect(screen.getByRole("tab", { name: /^do the thing/ })).not.toHaveAttribute("aria-current", "true");
		expect(reviewerTab.querySelector("img")).toHaveAttribute("src");
		expect(screen.queryByRole("button", { name: "Back to agent" })).not.toBeInTheDocument();
	});

	it("makes the full reviewer tile the interactive selection target", () => {
		const onSelectReviewerTerminal = vi.fn();
		renderCenterPane({
			session: worker,
			reviewerTerminal: { handleId: "review-sess-1", harness: "opencode" },
			onSelectReviewerTerminal,
		});

		const reviewerTab = screen.getByRole("tab", { name: "Reviewer" });
		expect(reviewerTab.closest("[data-terminal-tab-frame]")).toHaveClass("self-stretch", "w-shell-tab-connected");
		expect(reviewerTab).toHaveClass("px-2", "cursor-pointer");
		expect(reviewerTab).toHaveClass("focus-visible:outline-2", "focus-visible:outline-accent/50");
		expect(reviewerTab.parentElement).not.toHaveClass("px-2", "w-shell-tab-connected");

		fireEvent.click(reviewerTab);
		expect(onSelectReviewerTerminal).toHaveBeenCalledWith({ handleId: "review-sess-1", harness: "opencode" });
	});

	it("leaves terminal creation out of the terminal strip", () => {
		renderCenterPane({ session: worker });

		expect(screen.queryByRole("button", { name: "New terminal" })).toBeNull();
	});

	it("uses the localized orchestrator label with provider and activity context", () => {
		renderCenterPane({
			session: { ...worker, id: "sess-orch", kind: "orchestrator" },
		});
		const orchestratorTab = screen.getByRole("tab", { name: "Orchestrator · OpenCode · Working" });
		expect(orchestratorTab).toHaveTextContent("Orchestrator");
		expect(orchestratorTab).not.toHaveTextContent(worker.title);
		expect(orchestratorTab.querySelector('img[aria-hidden="true"]')).toBeInTheDocument();
	});

	it("shows 'No session' when there is no session", () => {
		renderCenterPane();
		expect(screen.getByText("No session")).toBeInTheDocument();
	});

	it("uses the inspector tab height for the terminal header", () => {
		renderCenterPane({ session: worker });

		const tablist = screen.getByRole("tablist", { name: "Open terminals" });
		const header = tablist.closest(".h-inspector-tabs");
		expect(header?.classList.contains("h-inspector-tabs")).toBe(true);
		expect(tablist.classList.contains("h-full")).toBe(true);
	});

	it("keeps session tab actions on the primary agent tab and groups terminal creation with workspace actions", () => {
		renderCenterPane({
			session: worker,
			sessionTabAction: <button type="button">Session tab action</button>,
			tabStripAction: <button type="button">New terminal</button>,
			topbarActions: <button type="button">Workspace action</button>,
		});

		const terminalRegion = screen.getByTestId("session-terminal-region");
		const workspaceTopbar = screen.getByTestId("session-workspace-topbar");
		expect(workspaceTopbar).toHaveClass("session-topbar-surface");
		expect(workspaceTopbar).toContainElement(terminalRegion);
		expect(terminalRegion).toContainElement(screen.getByRole("tablist", { name: "Open terminals" }));
		expect(terminalRegion).not.toContainElement(screen.getByRole("button", { name: "New terminal" }));
		expect(screen.queryByRole("toolbar", { name: "Terminal display controls" })).not.toBeInTheDocument();
		expect(terminalRegion).toContainElement(screen.getByRole("button", { name: "Session tab action" }));
		expect(screen.getByTestId("session-tab-action").parentElement).toHaveClass("absolute", "right-1", "inset-y-0");
		expect(screen.getByRole("tab", { name: /^do the thing/ })).toHaveClass("h-full", "flex-1");
		expect(terminalRegion).not.toContainElement(screen.getByTestId("session-action-region"));
		const actionRegion = screen.getByTestId("session-action-region");
		expect(actionRegion).not.toHaveClass("border-l");
		expect(actionRegion).toHaveClass("gap-1");
		expect(actionRegion).toContainElement(screen.getByRole("button", { name: "New terminal" }));
		expect(actionRegion).toContainElement(screen.getByRole("button", { name: "Workspace action" }));
		expect(actionRegion).not.toContainElement(screen.getByRole("button", { name: "Session tab action" }));
		expect(screen.getByTestId("session-tab-strip-action")).toHaveTextContent("New terminal");
		expect(document.querySelector(".session-tab-scroll-fade")).not.toBeInTheDocument();
	});

	it("keeps the tab strip flush with the action controls", () => {
		renderCenterPane({ session: worker });

		expect(screen.getByTestId("session-terminal-region")).not.toHaveClass("pr-3");
		expect(screen.getByTestId("session-action-region")).toHaveClass("pr-3");
		expect(screen.getByTestId("session-action-region")).toHaveClass("pl-2");
		expect(screen.getByTestId("session-action-region")).not.toHaveClass("px-3");
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
		expect(screen.queryByRole("button", { name: "Exit fullscreen" })).toBeNull();
		Object.defineProperty(document, "fullscreenElement", { configurable: true, value: null });
	});

	it("does not reserve tab-strip space for unavailable overflow controls", () => {
		renderCenterPane({ session: worker });

		expect(screen.getByTestId("session-terminal-region")).not.toHaveClass("pl-0.5");
		expect(screen.getByRole("tablist", { name: "Open terminals" })).not.toHaveClass("pt-1.5");
		expect(screen.queryByRole("button", { name: "Scroll tabs left" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Scroll tabs right" })).not.toBeInTheDocument();
	});

	it("keeps every tab in a visually hidden native scroll strip", () => {
		const shells = makeShells(8);
		renderCenterPane({ session: worker, shellTerminals: shells });

		const scrollRegion = document.querySelector(".session-tab-scroll-region");
		expect(scrollRegion?.classList.contains("scrollbar-none")).toBe(true);
		expect(scrollRegion?.classList.contains("terminal-tabs-scrollbar")).toBe(false);
		expect(scrollRegion?.classList.contains("min-w-flex-min")).toBe(true);
		expect(scrollRegion?.classList.contains("h-full")).toBe(true);
		expect(scrollRegion?.contains(screen.getByRole("tab", { name: /^do the thing/ }).parentElement)).toBe(true);
		for (const tab of screen.getAllByTitle(/^\/tmp\/ws/)) {
			const frame = tab.closest("[data-terminal-tab-frame]");
			expect(frame?.classList.contains("max-w-shell-tab-max")).toBe(true);
			expect(frame?.classList.contains("shrink-0")).toBe(true);
			expect(frame?.classList.contains("w-shell-tab-connected")).toBe(false);
			expect(frame?.classList.contains("min-w-16")).toBe(false);
			expect(tab.classList.contains("min-w-0")).toBe(true);
		}
		// Overflow is handled directly by the scroll strip; arrow controls never reserve space.
		expect(screen.queryByRole("button", { name: "Scroll tabs left" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Scroll tabs right" })).toBeNull();

		// Both display actions live outside this topbar; the flexible scroll region
		// can therefore consume the remaining terminal-region width.
		const tabList = screen.getByRole("tablist", { name: "Open terminals" });
		expect(tabList).toHaveClass("flex-1");
		expect(screen.queryByRole("toolbar", { name: "Terminal display controls" })).not.toBeInTheDocument();
	});

	it("does not add arrow controls when the native tab strip overflows", () => {
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

		expect(screen.queryByRole("button", { name: "Scroll tabs left" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Scroll tabs right" })).toBeNull();
	});

	it("reorders reviewer and shell terminals together while keeping the owner terminal first", () => {
		const shells = makeShells(2);
		renderCenterPane({
			reviewerTerminal: { handleId: "review-sess-1", harness: "opencode" },
			session: worker,
			shellTerminals: shells,
		});

		const tabLabels = () =>
			Array.from(screen.getByRole("tablist", { name: "Open terminals" }).querySelectorAll('[role="tab"]')).map(
				(tab) => tab.textContent,
			);
		expect(tabLabels()).toEqual(["do the thing", "Reviewer", "agent-orchestrator-0", "agent-orchestrator-1"]);
		expect(reorderMocks.onReorder).toBeTypeOf("function");

		act(() => reorderMocks.onReorder?.(["h-0", "reviewer:review-sess-1", "h-1"]));

		expect(tabLabels()).toEqual(["do the thing", "agent-orchestrator-0", "Reviewer", "agent-orchestrator-1"]);
	});

	it("appends new terminals after open files and reorders terminals with files", () => {
		Object.defineProperty(HTMLElement.prototype, "scrollTo", {
			configurable: true,
			value: vi.fn(),
		});
		const shells = makeShells(2);
		const fileTab = {
			key: "file:.gitignore",
			content: <button role="tab">.gitignore</button>,
			onSelect: vi.fn(),
		};
		const view = renderCenterPane({ session: worker, shellTerminals: [shells[0]], workspaceTabs: [fileTab] });
		const tabLabels = () =>
			Array.from(screen.getByRole("tablist", { name: "Open terminals" }).querySelectorAll('[role="tab"]')).map(
				(tab) => tab.textContent,
			);

		expect(tabLabels()).toEqual(["do the thing", "agent-orchestrator-0", ".gitignore"]);
		view.rerender(
			<TooltipProvider>
				<CenterPane daemonReady session={worker} shellTerminals={shells} theme="dark" workspaceTabs={[fileTab]} />
			</TooltipProvider>,
		);
		expect(tabLabels()).toEqual(["do the thing", "agent-orchestrator-0", ".gitignore", "agent-orchestrator-1"]);

		act(() => reorderMocks.onReorder?.(["file:.gitignore", "h-1", "h-0"]));
		expect(tabLabels()).toEqual(["do the thing", ".gitignore", "agent-orchestrator-1", "agent-orchestrator-0"]);
	});

	it("restores a session's remembered tab order after navigating away", () => {
		const shells = makeShells(2);
		const view = renderCenterPane({ session: worker, shellTerminals: shells });
		const tabLabels = () =>
			Array.from(screen.getByRole("tablist", { name: "Open terminals" }).querySelectorAll('[role="tab"]')).map(
				(tab) => tab.textContent,
			);

		act(() => reorderMocks.onReorder?.(["h-1", "h-0"]));
		expect(tabLabels()).toEqual(["do the thing", "agent-orchestrator-1", "agent-orchestrator-0"]);

		view.rerender(
			<TooltipProvider>
				<CenterPane daemonReady session={{ ...worker, id: "sess-2", title: "second session" }} shellTerminals={shells} theme="dark" />
			</TooltipProvider>,
		);
		view.rerender(
			<TooltipProvider>
				<CenterPane daemonReady session={worker} shellTerminals={shells} theme="dark" />
			</TooltipProvider>,
		);

		expect(tabLabels()).toEqual(["do the thing", "agent-orchestrator-1", "agent-orchestrator-0"]);
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

		// Horizontal trackpad input is already native to overflow-x-auto and must
		// not be re-applied by the vertical-wheel adapter.
		scrollBy.mockClear();
		fireEvent.wheel(scrollRegion, { deltaX: 60 });
		expect(scrollBy).not.toHaveBeenCalled();

		// Ctrl+wheel is terminal font zoom, not tab scrolling.
		scrollBy.mockClear();
		fireEvent.wheel(scrollRegion, { deltaY: 80, ctrlKey: true });
		expect(scrollBy).not.toHaveBeenCalled();
	});

	it("reveals the active terminal by scrolling only the horizontal tab strip", () => {
		const [shell] = makeShells(1);
		const view = renderCenterPane({ session: worker, shellTerminals: [shell] });
		const scrollRegion = document.querySelector(".overflow-x-auto") as HTMLElement;
		const shellCard = scrollRegion.querySelector<HTMLElement>('[data-terminal-tab-key="h-0"]');
		const scrollTo = vi.fn();
		Object.defineProperties(scrollRegion, {
			scrollLeft: { configurable: true, value: 20 },
			scrollTo: { configurable: true, value: scrollTo },
		});
		scrollRegion.getBoundingClientRect = () => ({ left: 100, right: 300 }) as DOMRect;
		if (!shellCard) throw new Error("Expected shell terminal card");
		shellCard.getBoundingClientRect = () => ({ left: 350, right: 450 }) as DOMRect;

		view.rerender(
			<TooltipProvider>
				<CenterPane
					daemonReady
					session={worker}
					shellTerminals={[shell]}
					terminalTarget={{
						generation: shell.createdAt,
						handleId: shell.handleId,
						kind: "shell",
						title: shell.title,
					}}
					theme="dark"
				/>
			</TooltipProvider>,
		);

		expect(scrollTo).toHaveBeenCalledWith({ behavior: "smooth", left: 170 });
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
		expect(sessionTab.getAttribute("tabindex")).toBe("0");
		expect(firstShellTab.getAttribute("tabindex")).toBe("-1");

		sessionTab.focus();
		fireEvent.keyDown(sessionTab, { key: "ArrowRight" });
		expect(document.activeElement).toBe(firstShellTab);
		expect(onSelectShellTerminal).toHaveBeenCalledWith("h-0");

		fireEvent.keyDown(firstShellTab, { key: "Home" });
		expect(document.activeElement).toBe(sessionTab);
		expect(onSelectSessionTerminal).toHaveBeenCalledOnce();

		// Revisiting a tab quickly by keyboard must not count as a double-click
		// and enter rename mode.
		fireEvent.keyDown(sessionTab, { key: "ArrowRight" });
		expect(document.activeElement).toBe(firstShellTab);
		expect(screen.queryByRole("textbox", { name: /rename terminal/i })).toBeNull();
	});
});
