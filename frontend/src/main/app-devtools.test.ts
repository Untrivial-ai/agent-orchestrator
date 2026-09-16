import { describe, expect, it, vi } from "vitest";
import { toggleAppDevTools } from "./app-devtools";

describe("toggleAppDevTools", () => {
	it.each([true, false])("keeps the shell untouched when Browser DevTools are open=%s", async (open) => {
		const browserHost = {
			toggleDevToolsForLastFocused: vi.fn().mockResolvedValue({
				viewId: "view-1", activeTabId: "tab-1", open,
			}),
		};
		const shell = { toggleDevTools: vi.fn() };

		await toggleAppDevTools(browserHost, () => shell);

		expect(browserHost.toggleDevToolsForLastFocused).toHaveBeenCalledOnce();
		expect(shell.toggleDevTools).not.toHaveBeenCalled();
	});

	it.each(["no host", "no focused view", "unavailable view"])("toggles shell DevTools once with %s", async (scenario) => {
		const browserHost = scenario === "no host" ? null : {
			toggleDevToolsForLastFocused: scenario === "no focused view"
				? vi.fn().mockResolvedValue(null)
				: vi.fn().mockRejectedValue(new Error("Browser view unavailable")),
		};
		const shell = { toggleDevTools: vi.fn() };

		await toggleAppDevTools(browserHost, () => shell);

		expect(shell.toggleDevTools).toHaveBeenCalledOnce();
	});

	it("tolerates the shell closing while the Browser operation is pending", async () => {
		let finish: (state: null) => void = () => undefined;
		const browserHost = {
			toggleDevToolsForLastFocused: () => new Promise<null>((resolve) => { finish = resolve; }),
		};
		let shell: { toggleDevTools: () => void } | null = { toggleDevTools: vi.fn() };
		const toggle = toggleAppDevTools(browserHost, () => shell);

		shell = null;
		finish(null);

		await expect(toggle).resolves.toBeUndefined();
	});
});
