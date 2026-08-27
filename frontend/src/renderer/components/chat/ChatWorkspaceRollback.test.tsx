/**
 * The per-turn undo control and the honesty of its confirmation.
 *
 * ChatWorkspace takes props only, so these render it directly with a fixture and a
 * spy. What is asserted is deliberately about behaviour a user would notice: the
 * control appears only when an undo can actually work, the confirmation says what is
 * lost, and the turn id that reaches the daemon is the one that was clicked.
 */

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChatWorkspace } from "./ChatWorkspace";
import { chatFixture } from "../../lib/chat-fixture";
import type { ConversationSnapshot } from "../../types/conversation";

/** A conversation with nothing in flight, which is when an undo is offered. */
function idleSnapshot(): ConversationSnapshot {
	return {
		...chatFixture,
		controller: { state: "ready" },
		turns: chatFixture.turns.map((turn) =>
			turn.state === "running"
				? { ...turn, state: "completed" as const, completedAt: turn.requestedAt }
				: turn,
		),
	};
}

describe("ChatWorkspace rollback", () => {
	it("offers an undo on each settled turn and reports the clicked turn", async () => {
		const onRollback = vi.fn();
		render(<ChatWorkspace snapshot={idleSnapshot()} onRollback={onRollback} />);

		const controls = screen.getAllByRole("button", { name: "Roll back to here" });
		expect(controls.length).toBeGreaterThan(0);

		await userEvent.click(controls[0]!);
		const dialog = screen.getByRole("dialog");
		await userEvent.click(within(dialog).getByRole("button", { name: "Roll back" }));

		// The first settled turn in the fixture is turn-1; the daemon must be given
		// AO's own turn id, which is what the snapshot exposes.
		expect(onRollback).toHaveBeenCalledWith("turn-1");
	});

	it("says the agent forgets, and that the worktree does not change", async () => {
		render(<ChatWorkspace snapshot={idleSnapshot()} onRollback={vi.fn()} />);
		await userEvent.click(screen.getAllByRole("button", { name: "Roll back to here" })[0]!);

		const dialog = screen.getByRole("dialog");
		expect(dialog.textContent).toContain("forget this exchange and everything after it");
		// The provider does not revert files, so a confirmation that implied otherwise
		// would be the one thing a user could not recover from believing.
		expect(dialog.textContent).toContain("left exactly as they are");
		expect(dialog.textContent).toContain("cannot be undone");
	});

	it("does not commit anything when the confirmation is cancelled", async () => {
		const onRollback = vi.fn();
		render(<ChatWorkspace snapshot={idleSnapshot()} onRollback={onRollback} />);

		await userEvent.click(screen.getAllByRole("button", { name: "Roll back to here" })[0]!);
		const dialog = screen.getByRole("dialog");
		await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

		expect(onRollback).not.toHaveBeenCalled();
	});

	// The daemon refuses a rollback mid-turn. A control that exists only to be
	// refused is worse than one that waits for the agent to finish.
	it("withholds the control while a turn is in flight", () => {
		render(<ChatWorkspace snapshot={chatFixture} onRollback={vi.fn()} />);
		expect(screen.queryByRole("button", { name: "Roll back to here" })).toBeNull();
	});

	// Feature detection reaches the UI as an absent callback, following how the model
	// picker already hides itself for a provider that offers no choice.
	it("draws no control when the agent cannot undo", () => {
		render(<ChatWorkspace snapshot={idleSnapshot()} />);
		expect(screen.queryByRole("button", { name: "Roll back to here" })).toBeNull();
	});

	// A turn the provider never accepted holds no history to discard, and the daemon
	// refuses it.
	it("draws no control on a turn the provider never accepted", () => {
		const snapshot = idleSnapshot();
		render(
			<ChatWorkspace
				snapshot={{
					...snapshot,
					turns: snapshot.turns.map((turn) => ({ ...turn, providerTurnId: undefined })),
				}}
				onRollback={vi.fn()}
			/>,
		);
		expect(screen.queryByRole("button", { name: "Roll back to here" })).toBeNull();
	});

	it("says how much an undo took away", () => {
		const snapshot = idleSnapshot();
		render(
			<ChatWorkspace
				snapshot={{
					...snapshot,
					turns: [
						snapshot.turns[0]!,
						{ ...snapshot.turns[1]!, rolledBack: true },
					],
				}}
				onRollback={vi.fn()}
			/>,
		);
		expect(screen.getByText(/1 turn was rolled back/)).toBeInTheDocument();
		expect(screen.getByText(/no longer remembers it/)).toBeInTheDocument();
	});

	it("surfaces a refusal inside the confirmation rather than closing on it", async () => {
		render(
			<ChatWorkspace
				snapshot={idleSnapshot()}
				onRollback={vi.fn()}
				rollbackError="stop the agent before rolling back: it is in the middle of a turn"
			/>,
		);
		await userEvent.click(screen.getAllByRole("button", { name: "Roll back to here" })[0]!);
		expect(screen.getByRole("alert").textContent).toContain("stop the agent");
	});

	it("shows the agent surface in the header when the thread has a title", () => {
		render(<ChatWorkspace snapshot={{ ...chatFixture, title: "Fix OAuth Return URL Loss" }} />);
		expect(screen.getByText("Codex")).toBeInTheDocument();
		expect(screen.queryByText("Fix OAuth Return URL Loss")).toBeNull();
		expect(screen.queryByText(chatFixture.sessionId)).toBeNull();
	});

	it("shows the agent surface when the thread has no name", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		expect(screen.getByText("Codex")).toBeInTheDocument();
		expect(screen.queryByText(chatFixture.sessionId)).toBeNull();
	});
});
