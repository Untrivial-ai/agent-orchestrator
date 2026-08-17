import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChatWorkspace } from "./ChatWorkspace";
import { HumanMessage, OriginMessage } from "./ChatTimelineItems";
import {
	chatFixture,
	chatFixtureEmpty,
	chatFixtureLongHistory,
	chatFixtureMcpFailed,
	chatFixtureSettled,
	chatFixtureThreadError,
} from "../../lib/chat-fixture";
import type { ConversationMessage, ConversationSnapshot } from "../../types/conversation";
import { setApiBaseUrl } from "../../lib/api-client";
import { useUiStore } from "../../stores/ui-store";
import type { WorkspaceSession } from "../../types/workspace";

const writeText = vi.fn(async (_text: string) => undefined);
const menuAction = vi.fn(async (_action: string) => undefined);
const previousTabListeners = new Set<() => void>();
const nextTabListeners = new Set<() => void>();
const closeShellTerminalListeners = new Set<() => void>();
const closeShellTerminalShortcutStates: boolean[] = [];
type TerminalPaneTestProps = {
	fontSize?: number;
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
		turns: snapshot.turns.map((turn) =>
			turn.state === "running"
				? { ...turn, state: "completed" as const, completedAt: turn.requestedAt }
				: turn,
		),
	};
}

/** jsdom has no layout, so the scroller's geometry has to be stated. */
function stubGeometry(node: HTMLElement, { scrollHeight, clientHeight, scrollTop }: {
	scrollHeight: number;
	clientHeight: number;
	scrollTop: number;
}) {
	Object.defineProperty(node, "scrollHeight", { configurable: true, value: scrollHeight });
	Object.defineProperty(node, "clientHeight", { configurable: true, value: clientHeight });
	Object.defineProperty(node, "scrollTop", { configurable: true, writable: true, value: scrollTop });
}

beforeEach(() => {
	writeText.mockClear();
	menuAction.mockClear();
	previousTabListeners.clear();
	nextTabListeners.clear();
	closeShellTerminalListeners.clear();
	closeShellTerminalShortcutStates.length = 0;
	terminalPaneState.props = undefined;
	window.localStorage.clear();
	setApiBaseUrl("http://127.0.0.1:3001");
	useUiStore.setState({ isSidebarOpen: true });
});

