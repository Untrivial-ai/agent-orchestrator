import { describe, expect, it, vi } from "vitest";
import { runConversationStop } from "./stopAction";

describe("mobile Chat Stop command", () => {
	it("sends an empty queue scope without an unnecessary refresh", async () => {
		const interrupt = vi.fn().mockResolvedValue(undefined);
		const refresh = vi.fn().mockResolvedValue(undefined);

		await runConversationStop([], interrupt, refresh);

		expect(interrupt).toHaveBeenCalledWith([]);
		expect(refresh).not.toHaveBeenCalled();
	});

	it("refreshes a changed queue before the user can reconfirm its new exact scope", async () => {
		const changed = Object.assign(new Error("queue changed"), {
			code: "CHAT_QUEUE_SCOPE_CHANGED",
			status: 409,
		});
		const interrupt = vi.fn()
			.mockRejectedValueOnce(changed)
			.mockResolvedValueOnce(undefined);
		const refresh = vi.fn().mockResolvedValue(undefined);

		await expect(runConversationStop(["old"], interrupt, refresh)).rejects.toBe(changed);
		expect(refresh).toHaveBeenCalledOnce();
		await runConversationStop(["old", "new"], interrupt, refresh);

		expect(interrupt.mock.calls).toEqual([
			[["old"]],
			[["old", "new"]],
		]);
	});

	it("does not hide unrelated Stop failures behind a refresh", async () => {
		const failed = Object.assign(new Error("offline"), { code: "CHAT_PROVIDER_REFUSED" });
		const interrupt = vi.fn().mockRejectedValue(failed);
		const refresh = vi.fn().mockResolvedValue(undefined);

		await expect(runConversationStop(["queued"], interrupt, refresh)).rejects.toBe(failed);
		expect(refresh).not.toHaveBeenCalled();
	});
});
