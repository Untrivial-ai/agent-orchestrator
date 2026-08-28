import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL, FOCUS_TERMINAL_SHORTCUT_CHANNEL, KEYBOARD_SHORTCUTS_HELP_CHANNEL, NEXT_SESSION_SHORTCUT_CHANNEL, NEXT_TAB_SHORTCUT_CHANNEL, NEW_SESSION_SHORTCUT_CHANNEL, NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL, OPEN_SETTINGS_SHORTCUT_CHANNEL, PREVIOUS_SESSION_SHORTCUT_CHANNEL, PREVIOUS_TAB_SHORTCUT_CHANNEL, SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL } from "./shared/shortcuts";
import { SET_CHAT_DRAFT_RISK_CHANNEL } from "./shared/chat-draft-risk";
import type { AoBridge } from "./preload";

const electronMocks = vi.hoisted(() => {
	const listeners = new Map<string, (...args: unknown[]) => void>();
	return {
		exposeInMainWorld: vi.fn(),
		getPathForFile: vi.fn(),
		invoke: vi.fn(),
		listeners,
		off: vi.fn(),
		on: vi.fn((channel: string, listener: (...args: unknown[]) => void) => {
			listeners.set(channel, listener);
		}),
		send: vi.fn(),
	};
});

vi.mock("electron", () => ({
	contextBridge: { exposeInMainWorld: electronMocks.exposeInMainWorld },
	ipcRenderer: {
		invoke: electronMocks.invoke,
		off: electronMocks.off,
		on: electronMocks.on,
		send: electronMocks.send,
	},
	webUtils: { getPathForFile: electronMocks.getPathForFile },
}));

await import("./preload");

// Captured once, before any test's beforeEach clears the listeners map: this
// is the always-on buffering listener preload.ts registers at module load,
// not the per-call listener onOpenFolderPath registers when invoked.
const openFolderPathBufferListener = electronMocks.listeners.get("app:openFolderPath");

function exposedBridge(): AoBridge {
	const call = electronMocks.exposeInMainWorld.mock.calls.find(([key]) => key === "ao");
	if (!call) throw new Error("preload bridge was not exposed");
	return call[1] as AoBridge;
}

beforeEach(() => {
	electronMocks.listeners.clear();
	electronMocks.getPathForFile.mockClear();
	electronMocks.invoke.mockClear();
	electronMocks.off.mockClear();
	electronMocks.on.mockClear();
	electronMocks.send.mockClear();
});

describe("preload getPathForFile bridge", () => {
	it("forwards the File to webUtils.getPathForFile without going through IPC", () => {
		electronMocks.getPathForFile.mockReturnValue("/Users/x/dropped-folder");
		const file = new File([], "dropped-folder");

		const path = exposedBridge().app.getPathForFile(file);

		expect(path).toBe("/Users/x/dropped-folder");
		expect(electronMocks.getPathForFile).toHaveBeenCalledWith(file);
		expect(electronMocks.invoke).not.toHaveBeenCalled();
	});
});

describe("preload openFolderPath bridge", () => {
	// The dispatcher's "active listener" is module-level state that outlives a
	// single test, exactly like the real renderer's mounted subscription — so
	// every test below disposes its own subscription before finishing, the same
	// way a real unmount would, to avoid leaking into the next test.
	let dispose: (() => void) | undefined;

	afterEach(() => {
		dispose?.();
		dispose = undefined;
	});

	it("replays a folder path that arrived before onOpenFolderPath was called", () => {
		// Regression: cold start / an early second-instance can flush
		// app:openFolderPath before ShellLayout's own effect has run to call
		// onOpenFolderPath and register its listener (React mounts TrayRuntime's
		// child effect, whose ready-ping triggers the main-process flush, before
		// ShellLayout's own parent effect). Without buffering, that path was lost.
		openFolderPathBufferListener?.({}, "/dropped/via-icon");

		const listener = vi.fn();
		dispose = exposedBridge().app.onOpenFolderPath(listener);

		expect(listener).toHaveBeenCalledWith("/dropped/via-icon");
	});

	it("does not replay an already-consumed buffered path a second time", () => {
		openFolderPathBufferListener?.({}, "/dropped/first");
		const firstListener = vi.fn();
		exposedBridge().app.onOpenFolderPath(firstListener)();
		expect(firstListener).toHaveBeenCalledTimes(1);

		const secondListener = vi.fn();
		dispose = exposedBridge().app.onOpenFolderPath(secondListener);
		expect(secondListener).not.toHaveBeenCalled();
	});

	it("delivers a path that arrives normally, after the listener is already registered", () => {
		const listener = vi.fn();
		dispose = exposedBridge().app.onOpenFolderPath(listener);

		openFolderPathBufferListener?.({}, "/dropped/normal");

		expect(listener).toHaveBeenCalledWith("/dropped/normal");
	});

	it("does not replay a normally delivered path to a later resubscription", () => {
		// Regression: a single dispatcher forwards straight to the active
		// listener without ever touching the buffer, so unsubscribing and
		// resubscribing afterward must not hand the new listener a path that
		// was already delivered while the first listener was active.
		const firstListener = vi.fn();
		const disposeFirst = exposedBridge().app.onOpenFolderPath(firstListener);

		openFolderPathBufferListener?.({}, "/dropped/already-delivered");
		expect(firstListener).toHaveBeenCalledWith("/dropped/already-delivered");
		disposeFirst();

		const secondListener = vi.fn();
		dispose = exposedBridge().app.onOpenFolderPath(secondListener);

		expect(secondListener).not.toHaveBeenCalled();
	});
});

