import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps, ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentSwitch } from "../hooks/useAgentSwitches";
import type { SwitchAgentInput } from "../hooks/useSwitchAgent";
import type { WorkspaceSession } from "../types/workspace";
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

const reorderMocks = vi.hoisted(() => ({
	onReorder: undefined as ((values: string[]) => void) | undefined,
}));

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

vi.mock("../hooks/useAgentSwitches", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../hooks/useAgentSwitches")>();
	return {
		...actual,
		useAgentSwitches: () => ({ data: agentSwitchMocks.switches, refetch: agentSwitchMocks.refetch }),
	};
});

vi.mock("../hooks/useSwitchAgent", () => ({
	useSwitchAgentState: () => agentSwitchMocks.mutation,
}));

vi.mock("./TerminalSwitchAgentButton", () => ({
	TerminalSwitchAgentButton: ({
		session,
		onOpenChange,
	}: {
		session: WorkspaceSession;
		onOpenChange?: (open: boolean) => void;
	}) => (
		<button
			aria-label="Switch agent"
			data-testid="terminal-switch-agent"
			onClick={() => onOpenChange?.(true)}
			type="button"
		>
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
	provider: "claude-code",
	kind: "worker",
	branch: "ao/sess-1",
	status: "working",
	updatedAt: "2026-06-10T00:00:00Z",
	activity: { state: "active", lastActivityAt: "2026-06-10T00:00:00Z" },
	prs: [],
} satisfies WorkspaceSession;

function switchRecord(overrides: Partial<AgentSwitch> = {}): AgentSwitch {
	return {
		agentHandoffStatus: "not_attempted",
		fromHarness: "claude-code",
		id: "switch-1",
		state: "preparing_handoff",
		targetHarness: "codex",
		...overrides,
	};
}

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
	reorderMocks.onReorder = undefined;
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

	it("blocks only the terminal interaction surface while the switch selector is open", async () => {
		renderCenterPane({ session: worker });

		await userEvent.click(screen.getByRole("button", { name: "Switch agent" }));

		expect(screen.getByTestId("terminal-interaction-surface")).toHaveAttribute("inert");
		expect(screen.getByText("terminal body")).toHaveAttribute("data-input-disabled", "true");
		expect(document.body.style.pointerEvents).not.toBe("none");
	});

	it("uses mutation input only while switch admission is still pending", () => {
		agentSwitchMocks.mutation.input = {
			idempotencyKey: "switch-request-1",
			model: "",
			session: worker,
			targetHarness: "codex",
		};
		agentSwitchMocks.mutation.isPending = true;

		renderCenterPane({ session: worker });

		const overlay = screen.getByRole("status", { name: "Switching from Claude Code to Codex" });
		const terminalPanel = screen.getByRole("tabpanel", { name: "do the thing terminal" });
		expect(terminalPanel).toContainElement(overlay);
		expect(overlay).toHaveClass("agent-switch-terminal-scrim");
		expect(within(overlay).getByTestId("agent-switch-transition-card")).toHaveClass(
			"animate-modal-in",
		);
		expect(screen.getByTestId("terminal-interaction-surface")).toHaveAttribute("inert");
		expect(within(overlay).getByText("Claude Code")).toBeInTheDocument();
		expect(within(overlay).getByText("Codex")).toBeInTheDocument();
		expect(screen.getByText("terminal body")).toHaveAttribute("data-input-disabled", "true");
	});

	it("renders a connected transfer arrow and a coupled, wrapping lifecycle", () => {
		agentSwitchMocks.mutation.input = {
			idempotencyKey: "switch-request-visuals",
			model: "",
			session: worker,
			targetHarness: "codex",
		};
		agentSwitchMocks.mutation.isPending = true;

		renderCenterPane({ session: worker });

		const card = screen.getByTestId("agent-switch-transition-card");
		const arrow = within(card).getByTestId("agent-switch-transfer-arrow");
		const shaft = within(arrow).getByTestId("agent-switch-transfer-shaft");
		const arrowIcon = within(arrow).getByTestId("agent-switch-transfer-arrow-icon");
		expect(arrowIcon).toHaveClass("lucide-arrow-right", "text-foreground/55");
		expect(arrowIcon.querySelector(".agent-switch-transfer-pulse")).toBeNull();
		expect(shaft.querySelector(".agent-switch-transfer-pulse")).not.toBeNull();

		const statusGroup = within(card).getByTestId("agent-switch-status-group");
		const progress = within(statusGroup).getByRole("list", { name: "Switching…" });
		expect(within(statusGroup).getAllByText("Preparing handoff")).toHaveLength(2);
		expect(statusGroup).toContainElement(progress);
		for (const label of [
			"Preparing handoff",
			"Stopping source agent",
			"Starting target agent",
			"Delivering context",
		]) {
			expect(within(progress).getByText(label)).toHaveClass(
				"max-w-16",
				"break-words",
				"whitespace-normal",
				"text-center",
			);
			expect(within(progress).getByText(label)).not.toHaveClass("truncate");
		}
	});

	it("shows one terminal scrim while the selector is open during admission", async () => {
		agentSwitchMocks.mutation.input = {
			idempotencyKey: "switch-request-1",
			model: "",
			session: worker,
			targetHarness: "codex",
		};
		agentSwitchMocks.mutation.isPending = true;

		renderCenterPane({ session: worker });
		expect(screen.getByTestId("agent-switch-terminal-overlay")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Switch agent" }));

		expect(screen.queryByTestId("agent-switch-terminal-overlay")).not.toBeInTheDocument();
	});

	it("keeps a new admission presented above unrelated settled completion history", () => {
		const settledSession = {
			...worker,
			provider: "codex",
			terminalHandleId: "settled-target-terminal",
		} satisfies WorkspaceSession;
		agentSwitchMocks.switches.push(switchRecord({ state: "completed" }));
		agentSwitchMocks.mutation.input = {
			idempotencyKey: "switch-request-2",
			model: "",
			session: settledSession,
			targetHarness: "claude-code",
		};
		agentSwitchMocks.mutation.isPending = true;

		renderCenterPane({ session: settledSession });

		expect(
			screen.getByRole("status", { name: "Switching from Codex to Claude Code" }),
		).toHaveAttribute("aria-busy", "true");
		expect(screen.getByTestId("terminal-interaction-surface")).toHaveAttribute("inert");
		expect(screen.getByText("terminal body")).toHaveAttribute("data-input-disabled", "true");
	});

	it.each([
		["preparing_handoff", "Preparing handoff"],
		["stopping_source", "Stopping source agent"],
		["starting_target", "Starting target agent"],
		["target_ready", "Target ready"],
		["delivering_context", "Delivering context"],
	] as const)("renders the shared title and description for %s", (state, description) => {
		const activeSwitch = switchRecord({ state });
		agentSwitchMocks.switches.push(activeSwitch);

		renderCenterPane({ session: { ...worker, activeAgentSwitch: activeSwitch } });

		const status = screen.getByRole("status", { name: "Switching from Claude Code to Codex" });
		expect(within(status).getByText("Switching from Claude Code to Codex")).toBeInTheDocument();
		expect(within(status).getAllByText(description).length).toBeGreaterThan(0);
		expect(screen.getByRole("tab", { name: "do the thing · Working" })).toBeInTheDocument();
	});

	it("keeps the preparation stage active while the source handoff is requested", () => {
		const activeSwitch = switchRecord({ agentHandoffStatus: "requested" });
		agentSwitchMocks.switches.push(activeSwitch);

		renderCenterPane({ session: { ...worker, activeAgentSwitch: activeSwitch } });

		const status = screen.getByRole("status", { name: "Switching from Claude Code to Codex" });
		expect(within(status).getAllByText("Preparing handoff").length).toBeGreaterThan(0);
		expect(within(status).getAllByText("Preparing handoff").at(-1)?.closest("li")).toHaveAttribute(
			"aria-current",
			"step",
		);
	});

	it("uses the matching history row to enrich the active session summary", () => {
		const summary = switchRecord({ state: "starting_target" });
		agentSwitchMocks.switches.push({ ...summary, state: "target_ready" });

		renderCenterPane({ session: { ...worker, activeAgentSwitch: summary } });

		expect(screen.getByRole("status")).toHaveTextContent("Target ready");
	});

	it("gates both DOM and byte input only for the worker during ordinary progress", () => {
		const activeSwitch = switchRecord({ state: "starting_target" });
		agentSwitchMocks.switches.push(activeSwitch);

		renderCenterPane({ session: { ...worker, activeAgentSwitch: activeSwitch } });

		expect(screen.getByTestId("terminal-interaction-surface")).toHaveAttribute("inert");
		expect(screen.getByText("terminal body")).toHaveAttribute("data-input-disabled", "true");
		expect(screen.getByTestId("agent-switch-terminal-overlay")).toHaveFocus();
	});

	it("keeps an auxiliary shell interactive and offers the existing worker-selection action", async () => {
		const [shell] = makeShells(1);
		const activeSwitch = switchRecord({ state: "starting_target" });
		const onSelectSessionTerminal = vi.fn();
		agentSwitchMocks.switches.push(activeSwitch);

		renderCenterPane({
			onSelectSessionTerminal,
			session: { ...worker, activeAgentSwitch: activeSwitch },
			shellTerminals: [shell],
			terminalTarget: { generation: shell.createdAt, kind: "shell", handleId: shell.handleId, title: shell.title },
		});

		expect(screen.getByTestId("terminal-interaction-surface")).not.toHaveAttribute("inert");
		expect(screen.getByText("terminal body")).toHaveAttribute("data-input-disabled", "false");
		expect(screen.queryByTestId("agent-switch-terminal-overlay")).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Back to agent terminal" }));
		expect(onSelectSessionTerminal).toHaveBeenCalledOnce();
	});

	it("opens and focuses only source worker input once, then relocks on stage change", () => {
		const [shell] = makeShells(1);
		const requestedSwitch = switchRecord({ agentHandoffStatus: "requested" });
		const onSelectSessionTerminal = vi.fn();
		agentSwitchMocks.switches.push(requestedSwitch);
		const sourceSession = {
			...worker,
			activeAgentSwitch: requestedSwitch,
			activity: { state: "waiting_input", lastActivityAt: "2026-06-10T00:00:02Z" },
		} satisfies WorkspaceSession;
		const view = renderCenterPane({
			onSelectSessionTerminal,
			session: sourceSession,
			shellTerminals: [shell],
			terminalTarget: { generation: shell.createdAt, kind: "shell", handleId: shell.handleId, title: shell.title },
		});

		expect(onSelectSessionTerminal).toHaveBeenCalledOnce();
		view.rerender(
			<TooltipProvider>
				<CenterPane
					daemonReady
					onSelectSessionTerminal={onSelectSessionTerminal}
					session={sourceSession}
					shellTerminals={[shell]}
					terminalTarget={{ kind: "worker" }}
					theme="dark"
				/>
			</TooltipProvider>,
		);
		expect(onSelectSessionTerminal).toHaveBeenCalledOnce();
		expect(screen.getByTestId("terminal-interaction-surface")).not.toHaveAttribute("inert");
		expect(screen.getByText("terminal body")).toHaveAttribute("data-input-disabled", "false");
		expect(screen.getByText("terminal body")).toHaveAttribute("data-focus-requested", "true");
		expect(screen.getByTestId("agent-switch-terminal-overlay")).toHaveClass("agent-switch-source-input-strip");

		const startingSwitch: AgentSwitch = {
			...requestedSwitch,
			agentHandoffStatus: "received",
			state: "starting_target",
		};
		agentSwitchMocks.switches.splice(0, 1, startingSwitch);
		view.rerender(
			<TooltipProvider>
				<CenterPane
					daemonReady
					onSelectSessionTerminal={onSelectSessionTerminal}
					session={{ ...worker, activeAgentSwitch: startingSwitch }}
					terminalTarget={{ kind: "worker" }}
					theme="dark"
				/>
			</TooltipProvider>,
		);
		expect(screen.getByTestId("terminal-interaction-surface")).toHaveAttribute("inert");
		expect(screen.getByText("terminal body")).toHaveAttribute("data-input-disabled", "true");
		expect(screen.getByText("terminal body")).toHaveAttribute("data-focus-requested", "false");
	});

	it.each([
		["recovery", switchRecord({ errorCode: "target_start_unconfirmed", state: "starting_target" })],
		["failure", switchRecord({ errorCode: "target_binary_missing", state: "failed" })],
	] as const)("renders %s as a static alert without busy animation", (_name, terminalSwitch) => {
		const activeSwitch = switchRecord({ id: terminalSwitch.id, state: "starting_target" });
		agentSwitchMocks.switches.push(activeSwitch);
		const view = renderCenterPane({ session: { ...worker, activeAgentSwitch: activeSwitch } });

		agentSwitchMocks.switches.splice(0, 1, terminalSwitch);
		view.rerender(
			<TooltipProvider>
				<CenterPane daemonReady session={worker} theme="dark" />
			</TooltipProvider>,
		);

		const status = screen.getByRole("status");
		expect(status).not.toHaveAttribute("aria-busy");
		expect(status.querySelector(".agent-switch-transfer-pulse")).not.toBeInTheDocument();
		expect(screen.getByRole("alert")).toBeInTheDocument();
	});

	it("lets the user dismiss a terminal switch failure without dismissing recovery", async () => {
		const activeSwitch = switchRecord({ state: "starting_target" });
		const failedSwitch = switchRecord({ errorCode: "target_binary_missing", state: "failed" });
		agentSwitchMocks.switches.push(activeSwitch);
		const view = renderCenterPane({ session: { ...worker, activeAgentSwitch: activeSwitch } });

		agentSwitchMocks.switches.splice(0, 1, failedSwitch);
		view.rerender(
			<TooltipProvider>
				<CenterPane daemonReady session={worker} theme="dark" />
			</TooltipProvider>,
		);

		await userEvent.click(screen.getByRole("button", { name: "Close" }));
		expect(screen.queryByTestId("agent-switch-terminal-overlay")).not.toBeInTheDocument();
	});

	it("keeps completion covered until target settlement without stealing terminal selection", () => {
		vi.useFakeTimers();
		const activeSwitch = switchRecord({ state: "delivering_context" });
		const completedSwitch: AgentSwitch = { ...activeSwitch, state: "completed" };
		const onSelectSessionTerminal = vi.fn();
		agentSwitchMocks.switches.push(activeSwitch);
		const view = renderCenterPane({ session: { ...worker, activeAgentSwitch: activeSwitch } });

		agentSwitchMocks.switches.splice(0, 1, completedSwitch);
		view.rerender(
			<TooltipProvider>
				<CenterPane daemonReady onSelectSessionTerminal={onSelectSessionTerminal} session={worker} theme="dark" />
			</TooltipProvider>,
		);
		expect(screen.getByTestId("terminal-interaction-surface")).toHaveAttribute("inert");
		expect(screen.getByRole("status")).toHaveTextContent("Completed");

		const settledSession = { ...worker, provider: "codex", terminalHandleId: "target-terminal" } satisfies WorkspaceSession;
		view.rerender(
			<TooltipProvider>
				<CenterPane daemonReady onSelectSessionTerminal={onSelectSessionTerminal} session={settledSession} theme="dark" />
			</TooltipProvider>,
		);
		expect(onSelectSessionTerminal).not.toHaveBeenCalled();
		expect(screen.getByText("terminal body")).toHaveAttribute("data-focus-requested", "true");
		expect(screen.getByRole("status")).toHaveTextContent("Completed");

		view.rerender(
			<TooltipProvider>
				<CenterPane daemonReady onSelectSessionTerminal={onSelectSessionTerminal} session={settledSession} theme="dark" />
			</TooltipProvider>,
		);
		expect(onSelectSessionTerminal).not.toHaveBeenCalled();
		act(() => vi.advanceTimersByTime(3_100));
		expect(screen.queryByTestId("agent-switch-terminal-overlay")).not.toBeInTheDocument();
		vi.useRealTimers();
	});

	it("never flashes success for a cold historical completion", () => {
		agentSwitchMocks.switches.push(switchRecord({ state: "completed" }));

		renderCenterPane({
			session: { ...worker, provider: "codex", terminalHandleId: "target-terminal" },
		});

		expect(screen.queryByTestId("agent-switch-terminal-overlay")).not.toBeInTheDocument();
	});

	it("does not replay a cold historical failure", () => {
		agentSwitchMocks.switches.push(switchRecord({ state: "failed" }));

		renderCenterPane({ session: worker });

		expect(screen.queryByTestId("agent-switch-terminal-overlay")).not.toBeInTheDocument();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	it("observes a cold unsettled completion without changing the selected terminal", () => {
		const onSelectSessionTerminal = vi.fn();
		agentSwitchMocks.switches.push(switchRecord({ state: "completed" }));

		const view = renderCenterPane({ onSelectSessionTerminal, session: worker });

		expect(screen.getByTestId("terminal-interaction-surface")).toHaveAttribute("inert");
		expect(screen.getByRole("status")).toHaveTextContent("Completed");

		view.rerender(
			<TooltipProvider>
				<CenterPane
					daemonReady
					onSelectSessionTerminal={onSelectSessionTerminal}
					session={{ ...worker, provider: "codex", terminalHandleId: "target-terminal" }}
					theme="dark"
				/>
			</TooltipProvider>,
		);

		expect(onSelectSessionTerminal).not.toHaveBeenCalled();
		expect(screen.getByText("terminal body")).toHaveAttribute("data-focus-requested", "true");
		expect(screen.getByRole("status")).toHaveTextContent("Completed");
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

	it("places the active session indicator along the bottom edge", () => {
		renderCenterPane({ session: worker });

		const classes = screen.getByRole("tab", { name: /^do the thing/ }).parentElement?.classList;
		expect(classes?.contains("after:bottom-0")).toBe(true);
		expect(classes?.contains("after:top-0")).toBe(false);
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
		expect(within(mainContainer as HTMLElement).queryByTestId("terminal-switch-agent")).toBeNull();
	});

	it("keeps the compact owner card fixed before the scrollable terminal list", () => {
		const [shell] = makeShells(1);
		renderCenterPane({ session: worker, shellTerminals: [shell] });

		const ownerTab = screen.getByRole("tab", { name: /^do the thing/ });
		const ownerCard = ownerTab.parentElement;
		const scrollRegion = document.querySelector(".overflow-x-auto");
		const avatar = ownerCard?.querySelector('img[aria-hidden="true"]');

		expect(ownerCard?.classList.contains("shrink-0")).toBe(true);
		expect(scrollRegion?.contains(ownerCard)).toBe(false);
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
			reviewerTerminal: { handleId: "review-sess-1", harness: "codex" },
			shellTerminals: [shell],
			terminalTarget: { kind: "reviewer", handleId: "review-sess-1", harness: "codex", sessionId: worker.id },
		});

		const reviewerTab = screen.getByRole("tab", { name: "Reviewer" });
		const shellTab = screen.getByRole("tab", { name: shell.title });
		expect(reviewerTab).toHaveAttribute("aria-current", "true");
		expect(reviewerTab.querySelector("img")).toHaveClass("size-terminal-agent-icon");
		expect(reviewerTab.parentElement).toHaveClass(
			"min-w-shell-tab-min",
			"shrink-0",
			"self-stretch",
			"w-shell-tab-connected",
			"border-x",
			"border-border-strong",
			"bg-overlay",
		);
		expect(shellTab.parentElement).toHaveClass(
			"min-w-shell-tab-min",
			"shrink-0",
			"self-stretch",
			"w-shell-tab-connected",
		);
		expect(reviewerTab.parentElement).not.toHaveAttribute("data-terminal-role", "primary");
		expect(screen.getByRole("tab", { name: /^do the thing/ })).not.toHaveAttribute("aria-current", "true");
		expect(reviewerTab.querySelector("img")).toHaveAttribute("src");
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

	it("leaves terminal creation out of the terminal strip", () => {
		renderCenterPane({ session: worker });

		expect(screen.queryByRole("button", { name: "New terminal" })).toBeNull();
	});

	it("shows 'Orchestrator' for an orchestrator session", () => {
		renderCenterPane({
			session: { ...worker, id: "sess-orch", kind: "orchestrator" },
		});
		expect(screen.getByText("Orchestrator")).toBeInTheDocument();
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

	it("keeps terminal tabs in the measured terminal region and session actions outside it", () => {
		renderCenterPane({
			session: worker,
			topbarActions: <button type="button">Session action</button>,
		});

		const terminalRegion = screen.getByTestId("session-terminal-region");
		const workspaceTopbar = screen.getByTestId("session-workspace-topbar");
		expect(workspaceTopbar).toHaveClass("session-topbar-surface");
		expect(workspaceTopbar).toContainElement(terminalRegion);
		expect(terminalRegion).toContainElement(screen.getByRole("tablist", { name: "Open terminals" }));
		expect(terminalRegion).not.toContainElement(screen.queryByRole("button", { name: "New terminal" }));
		expect(screen.queryByRole("toolbar", { name: "Terminal display controls" })).not.toBeInTheDocument();
		expect(terminalRegion).not.toContainElement(screen.getByTestId("session-action-region"));
		const actionRegion = screen.getByTestId("session-action-region");
		expect(actionRegion).not.toHaveClass("border-l");
		expect(actionRegion).toContainElement(
			screen.getByRole("button", { name: "Session action" }),
		);
	});

	it("reserves trailing space after the terminal strip controls", () => {
		renderCenterPane({ session: worker });

		expect(screen.getByTestId("session-terminal-region").classList.contains("pr-3")).toBe(true);
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

	it("keeps fixed-width auxiliary tabs in a visually hidden native scroll strip while the owner stays fixed", () => {
		const shells = makeShells(8);
		renderCenterPane({ session: worker, shellTerminals: shells });

		const scrollRegion = document.querySelector(".overflow-x-auto");
		expect(scrollRegion?.classList.contains("scrollbar-none")).toBe(true);
		expect(scrollRegion?.classList.contains("terminal-tabs-scrollbar")).toBe(false);
		expect(scrollRegion?.classList.contains("min-w-flex-min")).toBe(true);
		expect(scrollRegion?.classList.contains("flex-1")).toBe(true);
		expect(scrollRegion?.contains(screen.getByRole("tab", { name: /^do the thing/ }).parentElement)).toBe(false);
		for (const tab of screen.getAllByTitle(/^\/tmp\/ws/)) {
			expect(tab.parentElement?.classList.contains("min-w-shell-tab-min")).toBe(true);
			expect(tab.parentElement?.classList.contains("shrink-0")).toBe(true);
			expect(tab.parentElement?.classList.contains("w-shell-tab-connected")).toBe(true);
			expect(tab.parentElement?.classList.contains("min-w-16")).toBe(false);
			expect(tab.classList.contains("min-w-0")).toBe(true);
			expect(tab.classList.contains("w-full")).toBe(true);
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
			reviewerTerminal: { handleId: "review-sess-1", harness: "codex" },
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

	it("drops a session's remembered terminal order after navigating away", () => {
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

		expect(tabLabels()).toEqual(["do the thing", "agent-orchestrator-0", "agent-orchestrator-1"]);
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
