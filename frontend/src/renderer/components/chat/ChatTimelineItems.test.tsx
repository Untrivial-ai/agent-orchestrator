import { act, render as rtlRender, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { StrictMode, type ReactElement } from "react";
import { ActivityRow, AssistantMessage, HumanMessage, TurnOutcome } from "./ChatTimelineItems";
import type { ConversationMessage } from "../../types/conversation";
import { TooltipProvider } from "../ui/tooltip";

function render(ui: ReactElement) {
	const result = rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
	return {
		...result,
		rerender: (nextUi: ReactElement) => result.rerender(<TooltipProvider>{nextUi}</TooltipProvider>),
	};
}

function message(overrides: Partial<ConversationMessage> = {}): ConversationMessage {
	return {
		kind: "message",
		id: "assistant-1",
		sequence: 1,
		revision: 1,
		role: "assistant",
		origin: "provider",
		text: "a",
		streaming: true,
		createdAt: "2026-08-24T00:00:00Z",
		...overrides,
	};
}

function advance(ms: number) {
	act(() => {
		vi.advanceTimersByTime(ms);
	});
}

afterEach(() => vi.restoreAllMocks());

describe("TurnOutcome", () => {
	it("keeps a recovered historical turn distinct from success", () => {
		render(<TurnOutcome state="recovered" />);
		expect(screen.getByText("This turn was recovered from an earlier session")).toBeInTheDocument();
		expect(screen.queryByText("Done")).not.toBeInTheDocument();
	});

	it("shows the failed outcome and the provider's explanation", () => {
		render(<TurnOutcome state="failed" error="Provider error" />);

		expect(screen.getByText("The agent ran into a problem")).toBeInTheDocument();
		expect(screen.getByText("Provider error")).toBeInTheDocument();
	});

	it("preserves multiline provider text and links without interpreting its structure", () => {
		render(
			<TurnOutcome
				state="failed"
				error={"Usage limit reached\n\nManage billing at https://example.com/billing."}
			/>,
		);

		expect(screen.getByText(/Usage limit reached/)).toBeInTheDocument();
		expect(screen.getByText(/Manage billing at/)).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "https://example.com/billing" })).toHaveAttribute(
			"href",
			"https://example.com/billing",
		);
	});
});

function paragraph(): string {
	return document.querySelector("p")?.textContent ?? "";
}

