import {
	resetClosedTabsForTest,
	resetConsumedPreviewTriggersForTest,
	useBrowserView,
	type BrowserNavState,
} from "./useBrowserView";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

type Listener = (state: BrowserNavState) => void;
type TabsListener = (state: import("../../main/browser-view-host").BrowserTabsState) => void;
type DevToolsListener = (state: import("../../main/browser-view-host").BrowserDevToolsState) => void;
type ActivityListener = (state: import("../../main/browser-view-host").BrowserAgentActivityState) => void;
type ProfileListener = (state: import("../../shared/browser-profiles").BrowserProfileViewState) => void;

function createSlot(rect: Partial<DOMRect> = {}) {
	const slot = document.createElement("div");
	document.body.appendChild(slot);
	slot.getBoundingClientRect = vi.fn(() => ({
		x: 12,
		y: 34,
		width: 320,
		height: 240,
		top: 34,
		right: 332,
		bottom: 274,
		left: 12,
		toJSON: () => ({}),
		...rect,
	}));
	return slot;
}

function setupBridge() {
	const listeners = new Set<Listener>();
	const tabsListeners = new Set<TabsListener>();
	const devtoolsListeners = new Set<DevToolsListener>();
	const activityListeners = new Set<ActivityListener>();
	const profileListeners = new Set<ProfileListener>();
	const bridge = {
		nativeCompositionEnabled: false,
		stateFor(viewId: string): BrowserNavState {
			return {
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			};
		},
		ensure: vi.fn(async (sessionId: string): Promise<BrowserNavState> => ({
			viewId: `42:${sessionId}`,
			url: "",
			title: "",
			canGoBack: false,
			canGoForward: false,
			isLoading: false,
		})),
		setBounds: vi.fn(),
		setOverlayOpen: vi.fn(),
		navigate: vi.fn(async ({ viewId }: { viewId: string }) => bridge.stateFor(viewId)),
		clear: vi.fn(async (viewId: string) => bridge.stateFor(viewId)),
		goBack: vi.fn(async (viewId: string) => bridge.stateFor(viewId)),
		goForward: vi.fn(async (viewId: string) => bridge.stateFor(viewId)),
		reload: vi.fn(async (viewId: string) => bridge.stateFor(viewId)),
		stop: vi.fn(async (viewId: string) => bridge.stateFor(viewId)),
		getTabs: vi.fn(async (viewId: string) => ({
			viewId,
			activeTabId: "t1",
			tabs: [{ id: "t1", url: "", title: "", active: true }],
		})),
		selectTab: vi.fn(async ({ viewId, tabId }: { viewId: string; tabId: string }) => ({
			viewId,
			activeTabId: tabId,
			tabs: [{ id: tabId, url: "http://localhost:4173/", title: "Selected", active: true }],
		})),
		closeTab: vi.fn(async ({ viewId }: { viewId: string; tabId: string }) => ({
			viewId,
			activeTabId: "t1",
			tabs: [{ id: "t1", url: "http://localhost:3000/", title: "First", active: true }],
		})),
		openTab: vi.fn(async ({ viewId }: { viewId: string; url?: string }) => ({
			viewId,
			activeTabId: "t2",
			tabs: [
				{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
				{ id: "t2", url: "", title: "", active: true },
			],
		})),
		notifyPanelUsed: vi.fn(),
		notifyPanelBlur: vi.fn(),
		onFocusLocation: vi.fn(() => () => undefined),
		onReopenClosedTab: vi.fn(() => () => undefined),
		devtools: vi.fn(
			async ({ viewId, operation, placement }: {
				viewId: string;
				operation: "open" | "close" | "setPlacement";
				placement?: "right" | "bottom" | "left" | "undocked";
			}) => ({
				viewId,
				open: operation !== "close",
				activeTabId: "t1",
				placement: placement ?? "undocked",
			}),
		),
		getProfile: vi.fn(async (viewId: string) => ({ viewId, profileId: null, temporary: true })),
		showProfileMenu: vi.fn(),
		selectProfile: vi.fn(),
		historySuggestions: vi.fn(async () => []),
		captureScreenshot: vi.fn(async () => undefined),
		downloads: {
			list: vi.fn(async () => ({ downloads: [] })),
			action: vi.fn(async () => ({ downloads: [] })),
			clear: vi.fn(async () => ({ downloads: [] })),
			onChanged: vi.fn(() => () => { /* no-op test subscription */ }),
		},
		destroy: vi.fn(),
		setAnnotationMode: vi.fn(async () => undefined),
		onNavState: vi.fn((listener: Listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		}),
		onPageFocus: vi.fn(() => () => undefined),
		onTabsState: vi.fn((listener: TabsListener) => {
			tabsListeners.add(listener);
			return () => tabsListeners.delete(listener);
		}),
		onDevToolsState: vi.fn((listener: DevToolsListener) => {
			devtoolsListeners.add(listener);
			return () => devtoolsListeners.delete(listener);
		}),
		onAgentActivity: vi.fn((listener: ActivityListener) => {
			activityListeners.add(listener);
			return () => activityListeners.delete(listener);
		}),
		onProfileState: vi.fn((listener: ProfileListener) => {
			profileListeners.add(listener);
			return () => profileListeners.delete(listener);
		}),
		onProfileManage: vi.fn(() => () => undefined),
		onAnnotationSubmit: vi.fn(() => () => undefined),
		onAnnotationCancel: vi.fn(() => () => undefined),
		emit(state: BrowserNavState) {
			listeners.forEach((listener) => listener(state));
		},
		emitTabs(state: Parameters<TabsListener>[0]) {
			tabsListeners.forEach((listener) => listener(state));
		},
		emitDevTools(state: Parameters<DevToolsListener>[0]) {
			devtoolsListeners.forEach((listener) => listener(state));
		},
		emitActivity(state: Parameters<ActivityListener>[0]) {
			activityListeners.forEach((listener) => listener(state));
		},
		emitProfile(state: Parameters<ProfileListener>[0]) {
			profileListeners.forEach((listener) => listener(state));
		},
	};
	window.ao = { ...window.ao!, browser: bridge };
	return bridge;
}

// jsdom does not implement the Fullscreen API, so `document.fullscreenElement`
// has no property descriptor to spy on. Define it directly, and clear it after
// each test so state never leaks between cases.
function setFullscreenElement(element: Element | null): void {
	Object.defineProperty(document, "fullscreenElement", {
		configurable: true,
		get: () => element,
	});
}

describe("useBrowserView", () => {
	afterEach(() => {
		vi.restoreAllMocks();
		setFullscreenElement(null);
		document.body.replaceChildren();
		resetConsumedPreviewTriggersForTest();
		resetClosedTabsForTest();
	});

	it("ensures a scoped browser view and reports the measured slot bounds", async () => {
		const bridge = setupBridge();
		const slot = createSlot();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-1"));
		// Simulate the real IPC flow: after ensure, a navigate call sends a nav
		// state with a URL so the positioning effect considers the view visible.
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);
		act(() => result.current.slotRef(slot));

		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				rect: { x: 12, y: 34, width: 320, height: 240 },
				visible: true,
			}),
		);
		expect(result.current.viewId).toBe("42:sess-1");
	});

	it("keeps an active blank target live", async () => {
		const bridge = setupBridge();
		const slot = createSlot();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(result.current.viewId).toBe("42:sess-1"));
		act(() => result.current.slotRef(slot));

		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				rect: { x: 12, y: 34, width: 320, height: 240 },
				visible: true,
			}),
		);
	});

	it("tracks popup tabs and routes manual select and close actions", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Popup", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);

		expect(result.current.activeTabId).toBe("t2");
		expect(result.current.tabNotice).toBe("Opened new tab");

		await act(() => result.current.selectTab("t1"));
		expect(bridge.selectTab).toHaveBeenCalledWith({ viewId: "42:sess-1", tabId: "t1" });
		await act(() => result.current.closeTab("t2"));
		expect(bridge.closeTab).toHaveBeenCalledWith({ viewId: "42:sess-1", tabId: "t2" });
	});

	it("remembers a closed tab so it can be reopened, and forgets it once reopened", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);

		await act(() => result.current.closeTab("t2"));
		expect(result.current.closedTabs).toEqual([{ id: "t2", url: "http://localhost:4173/", title: "Second", favicon: undefined }]);

		await act(() => result.current.reopenClosedTab("t2"));
		expect(bridge.openTab).toHaveBeenCalledWith({ viewId: "42:sess-1", url: "http://localhost:4173/" });
		expect(result.current.closedTabs).toEqual([]);
	});

	it("remembers a tab closed by a main-process keyboard shortcut", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));

		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t1",
				tabs: [{ id: "t1", url: "http://localhost:3000/", title: "First", active: true }],
				change: {
					kind: "closed",
					tabId: "t2",
					tab: { id: "t2", url: "http://localhost:4173/", title: "Keyboard closed", active: false },
				},
			}),
		);

		expect(result.current.closedTabs).toEqual([
			{ id: "t2", url: "http://localhost:4173/", title: "Keyboard closed", favicon: undefined },
		]);
		await act(() => result.current.reopenClosedTab("t2"));
		expect(bridge.openTab).toHaveBeenCalledWith({ viewId: "42:sess-1", url: "http://localhost:4173/" });
	});

	it("can reopen a tab immediately after receiving its keyboard-close event", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));

		await act(async () => {
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t1",
				tabs: [{ id: "t1", url: "http://localhost:3000/", title: "First", active: true }],
				change: {
					kind: "closed",
					tabId: "t2",
					tab: { id: "t2", url: "http://localhost:4173/", title: "Keyboard closed", active: false },
				},
			});
			await result.current.reopenClosedTab("t2");
		});

		expect(bridge.openTab).toHaveBeenCalledWith({ viewId: "42:sess-1", url: "http://localhost:4173/" });
	});

	it("reopens a closed tab beyond the former tab cap", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);
		await act(() => result.current.closeTab("t2"));
		expect(result.current.closedTabs).toHaveLength(1);

		// Simulate many other tabs having opened since (e.g. via agent activity).
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t1",
				tabs: Array.from({ length: 16 }, (_, i) => ({
					id: `t${i + 10}`,
					url: `http://localhost:3000/${i}`,
					title: `Tab ${i}`,
					active: i === 0,
				})),
			}),
		);

		await act(() => result.current.reopenClosedTab("t2"));

		expect(bridge.openTab).toHaveBeenCalledWith({ viewId: "42:sess-1", url: "http://localhost:4173/" });
		expect(result.current.closedTabs).toHaveLength(0);
	});

	// Regression: the same drop-before-await bug also loses the entry on any
	// other openTab failure (e.g. a race where the cap is hit between the row
	// rendering and the click) — restore it instead.
	it("restores a closed-tab entry if reopening it fails", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);
		await act(() => result.current.closeTab("t2"));
		expect(result.current.closedTabs).toHaveLength(1);

		bridge.openTab.mockRejectedValueOnce(new Error("Browser tab creation failed"));
		await act(() => result.current.reopenClosedTab("t2"));

		expect(result.current.closedTabs).toHaveLength(1);
		expect(result.current.tabNotice).toBe("Couldn't reopen that tab");
	});

	// Regression: closeTab/selectTab are invoked fire-and-forget (`void
	// onCloseTab(...)`) from the tabs rail, so a rejection used to become a
	// silent unhandled rejection with zero user feedback.
	it("surfaces a notice instead of throwing when closing or selecting a tab fails", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));

		bridge.closeTab.mockRejectedValueOnce(new Error("boom"));
		await expect(act(() => result.current.closeTab("t1"))).resolves.toBeUndefined();
		expect(result.current.tabNotice).toBe("Couldn't close that tab");

		bridge.selectTab.mockRejectedValueOnce(new Error("boom"));
		await expect(act(() => result.current.selectTab("t1"))).resolves.toBeUndefined();
		expect(result.current.tabNotice).toBe("Couldn't switch to that tab");
	});

	// Regression: the main process can mutate its own tab state (e.g. the
	// automation runtime's closeTarget callback removes the tab) before still
	// reporting the close as failed. Without a resync, the renderer kept
	// showing the already-closed tab's row forever — every retry re-failed
	// identically since the main process had nothing left to close.
	it("resyncs tab state from the main process after a failed close, instead of leaving a ghost row", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);

		// The close call rejects, but the main process actually removed t2
		// already — getTabs reflects that reality.
		bridge.closeTab.mockRejectedValueOnce(new Error("agent-browser lost the connection mid-command"));
		bridge.getTabs.mockResolvedValueOnce({
			viewId: "42:sess-1",
			activeTabId: "t1",
			tabs: [{ id: "t1", url: "http://localhost:3000/", title: "First", active: true }],
		});

		await act(() => result.current.closeTab("t2"));

		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
	});

	// Regression: closeTab can silently no-op — the underlying native close
	// fails and the tab stays in session.tabs — and the recently-closed
	// capture used to run before ever checking the response, so a tab that
	// visibly failed to close still showed up as "closed" anyway.
	it("does not remember a tab as closed when the main process reports it is still open", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);

		bridge.closeTab.mockResolvedValueOnce({
			viewId: "42:sess-1",
			activeTabId: "t2",
			tabs: [
				{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
				{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
			],
		});
		await act(() => result.current.closeTab("t2"));

		expect(result.current.closedTabs).toEqual([]);
		expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1", "t2"]);
	});

	it("does not remember a closed blank tab, since there is nothing to reopen", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "", title: "", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);

		bridge.closeTab.mockResolvedValueOnce({
			viewId: "42:sess-1",
			activeTabId: "t2",
			tabs: [{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true }],
		});
		await act(() => result.current.closeTab("t1"));
		expect(result.current.closedTabs).toEqual([]);
	});

	// Regression: the main process reports a freshly-opened tab as the literal
	// string "about:blank" once its initial load settles (see
	// isBlankBrowserEntry in browser-view-host.ts), not an empty string. A
	// plain truthiness check on the url treated that as "real" content, so
	// closing a tab nobody had navigated in showed up in Recently Closed.
	it("does not remember a closed about:blank tab, since there is nothing to reopen", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "about:blank", title: "", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);

		bridge.closeTab.mockResolvedValueOnce({
			viewId: "42:sess-1",
			activeTabId: "t2",
			tabs: [{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true }],
		});
		await act(() => result.current.closeTab("t1"));
		expect(result.current.closedTabs).toEqual([]);
	});

	// Regression: unlike live tabs (kept alive in the main process and simply
	// re-fetched), Recently Closed was built up purely from this hook's own
	// state — so switching sessions and back lost the list for good, even
	// though nothing about that session actually changed.
	it("keeps a session's Recently Closed list across switching away and back", async () => {
		const bridge = setupBridge();
		const { result, rerender } = renderHook(
			({ sessionId }) => useBrowserView({ sessionId, active: true, poppedOut: false }),
			{ initialProps: { sessionId: "sess-1" } },
		);
		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);
		await act(() => result.current.closeTab("t2"));
		expect(result.current.closedTabs).toEqual([{ id: "t2", url: "http://localhost:4173/", title: "Second", favicon: undefined }]);

		rerender({ sessionId: "sess-2" });
		await waitFor(() => expect(result.current.viewId).toBe("42:sess-2"));
		expect(result.current.closedTabs).toEqual([]);

		rerender({ sessionId: "sess-1" });
		await waitFor(() => expect(result.current.viewId).toBe("42:sess-1"));
		expect(result.current.closedTabs).toEqual([{ id: "t2", url: "http://localhost:4173/", title: "Second", favicon: undefined }]);
	});

	it("forgets a session's Recently Closed list once the session is genuinely terminated", async () => {
		const bridge = setupBridge();
		const { result, rerender } = renderHook(
			({ terminated }) => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false, terminated }),
			{ initialProps: { terminated: false } },
		);
		await waitFor(() => expect(result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		act(() =>
			bridge.emitTabs({
				viewId: "42:sess-1",
				activeTabId: "t2",
				tabs: [
					{ id: "t1", url: "http://localhost:3000/", title: "First", active: false },
					{ id: "t2", url: "http://localhost:4173/", title: "Second", active: true },
				],
				change: { kind: "popup", tabId: "t2" },
			}),
		);
		await act(() => result.current.closeTab("t2"));
		expect(result.current.closedTabs).toHaveLength(1);

		rerender({ terminated: true });
		await waitFor(() => expect(result.current.viewId).toBe(""));

		const revived = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(revived.result.current.tabs.map((tab) => tab.id)).toEqual(["t1"]));
		expect(revived.result.current.closedTabs).toEqual([]);
	});

	it("remeasures the live native view while moving between panel and maximized browser slots", async () => {
		const bridge = setupBridge();
		const slot = createSlot();
		const { result, rerender } = renderHook(
			({ poppedOut }) => useBrowserView({ sessionId: "sess-1", active: true, poppedOut }),
			{ initialProps: { poppedOut: false } },
		);
		await waitFor(() => expect(result.current.viewId).toBe("42:sess-1"));
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);
		act(() => result.current.slotRef(slot));
		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				rect: { x: 12, y: 34, width: 320, height: 240 },
				visible: true,
			}),
		);
		bridge.setBounds.mockClear();

		act(() => {
			rerender({ poppedOut: true });
		});

		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				rect: { x: 12, y: 34, width: 320, height: 240 },
				visible: true,
			}),
		);
	});

	it("clamps the native view to its resizable-panel column when the slot overspills", async () => {
		const bridge = setupBridge();
		// The slot is wider than its column (e.g. the `min-w-[280px]` wrapper on a
		// narrower inspector panel). The native overlay isn't clipped by DOM
		// overflow, so the reported bounds must be intersected with the column.
		const column = document.createElement("div");
		column.setAttribute("data-panel", "");
		column.getBoundingClientRect = vi.fn(() => ({
			x: 100,
			y: 0,
			width: 150,
			height: 600,
			top: 0,
			right: 250,
			bottom: 600,
			left: 100,
			toJSON: () => ({}),
		}));
		const slot = createSlot();
		column.appendChild(slot);
		document.body.appendChild(column);

		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-1"));
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);
		act(() => result.current.slotRef(slot));

		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				// Left edge is inset by the resize handle's reserved 6px (see
				// RESIZE_HANDLE_RESERVE_PX in useBrowserView.ts) so the handle's
				// hit area, inside this same column, is never covered by the view.
				rect: { x: 106, y: 34, width: 144, height: 240 },
				visible: true,
			}),
		);
	});

	it("re-measures after a layout transition settles, catching a position-only shift", async () => {
		// A ResizeObserver fires on size changes only; entering pop-out / opening the
		// inspector moves the slot to a new x without resizing it, so the transition
		// itself must drive a settle re-measure or the native overlay keeps stale
		// (spilled) bounds. This is the regression behind the preview covering the
		// terminal until an unrelated window resize fixed it.
		vi.useFakeTimers();
		try {
			const bridge = setupBridge();
			const slot = createSlot();
			const { result, rerender } = renderHook(
				({ poppedOut }) => useBrowserView({ sessionId: "sess-1", active: true, poppedOut }),
				{ initialProps: { poppedOut: false } },
			);
			// ensure() resolves on a microtask; flush it without advancing timers.
			await act(async () => {
				await Promise.resolve();
			});
			// Simulate a real nav state with URL so the positioning effect shows the view.
			act(() =>
				bridge.emit({
					viewId: "42:sess-1",
					url: "http://localhost:3000/",
					title: "",
					canGoBack: false,
					canGoForward: false,
					isLoading: false,
				}),
			);
			act(() => result.current.slotRef(slot));
			// Flush the mount measure (immediate frame + settle timer).
			await act(async () => {
				vi.advanceTimersByTime(300);
			});
			expect(bridge.setBounds).toHaveBeenCalled();

			// Pop-out transition: the immediate frame captures the still-animating
			// geometry; the final position only lands once the panel has settled.
			act(() => rerender({ poppedOut: true }));
			await act(async () => {
				vi.advanceTimersByTime(20);
			});
			bridge.setBounds.mockClear();
			slot.getBoundingClientRect = vi.fn(() => ({
				x: 240,
				y: 34,
				width: 320,
				height: 240,
				top: 34,
				right: 560,
				bottom: 274,
				left: 240,
				toJSON: () => ({}),
			}));
			await act(async () => {
				vi.advanceTimersByTime(300);
			});
			expect(bridge.setBounds).toHaveBeenCalledWith(
				expect.objectContaining({ rect: expect.objectContaining({ x: 240, width: 320 }) }),
			);
		} finally {
			vi.useRealTimers();
		}
	});

	it("hides the native view when inactive and on unmount without destroying session state", async () => {
		const bridge = setupBridge();
		const slot = createSlot();
		const { result, rerender, unmount } = renderHook(
			({ active }) => useBrowserView({ sessionId: "sess-1", active, poppedOut: false }),
			{ initialProps: { active: true } },
		);
		await waitFor(() => expect(result.current.viewId).toBe("42:sess-1"));
		act(() => result.current.slotRef(slot));

		rerender({ active: false });
		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenLastCalledWith({
				viewId: "42:sess-1",
				rect: { x: 0, y: 0, width: 0, height: 0 },
				visible: false,
			}),
		);

		unmount();
		expect(bridge.setBounds).toHaveBeenLastCalledWith({
			viewId: "42:sess-1",
			rect: { x: 0, y: 0, width: 0, height: 0 },
			visible: false,
		});
		expect(bridge.destroy).not.toHaveBeenCalled();
	});

	it("does not snapshot or park the native page for an ordinary modal dialog", async () => {
		const bridge = setupBridge();
		const slot = createSlot();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-1"));
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);
		act(() => result.current.slotRef(slot));
		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				rect: { x: 12, y: 34, width: 320, height: 240 },
				visible: true,
			}),
		);

		bridge.setBounds.mockClear();
		const dialog = document.createElement("div");
		dialog.setAttribute("role", "dialog");
		dialog.setAttribute("data-state", "open");
		await act(async () => {
			document.body.appendChild(dialog);
			await Promise.resolve();
		});
		expect(bridge.setBounds).not.toHaveBeenCalled();

		bridge.setBounds.mockClear();
		await act(async () => {
			dialog.remove();
			await Promise.resolve();
		});
	});

	it("raises the shell above the live native view while a browser overlay is open", async () => {
		const bridge = setupBridge();
		const slot = createSlot();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-1"));
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);
		act(() => result.current.slotRef(slot));
		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				rect: { x: 12, y: 34, width: 320, height: 240 },
				visible: true,
			}),
		);

		const menu = document.createElement("div");
		menu.setAttribute("role", "menu");
		menu.setAttribute("data-browser-native-overlay", "true");
		menu.setAttribute("data-state", "open");
		await act(async () => {
			document.body.appendChild(menu);
			await Promise.resolve();
		});

		await waitFor(() => expect(bridge.setOverlayOpen).toHaveBeenCalledWith(true));
	});

	it("opens a browser overlay without waiting for a snapshot", async () => {
		const bridge = setupBridge();
		const slot = createSlot();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-1"));
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);
		act(() => result.current.slotRef(slot));
		await waitFor(() => expect(result.current.navState.url).toBe("http://localhost:3000/"));

		const menu = document.createElement("div");
		menu.setAttribute("role", "menu");
		menu.setAttribute("data-browser-native-overlay", "true");
		menu.setAttribute("data-state", "open");
		await act(async () => {
			document.body.appendChild(menu);
			await Promise.resolve();
		});
		await waitFor(() => expect(bridge.setOverlayOpen).toHaveBeenCalledWith(true));
	});

	it("keeps the native page live while native composition raises a browser overlay", async () => {
		const bridge = setupBridge();
		const slot = createSlot();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-1"));
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);
		act(() => result.current.slotRef(slot));
		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				rect: { x: 12, y: 34, width: 320, height: 240 },
				visible: true,
			}),
		);

		const menu = document.createElement("div");
		menu.setAttribute("role", "menu");
		menu.setAttribute("data-browser-native-overlay", "true");
		menu.setAttribute("data-state", "open");
		await act(async () => {
			document.body.appendChild(menu);
			await Promise.resolve();
		});

		await waitFor(() => expect(bridge.setOverlayOpen).toHaveBeenCalledWith(true));

		menu.remove();
		await waitFor(() => expect(bridge.setOverlayOpen).toHaveBeenLastCalledWith(false));
	});

	it("tracks a reused portal when its data-state flips in place", async () => {
		// Radix reuses its portal node and flips data-state="open"↔"closed" without
		// adding/removing a body child. A childList-only observer misses this, so the
		// view stays un-parked while the dropdown is open. The hardened observer must
		// catch the in-place attribute flip and re-park.
		const bridge = setupBridge();
		const slot = createSlot();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));

		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-1"));
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);
		act(() => result.current.slotRef(slot));

		// The portal node is present the whole time; only its data-state flips.
		const portal = document.createElement("div");
		portal.setAttribute("role", "menu");
		portal.setAttribute("data-browser-native-overlay", "true");
		portal.setAttribute("data-state", "closed");
		await act(async () => {
			document.body.appendChild(portal);
			await Promise.resolve();
		});

		await act(async () => {
			portal.setAttribute("data-state", "open");
			await Promise.resolve();
		});
		await waitFor(() => expect(bridge.setOverlayOpen).toHaveBeenCalledWith(true));

		await act(async () => {
			portal.setAttribute("data-state", "closed");
			await Promise.resolve();
		});
		await waitFor(() => expect(bridge.setOverlayOpen).toHaveBeenLastCalledWith(false));
	});

	it("updates nav state only for the current view", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(result.current.viewId).toBe("42:sess-1"));

		act(() =>
			bridge.emit({
				viewId: "other:sess-1",
				url: "https://ignored.test/",
				title: "Ignored",
				canGoBack: true,
				canGoForward: true,
				isLoading: true,
			}),
		);
		expect(result.current.navState.url).toBe("");

		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:5173/",
				title: "Local app",
				canGoBack: false,
				canGoForward: true,
				isLoading: false,
			}),
		);
		expect(result.current.navState.url).toBe("http://localhost:5173/");
		expect(result.current.navState.title).toBe("Local app");
	});

	it("never exposes a departed session's leftover navState, even transiently, once sessionId changes", async () => {
		// `navState` is component state, not derived from `sessionId` — without a
		// synchronous ownership guard, the hook returns the PREVIOUS session's
		// stale url for one render before its own reset effect lands. A
		// consumer that decides whether to auto-open a browser tab from
		// `navState.url` (e.g. SessionView) could read that stale render and
		// wrongly treat a departed session's leftover URL as this session's own
		// content. An effect that observes every committed render, not just the
		// final settled one, is the only way to catch a transient wrong value —
		// checking `result.current` after `rerender` proves nothing here, since
		// testing-library's `act` flushes any reset effect before returning.
		const bridge = setupBridge();
		const observedUrls: string[] = [];
		function useProbe(sid: string) {
			const view = useBrowserView({ sessionId: sid, active: true, poppedOut: false });
			useEffect(() => {
				observedUrls.push(view.navState.url);
			});
			return view;
		}
		const { rerender } = renderHook(({ sessionId }) => useProbe(sessionId), {
			initialProps: { sessionId: "sess-1" },
		});
		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-1"));
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);

		observedUrls.length = 0;
		rerender({ sessionId: "sess-2" });
		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-2"));

		expect(observedUrls).not.toContain("http://localhost:3000/");
	});

	it("navigates on each preview revision, including a same-URL re-run, and ignores replays", async () => {
		const bridge = setupBridge();
		const { rerender } = renderHook(
			({ previewUrl, previewRevision }) =>
				useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false, previewUrl, previewRevision }),
			{ initialProps: { previewUrl: "http://localhost:5173/", previewRevision: 1 } },
		);

		await waitFor(() =>
			expect(bridge.navigate).toHaveBeenCalledWith({ viewId: "42:sess-1", url: "http://localhost:5173/" }),
		);
		expect(bridge.navigate).toHaveBeenCalledTimes(1);

		// CDC replays the session payload on an unrelated update (revision
		// unchanged) — the panel must not reload.
		rerender({ previewUrl: "http://localhost:5173/", previewRevision: 1 });
		expect(bridge.navigate).toHaveBeenCalledTimes(1);

		// Re-running `ao preview` with the SAME url bumps the revision and must
		// re-navigate (refresh) — the regression this issue fixes.
		rerender({ previewUrl: "http://localhost:5173/", previewRevision: 2 });
		await waitFor(() => expect(bridge.navigate).toHaveBeenCalledTimes(2));

		// A changed target with a fresh revision navigates to the new URL.
		rerender({ previewUrl: "file:///tmp/preview/index.html", previewRevision: 3 });
		await waitFor(() =>
			expect(bridge.navigate).toHaveBeenCalledWith({ viewId: "42:sess-1", url: "file:///tmp/preview/index.html" }),
		);
		expect(bridge.navigate).toHaveBeenCalledTimes(3);
	});

	it("keeps the user's manual navigation on session switch-back and only re-navigates on a new preview", async () => {
		// Regression for #3536: the native view survives a session switch in the
		// main process with the user's last URL (e.g. google.com), but the preview
		// effect used to re-fire on remount and navigate it back to previewUrl.
		const bridge = setupBridge();
		const { result, rerender } = renderHook(
			({ sessionId, previewUrl, previewRevision }) =>
				useBrowserView({ sessionId, active: true, poppedOut: false, previewUrl, previewRevision }),
			{
				initialProps: {
					sessionId: "sess-1",
					previewUrl: "http://localhost:5217/" as string | undefined,
					previewRevision: 1 as number | undefined,
				},
			},
		);
		await waitFor(() =>
			expect(bridge.navigate).toHaveBeenCalledWith({ viewId: "42:sess-1", url: "http://localhost:5217/" }),
		);
		expect(bridge.navigate).toHaveBeenCalledTimes(1);

		// The user browses elsewhere; the main-process view now holds google.com.
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "https://www.google.com/",
				title: "Google",
				canGoBack: true,
				canGoForward: false,
				isLoading: false,
			}),
		);

		// Switch to another session, then back.
		rerender({ sessionId: "sess-2", previewUrl: undefined, previewRevision: undefined });
		await waitFor(() => expect(result.current.viewId).toBe("42:sess-2"));
		rerender({ sessionId: "sess-1", previewUrl: "http://localhost:5217/", previewRevision: 1 });
		await waitFor(() => expect(result.current.viewId).toBe("42:sess-1"));

		// The already-consumed preview must not be re-asserted: the view keeps
		// whatever the user navigated to.
		expect(bridge.navigate).toHaveBeenCalledTimes(1);
		expect(bridge.clear).not.toHaveBeenCalled();

		// A genuine new `ao preview` (revision bump) still takes over.
		rerender({ sessionId: "sess-1", previewUrl: "http://localhost:5217/", previewRevision: 2 });
		await waitFor(() => expect(bridge.navigate).toHaveBeenCalledTimes(2));
		expect(bridge.navigate).toHaveBeenLastCalledWith({ viewId: "42:sess-1", url: "http://localhost:5217/" });
	});

	it("does not re-navigate to the preview when the hook fully remounts for the same session", async () => {
		// SessionView may unmount entirely on a session switch; the consumed
		// trigger must outlive the hook instance, not just a prop change.
		const bridge = setupBridge();
		const first = renderHook(() =>
			useBrowserView({
				sessionId: "sess-1",
				active: true,
				poppedOut: false,
				previewUrl: "http://localhost:5217/",
				previewRevision: 1,
			}),
		);
		await waitFor(() => expect(bridge.navigate).toHaveBeenCalledTimes(1));
		first.unmount();

		const second = renderHook(() =>
			useBrowserView({
				sessionId: "sess-1",
				active: true,
				poppedOut: false,
				previewUrl: "http://localhost:5217/",
				previewRevision: 1,
			}),
		);
		await waitFor(() => expect(second.result.current.viewId).toBe("42:sess-1"));
		expect(bridge.navigate).toHaveBeenCalledTimes(1);
		second.unmount();
	});

	it("re-applies the preview after termination frees the consumed trigger for a reused session ID", async () => {
		const bridge = setupBridge();
		const first = renderHook(
			({ terminated }) =>
				useBrowserView({
					sessionId: "sess-1",
					active: true,
					poppedOut: false,
					terminated,
					previewUrl: "http://localhost:5217/",
					previewRevision: 1,
				}),
			{ initialProps: { terminated: false } },
		);
		await waitFor(() => expect(bridge.navigate).toHaveBeenCalledTimes(1));
		first.rerender({ terminated: true });
		await waitFor(() => expect(bridge.destroy).toHaveBeenCalledWith("42:sess-1"));
		first.unmount();

		// A fresh worker reusing the session ID gets its own preview navigation.
		const second = renderHook(() =>
			useBrowserView({
				sessionId: "sess-1",
				active: true,
				poppedOut: false,
				previewUrl: "http://localhost:5217/",
				previewRevision: 1,
			}),
		);
		await waitFor(() => expect(bridge.navigate).toHaveBeenCalledTimes(2));
		second.unmount();
	});

	it("re-applies the preview on remount without a native browser, whose view state does not survive", async () => {
		// In web/mock mode navState is component-local, so remounting with an
		// already-consumed trigger must still restore the static preview.
		const original = window.ao;
		window.ao = undefined;
		try {
			const props = {
				sessionId: "sess-1",
				active: true,
				poppedOut: false,
				previewUrl: "http://localhost:5217/",
				previewRevision: 1,
			};
			const first = renderHook(() => useBrowserView(props));
			await waitFor(() => expect(first.result.current.navState.url).toBe("http://localhost:5217/"));
			first.unmount();

			const second = renderHook(() => useBrowserView(props));
			await waitFor(() => expect(second.result.current.navState.url).toBe("http://localhost:5217/"));
			second.unmount();
		} finally {
			window.ao = original;
		}
	});

	it("navigates each worker to its own target when sessions share a revision number", async () => {
		const bridge = setupBridge();
		const { rerender } = renderHook(
			({ sessionId, previewUrl }) =>
				useBrowserView({
					sessionId,
					active: true,
					poppedOut: false,
					previewUrl,
					previewRevision: 1,
				}),
			{ initialProps: { sessionId: "sess-1", previewUrl: "http://127.0.0.1:4173/" } },
		);

		await waitFor(() =>
			expect(bridge.navigate).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				url: "http://127.0.0.1:4173/",
			}),
		);

		rerender({ sessionId: "sess-2", previewUrl: "http://127.0.0.1:5173/" });
		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-2"));
		await waitFor(() =>
			expect(bridge.navigate).toHaveBeenCalledWith({
				viewId: "42:sess-2",
				url: "http://127.0.0.1:5173/",
			}),
		);
		expect(bridge.navigate).toHaveBeenCalledTimes(2);
	});

	it("navigates legacy preview URLs when the daemon omits preview revisions", async () => {
		const bridge = setupBridge();
		const { result, rerender } = renderHook(
			({ previewUrl }) => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false, previewUrl }),
			{ initialProps: { previewUrl: undefined as string | undefined } },
		);
		await waitFor(() => expect(result.current.viewId).toBe("42:sess-1"));
		expect(bridge.navigate).not.toHaveBeenCalled();

		rerender({ previewUrl: "http://localhost:5173/" });
		await waitFor(() =>
			expect(bridge.navigate).toHaveBeenCalledWith({ viewId: "42:sess-1", url: "http://localhost:5173/" }),
		);
		expect(bridge.navigate).toHaveBeenCalledTimes(1);

		rerender({ previewUrl: "http://localhost:5173/" });
		expect(bridge.navigate).toHaveBeenCalledTimes(1);

		rerender({ previewUrl: "C:\\Users\\Lenovo\\Downloads\\sm5\\paper_explainer.html" });
		await waitFor(() =>
			expect(bridge.navigate).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				url: "C:\\Users\\Lenovo\\Downloads\\sm5\\paper_explainer.html",
			}),
		);
		expect(bridge.navigate).toHaveBeenCalledTimes(2);
	});

	it("clears the view when the preview is reset (ao preview clear) and does not navigate", async () => {
		const bridge = setupBridge();
		const { rerender } = renderHook(
			({ previewUrl, previewRevision }) =>
				useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false, previewUrl, previewRevision }),
			{ initialProps: { previewUrl: "http://localhost:5173/" as string | undefined, previewRevision: 1 } },
		);
		await waitFor(() => expect(bridge.navigate).toHaveBeenCalledTimes(1));

		// `ao preview clear` empties previewUrl and bumps the revision.
		rerender({ previewUrl: undefined, previewRevision: 2 });
		await waitFor(() => expect(bridge.clear).toHaveBeenCalledWith("42:sess-1"));
		expect(bridge.navigate).toHaveBeenCalledTimes(1);
	});

	it("does not navigate or clear without a preview URL at revision zero", async () => {
		const bridge = setupBridge();
		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(result.current.viewId).toBe("42:sess-1"));
		expect(bridge.navigate).not.toHaveBeenCalled();
		expect(bridge.clear).not.toHaveBeenCalled();
	});

	it("destroys the complete browser target when the session is terminated", async () => {
		const bridge = setupBridge();
		const { rerender, result } = renderHook(
			({ terminated }) =>
				useBrowserView({
					sessionId: "sess-1",
					active: true,
					poppedOut: false,
					terminated,
					previewUrl: "http://localhost:5173/",
					previewRevision: 1,
				}),
			{ initialProps: { terminated: false } },
		);
		// The preview drives a navigate on mount.
		await waitFor(() => expect(bridge.navigate).toHaveBeenCalledTimes(1));

		// Terminate the session – the view must be cleared and no re-navigate.
		rerender({ terminated: true });
		await waitFor(() => expect(bridge.destroy).toHaveBeenCalledWith("42:sess-1"));
		expect(bridge.clear).not.toHaveBeenCalled();
		expect(bridge.navigate).toHaveBeenCalledTimes(1);
		expect(result.current.viewId).toBe("");
	});

	it("hides the native view while an element outside the slot is fullscreen, and restores it on exit", async () => {
		// The terminal pane's fullscreen button promotes it into the DOM top layer,
		// which covers every DOM node but not the native view — Chromium composites
		// that above the page regardless. The transition also leaves the slot's box
		// untouched, so no observer fires and the view kept painting its stale
		// bounds over the fullscreen terminal, toolbar-less. Fullscreen must hide it.
		vi.useFakeTimers();
		try {
			const bridge = setupBridge();
			const slot = createSlot();
			const terminalPane = document.createElement("div");
			document.body.appendChild(terminalPane);

			const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
			await act(async () => {
				await Promise.resolve();
			});
			act(() =>
				bridge.emit({
					viewId: "42:sess-1",
					url: "http://localhost:3000/",
					title: "",
					canGoBack: false,
					canGoForward: false,
					isLoading: false,
				}),
			);
			act(() => result.current.slotRef(slot));
			await act(async () => {
				vi.advanceTimersByTime(300);
			});
			expect(bridge.setBounds).toHaveBeenLastCalledWith(
				expect.objectContaining({ visible: true, rect: expect.objectContaining({ width: 320 }) }),
			);

			// Terminal pane enters fullscreen: the slot is not inside it, so the
			// view must go hidden even though the slot's own box never changed.
			bridge.setBounds.mockClear();
			setFullscreenElement(terminalPane);
			act(() => document.dispatchEvent(new Event("fullscreenchange")));
			await act(async () => {
				vi.advanceTimersByTime(300);
			});
			expect(bridge.setBounds).toHaveBeenLastCalledWith({
				viewId: "42:sess-1",
				rect: { x: 0, y: 0, width: 0, height: 0 },
				visible: false,
			});

			// Exiting fullscreen restores the view at its measured bounds.
			bridge.setBounds.mockClear();
			setFullscreenElement(null);
			act(() => document.dispatchEvent(new Event("fullscreenchange")));
			await act(async () => {
				vi.advanceTimersByTime(300);
			});
			expect(bridge.setBounds).toHaveBeenLastCalledWith(
				expect.objectContaining({ visible: true, rect: expect.objectContaining({ x: 12, width: 320 }) }),
			);
		} finally {
			vi.useRealTimers();
		}
	});

	it("keeps the native view visible when the slot itself is inside the fullscreen element", async () => {
		// Guards the `contains` check: if the browser subtree is the thing going
		// fullscreen, the slot is still on screen and must keep painting.
		const bridge = setupBridge();
		const host = document.createElement("div");
		document.body.appendChild(host);
		const slot = createSlot();
		host.appendChild(slot);

		const { result } = renderHook(() => useBrowserView({ sessionId: "sess-1", active: true, poppedOut: false }));
		await waitFor(() => expect(bridge.ensure).toHaveBeenCalledWith("sess-1"));
		act(() =>
			bridge.emit({
				viewId: "42:sess-1",
				url: "http://localhost:3000/",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			}),
		);
		act(() => result.current.slotRef(slot));

		setFullscreenElement(host);
		act(() => document.dispatchEvent(new Event("fullscreenchange")));

		await waitFor(() =>
			expect(bridge.setBounds).toHaveBeenLastCalledWith(
				expect.objectContaining({ visible: true, rect: expect.objectContaining({ width: 320 }) }),
			),
		);
	});
});
