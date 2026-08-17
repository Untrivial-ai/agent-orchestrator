import type {
	IpcMain,
	IpcMainEvent,
	IpcMainInvokeEvent,
	Rectangle,
	Session,
	View,
	WebContents,
	OpenDevToolsOptions,
} from "electron";
import { nativeImage } from "electron";
import { randomUUID } from "node:crypto";
import type {
	BrowserAnnotationCancelPayload,
	BrowserAnnotationContext,
	BrowserAnnotationModeInput,
	BrowserAnnotationPageCancelPayload,
	BrowserAnnotationPageSubmitPayload,
	BrowserAnnotationSelection,
	BrowserAnnotationSnapshot,
	BrowserAnnotationSubmitPayload,
} from "../shared/browser-annotations";
import { attachAppShortcuts } from "./app-shortcuts";
import { MAX_BROWSER_TABS } from "../shared/browser-tabs";
import type { KeybindingOverrides } from "../shared/shortcuts";
import type { AgentBrowserRuntime } from "./agent-browser-runtime";
import type { AgentBrowserTarget, AgentBrowserTargetProvider } from "./agent-browser-cdp-bridge";

function isValidAnnotationContext(value: unknown): value is BrowserAnnotationContext {
	if (typeof value !== "object" || value === null) return false;
	const context = value as {
		url?: unknown;
		tag?: unknown;
		classes?: unknown;
		selector?: unknown;
		size?: unknown;
		computedStyle?: unknown;
	};
	if (typeof context.url !== "string") return false;
	if (typeof context.tag !== "string") return false;
	if (!Array.isArray(context.classes)) return false;
	if (typeof context.selector !== "string") return false;
	if (typeof context.size !== "object" || context.size === null) return false;
	const size = context.size as { width?: unknown; height?: unknown };
	if (typeof size.width !== "number" || typeof size.height !== "number") return false;
	if (typeof context.computedStyle !== "object" || context.computedStyle === null) return false;
	return true;
}

function isValidAnnotationSelection(value: unknown): value is BrowserAnnotationSelection {
	if (typeof value !== "object" || value === null) return false;
	const selection = value as { kind?: unknown; context?: unknown; contexts?: unknown };
	if (selection.kind === "element") {
		return isValidAnnotationContext(selection.context);
	}
	if (selection.kind === "elements") {
		return (
			Array.isArray(selection.contexts) &&
			selection.contexts.length > 0 &&
			selection.contexts.every(isValidAnnotationContext)
		);
	}
	return false;
}

export type BrowserRect = Pick<Rectangle, "x" | "y" | "width" | "height">;

export type BrowserNavState = {
	viewId: string;
	url: string;
	title: string;
	canGoBack: boolean;
	canGoForward: boolean;
	isLoading: boolean;
	error?: string;
};

export type BrowserTabState = {
	id: string;
	url: string;
	title: string;
	active: boolean;
	favicon?: string;
};

export type BrowserTabsState = {
	viewId: string;
	activeTabId: string;
	tabs: BrowserTabState[];
	change?: {
		kind: "opened" | "popup" | "selected" | "closed";
		tabId: string;
	};
};

export type BrowserAgentActivityState = {
	viewId: string;
	active: boolean;
	action: string;
	phase?: "started" | "finished";
	commandId?: string;
};

export type BrowserDevToolsState = {
	viewId: string;
	open: boolean;
	activeTabId: string;
	placement?: BrowserDevToolsPlacement;
};

export type BrowserDevToolsPlacement = "right" | "bottom" | "left" | "undocked";

export type BrowserDevToolsInput = {
	viewId: string;
	operation: "open" | "close" | "setPlacement";
	placement?: BrowserDevToolsPlacement;
};

type InternalBrowserDevToolsOperation = BrowserDevToolsInput["operation"] | "toggle";

type BrowserBoundsInput = {
	viewId: string;
	rect: BrowserRect;
	visible: boolean;
};

type BrowserNavigateInput = {
	viewId: string;
	url: string;
};

type BrowserTabInput = {
	viewId: string;
	tabId: string;
};

type BrowserOpenTabInput = {
	viewId: string;
	url?: string;
};

type BrowserWebContents = Pick<
	WebContents,
	| "id"
	| "canGoBack"
	| "canGoForward"
	| "capturePage"
	| "clearHistory"
	| "debugger"
	| "executeJavaScript"
	| "focus"
	| "mainFrame"
	| "getTitle"
	| "getURL"
	| "goBack"
	| "goForward"
	| "isLoading"
	| "loadURL"
	| "on"
	| "reload"
	| "send"
	| "setWindowOpenHandler"
	| "stop"
> & {
	openDevTools?: (options?: Pick<OpenDevToolsOptions, "mode" | "activate">) => void;
	closeDevTools?: () => void;
	close?: () => void;
	session?: Pick<Session, "setPermissionCheckHandler" | "setPermissionRequestHandler">;
};

type BrowserViewLike = View & {
	webContents: BrowserWebContents;
	setBounds: (bounds: BrowserRect) => void;
	setBorderRadius?: (radius: number) => void;
	setVisible?: (visible: boolean) => void;
};

type BrowserWindowLike = {
	contentView: {
		addChildView: (view: BrowserViewLike) => void;
		removeChildView?: (view: BrowserViewLike) => void;
	};
	getContentBounds: () => BrowserRect;
	webContents?: WebContents;
	isDestroyed?: () => boolean;
};

type ShellLike = {
	openExternal: (url: string) => Promise<void>;
};

type WebContentsViewConstructor = new (options: { webPreferences: Electron.WebPreferences }) => BrowserViewLike;

export type BrowserViewHostOptions = {
	mainWindow: BrowserWindowLike;
	shellWebContents?: WebContents;
	ipcMain: Pick<IpcMain, "handle" | "on" | "removeHandler" | "off">;
	shell: ShellLike;
	WebContentsView: WebContentsViewConstructor;
	annotatePreloadPath: string;
	rendererOrigin: string;
	// Platform flag for application shortcuts forwarded from each preview view
	// to the shell. Defaults to non-mac when omitted (tests).
	isMac?: boolean;
	getKeybindingOverrides?: () => KeybindingOverrides;
	isKeybindingRecording?: () => boolean;
	agentBrowserRuntime?: AgentBrowserRuntime;
	isCloseShellTerminalShortcutEnabled?: () => boolean;
};

export type BrowserViewHost = {
	dispose: () => Promise<void>;
	destroy: (viewId: string) => void;
	destroyAll: () => void;
	execute: (sessionId: string, action: string, args?: Record<string, unknown>, signal?: AbortSignal) => Promise<unknown>;
	// webContents of the most recently focused browser panel (or null); the titlebar menu targets it for Edit/Reload/Zoom/DevTools.
	getLastFocusedPanelContents: () => WebContents | null;
	/** Toggle Chromium DevTools for the last focused AO browser panel. */
	toggleDevToolsForLastFocused: () => Promise<BrowserDevToolsState | null>;
	// Drop the remembered panel; call when the shell gains focus for a real reason so a stale panel stops absorbing menu actions.
	forgetLastFocusedPanel: () => void;
};

type BrowserEntry = {
	sessionId: string;
	tabId: string;
	view: BrowserViewLike;
	ready: Promise<void>;
	state: BrowserNavState;
	annotationEnabled: boolean;
	networkCapture?: BrowserNetworkCapture;
	favicon?: string;
	// URL of the favicon currently applied to `favicon` (fetch succeeded).
	faviconSourceUrl?: string;
	// URL of a favicon fetch currently in flight, so a duplicate event for the
	// same URL doesn't start a second fetch while one is already pending.
	faviconPendingUrl?: string;
	// Origin `favicon` was captured for, so a same-tab navigation to a new
	// origin can drop the (now-stale) favicon immediately instead of leaving
	// the previous site's icon showing until the new one finishes loading.
	faviconOrigin?: string;
};

type BrowserSessionEntry = {
	sessionId: string;
	viewId: string;
	profilePartition: string;
	tabs: Map<string, BrowserEntry>;
	activeTabId: string;
	nextTabNumber: number;
	bounds: BrowserRect;
	rendererBounds: BrowserRect;
	zoomFactor: number;
	visible: boolean;
	networkTabId?: string;
	agentBrowserCommands: number;
	nativeActiveTabId?: string;
	nativeOperationQueue: Promise<void>;
	devtoolsPlacement: BrowserDevToolsPlacement;
	devtools?: {
		contents: BrowserWebContents;
		placement: BrowserDevToolsPlacement;
		nativeCloseForReopen?: boolean;
		targetTabId: string;
		desiredTabId: string;
		retargetGeneration: number;
		retargetQueue: Promise<void>;
		revealRequested: boolean;
	};
};

type BrowserLogEntry = {
	level: string;
	message: string;
	source?: string;
	line?: number;
	timestamp: string;
};

type BrowserNetworkRequest = {
	id: string;
	method: string;
	url: string;
	resourceType?: string;
	startedAt: string;
	status?: number;
	statusText?: string;
	mimeType?: string;
	durationMs?: number;
	failed?: boolean;
	canceled?: boolean;
	errorText?: string;
	fromCache?: boolean;
	fromServiceWorker?: boolean;
	redirectedTo?: string;
	requestHeaders?: Record<string, string>;
	responseHeaders?: Record<string, string>;
};

type InternalBrowserNetworkRequest = BrowserNetworkRequest & {
	protocolRequestId: string;
	startedMonotonic?: number;
};

type BrowserNetworkCapture = {
	active: boolean;
	tabId: string;
	startedAt: string;
	expiresAt: string;
	stoppedAt?: string;
	stopReason?: string;
	maxEntries: number;
	nextSequence: number;
	requests: InternalBrowserNetworkRequest[];
	byRequestId: Map<string, InternalBrowserNetworkRequest>;
	timer?: ReturnType<typeof setTimeout>;
};

