import { describe, expect, it, vi } from "vitest";
import { runSheetMutation } from "./sheetMutation";

describe("sheet mutation", () => {
	it("commits only after the remote action succeeds", async () => {
		const commit = vi.fn();
		const reject = vi.fn();
		await runSheetMutation(() => Promise.resolve("saved"), commit, reject);
		expect(commit).toHaveBeenCalledWith("saved");
		expect(reject).not.toHaveBeenCalled();
	});

	it("keeps local state unchanged and exposes a failed save", async () => {
		const commit = vi.fn();
		const reject = vi.fn();
		await runSheetMutation(() => Promise.reject(new Error("save failed")), commit, reject);
		expect(commit).not.toHaveBeenCalled();
		expect(reject).toHaveBeenCalledWith("save failed");
	});
});
