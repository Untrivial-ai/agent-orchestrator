import type { WebContents } from "electron";
import type { BrowserViewHost } from "./browser-view-host";

export async function toggleAppDevTools(
	browserHost: Pick<BrowserViewHost, "toggleDevToolsForLastFocused"> | null,
	getShell: () => Pick<WebContents, "toggleDevTools"> | null,
): Promise<void> {
	try {
		const state = await browserHost?.toggleDevToolsForLastFocused();
		if (state) return;
	} catch {
		// A missing or unavailable Browser panel falls back to the shell.
	}
	getShell()?.toggleDevTools();
}