// Hidden targets still need a real viewport for screenshots, responsive
// layout, scrolling, and pointer automation before the panel is first shown.
const OFFSCREEN_BOUNDS: BrowserRect = { x: -10_000, y: -10_000, width: 1280, height: 720 };
// Must match `--radius-lg` (tokens.css, 0.625rem = 10px) — `.browser-panel`'s own
// `rounded-lg` corner. The native view isn't a DOM node, so CSS never clips it;
// this is the only thing rounding its corners. A mismatch here leaves a sliver of
// the page's own background peeking past the DOM panel's rounded corner curve.
const BROWSER_VIEW_BORDER_RADIUS = 10;
const DEFAULT_NETWORK_CAPTURE_SECONDS = 60;
const MAX_NETWORK_CAPTURE_SECONDS = 300;
const MAX_NETWORK_REQUESTS = 200;
const FAVICON_SIZE = 32;
const MAX_FAVICON_BYTES = 256 * 1024;
const DEFAULT_NATIVE_DEVTOOLS_PLACEMENT: BrowserDevToolsPlacement = "right";
const MAX_EXTERNAL_TEXT_BYTES = 1 << 20;
// Annotation submit must never feel laggy: capture is best-effort and bounded
// so a slow/hung capturePage() can't delay the send past this ceiling.
const ANNOTATION_SNAPSHOT_TIMEOUT_MS = 200;
// Caps the longest edge so the encoded image stays small and matches Claude
// vision's effective resolution — larger just costs more tokens for no gain.
const ANNOTATION_SNAPSHOT_MAX_DIMENSION = 1568;
const UNTRUSTED_BEGIN = "<<<BEGIN UNTRUSTED EXTERNAL CONTENT>>>";
const UNTRUSTED_END = "<<<END UNTRUSTED EXTERNAL CONTENT>>>";
// Browser targets are shared with session automation after navigation. Keep
// local files out of this surface even when navigation starts in the human
// address bar; workspace files arrive through the daemon's confined HTTP
// preview origin instead.
const ALLOWED_PROTOCOLS = new Set(["http:", "https:"]);
export function normalizeBrowserURL(input: string): URL {
	const raw = input.trim();
	if (raw === "") {
		throw new Error("URL is required");
	}
	const candidate = withDefaultScheme(raw);
	const url = new URL(candidate);
	if (!ALLOWED_PROTOCOLS.has(url.protocol)) {
		throw new Error(`Unsupported browser URL scheme: ${url.protocol}`);
	}
	return url;
}

export function isAllowedBrowserURL(input: string, rendererOrigin?: string): boolean {
	try {
		const url = normalizeBrowserURL(input);
		if (rendererOrigin && url.origin === rendererOrigin) return false;
		return true;
	} catch {
		return false;
	}
}

export function clampBoundsToWindow(
	rect: BrowserRect,
	windowBounds: Pick<BrowserRect, "width" | "height">,
): BrowserRect {
	const rounded = {
		x: Math.round(rect.x),
		y: Math.round(rect.y),
		width: Math.max(0, Math.round(rect.width)),
		height: Math.max(0, Math.round(rect.height)),
	};
	const maxX = Math.max(0, Math.round(windowBounds.width));
	const maxY = Math.max(0, Math.round(windowBounds.height));
	const x = Math.min(Math.max(rounded.x, 0), maxX);
	const y = Math.min(Math.max(rounded.y, 0), maxY);
	return {
		x,
		y,
		width: Math.min(rounded.width, Math.max(0, maxX - x)),
		height: Math.min(rounded.height, Math.max(0, maxY - y)),
	};
}

export function scaleBoundsForZoom(rect: BrowserRect, zoomFactor: number): BrowserRect {
	if (!Number.isFinite(zoomFactor) || zoomFactor <= 0 || zoomFactor === 1) return rect;
	return {
		x: rect.x * zoomFactor,
		y: rect.y * zoomFactor,
		width: rect.width * zoomFactor,
		height: rect.height * zoomFactor,
	};
}

