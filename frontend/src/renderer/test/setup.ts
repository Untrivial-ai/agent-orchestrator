import "@testing-library/jest-dom/vitest";
import * as jestDomMatchers from "@testing-library/jest-dom/matchers";
import { expect } from "vitest";
import "../i18n";
import { coerceUiSettings, DEFAULT_UI_SETTINGS } from "../../shared/ui-locale";

// Vitest 4 can load the convenience entry against a different matcher
// instance. Register the matchers on the active test runtime as well.
expect.extend(jestDomMatchers);

// Guard: src/main/** tests run in the Node.js environment (no DOM). vitest still
// routes setupFiles here, so only install the DOM stubs when a DOM exists.
// ponytail: single guard; node env has no DOM to stub.
if (typeof window !== "undefined") {
	const emptyRect = () => ({
		bottom: 0,
		height: 0,
		left: 0,
		right: 0,
		top: 0,
		width: 0,
		x: 0,
		y: 0,
		toJSON: () => ({}),
	});

	// JSDOM does not implement the selection geometry rich text editors use to
	// keep the caret visible. Lexical only reads these values; zero geometry is
	// sufficient for component tests.
	if (!Range.prototype.getBoundingClientRect) {
		Range.prototype.getBoundingClientRect = emptyRect;
	}
	if (!Range.prototype.getClientRects) {
		Range.prototype.getClientRects = () => [] as unknown as DOMRectList;
	}
	if (!(Text.prototype as Text & { getBoundingClientRect?: () => DOMRect }).getBoundingClientRect) {
		Object.defineProperty(Text.prototype, "getBoundingClientRect", {
			configurable: true,
			value: emptyRect,
		});
	}
	if (typeof globalThis.ClipboardEvent === "undefined") {
		Object.defineProperty(globalThis, "ClipboardEvent", {
			configurable: true,
			value: class ClipboardEventStub extends Event {},
		});
	}

	class ResizeObserverStub {
		observe() {}
		unobserve() {}
		disconnect() {}
	}

	Object.defineProperty(window, "ResizeObserver", {
		configurable: true,
		writable: true,
		value: ResizeObserverStub,
	});

	Object.defineProperty(window, "matchMedia", {
		configurable: true,
		writable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			onchange: null,
			addEventListener: () => undefined,
			removeEventListener: () => undefined,
			addListener: () => undefined,
			removeListener: () => undefined,
			dispatchEvent: () => false,
		}),
	});

	const localStorageStub = (() => {
		const values = new Map<string, string>();
		return {
			clear: () => values.clear(),
			getItem: (key: string) => values.get(key) ?? null,
			removeItem: (key: string) => values.delete(key),
			setItem: (key: string, value: string) => values.set(key, value),
		};
	})();

	Object.defineProperty(window, "localStorage", {
		configurable: true,
		writable: true,
		value: localStorageStub,
	});

	HTMLCanvasElement.prototype.getContext = (() => ({})) as unknown as typeof HTMLCanvasElement.prototype.getContext;

	Element.prototype.hasPointerCapture = (() => false) as typeof Element.prototype.hasPointerCapture;
	Element.prototype.setPointerCapture = (() => undefined) as typeof Element.prototype.setPointerCapture;
	Element.prototype.releasePointerCapture = (() => undefined) as typeof Element.prototype.releasePointerCapture;
	Element.prototype.scrollIntoView = (() => undefined) as typeof Element.prototype.scrollIntoView;

	window.ao = {
		app: {
			getVersion: async () => "0.0.0-test",
			chooseDirectory: async () => null,
			checkGitRepository: async () => true,
			openExternal: async () => undefined,
			scanImportFolder: async ({ path }: { path: string }) => ({ path, repos: [] }),
			checkAncestorRepo: async () => undefined,
			getRepositoryBranch: async () => undefined,
			getGitHubLogin: async (_repoPath?: string) => "",
			getCachedGitHubOwners: async () => [],
			refreshGitHubOwners: async () => [],
			checkGitHubRepositoryAvailability: async () => ({ available: true }),
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
			writeText: async () => undefined,
			readText: async () => "",
		},
		daemon: {
			getStatus: async () => ({ state: "stopped" }),
			start: async () => ({ state: "starting" }),
			stop: async () => ({ state: "stopped" }),
			restart: async () => ({ state: "starting" }),
			onStatus: () => () => undefined,
		},
		editorHandoff: {
			getState: async () => ({
				targets: [
					{ id: "cursor", name: "Cursor", kind: "editor" },
					{ id: "file-manager", name: "Finder", kind: "file_manager" },
					{ id: "terminal", name: "Terminal", kind: "terminal" },
				],
				preferredEditorId: "cursor",
				workspaceAvailable: true,
			}),
			open: async ({ targetId }) => {
				if (targetId === "file-manager") return { id: "file-manager", name: "Finder", kind: "file_manager" };
				if (targetId === "terminal") return { id: "terminal", name: "Terminal", kind: "terminal" };
				return { id: targetId ?? "cursor", name: "Cursor", kind: "editor" };
			},
		},
		telemetry: {
			getBootstrap: async () => null,
			getPolicy: async () => ({ eventsEnabled: false, consentGeneration: "test", updatedAt: new Date(0).toISOString(), acknowledged: false, state: "applied", environmentVeto: true, durabilitySupported: false }),
			setEventsEnabled: async () => ({ eventsEnabled: false, consentGeneration: "test", updatedAt: new Date(0).toISOString(), acknowledged: false, state: "applied", environmentVeto: true, durabilitySupported: false }),
			onPolicy: () => () => false,
			onClearQueues: () => () => false,
			capture: async () => false,
			signalAgentSwitchVisibility: () => false,
		},
		browser: {
			nativeCompositionEnabled: true,
			ensure: async (sessionId: string) => ({
				viewId: `test:${sessionId}`,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
			setBounds: () => undefined,
			setOverlayOpen: () => undefined,
			navigate: async ({ viewId }: { viewId: string }) => ({
				viewId,
				url: "",
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
			captureScreenshot: async () => undefined,
			downloads: {
				list: async () => ({ downloads: [] }),
				action: async () => ({ downloads: [] }),
				clear: async () => ({ downloads: [] }),
				onChanged: () => () => { /* no-op test subscription */ },
			},
			getTabs: async (viewId: string) => ({ viewId, activeTabId: "t1", tabs: [] }),
			selectTab: async ({ viewId, tabId }) => ({ viewId, activeTabId: tabId, tabs: [] }),
			closeTab: async ({ viewId }) => ({ viewId, activeTabId: "", tabs: [] }),
			openTab: async ({ viewId }) => ({ viewId, activeTabId: "", tabs: [] }),
			getProfile: async (viewId: string) => ({ viewId, profileId: null, temporary: true }),
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
				return { id: `test-${name}`, name, createdAt: now, updatedAt: now };
			},
			rename: async ({ id, name }: { id: string; name: string }) => {
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
			get: async () => ({ enabled: false, channel: "latest", nightlyAck: false, feature: null, macDifferentialUpdates: false }),
			set: async () => undefined,
			setMacDifferentialUpdates: async () => undefined,
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
				throw new Error("not available in tests");
			},
			localLogin: async () => {
				throw new Error("not available in tests");
			},
			onSessionChanged: () => () => undefined,
		},
		cloudCp: {
			request: async () => ({ status: 401, headers: {}, body: "" }),
			openStream: async () => ({ streamId: "stream_test" }),
			closeStream: () => undefined,
			onStreamEvent: () => () => undefined,
		},
	};
} // end if (typeof window !== "undefined")
