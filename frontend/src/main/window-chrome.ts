import type { BaseWindow, WebContents } from "electron";
import { MAC_TITLEBAR_HEIGHT, MAC_WINDOW_BUTTON_RADIUS, MAC_WINDOW_BUTTON_X } from "../shared/window-chrome";

/** Native buttons use points, while the shell header scales with Chromium zoom. */
export function syncMacWindowButtons(
	window: Pick<BaseWindow, "isDestroyed" | "isFullScreen" | "setWindowButtonPosition" | "getWindowButtonPosition">,
	shell: Pick<WebContents, "isDestroyed" | "getZoomFactor">,
): void {
	if (window.isDestroyed() || shell.isDestroyed()) return;
	if (window.isFullScreen()) return;
	const y = Math.max(0, Math.round(MAC_TITLEBAR_HEIGHT * shell.getZoomFactor() / 2 - MAC_WINDOW_BUTTON_RADIUS));
	const current = window.getWindowButtonPosition();
	if (current?.x === MAC_WINDOW_BUTTON_X && current.y === y) return;
	window.setWindowButtonPosition({
		x: MAC_WINDOW_BUTTON_X,
		y,
	});
}
