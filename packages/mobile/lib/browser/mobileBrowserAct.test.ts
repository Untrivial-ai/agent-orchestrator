import { describe, expect, it, vi } from "vitest";
import { executeMobileBrowserAct } from "./mobileBrowserAct";
import type { MobileBrowserCommand, MobileBrowserCommandResult } from "./mobileBrowserRuntime";

const command: MobileBrowserCommand = {
	type: "command",
	requestId: "r1",
	sessionId: "s1",
	action: "act",
	args: { instruction: "More information link", action: "click" },
};

describe("mobile browser act", () => {
	it("collapses snapshot, match, and click into one agent command", async () => {
		const run = vi.fn(async (next: MobileBrowserCommand): Promise<MobileBrowserCommandResult> => next.action === "snapshot"
			? { ok: true, result: { text: '- link "More information" [ref=e3]', refs: { e3: { role: "link", name: "More information" } } } }
			: { ok: true, result: { clicked: "e3" } });
		await expect(executeMobileBrowserAct(command, run)).resolves.toMatchObject({
			ok: true,
			result: { outcome: "matched", resolvedRef: "e3", retried: false },
		});
		expect(run.mock.calls.map(([next]) => [next.action, next.args])).toEqual([
			["snapshot", { interactive: true }],
			["click", { ref: "e3" }],
		]);
	});

	it("retries one stale reference without another agent round trip", async () => {
		let clickCount = 0;
		const run = vi.fn(async (next: MobileBrowserCommand): Promise<MobileBrowserCommandResult> => {
			if (next.action === "snapshot") return { ok: true, result: { text: "tree", refs: { e1: { role: "button", name: "Save" } } } };
			clickCount += 1;
			return clickCount === 1
				? { ok: false, error: { code: "STALE_REFERENCE", message: "stale" } }
				: { ok: true, result: { clicked: "e1" } };
		});
		const result = await executeMobileBrowserAct({ ...command, args: { instruction: "Save button" } }, run);
		expect(result).toMatchObject({ ok: true, result: { outcome: "matched", retried: true } });
		expect(run).toHaveBeenCalledTimes(4);
	});

	it("returns the snapshot instead of guessing on no match", async () => {
		const run = vi.fn(async (): Promise<MobileBrowserCommandResult> => ({
			ok: true,
			result: { text: '- button "Save" [ref=e1]', refs: { e1: { role: "button", name: "Save" } } },
		}));
		await expect(executeMobileBrowserAct({ ...command, args: { instruction: "Delete button" } }, run)).resolves.toMatchObject({
			ok: true,
			result: { outcome: "no-match", snapshot: '- button "Save" [ref=e1]' },
		});
		expect(run).toHaveBeenCalledTimes(1);
	});
});
