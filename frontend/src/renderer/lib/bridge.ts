import type { AoBridge } from "../../preload";
import { coerceUiSettings, DEFAULT_UI_SETTINGS } from "../../shared/ui-locale";
export type { FeatureBuild } from "../../main/feature-builds";


export const aoBridge: AoBridge =
	window.ao ??
	({
		app: {
			getVersion: async () => "0.0.0-preview",
			chooseDirectory: async () => null,
			checkGitRepository: async () => true,
			openExternal: async (url: string) => {
				window.open(url, "_blank", "noopener,noreferrer");
			},
			scanImportFolder: async ({ path }) => ({ path, repos: [] }),
			checkAncestorRepo: async () => undefined,
			getRepositoryBranch: async () => undefined,
			getGitHubLogin: async () => "",
			getPathForFile: () => "",
			onOpenFolderPath: () => () => undefined,
			onNewSessionShortcut: () => () => undefined,
			onKeyboardShortcutsHelp: () => () => undefined,
			onNewShellTerminalShortcut: () => () => undefined,
			onCloseShellTerminalShortcut: () => () => undefined,
			setCloseShellTerminalShortcutEnabled: () => undefined,
			onOpenSettingsShortcut: () => () => undefined,
			onPreviousSessionShortcut: () => () => undefined,
			onNextSessionShortcut: () => () => undefined,
			onPreviousTabShortcut: () => () => undefined,
			onNextTabShortcut: () => () => undefined,
			onFocusTerminalShortcut: () => () => undefined,
		},
		terminal: {
			saveDroppedFile: async () => "",
			setFocused: () => undefined,
			onFontSizeShortcut: () => () => undefined,
		},
		window: {
			isMaximized: async () => false,
			onMaximized: () => () => undefined,
			isFullScreen: async () => false,
			onFullScreen: () => () => undefined,
		},
		theme: {
			set: async () => undefined,
			persistTerminal: async () => undefined,
		},
		menu: {
			action: async () => undefined,
			notifyShellFocus: () => undefined,
		},
		clipboard: {
			writeText: async (text: string) => {
				if (navigator.clipboard?.writeText) {
					await navigator.clipboard.writeText(text);
				}
			},
			readText: async () => (navigator.clipboard?.readText ? navigator.clipboard.readText() : ""),
		},
		daemon: {
			getStatus: async () => ({
				state: "stopped",
				message: "Electron preload is not available in browser preview.",
			}),
			start: async () => ({ state: "starting" }),
			stop: async () => ({ state: "stopped" }),
			restart: async () => ({ state: "starting" }),
			onStatus: () => () => undefined,
		},
		editorHandoff: {
			getState: async () => ({
				targets: [],
				preferredEditorId: "cursor",
				workspaceAvailable: false,
				unavailableReason: "Desktop app is required to open a workspace.",
			}),
			open: async () => {
				throw new Error("Desktop app is required to open a workspace.");
			},
		},
		telemetry: {
			getBootstrap: async () => null,
			getPolicy: async () => ({ eventsEnabled: false, consentGeneration: "preview", updatedAt: new Date(0).toISOString(), acknowledged: false, state: "applied", environmentVeto: true, durabilitySupported: false, reason: "environment_veto" }),
			setEventsEnabled: async () => ({ eventsEnabled: false, consentGeneration: "preview", updatedAt: new Date(0).toISOString(), acknowledged: false, state: "applied", environmentVeto: true, durabilitySupported: false, reason: "environment_veto" }),
			onPolicy: () => () => false,
			onClearQueues: () => () => false,
			capture: async () => false,
			signalAgentSwitchVisibility: () => false,
		},
		browser: {
			nativeCompositionEnabled: false,
			ensure: async (sessionId: string) => ({
				viewId: `preview:${sessionId}`,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			setBounds: () => undefined,
			setOverlayOpen: () => undefined,
			navigate: async ({ viewId, url }) => ({
				viewId,
				url,
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			historySuggestions: async () => [],
			clear: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			goBack: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			goForward: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			reload: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			stop: async (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			getTabs: async (viewId: string) => ({ viewId, activeTabId: "t1", tabs: [] }),
			selectTab: async ({ viewId, tabId }) => ({ viewId, activeTabId: tabId, tabs: [] }),
			closeTab: async ({ viewId }) => ({ viewId, activeTabId: "", tabs: [] }),
			openTab: async ({ viewId }) => ({ viewId, activeTabId: "", tabs: [] }),
			getProfile: async (viewId) => ({ viewId, profileId: null, temporary: true }),
			showProfileMenu: async () => undefined,
			selectProfile: async () => undefined,
			notifyPanelUsed: () => undefined,
			notifyPanelBlur: () => undefined,
			onFocusLocation: () => () => undefined,
			onReopenClosedTab: () => () => undefined,
			devtools: async ({ viewId, operation }) => ({
				viewId,
				open: operation !== "close",
				activeTabId: "",
			}),
			destroy: () => undefined,
			setAnnotationMode: async () => undefined,
			onNavState: () => () => undefined,
			onPageFocus: () => () => undefined,
			onTabsState: () => () => undefined,
			onAgentActivity: () => () => undefined,
			onDevToolsState: () => () => undefined,
			onProfileState: () => () => undefined,
			onProfileManage: () => () => undefined,
			onAnnotationSubmit: () => () => undefined,
			onAnnotationCancel: () => () => undefined,
		},
		browserProfiles: {
			list: async () => ({ profiles: [] }),
			create: async (name: string) => {
				const now = new Date().toISOString();
				return { id: `preview-${name}`, name, createdAt: now, updatedAt: now };
			},
			rename: async ({ id, name }) => {
				const now = new Date().toISOString();
				return { id, name, createdAt: now, updatedAt: now };
			},
			clear: async () => undefined,
			delete: async () => undefined,
			discoverImportSources: async () => ({ sources: [] }),
			import: async () => ({ sourceName: "", entries: [] }),
			onImportProgress: () => () => undefined,
		},
		notifications: {
			show: async () => undefined,
			setBadge: async () => undefined,
			devBounce: async () => undefined,
			onClick: () => () => undefined,
		},
		tray: {
			setAttentionState: () => undefined,
			onOpenSession: () => () => undefined,
		},
		appState: {
			getMigration: async () => ({ status: "pending" }),
			setMigration: async () => undefined,
		},
		updateSettings: {
			get: async () => ({ enabled: false, channel: "latest", nightlyAck: false, feature: null }),
			set: async () => undefined,
		},
		uiSettings: {
			get: async () => ({ ...DEFAULT_UI_SETTINGS }),
			set: async (settings) => coerceUiSettings({ ...DEFAULT_UI_SETTINGS, ...settings }),
		},
		keybindings: {
			get: async () => ({}),
			set: async (overrides) => overrides,
			setRecording: async () => undefined,
		},
		updates: {
			getStatus: async () => ({ state: "idle" }),
			check: async () => undefined,
			returnHome: async () => undefined,
			download: async () => undefined,
			install: async () => undefined,
			onStatus: () => () => undefined,
			onTelemetry: () => () => undefined,
		},
		featureBuilds: {
			list: async () => [],
			getActive: async () => null,
		},
		cloud: {
			getSession: async () => null,
			signIn: async () => undefined,
			signOut: async () => undefined,
			localAuthAvailable: async () => false,
			localRegister: async () => {
				throw new Error("AO Cloud sign-in requires the desktop app.");
			},
			localLogin: async () => {
				throw new Error("AO Cloud sign-in requires the desktop app.");
			},
			onSessionChanged: () => () => undefined,
		},
		cloudCp: {
			request: async () => {
				throw new Error("AO Cloud requests require the desktop app.");
			},
			openStream: async () => {
				throw new Error("AO Cloud event streams require the desktop app.");
			},
			closeStream: () => undefined,
			onStreamEvent: () => () => undefined,
		},
	} satisfies AoBridge);
