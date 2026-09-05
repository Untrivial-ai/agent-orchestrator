import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL, FOCUS_TERMINAL_SHORTCUT_CHANNEL, KEYBOARD_SHORTCUTS_HELP_CHANNEL, NEXT_SESSION_SHORTCUT_CHANNEL, NEXT_TAB_SHORTCUT_CHANNEL, NEW_SESSION_SHORTCUT_CHANNEL, NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL, OPEN_SETTINGS_SHORTCUT_CHANNEL, PREVIOUS_SESSION_SHORTCUT_CHANNEL, PREVIOUS_TAB_SHORTCUT_CHANNEL, SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL } from "./shared/shortcuts";
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
const telemetryPolicyBroadcastListener = electronMocks.listeners.get("telemetry:policyChanged");
const rendererQueuePurgeRequestListener = electronMocks.listeners.get("telemetry:clearRendererQueues");

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

describe("preload repository branch bridge", () => {
	it("invokes the main-process branch probe over IPC", async () => {
		electronMocks.invoke.mockResolvedValueOnce("main");

		await expect(exposedBridge().app.getRepositoryBranch("/repo/project")).resolves.toBe("main");

		expect(electronMocks.invoke).toHaveBeenCalledWith("app:getRepositoryBranch", "/repo/project");
	});
});

describe("preload Developer Mode updater bridge", () => {
	it("sends only the updater eligibility boolean to the main process", async () => {
		await exposedBridge().updateSettings.setMacDifferentialUpdates(true);

		expect(electronMocks.invoke).toHaveBeenCalledWith(
			"updateSettings:setMacDifferentialUpdates",
			true,
		);
	});
});

