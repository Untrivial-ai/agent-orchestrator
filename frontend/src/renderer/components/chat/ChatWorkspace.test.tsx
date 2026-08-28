import { act, fireEvent, render as rtlRender, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { typeInLexicalEditor } from "../../test/lexical";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChatWorkspace, promptSpacerHeight, promptTopInset } from "./ChatWorkspace";
import { AssistantMessage, HumanMessage, OriginMessage } from "./ChatTimelineItems";
import {
	chatFixture,
	chatFixtureEmpty,
	chatFixtureLongHistory,
	chatFixtureMcpFailed,
	chatFixtureSettled,
	chatFixtureThreadError,
} from "../../lib/chat-fixture";
import { appI18n } from "../../i18n";
import type { ConversationMessage, ConversationSnapshot } from "../../types/conversation";
import { setApiBaseUrl } from "../../lib/api-client";
import { useUiStore } from "../../stores/ui-store";
import type { WorkspaceSession } from "../../types/workspace";
import { TooltipProvider } from "../ui/tooltip";

const renameSessionMock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));

vi.mock("../../lib/rename-session", () => ({ renameSession: renameSessionMock }));

function render(ui: ReactElement) {
	const result = rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
	return {
		...result,
		rerender: (nextUi: ReactElement) => result.rerender(<TooltipProvider>{nextUi}</TooltipProvider>),
	};
}

const writeText = vi.fn(async (_text: string) => undefined);
const menuAction = vi.fn(async (_action: string) => undefined);
const previousTabListeners = new Set<() => void>();
const nextTabListeners = new Set<() => void>();
const closeShellTerminalListeners = new Set<() => void>();
const closeShellTerminalShortcutStates: boolean[] = [];
type TerminalPaneTestProps = {
	fontSize?: number;
	focusRequested?: boolean;
	isFullscreen?: boolean;
	onChangeFontSize?: (delta: number) => void;
	onToggleFullscreen?: () => Promise<void> | void;
};
const terminalPaneState = vi.hoisted(() => ({
	props: undefined as TerminalPaneTestProps | undefined,
}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: {
		app: {
			onPreviousTabShortcut: (listener: () => void) => {
				previousTabListeners.add(listener);
				return () => previousTabListeners.delete(listener);
			},
			onNextTabShortcut: (listener: () => void) => {
				nextTabListeners.add(listener);
				return () => nextTabListeners.delete(listener);
			},
			onCloseShellTerminalShortcut: (listener: () => void) => {
				closeShellTerminalListeners.add(listener);
				return () => closeShellTerminalListeners.delete(listener);
			},
			setCloseShellTerminalShortcutEnabled: (enabled: boolean) => {
				closeShellTerminalShortcutStates.push(enabled);
			},
		},
		clipboard: { writeText: (text: string) => writeText(text) },
		menu: { action: (action: string) => menuAction(action) },
	},
}));

vi.mock("../TerminalPane", () => ({
	TerminalPane: (props: NonNullable<typeof terminalPaneState.props>) => {
		terminalPaneState.props = props;
		return <div data-testid="terminal-pane" />;
	},
}));

vi.mock("../../lib/platform", () => ({
	isMacPlatform: () => true,
	isLinuxPlatform: () => false,
	isWindowsPlatform: () => false,
}));

/** A refetch: identical content, all-new objects, which is what JSON parsing gives. */
function poll(snapshot: ConversationSnapshot): ConversationSnapshot {
	return structuredClone(snapshot);
}

function idleSnapshot(snapshot: ConversationSnapshot = chatFixture): ConversationSnapshot {
	return {
		...snapshot,
		controller: { state: "ready" },
		items: snapshot.items.filter(
			(item) =>
				!(
					item.kind === "activity" &&
					item.activityKind === "approval" &&
					item.status === "pending"
				),
		),
		turns: snapshot.turns.map((turn) =>
			turn.state === "running"
				? {
						...turn,
						state: "completed" as const,
						completedAt: turn.requestedAt,
					}
				: turn,
		),
	};
}

/** jsdom has no layout, so the scroller's geometry has to be stated. */
function stubGeometry(
	node: HTMLElement,
	{
		scrollHeight,
		clientHeight,
		scrollTop,
	}: {
		scrollHeight: number;
		clientHeight: number;
		scrollTop: number;
	},
) {
	Object.defineProperty(node, "scrollHeight", {
		configurable: true,
		value: scrollHeight,
	});
	Object.defineProperty(node, "clientHeight", {
		configurable: true,
		value: clientHeight,
	});
	Object.defineProperty(node, "scrollTop", {
		configurable: true,
		writable: true,
		value: scrollTop,
	});
}

beforeEach(() => {
	writeText.mockClear();
	menuAction.mockClear();
	previousTabListeners.clear();
	nextTabListeners.clear();
	closeShellTerminalListeners.clear();
	closeShellTerminalShortcutStates.length = 0;
	terminalPaneState.props = undefined;
	renameSessionMock.mockReset().mockResolvedValue(undefined);
	window.localStorage.clear();
	setApiBaseUrl("http://127.0.0.1:3001");
	useUiStore.setState({ isSidebarOpen: true, inspectorSessions: {} });
});

afterEach(async () => {
	setApiBaseUrl(null);
	await appI18n.changeLanguage("en");
});

function humanMessage(text: string): ConversationMessage {
	return {
		kind: "message",
		id: "message-with-attachment",
		sequence: 1,
		revision: 1,
		role: "user",
		origin: "human",
		text,
		streaming: false,
		createdAt: "2026-08-08T00:00:00Z",
	};
}

const chatSession = {
	id: chatFixture.sessionId,
	workspaceId: "project-1",
	workspaceName: "agent-orchestrator",
	title: "Reviewer chat",
	provider: "codex",
	kind: "worker",
	mode: "chat",
	status: "working",
	updatedAt: "2026-08-15T00:00:00Z",
	activity: { state: "active", lastActivityAt: "2026-08-15T00:00:00Z" },
	prs: [],
} satisfies WorkspaceSession;

