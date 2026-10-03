import { describe, expect, it, vi } from "vitest";
import { syncMacWindowButtons } from "./window-chrome";

describe("native macOS header alignment", () => {
	it.each([0.75, 1, 1.25, 1.5])("centers unscaled native buttons at zoom %s", (zoom) => {
		const window = { isDestroyed: () => false, isFullScreen: () => false, getWindowButtonPosition: (): { x: number; y: number } | null => null, setWindowButtonPosition: vi.fn() };
		syncMacWindowButtons(window, { isDestroyed: () => false, getZoomFactor: () => zoom });
		const position = window.setWindowButtonPosition.mock.calls[0][0];
		expect(Math.abs(position.y + 7 - 18 * zoom)).toBeLessThanOrEqual(0.5);
		expect(position.x).toBe(14);
	});
	it("avoids reconfiguring the native view when zoom has not changed", () => {
		const window = { isDestroyed: () => false, isFullScreen: () => false, getWindowButtonPosition: (): { x: number; y: number } | null => null, setWindowButtonPosition: vi.fn() };
		const shell = { isDestroyed: () => false, getZoomFactor: () => 1 };
		window.getWindowButtonPosition = () => window.setWindowButtonPosition.mock.calls.at(-1)?.[0] ?? null;
		syncMacWindowButtons(window, shell);
		syncMacWindowButtons(window, shell);
		expect(window.setWindowButtonPosition).toHaveBeenCalledTimes(1);
	});
	it.each(["fullscreen", "closed-window", "closed-shell"])("does not reposition %s", (state) => {
		const window = { isDestroyed: () => state === "closed-window", isFullScreen: () => state === "fullscreen", getWindowButtonPosition: (): { x: number; y: number } | null => null, setWindowButtonPosition: vi.fn() };
		syncMacWindowButtons(window, { isDestroyed: () => state === "closed-shell", getZoomFactor: () => 1 });
		expect(window.setWindowButtonPosition).not.toHaveBeenCalled();
	});
});
