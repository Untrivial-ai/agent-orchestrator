import { describe, expect, it, vi } from "vitest";
import { createTrayLifecycle, isTrayEnabled, type TrayLifecycleDeps } from "./tray-lifecycle";
import type { TrayController } from "./tray";
import { TRAY_OPEN_SESSION_CHANNEL } from "../shared/tray";

type FakeWebContents = {
	isLoading: () => boolean;
	send: ReturnType<typeof vi.fn>;
};

function fakeWindow(loading = false): { webContents: FakeWebContents } {
	return { webContents: { isLoading: () => loading, send: vi.fn() } };
}

function fakeTrayController() {
	return {
		setState: vi.fn(),
		setLocale: vi.fn(),
		setThemePreference: vi.fn(),
		setUpdateSettings: vi.fn(),
		clear: vi.fn(),
		dispose: vi.fn(),
	};
}

function setup(overrides: Partial<TrayLifecycleDeps> = {}) {
	const window = fakeWindow();
	const tray = fakeTrayController();
	const focusWindow = vi.fn();
	const deps: TrayLifecycleDeps = {
		getWindow: () => window as never,
		getTrayController: () => tray as unknown as TrayController,
		focusWindow,
		...overrides,
	};
	const lifecycle = createTrayLifecycle(deps);
	return { lifecycle, window, tray, focusWindow };
}

function eventFrom(sender: unknown) {
	return { sender } as never;
}

describe("isTrayEnabled", () => {
	it("is enabled on darwin for dev builds regardless of version", () => {
		expect(isTrayEnabled("darwin", false, "0.10.3")).toBe(true);
	});

	it("is enabled on darwin for packaged nightly builds", () => {
		expect(isTrayEnabled("darwin", true, "0.10.3-nightly.20260804")).toBe(true);
	});

	it("is disabled on darwin for packaged stable builds", () => {
		expect(isTrayEnabled("darwin", true, "0.10.3")).toBe(false);
	});

	it("is disabled on every non-darwin platform even for dev/nightly builds", () => {
		expect(isTrayEnabled("win32", false, "0.10.3")).toBe(false);
		expect(isTrayEnabled("linux", true, "0.10.3-nightly.20260804")).toBe(false);
	});
});

describe("createTrayLifecycle openSession", () => {
	it("focuses the window and sends immediately when the window is not loading", () => {
		const { lifecycle, window, focusWindow } = setup();
		lifecycle.openSession({ projectId: "p1", sessionId: "s1" });
		expect(focusWindow).toHaveBeenCalledTimes(1);
		expect(window.webContents.send).toHaveBeenCalledWith(TRAY_OPEN_SESSION_CHANNEL, { projectId: "p1", sessionId: "s1" });
	});

	it("queues the target instead of sending while the window is still loading", () => {
		const window = fakeWindow(true);
		const { lifecycle } = setup({ getWindow: () => window as never });
		lifecycle.openSession({ projectId: "p1", sessionId: "s1" });
		expect(window.webContents.send).not.toHaveBeenCalled();
		lifecycle.handleRendererReady(eventFrom(window.webContents));
		expect(window.webContents.send).toHaveBeenCalledWith(TRAY_OPEN_SESSION_CHANNEL, { projectId: "p1", sessionId: "s1" });
	});

	it("queues the target when there is no window at all", () => {
		const window = fakeWindow(true);
		let currentWindow: ReturnType<typeof fakeWindow> | null = null;
		const { lifecycle } = setup({ getWindow: () => currentWindow as never });
		lifecycle.openSession({ projectId: "p1", sessionId: "s1" });
		currentWindow = window;
		lifecycle.handleRendererReady(eventFrom(window.webContents));
		expect(window.webContents.send).toHaveBeenCalledWith(TRAY_OPEN_SESSION_CHANNEL, { projectId: "p1", sessionId: "s1" });
	});
});

