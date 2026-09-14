import { describe, expect, it, vi } from "vitest";
import { applyQueuedTurnAction, queuedTurnPresentation } from "./queuedTurnControls";

describe("mobile queued-turn controls", () => {
	it("offers promote-next only for human work while every item stays cancellable", () => {
		expect(queuedTurnPresentation({
			turnId: "human", text: "  explain the fix  ", origin: "human",
		})).toEqual({
			label: "explain the fix",
			cancelLabel: "Cancel queued message: explain the fix",
			promoteLabel: "Use as next message: explain the fix",
			canPromote: true,
		});
		expect(queuedTurnPresentation({
			turnId: "automation", text: "nightly relay", origin: "automation",
		})).toEqual({
			label: "nightly relay",
			originLabel: "Automation",
			cancelLabel: "Cancel queued automation: nightly relay",
			canPromote: false,
		});
	});

	it("mutates only the selected turn and refreshes the durable queue", async () => {
		const mutate = vi.fn().mockResolvedValue(undefined);
		const refresh = vi.fn().mockResolvedValue(undefined);

		await applyQueuedTurnAction("queued-2", mutate, refresh);

		expect(mutate).toHaveBeenCalledWith("queued-2");
		expect(refresh).toHaveBeenCalledOnce();
		expect(mutate.mock.invocationCallOrder[0]).toBeLessThan(refresh.mock.invocationCallOrder[0]);
	});

	it("keeps a failed item visible by not refreshing away its actionable error", async () => {
		const failed = new Error("queue changed");
		const mutate = vi.fn().mockRejectedValue(failed);
		const refresh = vi.fn().mockResolvedValue(undefined);

		await expect(applyQueuedTurnAction("queued-1", mutate, refresh)).rejects.toBe(failed);
		expect(refresh).not.toHaveBeenCalled();
	});
});
