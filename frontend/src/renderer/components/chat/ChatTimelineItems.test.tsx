import { act, render as rtlRender, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { StrictMode, type ReactElement } from "react";
import { ActivityRow, AssistantMessage, TurnOutcome } from "./ChatTimelineItems";
import type { ConversationMessage } from "../../types/conversation";
import { TooltipProvider } from "../ui/tooltip";

function render(ui: ReactElement) {
	const result = rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
	return {
		...result,
		rerender: (nextUi: ReactElement) => result.rerender(<TooltipProvider>{nextUi}</TooltipProvider>),
	};
}

let nextFrame = 1;
let frameTime = 0;
let frames = new Map<number, FrameRequestCallback>();

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

function runFrame(now: number) {
	const [id, callback] = frames.entries().next().value ?? [];
	if (id === undefined || callback === undefined) throw new Error("No animation frame scheduled");
	frames.delete(id);
	frameTime = now;
	act(() => callback(now));
}

beforeEach(() => {
	nextFrame = 1;
	frameTime = 0;
	frames = new Map();
	vi.spyOn(performance, "now").mockImplementation(() => frameTime);
	vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
		const id = nextFrame++;
		frames.set(id, callback);
		return id;
	});
	vi.spyOn(window, "cancelAnimationFrame").mockImplementation((id) => {
		frames.delete(id);
	});
});

afterEach(() => vi.restoreAllMocks());

describe("TurnOutcome", () => {
	it("keeps a recovered historical turn distinct from success", () => {
		render(<TurnOutcome state="recovered" />);
		expect(screen.getByText("This turn was recovered from an earlier session")).toBeInTheDocument();
		expect(screen.queryByText("Done")).not.toBeInTheDocument();
	});

	it("shows the message above a full-width rule", () => {
		const { container } = render(<TurnOutcome state="failed" error="Provider error" />);

		expect(screen.getByText("The agent ran into a problem")).toBeInTheDocument();
		expect(screen.getByText("Provider error")).toBeInTheDocument();
		expect(container.querySelector(".h-px.w-full.bg-border")).toBeInTheDocument();
	});
});

