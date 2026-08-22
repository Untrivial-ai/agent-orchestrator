/**
 * The per-turn retry control for a failed turn.
 *
 * Retry is the inverse of rollback: it re-dispatches a failed turn's durable
 * prompt as a NEW turn instead of discarding the exchange. What is asserted is
 * behaviour a user would notice: the control appears only on an eligible failed
 * turn, and the turn id that reaches the daemon is the one that was clicked.
 */

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChatWorkspace } from "./ChatWorkspace";
import { chatFixture } from "../../lib/chat-fixture";
import type { ConversationSnapshot } from "../../types/conversation";

/** A conversation with one failed turn and nothing in flight. */
function failedSnapshot(): ConversationSnapshot {
	return {
		...chatFixture,
		controller: { state: "ready" },
		turns: chatFixture.turns.map((turn) =>
			turn.id === "turn-1"
				? {
						...turn,
						state: "failed" as const,
						completedAt: turn.requestedAt,
						errorMessage: "stream disconnected before completion",
					}
				: turn.state === "running"
					? { ...turn, state: "completed" as const, completedAt: turn.requestedAt }
					: turn,
		),
	};
}

describe("ChatWorkspace retry", () => {
	it("offers a retry on the failed turn and reports the clicked turn", async () => {
		const onRetryTurn = vi.fn();
		render(<ChatWorkspace snapshot={failedSnapshot()} onRetryTurn={onRetryTurn} />);

		const retry = screen.getByRole("button", { name: "Retry this turn" });
		expect(retry).toBeDefined();

		await userEvent.click(retry);

		// The failed turn in the fixture is turn-1; the daemon must be given AO's
		// own turn id, which is what the snapshot exposes.
		expect(onRetryTurn).toHaveBeenCalledWith("turn-1");
	});

	it("draws no retry control for a turn that succeeded", () => {
		// The idle fixture has only completed turns, so retry must be absent.
		const completed = {
			...chatFixture,
			controller: { state: "ready" as const },
			turns: chatFixture.turns.map((turn) =>
				turn.state === "running"
					? { ...turn, state: "completed" as const, completedAt: turn.requestedAt }
					: turn,
			),
		};
		render(<ChatWorkspace snapshot={completed} onRetryTurn={vi.fn()} />);
		expect(screen.queryByRole("button", { name: "Retry this turn" })).toBeNull();
	});

	it("draws no retry control when the daemon offers none", () => {
		render(<ChatWorkspace snapshot={failedSnapshot()} />);
		expect(screen.queryByRole("button", { name: "Retry this turn" })).toBeNull();
	});
});
