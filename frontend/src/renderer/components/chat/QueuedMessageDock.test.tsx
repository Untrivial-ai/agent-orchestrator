import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { QueuedMessageDock, type QueuedMessage } from "./QueuedMessageDock";

const { dragEnds, dragStarts } = vi.hoisted(() => ({
	dragEnds: new Map<string, (event: { active: { id: string }; over: { id: string } | null }) => void>(),
	dragStarts: new Map<string, (event: {
		active: { id: string; rect: { current: { initial: { width: number } } } };
	}) => void>(),
}));

vi.mock("@dnd-kit/core", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@dnd-kit/core")>();
	return {
		...actual,
		DndContext: ({
			children,
			id,
			onDragEnd,
			onDragStart,
		}: {
			children: React.ReactNode;
			id?: string;
			onDragEnd?: (event: { active: { id: string }; over: { id: string } | null }) => void;
			onDragStart?: (event: {
				active: { id: string; rect: { current: { initial: { width: number } } } };
			}) => void;
		}) => {
			if (id && onDragEnd) dragEnds.set(id, onDragEnd);
			if (id && onDragStart) dragStarts.set(id, onDragStart);
			return children;
		},
		DragOverlay: ({ children }: { children: React.ReactNode }) => children,
	};
});

function queuedMessages(texts: string[]): QueuedMessage[] {
	return texts.map((text, index) => {
		const turnId = `queued-${index + 1}`;
		return {
			turnId,
			message: {
				kind: "message",
				id: `${turnId}-message`,
				turnId,
				sequence: 100 + index,
				revision: 0,
				role: "user",
				origin: "human",
				text,
				streaming: false,
				createdAt: `2026-08-11T10:0${index + 1}:00Z`,
			},
		};
	});
}

function rowTexts() {
	const dock = screen.getByTestId("queued-message-dock");
	return within(dock)
		.getAllByTestId(/^queued-message-queued-/, { exact: false })
		.map((row) => row.textContent?.replace(/\s+/g, " ").trim() ?? "");
}

function simulateDrag(activeId: string, overId: string) {
	const dragStart = dragStarts.get("queue-dock-reorder");
	const dragEnd = dragEnds.get("queue-dock-reorder");
	if (!dragStart || !dragEnd) {
		throw new Error("queue dock drag handlers were not registered");
	}
	act(() => {
		dragStart({
			active: {
				id: activeId,
				rect: { current: { initial: { width: 420 } } },
			},
		});
		dragEnd({ active: { id: activeId }, over: { id: overId } });
	});
}

describe("QueuedMessageDock reorder", () => {
	it("submits FIFO order when dragging the first display row to the bottom", async () => {
		const onReorderQueuedTurns = vi.fn().mockResolvedValue(undefined);
		render(
			<QueuedMessageDock
				messages={queuedMessages(["first queued", "second queued", "third queued"])}
				onReorderQueuedTurns={onReorderQueuedTurns}
			/>,
		);

		simulateDrag("queued-3", "queued-1");

		await waitFor(() => {
			expect(onReorderQueuedTurns).toHaveBeenCalledWith(["queued-3", "queued-1", "queued-2"]);
		});
	});

	it("submits FIFO order when dragging the last display row to the top", async () => {
		const onReorderQueuedTurns = vi.fn().mockResolvedValue(undefined);
		render(
			<QueuedMessageDock
				messages={queuedMessages(["first queued", "second queued", "third queued"])}
				onReorderQueuedTurns={onReorderQueuedTurns}
			/>,
		);

		simulateDrag("queued-1", "queued-3");

		await waitFor(() => {
			expect(onReorderQueuedTurns).toHaveBeenCalledWith(["queued-2", "queued-3", "queued-1"]);
		});
	});

	it("restores the visual order and surfaces an error when reorder fails", async () => {
		const onReorderQueuedTurns = vi.fn().mockRejectedValue(new Error("reorder failed"));
		render(
			<QueuedMessageDock
				messages={queuedMessages(["first queued", "second queued", "third queued"])}
				onReorderQueuedTurns={onReorderQueuedTurns}
			/>,
		);

		expect(rowTexts()).toEqual([
			expect.stringContaining("third queued"),
			expect.stringContaining("second queued"),
			expect.stringContaining("first queued"),
		]);

		simulateDrag("queued-3", "queued-1");

		await waitFor(() => {
			expect(screen.getByRole("status")).toHaveTextContent("reorder failed");
		});
		expect(rowTexts()).toEqual([
			expect.stringContaining("third queued"),
			expect.stringContaining("second queued"),
			expect.stringContaining("first queued"),
		]);
	});

	it("freezes hover-only steer visibility while dragging", async () => {
		const user = userEvent.setup();
		const onReorderQueuedTurns = vi.fn().mockResolvedValue(undefined);
		render(
			<QueuedMessageDock
				canSteer
				messages={queuedMessages(["first queued", "second queued"])}
				onReorderQueuedTurns={onReorderQueuedTurns}
			/>,
		);

		await user.hover(screen.getByTestId("queued-message-queued-2"));
		simulateDrag("queued-2", "queued-1");

		await waitFor(() => {
			expect(onReorderQueuedTurns).toHaveBeenCalledWith(["queued-2", "queued-1"]);
		});
	});
});
