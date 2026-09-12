import { describe, expect, it, vi } from "vitest";
import { createWindowComposition } from "./window-composition";

function setup(platform: NodeJS.Platform = "win32") {
	let bounds = { x: 0, y: 0, width: 900, height: 640 };
	let boundsChanged: (() => void) | undefined;
	const addChildView = vi.fn();
	const removeChildView = vi.fn();
	const removeListener = vi.fn();
	const close = vi.fn();
	const view = {
		webContents: { close },
		setBackgroundColor: vi.fn(),
		setBounds: vi.fn(),
		setVisible: vi.fn(),
	};
	const mainWindow = {
		contentView: {
			addChildView,
			getBounds: () => bounds,
			on: vi.fn((event: string, listener: () => void) => {
				if (event === "bounds-changed") boundsChanged = listener;
			}),
			removeChildView,
			removeListener,
		},
		isDestroyed: () => false,
	};
	function FakeWebContentsView() {
		return view;
	}
	const composition = createWindowComposition({
		mainWindow: mainWindow as never,
		WebContentsView: FakeWebContentsView as never,
		preload: "/preload.js",
		platform,
	});
	return {
		addChildView,
		bounds: () => bounds,
		close,
		composition,
		emitBoundsChanged: () => boundsChanged?.(),
		mainWindow,
		removeChildView,
		removeListener,
		setBounds: (next: typeof bounds) => {
			bounds = next;
		},
		view,
	};
}

describe("createWindowComposition", () => {
	it("creates a transparent shell at window bounds and reorders it for overlays", () => {
		const { addChildView, composition, view } = setup();

		expect(view.setBackgroundColor).toHaveBeenCalledWith("#00000000");
		expect(addChildView).toHaveBeenNthCalledWith(1, view, 0);
		expect(view.setBounds).toHaveBeenCalledWith({ x: 0, y: 0, width: 900, height: 640 });

		composition.setOverlayOpen(true);
		expect(addChildView).toHaveBeenLastCalledWith(view);
		composition.setOverlayOpen(false);
		expect(addChildView).toHaveBeenLastCalledWith(view, 0);
	});

	it("does not resize the transparent shell when raising overlays on Windows", () => {
		const { composition, view } = setup("win32");

		(view.setBounds as ReturnType<typeof vi.fn>).mockClear();
		composition.setOverlayOpen(true);

		expect(view.setBounds).not.toHaveBeenCalled();
	});

	it("refreshes the shell surface when raising overlays on macOS", () => {
		vi.useFakeTimers();
		try {
			const { composition, view } = setup("darwin");
			(view.setBounds as ReturnType<typeof vi.fn>).mockClear();

			composition.setOverlayOpen(true);

			expect(view.setBounds).toHaveBeenCalledWith({ x: 0, y: 0, width: 900, height: 639 });
			vi.runAllTimers();
			expect(view.setBounds).toHaveBeenLastCalledWith({ x: 0, y: 0, width: 900, height: 640 });
		} finally {
			vi.useRealTimers();
		}
	});

	it("does not let a stale macOS refresh timer override a newer overlay transition", () => {
		vi.useFakeTimers();
		try {
			const { composition, view } = setup("darwin");
			(view.setBounds as ReturnType<typeof vi.fn>).mockClear();

			composition.setOverlayOpen(true);
			composition.setOverlayOpen(false);
			composition.setOverlayOpen(true);
			vi.runAllTimers();

			expect(view.setBounds).toHaveBeenLastCalledWith({ x: 0, y: 0, width: 900, height: 640 });
		} finally {
			vi.useRealTimers();
		}
	});

	it("resizes and disposes the explicit shell without recreating it", () => {
		const { bounds, close, composition, emitBoundsChanged, removeChildView, removeListener, setBounds, view } = setup();

		(view.setBounds as ReturnType<typeof vi.fn>).mockClear();
		setBounds({ x: 0, y: 0, width: 1920, height: 1080 });
		emitBoundsChanged();
		expect(view.setBounds).toHaveBeenCalledWith({ x: 0, y: 0, width: 1920, height: 1080 });
		expect(bounds()).toEqual({ x: 0, y: 0, width: 1920, height: 1080 });

		composition.dispose();
		expect(removeListener).toHaveBeenCalledWith("bounds-changed", composition.resize);
		expect(removeChildView).toHaveBeenCalledWith(view);
		expect(close).toHaveBeenCalledOnce();
	});

	it("still closes the shell WebContents when the BaseWindow is already destroyed", () => {
		const close = vi.fn();
		const view = {
			webContents: { close },
			setBackgroundColor: vi.fn(),
			setBounds: vi.fn(),
			setVisible: vi.fn(),
		};
		// A real `contentView` is a stable object; keep the spies stable too so
		// the assertions below observe what dispose() actually touched.
		const contentView = {
			addChildView: vi.fn(),
			getBounds: () => ({ x: 0, y: 0, width: 900, height: 640 }),
			on: vi.fn(),
			removeChildView: vi.fn(),
			removeListener: vi.fn(),
		};
		let destroyed = false;
		const mainWindow = {
			get contentView() {
				// Electron throws this exact error for any property access on a
				// destroyed BaseWindow; `closed` fires after the window is gone.
				if (destroyed) throw new TypeError("Object has been destroyed");
				return contentView;
			},
			isDestroyed: () => destroyed,
		};
		const composition = createWindowComposition({
			mainWindow: mainWindow as never,
			WebContentsView: function FakeWebContentsView() {
				return view;
			} as never,
			preload: "/preload.js",
			platform: "darwin",
		});

		destroyed = true;
		expect(() => composition.dispose()).not.toThrow();
		// The getter throws before either call is reached, but the shell
		// WebContents teardown must still run.
		expect(contentView.removeListener).not.toHaveBeenCalled();
		expect(contentView.removeChildView).not.toHaveBeenCalled();
		expect(close).toHaveBeenCalledOnce();
	});
});
