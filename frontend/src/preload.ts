import { contextBridge, ipcRenderer } from "electron";
import { CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL, FOCUS_TERMINAL_SHORTCUT_CHANNEL, KEYBOARD_SHORTCUTS_HELP_CHANNEL, NEXT_SESSION_SHORTCUT_CHANNEL, NEXT_TAB_SHORTCUT_CHANNEL, NEW_SESSION_SHORTCUT_CHANNEL, NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL, OPEN_SETTINGS_SHORTCUT_CHANNEL, PREVIOUS_SESSION_SHORTCUT_CHANNEL, PREVIOUS_TAB_SHORTCUT_CHANNEL, SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL, SET_TERMINAL_FOCUSED_CHANNEL, TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL, type KeybindingOverrides } from "./shared/shortcuts";
import type {
	BrowserAgentActivityState,
	BrowserDevToolsInput,
	BrowserDevToolsState,
	BrowserNavState,
	BrowserRect,
	BrowserTabsState,
} from "./main/browser-view-host";
import {
	TRAY_OPEN_SESSION_CHANNEL,
	TRAY_RENDERER_READY_CHANNEL,
	TRAY_SET_ATTENTION_STATE_CHANNEL,
	type TrayAttentionState,
	type TrayOpenSessionTarget,
} from "./shared/tray";
import type { DaemonStatus } from "./shared/daemon-status";
import type { TelemetryBootstrap } from "./shared/telemetry";
import type { MigrationState } from "./main/app-state";
import type { UpdateSettings, UpdateStatus } from "./main/update-settings";
import type { CloudAccount } from "./shared/cloud-account";
import type { UpdateOutcome } from "./shared/update-telemetry";
import type { UiSettings } from "./main/ui-settings";
import type { UpdateCheckOptions } from "./main/auto-updater";
import type { FeatureBuild } from "./main/feature-builds";
import type {
	BrowserAnnotationCancelPayload,
	BrowserAnnotationModeInput,
	BrowserAnnotationSubmitPayload,
} from "./shared/browser-annotations";

if (typeof document !== "undefined") {
	const markNativeBrowserComposition = () => {
		const root = document.documentElement;
		if (root) {
			root.dataset.nativeBrowserComposition = "true";
			root.dataset.aoPlatform = process.platform;
		}
	};
	if (document.readyState === "loading") {
		document.addEventListener("DOMContentLoaded", markNativeBrowserComposition, { once: true });
	} else {
		markNativeBrowserComposition();
	}
}

export type BrowserBoundsInput = {
	viewId: string;
	rect: BrowserRect;
	visible: boolean;
};

export type BrowserNavigateInput = {
	viewId: string;
	url: string;
};

export type ImportFolderMode = "project" | "workspace";

export type ImportRepoScan = {
	name: string;
	path: string;
	relativePath: string;
	branch: string;
	remote: string;
	hasRemote: boolean;
	status?: "ok" | "error";
	reason?: string;
	needsGitInit?: boolean;
};

export type ImportFolderScan = {
	path: string;
	repos: ImportRepoScan[];
	setupWarning?: string;
};

