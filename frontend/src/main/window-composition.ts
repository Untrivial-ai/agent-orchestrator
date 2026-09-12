import type { BaseWindow, WebContents, WebContentsView } from "electron";

type MainWindowHost = Pick<BaseWindow, "contentView" | "isDestroyed">;

export type WebContentsViewConstructor = new (options: {
	webPreferences: Electron.WebPreferences;
}) => WebContentsView;

export type WindowComposition = {
	shellView: WebContentsView;
	shellWebContents: WebContents;
	setOverlayOpen: (open: boolean) => void;
	resize: () => void;
	dispose: () => void;
};

/**
 * Owns the explicit shell surface used by the native compositor.
 *
 * The native path uses a BaseWindow so there is no implicit BrowserWindow
 * renderer competing with this explicit shell surface. The main process can
 * then move the shell above/below the live browser page without a hidden blank
 * renderer covering it.
 */
export function createWindowComposition(options: {
	mainWindow: MainWindowHost;
	WebContentsView: WebContentsViewConstructor;
	preload: string;
	platform: NodeJS.Platform;
}): WindowComposition {
	const shellView = new options.WebContentsView({
		webPreferences: {
			preload: options.preload,
			contextIsolation: true,
			nodeIntegration: false,
			sandbox: true,
			transparent: true,
		},
	});
	shellView.setBackgroundColor("#00000000");
	options.mainWindow.contentView.addChildView(shellView, 0);

	let overlayOpen = false;
	const resize = (): void => {
		if (options.mainWindow.isDestroyed?.()) return;
		const bounds = options.mainWindow.contentView.getBounds();
		shellView.setBounds({ x: 0, y: 0, width: bounds.width, height: bounds.height });
		shellView.setVisible(true);
	};
	options.mainWindow.contentView.on("bounds-changed", resize);

	// Forces the compositor to rebuild the shell's surface. Identical bounds are
	// ignored, so shrink by a pixel and restore on the next tick — the two calls
	// would otherwise coalesce into a single no-op resize.
	const forceSurfaceRefresh = (): void => {
		if (options.mainWindow.isDestroyed?.()) return;
		const bounds = options.mainWindow.contentView.getBounds();
		if (bounds.width <= 0 || bounds.height <= 0) return;
		shellView.setBounds({ x: 0, y: 0, width: bounds.width, height: Math.max(1, bounds.height - 1) });
		setTimeout(() => {
			if (options.mainWindow.isDestroyed?.()) return;
			const current = options.mainWindow.contentView.getBounds();
			shellView.setBounds({ x: 0, y: 0, width: current.width, height: current.height });
		}, 0);
	};

	const setOverlayOpen = (open: boolean): void => {
		if (overlayOpen === open) return;
		overlayOpen = open;
		if (open) {
			// Re-adding an existing child raises it above all page/DevTools views.
			options.mainWindow.contentView.addChildView(shellView);
		} else {
			// Index zero leaves every live native surface above the transparent shell.
			options.mainWindow.contentView.addChildView(shellView, 0);
		}
		// Restacking alone does not re-establish the shell's compositing surface:
		// on macOS the freshly-raised shell can present a stale surface that hides
		// the live page beneath it, leaving the viewport blank until some real
		// geometry change rebuilds it. (Symptom: blank on a fresh launch, but
		// correct at every size once the window has been resized or fullscreened
		// even once.) Re-applying identical bounds is a no-op, so nudge the height
		// by a pixel and restore it on the next tick to force a real resize. Keep
		// that workaround macOS-only: on Windows, resizing the transparent shell while a
		// maximized native browser view is underneath can invalidate that shell
		// to opaque black for the lifetime of the overlay.
		if (open && options.platform === "darwin") forceSurfaceRefresh();
	};

	resize();

	return {
		shellView,
		shellWebContents: shellView.webContents,
		setOverlayOpen,
		resize,
		dispose: () => {
			try {
				// Reading `contentView` on an already-destroyed BaseWindow throws
				// "Object has been destroyed". On window close the `closed` event
				// fires after the native window is gone, so both calls here can
				// throw — keep them together and out of the way of the shell
				// WebContents teardown below, which must always run.
				options.mainWindow.contentView.removeListener("bounds-changed", resize);
				options.mainWindow.contentView.removeChildView(shellView);
			} catch {
				// The BaseWindow may already have destroyed its content hierarchy.
			}
			try {
				shellView.webContents.close();
			} catch {
				// Explicit shell WebContents ownership is idempotent during teardown.
			}
		},
	};
}