export function createBrowserViewHost(options: BrowserViewHostOptions): BrowserViewHost {
	const entries = new Map<string, BrowserSessionEntry>();
	const shellWebContents = options.shellWebContents ?? options.mainWindow.webContents;
	if (!shellWebContents) throw new Error("Browser view host requires shell WebContents");
	const viewIdsBySessionId = new Map<string, string>();
	const rendererOwnersByViewId = new Map<string, Set<number>>();
	const tabsByWebContentsId = new Map<number, BrowserEntry>();
	const ipcDisposers: Array<() => void> = [];
	let disposePromise: Promise<void> | null = null;
	// viewId of the panel that most recently held focus; cleared when it is hidden or destroyed.
	let lastFocusedViewId: string | null = null;
	const forgetIfFocused = (viewId: string): void => {
		if (lastFocusedViewId === viewId) lastFocusedViewId = null;
	};
	const setAgentBrowserActivity = (
		session: BrowserSessionEntry,
		action: string,
		active: boolean,
		commandId?: string,
		phase?: BrowserAgentActivityState["phase"],
	): void => {
		session.agentBrowserCommands = Math.max(0, session.agentBrowserCommands + (active ? 1 : -1));
		shellWebContents.send("browser:agentActivity", {
			viewId: session.viewId,
			active: session.agentBrowserCommands > 0,
			action,
			...(phase ? { phase } : {}),
			...(commandId ? { commandId } : {}),
		} satisfies BrowserAgentActivityState);
	};
	const applyBrowserViewBounds = (view: BrowserViewLike, bounds: BrowserRect, visible?: boolean): void => {
		view.setBounds(bounds);
		if (visible !== undefined) view.setVisible?.(visible);
		view.setBorderRadius?.(BROWSER_VIEW_BORDER_RADIUS);
	};
	const pushDevToolsState = (session: BrowserSessionEntry): BrowserDevToolsState => {
		const state: BrowserDevToolsState = {
			viewId: session.viewId,
			open: Boolean(session.devtools),
			activeTabId: session.activeTabId,
			placement: session.devtools?.placement ?? session.devtoolsPlacement,
		};
		shellWebContents.send("browser:devtoolsState", state);
		return state;
	};

	const destroyDevTools = (session: BrowserSessionEntry): void => {
		const devtools = session.devtools;
		if (!devtools) return;
		session.devtools = undefined;
		try {
			devtools.contents.closeDevTools?.();
		} catch {
			// Chromium may already have torn down the native DevTools surface.
		}
		pushDevToolsState(session);
	};

	const createTab = (session: BrowserSessionEntry, activate: boolean, syncNativeOnActivate = false): BrowserEntry => {
		if (session.tabs.size >= MAX_BROWSER_TABS) {
			throw browserError("BROWSER_TAB_LIMIT", `Browser tab limit of ${MAX_BROWSER_TABS} reached`);
		}
		const view = new options.WebContentsView({
			webPreferences: {
				contextIsolation: true,
				nodeIntegration: false,
				partition: session.profilePartition,
				preload: options.annotatePreloadPath,
				sandbox: true,
			},
		});
		applyBrowserViewBounds(view, OFFSCREEN_BOUNDS, false);
		options.mainWindow.contentView.addChildView(view);
		view.setBorderRadius?.(BROWSER_VIEW_BORDER_RADIUS);
		view.webContents.session?.setPermissionCheckHandler?.(() => false);
		view.webContents.session?.setPermissionRequestHandler?.((_contents, _permission, callback) => callback(false));

		const tabId = `t${session.nextTabNumber++}`;
		const state: BrowserNavState = emptyNavState(session.viewId);
		const entry: BrowserEntry = {
			sessionId: session.sessionId,
			tabId,
			view,
			ready: Promise.resolve(),
			state,
			annotationEnabled: false,
		};
		session.tabs.set(tabId, entry);
		tabsByWebContentsId.set(view.webContents.id, entry);
		// Native Chromium DevTools can be closed from its own window controls. Keep
		// the renderer's toggle state in sync with that user action. Programmatic
		// close/reopen cycles used for retargeting or placement changes are marked
		// and ignored so they do not look like a user close.
		view.webContents.on("devtools-closed", () => {
			const devtools = session.devtools;
			if (!devtools || devtools.contents !== view.webContents) return;
			if (devtools.nativeCloseForReopen) {
				devtools.nativeCloseForReopen = false;
				return;
			}
			session.devtools = undefined;
			pushDevToolsState(session);
		});
		hardenWebContents(view.webContents, options, entry, () => {
			const popup = createTab(session, true, true);
			pushTabsState(options, session, { kind: "popup", tabId: popup.tabId });
			return popup.view.webContents;
		}, () => session.tabs.size < MAX_BROWSER_TABS);
		wireNavEvents(
			view.webContents,
			options,
			entry,
			() => entries.get(session.viewId)?.activeTabId === entry.tabId,
			() => applySessionBounds(session, entry),
			() => pushTabsState(options, session),
		);
		wireFaviconEvents(view.webContents, entry, () => pushTabsState(options, session));
		wireAutomationEvents(view.webContents, entry);
		// The preview is a separate WebContentsView, so renderer-window keydown
		// listeners never see keys typed here. Forward application shortcuts to the
		// shell renderer so they still work with the panel focused.
		attachAppShortcuts(
			view.webContents,
			Boolean(options.isMac),
			shellWebContents,
			true,
			options.getKeybindingOverrides,
			options.isKeybindingRecording,
			(id) => id !== "close-shell-terminal" || options.isCloseShellTerminalShortcutEnabled?.() !== false,
			(id) => {
				if (id !== "toggle-browser-devtools") return;
				lastFocusedViewId = session.viewId;
				void devtoolsAction(session, "toggle").catch(() => undefined);
			},
		);
		view.webContents.on("focus", () => {
			lastFocusedViewId = session.viewId;
		});
		// A newly-created WebContentsView reports about:blank before its renderer
		// has actually been initialized. CDP commands can hang until that initial
		// document has completed, so make readiness explicit for every tab.
		entry.ready = view.webContents.loadURL("about:blank");
		// Keep an unobserved tab initialization failure from becoming an unhandled
		// rejection; callers that need the target still await the original promise.
		void entry.ready.catch(() => undefined);
		if (activate) {
			activateTab(session, tabId, false);
			if (syncNativeOnActivate) queueNativeActiveTabSync(session);
		}
		return entry;
	};

	const ensureSession = (sessionId: string, rendererId?: number): BrowserSessionEntry => {
		const existingViewId = viewIdsBySessionId.get(sessionId);
		const viewId = existingViewId ?? `${rendererId ?? 0}:${sessionId}`;
		let session = entries.get(viewId);
		if (!session) {
			session = {
				sessionId,
				viewId,
				// A non-persist: Electron partition is memory-only. Every tab in
				// this worker shares it, while a fresh worker runtime receives a
				// different partition even if a session ID is ever reused.
				profilePartition: `ao-browser-${randomUUID()}`,
				tabs: new Map(),
				activeTabId: "",
				nextTabNumber: 1,
				bounds: OFFSCREEN_BOUNDS,
				rendererBounds: OFFSCREEN_BOUNDS,
				zoomFactor: 1,
				visible: false,
				agentBrowserCommands: 0,
				nativeOperationQueue: Promise.resolve(),
				devtoolsPlacement: DEFAULT_NATIVE_DEVTOOLS_PLACEMENT,
			};
			entries.set(viewId, session);
			viewIdsBySessionId.set(sessionId, viewId);
			createTab(session, true);
			// A fresh native session starts on the provider's first target. Recording
			// that invariant avoids an unnecessary tab command before the first action;
			// later human selections and popups explicitly invalidate it.
			session.nativeActiveTabId = session.activeTabId;
		}
		if (rendererId !== undefined) {
			const owners = rendererOwnersByViewId.get(viewId) ?? new Set<number>();
			owners.add(rendererId);
			rendererOwnersByViewId.set(viewId, owners);
		}
		return session;
	};

	const queueNativeOperation = <T>(session: BrowserSessionEntry, operation: () => Promise<T>): Promise<T> => {
		const result = session.nativeOperationQueue.then(operation, operation);
		// A failed operation is returned to its caller, but must not permanently
		// poison the session queue. The next operation re-validates the active tab.
		session.nativeOperationQueue = result.then(
			() => undefined,
			() => undefined,
		);
		return result;
	};

	const ensureNativeActiveTab = async (session: BrowserSessionEntry, signal?: AbortSignal): Promise<void> => {
		if (!options.agentBrowserRuntime) return;
		while (session.nativeActiveTabId !== session.activeTabId) {
			const tabId = session.activeTabId;
			const entry = session.tabs.get(tabId);
			if (!entry) throw browserError("BROWSER_TARGET_UNAVAILABLE", "Active browser tab is unavailable");
			await entry.ready;
			// The human-facing BrowserView state is authoritative. Selecting through
			// agent-browser updates its independent active_page_index before another
			// native command is allowed to run.
			await options.agentBrowserRuntime.runAction(
				session.sessionId,
				"tab-select",
				{ tabId },
				agentBrowserTargets(session),
				signal,
			);
			session.nativeActiveTabId = tabId;
		}
	};

	function queueNativeActiveTabSync(session: BrowserSessionEntry): void {
		void queueNativeOperation(session, () => ensureNativeActiveTab(session)).catch(() => undefined);
	}

	const openTab = async (
		session: BrowserSessionEntry,
		url: string | undefined,
		activate: boolean,
		reason: "opened" | "popup" = "opened",
	): Promise<BrowserEntry> => {
		let normalizedURL: string | undefined;
		if (url) {
			const normalized = normalizeBrowserURL(url);
			if (!isAllowedBrowserURL(normalized.href, options.rendererOrigin)) {
				throw browserError("NAVIGATION_FAILED", "Unsupported browser URL");
			}
			normalizedURL = normalized.href;
		}
		const entry = createTab(session, activate);
		await entry.ready;
		if (normalizedURL) {
			const navigation = navigateEntry(entry, normalizedURL);
			pushTabsState(options, session, { kind: reason, tabId: entry.tabId });
			const state = await navigation;
			if (state.error) throw browserError("NAVIGATION_FAILED", state.error);
		} else {
			pushTabsState(options, session, { kind: reason, tabId: entry.tabId });
		}
		return entry;
	};

	function activateTab(session: BrowserSessionEntry, tabId: string, notify = true): BrowserEntry {
		const next = session.tabs.get(tabId);
		if (!next) throw browserError("TAB_NOT_FOUND", `Browser tab ${tabId} does not exist`);
		const previous = session.tabs.get(session.activeTabId);
		if (previous && previous !== next) {
			applyBrowserViewBounds(previous.view, OFFSCREEN_BOUNDS, false);
		}
		session.activeTabId = tabId;
		applySessionBounds(session, next);
		pushNavState(options, next);
		if (notify) pushTabsState(options, session, { kind: "selected", tabId });
		if (session.devtools) pushDevToolsState(session);
		if (session.devtools && session.devtools.desiredTabId !== tabId) {
			void retargetDevTools(session, tabId).catch(() => undefined);
		}
		return next;
	}

	function closeTab(session: BrowserSessionEntry, tabId = session.activeTabId): BrowserTabsState {
		if (session.tabs.size === 1) {
			throw browserError("CANNOT_CLOSE_LAST_TAB", "The only browser tab cannot be closed");
		}
		const tab = session.tabs.get(tabId);
		if (!tab) throw browserError("TAB_NOT_FOUND", `Browser tab ${tabId} does not exist`);
		const wasActive = tabId === session.activeTabId;
		disposeNetworkCapture(tab, "tab-closed");
		if (session.networkTabId === tabId) session.networkTabId = undefined;
		session.tabs.delete(tabId);
		tabsByWebContentsId.delete(tab.view.webContents.id);
		destroyTabView(tab);
		if (wasActive) {
			const nextTabId = [...session.tabs.keys()].at(-1)!;
			activateTab(session, nextTabId, false);
		}
		const state = listTabs(session, { kind: "closed", tabId });
		shellContents(options).send("browser:tabsState", state);
		return state;
	}

	function agentBrowserTargets(session: BrowserSessionEntry): AgentBrowserTargetProvider {
		const target = (entry: BrowserEntry): AgentBrowserTarget => ({
			id: entry.tabId,
			url: entry.view.webContents.getURL() || "about:blank",
			title: entry.view.webContents.getTitle(),
			debugger: entry.view.webContents.debugger,
		});
		return {
			listTargets: () => [...session.tabs.values()].map(target),
			createTarget: async (url) => target(await openTab(session, url === "about:blank" ? undefined : url, true)),
			activateTarget: async (targetId) => {
				const entry = session.tabs.get(targetId);
				if (!entry) throw browserError("TAB_NOT_FOUND", `Browser tab ${targetId} does not exist`);
				await entry.ready;
				activateTab(session, targetId);
			},
			closeTarget: (targetId) => {
				closeTab(session, targetId);
			},
		};
	}

	const retargetDevTools = async (
		session: BrowserSessionEntry,
		tabId = session.activeTabId,
		reveal = false,
	): Promise<BrowserDevToolsState> => {
		const devtools = session.devtools;
		if (!devtools) return pushDevToolsState(session);
		devtools.desiredTabId = tabId;
		if (reveal) devtools.revealRequested = true;
		const generation = ++devtools.retargetGeneration;
		const retarget = async (): Promise<BrowserDevToolsState> => {
			if (session.devtools !== devtools || generation !== devtools.retargetGeneration) {
				return pushDevToolsState(session);
			}
			const entry = session.tabs.get(tabId);
			if (!entry) throw browserError("TAB_NOT_FOUND", `Browser tab ${tabId} does not exist`);
			await entry.ready;
			if (session.devtools !== devtools || generation !== devtools.retargetGeneration) {
				return pushDevToolsState(session);
			}
			const contents = entry.view.webContents;
			if (!contents.openDevTools) {
				throw browserError("BROWSER_DEVTOOLS_UNAVAILABLE", "Browser DevTools are unavailable");
			}
			const targetChanged = devtools.contents !== contents || devtools.targetTabId !== entry.tabId;
			if (targetChanged) {
				const previousContents = devtools.contents;
				devtools.contents = contents;
				if (devtools.targetTabId || previousContents !== contents) {
					if (previousContents === contents) devtools.nativeCloseForReopen = true;
					try {
						previousContents.closeDevTools?.();
					} catch {
						// Chromium may already have closed the previous native surface.
					}
				}
			}
			if (targetChanged || devtools.revealRequested) {
				contents.openDevTools({
					mode: devtools.placement,
					activate: devtools.revealRequested,
				});
			}
			devtools.targetTabId = entry.tabId;
			devtools.revealRequested = false;
			applySessionBounds(session, entry);
			return pushDevToolsState(session);
		};
		const result = devtools.retargetQueue.then(retarget, retarget);
		devtools.retargetQueue = result.then(
			() => undefined,
			() => undefined,
		);
		return result;
	};

	const openDevTools = async (
		session: BrowserSessionEntry,
	): Promise<BrowserDevToolsState> => {
		const entry = activeEntry(session);
		if (!entry.view.webContents.openDevTools) {
			throw browserError("BROWSER_DEVTOOLS_UNAVAILABLE", "Browser DevTools are unavailable");
		}
		if (!session.devtools) {
			session.devtools = {
				contents: entry.view.webContents,
				placement: session.devtoolsPlacement,
				targetTabId: "",
				desiredTabId: entry.tabId,
				retargetGeneration: 0,
				retargetQueue: Promise.resolve(),
				revealRequested: false,
			};
		}
		return retargetDevTools(session, entry.tabId, true);
	};

	const devtoolsAction = async (
		session: BrowserSessionEntry,
		operation: InternalBrowserDevToolsOperation,
		placement?: BrowserDevToolsPlacement,
	): Promise<BrowserDevToolsState> => {
		switch (operation) {
			case "open":
				return openDevTools(session);
			case "toggle":
				if (session.devtools) {
					destroyDevTools(session);
					return pushDevToolsState(session);
				}
				return openDevTools(session);
			case "close":
				destroyDevTools(session);
				return pushDevToolsState(session);
			case "setPlacement":
				if (!placement) throw browserError("INVALID_ARGUMENT", "DevTools placement is required");
				return setDevToolsPlacement(session, placement);
		}
	};

	const setDevToolsPlacement = (
		session: BrowserSessionEntry,
		placement: BrowserDevToolsPlacement,
	): BrowserDevToolsState => {
		if (!Object.hasOwn({ right: true, bottom: true, left: true, undocked: true }, placement)) {
			throw browserError("INVALID_ARGUMENT", "Unsupported browser DevTools placement");
		}
		session.devtoolsPlacement = placement;
		const devtools = session.devtools;
		if (!devtools) return pushDevToolsState(session);
		devtools.placement = placement;
		const contents = activeEntry(session).view.webContents;
		if (!contents.openDevTools) {
			throw browserError("BROWSER_DEVTOOLS_UNAVAILABLE", "Browser DevTools are unavailable");
		}
		const previousContents = devtools.contents;
		if (previousContents === contents) devtools.nativeCloseForReopen = true;
		devtools.contents = contents;
		try {
			previousContents.closeDevTools?.();
		} catch {
			// Chromium may already have closed the native surface.
			devtools.nativeCloseForReopen = false;
		}
		devtools.targetTabId = session.activeTabId;
		try {
			contents.openDevTools({ mode: placement, activate: placement === "undocked" });
		} catch (error) {
			devtools.nativeCloseForReopen = false;
			throw error;
		}
		return pushDevToolsState(session);
	};

	function applySessionBounds(session: BrowserSessionEntry, entry: BrowserEntry): void {
		if (!session.visible) {
			applyBrowserViewBounds(entry.view, OFFSCREEN_BOUNDS, false);
			return;
		}
		// Keep the initialized blank target available to automation, but let the
		// renderer show AO's empty-page UI instead of Chromium's white about:blank.
		const currentURL = entry.view.webContents.getURL();
		if (!currentURL || currentURL === "about:blank") {
			applyBrowserViewBounds(entry.view, session.bounds, false);
			return;
		}
		applyBrowserViewBounds(
			entry.view,
			session.bounds,
			session.bounds.width > 0 && session.bounds.height > 0,
		);
	}

	const isRendererOwned = (event: IpcMainInvokeEvent | IpcMainEvent, viewId: string): boolean =>
		rendererOwnersByViewId.get(viewId)?.has(event.sender.id) ?? false;

	const setBounds = ({ viewId, rect, visible }: BrowserBoundsInput, zoomFactor = 1): void => {
		const session = entries.get(viewId);
		if (!session) return;
		const effectiveZoomFactor = Number.isFinite(zoomFactor) && zoomFactor > 0 ? zoomFactor : 1;
		session.zoomFactor = effectiveZoomFactor;
		const entry = activeEntry(session);
		if (!visible) {
			session.bounds = OFFSCREEN_BOUNDS;
			session.visible = false;
			applySessionBounds(session, entry);
			forgetIfFocused(viewId);
			return;
		}
		// The renderer measures the slot in page-zoomed CSS pixels, while
		// WebContentsView bounds are window coordinates. Convert before clamping so
		// Cmd+/Cmd- page zoom does not detach the native view from its React slot.
		session.rendererBounds = { ...rect };
		session.bounds = clampBoundsToWindow(
			scaleBoundsForZoom(rect, effectiveZoomFactor),
			options.mainWindow.getContentBounds(),
		);
		session.visible = true;
		applySessionBounds(session, entry);
		// The shell toolbar can receive focus immediately after the Browser panel
		// becomes visible. Remember that active panel too, so the DevTools shortcut
		// still targets the browser even when the native page itself is not focused.
		lastFocusedViewId = viewId;
	};

	const navigate = async ({ viewId, url }: BrowserNavigateInput): Promise<BrowserNavState> => {
		const session = entries.get(viewId);
		if (!session) throw browserError("BROWSER_TARGET_UNAVAILABLE", "Browser target is unavailable");
		return navigateEntry(activeEntry(session), url);
	};

	const navigateEntry = async (entry: BrowserEntry, url: string): Promise<BrowserNavState> => {
		await entry.ready;
		cancelAnnotation(options, entry, "navigation");
		const normalized = normalizeBrowserURL(url);
		if (!isAllowedBrowserURL(normalized.href, options.rendererOrigin)) {
			throw new Error("Unsupported browser URL");
		}
		try {
			await entry.view.webContents.loadURL(normalized.href);
		} catch (err) {
			if ((err as { errorCode?: number })?.errorCode === -3) return pushNavState(options, entry);
			entry.view.setVisible?.(false);
			entry.state = { ...readNavState(entry), error: String((err as Error)?.message || "Unable to load page") };
			shellWebContents.send("browser:navState", entry.state);
			return entry.state;
		}
		const session = entries.get(entry.state.viewId);
		if (session?.activeTabId === entry.tabId) applySessionBounds(session, entry);
		return pushNavState(options, entry);
	};

	// clear resets the view to a blank page (`ao preview clear`). about:blank is
	// loaded directly, bypassing the URL allowlist — it carries no content and
	// readNavState normalizes it back to an empty url so the panel shows its
	// empty state.
	const clear = async (viewId: string): Promise<BrowserNavState> => {
		const session = entries.get(viewId);
		if (!session) throw browserError("BROWSER_TARGET_UNAVAILABLE", "Browser target is unavailable");
		const entry = activeEntry(session);
		cancelAnnotation(options, entry, "navigation");
		session.visible = false;
		session.bounds = OFFSCREEN_BOUNDS;
		applySessionBounds(session, entry);
		forgetIfFocused(viewId);
		entry.ready = entry.view.webContents.loadURL("about:blank");
		await entry.ready;
		entry.view.webContents.clearHistory();
		return pushNavState(options, entry);
	};

	// Best-effort full-viewport capture for a browser-annotation submit. Bounded
	// by ANNOTATION_SNAPSHOT_TIMEOUT_MS so a slow/hung capturePage() can never
	// delay the send — on timeout, error, or an empty frame this resolves
	// undefined and the caller proceeds with a text-only message.
	const captureAnnotationSnapshot = async (entry: BrowserEntry): Promise<BrowserAnnotationSnapshot | undefined> => {
		try {
			const timedOut = Symbol("annotation-snapshot-timeout");
			const image = await Promise.race([
				entry.view.webContents.capturePage(),
				new Promise<typeof timedOut>((resolve) => {
					setTimeout(() => resolve(timedOut), ANNOTATION_SNAPSHOT_TIMEOUT_MS);
				}),
			]);
			if (image === timedOut || image.isEmpty()) return undefined;
			const { width, height } = image.getSize();
			const longestEdge = Math.max(width, height);
			const resized =
				longestEdge > ANNOTATION_SNAPSHOT_MAX_DIMENSION
					? image.resize(
							width >= height
								? { width: ANNOTATION_SNAPSHOT_MAX_DIMENSION }
								: { height: ANNOTATION_SNAPSHOT_MAX_DIMENSION },
						)
					: image;
			return { mimeType: "image/png", data: resized.toPNG().toString("base64") };
		} catch {
			return undefined;
		}
	};

	const destroy = (viewId: string): void => {
		const session = entries.get(viewId);
		if (!session) return;
		if (options.mainWindow.isDestroyed?.()) session.devtools = undefined;
		else destroyDevTools(session);
		void options.agentBrowserRuntime?.closeSession(session.sessionId);
		entries.delete(viewId);
		viewIdsBySessionId.delete(session.sessionId);
		rendererOwnersByViewId.delete(viewId);
		forgetIfFocused(viewId);
		// When the window is already gone (dispose fired from mainWindow "closed"),
		// Electron has torn down contentView and the child WebContentsViews. Touching
		// them throws "Object has been destroyed", so just drop our reference.
		if (options.mainWindow.isDestroyed?.()) {
			for (const entry of session.tabs.values()) {
				tabsByWebContentsId.delete(entry.view.webContents.id);
				disposeNetworkCapture(entry, "session-closed");
			}
			return;
		}
		for (const entry of session.tabs.values()) {
			tabsByWebContentsId.delete(entry.view.webContents.id);
			disposeNetworkCapture(entry, "session-closed");
			destroyTabView(entry);
		}
	};

	const destroyTabView = (entry: BrowserEntry): void => {
		applyBrowserViewBounds(entry.view, OFFSCREEN_BOUNDS, false);
		options.mainWindow.contentView.removeChildView?.(entry.view);
		if (entry.view.webContents.debugger?.isAttached()) {
			entry.view.webContents.debugger.detach();
		}
		entry.view.webContents.close?.();
	};

	const invokeNav = (
		viewId: string,
		action: (contents: BrowserWebContents) => void,
		cancelForNavigation = false,
	): BrowserNavState => {
		const session = entries.get(viewId);
		if (!session) return emptyNavState(viewId);
		const entry = activeEntry(session);
		if (cancelForNavigation) {
			cancelAnnotation(options, entry, "navigation");
			applySessionBounds(session, entry);
		}
		action(entry.view.webContents);
		return pushNavState(options, entry);
	};

	const setAnnotationMode = (event: IpcMainInvokeEvent, input: BrowserAnnotationModeInput): void => {
		if (!isRendererOwned(event, input.viewId)) return;
		const session = entries.get(input.viewId);
		if (!session) return;
		const entry = activeEntry(session);
		entry.annotationEnabled = input.enabled;
		entry.view.webContents.send("browser:annotation:setMode", { enabled: input.enabled });
		if (input.enabled) entry.view.webContents.focus();
	};

	const forwardAnnotationSubmit = async (
		event: IpcMainInvokeEvent,
		payload: BrowserAnnotationPageSubmitPayload | undefined,
	): Promise<void> => {
		const entry = tabsByWebContentsId.get(event.sender.id);
		const viewId = entry?.state.viewId;
		if (
			!viewId ||
			!entry ||
			!payload ||
			typeof payload.instruction !== "string" ||
			!isValidAnnotationSelection(payload.selection)
		) {
			return;
		}
		entry.annotationEnabled = false;
		// Captured now, before returning: the preload only tears down the
		// highlight overlay after this handler resolves, so the frame we grab
		// here still has the selection ring(s) on it and not the prompt box
		// (the preload hides that synchronously before invoking).
		const snapshot = await captureAnnotationSnapshot(entry);
		const forwarded: BrowserAnnotationSubmitPayload = {
			viewId,
			instruction: payload.instruction,
			selection: payload.selection,
			...(snapshot ? { snapshot } : {}),
		};
		shellWebContents.send("browser:annotation:submitted", forwarded);
	};

	const forwardAnnotationCancel = (
		event: IpcMainEvent,
		payload: BrowserAnnotationPageCancelPayload | undefined,
	): void => {
		const entry = tabsByWebContentsId.get(event.sender.id);
		const viewId = entry?.state.viewId;
		if (!viewId || !entry) return;
		entry.annotationEnabled = false;
		const forwarded: BrowserAnnotationCancelPayload = {
			viewId,
			reason: payload?.reason ?? "cancel",
		};
		shellWebContents.send("browser:annotation:canceled", forwarded);
	};

	const handle = <Args extends unknown[], Result>(
		channel: string,
		fn: (event: IpcMainInvokeEvent, ...args: Args) => Result,
	): void => {
		options.ipcMain.handle(channel, fn);
		ipcDisposers.push(() => options.ipcMain.removeHandler(channel));
	};
	const on = <Args extends unknown[]>(channel: string, fn: (event: IpcMainEvent, ...args: Args) => void): void => {
		options.ipcMain.on(channel, fn);
		ipcDisposers.push(() => options.ipcMain.off(channel, fn));
	};

	handle("browser:ensure", (event, sessionId: string) => {
		const session = ensureSession(sessionId, event.sender.id);
		pushDevToolsState(session);
		return pushNavState(options, activeEntry(session));
	});
	on("browser:setBounds", (event, input: BrowserBoundsInput) => {
		if (isRendererOwned(event, input.viewId)) setBounds(input, event.sender.getZoomFactor());
	});
	handle("browser:navigate", (event, input: BrowserNavigateInput) =>
		isRendererOwned(event, input.viewId) ? navigate(input) : emptyNavState(input.viewId),
	);
	handle("browser:clear", (event, viewId: string) =>
		isRendererOwned(event, viewId) ? clear(viewId) : emptyNavState(viewId),
	);
	handle("browser:goBack", (event, viewId: string) =>
		isRendererOwned(event, viewId) ? invokeNav(viewId, (contents) => contents.goBack(), true) : emptyNavState(viewId),
	);
	handle("browser:goForward", (event, viewId: string) =>
		isRendererOwned(event, viewId)
			? invokeNav(viewId, (contents) => contents.goForward(), true)
			: emptyNavState(viewId),
	);
	handle("browser:reload", (event, viewId: string) =>
		isRendererOwned(event, viewId) ? invokeNav(viewId, (contents) => contents.reload(), true) : emptyNavState(viewId),
	);
	handle("browser:stop", (event, viewId: string) =>
		isRendererOwned(event, viewId) ? invokeNav(viewId, (contents) => contents.stop()) : emptyNavState(viewId),
	);
	handle("browser:getTabs", (event, viewId: string) => {
		const session = entries.get(viewId);
		return session && isRendererOwned(event, viewId) ? listTabs(session) : emptyTabsState(viewId);
	});
	handle("browser:selectTab", (event, input: BrowserTabInput) => {
		const session = entries.get(input.viewId);
		if (!session || !isRendererOwned(event, input.viewId)) return emptyTabsState(input.viewId);
		return queueNativeOperation(session, async () => {
			activateTab(session, input.tabId);
			await ensureNativeActiveTab(session);
			return listTabs(session);
		});
	});
	handle("browser:closeTab", (event, input: BrowserTabInput) => {
		const session = entries.get(input.viewId);
		if (!session || !isRendererOwned(event, input.viewId)) return emptyTabsState(input.viewId);
		if (session.tabs.size === 1) {
			throw browserError("CANNOT_CLOSE_LAST_TAB", "The only browser tab cannot be closed");
		}
		if (!session.tabs.has(input.tabId)) {
			throw browserError("TAB_NOT_FOUND", `Browser tab ${input.tabId} does not exist`);
		}
		if (!options.agentBrowserRuntime) return closeTab(session, input.tabId);
		return queueNativeOperation(session, async () => {
			await ensureNativeActiveTab(session);
			await options.agentBrowserRuntime!.runAction(
				session.sessionId,
				"tab-close",
				{ tabId: input.tabId },
				agentBrowserTargets(session),
			);
			session.nativeActiveTabId = undefined;
			await ensureNativeActiveTab(session);
			return listTabs(session);
		});
	});
	handle("browser:devtools", (event, input: BrowserDevToolsInput) => {
		if (!input || typeof input.viewId !== "string" || !isRendererOwned(event, input.viewId)) {
			return emptyDevToolsState(input?.viewId ?? "");
		}
		const session = entries.get(input.viewId);
		if (!session) return emptyDevToolsState(input.viewId);
		if (!["open", "close", "setPlacement"].includes(input.operation)) {
			throw browserError("INVALID_ARGUMENT", "Unsupported browser DevTools operation");
		}
		return devtoolsAction(session, input.operation, input.placement);
	});
	handle("browser:openTab", async (event, input: BrowserOpenTabInput) => {
		const session = entries.get(input.viewId);
		if (!session || !isRendererOwned(event, input.viewId)) return emptyTabsState(input.viewId);
		let url = input.url;
		if (url) {
			const normalized = normalizeBrowserURL(url);
			if (!isAllowedBrowserURL(normalized.href, options.rendererOrigin)) {
				throw browserError("NAVIGATION_FAILED", "Unsupported browser URL");
			}
			url = normalized.href;
		}
		if (!options.agentBrowserRuntime) {
			await openTab(session, url, true);
			return listTabs(session);
		}
		return queueNativeOperation(session, async () => {
			// Let agent-browser create UI tabs too so its stable tab registry and
			// AO's WebContentsView IDs stay aligned for later select/close commands.
			await options.agentBrowserRuntime!.runAction(
				session.sessionId,
				"tab-new",
				{ url },
				agentBrowserTargets(session),
			);
			session.nativeActiveTabId = session.activeTabId;
			return listTabs(session);
		});
	});
	handle("browser:annotation:setMode", (event, input: BrowserAnnotationModeInput) => setAnnotationMode(event, input));
	on("browser:destroy", (event, viewId: string) => {
		if (isRendererOwned(event, viewId)) destroy(viewId);
	});
	handle("browser:annotation:submit", (event, payload: BrowserAnnotationPageSubmitPayload) =>
		forwardAnnotationSubmit(event, payload),
	);
	on("browser:annotation:cancel", (event, payload: BrowserAnnotationPageCancelPayload) =>
		forwardAnnotationCancel(event, payload),
	);

	return {
		execute: async (sessionId, action, args = {}, signal) => {
			throwIfAborted(signal);
			if (!sessionId.trim()) throw browserError("INVALID_ARGUMENT", "sessionId is required");
			if (action === "__destroy-session") {
				const viewId = viewIdsBySessionId.get(sessionId);
				await options.agentBrowserRuntime?.closeSession(sessionId);
				if (viewId) destroy(viewId);
				return { destroyed: Boolean(viewId) };
			}
			const session = ensureSession(sessionId);
			const commandId = randomUUID();
			setAgentBrowserActivity(session, action, true, commandId, "started");
			try {
				const entry = activeEntry(session);
			const runNative = async (nativeAction: string, nativeArgs: Record<string, unknown> = {}) => {
				if (!options.agentBrowserRuntime) {
					throw browserError("BROWSER_AUTOMATION_UNAVAILABLE", "Browser automation runtime is unavailable");
				}
				return queueNativeOperation(session, async () => {
					await ensureNativeActiveTab(session, signal);
					await activeEntry(session).ready;
					const result = await options.agentBrowserRuntime!.runAction(
						sessionId,
						nativeAction,
						nativeArgs,
						agentBrowserTargets(session),
						signal,
					);
					if (nativeAction === "tab-select" && typeof nativeArgs.tabId === "string") {
						session.nativeActiveTabId = nativeArgs.tabId;
					}
					if (nativeAction === "tab-new" || nativeAction === "tab-close") {
						session.nativeActiveTabId = undefined;
					}
					if (nativeAction.startsWith("tab-")) await ensureNativeActiveTab(session, signal);
					return result;
				});
			};
			switch (action) {
				case "open": {
					const url = stringArg(args, "url", "URL_REQUIRED", "url is required");
					await runNative(action, { url: normalizeAgentBrowserURL(url) });
					return pushNavState(options, activeEntry(session));
				}
				case "snapshot": {
					const result = await runNative(action, { interactive: Boolean(args.interactive) });
					if (typeof result.snapshot !== "string") {
						throw browserError("BROWSER_AUTOMATION_INVALID_OUTPUT", "Browser snapshot output was invalid");
					}
					return {
						text: result.snapshot,
						refs: result.refs,
						...(result._boundary ? { _boundary: result._boundary } : {}),
						untrustedExternalContent: true,
					};
				}
				case "click":
				case "dblclick":
				case "focus":
				case "hover":
				case "highlight":
				case "scrollintoview":
				case "check":
				case "uncheck":
					return runNative(action, { ref: stringArg(args, "ref", "REFERENCE_REQUIRED", "ref is required") });
				case "fill":
				case "type":
					return runNative(action, {
						ref: stringArg(args, "ref", "REFERENCE_REQUIRED", "ref is required"),
						text: stringArg(args, "text", "INVALID_ARGUMENT", "text is required", true),
					});
				case "press":
					return runNative(action, { key: stringArg(args, "key", "INVALID_ARGUMENT", "key is required") });
				case "drag":
					return runNative(action, {
						ref: stringArg(args, "ref", "REFERENCE_REQUIRED", "ref is required"),
						targetRef: stringArg(args, "targetRef", "REFERENCE_REQUIRED", "target ref is required"),
					});
				case "unhighlight":
					return unhighlightEntry(entry);
				case "tabs":
					return listTabs(session);
				case "tab-new": {
					const url =
						typeof args.url === "string" && args.url.trim() ? normalizeAgentBrowserURL(args.url) : undefined;
					await runNative(action, { url });
					return tabResult(activeEntry(session), true);
				}
				case "tab-select": {
					await runNative(action, { tabId: stringArg(args, "tabId", "TAB_ID_REQUIRED", "tabId is required") });
					return tabResult(activeEntry(session), true);
				}
				case "tab-close": {
					const tabId =
						typeof args.tabId === "string" && args.tabId.trim() ? args.tabId.trim() : session.activeTabId;
					await runNative(action, { tabId });
					return { closedTabId: tabId, ...listTabs(session) };
				}
				case "scroll":
					return runNative(action, {
						direction: stringArg(args, "direction", "INVALID_ARGUMENT", "direction is required"),
						amount: numberArg(args.amount, 1, 5_000) || 600,
					});
				case "select":
					return runNative(action, {
						ref: stringArg(args, "ref", "REFERENCE_REQUIRED", "ref is required"),
						value: stringArg(args, "value", "INVALID_ARGUMENT", "value is required", true),
					});
				case "get": {
					const property = stringArg(args, "property", "INVALID_ARGUMENT", "property is required");
					const result = await runNative(action, {
						property,
						ref: typeof args.ref === "string" && args.ref.trim() ? args.ref : undefined,
					});
					return { ...result, value: result.value ?? result[property] };
				}
				case "wait":
					return runNative(action, args);
				case "frame":
				case "dialog":
					return runNative(action, args);
				case "devtools-open":
				case "devtools-close":
				{
					const operation = action.slice("devtools-".length) as BrowserDevToolsInput["operation"];
					return devtoolsAction(session, operation);
				}
				case "screenshot":
					if (!options.agentBrowserRuntime) {
						throw browserError("BROWSER_AUTOMATION_UNAVAILABLE", "Browser automation runtime is unavailable");
					}
					await activeEntry(session).ready;
					return options.agentBrowserRuntime.screenshot(sessionId, agentBrowserTargets(session), signal);
				case "network-start":
					return startNetworkCapture(
						session,
						entry,
						networkDurationArg(args.durationSeconds),
					);
				case "network-status":
					return networkCaptureStatus(networkEntryFor(session));
				case "network-list":
					return networkCaptureResult(networkEntryFor(session));
				case "network-stop":
					return stopNetworkCapture(networkEntryFor(session), "stopped");
				case "network-clear":
					return clearNetworkCapture(networkEntryFor(session));
				case "console":
				case "errors":
					return normalizeNativeMessages(await runNative(action), action);
				default:
					throw browserError("INVALID_ARGUMENT", `Unsupported browser action: ${action}`);
			}
			} finally {
				setAgentBrowserActivity(session, action, false, commandId, "finished");
			}
		},
		dispose: () => {
			if (disposePromise) return disposePromise;
			disposePromise = (async () => {
				ipcDisposers.splice(0).forEach((dispose) => dispose());
				await options.agentBrowserRuntime?.dispose();
				for (const viewId of [...entries.keys()]) {
					destroy(viewId);
				}
			})();
			return disposePromise;
		},
		destroy,
		destroyAll: () => {
			for (const viewId of [...entries.keys()]) {
				destroy(viewId);
			}
		},
		getLastFocusedPanelContents: () => {
			if (lastFocusedViewId === null) return null;
			const session = entries.get(lastFocusedViewId);
			if (!session) return null;
			const entry = activeEntry(session);
			// Stored narrowed as BrowserWebContents but is a full WebContents at runtime.
			const contents = entry.view.webContents as unknown as WebContents;
			return contents.isDestroyed() ? null : contents;
		},
		toggleDevToolsForLastFocused: async () => {
			if (lastFocusedViewId === null) return null;
			const session = entries.get(lastFocusedViewId);
			if (!session) return null;
			return devtoolsAction(session, "toggle");
		},
		forgetLastFocusedPanel: () => {
			lastFocusedViewId = null;
		},
	};
}