afterEach(() => setApiBaseUrl(null));

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
		expect(screen.queryByText(/Attached (?:files|images) \(read these files/)).not.toBeInTheDocument();
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

		expect(container.querySelector(".cursor-chat-human-message > p")?.textContent).toBe(authoredBody);
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

describe("ChatWorkspace timeline", () => {
	it("uses the shared session topbar chrome for workers and orchestrators", () => {
		const view = render(<ChatWorkspace snapshot={chatFixture} sessionRole="worker" />);

		expect(screen.getByLabelText("Chat")).toHaveAttribute("data-session-role", "worker");
		expect(screen.getByTestId("session-workspace-topbar")).toBeInTheDocument();
		expect(screen.getByTestId("session-terminal-region")).toBeInTheDocument();
		expect(screen.getByRole("tab", { name: chatFixture.title })).toBeInTheDocument();

		view.rerender(<ChatWorkspace snapshot={chatFixture} sessionRole="orchestrator" />);

		expect(screen.getByLabelText("Chat")).toHaveAttribute("data-session-role", "orchestrator");
		expect(screen.getByTestId("session-workspace-topbar")).toBeInTheDocument();
		expect(screen.getByTestId("session-action-region")).toBeInTheDocument();
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

	it("leaves new-terminal and display controls out of the chat strip, like the terminal session", () => {
		render(<ChatWorkspace snapshot={chatFixture} onOpenShell={vi.fn()} />);

		const terminalRegion = screen.getByTestId("session-terminal-region");
		expect(terminalRegion).toContainElement(screen.getByRole("tablist", { name: "Chat tabs" }));
		expect(terminalRegion).not.toContainElement(screen.queryByRole("button", { name: "New terminal" }));
		expect(screen.queryByRole("toolbar", { name: "Chat display controls" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Decrease font size" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Fullscreen" })).not.toBeInTheDocument();
	});

	it("starts chat text at 12px without a topbar font control", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		const chat = screen.getByLabelText("Chat");

		expect(chat.style.getPropertyValue("--chat-font-size")).toBe("12px");
	});

	it("keeps the composer aligned to the readable conversation width", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		const composer = screen.getByLabelText("Message the agent").closest("form");
		expect(composer?.parentElement).toHaveClass("mx-auto", "w-full", "max-w-3xl");
	});

	it("shows live working state inline with the current turn while the composer owns the stop action", async () => {
		const user = userEvent.setup();
		const onInterrupt = vi.fn();
		const snapshot = structuredClone(chatFixture);
		snapshot.items = snapshot.items.filter(
			(item) => !(item.kind === "activity" && item.activityKind === "approval" && item.status === "pending"),
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

	it("shows blocked turn state inline with the current turn", () => {
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
			decisions: [{ id: "accept", label: "Approve" }],
			detail: { command: "npm test" },
			createdAt: "2026-08-08T00:00:00Z",
		});

		render(<ChatWorkspace snapshot={snapshot} onInterrupt={vi.fn()} />);

		expect(screen.getByRole("alert")).toHaveTextContent("The agent is waiting for your decision.");
		expect(screen.getByText("Waiting for your decision")).toBeInTheDocument();
		expect(screen.queryByText(/^Working for /)).not.toBeInTheDocument();
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

	it("offers real recovery actions when the controller stops", async () => {
		const user = userEvent.setup();
		const resume = vi.fn();
		const openShell = vi.fn();
		render(
			<ChatWorkspace
				snapshot={{ ...chatFixtureSettled, controller: { state: "stopped" } }}
				onResumeAgent={resume}
				onOpenShell={openShell}
			/>,
		);

		expect(screen.getByRole("alert")).toHaveTextContent("The agent controller stopped");
		await user.click(screen.getByRole("button", { name: "Resume agent" }));
		await user.click(screen.getByRole("button", { name: "Open shell" }));
		expect(resume).toHaveBeenCalledOnce();
		expect(openShell).toHaveBeenCalledOnce();
	});

	it("does not report the intentional controller gap during an interface handoff as a crash", () => {
		render(
			<ChatWorkspace
				snapshot={{ ...chatFixtureSettled, controller: { state: "stopped" } }}
				controllerTransitioning
				onResumeAgent={vi.fn()}
				onOpenShell={vi.fn()}
			/>,
		);

		expect(screen.queryByText("The agent controller stopped")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Resume agent" })).not.toBeInTheDocument();
	});

	it("announces thread and tool-server failures", () => {
		const { rerender } = render(<ChatWorkspace snapshot={chatFixtureThreadError} />);
		expect(screen.getByRole("alert")).toHaveTextContent("thread hit an internal error");

		rerender(<ChatWorkspace snapshot={chatFixtureMcpFailed} />);
		expect(screen.getByRole("status")).toHaveTextContent(/tool servers? did not start/);
	});

	it("provides an interactive conversation minimap", () => {
		render(<ChatWorkspace snapshot={chatFixtureLongHistory(8)} />);
		const log = screen.getByRole("log");
		const scrollbar = screen.getByRole("scrollbar", { name: "Conversation scrollbar" });
		stubGeometry(log, { scrollHeight: 4000, clientHeight: 800, scrollTop: 1000 });
		stubGeometry(scrollbar, { scrollHeight: 800, clientHeight: 800, scrollTop: 0 });

		fireEvent.scroll(log);
		expect(scrollbar).toHaveAttribute("aria-valuenow", "31");
		const markers = Array.from(
			scrollbar.querySelectorAll<HTMLElement>("[data-chat-scroll-marker]"),
		);
		expect(markers.length).toBeGreaterThan(1);
		expect(Number.parseFloat(markers[1]!.style.top) - Number.parseFloat(markers[0]!.style.top)).toBeLessThanOrEqual(
			18,
		);

		fireEvent.wheel(scrollbar, { deltaY: 200 });
		expect(log.scrollTop).toBe(1200);
		expect(scrollbar).toHaveAttribute("aria-valuenow", "38");

		fireEvent.keyDown(scrollbar, { key: "End" });
		expect(log.scrollTop).toBe(3200);
		expect(scrollbar).toHaveAttribute("aria-valuenow", "100");
	});

	it("previews the request and response for a hovered conversation marker", () => {
		render(<ChatWorkspace snapshot={chatFixtureLongHistory(4)} />);
		const log = screen.getByRole("log");
		const scrollbar = screen.getByRole("scrollbar", { name: "Conversation scrollbar" });
		stubGeometry(log, { scrollHeight: 2400, clientHeight: 600, scrollTop: 0 });
		stubGeometry(scrollbar, { scrollHeight: 600, clientHeight: 600, scrollTop: 0 });
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
		render(<ChatWorkspace snapshot={snapshot} />);
		const log = screen.getByRole("log");
		const scrollbar = screen.getByRole("scrollbar", { name: "Conversation scrollbar" });
		stubGeometry(log, { scrollHeight: 1800, clientHeight: 600, scrollTop: 0 });
		stubGeometry(scrollbar, { scrollHeight: 600, clientHeight: 600, scrollTop: 0 });
		fireEvent.scroll(log);

		const markers = Array.from(
			scrollbar.querySelectorAll<HTMLElement>("[data-chat-scroll-marker]"),
		);
		expect(markers).toHaveLength(2);
		fireEvent.pointerEnter(markers[1]!);
		expect(screen.getByRole("tooltip")).not.toHaveTextContent("Automatic compaction completed");
	});

	it("explains itself instead of showing an empty scroller", () => {
		render(<ChatWorkspace snapshot={chatFixtureEmpty} />);
		expect(screen.getByText("Start the conversation")).toBeInTheDocument();
		expect(screen.queryByRole("log")).not.toBeInTheDocument();
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

		stubGeometry(log, { scrollHeight: 4000, clientHeight: 800, scrollTop: 100 });
		log.dispatchEvent(new Event("scroll"));
		const jump = await screen.findByRole("button", { name: /jump to latest/i });
		expect(jump).toHaveAttribute("title", "Jump to latest");
		expect(jump).not.toHaveTextContent("Jump to latest");
		expect(jump).toHaveClass(
			"bg-raised",
			"dark:bg-raised",
			"hover:bg-surface",
			"dark:hover:bg-surface",
		);
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

		stubGeometry(log, { scrollHeight: 4000, clientHeight: 800, scrollTop: 1200 });
		log.dispatchEvent(new Event("scroll"));
		await screen.findByRole("button", { name: /jump to latest/i });

		rerender(<ChatWorkspace snapshot={{ ...poll(snapshot), latestSequence: 999 }} />);
		expect(log.scrollTop).toBe(1200);
	});

	it("follows new output while the reader is already at the bottom", () => {
		const snapshot = chatFixtureLongHistory(8);
		const { rerender } = render(<ChatWorkspace snapshot={snapshot} />);
		const log = screen.getByRole("log");
		stubGeometry(log, { scrollHeight: 4000, clientHeight: 800, scrollTop: 0 });

		rerender(<ChatWorkspace snapshot={{ ...poll(snapshot), latestSequence: 999 }} />);
		expect(log.scrollTop).toBe(4000);
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
		await user.type(composer, "unsent composer draft");

		await user.click(screen.getAllByRole("button", { name: "Edit user message" })[0]!);
		expect(composer).toHaveValue("unsent composer draft");

		const editor = screen.getByRole("textbox", { name: "Edit message" });
		expect(editor).toHaveFocus();
		expect(editor).toHaveValue("Check the worktree state and tell me what changed since the base commit.");

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
		expect(composer).toHaveValue("unsent composer draft");
	});

	it("keeps edit available while another turn is active", async () => {
		const user = userEvent.setup();
		render(
			<ChatWorkspace
				snapshot={chatFixture}
				onEditMessage={vi.fn(async () => undefined)}
			/>,
		);

		const editButtons = screen.getAllByRole("button", { name: "Edit user message" });
		expect(editButtons.length).toBeGreaterThan(0);

		await user.click(editButtons[0]!);
		expect(screen.getByRole("textbox", { name: "Edit message" })).toHaveValue(
			"Check the worktree state and tell me what changed since the base commit.",
		);
		expect(screen.getByRole("button", { name: "Send edited message" })).toBeDisabled();
		expect(screen.getByText("Stop the current turn before branching")).toBeVisible();
	});

	it("retains the inline draft when branch creation fails", async () => {
		const user = userEvent.setup();
		const onEditMessage = vi.fn(async () => {
			throw new Error("branch failed");
		});
		const view = render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				onEditMessage={onEditMessage}
				editMessageError="branch failed"
			/>,
		);

		await user.click(screen.getAllByRole("button", { name: "Edit user message" })[0]!);
		const editor = screen.getByRole("textbox", { name: "Edit message" });
		await user.clear(editor);
		await user.type(editor, "keep this draft");
		fireEvent.keyDown(editor, { key: "Enter", ctrlKey: true });

		await waitFor(() => expect(onEditMessage).toHaveBeenCalledWith("turn-1", "keep this draft"));
		expect(screen.getByRole("textbox", { name: "Edit message" })).toHaveValue("keep this draft");
		expect(screen.getByRole("alert")).toHaveTextContent("branch failed");

		view.rerender(
			<ChatWorkspace
				snapshot={{
					...idleSnapshot(),
					activeBranchId: "branch-failed",
					branchedFromEarlierMessage: true,
					items: [],
					turns: [],
				}}
				onEditMessage={onEditMessage}
				editMessageError="branch failed"
			/>,
		);
		expect(screen.getByRole("textbox", { name: "Edit message" })).toHaveValue("keep this draft");
		expect(screen.getByRole("alert")).toHaveTextContent("branch failed");
	});

	it("navigates prompt branches and explains that files are unchanged", async () => {
		const user = userEvent.setup();
		const onActivateBranch = vi.fn(async () => undefined);
		const snapshot = {
			...idleSnapshot(),
			activeBranchId: "branch-current",
			branchedFromEarlierMessage: true,
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
		await user.click(screen.getByRole("button", { name: "Previous conversation branch" }));
		expect(onActivateBranch).toHaveBeenCalledWith("branch-previous");
		expect(
			screen.getByText("Conversation branched; worktree files were left unchanged."),
		).toBeVisible();
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

		const copies = screen.getAllByRole("button", { name: /copy message as markdown/i });
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
		// reader means by "copy this".
		const streaming = screen.getByLabelText("still writing").closest("div");
		expect(streaming?.querySelector('[aria-label*="Copy message"]')).toBeNull();
	});

	it("does not leave a writing caret behind prose followed by tool activity", () => {
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
		const composer = screen.getByRole("combobox", { name: "Message the agent" });
		await user.type(composer, "unsent reviewer-switch draft");
		fireEvent.paste(composer, {
			clipboardData: {
				files: [new File([new Uint8Array([137, 80, 78, 71])], "review.png", { type: "image/png" })],
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
		stubGeometry(timeline, { scrollHeight: 2_000, clientHeight: 500, scrollTop: 417 });

		view.rerender(<ChatWorkspace {...common} reviewerTarget={reviewerTarget} />);

		expect(composer).toBeInTheDocument();
		expect(attachment).toBeInTheDocument();
		expect(editor).toBeInTheDocument();
		expect(timeline).toBeInTheDocument();
		expect(screen.getByTestId("chat-conversation-panel")).toHaveAttribute("hidden");
		expect(screen.getByTestId("chat-conversation-panel")).toHaveAttribute("inert");

		view.rerender(<ChatWorkspace {...common} />);

		expect(screen.getByRole("combobox", { name: "Message the agent" })).toBe(composer);
		expect(composer).toHaveValue("unsent reviewer-switch draft");
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

		expect(terminalPaneState.props).toMatchObject({ fontSize: 14, isFullscreen: false });
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
		Object.defineProperty(surface, "requestFullscreen", { configurable: true, value: requestFullscreen });
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
		const chatTab = screen.getByRole("tab", { name: chatFixture.sessionId });
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
		expect(screen.getByRole("tab", { name: chatFixture.sessionId })).toHaveAttribute("tabindex", "-1");
		expect(activeReviewerTab).toHaveAttribute("tabindex", "0");

		activeReviewerTab.focus();
		fireEvent.keyDown(activeReviewerTab, { key: "Home" });
		expect(onSelectChat).toHaveBeenCalledOnce();
		expect(screen.getByRole("tab", { name: chatFixture.sessionId })).toHaveFocus();

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
		expect(terminalPaneState.props).toMatchObject({ terminalTarget: shellTarget("shell-2") });
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

		const workerTab = screen.getByRole("tab", { name: "ao-14" });
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
		expect(onOpenReviewerTerminal).toHaveBeenCalledWith({ handleId: "review-1", harness: "codex" });

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
});