const api = {
	app: {
		getVersion: () => ipcRenderer.invoke("app:getVersion") as Promise<string>,
		chooseDirectory: (title?: string) => ipcRenderer.invoke("app:chooseDirectory", title) as Promise<string | null>,
		openExternal: (url: string) => ipcRenderer.invoke("app:openExternal", url) as Promise<void>,
		scanImportFolder: (input: { path: string; mode: ImportFolderMode }) =>
			ipcRenderer.invoke("app:scanImportFolder", input) as Promise<ImportFolderScan>,
		checkAncestorRepo: (path: string) =>
			ipcRenderer.invoke("app:checkAncestorRepo", path) as Promise<string | undefined>,
		// Fired by the main process when the app-level new-session shortcut
		// (⌘N / Ctrl+Shift+N) is pressed in any web contents.
		onNewSessionShortcut: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(NEW_SESSION_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(NEW_SESSION_SHORTCUT_CHANNEL, wrapped);
			};
		},
		onKeyboardShortcutsHelp: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(KEYBOARD_SHORTCUTS_HELP_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(KEYBOARD_SHORTCUTS_HELP_CHANNEL, wrapped);
			};
		},
		// Fired by the main process when ⌘T / Ctrl+T is pressed in any web contents,
		// including while focus is inside a terminal pane.
		onNewShellTerminalShortcut: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(NEW_SHELL_TERMINAL_SHORTCUT_CHANNEL, wrapped);
			};
		},
		onCloseShellTerminalShortcut: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(CLOSE_SHELL_TERMINAL_SHORTCUT_CHANNEL, wrapped);
			};
		},
		setCloseShellTerminalShortcutEnabled: (enabled: boolean) => {
			ipcRenderer.send(SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL, enabled);
		},
		onOpenSettingsShortcut: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(OPEN_SETTINGS_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(OPEN_SETTINGS_SHORTCUT_CHANNEL, wrapped);
			};
		},
		onPreviousSessionShortcut: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(PREVIOUS_SESSION_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(PREVIOUS_SESSION_SHORTCUT_CHANNEL, wrapped);
			};
		},
		onNextSessionShortcut: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(NEXT_SESSION_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(NEXT_SESSION_SHORTCUT_CHANNEL, wrapped);
			};
		},
		onPreviousTabShortcut: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(PREVIOUS_TAB_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(PREVIOUS_TAB_SHORTCUT_CHANNEL, wrapped);
			};
		},
		onNextTabShortcut: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(NEXT_TAB_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(NEXT_TAB_SHORTCUT_CHANNEL, wrapped);
			};
		},
		onFocusTerminalShortcut: (listener: () => void) => {
			const wrapped = () => listener();
			ipcRenderer.on(FOCUS_TERMINAL_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(FOCUS_TERMINAL_SHORTCUT_CHANNEL, wrapped);
			};
		},
	},
	terminal: {
		saveDroppedFile: (input: { name: string; bytes: Uint8Array }) =>
			ipcRenderer.invoke("terminal:saveDroppedFile", input) as Promise<string>,
		setFocused: (focused: boolean) => ipcRenderer.send(SET_TERMINAL_FOCUSED_CHANNEL, focused),
		onFontSizeShortcut: (listener: (delta: -1 | 1) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, delta: -1 | 1) => listener(delta);
			ipcRenderer.on(TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL, wrapped);
			return () => {
				ipcRenderer.off(TERMINAL_FONT_SIZE_SHORTCUT_CHANNEL, wrapped);
			};
		},
	},
	window: {
		isMaximized: () => ipcRenderer.invoke("window:isMaximized") as Promise<boolean>,
		onMaximized: (listener: (maximized: boolean) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, maximized: boolean) => listener(maximized);
			ipcRenderer.on("window:maximized", wrapped);
			return () => {
				ipcRenderer.off("window:maximized", wrapped);
			};
		},
		isFullScreen: () => ipcRenderer.invoke("window:isFullScreen") as Promise<boolean>,
		onFullScreen: (listener: (fullScreen: boolean) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, fullScreen: boolean) => listener(fullScreen);
			ipcRenderer.on("window:fullscreen", wrapped);
			return () => {
				ipcRenderer.off("window:fullscreen", wrapped);
			};
		},
	},
	theme: {
		// Propagate the app's theme preference to Electron's nativeTheme so embedded
		// WebContentsView previews (which follow prefers-color-scheme) stay in sync
		// with the shell. "system" lets both follow the OS.
		set: (preference: "light" | "dark" | "system") => ipcRenderer.invoke("theme:set", preference) as Promise<void>,
	},
	menu: {
		action: (action: string) => ipcRenderer.invoke("menu:action", action) as Promise<void>,
		notifyShellFocus: () => ipcRenderer.send("shell:focus"),
	},
	clipboard: {
		writeText: (text: string) => ipcRenderer.invoke("clipboard:writeText", text) as Promise<void>,
		readText: () => ipcRenderer.invoke("clipboard:readText") as Promise<string>,
	},
	daemon: {
		getStatus: () => ipcRenderer.invoke("daemon:getStatus") as Promise<DaemonStatus>,
		start: () => ipcRenderer.invoke("daemon:start") as Promise<DaemonStatus>,
		stop: () => ipcRenderer.invoke("daemon:stop") as Promise<DaemonStatus>,
		restart: () => ipcRenderer.invoke("daemon:restart") as Promise<DaemonStatus>,
		onStatus: (listener: (status: DaemonStatus) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, status: DaemonStatus) => listener(status);
			ipcRenderer.on("daemon:status", wrapped);
			return () => {
				ipcRenderer.off("daemon:status", wrapped);
			};
		},
	},
	telemetry: {
		getBootstrap: () => ipcRenderer.invoke("telemetry:getBootstrap") as Promise<TelemetryBootstrap | null>,
	},
	browser: {
		nativeCompositionEnabled: true,
		ensure: (sessionId: string) => ipcRenderer.invoke("browser:ensure", sessionId) as Promise<BrowserNavState>,
		setBounds: (input: BrowserBoundsInput) => ipcRenderer.send("browser:setBounds", input),
		setOverlayOpen: (open: boolean) => ipcRenderer.send("browser:overlay", open),
		navigate: (input: BrowserNavigateInput) =>
			ipcRenderer.invoke("browser:navigate", input) as Promise<BrowserNavState>,
		clear: (viewId: string) => ipcRenderer.invoke("browser:clear", viewId) as Promise<BrowserNavState>,
		goBack: (viewId: string) => ipcRenderer.invoke("browser:goBack", viewId) as Promise<BrowserNavState>,
		goForward: (viewId: string) => ipcRenderer.invoke("browser:goForward", viewId) as Promise<BrowserNavState>,
		reload: (viewId: string) => ipcRenderer.invoke("browser:reload", viewId) as Promise<BrowserNavState>,
		stop: (viewId: string) => ipcRenderer.invoke("browser:stop", viewId) as Promise<BrowserNavState>,
		getTabs: (viewId: string) => ipcRenderer.invoke("browser:getTabs", viewId) as Promise<BrowserTabsState>,
		selectTab: (input: { viewId: string; tabId: string }) =>
			ipcRenderer.invoke("browser:selectTab", input) as Promise<BrowserTabsState>,
		closeTab: (input: { viewId: string; tabId: string }) =>
			ipcRenderer.invoke("browser:closeTab", input) as Promise<BrowserTabsState>,
		openTab: (input: { viewId: string; url?: string }) =>
			ipcRenderer.invoke("browser:openTab", input) as Promise<BrowserTabsState>,
		devtools: (input: BrowserDevToolsInput) =>
			ipcRenderer.invoke("browser:devtools", input) as Promise<BrowserDevToolsState>,
		destroy: (viewId: string) => ipcRenderer.send("browser:destroy", viewId),
		setAnnotationMode: (input: BrowserAnnotationModeInput) =>
			ipcRenderer.invoke("browser:annotation:setMode", input) as Promise<void>,
		onNavState: (listener: (state: BrowserNavState) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, state: BrowserNavState) => listener(state);
			ipcRenderer.on("browser:navState", wrapped);
			return () => {
				ipcRenderer.off("browser:navState", wrapped);
			};
		},
		onTabsState: (listener: (state: BrowserTabsState) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, state: BrowserTabsState) => listener(state);
			ipcRenderer.on("browser:tabsState", wrapped);
			return () => {
				ipcRenderer.off("browser:tabsState", wrapped);
			};
		},
		onAgentActivity: (listener: (state: BrowserAgentActivityState) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, state: BrowserAgentActivityState) => listener(state);
			ipcRenderer.on("browser:agentActivity", wrapped);
			return () => {
				ipcRenderer.off("browser:agentActivity", wrapped);
			};
		},
		onDevToolsState: (listener: (state: BrowserDevToolsState) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, state: BrowserDevToolsState) => listener(state);
			ipcRenderer.on("browser:devtoolsState", wrapped);
			return () => {
				ipcRenderer.off("browser:devtoolsState", wrapped);
			};
		},
		onAnnotationSubmit: (listener: (payload: BrowserAnnotationSubmitPayload) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, payload: BrowserAnnotationSubmitPayload) => listener(payload);
			ipcRenderer.on("browser:annotation:submitted", wrapped);
			return () => {
				ipcRenderer.off("browser:annotation:submitted", wrapped);
			};
		},
		onAnnotationCancel: (listener: (payload: BrowserAnnotationCancelPayload) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, payload: BrowserAnnotationCancelPayload) => listener(payload);
			ipcRenderer.on("browser:annotation:canceled", wrapped);
			return () => {
				ipcRenderer.off("browser:annotation:canceled", wrapped);
			};
		},
	},
	notifications: {
		show: (notification: { id: string; title: string; body?: string; type?: string }) =>
			ipcRenderer.invoke("notifications:show", notification) as Promise<void>,
		setBadge: (count: number) => ipcRenderer.invoke("notifications:setBadge", count) as Promise<void>,
		devBounce: () => ipcRenderer.invoke("notifications:devBounce") as Promise<void>,
		onClick: (listener: (id: string) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, id: string) => listener(id);
			ipcRenderer.on("notifications:click", wrapped);
			return () => {
				ipcRenderer.off("notifications:click", wrapped);
			};
		},
	},
	tray: {
		setAttentionState: (state: TrayAttentionState) => ipcRenderer.send(TRAY_SET_ATTENTION_STATE_CHANNEL, state),
			onOpenSession: (listener: (target: TrayOpenSessionTarget) => void) => {
				const wrapped = (_event: Electron.IpcRendererEvent, target: TrayOpenSessionTarget) => listener(target);
				ipcRenderer.on(TRAY_OPEN_SESSION_CHANNEL, wrapped);
				ipcRenderer.send(TRAY_RENDERER_READY_CHANNEL);
			return () => {
				ipcRenderer.off(TRAY_OPEN_SESSION_CHANNEL, wrapped);
			};
		},
	},
	appState: {
		getMigration: () => ipcRenderer.invoke("appState:getMigration") as Promise<MigrationState>,
		setMigration: (migration: MigrationState) =>
			ipcRenderer.invoke("appState:setMigration", migration) as Promise<void>,
	},
	updateSettings: {
		get: () => ipcRenderer.invoke("updateSettings:get") as Promise<UpdateSettings>,
		set: (settings: UpdateSettings) => ipcRenderer.invoke("updateSettings:set", settings) as Promise<void>,
	},
	uiSettings: {
		get: () => ipcRenderer.invoke("uiSettings:get") as Promise<UiSettings>,
		set: (settings: UiSettings) => ipcRenderer.invoke("uiSettings:set", settings) as Promise<UiSettings>,
	},
	keybindings: {
		get: () => ipcRenderer.invoke("keybindings:get") as Promise<KeybindingOverrides>,
		set: (overrides: KeybindingOverrides) =>
			ipcRenderer.invoke("keybindings:set", overrides) as Promise<KeybindingOverrides>,
		setRecording: (active: boolean) => ipcRenderer.invoke("keybindings:setRecording", active) as Promise<void>,
	},
	updates: {
		getStatus: () => ipcRenderer.invoke("updates:getStatus") as Promise<UpdateStatus>,
		check: (options?: UpdateCheckOptions) => ipcRenderer.invoke("updates:check", options) as Promise<void>,
		returnHome: (requestId?: string) => ipcRenderer.invoke("updates:returnHome", requestId) as Promise<void>,
		download: (requestId?: string) => ipcRenderer.invoke("updates:download", requestId) as Promise<void>,
		install: () => ipcRenderer.invoke("updates:install") as Promise<void>,
		onStatus: (listener: (status: UpdateStatus) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, status: UpdateStatus) => listener(status);
			ipcRenderer.on("updates:status", wrapped);
			return () => {
				ipcRenderer.off("updates:status", wrapped);
			};
		},
		// Separate from onStatus: the main process suppresses the *status* for
		// automatic failures but still reports the outcome here.
		onTelemetry: (listener: (outcome: UpdateOutcome) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, outcome: UpdateOutcome) => listener(outcome);
			ipcRenderer.on("updates:telemetry", wrapped);
			return () => {
				ipcRenderer.off("updates:telemetry", wrapped);
			};
		},
	},
	featureBuilds: {
		list: () => ipcRenderer.invoke("featureBuilds:list") as Promise<FeatureBuild[]>,
		getActive: () => ipcRenderer.invoke("featureBuilds:getActive") as Promise<{ pr: number } | null>,
	},
	cloud: {
		getSession: () => ipcRenderer.invoke("cloud:getSession") as Promise<CloudAccount | null>,
		signIn: () => ipcRenderer.invoke("cloud:signIn") as Promise<void>,
		signOut: () => ipcRenderer.invoke("cloud:signOut") as Promise<void>,
		onSessionChanged: (listener: (account: CloudAccount | null) => void) => {
			const wrapped = (_event: Electron.IpcRendererEvent, account: CloudAccount | null) => listener(account);
			ipcRenderer.on("cloud:sessionChanged", wrapped);
			return () => {
				ipcRenderer.off("cloud:sessionChanged", wrapped);
			};
		},
	},
};

contextBridge.exposeInMainWorld("ao", api);

export type AoBridge = typeof api;