describe("AssistantMessage streaming", () => {
	it("shows the first durable snapshot and a replacement message immediately", () => {
		const text = "A first snapshot 👨‍👩‍👧‍👦";
		const view = render(<AssistantMessage message={message({ text })} />);
		expect(document.querySelector("p")?.textContent).toBe(text);
		expect(frames.size).toBe(0);

		view.rerender(<AssistantMessage message={message({ text: text + " buffered" })} />);
		runFrame(0);
		view.rerender(<AssistantMessage message={message({ id: "assistant-2", text: "New message" })} />);
		expect(document.querySelector("p")?.textContent).toBe("New message");
		expect(frames.size).toBe(0);
	});

	it("shows a provider correction immediately and starts a fresh drain afterward", () => {
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "a".padEnd(2000, "x") })} />);
		runFrame(0);
		runFrame(150);
		view.rerender(<AssistantMessage message={message({ text: "Corrected" })} />);
		expect(document.querySelector("p")?.textContent).toBe("Corrected");
		expect(frames.size).toBe(0);

		view.rerender(<AssistantMessage message={message({ text: "Corrected text" })} />);
		runFrame(190);
		runFrame(198);
		expect(document.querySelector("p")?.textContent).toBe("Corrected");
		runFrame(390);
		expect(document.querySelector("p")?.textContent).toBe("Corrected text");
	});

	it("does not force a character on high-refresh frames", () => {
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "abcdefghij" })} />);

		runFrame(0);
		runFrame(8);

		expect(screen.getByText("a")).toBeInTheDocument();
		expect(screen.queryByText("ab")).not.toBeInTheDocument();
	});

	it("shows the current snapshot immediately when an occluded tab resumes", () => {
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "a".padEnd(2000, "x") })} />);

		runFrame(0);
		runFrame(5 * 60 * 1000);
		const rendered = document.querySelector("p");

		expect(rendered?.textContent).toBe("a".padEnd(2000, "x"));
	});

	it("flushes on resume when a hidden tab received no initial animation frame", () => {
		const view = render(<AssistantMessage message={message()} />);
		const text = "a".padEnd(2000, "x");
		view.rerender(<AssistantMessage message={message({ text })} />);

		runFrame(5 * 60 * 1000);

		expect(document.querySelector("p")?.textContent).toBe(text);
		expect(frames.size).toBe(0);
	});

	it("shows a large received burst within 250ms", () => {
		const view = render(<AssistantMessage message={message()} />);
		const text = "a".padEnd(10_000, "x");
		view.rerender(<AssistantMessage message={message({ text })} />);

		runFrame(0);
		for (let now = 16; now <= 240 && frames.size; now += 16) runFrame(now);

		expect(document.querySelector("p")?.textContent).toBe(text);
		expect(frames.size).toBe(0);
	});

	it("does not postpone the drain deadline when new snapshots keep arriving", () => {
		const view = render(<AssistantMessage message={message()} />);
		let text = "a".padEnd(2000, "x");
		view.rerender(<AssistantMessage message={message({ text })} />);
		runFrame(0);
		for (let now = 40; now <= 200; now += 40) {
			text += "x".repeat(2000);
			view.rerender(<AssistantMessage message={message({ text })} />);
			runFrame(now);
		}

		expect(document.querySelector("p")?.textContent).toBe(text);
		expect(frames.size).toBe(0);
	});

	it("segments each snapshot once and reuses it across animation frames", () => {
		const segment = vi.spyOn(Intl.Segmenter.prototype, "segment");
		const view = render(<AssistantMessage message={message()} />);
		const text = "a".padEnd(2000, "x");
		view.rerender(<AssistantMessage message={message({ text })} />);
		runFrame(0);
		runFrame(50);
		runFrame(100);

		expect(segment.mock.calls.filter(([input]) => input === text)).toHaveLength(1);
	});

	it("keeps emoji and combining sequences intact while streaming", () => {
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "a👨‍👩‍👧‍👦e\u0301" })} />);

		runFrame(0);
		runFrame(1000);

		expect(document.querySelector("p")?.textContent).toBe("a👨‍👩‍👧‍👦e\u0301");
	});

	it("reconciles a grapheme when a later snapshot adds a ZWJ", () => {
		const view = render(<AssistantMessage message={message()} />);
		view.rerender(<AssistantMessage message={message({ text: "a👨" })} />);
		runFrame(0);
		runFrame(1000);

		view.rerender(<AssistantMessage message={message({ text: "a👨‍👩" })} />);
		expect(document.querySelector("p")?.textContent).toBe("a");
		runFrame(1000);
		runFrame(1200);

		expect(document.querySelector("p")?.textContent).toBe("a👨‍👩");
	});

	it("reconciles a later combining mark without skipping the following grapheme", () => {
		const view = render(<AssistantMessage message={message({ text: "ae" })} />);
		view.rerender(<AssistantMessage message={message({ text: "ae\u0301z" })} />);
		expect(document.querySelector("p")?.textContent).toBe("a");
		runFrame(0);
		runFrame(20);
		expect(document.querySelector("p")?.textContent).toBe("ae\u0301");
		runFrame(40);
		expect(document.querySelector("p")?.textContent).toBe("ae\u0301z");
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
		expect(frames.size).toBe(0);
	});

	it("flushes buffered text when streaming completes and restores actions", () => {
		const view = render(<AssistantMessage message={message()} showCopy />);
		view.rerender(<AssistantMessage message={message({ text: "aThe complete answer", streaming: true })} showCopy />);
		runFrame(0);
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
		expect(screen.getByLabelText(/^Sent [A-Z][a-z]{2} \d{1,2}, \d{4}$/)).toBeInTheDocument();
	});

	it("survives StrictMode effect cleanup and keeps draining", () => {
		const view = render(
			<StrictMode>
				<AssistantMessage message={message()} />
			</StrictMode>,
		);
		view.rerender(
			<StrictMode>
				<AssistantMessage message={message({ text: "abcdefghij" })} />
			</StrictMode>,
		);
		runFrame(0);
		runFrame(100);

		expect(document.querySelector("p")?.textContent).toBe("abcdef");
	});
});

describe("ActivityRow", () => {
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