describe("HumanMessage attachments", () => {
	function renderImageAttachment(header: string, name: string) {
		render(
			<HumanMessage
				message={humanMessage(`check again\n\n${header}\n- .ao/attachments/${name}`)}
				sessionId="ao session/1"
			/>,
		);

		const image = screen.getByRole("img", { name });
		expect(image).toHaveAttribute(
			"src",
			`http://127.0.0.1:3001/api/v1/sessions/ao%20session%2F1/preview/files/.ao/attachments/${name}`,
		);
		expect(screen.getByText("check again")).toBeInTheDocument();
		expect(
			screen.queryByText(/Attached (?:files|images) \(read these files/),
		).not.toBeInTheDocument();
	}

	it("renders staged image references in human messages as images", () => {
		renderImageAttachment(
			"Attached files (read these files in the workspace):",
			"attachment-d9014f798f.png",
		);
	});

	it.each([
		[
			"spawn",
			"Attached files (read these files in the workspace for context):",
			"attachment-1.jpg",
		],
		[
			"legacy chat",
			"Attached images (read these files in the workspace for visual context):",
			"image-a1b2c3d4.webp",
		],
	])("renders an AO-generated %s image reference as an image", (_source, header, name) => {
		renderImageAttachment(header, name);
	});

	it("preserves authored trailing whitespace before generated references", () => {
		const authoredBody = "keep my spacing  \n";
		const { container } = render(
			<HumanMessage
				message={humanMessage(
					`${authoredBody}\n\nAttached files (read these files in the workspace):\n- .ao/attachments/attachment-ab12.png`,
				)}
				sessionId="ao-1"
			/>,
		);

		expect(container.querySelector(".cursor-chat-human-message > p")?.textContent).toBe(
			authoredBody,
		);
	});

	it("shows non-image attachments as file labels instead of internal prompt text", () => {
		render(
			<HumanMessage
				message={humanMessage(
					"inspect these\n\nAttached files (read these files in the workspace):\n- .ao/attachments/attachment-ab12.png\n- .ao/attachments/attachment-cd34.pdf",
				)}
				sessionId="ao-1"
			/>,
		);

		expect(screen.getByRole("img", { name: "attachment-ab12.png" })).toBeInTheDocument();
		expect(screen.getByText("attachment-cd34.pdf")).toBeInTheDocument();
		expect(screen.getByRole("list", { name: "Attached files" })).toBeInTheDocument();
		expect(screen.queryByText(/Attached files \(read these files/)).not.toBeInTheDocument();
	});

	it("leaves ordinary user-authored path lists untouched", () => {
		const text =
			"Document this example:\n\nAttached files (read these files in the workspace):\n- docs/screenshot.png";
		render(<HumanMessage message={humanMessage(text)} sessionId="ao-1" />);

		expect(screen.queryByRole("img")).not.toBeInTheDocument();
		expect(document.body.textContent).toContain(text);
	});
});

describe("Chat message timestamps", () => {
	it("exposes user and assistant sent times as 24-hour hover labels", () => {
		const today = new Date().toISOString();
		const user = { ...humanMessage("user message"), createdAt: today };
		const assistant = {
			...user,
			id: "assistant-message",
			role: "assistant",
			origin: "provider",
			text: "assistant message",
		} satisfies ConversationMessage;
		render(
			<>
				<HumanMessage message={user} sessionId="ao-1" />
				<AssistantMessage message={assistant} showCopy />
			</>,
		);

		expect(screen.getAllByLabelText(/^Sent \d{2}:\d{2}$/)).toHaveLength(2);
	});

	it("labels yesterday and older messages with calendar dates", () => {
		const now = new Date();
		const relativeDate = (daysAgo: number) => {
			const date = new Date(now);
			date.setDate(date.getDate() - daysAgo);
			return date.toISOString();
		};
		const messages = [
			{
				...humanMessage("yesterday"),
				id: "yesterday",
				createdAt: relativeDate(1),
			},
			{
				...humanMessage("older"),
				id: "older",
				createdAt: relativeDate(3),
			},
		];
		render(
			<>
				{messages.map((message) => (
					<HumanMessage key={message.id} message={message} sessionId="ao-1" />
				))}
			</>,
		);

		expect(screen.getByLabelText(/^Sent Yesterday · \d{2}:\d{2}$/)).toBeInTheDocument();
		expect(screen.getByLabelText(/^Sent [A-Z][a-z]{2} \d{1,2}, \d{4}$/)).toBeInTheDocument();
	});
});

describe("ChatWorkspace timeline", () => {
	it("makes composer and history controls inert while a durable agent switch owns input", () => {
		render(<ChatWorkspace snapshot={idleSnapshot()} agentInputDisabled />);

		expect(screen.getByTestId("chat-conversation-panel")).toHaveAttribute("inert");
	});

	it("fences new work during a drain while keeping the current turn's approval interactive", async () => {
		const user = userEvent.setup();
		const onDecide = vi.fn();
		const view = render(<ChatWorkspace snapshot={idleSnapshot()} newWorkDisabled />);
		expect(screen.getByText("Switching to terminal UI…")).toBeInTheDocument();
		expect(screen.queryByText("The controller is not connected")).not.toBeInTheDocument();

		expect(screen.getByTestId("chat-conversation-panel")).not.toHaveAttribute("inert");
		expect(screen.getByLabelText("Message the agent")).toHaveAttribute("aria-disabled", "true");

		const snapshot = structuredClone(chatFixture);
		snapshot.items.push({
			kind: "activity",
			id: "drain-approval",
			sequence: 99,
			revision: 1,
			turnId: "turn-2",
			activityKind: "approval",
			status: "pending",
			summary: "Run command",
			requestId: "drain-approval",
			decisions: [{ id: "allow_once", label: "Allow Once", kind: "allow_once" }],
			detail: { command: "npm test" },
			createdAt: "2026-08-08T00:00:00Z",
		});
		view.rerender(
			<ChatWorkspace snapshot={snapshot} newWorkDisabled onDecide={onDecide} />,
		);

		const approval = screen.getByRole("group", { name: "Approval request drain-approval" });
		await user.click(within(approval).getByRole("button", { name: /Allow once/ }));
		expect(onDecide).toHaveBeenCalledWith("drain-approval", "allow_once");
	});

	it("labels worker and orchestrator primary tabs with accessible provider context", () => {
		const view = render(<ChatWorkspace snapshot={chatFixture} session={chatSession} sessionRole="worker" />);

		expect(screen.getByLabelText("Chat")).toHaveAttribute("data-session-role", "worker");
		expect(screen.getByTestId("session-workspace-topbar")).toBeInTheDocument();
		expect(screen.getByTestId("session-terminal-region")).toBeInTheDocument();
		const workerTab = screen.getByRole("tab", { name: "Reviewer chat · Codex · Working" });
		expect(workerTab).toHaveTextContent(chatSession.title);
		expect(workerTab).not.toHaveTextContent("Codex");
		expect(workerTab.querySelector('img[aria-hidden="true"]')).toBeInTheDocument();

		view.rerender(
			<ChatWorkspace
				snapshot={chatFixture}
				session={{ ...chatSession, id: "ao-demo-orchestrator", kind: "orchestrator" }}
				sessionRole="orchestrator"
			/>,
		);

		expect(screen.getByLabelText("Chat")).toHaveAttribute("data-session-role", "orchestrator");
		expect(screen.getByTestId("session-workspace-topbar")).toBeInTheDocument();
		const actionRegion = screen.getByTestId("session-action-region");
		expect(actionRegion).toHaveClass("pl-2", "pr-3");
		expect(actionRegion).not.toHaveClass("px-3");
		expect(screen.getByRole("tab", { name: "Orchestrator · Codex · Working" })).toBeInTheDocument();

		view.rerender(
			<ChatWorkspace
				snapshot={chatFixture}
				session={{ ...chatSession, id: "legacy-orchestrator", kind: undefined }}
			/>,
		);
		expect(screen.getByRole("tab", { name: "Orchestrator · Codex · Working" })).toBeInTheDocument();
	});

	it("refreshes the owning workspace after renaming the primary chat tab", async () => {
		const user = userEvent.setup();
		const onSessionRenamed = vi.fn().mockResolvedValue(undefined);
		render(
			<ChatWorkspace
				snapshot={chatFixture}
				session={chatSession}
				sessionRole="worker"
				onSessionRenamed={onSessionRenamed}
			/>,
		);

		await user.dblClick(screen.getByRole("tab", { name: "Reviewer chat · Codex · Working" }));
		const input = screen.getByRole("textbox", { name: "Rename Reviewer chat" });
		await user.clear(input);
		await user.type(input, "Focused review{Enter}");

		await waitFor(() => expect(renameSessionMock).toHaveBeenCalledWith(chatSession.id, "Focused review"));
		expect(onSessionRenamed).toHaveBeenCalledOnce();
	});

	it("clears the fixed titlebar nav when the sidebar is collapsed, like the terminal session", () => {
		useUiStore.setState({ isSidebarOpen: false });
		const { rerender } = render(<ChatWorkspace snapshot={chatFixture} />);

		expect(screen.getByTestId("session-terminal-region")).toHaveClass(
			"session-topbar-titlebar-clearance-mac",
		);

		useUiStore.setState({ isSidebarOpen: true });
		rerender(<ChatWorkspace snapshot={chatFixture} />);

		expect(screen.getByTestId("session-terminal-region")).not.toHaveClass(
			"session-topbar-titlebar-clearance-mac",
		);
	});

	it("keeps session tab actions on the primary chat tab, like the terminal session", () => {
		render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				session={chatSession}
				sessionTabAction={<button type="button">Session tab action</button>}
				headerActions={<button type="button">Workspace action</button>}
			/>,
		);

		const terminalRegion = screen.getByTestId("session-terminal-region");
		expect(terminalRegion).toContainElement(screen.getByRole("tab", { name: /^Reviewer chat/ }));
		expect(terminalRegion).toContainElement(screen.getByRole("button", { name: "Session tab action" }));
		expect(screen.getByTestId("session-tab-action")).toContainElement(
			screen.getByRole("button", { name: "Session tab action" }),
		);
		const actionRegion = screen.getByTestId("session-action-region");
		expect(actionRegion).toContainElement(screen.getByRole("button", { name: "Workspace action" }));
		expect(actionRegion).not.toContainElement(screen.getByRole("button", { name: "Session tab action" }));
	});

	it("leaves new-terminal and display controls out of the chat strip, like the terminal session", () => {
		render(<ChatWorkspace snapshot={chatFixture} onOpenShell={vi.fn()} />);

		const terminalRegion = screen.getByTestId("session-terminal-region");
		expect(terminalRegion).toContainElement(screen.getByRole("tablist", { name: "Chat tabs" }));
		expect(terminalRegion).not.toContainElement(
			screen.queryByRole("button", { name: "New terminal" }),
		);
		expect(
			screen.queryByRole("toolbar", { name: "Chat display controls" }),
		).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Decrease font size" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Fullscreen" })).not.toBeInTheDocument();
	});

	it("starts chat text at 14px without a topbar font control", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		const chat = screen.getByLabelText("Chat");

		expect(chat.style.getPropertyValue("--chat-font-size")).toBe("14px");
	});

	it("keeps the composer aligned to the readable conversation width", () => {
		render(<ChatWorkspace snapshot={idleSnapshot(chatFixtureSettled)} />);
		const composer = screen.getByLabelText("Message the agent").closest("form");
		expect(composer?.parentElement).toHaveClass("mx-auto", "w-full", "max-w-3xl");
	});

	it("shows live working state inline with the current turn while the composer owns the stop action", async () => {
		const user = userEvent.setup();
		const onInterrupt = vi.fn();
		const snapshot = structuredClone(chatFixture);
		snapshot.items = snapshot.items.filter(
			(item) =>
				!(
					item.kind === "activity" &&
					item.activityKind === "approval" &&
					item.status === "pending"
				),
		);

		render(<ChatWorkspace snapshot={snapshot} onInterrupt={onInterrupt} />);

		const status = screen.getByTestId("live-turn-status");
		expect(screen.getByRole("log", { name: "Conversation" })).toContainElement(status);
		expect(status).toHaveClass("min-h-6", "px-1");
		expect(status).not.toHaveClass("border", "bg-surface", "rounded-md");
		expect(status).toHaveTextContent(/^Working for /);
		expect(within(status).queryByRole("button")).not.toBeInTheDocument();

		const stop = screen.getByRole("button", { name: "Stop turn" });
		expect(screen.getByLabelText("Message the agent").closest("form")).toContainElement(stop);
		await user.click(stop);
		expect(onInterrupt).toHaveBeenCalledOnce();
	});

	it("replaces the generic working label with Claude's live retry count and backoff", () => {
		const snapshot = structuredClone(chatFixture);
		snapshot.items = snapshot.items.filter(
			(item) =>
				!(
					item.kind === "activity" &&
					item.activityKind === "approval" &&
					item.status === "pending"
				),
		);
		snapshot.items.push({
			kind: "activity",
			id: "retry-1",
			turnId: "turn-2",
			sequence: 100,
			revision: 2,
			activityKind: "system",
			status: "running",
			summary: "Reconnecting to Claude, attempt 2 of 10.",
			detail: {
				event: "provider.failure",
				category: "connection",
				severity: "warning",
				text: "The API request failed. Trying again in 4s.",
			},
			createdAt: "2026-08-28T00:00:00Z",
		});

		render(<ChatWorkspace snapshot={snapshot} onInterrupt={vi.fn()} />);

		const status = screen.getByTestId("live-turn-status");
		expect(status).toHaveTextContent("Reconnecting to Claude, attempt 2 of 10.");
		expect(status).toHaveTextContent("The API request failed. Trying again in 4s.");
		expect(status).not.toHaveTextContent("Working for");
	});

	it("interrupts the active turn when Escape is pressed", () => {
		const onInterrupt = vi.fn();
		const snapshot = structuredClone(chatFixture);
		snapshot.items = snapshot.items.filter(
			(item) =>
				!(
					item.kind === "activity" &&
					item.activityKind === "approval" &&
					item.status === "pending"
				),
		);

		render(<ChatWorkspace snapshot={snapshot} onInterrupt={onInterrupt} />);
		fireEvent.keyDown(screen.getByLabelText("Message the agent"), {
			key: "Escape",
		});

		expect(onInterrupt).toHaveBeenCalledOnce();
	});

	it("focuses the composer from conversation whitespace but preserves button focus", async () => {
		const user = userEvent.setup();
		const snapshot = structuredClone(chatFixture);
		snapshot.items = snapshot.items.filter(
			(item) =>
				!(
					item.kind === "activity" &&
					item.activityKind === "approval" &&
					item.status === "pending"
				),
		);

		render(<ChatWorkspace snapshot={snapshot} onInterrupt={vi.fn()} />);
		const composer = screen.getByLabelText("Message the agent");
		await user.click(screen.getByRole("log", { name: "Conversation" }));
		expect(document.activeElement).toBe(composer);

		const stop = screen.getByRole("button", { name: "Stop turn" });
		await user.click(stop);
		expect(document.activeElement).toBe(stop);
	});

	it("does not steal focus from a text selection in the conversation", () => {
		const snapshot = structuredClone(chatFixture);
		snapshot.items = snapshot.items.filter(
			(item) =>
				!(
					item.kind === "activity" &&
					item.activityKind === "approval" &&
					item.status === "pending"
				),
		);
		render(<ChatWorkspace snapshot={snapshot} onInterrupt={vi.fn()} />);

		const selection = window.getSelection();
		const range = document.createRange();
		const text = screen.getByRole("log", { name: "Conversation" }).querySelector("p")?.firstChild;
		expect(text).not.toBeNull();
		range.setStart(text as Text, 0);
		range.setEnd(text as Text, Math.min(4, text?.textContent?.length ?? 0));
		selection?.removeAllRanges();
		selection?.addRange(range);

		fireEvent.click(screen.getByRole("log", { name: "Conversation" }));
		expect(selection?.isCollapsed).toBe(false);
	});

	it("does not interrupt while an elicitation is open", () => {
		const onInterrupt = vi.fn();
		const snapshot = structuredClone(chatFixture);
		snapshot.turns[0] = { ...snapshot.turns[0], state: "running" };
		snapshot.items = snapshot.items.filter(
			(item) =>
				!(
					item.kind === "activity" &&
					item.activityKind === "approval" &&
					item.status === "pending"
				),
		);
		snapshot.items.push({
			kind: "activity",
			id: "input-1",
			sequence: 100,
			revision: 1,
			turnId: "turn-1",
			activityKind: "user_input",
			status: "pending",
			summary: "Choose a direction",
			requestId: "input-1",
			detail: { inputMode: "form", message: "Choose a direction" },
			createdAt: "2026-08-24T00:00:00Z",
		});
		render(<ChatWorkspace snapshot={snapshot} onInterrupt={onInterrupt} />);
		fireEvent.keyDown(screen.getByLabelText("Message the agent"), {
			key: "Escape",
		});
		expect(onInterrupt).not.toHaveBeenCalled();
	});

	it("does not interrupt while a menu is open", () => {
		const onInterrupt = vi.fn();
		const snapshot = structuredClone(chatFixture);
		snapshot.turns[0] = { ...snapshot.turns[0], state: "running" };
		snapshot.items = snapshot.items.filter(
			(item) =>
				!(
					item.kind === "activity" &&
					item.activityKind === "approval" &&
					item.status === "pending"
				),
		);
		render(<ChatWorkspace snapshot={snapshot} onInterrupt={onInterrupt} />);
		const menu = document.createElement("div");
		menu.setAttribute("role", "menu");
		menu.setAttribute("data-state", "open");
		document.body.appendChild(menu);
		fireEvent.keyDown(screen.getByLabelText("Message the agent"), {
			key: "Escape",
		});
		expect(onInterrupt).not.toHaveBeenCalled();
		menu.remove();
	});

	it("moves a pending approval into the chat composer", async () => {
		const user = userEvent.setup();
		const onDecide = vi.fn();
		const snapshot = structuredClone(chatFixture);
		snapshot.items.push({
			kind: "activity",
			id: "approval-1",
			sequence: 99,
			revision: 1,
			turnId: "turn-2",
			activityKind: "approval",
			status: "pending",
			summary: "Run command",
			requestId: "approval-1",
			decisions: [
				{ id: "deny", label: "Deny", kind: "reject_once" },
				{ id: "allow_once", label: "Allow Once", kind: "allow_once" },
				{
					id: "always_allow",
					label: "Always Allow",
					kind: "allow_always",
				},
			],
			detail: { command: "npm test" },
			createdAt: "2026-08-08T00:00:00Z",
		});

		render(<ChatWorkspace snapshot={snapshot} onDecide={onDecide} onInterrupt={vi.fn()} />);

		expect(screen.getByRole("alert")).toHaveTextContent("The agent is waiting for your decision.");
		expect(screen.getByText("Do you want to run this command?")).toBeInTheDocument();
		expect(screen.queryByText("Waiting for your decision")).not.toBeInTheDocument();
		expect(screen.queryByText(/^Working for /)).not.toBeInTheDocument();
		const approval = screen.getByRole("group", {
			name: "Approval request approval-1",
		});
		const composer = approval.closest("form");
		expect(composer).toHaveClass("cursor-chat-composer", "border");
		expect(screen.getByRole("log", { name: "Conversation" })).not.toContainElement(approval);
		expect(screen.queryByLabelText("Message the agent")).not.toBeInTheDocument();
		expect(within(approval).queryByText("Terminal")).not.toBeInTheDocument();
		expect(within(approval).getByRole("button", { name: /Deny/ })).toHaveTextContent("DenyEsc");
		expect(within(approval).getByRole("button", { name: /Allow once/ })).toBeInTheDocument();
		expect(
			within(approval).getByRole("button", {
				name: "More approval options",
			}),
		).toBeInTheDocument();
		expect(within(approval).queryByRole("button", { name: "Allow Once" })).not.toBeInTheDocument();
		expect(
			within(approval).queryByRole("button", { name: "Always Allow" }),
		).not.toBeInTheDocument();
		expect(within(approval).queryByRole("button", { name: "Deny" })).not.toBeInTheDocument();
		await user.click(
			within(approval).getByRole("button", {
				name: "More approval options",
			}),
		);
		await user.keyboard("{Escape}");
		expect(onDecide).not.toHaveBeenCalled();

		approval.focus();
		fireEvent.keyDown(approval, { key: "Enter" });
		expect(onDecide).toHaveBeenCalledWith("approval-1", "allow_once");
		fireEvent.keyDown(approval, { key: "Escape" });
		expect(onDecide).toHaveBeenCalledWith("approval-1", "deny");
		onDecide.mockClear();

		const deny = within(approval).getByRole("button", { name: /Deny/ });
		deny.focus();
		fireEvent.keyDown(deny, { key: "Enter" });
		expect(onDecide).not.toHaveBeenCalled();
		fireEvent.click(deny);
		expect(onDecide).toHaveBeenCalledWith("approval-1", "deny");
		onDecide.mockClear();

		document.body.focus();
		fireEvent.keyDown(window, { key: "Enter" });
		expect(onDecide).not.toHaveBeenCalled();

		await user.click(within(approval).getByRole("button", { name: /Allow once/ }));
		expect(onDecide).toHaveBeenCalledWith("approval-1", "allow_once");

		await user.click(
			within(approval).getByRole("button", {
				name: "More approval options",
			}),
		);
		await user.click(screen.getByRole("menuitem", { name: "Always allow this command" }));
		expect(onDecide).toHaveBeenCalledWith("approval-1", "always_allow");
	});

	it("shows an unbound pending approval in the composer", () => {
		const snapshot = structuredClone(chatFixture);
		snapshot.items.push({
			kind: "activity",
			id: "approval-unbound",
			sequence: 100,
			revision: 1,
			activityKind: "approval",
			status: "pending",
			summary: "Run command",
			requestId: "0",
			decisions: [
				{ id: "cancel", label: "Cancel", kind: "reject_once" },
				{ id: "accept", label: "Approve", kind: "allow_once" },
			],
			detail: { command: "touch /tmp/marker" },
			createdAt: "2026-08-08T00:00:00Z",
		});

		render(<ChatWorkspace snapshot={snapshot} onDecide={vi.fn()} onInterrupt={vi.fn()} />);

		const approval = screen.getByRole("group", {
			name: "Approval request 0",
		});
		expect(approval.closest("form")).toHaveClass("cursor-chat-composer");
		expect(screen.queryByLabelText("Message the agent")).not.toBeInTheDocument();
	});

	it("uses ACP edit metadata and semantic kinds instead of guessing opaque labels", () => {
		const onDecide = vi.fn();
		const snapshot = structuredClone(chatFixture);
		snapshot.items.push({
			kind: "activity",
			id: "approval-edit",
			sequence: 101,
			revision: 1,
			turnId: "turn-2",
			activityKind: "approval",
			status: "pending",
			summary: "Write marker.txt",
			requestId: "opaque-request",
			decisions: [
				{ id: "option-a", label: "Einmal", kind: "allow_once" },
				{ id: "option-b", label: "Nein", kind: "reject_once" },
				{ id: "option-c", label: "Immer", kind: "allow_always" },
			],
			detail: { subjectKind: "file_change", toolKind: "edit" },
			createdAt: "2026-08-08T00:00:00Z",
		});

		render(<ChatWorkspace snapshot={snapshot} onDecide={onDecide} onInterrupt={vi.fn()} />);

		expect(screen.getByText("Do you want to allow these file changes?")).toBeInTheDocument();
		expect(screen.queryByText("Do you want to run this command?")).not.toBeInTheDocument();
		const approval = screen.getByRole("group", {
			name: "Approval request opaque-request",
		});
		approval.focus();
		fireEvent.keyDown(approval, { key: "Enter" });
		expect(onDecide).toHaveBeenCalledWith("opaque-request", "option-a");
	});

	it("requires an explicit click for decisions without a semantic kind", () => {
		const onDecide = vi.fn();
		const snapshot = structuredClone(chatFixture);
		snapshot.items.push({
			kind: "activity",
			id: "approval-unknown",
			sequence: 102,
			revision: 1,
			turnId: "turn-2",
			activityKind: "approval",
			status: "pending",
			summary: "Unknown provider decision",
			requestId: "unknown-request",
			decisions: [
				{
					id: "acceptWithDifferentSemantics",
					label: "acceptWithDifferentSemantics",
				},
				{ id: "cancel", label: "Cancel", kind: "reject_once" },
			],
			detail: { subjectKind: "command" },
			createdAt: "2026-08-08T00:00:00Z",
		});

		render(<ChatWorkspace snapshot={snapshot} onDecide={onDecide} onInterrupt={vi.fn()} />);

		const approval = screen.getByRole("group", {
			name: "Approval request unknown-request",
		});
		expect(within(approval).queryByRole("button", { name: /Allow once/ })).not.toBeInTheDocument();
		const providerDecision = within(approval).getByRole("button", {
			name: "acceptWithDifferentSemantics",
		});

		approval.focus();
		fireEvent.keyDown(approval, { key: "Enter" });
		expect(onDecide).not.toHaveBeenCalled();

		fireEvent.click(providerDecision);
		expect(onDecide).toHaveBeenCalledWith("unknown-request", "acceptWithDifferentSemantics");
	});

	it.each([
		["accept", "Approved"],
		["acceptWithExecpolicyAmendment", "Approved and remembered"],
		["cancel", "Cancelled"],
	] as const)("keeps a resolved %s approval compact and explicit", (decision, label) => {
		const snapshot = structuredClone(chatFixture);
		snapshot.items = [
			{
				kind: "activity",
				id: "approval-resolved-1",
				sequence: 99,
				revision: 2,
				turnId: "turn-2",
				activityKind: "approval",
				status: "resolved",
				summary: "Run gh pr create --base main --head ao/example",
				requestId: "approval-resolved-1",
				detail: { decision },
				createdAt: "2026-08-08T00:00:00Z",
			},
		];

		render(<ChatWorkspace snapshot={snapshot} />);

		expect(screen.getByText(label)).toBeInTheDocument();
		expect(screen.getByText("Run gh pr create --base main --head ao/example")).toBeInTheDocument();
		expect(screen.queryByText(/req approval-resolved-1/)).not.toBeInTheDocument();
		expect(
			screen.queryByText("Already answered. This card is kept for the record."),
		).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
	});

	it("uses the preserved semantic kind for an opaque resolved decision", () => {
		const snapshot = structuredClone(chatFixture);
		snapshot.items = [
			{
				kind: "activity",
				id: "approval-resolved-opaque",
				sequence: 99,
				revision: 2,
				turnId: "turn-2",
				activityKind: "approval",
				status: "resolved",
				summary: "Opaque provider decision",
				requestId: "approval-resolved-opaque",
				decisions: [{ id: "option-b", label: "Nein", kind: "reject_once" }],
				detail: { decision: "option-b" },
				createdAt: "2026-08-08T00:00:00Z",
			},
		];

		render(<ChatWorkspace snapshot={snapshot} />);

		expect(screen.getByText("Cancelled")).toBeInTheDocument();
		expect(screen.queryByText("Resolved")).not.toBeInTheDocument();
	});

	it("does not describe an expired approval as approved", () => {
		const snapshot = structuredClone(chatFixture);
		snapshot.items = [
			{
				kind: "activity",
				id: "approval-expired-1",
				sequence: 99,
				revision: 2,
				turnId: "turn-2",
				activityKind: "approval",
				status: "failed",
				summary: "Run npm test",
				requestId: "approval-expired-1",
				createdAt: "2026-08-08T00:00:00Z",
			},
		];

		render(<ChatWorkspace snapshot={snapshot} />);

		expect(screen.getByText("Approval expired")).toBeInTheDocument();
		expect(screen.queryByText("Approved")).not.toBeInTheDocument();
	});

	it("lets readers select conversation text", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);

		expect(screen.getByRole("log", { name: "Conversation" })).toHaveClass("select-text");
	});

	it("routes rendered message links through the session link handler", async () => {
		const user = userEvent.setup();
		const snapshot = structuredClone(chatFixtureSettled);
		const message = snapshot.items.find(
			(item): item is ConversationMessage => item.kind === "message" && item.role === "assistant",
		);
		if (!message) throw new Error("fixture has no assistant message");
		message.text = "Open the [local preview](http://localhost:5173).";
		const onLinkOpen = vi.fn();

		render(<ChatWorkspace snapshot={snapshot} onLinkOpen={onLinkOpen} />);
		await user.click(screen.getByRole("link", { name: "local preview" }));

		expect(onLinkOpen).toHaveBeenCalledWith("http://localhost:5173");
	});

	it.each([
		["human", "user"],
		["automation", "user"],
		["daemon", "user"],
		["provider", "assistant"],
	] as const)("activates session links from %s messages", async (origin, role) => {
		const snapshot = structuredClone(chatFixtureSettled);
		const template = snapshot.items.find((item): item is ConversationMessage => item.kind === "message");
		if (!template) throw new Error("fixture has no message");
		snapshot.items = [{ ...template, id: `link-${origin}`, origin, role, text: "ao://sessions/project/session", streaming: false }];
		const onSessionLinkOpen = vi.fn();
		render(<ChatWorkspace snapshot={snapshot} onSessionLinkOpen={onSessionLinkOpen} />);
		await userEvent.setup().click(screen.getByRole("link"));
		expect(onSessionLinkOpen).toHaveBeenCalledWith("ao://sessions/project/session");
	});

	it("offers real recovery actions when the controller stops", async () => {
		const user = userEvent.setup();
		const resume = vi.fn();
		const openShell = vi.fn();
		render(
			<ChatWorkspace
				snapshot={{
					...chatFixtureSettled,
					controller: { state: "stopped" },
				}}
				onResumeAgent={resume}
				onOpenShell={openShell}
			/>,
		);

		expect(screen.getByRole("alert")).toHaveTextContent("The agent controller stopped");
		expect(screen.getByText("The controller is not connected")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Resume agent" }));
		await user.click(screen.getByRole("button", { name: "Open shell" }));
		expect(resume).toHaveBeenCalledOnce();
		expect(openShell).toHaveBeenCalledOnce();
	});

	it("shows connecting during the controller gap, then restores the composer when ready", () => {
		const { rerender } = render(
			<ChatWorkspace
				snapshot={{
					...chatFixtureSettled,
					controller: { state: "stopped" },
				}}
				controllerTransitioning
				onResumeAgent={vi.fn()}
				onOpenShell={vi.fn()}
			/>,
		);

		expect(screen.queryByText("The agent controller stopped")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Resume agent" })).not.toBeInTheDocument();
		expect(screen.getByText("Connecting to the agent…")).toBeInTheDocument();
		expect(screen.queryByText("The controller is not connected")).not.toBeInTheDocument();
		expect(screen.getByRole("combobox", { name: "Message the agent" })).toHaveAttribute("contenteditable", "false");

		rerender(<ChatWorkspace snapshot={chatFixtureEmpty} />);
		expect(screen.queryByText("Connecting to the agent…")).not.toBeInTheDocument();
		expect(screen.getByRole("combobox", { name: "Message the agent" })).toHaveAttribute("contenteditable", "true");
	});

	it("announces thread and tool-server failures", () => {
		const { rerender } = render(<ChatWorkspace snapshot={chatFixtureThreadError} />);
		expect(screen.getByRole("alert")).toHaveTextContent("thread hit an internal error");

		rerender(<ChatWorkspace snapshot={chatFixtureMcpFailed} />);
		expect(screen.getByRole("status")).toHaveTextContent(/tool servers? did not start/);
	});

	it("reuses anchor measurements while scrolling and refreshes after content mutations", () => {
		useUiStore.setState({ inspectorSessions: { "ao-long": { isOpen: false, view: "summary" } } });
		render(<ChatWorkspace snapshot={chatFixtureLongHistory(8)} />);
		const log = screen.getByRole("log");
		stubGeometry(log, { scrollHeight: 4000, clientHeight: 800, scrollTop: 1000 });
		const anchors = Array.from(log.querySelectorAll<HTMLElement>("[data-chat-scroll-anchor]"));
		const reads = anchors.map((anchor) => vi.spyOn(anchor, "getBoundingClientRect"));
		fireEvent.scroll(log);
		for (const read of reads) read.mockClear();
		for (let i = 0; i < 5; i++) {
			log.scrollTop += 10;
			fireEvent.scroll(log);
		}
		expect(reads.reduce((total, read) => total + read.mock.calls.length, 0)).toBe(0);
		// Same overall content height can hide a changed prompt position. Detect
		// DOM mutations too, including those queued before the observer callback.
		reads[0]!.mockReturnValue({ top: 1000, height: 20 } as DOMRect);
		anchors[0]!.setAttribute("style", "padding-top: 20px");
		fireEvent.scroll(log);
		expect(reads.every((read) => read.mock.calls.length > 0)).toBe(true);
		const marker = screen.getByRole("scrollbar", { name: "Conversation scrollbar" }).querySelector<HTMLElement>("[data-chat-scroll-marker]");
		expect(Number(marker?.dataset.scrollTarget)).toBe(1660);
	});

	it("provides an interactive conversation minimap", () => {
		useUiStore.setState({
			inspectorSessions: {
				"ao-long": { isOpen: false, view: "summary" },
			},
		});
		render(<ChatWorkspace snapshot={chatFixtureLongHistory(8)} />);
		const log = screen.getByRole("log");
		const scrollbar = screen.getByRole("scrollbar", {
			name: "Conversation scrollbar",
		});
		stubGeometry(log, {
			scrollHeight: 4000,
			clientHeight: 800,
			scrollTop: 1000,
		});
		stubGeometry(scrollbar, {
			scrollHeight: 800,
			clientHeight: 800,
			scrollTop: 0,
		});

		fireEvent.scroll(log);
		expect(scrollbar).toHaveAttribute("aria-valuenow", "31");
		const markers = Array.from(
			scrollbar.querySelectorAll<HTMLElement>("[data-chat-scroll-marker]"),
		);
		expect(markers.length).toBeGreaterThan(1);
		expect(
			Number.parseFloat(markers[1]!.style.top) - Number.parseFloat(markers[0]!.style.top),
		).toBeLessThanOrEqual(8);

		fireEvent.wheel(scrollbar, { deltaY: 200 });
		expect(log.scrollTop).toBe(1200);
		expect(scrollbar).toHaveAttribute("aria-valuenow", "38");

		fireEvent.keyDown(scrollbar, { key: "End" });
		expect(log.scrollTop).toBe(3200);
		expect(scrollbar).toHaveAttribute("aria-valuenow", "100");
	});

	it("disables the conversation minimap while the inspector is open", () => {
		useUiStore.setState({
			inspectorSessions: { "ao-long": { isOpen: true, view: "summary" } },
		});
		render(<ChatWorkspace snapshot={chatFixtureLongHistory(8)} />);
		const log = screen.getByRole("log");
		const scrollbar = screen.getByTestId("chat-conversation-minimap");
		stubGeometry(log, {
			scrollHeight: 4000,
			clientHeight: 800,
			scrollTop: 1000,
		});
		stubGeometry(scrollbar, {
			scrollHeight: 800,
			clientHeight: 800,
			scrollTop: 0,
		});
		fireEvent.scroll(log);

		expect(scrollbar).toHaveAttribute("aria-hidden", "true");
		expect(scrollbar).toHaveClass("pointer-events-none");
		expect(scrollbar.querySelectorAll("[data-chat-scroll-marker]")).toHaveLength(0);

		fireEvent.wheel(scrollbar, { deltaY: 200 });
		expect(log.scrollTop).toBe(1000);
	});

	it("re-enables the conversation minimap when the inspector closes again", async () => {
		useUiStore.setState({
			inspectorSessions: { "ao-long": { isOpen: false, view: "summary" } },
		});
		render(<ChatWorkspace snapshot={chatFixtureLongHistory(8)} />);
		const log = screen.getByRole("log");
		const scrollbar = screen.getByTestId("chat-conversation-minimap");
		stubGeometry(log, {
			scrollHeight: 4000,
			clientHeight: 800,
			scrollTop: 1000,
		});
		stubGeometry(scrollbar, {
			scrollHeight: 800,
			clientHeight: 800,
			scrollTop: 0,
		});
		fireEvent.scroll(log);

		expect(scrollbar.querySelectorAll("[data-chat-scroll-marker]").length).toBeGreaterThan(0);
		fireEvent.wheel(scrollbar, { deltaY: 200 });
		expect(log.scrollTop).toBe(1200);

		act(() => {
			useUiStore.setState({
				inspectorSessions: { "ao-long": { isOpen: true, view: "summary" } },
			});
		});
		await waitFor(() => {
			expect(scrollbar).toHaveAttribute("aria-hidden", "true");
		});
		fireEvent.wheel(scrollbar, { deltaY: 200 });
		expect(log.scrollTop).toBe(1200);

		act(() => {
			useUiStore.setState({
				inspectorSessions: { "ao-long": { isOpen: false, view: "summary" } },
			});
		});
		await waitFor(() => {
			expect(scrollbar).not.toHaveAttribute("aria-hidden", "true");
			expect(scrollbar.querySelectorAll("[data-chat-scroll-marker]").length).toBeGreaterThan(0);
		});
		fireEvent.wheel(scrollbar, { deltaY: 200 });
		expect(log.scrollTop).toBe(1400);
	});

	it("previews the request and response for a hovered conversation marker", () => {
		useUiStore.setState({
			inspectorSessions: {
				"ao-long": { isOpen: false, view: "summary" },
			},
		});
		render(<ChatWorkspace snapshot={chatFixtureLongHistory(4)} />);
		const log = screen.getByRole("log");
		const scrollbar = screen.getByRole("scrollbar", {
			name: "Conversation scrollbar",
		});
		stubGeometry(log, {
			scrollHeight: 2400,
			clientHeight: 600,
			scrollTop: 0,
		});
		stubGeometry(scrollbar, {
			scrollHeight: 600,
			clientHeight: 600,
			scrollTop: 0,
		});
		fireEvent.scroll(log);

		const markers = Array.from(
			scrollbar.querySelectorAll<HTMLElement>("[data-chat-scroll-marker]"),
		);
		expect(markers.length).toBeGreaterThan(2);
		fireEvent.pointerEnter(markers[0]!);

		const preview = screen.getByRole("tooltip");
		expect(preview).toHaveTextContent("Wire the snapshot endpoint into the handler (round 1)");
		expect(preview).toHaveTextContent("Done. conversation now returns the durable snapshot");
		expect(markers[0]!.querySelector(".chat-scroll-marker")).toHaveClass(
			"chat-scroll-marker-active",
		);
		expect(markers[1]!.querySelector(".chat-scroll-marker")).toHaveClass(
			"chat-scroll-marker-adjacent",
		);

		fireEvent.pointerLeave(scrollbar);
		expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
	});

	it("limits conversation minimap navigation to human prompts", () => {
		const snapshot = structuredClone(chatFixtureLongHistory(2));
		snapshot.items.splice(1, 0, {
			kind: "activity",
			id: "loose-status",
			sequence: 1.5,
			revision: 1,
			activityKind: "system",
			status: "completed",
			summary: "Automatic compaction completed",
			createdAt: "2026-08-08T00:00:00Z",
		});
		useUiStore.setState({
			inspectorSessions: {
				[snapshot.sessionId]: { isOpen: false, view: "summary" },
			},
		});
		render(<ChatWorkspace snapshot={snapshot} />);
		const log = screen.getByRole("log");
		const scrollbar = screen.getByRole("scrollbar", {
			name: "Conversation scrollbar",
		});
		stubGeometry(log, {
			scrollHeight: 1800,
			clientHeight: 600,
			scrollTop: 0,
		});
		stubGeometry(scrollbar, {
			scrollHeight: 600,
			clientHeight: 600,
			scrollTop: 0,
		});
		fireEvent.scroll(log);

		const markers = Array.from(
			scrollbar.querySelectorAll<HTMLElement>("[data-chat-scroll-marker]"),
		);
		expect(markers).toHaveLength(2);
		fireEvent.pointerEnter(markers[1]!);
		expect(screen.getByRole("tooltip")).not.toHaveTextContent("Automatic compaction completed");
	});

	it("centers the composer on an empty conversation instead of a starter blurb", () => {
		render(<ChatWorkspace snapshot={chatFixtureEmpty} />);
		expect(screen.queryByText("Start the conversation")).not.toBeInTheDocument();
		expect(screen.queryByRole("log")).not.toBeInTheDocument();
		expect(screen.getByLabelText("Message the agent")).toBeInTheDocument();
		expect(
			screen
				.getByTestId("chat-conversation-panel")
				.querySelector("[data-composer-placement='center']"),
		).not.toBeNull();
	});

	it("docks the composer once the conversation has content", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		expect(
			screen
				.getByTestId("chat-conversation-panel")
				.querySelector("[data-composer-placement='dock']"),
		).not.toBeNull();
	});

	it("keeps a turn as one block, positioned by its first item", () => {
		// The fixture's automation relay carries sequence 8, in the middle of turn-2's
		// items. Reading strictly by sequence would split turn-2 around it; the rule is
		// that a turn takes the position of its first item and stays contiguous.
		render(<ChatWorkspace snapshot={chatFixture} />);
		const log = screen.getByRole("log");
		const text = log.textContent ?? "";
		const question = text.indexOf("Run the backend tests");
		const answer = text.indexOf("Tests are still running");
		const relay = text.indexOf("Checks failed on the base branch");

		expect(question).toBeGreaterThan(-1);
		expect(answer).toBeGreaterThan(question);
		expect(relay).toBeGreaterThan(answer);

		const relayCard = screen.getByText(/Checks failed on the base branch/).parentElement;
		expect(relayCard).toHaveClass("border-l-logo-accent/60");
		expect(relayCard?.querySelector("svg")).toHaveClass("text-logo-accent");
	});

	it("offers Jump to latest only once the reader has scrolled away from the bottom", async () => {
		const user = userEvent.setup();
		render(<ChatWorkspace snapshot={chatFixtureLongHistory(8)} />);
		const log = screen.getByRole("log");

		expect(screen.queryByRole("button", { name: /jump to latest/i })).not.toBeInTheDocument();

		stubGeometry(log, {
			scrollHeight: 4000,
			clientHeight: 800,
			scrollTop: 100,
		});
		log.dispatchEvent(new Event("scroll"));
		const jump = await screen.findByRole("button", {
			name: /jump to latest/i,
		});
		expect(jump).toHaveAttribute("title", "Jump to latest");
		expect(jump).not.toHaveTextContent("Jump to latest");
		expect(jump).toHaveClass("rounded-full", "size-12", "bg-raised", "dark:bg-raised");
		expect(jump).not.toHaveClass("dark:bg-transparent");
		expect(jump).not.toHaveClass("dark:hover:bg-input/30");

		// Taking the jump re-arms following, so the control retires itself.
		await user.click(jump);
		expect(log.scrollTop).toBe(4000);
		await waitFor(() =>
			expect(screen.queryByRole("button", { name: /jump to latest/i })).not.toBeInTheDocument(),
		);
	});

	it("does not yank a reader who scrolled up when the next poll arrives", async () => {
		const snapshot = chatFixtureLongHistory(8);
		const { rerender } = render(<ChatWorkspace snapshot={snapshot} />);
		const log = screen.getByRole("log");

		stubGeometry(log, {
			scrollHeight: 4000,
			clientHeight: 800,
			scrollTop: 1200,
		});
		log.dispatchEvent(new Event("scroll"));
		await screen.findByRole("button", { name: /jump to latest/i });

		rerender(<ChatWorkspace snapshot={{ ...poll(snapshot), latestSequence: 999 }} />);
		expect(log.scrollTop).toBe(1200);
	});

	it("follows new output while the reader is already at the bottom", () => {
		const snapshot = chatFixtureLongHistory(8);
		const { rerender } = render(<ChatWorkspace snapshot={snapshot} />);
		const log = screen.getByRole("log");
		stubGeometry(log, {
			scrollHeight: 4000,
			clientHeight: 800,
			scrollTop: 0,
		});

		rerender(<ChatWorkspace snapshot={{ ...poll(snapshot), latestSequence: 999 }} />);
		expect(log.scrollTop).toBe(4000);
	});

	it("keeps a trailing spacer so a dropped prompt can sit near the top", async () => {
		const snapshot = chatFixtureSettled;
		const { rerender } = render(<ChatWorkspace snapshot={snapshot} />);
		const log = screen.getByRole("log");
		const spacer = screen.getByTestId("chat-prompt-spacer");
		const anchors = log.querySelectorAll<HTMLElement>("[data-chat-scroll-anchor]");
		const anchor = anchors[anchors.length - 1];
		expect(anchor).toBeTruthy();

		const contentHeight = 240;
		Object.defineProperty(spacer, "offsetHeight", {
			configurable: true,
			get: () => Number.parseFloat(spacer.style.height || "0") || 0,
		});
		Object.defineProperty(log, "clientHeight", {
			configurable: true,
			value: 800,
		});
		Object.defineProperty(log, "scrollHeight", {
			configurable: true,
			get: () => contentHeight + (Number.parseFloat(spacer.style.height || "0") || 0),
		});
		Object.defineProperty(log, "scrollTop", {
			configurable: true,
			writable: true,
			value: 0,
		});
		log.getBoundingClientRect = () =>
			({
				x: 0,
				y: 0,
				top: 0,
				left: 0,
				bottom: 800,
				right: 480,
				width: 480,
				height: 800,
				toJSON() {
					return this;
				},
			}) as DOMRect;
		anchor!.getBoundingClientRect = () =>
			({
				x: 0,
				y: 48,
				top: 48,
				left: 0,
				bottom: 96,
				right: 480,
				width: 480,
				height: 48,
				toJSON() {
					return this;
				},
			}) as DOMRect;

		await act(async () => {
			rerender(
				<ChatWorkspace
					snapshot={{
						...snapshot,
						latestSequence: snapshot.latestSequence + 1,
					}}
				/>,
			);
		});

		expect(Number.parseFloat(spacer.style.height)).toBe(
			promptSpacerHeight({
				viewportHeight: 800,
				contentHeightWithoutSpacer: contentHeight,
				anchorOffset: 48,
				topInset: promptTopInset(800),
			}),
		);
		expect(log.scrollTop).toBe(log.scrollHeight);
	});

	it("survives a poll without disturbing what the reader opened", async () => {
		const user = userEvent.setup();
		const snapshot = chatFixtureLongHistory(3);
		const { rerender } = render(<ChatWorkspace snapshot={snapshot} />);

		// A run of tool calls is collapsed; opening one is local state that a poll must
		// not reset, which is what remounting the subtree on every refetch would do.
		const run = screen.getAllByRole("button", { expanded: false })[0]!;
		await user.click(run);
		expect(run).toHaveAttribute("aria-expanded", "true");

		rerender(<ChatWorkspace snapshot={poll(snapshot)} />);
		expect(run).toHaveAttribute("aria-expanded", "true");
	});
});