describe("createTrayLifecycle sender validation", () => {
	it("forwards attention state only when the event comes from the tracked window", () => {
		const { lifecycle, window, tray } = setup();
		lifecycle.handleSetAttentionState(eventFrom(window.webContents), { sessions: [] });
		expect(tray.setState).toHaveBeenCalledWith({ sessions: [] });
	});

	it("rejects attention-state updates from a sender that is not the tracked window", () => {
		const { lifecycle, tray } = setup();
		lifecycle.handleSetAttentionState(eventFrom({ other: true }), { sessions: [] });
		expect(tray.setState).not.toHaveBeenCalled();
	});

	it("no-ops when the tray controller does not exist yet", () => {
		const { lifecycle, window, tray } = setup({ getTrayController: () => null });
		lifecycle.handleSetAttentionState(eventFrom(window.webContents), { sessions: [] });
		expect(tray.setState).not.toHaveBeenCalled();
	});

	it("ignores a renderer-ready ping from a sender that is not the tracked window", () => {
		const loadingWindow = fakeWindow(true);
		const { lifecycle } = setup({ getWindow: () => loadingWindow as never });
		lifecycle.openSession({ projectId: "p1", sessionId: "s1" });
		lifecycle.handleRendererReady(eventFrom({ other: true }));
		expect(loadingWindow.webContents.send).not.toHaveBeenCalled();
		lifecycle.handleRendererReady(eventFrom(loadingWindow.webContents));
		expect(loadingWindow.webContents.send).toHaveBeenCalledWith(TRAY_OPEN_SESSION_CHANNEL, { projectId: "p1", sessionId: "s1" });
	});
});

describe("createTrayLifecycle pending-target flush", () => {
	it("flushes a queued target once the renderer reports ready", () => {
		const loadingWindow = fakeWindow(true);
		const { lifecycle } = setup({ getWindow: () => loadingWindow as never });
		lifecycle.openSession({ projectId: "p1", sessionId: "s1" });

		lifecycle.handleRendererReady(eventFrom(loadingWindow.webContents));
		expect(loadingWindow.webContents.send).toHaveBeenCalledWith(TRAY_OPEN_SESSION_CHANNEL, {
			projectId: "p1",
			sessionId: "s1",
		});
	});

	it("does nothing on renderer-ready when there is no queued target", () => {
		const { lifecycle, window } = setup();
		lifecycle.handleRendererReady(eventFrom(window.webContents));
		expect(window.webContents.send).not.toHaveBeenCalled();
	});
});

describe("createTrayLifecycle clear and dispose", () => {
	it("clears the pending target and the tray controller together", () => {
		const loadingWindow = fakeWindow(true);
		const tray = fakeTrayController();
		const { lifecycle } = setup({ getWindow: () => loadingWindow as never, getTrayController: () => tray });
		lifecycle.openSession({ projectId: "p1", sessionId: "s1" });

		lifecycle.clear();
		lifecycle.handleRendererReady(eventFrom(loadingWindow.webContents));
		expect(loadingWindow.webContents.send).not.toHaveBeenCalled();
		expect(tray.clear).toHaveBeenCalledTimes(1);
	});

	it("clears a pending target without clearing the tray state", () => {
		const loadingWindow = fakeWindow(true);
		const tray = fakeTrayController();
		const { lifecycle } = setup({ getWindow: () => loadingWindow as never, getTrayController: () => tray });
		lifecycle.openSession({ projectId: "p1", sessionId: "s1" });

		lifecycle.clearPendingTarget();
		lifecycle.handleRendererReady(eventFrom(loadingWindow.webContents));
		expect(loadingWindow.webContents.send).not.toHaveBeenCalled();
		expect(tray.clear).not.toHaveBeenCalled();
	});

	it("clear is a no-op on the controller when none exists", () => {
		const { lifecycle } = setup({ getTrayController: () => null });
		expect(() => lifecycle.clear()).not.toThrow();
	});

	it("disposes the tray controller", () => {
		const { lifecycle, tray } = setup();
		lifecycle.dispose();
		expect(tray.dispose).toHaveBeenCalledTimes(1);
	});
});
