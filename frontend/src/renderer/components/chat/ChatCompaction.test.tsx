import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChatWorkspace } from "./ChatWorkspace";
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
	it("marks where history was compacted, and says what it reclaimed", () => {
		render(<ChatWorkspace snapshot={snapshot([assistantSaid, compaction()])} />);

		expect(screen.getByText("History compacted")).toBeInTheDocument();
		// The reclaim, not the raw before/after: what was gained is the useful figure.
		expect(screen.getByText("−11.0k")).toBeInTheDocument();
		// How full the context is afterwards, which is what tells the user whether
		// they have room to keep going.
		expect(screen.getByText("2% full")).toBeInTheDocument();
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

		expect(screen.getByText("History compacted")).toBeInTheDocument();
		expect(screen.queryByText(/0 tokens/)).not.toBeInTheDocument();
		expect(screen.queryByText(/% full/)).not.toBeInTheDocument();
	});

	// It is a boundary in the conversation, not a step in one: everything above it is
	// no longer what the agent sees verbatim, which a collapsed tool-call run would
	// hide entirely.
	it("renders as a divider rather than a collapsible activity row", () => {
		const { container } = render(
			<ChatWorkspace snapshot={snapshot([assistantSaid, compaction()])} />,
		);

		expect(container.querySelector('[data-compaction="true"]')).not.toBeNull();
		expect(
			screen.queryByRole("button", { name: /Compacted history/ }),
		).not.toBeInTheDocument();
	});
});

describe("the compact control", () => {
	it("is offered in the composer message tools", () => {
		const onCompact = vi.fn();
		render(<ChatWorkspace snapshot={snapshot([assistantSaid])} onCompact={onCompact} />);

		const tools = screen.getByRole("group", { name: "Message tools" });
		const compactButton = within(tools).getByRole("button", {
			name: "Compact conversation history",
		});
		compactButton.click();
		expect(onCompact).toHaveBeenCalledOnce();
	});

	it("offers compact alongside provider skills in the slash menu", async () => {
		const user = userEvent.setup();
		render(<ChatWorkspace snapshot={snapshot([assistantSaid])} onCompact={vi.fn()} />);

		await user.type(screen.getByLabelText("Message the agent"), "/");
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
		await user.type(field, "/compact");
		await user.keyboard("{Enter}");

		expect(onCompact).not.toHaveBeenCalled();
		expect(field).toHaveValue("/compact");
		expect(screen.getByRole("alert")).toHaveTextContent("Stop the current turn");
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

		expect(screen.getByText("This agent cannot compact its history")).toBeInTheDocument();
		await user.type(screen.getByLabelText("Message the agent"), "/compact");
		await user.keyboard("{Enter}");
		expect(screen.getByRole("alert")).toHaveTextContent("This agent cannot compact its history");
	});

	it("does not start a second compaction while one is running", async () => {
		const user = userEvent.setup();
		const onCompact = vi.fn();
		render(
			<ChatWorkspace snapshot={snapshot([assistantSaid])} onCompact={onCompact} compacting />,
		);

		await user.type(screen.getByLabelText("Message the agent"), "/compact");
		await user.keyboard("{Enter}");
		expect(onCompact).not.toHaveBeenCalled();
		expect(screen.getByRole("alert")).toHaveTextContent("already being compacted");
	});
});