function withDefaultScheme(raw: string): string {
	if (isWindowsAbsolutePath(raw) || isPosixAbsolutePath(raw)) return localPathToFileURL(raw);
	if (/^https?:\/\//i.test(raw)) return raw;
	if (isLocalhostLike(raw)) return `http://${raw}`;
	// A single token with no whitespace can be a destination: an explicit scheme
	// (file:, mailto:, ...) or a bare hostname we default to https. Anything else —
	// whitespace-containing text, or a lone word that is not a hostname — is a
	// search query, not a URL (Chrome-style omnibox behavior).
	if (!/\s/.test(raw)) {
		if (/^[a-zA-Z][a-zA-Z\d+.-]*:/.test(raw)) return raw;
		if (looksLikeHost(raw)) return `https://${raw}`;
	}
	return searchURL(raw);
}

// Treat input as a navigable host when the authority (the part before any
// path/query/fragment) is an IPv6 literal, carries an explicit :port, or has a
// dot (a domain). Bare words like "hi" fail this and become a search instead.
function looksLikeHost(raw: string): boolean {
	const host = raw.split(/[/?#]/, 1)[0];
	if (host === "") return false;
	if (host.startsWith("[") && host.includes("]")) return true;
	if (/:\d+$/.test(host)) return true;
	return host.includes(".");
}

function searchURL(query: string): string {
	return `https://www.google.com/search?q=${encodeURIComponent(query)}`;
}

function isWindowsAbsolutePath(raw: string): boolean {
	return /^[a-zA-Z]:[\\/]/.test(raw);
}

function isPosixAbsolutePath(raw: string): boolean {
	return raw.startsWith("/");
}

function localPathToFileURL(raw: string): string {
	if (isWindowsAbsolutePath(raw)) {
		const normalized = raw.replace(/\\/g, "/");
		return `file:///${encodePathSegments(normalized).replace(/^([A-Za-z])%3A(?=\/)/, "$1:")}`;
	}
	return `file://${encodePathSegments(raw)}`;
}

function encodePathSegments(pathname: string): string {
	return pathname.split("/").map(encodeURIComponent).join("/");
}

function isLocalhostLike(raw: string): boolean {
	return /^(localhost|127(?:\.\d{1,3}){3}|0\.0\.0\.0|\[::1\])(?::\d+)?(?:[/?#]|$)/i.test(raw);
}

function emptyNavState(viewId: string): BrowserNavState {
	return {
		viewId,
		url: "",
		title: "",
		canGoBack: false,
		canGoForward: false,
		isLoading: false,
	};
}

function emptyTabsState(viewId: string): BrowserTabsState {
	return { viewId, activeTabId: "", tabs: [] };
}

function emptyDevToolsState(viewId: string): BrowserDevToolsState {
	return { viewId, open: false, activeTabId: "", placement: "undocked" };
}

function activeEntry(session: BrowserSessionEntry): BrowserEntry {
	const entry = session.tabs.get(session.activeTabId);
	if (!entry) throw browserError("BROWSER_TARGET_UNAVAILABLE", "Active browser tab is unavailable");
	return entry;
}

function tabResult(entry: BrowserEntry, active: boolean): BrowserTabState & { untrustedExternalContent: true } {
	return {
		id: entry.tabId,
		url: entry.view.webContents.getURL(),
		title: entry.view.webContents.getTitle(),
		active,
		...(entry.favicon ? { favicon: entry.favicon } : {}),
		untrustedExternalContent: true,
	};
}

function listTabs(
	session: BrowserSessionEntry,
	change?: BrowserTabsState["change"],
): BrowserTabsState & { untrustedExternalContent: true } {
	return {
		viewId: session.viewId,
		activeTabId: session.activeTabId,
		tabs: [...session.tabs.values()].map((entry) => tabResult(entry, entry.tabId === session.activeTabId)),
		untrustedExternalContent: true,
		...(change ? { change } : {}),
	};
}

function pushTabsState(
	options: BrowserViewHostOptions,
	session: BrowserSessionEntry,
	change?: BrowserTabsState["change"],
): BrowserTabsState {
	const state = listTabs(session, change);
	shellContents(options).send("browser:tabsState", state);
	return state;
}

function shellContents(options: BrowserViewHostOptions): WebContents {
	const contents = options.shellWebContents ?? options.mainWindow.webContents;
	if (!contents) throw new Error("Browser view host requires shell WebContents");
	return contents;
}

function hardenWebContents(
	contents: BrowserWebContents,
	options: BrowserViewHostOptions,
	entry: BrowserEntry,
	createPopup: () => BrowserWebContents,
	canCreatePopup: () => boolean,
): void {
	contents.setWindowOpenHandler(({ url }) => {
		if (!isAllowedBrowserURL(url, options.rendererOrigin) || !canCreatePopup()) {
			return { action: "deny" };
		}
		return {
			action: "allow",
			createWindow: () => createPopup() as WebContents,
		};
	});
	const blockUnsafeNavigation = (event: Electron.Event, url: string) => {
		if (!isAllowedBrowserURL(url, options.rendererOrigin)) {
			event.preventDefault();
			entry.state = { ...entry.state, error: "Unsupported browser URL" };
			shellContents(options).send("browser:navState", entry.state);
		}
	};
	contents.on("will-navigate", blockUnsafeNavigation);
	contents.on("will-redirect", blockUnsafeNavigation);
}

function wireNavEvents(
	contents: BrowserWebContents,
	options: BrowserViewHostOptions,
	entry: BrowserEntry,
	isActive: () => boolean,
	syncActiveBounds: () => void,
	syncTabs: () => void,
): void {
	const update = () => {
		syncTabs();
		if (isActive()) pushNavState(options, entry);
	};
	contents.on("did-navigate", (_event, url) => {
		clearStaleFavicon(entry, url);
		if (isActive()) syncActiveBounds();
		update();
	});
	contents.on("did-navigate-in-page", update);
	contents.on("page-title-updated", update);
	contents.on("did-start-loading", () => {
		cancelAnnotation(options, entry, "navigation");
		update();
	});
	contents.on("did-stop-loading", () => {
		update();
	});
	contents.on("did-fail-load", (_event, errorCode, errorDescription) => {
		if (errorCode === -3) return;
		if (isActive()) entry.view.setVisible?.(false);
		entry.state = { ...readNavState(entry), error: String(errorDescription || "Unable to load page") };
		if (isActive()) shellContents(options).send("browser:navState", entry.state);
	});
}

// A same-tab navigation to a different site (search result, link, address bar)
// keeps the previous site's favicon displayed until the new one has been
// fetched and decoded — a real network round trip — which reads as a laggy
// icon swap. Clear it as soon as the new origin is known (favicons are
// near-always origin-scoped, so a same-origin navigation likely keeps the
// same one and is left alone) so the rail falls back to the generic icon
// immediately instead of showing the *wrong* one while it waits. Called from
// wireNavEvents' own did-navigate handler rather than registering a second
// listener for the same event.
function clearStaleFavicon(entry: BrowserEntry, url: string): void {
	const origin = originOf(url);
	if (origin && origin === entry.faviconOrigin) return;
	entry.favicon = undefined;
	entry.faviconSourceUrl = undefined;
	entry.faviconPendingUrl = undefined;
	entry.faviconOrigin = undefined;
}

function wireFaviconEvents(contents: BrowserWebContents, entry: BrowserEntry, syncTabs: () => void): void {
	contents.on("page-favicon-updated", (_event, favicons) => {
		const url = favicons[0];
		// Not yet applied (faviconSourceUrl) and not already in flight
		// (faviconPendingUrl) — a page re-announcing the same favicon while a
		// prior fetch for it is still pending must not start a duplicate.
		if (!url || url === entry.faviconSourceUrl || url === entry.faviconPendingUrl) return;
		entry.faviconPendingUrl = url;
		void fetchFavicon(entry, url).then((dataUrl) => {
			if (entry.faviconPendingUrl === url) entry.faviconPendingUrl = undefined;
			// Leave faviconSourceUrl unset on failure (rather than marking the
			// URL "seen") so a later page-favicon-updated event for the same
			// URL — browsers re-fire these on soft/in-page navigations — gets a
			// fresh attempt instead of being permanently stuck on the fallback
			// icon from one transient fetch failure.
			if (entry.faviconSourceUrl !== url && dataUrl) {
				entry.faviconSourceUrl = url;
				entry.favicon = dataUrl;
				entry.faviconOrigin = originOf(url);
				syncTabs();
			}
		});
	});
}

function originOf(url: string): string | undefined {
	try {
		return new URL(url).origin;
	} catch {
		return undefined;
	}
}

// Fetched through the tab's own isolated partition (not the shell session), so
// it carries whatever cookies/proxy config that site's tab already has, and
// resized/re-encoded like other browser-view thumbnail capture in this file.
async function fetchFavicon(entry: BrowserEntry, url: string): Promise<string | undefined> {
	try {
		// Some sites inline a tiny favicon as a data: URI rather than serving a
		// file — decode it directly instead of rejecting it as an unsupported
		// scheme, still normalized/capped through the same resize path.
		if (url.startsWith("data:")) {
			const image = nativeImage.createFromDataURL(url);
			if (image.isEmpty()) return undefined;
			return image.resize({ width: FAVICON_SIZE, height: FAVICON_SIZE, quality: "good" }).toDataURL();
		}
		const parsed = new URL(url);
		if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
		const tabSession = (entry.view.webContents as unknown as WebContents).session;
		const response = await tabSession.fetch(url);
		if (!response.ok) return undefined;
		const buffer = Buffer.from(await response.arrayBuffer());
		if (buffer.byteLength === 0 || buffer.byteLength > MAX_FAVICON_BYTES) return undefined;
		const image = nativeImage.createFromBuffer(buffer);
		if (image.isEmpty()) return undefined;
		return image.resize({ width: FAVICON_SIZE, height: FAVICON_SIZE, quality: "good" }).toDataURL();
	} catch {
		return undefined;
	}
}

function cancelAnnotation(
	options: BrowserViewHostOptions,
	entry: BrowserEntry,
	reason: BrowserAnnotationCancelPayload["reason"],
): void {
	if (!entry.annotationEnabled) return;
	entry.annotationEnabled = false;
	entry.view.webContents.send("browser:annotation:setMode", { enabled: false });
	shellContents(options).send("browser:annotation:canceled", {
		viewId: entry.state.viewId,
		reason,
	});
}

function pushNavState(options: BrowserViewHostOptions, entry: BrowserEntry): BrowserNavState {
	entry.state = readNavState(entry);
	shellContents(options).send("browser:navState", entry.state);
	return entry.state;
}

function readNavState(entry: BrowserEntry): BrowserNavState {
	const { webContents } = entry.view;
	const currentURL = webContents.getURL();
	return {
		viewId: entry.state.viewId,
		// about:blank is the cleared/blank state — surface it as an empty url so
		// the panel renders its "enter a URL" empty state and the address bar is
		// blank rather than showing "about:blank".
		url: currentURL === "about:blank" ? "" : currentURL,
		title: webContents.getTitle(),
		canGoBack: webContents.canGoBack(),
		canGoForward: webContents.canGoForward(),
		isLoading: webContents.isLoading(),
	};
}

function wireAutomationEvents(contents: BrowserWebContents, entry: BrowserEntry): void {
	contents.debugger?.on("message", (_event, method, params) => {
		handleNetworkDebuggerEvent(entry, method, params as Record<string, unknown>);
	});
}


async function ensureDebugger(entry: BrowserEntry): Promise<void> {
	await entry.ready;
	const debug = entry.view.webContents.debugger;
	if (!debug) throw browserError("BROWSER_TARGET_UNAVAILABLE", "Browser debugger is unavailable");
	if (!debug.isAttached()) {
		try {
			debug.attach("1.3");
		} catch (error) {
			throw browserError(
				"BROWSER_TARGET_UNAVAILABLE",
				error instanceof Error ? error.message : "Unable to attach to browser target",
			);
		}
	}
	await debug.sendCommand("Runtime.enable");
	await debug.sendCommand("DOM.enable");
}

function networkEntryFor(session: BrowserSessionEntry): BrowserEntry {
	if (session.networkTabId) {
		const captured = session.tabs.get(session.networkTabId);
		if (captured) return captured;
		session.networkTabId = undefined;
	}
	return activeEntry(session);
}

async function startNetworkCapture(
	session: BrowserSessionEntry,
	entry: BrowserEntry,
	durationSeconds: number,
): Promise<unknown> {
	const existing = networkEntryFor(session);
	if (existing.networkCapture?.active) {
		return { ...networkCaptureStatus(existing), alreadyActive: true };
	}
	if (existing !== entry) disposeNetworkCapture(existing, "restarted");
	disposeNetworkCapture(entry, "restarted");
	await ensureDebugger(entry);
	await entry.view.webContents.debugger.sendCommand("Network.enable");
	const started = Date.now();
	const capture: BrowserNetworkCapture = {
		active: true,
		tabId: entry.tabId,
		startedAt: new Date(started).toISOString(),
		expiresAt: new Date(started + durationSeconds * 1_000).toISOString(),
		maxEntries: MAX_NETWORK_REQUESTS,
		nextSequence: 1,
		requests: [],
		byRequestId: new Map(),
	};
	capture.timer = setTimeout(() => {
		void stopNetworkCapture(entry, "expired");
	}, durationSeconds * 1_000);
	entry.networkCapture = capture;
	session.networkTabId = entry.tabId;
	return networkCaptureStatus(entry);
}

function networkCaptureStatus(entry: BrowserEntry): Record<string, unknown> {
	const capture = entry.networkCapture;
	if (!capture) {
		return {
			active: false,
			metadataOnly: true,
			tabId: entry.tabId,
			requestCount: 0,
			maxEntries: MAX_NETWORK_REQUESTS,
			untrustedExternalContent: true,
		};
	}
	return {
		active: capture.active,
		metadataOnly: true,
		tabId: capture.tabId,
		requestCount: capture.requests.length,
		maxEntries: capture.maxEntries,
		startedAt: capture.startedAt,
		expiresAt: capture.expiresAt,
		...(capture.stoppedAt ? { stoppedAt: capture.stoppedAt } : {}),
		...(capture.stopReason ? { stopReason: capture.stopReason } : {}),
		untrustedExternalContent: true,
	};
}

function networkCaptureResult(entry: BrowserEntry): Record<string, unknown> {
	return {
		...networkCaptureStatus(entry),
		requests: (entry.networkCapture?.requests ?? []).map(publicNetworkRequest),
		untrustedExternalContent: true,
	};
}

async function stopNetworkCapture(entry: BrowserEntry, reason: string): Promise<Record<string, unknown>> {
	const capture = entry.networkCapture;
	if (!capture?.active) return networkCaptureResult(entry);
	if (capture.timer) {
		clearTimeout(capture.timer);
		capture.timer = undefined;
	}
	capture.active = false;
	capture.stoppedAt = new Date().toISOString();
	capture.stopReason = reason;
	// The debugger attachment is shared by capture, agent-browser, and DevTools.
	// Stop recording locally, but leave the shared Network domain enabled until
	// the attachment itself is released.
	return networkCaptureResult(entry);
}

function clearNetworkCapture(entry: BrowserEntry): Record<string, unknown> {
	const capture = entry.networkCapture;
	if (capture) {
		capture.requests = [];
		capture.byRequestId.clear();
	}
	return networkCaptureStatus(entry);
}

function disposeNetworkCapture(entry: BrowserEntry, reason: string): void {
	const capture = entry.networkCapture;
	if (!capture) return;
	if (capture.timer) clearTimeout(capture.timer);
	capture.timer = undefined;
	capture.active = false;
	capture.stoppedAt = new Date().toISOString();
	capture.stopReason = reason;
}

function handleNetworkDebuggerEvent(entry: BrowserEntry, method: string, params: Record<string, unknown>): void {
	const capture = entry.networkCapture;
	if (!capture?.active || !method.startsWith("Network.")) return;

	const requestID = typeof params.requestId === "string" ? params.requestId : "";
	if (!requestID) return;
	const timestamp = finiteNumber(params.timestamp);

	if (method === "Network.requestWillBeSent") {
		const request = objectValue(params.request);
		const url = typeof request.url === "string" ? request.url : "";
		const previous = capture.byRequestId.get(requestID);
		const redirect = objectValue(params.redirectResponse);
		if (previous && Object.keys(redirect).length > 0) {
			applyNetworkResponse(previous, redirect);
			finishNetworkRequest(previous, timestamp);
			previous.redirectedTo = sanitizeNetworkURL(url);
		}
		const wallTime = finiteNumber(params.wallTime);
		const item: InternalBrowserNetworkRequest = {
			id: `n${capture.nextSequence++}`,
			protocolRequestId: requestID,
			method: typeof request.method === "string" ? request.method : "GET",
			url: sanitizeNetworkURL(url),
			resourceType: typeof params.type === "string" ? params.type.toLowerCase() : undefined,
			startedAt: wallTime ? new Date(wallTime * 1_000).toISOString() : new Date().toISOString(),
			startedMonotonic: timestamp,
			requestHeaders: selectedNetworkHeaders(request.headers, "request"),
		};
		appendNetworkRequest(capture, item);
		capture.byRequestId.set(requestID, item);
		return;
	}

	const item = capture.byRequestId.get(requestID);
	if (!item) return;
	switch (method) {
		case "Network.responseReceived":
			applyNetworkResponse(item, objectValue(params.response));
			break;
		case "Network.loadingFinished":
			finishNetworkRequest(item, timestamp);
			break;
		case "Network.loadingFailed":
			item.failed = true;
			item.canceled = params.canceled === true;
			item.errorText = typeof params.errorText === "string" ? params.errorText : "Request failed";
			finishNetworkRequest(item, timestamp);
			break;
		case "Network.requestServedFromCache":
			item.fromCache = true;
			break;
	}
}

function applyNetworkResponse(item: InternalBrowserNetworkRequest, response: Record<string, unknown>): void {
	const status = finiteNumber(response.status);
	if (status !== undefined) item.status = status;
	if (typeof response.statusText === "string" && response.statusText) item.statusText = response.statusText;
	if (typeof response.mimeType === "string" && response.mimeType) item.mimeType = response.mimeType;
	item.fromCache =
		item.fromCache === true ||
		response.fromDiskCache === true ||
		response.fromPrefetchCache === true;
	item.fromServiceWorker = response.fromServiceWorker === true;
	item.responseHeaders = selectedNetworkHeaders(response.headers, "response");
}

function finishNetworkRequest(item: InternalBrowserNetworkRequest, timestamp: number | undefined): void {
	if (timestamp !== undefined && item.startedMonotonic !== undefined) {
		item.durationMs = Math.max(0, Math.round((timestamp - item.startedMonotonic) * 1_000));
	}
}

function appendNetworkRequest(capture: BrowserNetworkCapture, item: InternalBrowserNetworkRequest): void {
	capture.requests.push(item);
	if (capture.requests.length <= capture.maxEntries) return;
	const removed = capture.requests.shift();
	if (removed && capture.byRequestId.get(removed.protocolRequestId) === removed) {
		capture.byRequestId.delete(removed.protocolRequestId);
	}
}

function publicNetworkRequest(item: InternalBrowserNetworkRequest): BrowserNetworkRequest {
	const { protocolRequestId: _protocolRequestId, startedMonotonic: _startedMonotonic, ...result } = item;
	return result;
}

const SAFE_REQUEST_HEADERS = new Set([
	"accept",
	"content-type",
	"origin",
	"referer",
	"sec-fetch-mode",
	"sec-fetch-site",
]);
const SAFE_RESPONSE_HEADERS = new Set([
	"access-control-allow-headers",
	"access-control-allow-methods",
	"access-control-allow-origin",
	"cache-control",
	"content-length",
	"content-type",
	"location",
	"vary",
]);

function selectedNetworkHeaders(value: unknown, kind: "request" | "response"): Record<string, string> | undefined {
	const headers = objectValue(value);
	const allowed = kind === "request" ? SAFE_REQUEST_HEADERS : SAFE_RESPONSE_HEADERS;
	const selected: Record<string, string> = {};
	for (const [rawName, rawValue] of Object.entries(headers)) {
		const name = rawName.toLowerCase();
		if (!allowed.has(name)) continue;
		let headerValue = typeof rawValue === "string" ? rawValue : String(rawValue);
		if (name === "referer" || name === "location") headerValue = sanitizeNetworkURL(headerValue);
		selected[name] = headerValue.slice(0, 1_000);
	}
	return Object.keys(selected).length > 0 ? selected : undefined;
}

function sanitizeNetworkURL(raw: string): string {
	try {
		const url = new URL(raw);
		if (!["http:", "https:", "file:"].includes(url.protocol)) {
			return `${url.protocol}[redacted]`;
		}
		url.username = "";
		url.password = "";
		url.hash = "";
		for (const name of [...url.searchParams.keys()]) {
			url.searchParams.set(name, "[redacted]");
		}
		return url.href;
	} catch {
		const withoutFragment = raw.split("#", 1)[0] ?? "";
		return (withoutFragment.split("?", 1)[0] ?? "").slice(0, 2_000);
	}
}

function objectValue(value: unknown): Record<string, unknown> {
	return value && typeof value === "object" && !Array.isArray(value)
		? (value as Record<string, unknown>)
		: {};
}

function finiteNumber(value: unknown): number | undefined {
	return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}


async function unhighlightEntry(entry: BrowserEntry): Promise<unknown> {
	await ensureDebugger(entry);
	await entry.view.webContents.debugger.sendCommand("Overlay.enable");
	await entry.view.webContents.debugger.sendCommand("Overlay.hideHighlight");
	return { url: entry.view.webContents.getURL() };
}


function stringArg(
	args: Record<string, unknown>,
	name: string,
	code: string,
	message: string,
	allowEmpty = false,
): string {
	const value = args[name];
	if (typeof value !== "string" || (!allowEmpty && !value.trim())) throw browserError(code, message);
	return value;
}


function numberArg(value: unknown, min: number, max: number): number {
	if (typeof value !== "number" || !Number.isFinite(value)) return 0;
	return Math.max(min, Math.min(max, Math.round(value)));
}

function networkDurationArg(value: unknown): number {
	if (value === undefined) return DEFAULT_NETWORK_CAPTURE_SECONDS;
	if (
		typeof value !== "number" ||
		!Number.isFinite(value) ||
		!Number.isInteger(value) ||
		value < 1 ||
		value > MAX_NETWORK_CAPTURE_SECONDS
	) {
		throw browserError(
			"INVALID_ARGUMENT",
			`network capture duration must be an integer from 1 to ${MAX_NETWORK_CAPTURE_SECONDS} seconds`,
		);
	}
	return value;
}

function normalizeAgentBrowserURL(input: string): string {
	const raw = input.trim();
	if (!raw) throw browserError("URL_REQUIRED", "url is required");
	if (isWindowsAbsolutePath(raw) || isPosixAbsolutePath(raw) || /^file:/i.test(raw)) {
		throw browserError("BROWSER_URL_FORBIDDEN", "Agent browser commands cannot open local files");
	}
	if (!/^https?:\/\//i.test(raw) && !isLocalhostLike(raw) && !looksLikeHost(raw)) {
		throw browserError("INVALID_URL", "ao browser open requires an explicit http(s) URL or hostname");
	}
	const normalized = normalizeBrowserURL(raw);
	if (normalized.protocol !== "http:" && normalized.protocol !== "https:") {
		throw browserError("BROWSER_URL_FORBIDDEN", "Agent browser commands support only http(s) URLs");
	}
	return normalized.href;
}

function externalText(value: unknown): string {
	const raw = value == null ? "" : String(value);
	const bytes = Buffer.from(raw, "utf8");
	if (bytes.length <= MAX_EXTERNAL_TEXT_BYTES) return raw;
	return `${bytes.subarray(0, MAX_EXTERNAL_TEXT_BYTES).toString("utf8")}\n[Content truncated at ${MAX_EXTERNAL_TEXT_BYTES} bytes]`;
}

function markUntrusted(value: string): string {
	const escaped = value
		.replaceAll(UNTRUSTED_BEGIN, `\\u003c${UNTRUSTED_BEGIN.slice(1)}`)
		.replaceAll(UNTRUSTED_END, `\\u003c${UNTRUSTED_END.slice(1)}`);
	return `${UNTRUSTED_BEGIN}\n${escaped}\n${UNTRUSTED_END}`;
}

function normalizeNativeMessages(result: Record<string, unknown>, action: string): Record<string, unknown> {
	const raw = Array.isArray(result.messages) ? result.messages : Array.isArray(result.value) ? result.value : [];
	const messages = raw.map((item): BrowserLogEntry => {
		if (typeof item === "string") {
			return {
				level: action === "errors" ? "error" : "log",
				message: markUntrusted(externalText(item)),
				timestamp: new Date().toISOString(),
			};
		}
		const record = item && typeof item === "object" ? (item as Record<string, unknown>) : {};
		const level =
			typeof record.level === "string"
				? record.level
				: typeof record.type === "string"
					? record.type
					: action === "errors"
						? "error"
						: "log";
		const message =
			typeof record.message === "string"
				? record.message
				: typeof record.text === "string"
					? record.text
					: JSON.stringify(record);
		return {
			level,
			message: markUntrusted(externalText(message)),
			timestamp: typeof record.timestamp === "string" ? record.timestamp : new Date().toISOString(),
		};
	});
	return { messages, untrustedExternalContent: true };
}

function throwIfAborted(signal?: AbortSignal): void {
	if (signal?.aborted) throw browserError("BROWSER_COMMAND_CANCELED", "Browser command was canceled");
}

function browserError(code: string, message: string): Error & { code: string } {
	return Object.assign(new Error(message), { code });
}