describe("preload telemetry generation bridge", () => {
	it("tags captures with the latest broadcast generation without a renderer reload", async () => {
		telemetryPolicyBroadcastListener?.({}, { eventsEnabled: false, consentGeneration: "generation-off", updatedAt: "2026-08-28T10:15:30.000Z", acknowledged: true, state: "applied", environmentVeto: false, durabilitySupported: true });
		await exposedBridge().telemetry.capture({ kind: "message", message: "first" });
		telemetryPolicyBroadcastListener?.({}, { eventsEnabled: true, consentGeneration: "generation-on", updatedAt: "2026-08-28T10:15:31.000Z", acknowledged: true, state: "applied", environmentVeto: false, durabilitySupported: true });
		await exposedBridge().telemetry.capture({ kind: "message", message: "second" });
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(1, "telemetry:capture", { kind: "message", message: "first", consentGeneration: "generation-off" });
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(2, "telemetry:capture", { kind: "message", message: "second", consentGeneration: "generation-on" });
	});

	it("attaches the latest policy generation to visibility signals without accepting renderer generation", () => {
		telemetryPolicyBroadcastListener?.({}, { eventsEnabled: true, consentGeneration: "generation-a", updatedAt: "2026-08-28T10:15:30.000Z", acknowledged: true, state: "applied", environmentVeto: false, durabilitySupported: true });
		expect(exposedBridge().telemetry.signalAgentSwitchVisibility({ kind: "focus", value: true })).toBe(true);
		telemetryPolicyBroadcastListener?.({}, { eventsEnabled: true, consentGeneration: "generation-b", updatedAt: "2026-08-28T10:15:31.000Z", acknowledged: true, state: "applied", environmentVeto: false, durabilitySupported: true });
		expect(exposedBridge().telemetry.signalAgentSwitchVisibility({ kind: "online", value: false })).toBe(true);
		expect(electronMocks.send).toHaveBeenNthCalledWith(1, "agent-switch:visibility", { consentGeneration: "generation-a", signal: { kind: "focus", value: true } });
		expect(electronMocks.send).toHaveBeenNthCalledWith(2, "agent-switch:visibility", { consentGeneration: "generation-b", signal: { kind: "online", value: false } });
	});

	it("acknowledges renderer queue purge only after every registered cleanup succeeds", async () => {
		const first = vi.fn();
		const second = vi.fn().mockResolvedValue(undefined);
		const disposeFirst = exposedBridge().telemetry.onClearQueues(first);
		const disposeSecond = exposedBridge().telemetry.onClearQueues(second);

		rendererQueuePurgeRequestListener?.({}, { requestId: "purge-1" });

		await vi.waitFor(() => {
			expect(first).toHaveBeenCalledOnce();
			expect(second).toHaveBeenCalledOnce();
			expect(electronMocks.send).toHaveBeenCalledWith("telemetry:rendererQueuesCleared", { requestId: "purge-1", ok: true });
		});
		disposeFirst();
		disposeSecond();
	});

	it("reports renderer queue purge failure instead of over-acknowledging opt-out", async () => {
		const dispose = exposedBridge().telemetry.onClearQueues(vi.fn().mockRejectedValue(new Error("purge failed")));

		rendererQueuePurgeRequestListener?.({}, { requestId: "purge-2" });

		await vi.waitFor(() => expect(electronMocks.send).toHaveBeenCalledWith("telemetry:rendererQueuesCleared", { requestId: "purge-2", ok: false }));
		dispose();
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

describe("preload browser profile bridge", () => {
	it("routes profile state, native menu, and CRUD calls over IPC", async () => {
		const bridge = exposedBridge();
		await bridge.browser.getProfile("1:worker-1");
		await bridge.browser.showProfileMenu({
			viewId: "1:worker-1",
			bounds: { x: 1, y: 2, width: 3, height: 4 },
			labels: {
				temporary: "Temporary",
				manage: "Manage",
				switchTitle: "Switch",
				switchMessage: "Reload",
				switchDetail: "Unsaved",
				cancel: "No",
				confirm: "Yes",
			},
		});
		await bridge.browser.selectProfile({
			viewId: "1:worker-1",
			profileId: null,
			labels: {
				temporary: "Temporary",
				manage: "Manage",
				switchTitle: "Switch",
				switchMessage: "Reload",
				switchDetail: "Unsaved",
				cancel: "No",
				confirm: "Yes",
			},
		});
		await bridge.browser.historySuggestions({ viewId: "1:worker-1", query: "git" });
		await bridge.browserProfiles.list();
		await bridge.browserProfiles.create("Work");
		await bridge.browserProfiles.rename({ id: "profile-id", name: "Personal" });
		await bridge.browserProfiles.clear("profile-id");
		await bridge.browserProfiles.delete("profile-id");
		await bridge.browserProfiles.discoverImportSources();
		await bridge.browserProfiles.import({
			requestId: "11111111-1111-4111-8111-111111111111",
			sourceId: "a".repeat(32),
			profileIds: ["b".repeat(32)],
			includeCookies: true,
			includeHistory: true,
			destination: { mode: "merge", name: "Work" },
		});

		expect(electronMocks.invoke).toHaveBeenNthCalledWith(1, "browser:profile:get", "1:worker-1");
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(2, "browser:profile:menu", expect.objectContaining({ viewId: "1:worker-1" }));
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(3, "browser:profile:select", expect.objectContaining({ viewId: "1:worker-1", profileId: null }));
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(4, "browser:history:suggest", { viewId: "1:worker-1", query: "git" });
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(5, "browserProfiles:list");
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(6, "browserProfiles:create", { name: "Work" });
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(7, "browserProfiles:rename", { id: "profile-id", name: "Personal" });
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(8, "browserProfiles:clear", { id: "profile-id" });
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(9, "browserProfiles:delete", { id: "profile-id" });
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(10, "browserProfiles:import:discover");
		expect(electronMocks.invoke).toHaveBeenNthCalledWith(11, "browserProfiles:import:start", expect.objectContaining({ destination: { mode: "merge", name: "Work" } }));
	});

	it("validates profile-management event payloads and removes wrapped listeners", () => {
		const bridge = exposedBridge();
		const stateListener = vi.fn();
		const stateDispose = bridge.browser.onProfileState(stateListener);
		const stateWrapped = electronMocks.listeners.get("browser:profileState");
		stateWrapped?.({}, { viewId: "1:worker-1", profileId: null, temporary: true });
		expect(stateListener).toHaveBeenCalledWith({ viewId: "1:worker-1", profileId: null, temporary: true });
		stateDispose();
		expect(electronMocks.off).toHaveBeenCalledWith("browser:profileState", stateWrapped);

		const manageListener = vi.fn();
		const manageDispose = bridge.browser.onProfileManage(manageListener);
		const manageWrapped = electronMocks.listeners.get("browser:profileManage");
		manageWrapped?.({}, { viewId: "1:worker-1" });
		manageWrapped?.({}, { viewId: 42 });
		expect(manageListener).toHaveBeenCalledTimes(1);
		expect(manageListener).toHaveBeenCalledWith("1:worker-1");
		manageDispose();
		expect(electronMocks.off).toHaveBeenCalledWith("browser:profileManage", manageWrapped);

		const progressListener = vi.fn();
		const progressDispose = bridge.browserProfiles.onImportProgress(progressListener);
		const progressWrapped = electronMocks.listeners.get("browserProfiles:import:progress");
		progressWrapped?.({}, { requestId: "request", phase: "reading", completed: 1, total: 2 });
		expect(progressListener).toHaveBeenCalledWith({ requestId: "request", phase: "reading", completed: 1, total: 2 });
		progressDispose();
		expect(electronMocks.off).toHaveBeenCalledWith("browserProfiles:import:progress", progressWrapped);
	});
});
