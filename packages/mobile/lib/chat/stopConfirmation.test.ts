import { beforeEach, describe, expect, it, vi } from "vitest";

const alert = vi.hoisted(() => vi.fn());
vi.mock("react-native", () => ({ Alert: { alert } }));

import { confirmConversationStop } from "./stopConfirmation";

describe("mobile Chat Stop confirmation", () => {
	beforeEach(() => alert.mockReset());

	it("stops an empty queue with an explicit empty durable scope", () => {
		const stop = vi.fn();
		confirmConversationStop([], stop);

		expect(alert).not.toHaveBeenCalled();
		expect(stop).toHaveBeenCalledWith([]);
	});

	it("freezes and names every queued turn in the destructive confirmation", () => {
		const stop = vi.fn();
		const queue = [
			{ turnId: "automation", text: "nightly relay", origin: "automation" as const },
			{ turnId: "human", text: "then explain", origin: "human" as const },
		];
		confirmConversationStop(queue, stop);
		queue.splice(0, queue.length, { turnId: "new", text: "arrived later", origin: "human" });

		expect(alert).toHaveBeenCalledOnce();
		const [title, message, buttons] = alert.mock.calls[0] ?? [];
		expect(title).toBe("Stop turn and cancel 2 queued messages?");
		expect(message).toContain("both queued messages");
		expect(buttons?.[0]).toMatchObject({ text: "Keep working", style: "cancel" });
		expect(buttons?.[1]).toMatchObject({ text: "Stop all", style: "destructive" });
		buttons?.[1]?.onPress?.();
		expect(stop).toHaveBeenCalledWith(["automation", "human"]);
	});

	it("requires a fresh confirmation after the caller refreshes a changed queue", () => {
		const stop = vi.fn();
		confirmConversationStop([{ turnId: "old", text: "old", origin: "human" }], stop);
		alert.mock.calls[0]?.[2]?.[1]?.onPress?.();
		confirmConversationStop([
			{ turnId: "old", text: "old", origin: "human" },
			{ turnId: "new", text: "new", origin: "automation" },
		], stop);
		alert.mock.calls[1]?.[2]?.[1]?.onPress?.();

		expect(stop.mock.calls).toEqual([
			[["old"]],
			[["old", "new"]],
		]);
	});

	it("refuses unsafe legacy snapshots whose complete queue is unknown", () => {
		const stop = vi.fn();
		confirmConversationStop(undefined, stop);

		expect(stop).not.toHaveBeenCalled();
		expect(alert).toHaveBeenCalledWith(
			"Stop unavailable",
			expect.stringMatching(/complete queued work/i),
		);
	});
});