describe("automation reports", () => {
	it("collapses a long report until the reader asks to expand it", async () => {
		const user = userEvent.setup();
		const source = chatFixture.items.find((item) => item.id === "m-4") as ConversationMessage;
		const message: ConversationMessage = {
			...source,
			text: `READ-ONLY follow-up report. ${"Adapter evidence and implementation details. ".repeat(20)}END OF REPORT`,
		};
		render(<OriginMessage message={message} />);

		expect(screen.getByRole("button", { name: "Show full report" })).toHaveAttribute(
			"aria-expanded",
			"false",
		);
		expect(screen.queryByText(/END OF REPORT/)).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Show full report" }));
		expect(screen.getByText(/END OF REPORT/)).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Hide report" })).toHaveAttribute(
			"aria-expanded",
			"true",
		);
	});

	it("keeps a short automation alert fully visible", () => {
		const message = chatFixture.items.find((item) => item.id === "m-4") as ConversationMessage;
		render(<OriginMessage message={message} />);
		expect(screen.getByText(/Checks failed on the base branch/)).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Show full report" })).not.toBeInTheDocument();
	});
});

describe("ChatWorkspace message actions", () => {
	it("hides first-message editing until the conversation controller is ready", () => {
		render(
			<ChatWorkspace
				snapshot={{
					...idleSnapshot(),
					controller: { state: "stopped" },
					capabilities: ["fork", "prompt_replay", "embedded_context"],
					hasMoreBefore: false,
				}}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		expect(screen.queryByRole("button", { name: "Edit user message" })).not.toBeInTheDocument();
	});

	it("offers exact first-message edits even when the provider cannot branch or replay history", async () => {
		const user = userEvent.setup();
		const onEditMessage = vi.fn(async () => undefined);
		const view = render(
			<ChatWorkspace
				snapshot={{
					...idleSnapshot(),
					capabilities: [],
					hasMoreBefore: true,
				}}
				onEditMessage={onEditMessage}
			/>,
		);

		// The earliest loaded prompt is not necessarily the first provider prompt
		// until every older page has been loaded.
		expect(screen.queryByRole("button", { name: "Edit user message" })).not.toBeInTheDocument();
		view.rerender(
			<ChatWorkspace
				snapshot={{
					...idleSnapshot(),
					capabilities: [],
					hasMoreBefore: false,
				}}
				onEditMessage={onEditMessage}
			/>,
		);

		const editButtons = screen.getAllByRole("button", {
			name: "Edit user message",
		});
		expect(editButtons).toHaveLength(1);
		await user.click(editButtons[0]!);
		expect(screen.queryByText(/Reconstructed context:/)).not.toBeInTheDocument();

		const editor = screen.getByRole("textbox", { name: "Edit message" });
		await user.clear(editor);
		await user.type(editor, "Replace the first prompt exactly.");
		await user.click(screen.getByRole("button", { name: "Send edited message" }));

		expect(onEditMessage).toHaveBeenCalledWith("turn-1", "Replace the first prompt exactly.");
	});

	it("uses the server's first eligible prompt after a provider boundary", () => {
		const snapshot = idleSnapshot();
		render(
			<ChatWorkspace
				snapshot={{
					...snapshot,
					capabilities: [],
					hasMoreBefore: false,
					items: snapshot.items.map((item) =>
						item.kind === "message" && item.turnId === "turn-1"
							? { ...item, editAvailable: false }
							: item,
					),
				}}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		const edits = screen.getAllByRole("button", {
			name: "Edit user message",
		});
		expect(edits).toHaveLength(1);
	});

	it("offers reconstructed historical edits only when text replay and embedded context are both negotiated", async () => {
		const user = userEvent.setup();
		const onEditMessage = vi.fn(async () => undefined);
		const promptReplayOnly = {
			...idleSnapshot(),
			capabilities: ["prompt_replay"],
		};
		const view = render(
			<ChatWorkspace snapshot={promptReplayOnly} onEditMessage={onEditMessage} />,
		);

		// The first prompt is still exactly reconstructable in a fresh provider session.
		expect(screen.getAllByRole("button", { name: "Edit user message" })).toHaveLength(1);

		view.rerender(
			<ChatWorkspace
				snapshot={{
					...promptReplayOnly,
					capabilities: ["prompt_replay", "embedded_context"],
				}}
				onEditMessage={onEditMessage}
			/>,
		);

		expect(screen.getAllByRole("button", { name: "Edit user message" })).toHaveLength(2);
		await user.click(screen.getAllByRole("button", { name: "Edit user message" })[1]!);
		const disclosure = screen.getByText(/Reconstructed context:/);
		expect(disclosure).toHaveTextContent(
			"Reconstructed context: text messages will be replayed into a new agent session. Tool calls, approvals, and workspace history will not be replayed; current worktree files stay as they are.",
		);
		expect(screen.getByRole("textbox", { name: "Edit message" })).toHaveAttribute(
			"aria-describedby",
			disclosure.id,
		);
	});

	it("keeps replay available when the provider also supports native forks", async () => {
		const user = userEvent.setup();
		const snapshot = idleSnapshot();
		render(
			<ChatWorkspace
				snapshot={{
					...snapshot,
					capabilities: ["fork", "prompt_replay", "embedded_context"],
					branchedFromEarlierMessage: true,
					branchMaterialization: {
						strategy: "approximate_context",
						replayTruncated: false,
					},
					turns: snapshot.turns.map((turn) => ({
						...turn,
						providerTurnId: undefined,
					})),
				}}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		expect(screen.getAllByRole("button", { name: "Edit user message" })).toHaveLength(2);
		await user.click(screen.getAllByRole("button", { name: "Edit user message" })[1]!);
		expect(screen.getByText(/Reconstructed context:/)).toBeVisible();
	});

	it("requires a prior visible provider turn before offering a native historical fork", () => {
		const snapshot = idleSnapshot();
		render(
			<ChatWorkspace
				snapshot={{
					...snapshot,
					capabilities: ["fork"],
					turns: snapshot.turns.map((turn) =>
						turn.id === "turn-1" ? { ...turn, providerTurnId: undefined } : turn,
					),
				}}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		// The first provider prompt can always be restarted fresh. The second needs
		// either a prior provider turn to fork or negotiated reconstructed replay.
		expect(screen.getAllByRole("button", { name: "Edit user message" })).toHaveLength(1);
	});

	it("does not warn about reconstruction when a native fork anchor is visible", async () => {
		const user = userEvent.setup();
		render(
			<ChatWorkspace
				snapshot={{
					...idleSnapshot(),
					capabilities: ["fork", "prompt_replay", "embedded_context"],
					nativeForkAvailableAfterSequence: 1,
				}}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		await user.click(screen.getAllByRole("button", { name: "Edit user message" })[1]!);
		expect(screen.queryByText(/Reconstructed context:/)).not.toBeInTheDocument();
	});

	it("keeps reconstructed ancestor prompts editable when their provider ids belong to the source scope", () => {
		const snapshot = idleSnapshot();
		render(
			<ChatWorkspace
				snapshot={{
					...snapshot,
					capabilities: ["prompt_replay", "embedded_context"],
					branchedFromEarlierMessage: true,
					branchMaterialization: {
						strategy: "approximate_context",
						replayTruncated: false,
					},
					turns: snapshot.turns.map((turn) =>
						turn.id === "turn-1" ? { ...turn, providerTurnId: undefined } : turn,
					),
				}}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		expect(screen.getAllByRole("button", { name: "Edit user message" })).toHaveLength(2);
	});

	it("treats the earliest loaded prompt as historical while older pages remain", async () => {
		const user = userEvent.setup();
		render(
			<ChatWorkspace
				snapshot={{
					...idleSnapshot(),
					hasMoreBefore: true,
					capabilities: ["prompt_replay", "embedded_context"],
				}}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		await user.click(screen.getAllByRole("button", { name: "Edit user message" })[0]!);
		expect(screen.getByText(/Reconstructed context:/)).toBeVisible();
	});

	it("uses the durable native anchor when its prompt is on an older page", async () => {
		const user = userEvent.setup();
		render(
			<ChatWorkspace
				snapshot={{
					...idleSnapshot(),
					hasMoreBefore: true,
					capabilities: ["fork"],
					nativeForkAvailableAfterSequence: 1,
				}}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		const editButtons = screen.getAllByRole("button", {
			name: "Edit user message",
		});
		expect(editButtons).toHaveLength(1);
		await user.click(editButtons[0]!);
		expect(screen.queryByText(/Reconstructed context:/)).not.toBeInTheDocument();
	});

	it("copies a human message as the exact text the user sent", async () => {
		const user = userEvent.setup();
		render(<ChatWorkspace snapshot={chatFixture} />);

		await user.click(screen.getAllByRole("button", { name: "Copy user message" })[0]!);

		expect(writeText).toHaveBeenCalledTimes(1);
		expect(writeText).toHaveBeenCalledWith(
			"Check the worktree state and tell me what changed since the base commit.",
		);
	});

	it("edits a human message through the branch endpoint without touching the composer", async () => {
		const user = userEvent.setup();
		const onRollback = vi.fn();
		const onEditMessage = vi.fn(async () => undefined);
		render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				onRollback={onRollback}
				onEditMessage={onEditMessage}
			/>,
		);
		const composer = screen.getByLabelText("Message the agent");
		await typeInLexicalEditor(composer, "unsent composer draft");

		await user.click(screen.getAllByRole("button", { name: "Edit user message" })[0]!);
		expect(composer).toHaveTextContent("unsent composer draft");

		const editor = screen.getByRole("textbox", { name: "Edit message" });
		expect(editor).toHaveFocus();
		expect(editor).toHaveValue(
			"Check the worktree state and tell me what changed since the base commit.",
		);

		await user.clear(editor);
		await user.type(editor, "Check worktree state, including staged files.");
		fireEvent.keyDown(editor, { key: "Enter", metaKey: true });

		await waitFor(() =>
			expect(onEditMessage).toHaveBeenCalledWith(
				"turn-1",
				"Check worktree state, including staged files.",
			),
		);
		expect(onRollback).not.toHaveBeenCalled();
		expect(composer).toHaveTextContent("unsent composer draft");
	});

	it("hides editing while another turn is active", () => {
		render(<ChatWorkspace snapshot={chatFixture} onEditMessage={vi.fn(async () => undefined)} />);

		expect(screen.queryByRole("button", { name: "Edit user message" })).not.toBeInTheDocument();
	});

	it("retains the inline draft when branch creation fails", async () => {
		const user = userEvent.setup();
		const onEditMessage = vi.fn(async () => {
			throw new Error("branch failed");
		});
		const approximateSnapshot = {
			...idleSnapshot(),
			capabilities: ["prompt_replay", "embedded_context"],
		};
		const view = render(
			<ChatWorkspace
				snapshot={approximateSnapshot}
				onEditMessage={onEditMessage}
				editMessageError="branch failed"
			/>,
		);

		await user.click(screen.getAllByRole("button", { name: "Edit user message" })[1]!);
		const editor = screen.getByRole("textbox", { name: "Edit message" });
		await user.clear(editor);
		await user.type(editor, "keep this draft");
		fireEvent.keyDown(editor, { key: "Enter", ctrlKey: true });

		await waitFor(() => expect(onEditMessage).toHaveBeenCalledWith("turn-2", "keep this draft"));
		expect(screen.getByRole("textbox", { name: "Edit message" })).toHaveValue("keep this draft");
		expect(screen.getByRole("alert")).toHaveTextContent("branch failed");
		expect(screen.getByText(/Reconstructed context:/)).toBeVisible();

		view.rerender(
			<ChatWorkspace
				snapshot={{
					...approximateSnapshot,
					items: [],
					turns: [],
				}}
				onEditMessage={onEditMessage}
				editMessageError="branch failed"
			/>,
		);
		expect(screen.getByRole("textbox", { name: "Edit message" })).toHaveValue("keep this draft");
		expect(screen.getByRole("alert")).toHaveTextContent("branch failed");
		expect(screen.getByText(/Reconstructed context:/)).toBeVisible();

		// An ambiguous provider failure can durably activate the replacement branch.
		// Once that refetch arrives, the source-branch draft is stale and must close.
		view.rerender(
			<ChatWorkspace
				snapshot={{
					...approximateSnapshot,
					activeBranchId: "branch-failed",
					branchedFromEarlierMessage: true,
					items: [],
					turns: [],
				}}
				onEditMessage={onEditMessage}
				editMessageError="branch failed"
			/>,
		);
		await waitFor(() =>
			expect(screen.queryByRole("textbox", { name: "Edit message" })).not.toBeInTheDocument(),
		);
	});

	it("lets the user retry a failed replacement on the active approximate branch", async () => {
		const user = userEvent.setup();
		const snapshot = idleSnapshot();
		render(
			<ChatWorkspace
				snapshot={{
					...snapshot,
					activeBranchId: "branch-failed",
					branchedFromEarlierMessage: true,
					branchMaterialization: {
						strategy: "approximate_context",
						replayTruncated: false,
					},
					capabilities: ["prompt_replay", "embedded_context"],
					turns: snapshot.turns.map((turn) =>
						turn.id === "turn-2"
							? {
									...turn,
									state: "failed" as const,
									providerTurnId: undefined,
									errorMessage: "provider unavailable",
								}
							: turn,
					),
				}}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		const edits = screen.getAllByRole("button", {
			name: "Edit user message",
		});
		expect(edits).toHaveLength(2);
		await user.click(edits[1]!);
		expect(screen.getByRole("textbox", { name: "Edit message" })).toBeVisible();
	});

	it("navigates prompt branches without a persistent context notice", async () => {
		const user = userEvent.setup();
		const onActivateBranch = vi.fn(async () => undefined);
		const snapshot = {
			...idleSnapshot(),
			activeBranchId: "branch-current",
			branchedFromEarlierMessage: true,
			branchMaterialization: {
				strategy: "approximate_context" as const,
				replayTruncated: true,
			},
			branchPoints: [
				{
					turnId: "turn-1",
					position: 2,
					total: 3,
					previousBranchId: "branch-previous",
					nextBranchId: "branch-next",
				},
			],
		};
		render(<ChatWorkspace snapshot={snapshot} onActivateBranch={onActivateBranch} />);

		expect(screen.getByText("2 / 3")).toBeVisible();
		expect(screen.queryByText(/Reconstructed context/)).not.toBeInTheDocument();
		await user.click(
			screen.getByRole("button", {
				name: "Previous conversation branch",
			}),
		);
		expect(onActivateBranch).toHaveBeenCalledWith("branch-previous");
	});

	it("does not label a provider-native branch as exact context", () => {
		render(
			<ChatWorkspace
				snapshot={{
					...idleSnapshot(),
					branchedFromEarlierMessage: true,
					branchMaterialization: {
						strategy: "native",
						replayTruncated: false,
					},
				}}
			/>,
		);

		expect(screen.queryByText(/Exact context/)).not.toBeInTheDocument();
	});

	it("copies an assistant message as the markdown the agent wrote", async () => {
		const user = userEvent.setup();
		const snapshot = structuredClone(chatFixture);
		const finalIndex = snapshot.items.findIndex((item) => item.id === "m-2");
		const finalMessage = snapshot.items[finalIndex] as ConversationMessage;
		snapshot.items.splice(finalIndex, 0, {
			...finalMessage,
			id: "m-intermediate",
			sequence: finalMessage.sequence - 0.5,
			text: "Intermediate progress while the turn is still working.",
		});
		render(<ChatWorkspace snapshot={snapshot} />);

		const copies = screen.getAllByRole("button", {
			name: /copy message as markdown/i,
		});
		expect(copies).toHaveLength(1);
		const copy = copies[0]!;
		await user.click(copy);

		expect(writeText).toHaveBeenCalledTimes(1);
		const copied = writeText.mock.calls[0]![0];
		// The stored text, fences and all — not a re-serialization of the rendered DOM.
		expect(copied).toContain("```go");
		expect(copied).toContain("func (m *Manager) Spawn");
		expect(copied).not.toContain("Intermediate progress");
	});

	it("offers no copy on a message that is still arriving", () => {
		const snapshot = structuredClone(chatFixture);
		snapshot.items = snapshot.items.filter((item) => item.sequence <= 12);
		render(<ChatWorkspace snapshot={snapshot} />);
		// The latest assistant message is mid-stream; half a message is not what the
		// reader means by "copy this", and streaming has no extra visual indicator.
		expect(screen.queryByLabelText("still writing")).not.toBeInTheDocument();
		expect(screen.queryByText("Writing…")).not.toBeInTheDocument();
	});

	it("does not show a writing caret while prose is streaming", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		expect(screen.queryByLabelText("still writing")).not.toBeInTheDocument();
	});
});

describe("ChatWorkspace reviewer tabs", () => {
	const reviewerTerminal = { handleId: "review-1", harness: "codex" };
	const reviewerTarget = {
		kind: "reviewer" as const,
		...reviewerTerminal,
		sessionId: chatSession.id,
	};

	it("makes each full-height tile its semantic click target", () => {
		const onOpenReviewerTerminal = vi.fn();
		render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				session={chatSession}
				reviewerTerminal={reviewerTerminal}
				onOpenReviewerTerminal={onOpenReviewerTerminal}
			/>,
		);

		const chatTab = screen.getByRole("tab", { name: /^Reviewer chat/ });
		const reviewerTab = screen.getByRole("tab", { name: "Reviewer" });
		expect(chatTab).toHaveClass("px-2", "cursor-pointer");
		expect(chatTab.closest("[data-terminal-tab-frame]")).toHaveClass("self-stretch");
		expect(reviewerTab).toHaveClass(
			"self-stretch",
			"px-3",
			"cursor-pointer",
			"focus-visible:outline-2",
		);
		expect(reviewerTab.querySelector("img")).toBeInTheDocument();

		fireEvent.click(reviewerTab);
		expect(onOpenReviewerTerminal).toHaveBeenCalledWith(reviewerTerminal);
	});

	it("keeps the chat draft, attachments, edit, and scroll state mounted while Reviewer is selected", async () => {
		const user = userEvent.setup();
		const common = {
			snapshot: idleSnapshot(),
			session: chatSession,
			reviewerTerminal,
			onOpenReviewerTerminal: vi.fn(),
			onSelectChat: vi.fn(),
			onEditMessage: vi.fn(async () => undefined),
			onStageAttachments: vi.fn(async () => []),
		};
		const view = render(<ChatWorkspace {...common} />);
		const composer = screen.getByRole("combobox", {
			name: "Message the agent",
		});
		await typeInLexicalEditor(composer, "unsent reviewer-switch draft");
		fireEvent.paste(composer, {
			clipboardData: {
				files: [
					new File([new Uint8Array([137, 80, 78, 71])], "review.png", {
						type: "image/png",
					}),
				],
				items: [],
			},
		});
		await waitFor(() => expect(screen.getByLabelText("Remove review.png")).toBeInTheDocument());
		const attachment = screen.getByLabelText("Remove review.png");
		await user.click(screen.getAllByRole("button", { name: "Edit user message" })[0]!);
		const editor = screen.getByRole("textbox", { name: "Edit message" });
		await user.clear(editor);
		await user.type(editor, "in-progress branch edit");
		const timeline = screen.getByRole("log", { name: "Conversation" });
		stubGeometry(timeline, {
			scrollHeight: 2_000,
			clientHeight: 500,
			scrollTop: 417,
		});

		view.rerender(<ChatWorkspace {...common} reviewerTarget={reviewerTarget} />);

		expect(composer).toBeInTheDocument();
		expect(attachment).toBeInTheDocument();
		expect(editor).toBeInTheDocument();
		expect(timeline).toBeInTheDocument();
		expect(screen.getByTestId("chat-conversation-panel")).toHaveAttribute("hidden");
		expect(screen.getByTestId("chat-conversation-panel")).toHaveAttribute("inert");

		view.rerender(<ChatWorkspace {...common} />);

		expect(screen.getByRole("combobox", { name: "Message the agent" })).toBe(composer);
		expect(composer).toHaveTextContent("unsent reviewer-switch draft");
		expect(screen.getByLabelText("Remove review.png")).toBe(attachment);
		expect(screen.getByRole("textbox", { name: "Edit message" })).toBe(editor);
		expect(editor).toHaveValue("in-progress branch edit");
		expect(screen.getByRole("log", { name: "Conversation" })).toBe(timeline);
		expect(timeline.scrollTop).toBe(417);
	});

	it("keeps the selected reviewer pane through temporary terminal unavailability", () => {
		const common = {
			snapshot: idleSnapshot(),
			session: chatSession,
			reviewerTarget,
			onSelectChat: vi.fn(),
		};
		const view = render(<ChatWorkspace {...common} reviewerTerminal={reviewerTerminal} />);
		expect(screen.getByTestId("chat-reviewer-terminal")).toBeInTheDocument();
		expect(screen.getByTestId("chat-reviewer-terminal")).not.toHaveClass("pl-2");

		view.rerender(<ChatWorkspace {...common} reviewerTerminal={undefined} />);

		expect(screen.getByTestId("chat-reviewer-terminal")).toBeInTheDocument();
		expect(screen.queryByRole("tab", { name: "Reviewer" })).not.toBeInTheDocument();
	});

	it("gives the reviewer terminal working zoom and fullscreen controls", async () => {
		window.localStorage.setItem("ao.terminal.fontSize", "14");
		render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				session={chatSession}
				reviewerTerminal={reviewerTerminal}
				reviewerTarget={reviewerTarget}
			/>,
		);

		expect(terminalPaneState.props).toMatchObject({
			fontSize: 14,
			isFullscreen: false,
		});
		expect(terminalPaneState.props?.onChangeFontSize).toEqual(expect.any(Function));
		expect(terminalPaneState.props?.onToggleFullscreen).toEqual(expect.any(Function));

		act(() => terminalPaneState.props?.onChangeFontSize?.(2));
		expect(window.localStorage.getItem("ao.terminal.fontSize")).toBe("16");
		expect(terminalPaneState.props?.fontSize).toBe(16);

		fireEvent.wheel(screen.getByTestId("chat-reviewer-panel"), {
			ctrlKey: true,
			deltaY: -80,
		});
		expect(window.localStorage.getItem("ao.terminal.fontSize")).toBe("17");
		expect(terminalPaneState.props?.fontSize).toBe(17);

		const surface = screen.getByLabelText("Chat");
		const requestFullscreen = vi.fn(async () => undefined);
		Object.defineProperty(surface, "requestFullscreen", {
			configurable: true,
			value: requestFullscreen,
		});
		await act(async () => terminalPaneState.props?.onToggleFullscreen?.());
		expect(requestFullscreen).toHaveBeenCalledOnce();
	});

	it("supports arrow, Home/End, and desktop previous/next tab shortcuts", () => {
		const onOpenReviewerTerminal = vi.fn();
		const onSelectChat = vi.fn();
		const common = {
			snapshot: idleSnapshot(),
			session: chatSession,
			reviewerTerminal,
			onOpenReviewerTerminal,
			onSelectChat,
		};
		const view = render(<ChatWorkspace {...common} />);
		const chatTab = screen.getByRole("tab", {
			name: /^Reviewer chat/,
		});
		const reviewerTab = screen.getByRole("tab", { name: "Reviewer" });
		expect(chatTab).toHaveAttribute("tabindex", "0");
		expect(reviewerTab).toHaveAttribute("tabindex", "-1");

		chatTab.focus();
		fireEvent.keyDown(chatTab, { key: "End" });
		expect(reviewerTab).toHaveFocus();
		expect(onOpenReviewerTerminal).toHaveBeenCalledWith(reviewerTerminal);

		onOpenReviewerTerminal.mockClear();
		chatTab.focus();
		fireEvent.keyDown(chatTab, { key: "ArrowRight" });
		expect(reviewerTab).toHaveFocus();
		expect(onOpenReviewerTerminal).toHaveBeenCalledWith(reviewerTerminal);

		onOpenReviewerTerminal.mockClear();
		expect(nextTabListeners.size).toBe(1);
		act(() => [...nextTabListeners][0]?.());
		expect(onOpenReviewerTerminal).toHaveBeenCalledWith(reviewerTerminal);

		view.rerender(<ChatWorkspace {...common} reviewerTarget={reviewerTarget} />);
		const activeReviewerTab = screen.getByRole("tab", { name: "Reviewer" });
		expect(screen.getByRole("tab", { name: /^Reviewer chat/ })).toHaveAttribute(
			"tabindex",
			"-1",
		);
		expect(activeReviewerTab).toHaveAttribute("tabindex", "0");

		activeReviewerTab.focus();
		fireEvent.keyDown(activeReviewerTab, { key: "Home" });
		expect(onSelectChat).toHaveBeenCalledOnce();
		expect(screen.getByRole("tab", { name: /^Reviewer chat/ })).toHaveFocus();

		onSelectChat.mockClear();
		activeReviewerTab.focus();
		fireEvent.keyDown(activeReviewerTab, { key: "ArrowLeft" });
		expect(onSelectChat).toHaveBeenCalledOnce();

		onSelectChat.mockClear();
		expect(previousTabListeners.size).toBe(1);
		act(() => [...previousTabListeners][0]?.());
		expect(onSelectChat).toHaveBeenCalledOnce();
	});
});

describe("promptSpacerHeight", () => {
	it("leaves room below a short prompt and collapses once the reply fills the viewport", () => {
		expect(
			promptSpacerHeight({
				viewportHeight: 800,
				contentHeightWithoutSpacer: 120,
				anchorOffset: 40,
				topInset: promptTopInset(800),
			}),
		).toBe(560);

		expect(
			promptSpacerHeight({
				viewportHeight: 800,
				contentHeightWithoutSpacer: 1200,
				anchorOffset: 40,
				topInset: promptTopInset(800),
			}),
		).toBe(0);
	});

	it("keeps a prior-chat band above the latest prompt instead of pinning flush to the top", () => {
		expect(promptTopInset(800)).toBe(160);
		expect(promptTopInset(300)).toBe(80);
	});
});

describe("ChatWorkspace shell tabs", () => {
	const shells = [
		{
			handleId: "shell-1",
			sessionId: chatFixture.sessionId,
			title: "chat worktree shell",
			workingDir: "/p",
			createdAt: "2026-08-04T00:00:00Z",
		},
		{
			handleId: "shell-2",
			sessionId: chatFixture.sessionId,
			title: "second shell",
			workingDir: "/p",
			createdAt: "2026-08-04T00:10:00Z",
		},
	];
	const shellTarget = (handleId: string) => {
		const shell = shells.find((candidate) => candidate.handleId === handleId)!;
		return {
			kind: "shell" as const,
			generation: shell.createdAt,
			handleId,
			sessionId: chatFixture.sessionId,
			title: shell.title,
		};
	};

	// Shells are tabs inside the chat surface (#4033): opening one must not cost
	// the conversation — the conversation panel hides instead of unmounting,
	// exactly like the reviewer pane.
	it("renders a selected shell as the tab body and hides, not unmounts, the conversation", () => {
		render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				session={chatSession}
				shellTerminals={shells}
				shellTarget={shellTarget("shell-1")}
				onSelectChat={vi.fn()}
			/>,
		);
		expect(screen.getByTestId("chat-shell-terminal")).toBeInTheDocument();
		expect(screen.getByTestId("chat-shell-terminal")).not.toHaveClass("pl-2");
		expect(screen.getByTestId("chat-conversation-panel")).toHaveAttribute("hidden");
		expect(screen.getByTestId("chat-conversation-panel")).toHaveAttribute("inert");

		expect(screen.getByRole("tab", { name: "chat worktree shell" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(screen.getByRole("tab", { name: "second shell" })).toHaveAttribute(
			"aria-selected",
			"false",
		);
	});

	it("passes the routed shell target straight to the terminal pane", () => {
		render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				session={chatSession}
				shellTerminals={shells}
				shellTarget={shellTarget("shell-2")}
			/>,
		);
		expect(terminalPaneState.props).toMatchObject({
			focusRequested: true,
			terminalTarget: shellTarget("shell-2"),
		});
	});

	it("cycles chat → reviewer → shells → chat on the desktop next-tab shortcut", () => {
		const onOpenReviewerTerminal = vi.fn();
		const onSelectShellTerminal = vi.fn();
		const onSelectChat = vi.fn();
		const common = {
			snapshot: idleSnapshot(),
			session: chatSession,
			shellTerminals: shells,
			onSelectShellTerminal,
			onSelectChat,
		};
		const view = render(
			<ChatWorkspace
				{...common}
				reviewerTerminal={{ handleId: "review-1", harness: "codex" }}
				onOpenReviewerTerminal={onOpenReviewerTerminal}
			/>,
		);

		// From chat: the reviewer comes first.
		act(() => [...nextTabListeners][0]?.());
		expect(onOpenReviewerTerminal).toHaveBeenCalledOnce();

		// From the reviewer: the first shell.
		view.rerender(
			<ChatWorkspace
				{...common}
				reviewerTerminal={{ handleId: "review-1", harness: "codex" }}
				onOpenReviewerTerminal={onOpenReviewerTerminal}
				reviewerTarget={{
					kind: "reviewer",
					handleId: "review-1",
					harness: "codex",
					sessionId: chatFixture.sessionId,
				}}
			/>,
		);
		act(() => [...nextTabListeners][0]?.());
		expect(onSelectShellTerminal).toHaveBeenCalledWith("shell-1");

		// From the last shell: wraps to chat.
		view.rerender(<ChatWorkspace {...common} shellTarget={shellTarget("shell-2")} />);
		act(() => [...nextTabListeners][0]?.());
		expect(onSelectChat).toHaveBeenCalledOnce();
	});

	it("cycles from the focused worker tab on Ctrl+Tab", () => {
		const onSelectShellTerminal = vi.fn();
		render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				session={chatSession}
				shellTerminals={shells}
				onSelectShellTerminal={onSelectShellTerminal}
				onSelectChat={vi.fn()}
			/>,
		);

		const workerTab = screen.getByRole("tab", { name: /^Reviewer chat/ });
		workerTab.focus();
		fireEvent.keyDown(workerTab, { key: "Tab", ctrlKey: true });

		expect(onSelectShellTerminal).toHaveBeenCalledWith("shell-1");
	});

	it("cycles shell → reviewer → chat on the desktop previous-tab shortcut", () => {
		const onOpenReviewerTerminal = vi.fn();
		const onSelectShellTerminal = vi.fn();
		const onSelectChat = vi.fn();
		const common = {
			snapshot: idleSnapshot(),
			session: chatSession,
			reviewerTerminal: { handleId: "review-1", harness: "codex" },
			onOpenReviewerTerminal,
			shellTerminals: shells,
			onSelectShellTerminal,
			onSelectChat,
		};
		const view = render(<ChatWorkspace {...common} shellTarget={shellTarget("shell-1")} />);

		act(() => [...previousTabListeners][0]?.());
		expect(onOpenReviewerTerminal).toHaveBeenCalledWith({
			handleId: "review-1",
			harness: "codex",
		});

		view.rerender(
			<ChatWorkspace
				{...common}
				reviewerTarget={{
					kind: "reviewer",
					handleId: "review-1",
					harness: "codex",
					sessionId: chatFixture.sessionId,
				}}
			/>,
		);
		act(() => [...previousTabListeners][0]?.());
		expect(onSelectChat).toHaveBeenCalledOnce();
	});

	it("enables the close-shell shortcut and closes the active shell tab", () => {
		const onCloseShellTerminal = vi.fn();
		const view = render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				session={chatSession}
				shellTerminals={shells}
				shellTarget={shellTarget("shell-2")}
				onCloseShellTerminal={onCloseShellTerminal}
			/>,
		);

		expect(closeShellTerminalShortcutStates.at(-1)).toBe(true);
		act(() => [...closeShellTerminalListeners][0]?.());
		expect(onCloseShellTerminal).toHaveBeenCalledWith("shell-2");

		view.rerender(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				session={chatSession}
				shellTerminals={shells}
				onCloseShellTerminal={onCloseShellTerminal}
			/>,
		);
		expect(closeShellTerminalShortcutStates.at(-1)).toBe(false);
	});

	it("enables the close shortcut and closes the active workspace file tab", () => {
		const onClose = vi.fn();
		render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				workspaceActiveTabKey="file:README.md"
				workspaceTabs={[
					{
						key: "file:README.md",
						content: <button role="tab">README.md</button>,
						onSelect: vi.fn(),
						onClose,
					},
				]}
			/>,
		);

		expect(closeShellTerminalShortcutStates.at(-1)).toBe(true);
		act(() => [...closeShellTerminalListeners][0]?.());
		expect(onClose).toHaveBeenCalledOnce();
	});
});
