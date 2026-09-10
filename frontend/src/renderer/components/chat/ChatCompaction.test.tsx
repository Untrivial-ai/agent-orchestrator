import { render as rtlRender, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { describe, expect, it, vi } from "vitest";
import { ChatWorkspace } from "./ChatWorkspace";
import { typeInLexicalEditor } from "../../test/lexical";
import { TooltipProvider } from "../ui/tooltip";

function render(ui: ReactElement) {
	return rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
}
import type {
	ConversationActivity,
	ConversationItem,
	ConversationSnapshot,
	ConversationTurn,
} from "../../types/conversation";

// Compaction is the difference between a session that works for an hour and one
// that works for a day: every turn re-sends the history, so context fills on its
// own until the conversation cannot accept another turn. These cover the two halves
// the user sees — that a compaction happened, and how to cause one.

function compaction(overrides: Partial<ConversationActivity> = {}): ConversationActivity {
	return {
		kind: "activity",
		id: "cc-1",
		sequence: 2,
		revision: 0,
		activityKind: "system",
		status: "completed",
		summary: "Compacted history, freeing 11.0k tokens",
		detail: {
			event: "compaction",
			tokensBefore: 15650,
			tokensAfter: 4632,
			tokensReclaimed: 11018,
			contextWindow: 258400,
		},
		createdAt: "2026-08-02T10:00:00Z",
		...overrides,
	};
}

function snapshot(
	items: ConversationItem[],
	turns: ConversationTurn[] = [],
	extra: Partial<ConversationSnapshot> = {},
): ConversationSnapshot {
	return {
		conversationId: "c1",
		sessionId: "p1-1",
		harness: "codex",
		mode: "chat",
		controller: { state: "ready" },
		turns,
		items,
		latestSequence: items.length,
		oldestSequence: items[0]?.sequence ?? 1,
		hasMoreBefore: false,
		settings: {},
		...extra,
	};
}

const assistantSaid: ConversationItem = {
	kind: "message",
	id: "m1",
	sequence: 1,
	revision: 0,
	role: "assistant",
	origin: "provider",
	text: "Earlier work.",
	streaming: false,
	createdAt: "2026-08-02T09:00:00Z",
};

describe("compaction in the timeline", () => {
	it("shows the message above a full-width rule", () => {
		render(<ChatWorkspace snapshot={snapshot([assistantSaid, compaction()])} />);

		expect(screen.getByText("The conversation history was compacted")).toBeInTheDocument();
		expect(screen.getByText("−11.0k · 2% full")).toBeInTheDocument();
		expect(screen.getByText("−11.0k · 2% full")).toHaveClass("text-muted-foreground/70");
	});

	// A compaction right after a daemon restart genuinely does not know what it
	// saved, because AO has seen no token report yet. Showing "0 freed" would be a
	// lie rather than a gap.
	it("claims no figures when the provider never reported any", () => {
		render(
			<ChatWorkspace
				snapshot={snapshot([
					assistantSaid,
					compaction({
						summary: "Compacted the conversation history",
						detail: { event: "compaction" },
					}),
				])}
			/>,
		);

		expect(screen.getByText("The conversation history was compacted")).toBeInTheDocument();
		expect(screen.queryByText(/0 tokens/)).not.toBeInTheDocument();
		expect(screen.queryByText(/% full/)).not.toBeInTheDocument();
	});

	it("does not render a centered label inside the rule", () => {
		const { container } = render(
			<ChatWorkspace snapshot={snapshot([assistantSaid, compaction()])} />,
		);

		expect(container.querySelector('[data-compaction="true"]')).toBeNull();
		expect(
			screen.queryByRole("button", { name: /Compacted history/ }),
		).not.toBeInTheDocument();
	});
});

describe("the compact control", () => {
	// Compaction lives on `/compact` rather than a toolbar button: the composer
	// tools stay for attach/settings, and compact is an AO slash command.
	it("is offered in the slash menu, not the message tools", async () => {
		render(<ChatWorkspace snapshot={snapshot([assistantSaid])} onCompact={vi.fn()} />);

		const tools = screen.getByRole("group", { name: "Message tools" });
		expect(
			within(tools).queryByRole("button", { name: "Compact conversation history" }),
		).not.toBeInTheDocument();

		await typeInLexicalEditor(screen.getByLabelText("Message the agent"), "/");
		expect(screen.getByRole("option", { name: /compact/i })).toBeInTheDocument();
	});

	it("offers compact alongside provider skills in the slash menu", async () => {
		render(<ChatWorkspace snapshot={snapshot([assistantSaid])} onCompact={vi.fn()} />);

		await typeInLexicalEditor(screen.getByLabelText("Message the agent"), "/");
		expect(screen.getByRole("option", { name: /compact/i })).toBeInTheDocument();
	});

	// Measured against a live app-server: thread/compact/start mid-turn silently
	// interrupts the running turn, then compacts. The command refuses locally and
	// preserves the draft rather than letting the user discover the loss afterwards.
	it("refuses while a turn is in flight and keeps the command editable", async () => {
		const user = userEvent.setup();
		const onCompact = vi.fn();
		render(
			<ChatWorkspace
				snapshot={snapshot([assistantSaid], [
					{ id: "t1", state: "running", requestedAt: "2026-08-02T10:00:00Z" },
				])}
				onCompact={onCompact}
			/>,
		);

		const field = screen.getByLabelText("Message the agent");
		await typeInLexicalEditor(field, "/compact");
		await user.keyboard("{Enter}");

		expect(onCompact).not.toHaveBeenCalled();
		expect(field).toHaveTextContent("/compact");
		await waitFor(() =>
			expect(screen.getByRole("alert")).toHaveTextContent("Stop the current turn"),
		);
	});

	it("surfaces a provider refusal only when the command is invoked", async () => {
		const user = userEvent.setup();
		render(
			<ChatWorkspace
				snapshot={snapshot([assistantSaid])}
				onCompact={vi.fn()}
				compactUnavailable="This agent cannot compact its history"
			/>,
		);

		expect(screen.queryByText("This agent cannot compact its history")).not.toBeInTheDocument();
		await typeInLexicalEditor(screen.getByLabelText("Message the agent"), "/compact");
		await user.keyboard("{Enter}");
		await waitFor(() =>
			expect(screen.getByRole("alert")).toHaveTextContent("This agent cannot compact its history"),
		);
	});

	it("does not start a second compaction while one is running", async () => {
		const user = userEvent.setup();
		const onCompact = vi.fn();
		render(
			<ChatWorkspace snapshot={snapshot([assistantSaid])} onCompact={onCompact} compacting />,
		);

		await typeInLexicalEditor(screen.getByLabelText("Message the agent"), "/compact");
		await user.keyboard("{Enter}");
		expect(onCompact).not.toHaveBeenCalled();
		await waitFor(() =>
			expect(screen.getByRole("alert")).toHaveTextContent("already being compacted"),
		);
	});
});