describe("preload new-session shortcut bridge", () => {
	it("delivers the IPC event and removes the exact wrapped listener", () => {
		const listener = vi.fn();
		const dispose = exposedBridge().app.onNewSessionShortcut(listener);
		const wrapped = electronMocks.listeners.get(NEW_SESSION_SHORTCUT_CHANNEL);
		expect(wrapped).toBeTypeOf("function");

		wrapped?.({});
		expect(listener).toHaveBeenCalledTimes(1);

		dispose();
		expect(electronMocks.off).toHaveBeenCalledWith(NEW_SESSION_SHORTCUT_CHANNEL, wrapped);
	});
});

describe("preload keyboard-shortcuts help bridge", () => {
	it("delivers the IPC event and removes the exact wrapped listener", () => {
		const listener = vi.fn();
		const dispose = exposedBridge().app.onKeyboardShortcutsHelp(listener);
		const wrapped = electronMocks.listeners.get(KEYBOARD_SHORTCUTS_HELP_CHANNEL);
		expect(wrapped).toBeTypeOf("function");

		wrapped?.({});
		expect(listener).toHaveBeenCalledTimes(1);

		dispose();
		expect(electronMocks.off).toHaveBeenCalledWith(KEYBOARD_SHORTCUTS_HELP_CHANNEL, wrapped);
	});
});

describe("preload application shortcut bridges", () => {
	it("reports whether the active view has a closeable shell terminal", () => {
		exposedBridge().app.setCloseShellTerminalShortcutEnabled(true);

		expect(electronMocks.send).toHaveBeenCalledWith(SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL, true);
	});

	it("reports whether the active Chat draft has an unsafe unload boundary", () => {
		exposedBridge().app.setChatDraftRisk(["persistence-failed", "pending-attachments"]);

		expect(electronMocks.send).toHaveBeenCalledWith(SET_CHAT_DRAFT_RISK_CHANNEL, [
			"persistence-failed",
			"pending-attachments",
		]);
	});

	it.each([
		[NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL, (listener: () => void) => exposedBridge().app.onNewShellTerminalShortcut(listener)],
		[CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL, (listener: () => void) => exposedBridge().app.onCloseShellTerminalShortcut(listener)],
		[OPEN_SETTINGS_SHORTCUT_CHANNEL, (listener: () => void) => exposedBridge().app.onOpenSettingsShortcut(listener)],
		[
			PREVIOUS_SESSION_SHORTCUT_CHANNEL,
			(listener: () => void) => exposedBridge().app.onPreviousSessionShortcut(listener),
		],
		[NEXT_SESSION_SHORTCUT_CHANNEL, (listener: () => void) => exposedBridge().app.onNextSessionShortcut(listener)],
		[PREVIOUS_TAB_SHORTCUT_CHANNEL, (listener: () => void) => exposedBridge().app.onPreviousTabShortcut(listener)],
		[NEXT_TAB_SHORTCUT_CHANNEL, (listener: () => void) => exposedBridge().app.onNextTabShortcut(listener)],
		[FOCUS_TERMINAL_SHORTCUT_CHANNEL, (listener: () => void) => exposedBridge().app.onFocusTerminalShortcut(listener)],
	] as const)("delivers and disposes %s", (channel, subscribe) => {
		const listener = vi.fn();
		const dispose = subscribe(listener);
		const wrapped = electronMocks.listeners.get(channel);

		wrapped?.({});
		expect(listener).toHaveBeenCalledTimes(1);

		dispose();
		expect(electronMocks.off).toHaveBeenCalledWith(channel, wrapped);
	});
});

describe("preload keybinding recording bridge", () => {
	it("tells the main process when shortcut capture starts and stops", async () => {
		await exposedBridge().keybindings.setRecording(true);
		await exposedBridge().keybindings.setRecording(false);

		expect(electronMocks.invoke).toHaveBeenNthCalledWith(1, "keybindings:setRecording", true);
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(2, "keybindings:setRecording", false);
	});
});

describe("preload uiSettings bridge", () => {
	it("invokes get and set over IPC", async () => {
		electronMocks.invoke.mockResolvedValueOnce({ locale: "en" });
		electronMocks.invoke.mockResolvedValueOnce({ locale: "zh-CN" });

		await expect(exposedBridge().uiSettings.get()).resolves.toEqual({ locale: "en" });
		await expect(exposedBridge().uiSettings.set({ locale: "zh-CN" })).resolves.toEqual({ locale: "zh-CN" });

		expect(electronMocks.invoke).toHaveBeenNthCalledWith(1, "uiSettings:get");
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(2, "uiSettings:set", { locale: "zh-CN" });
	});
});