describe("AssistantMessage streaming", () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	it("shows the first durable snapshot and a replacement message immediately", () => {
		const text = "A first snapshot 👨‍👩‍👧‍👦";
		const view = render(<AssistantMessage message={message({ text })} />);
		expect(paragraph()).toBe(text);

		view.rerender(<AssistantMessage message={message({ text: `${text} buffered ` })} />);
		advance(0);
		view.rerender(<AssistantMessage message={message({ id: "assistant-2", text: "New message" })} />);
		expect(paragraph()).toBe("New message");
		advance(10);
		expect(paragraph()).toBe("New message");
	});

	it("shows a provider correction immediately and drips the words after it", () => {
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "a one two " })} />);
		advance(0);
		expect(paragraph()).toBe("a one");

		view.rerender(<AssistantMessage message={message({ text: "Corrected" })} />);
		expect(paragraph()).toBe("Corrected");

		view.rerender(<AssistantMessage message={message({ text: "Corrected later " })} />);
		advance(0);
		expect(paragraph()).toBe("Corrected later");
	});

	it("releases one word every 10ms and holds an unfinished word", () => {
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "a one two three" })} />);

		advance(0);
		expect(paragraph()).toBe("a one");
		advance(10);
		expect(paragraph()).toBe("a one two");
		advance(50);
		expect(paragraph()).toBe("a one two");

		view.rerender(<AssistantMessage message={message({ text: "a one two three ", streaming: false })} />);
		expect(paragraph()).toBe("a one two three");
	});

	it("keeps a large burst word by word instead of dumping it", () => {
		const view = render(<AssistantMessage message={message()} />);
		const text = `a ${Array.from({ length: 40 }, (_, index) => `w${index}`).join(" ")} `;
		view.rerender(<AssistantMessage message={message({ text })} />);

		advance(0);
		advance(200);

		const shown = paragraph();
		expect(shown.startsWith("a w0 ")).toBe(true);
		expect(shown).not.toBe(text);
		expect(shown.split(/\s+/).filter(Boolean)).toHaveLength(22);
	});

	it("does not restart the gap when another burst arrives", () => {
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "a one two " })} />);
		advance(0);
		expect(paragraph()).toBe("a one");

		view.rerender(<AssistantMessage message={message({ text: "a one two three " })} />);
		advance(10);
		expect(paragraph()).toBe("a one two");
	});

	it("keeps an emoji and a combining mark inside the word they belong to", () => {
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "a hello 👨‍👩‍👧‍👦 cafe\u0301 " })} />);

		advance(0);
		expect(paragraph()).toBe("a hello");
		advance(10);
		expect(paragraph()).toBe("a hello 👨‍👩‍👧‍👦");
		advance(10);
		expect(paragraph()).toBe("a hello 👨‍👩‍👧‍👦 cafe\u0301");
	});

	it("shows the latest snapshot immediately when reduced motion is requested", () => {
		vi.spyOn(window, "matchMedia").mockImplementation((query) => ({
			matches: query === "(prefers-reduced-motion: reduce)",
			media: query,
			onchange: null,
			addEventListener: vi.fn(),
			removeEventListener: vi.fn(),
			addListener: vi.fn(),
			removeListener: vi.fn(),
			dispatchEvent: vi.fn(() => false),
		}));
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "The complete snapshot" })} />);

		expect(screen.getByText("The complete snapshot")).toBeInTheDocument();
	});

	it("flushes buffered text when streaming completes and restores actions", () => {
		const view = render(<AssistantMessage message={message()} showCopy />);
		view.rerender(<AssistantMessage message={message({ text: "aThe complete answer", streaming: true })} showCopy />);
		view.rerender(
			<AssistantMessage message={message({ text: "aThe complete answer", streaming: false })} showCopy />,
		);

		expect(screen.getByText("aThe complete answer")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Copy message as markdown" })).toBeInTheDocument();
	});

	it("hides message actions while text is still buffered", () => {
		const view = render(<AssistantMessage message={message()} showCopy />);
		view.rerender(<AssistantMessage message={message({ text: "a buffered answer" })} showCopy />);

		expect(screen.queryByRole("button", { name: "Copy message as markdown" })).not.toBeInTheDocument();
	});

	it("shows the message timestamp on hover", () => {
		render(
			<AssistantMessage
				message={message({ createdAt: new Date().toISOString(), text: "Timestamped answer", streaming: false })}
				showCopy
			/>,
		);

		expect(screen.getByLabelText(/^Sent \d{2}:\d{2}$/)).toBeInTheDocument();
	});

	it("labels yesterday and older messages by date", () => {
		const now = new Date();
		const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 12).toISOString();
		const older = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 3, 12).toISOString();
		const view = render(
			<AssistantMessage message={message({ createdAt: yesterday, streaming: false })} showCopy />,
		);

		expect(screen.getByLabelText(/^Sent Yesterday · \d{2}:\d{2}$/)).toBeInTheDocument();
		view.rerender(<AssistantMessage message={message({ createdAt: older, streaming: false })} showCopy />);
		expect(screen.queryByLabelText(/^Sent Yesterday ·/)).toBeNull();
		expect(screen.getByLabelText(/^Sent .+\d{4}$/)).toBeInTheDocument();
	});

	it("survives StrictMode effect cleanup and keeps releasing words", () => {
		const view = render(
			<StrictMode>
				<AssistantMessage message={message()} />
			</StrictMode>,
		);
		view.rerender(
			<StrictMode>
				<AssistantMessage message={message({ text: "a one two " })} />
			</StrictMode>,
		);
		advance(0);
		expect(paragraph()).toBe("a one");
		advance(10);
		expect(paragraph()).toBe("a one two");
	});
});

describe("ActivityRow", () => {
	it("renders inline code in provider activity titles without literal backticks", () => {
		const path = "/Users/sachin/.ao/worktrees/murdock/docs/system-design.html";
		const { container } = render(
			<ActivityRow activity={{
				kind: "activity", id: "edit-title", sequence: 1, revision: 0,
				createdAt: "2026-08-23T00:00:00Z", activityKind: "file_change",
				status: "completed", summary: `Edit \`${path}\``,
			}} />,
		);
		expect(screen.getByRole("button")).toHaveTextContent(`Edit ${path}`);
		expect(container.querySelector("code")).toHaveTextContent(path);
		expect(screen.getByRole("button").textContent).not.toContain("`");
	});

	it("does not present a recovered historical activity as failed", () => {
		render(
			<ActivityRow
				activity={{
					kind: "activity",
					id: "activity-1",
					sequence: 1,
					revision: 0,
					createdAt: "2026-08-23T00:00:00Z",
					activityKind: "command",
					status: "recovered",
					summary: "Run tests",
				}}
			/>,
		);
		expect(screen.getByText("outcome unknown")).toBeInTheDocument();
		expect(screen.queryByText("failed")).not.toBeInTheDocument();
	});
});

describe("HumanMessage", () => {
	function human(overrides: Partial<ConversationMessage> = {}): ConversationMessage {
		return message({ id: "human-1", role: "user", origin: "human", text: "hi", streaming: false, ...overrides });
	}

	// Light theme cannot separate a sent bubble from the canvas by fill alone, so it
	// paints an enclosure keyed off this attribute's absence. Losing it makes the
	// message read as unenclosed text again.
	it("marks a queued bubble and leaves a sent one unmarked", () => {
		const { container, rerender } = render(<HumanMessage message={human()} sessionId="s1" />);
		const sent = container.querySelector(".cursor-chat-human-message");
		expect(sent).not.toBeNull();
		expect(sent).not.toHaveAttribute("data-queued");

		rerender(<HumanMessage message={human()} sessionId="s1" queued />);
		expect(container.querySelector(".cursor-chat-human-message")).toHaveAttribute("data-queued");
	});
});
