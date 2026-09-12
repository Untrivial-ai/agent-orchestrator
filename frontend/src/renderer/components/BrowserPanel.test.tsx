import { act, fireEvent, render as rtlRender, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BrowserPanel, BrowserPanelView, BrowserTopTabDragOverlay, useBrowserAnnotationQueue } from "./BrowserPanel";
import { reorderBrowserTabs } from "../lib/browser-tab-order";
import { useBrowserView, type BrowserNavState } from "../hooks/useBrowserView";
import { OPEN_BROWSER_OVERLAY_SELECTOR } from "../lib/dom-selectors";
import { useUiStore } from "../stores/ui-store";
import type { WorkspaceSession } from "../types/workspace";
import { TooltipProvider } from "./ui/tooltip";
import type {
	BrowserAnnotationCancelPayload,
	BrowserAnnotationContext,
	BrowserAnnotationSubmitPayload,
} from "../../shared/browser-annotations";

function render(ui: ReactElement) {
	return rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
}

const postMock = vi.hoisted(() => vi.fn());

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock },
	apiErrorMessage: (error: unknown, fallback = "Request failed") =>
		typeof error === "object" && error !== null && "message" in error
			? String((error as { message: unknown }).message)
			: fallback,
}));

const hookState = vi.hoisted(() => ({
	navigate: vi.fn(),
	goBack: vi.fn(),
	goForward: vi.fn(),
	reload: vi.fn(),
	stop: vi.fn(),
	selectTab: vi.fn(),
	closeTab: vi.fn(),
	openTab: vi.fn(),
	reorderTabs: vi.fn(),
	closedTabs: [] as { id: string; title: string; url: string; favicon?: string }[],
	reopenClosedTab: vi.fn(),
	openDevTools: vi.fn(),
	closeDevTools: vi.fn(),
	devtoolsState: { viewId: "42:sess-1", open: false, activeTabId: "t1" },
	setAnnotationMode: vi.fn(),
	tabs: [{ id: "t1", url: "", title: "", active: true }],
	activeTabId: "t1",
	tabNotice: "",
	agentBrowserActive: false,
	agentBrowserActivity: null as { active: boolean; action?: string; phase?: "started" | "finished" } | null,
	profileState: { viewId: "42:sess-1", profileId: null as string | null, temporary: true },
	previewUrl: undefined as string | undefined,
	navState: {
		viewId: "42:sess-1",
		url: "",
		title: "",
		canGoBack: false,
		canGoForward: false,
		isLoading: false,
	} as BrowserNavState,
}));

vi.mock("../hooks/useBrowserView", () => ({
	useBrowserView: (options: { previewUrl?: string }) => {
		hookState.previewUrl = options.previewUrl;
		return {
			viewId: "42:sess-1",
			navState: hookState.navState,
			slotRef: vi.fn(),
			navigate: hookState.navigate,
			goBack: hookState.goBack,
			goForward: hookState.goForward,
			reload: hookState.reload,
			stop: hookState.stop,
			tabs: hookState.tabs,
			activeTabId: hookState.activeTabId,
			tabNotice: hookState.tabNotice,
			selectTab: hookState.selectTab,
			closeTab: hookState.closeTab,
			openTab: hookState.openTab,
			reorderTabs: hookState.reorderTabs,
			closedTabs: hookState.closedTabs,
			reopenClosedTab: hookState.reopenClosedTab,
			agentBrowserActive: hookState.agentBrowserActive,
			agentBrowserActivity: hookState.agentBrowserActivity,
			profileState: hookState.profileState,
			devtoolsState: hookState.devtoolsState,
			openDevTools: hookState.openDevTools,
			closeDevTools: hookState.closeDevTools,
			annotationMode: false,
			setAnnotationMode: hookState.setAnnotationMode,
		};
	},
}));

const session: WorkspaceSession = {
	id: "sess-1",
	workspaceId: "ws-1",
	workspaceName: "my-app",
	title: "do the thing",
	provider: "claude-code",
	kind: "worker",
	branch: "feat/ns",
	status: "needs_input",
	updatedAt: "2026-06-15T00:00:00Z",
	prs: [],
};

it("reorders browser tabs around the drop target", () => {
	expect(reorderBrowserTabs(["t1", "t2", "t3"], "t1", "t3")).toEqual(["t2", "t3", "t1"]);
	expect(reorderBrowserTabs(["t1", "t2", "t3"], "t2", "missing")).toBeNull();
});

type ElementAnnotationPayload = BrowserAnnotationSubmitPayload & {
	selection: { kind: "element"; context: BrowserAnnotationContext };
};

function annotationPayload(instruction: string): ElementAnnotationPayload {
	return {
		viewId: "42:sess-1",
		instruction,
		selection: {
			kind: "element",
			context: {
				url: "http://localhost:5173/",
				tag: "button",
				classes: [],
				selector: "button",
				size: { width: 80, height: 30 },
				computedStyle: {},
			},
		},
	};
}

function PersistentBrowserPanelView({
	currentSession,
	visible,
}: {
	currentSession: WorkspaceSession;
	visible: boolean;
}) {
	const browserView = useBrowserView({
		sessionId: currentSession.id,
		active: true,
		poppedOut: false,
		previewUrl: currentSession.previewUrl,
		previewRevision: currentSession.previewRevision,
	});
	const annotationQueue = useBrowserAnnotationQueue({
		sessionId: currentSession.id,
		navUrl: browserView.navState.url,
	});
	if (!visible) return null;
	return (
		<BrowserPanelView
			active
			annotationQueue={annotationQueue}
			browserView={browserView}
			onTogglePopOut={() => undefined}
			poppedOut={false}
			session={currentSession}
		/>
	);
}

describe("BrowserPanel", () => {
	const annotationSubmitListeners = new Set<(payload: BrowserAnnotationSubmitPayload) => void>();
	const annotationCancelListeners = new Set<(payload: BrowserAnnotationCancelPayload) => void>();
	let focusLocationListener: ((viewId: string) => void) | undefined;
	let reopenClosedTabListener: ((viewId: string) => void) | undefined;
	const pageFocusListeners = new Set<(viewId: string) => void>();

	async function openBrowserControls() {
		await userEvent.click(screen.getByRole("button", { name: "Browser controls" }));
	}

	async function openDevicePresets() {
		await openBrowserControls();
		await userEvent.click(screen.getByRole("menuitem", { name: "Device preset" }));
	}

	beforeEach(() => {
		useUiStore.setState({ globalToast: null, settingsModal: null });
		hookState.navigate.mockReset();
		hookState.goBack.mockReset();
		hookState.goForward.mockReset();
		hookState.reload.mockReset();
		hookState.stop.mockReset();
		hookState.selectTab.mockReset();
		hookState.closeTab.mockReset();
		hookState.reopenClosedTab.mockReset();
		hookState.closedTabs = [];
		hookState.openDevTools.mockReset();
		hookState.closeDevTools.mockReset();
		hookState.devtoolsState = { viewId: "42:sess-1", open: false, activeTabId: "t1" };
		hookState.navState = {
			viewId: "42:sess-1",
			url: "",
			title: "",
			canGoBack: false,
			canGoForward: false,
			isLoading: false,
		};
		hookState.setAnnotationMode.mockReset();
		hookState.setAnnotationMode.mockResolvedValue(undefined);
		postMock.mockReset();
		postMock.mockResolvedValue({ data: {} });
		annotationSubmitListeners.clear();
		annotationCancelListeners.clear();
		pageFocusListeners.clear();
		window.ao!.browser.onPageFocus = vi.fn((listener: (viewId: string) => void) => {
			pageFocusListeners.add(listener);
			return () => pageFocusListeners.delete(listener);
		});
		window.ao!.browser.onAnnotationSubmit = vi.fn((listener: (payload: BrowserAnnotationSubmitPayload) => void) => {
			annotationSubmitListeners.add(listener);
			return () => {
				annotationSubmitListeners.delete(listener);
			};
		});
		window.ao!.browser.onAnnotationCancel = vi.fn((listener: (payload: BrowserAnnotationCancelPayload) => void) => {
			annotationCancelListeners.add(listener);
			return () => {
				annotationCancelListeners.delete(listener);
			};
		});
		window.ao!.browser.historySuggestions = vi.fn(async () => []);
		window.ao!.browser.captureScreenshot = vi.fn(async () => undefined);
		window.ao!.browser.downloads.list = vi.fn(async () => ({ downloads: [] }));
		window.ao!.browser.selectProfile = vi.fn(async () => undefined);
		window.ao!.browserProfiles.list = vi.fn(async () => ({ profiles: [] }));
		window.ao!.browser.notifyPanelUsed = vi.fn();
		window.ao!.browser.notifyPanelBlur = vi.fn();
		window.ao!.browser.onFocusLocation = vi.fn((listener: (viewId: string) => void) => {
			focusLocationListener = listener;
			return () => {
				if (focusLocationListener === listener) focusLocationListener = undefined;
			};
		});
		window.ao!.browser.onReopenClosedTab = vi.fn((listener: (viewId: string) => void) => {
			reopenClosedTabListener = listener;
			return () => {
				if (reopenClosedTabListener === listener) reopenClosedTabListener = undefined;
			};
		});
		hookState.previewUrl = undefined;
		hookState.profileState = { viewId: "42:sess-1", profileId: null, temporary: true };
		hookState.tabs = [{ id: "t1", url: "", title: "", active: true }];
		hookState.activeTabId = "t1";
		hookState.tabNotice = "";
		hookState.navState = {
			viewId: "42:sess-1",
			url: "",
			title: "",
			canGoBack: false,
			canGoForward: false,
			isLoading: false,
		};
	});

	it("navigates to the entered URL on submit", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const input = screen.getByRole("textbox", { name: /browser url/i });

		await userEvent.clear(input);
		await userEvent.type(input, "localhost:5173{Enter}");

		expect(hookState.navigate).toHaveBeenCalledWith("localhost:5173");
		expect(input).not.toHaveFocus();
	});

	it("supports consecutive address-bar navigations after refocusing", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const input = screen.getByRole("textbox", { name: /browser url/i });

		await userEvent.clear(input);
		await userEvent.type(input, "first.example{Enter}");
		await userEvent.click(input);
		await userEvent.clear(input);
		await userEvent.type(input, "second.example{Enter}");

		expect(hookState.navigate).toHaveBeenNthCalledWith(1, "first.example");
		expect(hookState.navigate).toHaveBeenNthCalledWith(2, "second.example");
		expect(input).not.toHaveFocus();
	});

	it("shows imported history through native address-bar suggestions without adding an overlay", async () => {
		hookState.profileState = {
			viewId: "42:sess-1",
			profileId: "11111111-1111-4111-8111-111111111111",
			temporary: false,
		};
		window.ao!.browser.historySuggestions = vi.fn(async () => [
			{ url: "https://github.com/openai", title: "OpenAI" },
		]);
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const input = screen.getByRole("textbox", { name: /browser url/i });

		await userEvent.type(input, "git");

		await waitFor(() => expect(window.ao!.browser.historySuggestions).toHaveBeenCalledWith({
			viewId: "42:sess-1",
			query: "git",
		}), { timeout: 2_000 });
		await waitFor(() => expect(document.querySelector("datalist option")).not.toBeNull());
		const option = document.querySelector("datalist option")!;
		expect(option).toHaveValue("https://github.com/openai");
		expect(input).toHaveAttribute("list", option.closest("datalist")?.id);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

		fireEvent.change(input, { target: { value: "https://github.com/openai" } });
		expect(hookState.navigate).toHaveBeenCalledWith("https://github.com/openai");
		expect(input).not.toHaveFocus();
		expect(document.querySelector("datalist option")).toBeNull();
	});

	it("does not search imported history until the address is edited", async () => {
		hookState.profileState = {
			viewId: "42:sess-1",
			profileId: "11111111-1111-4111-8111-111111111111",
			temporary: false,
		};
		hookState.navState = { ...hookState.navState, url: "https://example.com/current" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const input = screen.getByRole("textbox", { name: /browser url/i });

		fireEvent.focus(input);
		await new Promise((resolve) => window.setTimeout(resolve, 150));
		expect(window.ao!.browser.historySuggestions).not.toHaveBeenCalled();

		await userEvent.clear(input);
		await userEvent.type(input, "exa");
		await waitFor(() =>
			expect(window.ao!.browser.historySuggestions).toHaveBeenCalledWith({
				viewId: "42:sess-1",
				query: "exa",
			}),
		);
	});

	it("marks browser UI as used and focuses the address bar for a matching shortcut request", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const input = screen.getByRole("textbox", { name: /browser url/i }) as HTMLInputElement;
		input.value = "http://localhost:5173/path";

		act(() => focusLocationListener?.("42:sess-1"));

		expect(input).toHaveFocus();
		expect(input.selectionStart).toBe(0);
		expect(input.selectionEnd).toBe(input.value.length);
		expect(window.ao!.browser.notifyPanelUsed).toHaveBeenCalledWith("42:sess-1");
	});

	it("reopens the most recently closed tab for a matching shortcut request", () => {
		hookState.closedTabs = [
			{ id: "latest", url: "http://localhost:5173/latest", title: "Latest" },
			{ id: "older", url: "http://localhost:5173/older", title: "Older" },
		];
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		act(() => reopenClosedTabListener?.("42:sess-1"));

		expect(hookState.reopenClosedTab).toHaveBeenCalledWith();
	});

	it("shows only the site address until the URL input is focused", () => {
		hookState.navState = {
			...hookState.navState,
			url: "https://www.google.com/search?q=agent+orchestrator#results",
		};

		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		const input = screen.getByRole("textbox", { name: /browser url/i });
		expect(input).toHaveValue("google.com");
		expect(input).toHaveClass("text-center");
	});

	it("expands the URL input, reveals the full URL, and selects it on focus", async () => {
		const url = "https://www.google.com/search?q=agent+orchestrator#results";
		hookState.navState = { ...hookState.navState, url, canGoBack: true };
		const user = userEvent.setup();
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const toolbar = screen.getByTestId("browser-toolbar");
		const input = screen.getByRole("textbox", { name: /browser url/i }) as HTMLInputElement;

		await user.click(input);

		await waitFor(() => {
			expect(input).toHaveValue(url);
			expect(input.selectionStart).toBe(0);
			expect(input.selectionEnd).toBe(url.length);
		});
		expect(input).not.toHaveClass("text-center");
		expect(toolbar).toHaveClass("browser-panel__toolbar--url-takeover");
		expect(within(toolbar).getByRole("button", { name: /back/i })).toHaveClass("browser-panel__navigation-btn");
		expect(within(toolbar).getByRole("button", { name: /back/i }).parentElement).toHaveClass("browser-panel__navigation-control");
		expect(within(toolbar).getByRole("button", { name: /forward/i })).toHaveClass("browser-panel__navigation-btn");
		expect(within(toolbar).getByRole("button", { name: /forward/i }).parentElement).toHaveClass("browser-panel__navigation-control");
		expect(within(toolbar).getByRole("button", { name: /reload/i })).toHaveClass("browser-panel__navigation-btn");

		act(() => {
			for (const listener of pageFocusListeners) listener("42:sess-1");
		});

		expect(input).toHaveValue("google.com");
		expect(toolbar).not.toHaveClass("browser-panel__toolbar--url-takeover");
		expect(within(toolbar).getByRole("button", { name: /back/i })).toBeInTheDocument();
	});

	it("keeps the normal toolbar and full URL while maximized", async () => {
		const url = "https://www.google.com/search?q=agent+orchestrator";
		hookState.navState = { ...hookState.navState, url };
		const user = userEvent.setup();
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut session={session} />);
		const toolbar = screen.getByTestId("browser-toolbar");
		const input = screen.getByRole("textbox", { name: /browser url/i });

		expect(input).toHaveValue(url);
		expect(input).not.toHaveClass("text-center");
		await user.click(input);

		expect(toolbar).not.toHaveClass("browser-panel__toolbar--url-takeover");
		expect(within(toolbar).getByRole("button", { name: /back/i })).toBeInTheDocument();
		expect(within(toolbar).getByRole("button", { name: /reload/i })).toBeInTheDocument();
	});

	it("opens the current page in the system browser from the address bar", async () => {
		const url = "https://www.google.com/search?q=agent+orchestrator";
		hookState.navState = { ...hookState.navState, url };
		const openExternal = vi.spyOn(window.ao!.app, "openExternal").mockResolvedValue(undefined);
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		fireEvent.click(screen.getByRole("button", { name: /open in system browser/i }));

		await waitFor(() => expect(openExternal).toHaveBeenCalledWith(url));
		openExternal.mockRestore();
	});

	it("keeps secondary browser controls compact until device presets are requested", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		await openBrowserControls();
		expect(screen.getByRole("menuitem", { name: "Device preset" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: /Browser profile/ })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Open DevTools" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: /iPhone SE/ })).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole("menuitem", { name: "Device preset" }));
		expect(screen.getByRole("menuitem", { name: /iPhone SE/ })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: /iPhone SE/ }).parentElement).toHaveClass("board-scrollbar");
	});

	it("captures the active page from the controls menu and confirms the clipboard copy", async () => {
		hookState.navState = { ...hookState.navState, url: "https://example.test/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		await openBrowserControls();
		await userEvent.click(screen.getByRole("menuitem", { name: "Take a screenshot" }));

		expect(window.ao!.browser.captureScreenshot).toHaveBeenCalledWith("42:sess-1");
		await waitFor(() =>
			expect(useUiStore.getState().globalToast?.title).toBe("Screenshot copied to clipboard"),
		);
		expect(useUiStore.getState().globalToast?.placement).toBe("top-center");
	});

	it("disables screenshots until the active tab has a page", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		await openBrowserControls();
		expect(screen.getByRole("menuitem", { name: "Take a screenshot" })).toHaveAttribute("data-disabled");
	});

	it("opens download history from the browser controls menu", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		await openBrowserControls();
		await userEvent.click(screen.getByRole("menuitem", { name: "Downloads" }));
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "global", section: "browserProfiles" });
	});

	it("does not show the Downloads tooltip when its menu returns focus", async () => {
		window.ao!.browser.downloads.list = vi.fn(async () => ({
			downloads: [{
				id: "download-1",
				fileName: "report.pdf",
				receivedBytes: 100,
				totalBytes: 100,
				status: "completed" as const,
				startedAt: 1,
				updatedAt: 2,
			}],
		}));
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		// A newly observed download opens the menu automatically. Close that first,
		// then exercise the user's explicit open/close flow.
		expect(await screen.findByText("report.pdf")).toBeInTheDocument();
		await userEvent.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByText("report.pdf")).not.toBeInTheDocument());
		const trigger = screen.getByRole("button", { name: "Downloads" });
		await userEvent.click(trigger);
		expect(screen.getByText("report.pdf")).toBeInTheDocument();
		await userEvent.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByText("report.pdf")).not.toBeInTheDocument());
		expect(trigger).toHaveFocus();
		expect(screen.queryByRole("tooltip", { name: "Downloads" })).not.toBeInTheDocument();
	});

	it("keeps browser profiles inside the AO controls menu", async () => {
		window.ao!.browserProfiles.list = vi.fn(async () => ({
			profiles: [
				{
					id: "11111111-1111-4111-8111-111111111111",
					name: "Work",
					createdAt: "2026-01-01T00:00:00.000Z",
					updatedAt: "2026-01-01T00:00:00.000Z",
				},
			],
		}));
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		expect(screen.queryByRole("button", { name: /Browser profile:/ })).not.toBeInTheDocument();
		await openBrowserControls();
		await userEvent.click(screen.getByRole("menuitem", { name: /Browser profile/ }));

		expect(await screen.findByRole("menuitem", { name: "Temporary" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Work" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Manage browser profiles" })).toBeInTheDocument();
		await userEvent.click(screen.getByRole("menuitem", { name: "Work" }));
		expect(window.ao!.browser.selectProfile).toHaveBeenCalledWith(
			expect.objectContaining({
				viewId: "42:sess-1",
				profileId: "11111111-1111-4111-8111-111111111111",
			}),
		);
	});

	it("constrains the device frame to a named preset's width, and clears it back to fit", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const frame = screen.getByTestId("browser-device-frame").parentElement as HTMLElement;
		expect(frame.style.width).toBe("");

		await openDevicePresets();
		await userEvent.click(screen.getByRole("menuitem", { name: /iPhone SE/ }));
		expect(frame.style.width).toBe("375px");

		await openDevicePresets();
		await userEvent.click(screen.getByRole("menuitem", { name: /iPad Mini/ }));
		expect(frame.style.width).toBe("768px");

		await openDevicePresets();
		await userEvent.click(screen.getByRole("menuitem", { name: "Fit panel" }));
		expect(frame.style.width).toBe("");
	});

	it("applies a custom device-frame width typed into the dropdown, clamped to a sane range", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const frame = screen.getByTestId("browser-device-frame").parentElement as HTMLElement;

		await openDevicePresets();
		const customWidthInput = screen.getByLabelText("Custom width") as HTMLInputElement;
		await userEvent.clear(customWidthInput);
		await userEvent.type(customWidthInput, "600");
		expect(frame.style.width).toBe("600px");

		await userEvent.clear(customWidthInput);
		await userEvent.type(customWidthInput, "10");
		expect(frame.style.width).toBe("240px");
	});

	// Regression: the reviewer flagged that the original 6-device list should
	// match Chrome DevTools' own "Standard" device list rather than a
	// hand-picked subset.
	it("offers Chrome DevTools' own standard device list", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		await openDevicePresets();

		for (const name of [
			"iPhone SE",
			"iPhone XR",
			"iPhone 12 Pro",
			"iPhone 14 Pro Max",
			"iPhone 15 Pro Max",
			"iPhone 16 Pro Max",
			"Pixel 7",
			"Pixel 8",
			"Pixel 9",
			"Pixel 10",
			"Samsung Galaxy S8+",
			"Samsung Galaxy S20 Ultra",
			"Samsung Galaxy A51/71",
			"iPad Mini",
			"iPad Air",
			"iPad Pro",
			"Surface Pro 7",
			"Surface Duo",
			"Galaxy Z Fold 5",
			"Asus Zenbook Fold",
			"Nest Hub Max",
		]) {
			expect(screen.getByRole("menuitem", { name: new RegExp(name.replace(/[+.]/g, "\\$&")) })).toBeInTheDocument();
		}
		// "Nest Hub" alone is a prefix of "Nest Hub Max" — assert it separately
		// with a negative lookahead so the two rows aren't ambiguous.
		expect(screen.getByRole("menuitem", { name: /Nest Hub(?! Max)/ })).toBeInTheDocument();
	});

	it("marks the device-preset dropdown as a browser overlay so it paints above the live page", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		await openDevicePresets();

		const menu = screen.getByRole("menu");
		expect(menu.getAttribute("data-browser-native-overlay")).toBe("true");
	});

	it("does not queue the controls tooltip when the dropdown returns focus to its trigger", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const trigger = screen.getByRole("button", { name: "Browser controls" });

		await userEvent.click(trigger);
		expect(screen.getByRole("menuitem", { name: "Device preset" })).toBeInTheDocument();
		fireEvent.pointerLeave(trigger);
		fireEvent.pointerDown(document.body);
		await waitFor(() => expect(screen.queryByRole("menuitem", { name: "Device preset" })).not.toBeInTheDocument());
		fireEvent.focus(trigger);

		await new Promise((resolve) => setTimeout(resolve, 450));
		expect(document.querySelector('[data-slot="tooltip-content"]')).toBeNull();
	});

	it("still shows the controls tooltip from an actual mouse hover", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const trigger = screen.getByRole("button", { name: "Browser controls" });

		await userEvent.hover(trigger);

		expect(await screen.findByRole("tooltip")).toHaveTextContent("Browser controls");
	});

	it("keeps the URL input editable while the browser is maximized", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut session={session} />);
		const input = screen.getByRole("textbox", { name: /browser url/i });

		await userEvent.clear(input);
		await userEvent.type(input, "http://localhost:4173/");

		expect(input).toHaveValue("http://localhost:4173/");
	});

	it("keeps the maximized tab rail on the right side of the viewport", () => {
		window.localStorage.removeItem("ao-browser-tabs-w");
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut session={session} />);

		const viewport = screen.getByTestId("browser-viewport");
		const rail = screen.getByTestId("browser-tabs-rail");
		const resizeHandle = screen.getByTestId("browser-tabs-resize-handle");
		expect(viewport.nextElementSibling).toBe(rail);
		expect(rail).toHaveClass("border-l");
		expect(rail).not.toHaveClass("border-r");
		expect(resizeHandle).toHaveClass("left-0");

		fireEvent.pointerDown(resizeHandle, { clientX: 220 });
		fireEvent.pointerMove(window, { clientX: 190 });
		fireEvent.pointerUp(window);

		expect(document.documentElement.style.getPropertyValue("--ao-browser-tabs-w")).toBe("250px");
		expect(window.localStorage.getItem("ao-browser-tabs-w")).toBe("250");
		window.localStorage.removeItem("ao-browser-tabs-w");
	});

	it("threads the session preview URL into the browser view (which drives navigation)", () => {
		render(
			<BrowserPanel
				active
				onTogglePopOut={() => undefined}
				poppedOut={false}
				session={{ ...session, previewUrl: "file:///tmp/preview/index.html" }}
			/>,
		);

		expect(hookState.previewUrl).toBe("file:///tmp/preview/index.html");
	});

	it("uses the active app theme for the static browser preview", () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		const ao = window.ao;
		Object.defineProperty(window, "ao", { configurable: true, value: undefined });
		try {
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

			const preview = screen.getByText("Demo app preview").closest(".bg-preview, .bg-background");
			expect(preview).toHaveClass("bg-background", "text-foreground");
		} finally {
			Object.defineProperty(window, "ao", { configurable: true, value: ao });
		}
	});

	it("binds navigation controls to nav state", async () => {
		hookState.navState = {
			viewId: "42:sess-1",
			url: "http://localhost:5173/",
			title: "Local app",
			canGoBack: true,
			canGoForward: false,
			isLoading: true,
		};
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		await userEvent.click(screen.getByRole("button", { name: /back/i }));
		await userEvent.click(screen.getByRole("button", { name: /stop/i }));

		expect(hookState.goBack).toHaveBeenCalled();
		expect(screen.getByRole("button", { name: /forward/i })).toBeDisabled();
		expect(hookState.stop).toHaveBeenCalled();
	});

	it("marks toolbar tooltips as browser overlays so they paint above the live page", async () => {
		// Same reasoning as the pinned-favicon overlay test: the toolbar sits
		// directly above the native browser view, so an unmarked tooltip here
		// would render behind the live page.
		hookState.navState = {
			viewId: "42:sess-1",
			url: "http://localhost:5173/",
			title: "Local app",
			canGoBack: true,
			canGoForward: false,
			isLoading: false,
		};
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		fireEvent.focus(screen.getByRole("button", { name: /back/i }));

		const tooltip = await screen.findByRole("tooltip");
		expect(tooltip.closest('[data-browser-native-overlay="true"]')).not.toBeNull();
	});

	it("uses the same styled browser overlay tooltip while maximized", async () => {
		hookState.navState = {
			viewId: "42:sess-1",
			url: "http://localhost:5173/",
			title: "Local app",
			canGoBack: true,
			canGoForward: false,
			isLoading: false,
		};
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut session={session} />);

		const annotateButton = screen.getByRole("button", { name: /annotate page/i });
		expect(annotateButton.closest("[title]")).toBeNull();
		fireEvent.focus(annotateButton);

		const tooltip = await screen.findByRole("tooltip");
		expect(tooltip).toHaveTextContent("Annotate page");
		expect(tooltip.closest('[data-browser-native-overlay="true"]')).not.toBeNull();
	});

	it("still opens a tooltip for a disabled toolbar button", async () => {
		// Disabled buttons never dispatch pointer/focus events natively, so the
		// hover listener has to live on a wrapping span around the button rather
		// than on the (potentially disabled) button itself.
		hookState.navState = {
			viewId: "42:sess-1",
			url: "http://localhost:5173/",
			title: "Local app",
			canGoBack: false,
			canGoForward: false,
			isLoading: false,
		};
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		const backButton = screen.getByRole("button", { name: /back/i });
		expect(backButton).toBeDisabled();
		const wrapper = backButton.parentElement;
		expect(wrapper?.tagName).toBe("SPAN");

		fireEvent.pointerMove(wrapper!, { pointerType: "mouse" });

		expect(await screen.findByRole("tooltip")).toHaveTextContent(/back/i);
	});

	it("lets the user select a tab from the hover flyout", async () => {
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
		];
		hookState.activeTabId = "t2";
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		// Docked defaults to a collapsed (0px) rail once there's more than one
		// tab, so tabs are only reachable through the hover flyout unless the
		// user has pinned the rail — same open sequence as the flyout tests below.
		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
			act(() => {
				vi.advanceTimersByTime(300);
			});
		} finally {
			vi.useRealTimers();
		}

		await userEvent.click(screen.getByRole("button", { name: "First app" }));

		await waitFor(() => expect(hookState.selectTab).toHaveBeenCalledWith("t1"));
	});

	it("shows browser tabs in a horizontal tab strip and selects them", async () => {
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
		];
		hookState.activeTabId = "t2";
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		const tabList = screen.getByRole("tablist", { name: "Browser tabs" });
		const firstTab = within(tabList).getByRole("tab", { name: "First app" });
		const secondTab = within(tabList).getByRole("tab", { name: "Second app" });

		expect(firstTab).toHaveAttribute("aria-selected", "false");
		expect(secondTab).toHaveAttribute("aria-selected", "true");
		await userEvent.click(firstTab);
		expect(hookState.selectTab).toHaveBeenCalledWith("t1");
	});

	it("renders the complete tab chrome in the drag overlay", () => {
		render(
			<BrowserTopTabDragOverlay
				onlyTab={false}
				tab={{ id: "t1", url: "http://localhost:3000/", title: "First app", active: true }}
			/>,
		);

		const overlay = screen.getByTestId("browser-tab-drag-overlay");
		expect(overlay).toHaveClass("browser-panel__tab--drag-overlay");
		expect(overlay).toHaveTextContent("First app");
		expect(overlay.querySelector(".browser-panel__tab-icon")).not.toBeNull();
		expect(overlay.querySelector(".browser-panel__tab-close")).not.toBeNull();
	});

	it("moves browser tab focus and selection with arrow keys", async () => {
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: true },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: false },
		];
		hookState.activeTabId = "t1";
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut session={session} />);

		const tabList = screen.getByRole("tablist", { name: "Browser tabs" });
		const firstTab = within(tabList).getByRole("tab", { name: "First app" });
		const secondTab = within(tabList).getByRole("tab", { name: "Second app" });
		firstTab.focus();
		await userEvent.keyboard("{ArrowRight}");

		expect(secondTab).toHaveFocus();
		expect(hookState.selectTab).toHaveBeenCalledWith("t2");
	});

	it("closes a browser tab from the horizontal tab strip", async () => {
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: true },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: false },
		];
		hookState.activeTabId = "t1";
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut session={session} />);

		const tabList = screen.getByRole("tablist", { name: "Browser tabs" });
		await userEvent.click(within(tabList).getByRole("button", { name: "Close tab Second app" }));

		expect(hookState.closeTab).toHaveBeenCalledWith("t2");
	});

	it("opens a new browser tab from the horizontal tab strip", async () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut session={session} />);

		await userEvent.click(within(screen.getByTestId("browser-tab-bar")).getByRole("button", { name: "Open new tab" }));

		expect(hookState.openTab).toHaveBeenCalledOnce();
	});

	it("keeps both responsive tab placements available in docked mode", () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		expect(screen.getByTestId("browser-tab-bar")).toBeInTheDocument();
		expect(screen.getByTestId("browser-tabs-rail")).toBeInTheDocument();
	});

	it("does not render a tab-specific agent marker", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
		];
		hookState.activeTabId = "t2";
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
			act(() => {
				vi.advanceTimersByTime(300);
			});
		} finally {
			vi.useRealTimers();
		}

		expect(screen.queryByText("Agent", { exact: true })).not.toBeInTheDocument();
	});

	it("opens DevTools from the compact browser controls menu", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:3000/" };
		const { rerender } = render(
			<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />,
		);
		const toolbarButtonCount = screen.getAllByRole("button").length;

		const controls = screen.getByRole("button", { name: "Browser controls" });
		expect(controls).not.toHaveAttribute("aria-pressed");
		await openBrowserControls();
		await userEvent.click(screen.getByRole("menuitem", { name: "Open DevTools" }));
		expect(hookState.openDevTools).toHaveBeenCalledOnce();

		hookState.devtoolsState = { viewId: "42:sess-1", open: true, activeTabId: "t1" };
		rerender(<TooltipProvider><BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} /></TooltipProvider>);
		expect(screen.getAllByRole("button")).toHaveLength(toolbarButtonCount);
		expect(controls).not.toHaveAttribute("aria-pressed");
		expect(controls).not.toHaveClass("bg-accent-strong", "text-accent-foreground");
		await openBrowserControls();
		await userEvent.click(screen.getByRole("menuitem", { name: "Close DevTools" }));
		expect(hookState.closeDevTools).toHaveBeenCalledOnce();
	});

	it("does not highlight browser controls when a saved profile is active", () => {
		hookState.profileState = {
			viewId: "42:sess-1",
			profileId: "11111111-1111-4111-8111-111111111111",
			temporary: false,
		};
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		const controls = screen.getByRole("button", { name: "Browser controls" });
		expect(controls).not.toHaveAttribute("aria-pressed");
		expect(controls).not.toHaveClass("bg-accent-strong", "text-accent-foreground");
	});

	it("disables DevTools in the controls menu until the active tab has a page", async () => {
		const { rerender } = render(
			<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />,
		);
		await openBrowserControls();
		expect(screen.getByRole("menuitem", { name: "Open DevTools" })).toHaveAttribute("data-disabled");

		hookState.navState = { ...hookState.navState, url: "http://localhost:3000/" };
		rerender(<TooltipProvider><BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} /></TooltipProvider>);
		expect(screen.getByRole("menuitem", { name: "Open DevTools" })).not.toHaveAttribute("data-disabled");
	});

	it("marks blank native panels as opaque and loaded panels as live", () => {
		const { rerender } = render(
			<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />,
		);
		expect(screen.getByTestId("browser-panel")).toHaveAttribute("data-browser-native-page", "empty");

		hookState.navState = { ...hookState.navState, url: "http://localhost:3000/" };
		rerender(<TooltipProvider><BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} /></TooltipProvider>);
		expect(screen.getByTestId("browser-panel")).toHaveAttribute("data-browser-native-page", "live");
	});

	it("releases the tabs overlay when tab selection fails", async () => {
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: true },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: false },
		];
		hookState.selectTab.mockRejectedValueOnce(new Error("selection failed"));
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
			act(() => {
				vi.advanceTimersByTime(300);
			});
		} finally {
			vi.useRealTimers();
		}

		await userEvent.click(screen.getByRole("button", { name: "Second app" }));

		await waitFor(() => expect(hookState.selectTab).toHaveBeenCalledWith("t2"));
	});

	it("opens the flyout on hover, after the hover-intent delay", () => {
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: true },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: false },
		];
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const rail = screen.getByTestId("browser-tabs-rail");
		const flyout = screen.getByTestId("browser-tabs-flyout");

		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(rail);
			expect(flyout).toHaveAttribute("data-state", "closed");

			act(() => {
				vi.advanceTimersByTime(300);
			});
			expect(flyout).toHaveAttribute("data-state", "open");
			expect(flyout).toHaveTextContent("First app");

			fireEvent.pointerLeave(rail);
			act(() => {
				vi.advanceTimersByTime(300);
			});
			expect(flyout).toHaveAttribute("data-state", "closed");
		} finally {
			vi.useRealTimers();
		}
	});

	it("opens recently closed tabs from the toolbar trigger when only one tab remains", () => {
		hookState.tabs = [{ id: "t1", url: "http://localhost:3000/", title: "Only app", active: true }];
		hookState.closedTabs = [{ id: "closed", url: "http://localhost:4173/", title: "Closed app" }];
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const trigger = screen.getByRole("button", { name: "1 browser tab" });
		const flyout = screen.getByTestId("browser-tabs-flyout");

		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(trigger);
			act(() => vi.advanceTimersByTime(300));

			expect(flyout).toHaveAttribute("data-state", "open");
			expect(flyout).toHaveTextContent("Recently closed");
			expect(flyout).toHaveTextContent("Closed app");
		} finally {
			vi.useRealTimers();
		}
	});

	it("lets the user close a tab from the hover flyout", async () => {
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
		];
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
			act(() => {
				vi.advanceTimersByTime(300);
			});
		} finally {
			vi.useRealTimers();
		}

		await userEvent.click(
			within(screen.getByTestId("browser-tabs-flyout")).getByRole("button", { name: "Close tab First app" }),
		);

		expect(hookState.closeTab).toHaveBeenCalledWith("t1");
	});

	it("lets the user reopen a recently closed tab from the hover flyout", async () => {
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
		];
		hookState.closedTabs = [{ id: "t3", url: "http://localhost:5173/", title: "Closed app" }];
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
			act(() => {
				vi.advanceTimersByTime(300);
			});
		} finally {
			vi.useRealTimers();
		}

		expect(screen.getByText("Recently closed")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Reopen Closed app" }));

		expect(hookState.reopenClosedTab).toHaveBeenCalledWith("t3");
	});

	it("does not show a recently closed section when nothing has been closed", () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		expect(screen.queryByText("Recently closed")).not.toBeInTheDocument();
	});

	// Regression: ClosedBrowserTab.favicon was captured, populated, and asserted
	// in useBrowserView's tests, but the recently-closed row always rendered a
	// generic icon and never actually read it.
	it("renders a recently closed tab's favicon when it has one", async () => {
		hookState.closedTabs = [
			{ id: "t3", url: "http://localhost:5173/", title: "Closed app", favicon: "http://localhost:5173/favicon.ico" },
		];
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
			act(() => {
				vi.advanceTimersByTime(300);
			});
		} finally {
			vi.useRealTimers();
		}

		const row = screen.getByRole("button", { name: "Reopen Closed app" });
		expect(row.querySelector("img")).toHaveAttribute("src", "http://localhost:5173/favicon.ico");
	});

	it("keeps opening and reopening tabs available beyond the former cap", async () => {
		hookState.tabs = Array.from({ length: 20 }, (_, i) => ({
			id: `t${i}`,
			url: `http://localhost:3000/${i}`,
			title: `Tab ${i}`,
			active: i === 0,
		}));
		hookState.closedTabs = [{ id: "closed", url: "http://localhost:5173/", title: "Closed app" }];
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
			act(() => {
				vi.advanceTimersByTime(300);
			});
		} finally {
			vi.useRealTimers();
		}

		const row = screen.getByRole("button", { name: "Reopen Closed app" });
		expect(row).toBeEnabled();
		await userEvent.click(row);
		expect(hookState.reopenClosedTab).toHaveBeenCalledWith("closed");
		expect(screen.getAllByRole("button", { name: "Open new tab" }).every((button) => !button.hasAttribute("disabled"))).toBe(true);
	});

	it("keeps the hover flyout open after closing a tab, since the cursor is still over it", async () => {
		hookState.tabs = [
			{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
			{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
		];
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const flyout = screen.getByTestId("browser-tabs-flyout");

		vi.useFakeTimers();
		try {
			fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
			act(() => {
				vi.advanceTimersByTime(300);
			});
			expect(flyout).toHaveAttribute("data-state", "open");
		} finally {
			vi.useRealTimers();
		}

		await userEvent.click(
			within(screen.getByTestId("browser-tabs-flyout")).getByRole("button", { name: "Close tab First app" }),
		);

		expect(flyout).toHaveAttribute("data-state", "open");
	});

	it("surfaces a popup-created tab notice alongside the rail", () => {
		hookState.tabNotice = "Opened new tab";
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		// Not getByRole("status"): the rail's DndContext renders its own
		// role="status" live region for drag accessibility announcements.
		expect(screen.getByText("Opened new tab")).toBeInTheDocument();
		expect(screen.getByTestId("browser-tabs-rail")).toBeInTheDocument();
	});

	it("keeps the tabs rail on the right of the viewport whether docked or popped out", () => {
		hookState.tabs = [
			{ id: "t1", url: "http://a.test", title: "A", active: true },
			{ id: "t2", url: "http://b.test", title: "B", active: false },
		];

		const { rerender } = render(
			<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />,
		);
		let viewport = screen.getByTestId("browser-viewport");
		let rail = screen.getByTestId("browser-tabs-rail");
		expect(viewport.compareDocumentPosition(rail) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

		rerender(<TooltipProvider><BrowserPanel active onTogglePopOut={() => undefined} poppedOut session={session} /></TooltipProvider>);
		viewport = screen.getByTestId("browser-viewport");
		rail = screen.getByTestId("browser-tabs-rail");
		expect(viewport.compareDocumentPosition(rail) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
		expect(rail).toHaveClass("border-l");
		expect(rail).not.toHaveClass("border-r");
	});

	it("shows empty and error states", () => {
		hookState.navState = { ...hookState.navState, error: "Connection refused" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		expect(screen.getByText("Enter a URL or click one in the terminal.")).toBeInTheDocument();
		expect(screen.getByText("Connection refused")).toBeInTheDocument();
	});

	it("toggles pop-out mode", async () => {
		const onTogglePopOut = vi.fn();
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={onTogglePopOut} poppedOut={false} session={session} />);

		await userEvent.click(screen.getByRole("button", { name: /pop out/i }));

		expect(onTogglePopOut.mock.calls[0]?.[0]).toBe(true);
		expect(onTogglePopOut.mock.calls[0]?.[1]).toEqual(expect.objectContaining({ width: expect.any(Number) }));
	});

	it("keeps workspace sizing controls out of the browser toolbar", () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		expect(screen.queryByRole("button", { name: /focus browser workspace/i })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: /pop out/i })).toBeInTheDocument();
	});

	it("pops out an empty browser", async () => {
		const onTogglePopOut = vi.fn();
		render(<BrowserPanel active onTogglePopOut={onTogglePopOut} poppedOut={false} session={session} />);

		const popOut = screen.getByRole("button", { name: /pop out/i });
		expect(popOut).not.toBeDisabled();
		await userEvent.click(popOut);

		expect(onTogglePopOut.mock.calls[0]?.[0]).toBe(true);
	});

	it("enables annotation mode from the toolbar when a page is loaded", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		await userEvent.click(screen.getByRole("button", { name: /annotate/i }));

		expect(hookState.setAnnotationMode).toHaveBeenCalledWith(true);
	});

	it("does not render a global browser activity status", () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };

		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		expect(screen.queryByText("Agent clicking")).not.toBeInTheDocument();
		expect(screen.queryByText("Agent using browser")).not.toBeInTheDocument();
		expect(screen.queryByTestId("browser-agent-status")).not.toBeInTheDocument();
	});

	it("renders the premium browser shell hooks in the default view", () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		expect(screen.getByTestId("browser-toolbar")).toHaveClass("browser-panel__toolbar");
		expect(screen.getByTestId("browser-viewport")).toHaveClass("browser-panel__viewport");
	});

	it("does not render a globe icon in the URL input", () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		expect(screen.queryByTestId("browser-url-icon")).not.toBeInTheDocument();
	});
	it("disables annotation mode when no page is loaded", () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		expect(screen.getByRole("button", { name: /annotate/i })).toBeDisabled();
	});

	it("sends submitted annotation instructions to the session agent", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(
			<BrowserPanel
				active
				onTogglePopOut={() => undefined}
				poppedOut={false}
				session={{ ...session, status: "idle" }}
			/>,
		);

		act(() => {
			annotationSubmitListeners.forEach((listener) =>
				listener({
					viewId: "42:sess-1",
					instruction: "Make this button blue.",
					selection: {
						kind: "element",
						context: {
							url: "http://localhost:5173/",
							title: "Preview",
							tag: "button",
							id: "save",
							classes: ["primary"],
							selector: "button#save",
							size: { width: 140, height: 36 },
							visibleText: "Save changes",
							computedStyle: {},
						},
					},
				}),
			);
		});

		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
			params: { path: { sessionId: "sess-1" } },
			body: {
				message: expect.stringContaining("Make this button blue."),
			},
		});
		const body = postMock.mock.calls[0][1].body as { message: string };
		expect(body.message).toContain("button#save");
		expect(body.message.length).toBeLessThanOrEqual(4096);
	});

	it("forwards the captured snapshot as the /send body's attachment field", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(
			<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={{ ...session, status: "idle" }} />,
		);

		act(() => {
			annotationSubmitListeners.forEach((listener) =>
				listener({
					...annotationPayload("Make this button blue."),
					snapshot: { mimeType: "image/png", data: "cG5nLWJ5dGVz" },
				}),
			);
		});

		expect(await screen.findByText("Sent")).toBeInTheDocument();
		const body = postMock.mock.calls[0][1].body as { attachment?: { mimeType: string; data: string } };
		expect(body.attachment).toEqual({ mimeType: "image/png", data: "cG5nLWJ5dGVz" });
	});

	it("omits the attachment field when the payload has no snapshot", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(
			<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={{ ...session, status: "idle" }} />,
		);

		act(() => {
			annotationSubmitListeners.forEach((listener) => listener(annotationPayload("Make this button blue.")));
		});

		expect(await screen.findByText("Sent")).toBeInTheDocument();
		const body = postMock.mock.calls[0][1].body as { attachment?: unknown };
		expect(body.attachment).toBeUndefined();
	});

	it("sends a follow-up annotation without waiting for an activity-state cycle", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		act(() => {
			annotationSubmitListeners.forEach((listener) => listener(annotationPayload("Make this button blue.")));
		});
		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledTimes(1);

		act(() => {
			annotationSubmitListeners.forEach((listener) => listener(annotationPayload("Make this button green.")));
		});

		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledTimes(2);
		expect((postMock.mock.calls[1][1].body as { message: string }).message).toContain("Make this button green.");
	});

	it("serializes annotations in order exactly once while status remains working", async () => {
		let resolveFirstPost: (value: unknown) => void = () => undefined;
		let resolveSecondPost: (value: unknown) => void = () => undefined;
		postMock
			.mockReturnValueOnce(
				new Promise((resolve) => {
					resolveFirstPost = resolve;
				}),
			)
			.mockReturnValueOnce(
				new Promise((resolve) => {
					resolveSecondPost = resolve;
				}),
			)
			.mockResolvedValueOnce({ data: {} });
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(
			<BrowserPanel
				active
				onTogglePopOut={() => undefined}
				poppedOut={false}
				session={{ ...session, status: "working" }}
			/>,
		);
		const instructions = ["Make this button blue.", "Make this heading shorter.", "Reduce the card padding."];

		act(() => {
			annotationSubmitListeners.forEach((listener) => {
				instructions.forEach((instruction) => listener(annotationPayload(instruction)));
			});
		});

		expect(postMock).toHaveBeenCalledTimes(1);
		await act(async () => {
			resolveFirstPost({ data: {} });
		});
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(2));
		expect(postMock).toHaveBeenCalledTimes(2);
		await act(async () => {
			resolveSecondPost({ data: {} });
		});
		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledTimes(3);
		expect(
			postMock.mock.calls.map(
				(call) => (call[1].body as { message: string }).message.match(/Request: (.+)/)?.[1],
			),
		).toEqual(instructions);
	});

	it("preserves queued annotations while the BrowserPanelView is unmounted", async () => {
		let resolvePost: (value: unknown) => void = () => undefined;
		postMock
			.mockReturnValueOnce(
				new Promise((resolve) => {
					resolvePost = resolve;
				}),
			)
			.mockResolvedValueOnce({ data: {} });
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		const { rerender } = render(<PersistentBrowserPanelView currentSession={session} visible />);

		act(() => {
			annotationSubmitListeners.forEach((listener) => {
				listener(annotationPayload("Make this button blue."));
				listener(annotationPayload("Make this heading shorter."));
			});
		});
		expect(postMock).toHaveBeenCalledTimes(1);

		rerender(<TooltipProvider><PersistentBrowserPanelView currentSession={session} visible={false} /></TooltipProvider>);
		expect(postMock).toHaveBeenCalledTimes(1);

		await act(async () => {
			resolvePost({ data: {} });
		});
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(2));
		expect(postMock).toHaveBeenCalledTimes(2);
		expect((postMock.mock.calls[0][1].body as { message: string }).message).toContain("Make this button blue.");
		expect((postMock.mock.calls[1][1].body as { message: string }).message).toContain("Make this heading shorter.");

		rerender(<TooltipProvider><PersistentBrowserPanelView currentSession={session} visible /></TooltipProvider>);
		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect((postMock.mock.calls[1][1].body as { message: string }).message).toContain("Make this heading shorter.");
	});

	it("continues queued delivery across activity status changes", async () => {
		let resolvePost: (value: unknown) => void = () => undefined;
		postMock
			.mockReturnValueOnce(
				new Promise((resolve) => {
					resolvePost = resolve;
				}),
			)
			.mockResolvedValueOnce({ data: {} });
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		const { rerender } = render(
			<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />,
		);
		const payload: BrowserAnnotationSubmitPayload = {
			viewId: "42:sess-1",
			instruction: "Make this button yellow.",
			selection: {
				kind: "element",
				context: {
					url: "http://localhost:5173/",
					tag: "button",
					classes: [],
					selector: "button",
					size: { width: 80, height: 30 },
					computedStyle: {},
				},
			},
		};

		act(() => {
			annotationSubmitListeners.forEach((listener) => {
				listener(payload);
				listener({ ...payload, instruction: "Make this button blue." });
			});
		});
		rerender(
			<TooltipProvider>
				<BrowserPanel
					active
					onTogglePopOut={() => undefined}
					poppedOut={false}
					session={{ ...session, status: "working" }}
				/>
			</TooltipProvider>,
		);
		await act(async () => {
			resolvePost({ data: {} });
		});
		rerender(
			<TooltipProvider>
				<BrowserPanel
					active
					onTogglePopOut={() => undefined}
					poppedOut={false}
					session={{ ...session, status: "idle" }}
				/>
			</TooltipProvider>,
		);
		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledTimes(2);
	});

	it("sends submitted annotations while the session status is working", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(
			<BrowserPanel
				active
				onTogglePopOut={() => undefined}
				poppedOut={false}
				session={{ ...session, status: "working" }}
			/>,
		);

		act(() => {
			annotationSubmitListeners.forEach((listener) =>
				listener({
					viewId: "42:sess-1",
					instruction: "Move this card higher.",
					selection: {
						kind: "element",
						context: {
							url: "http://localhost:5173/",
							tag: "section",
							classes: [],
							selector: "section",
							size: { width: 320, height: 180 },
							computedStyle: {},
						},
					},
				}),
			);
		});

		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledTimes(1);
	});

	it("clears the annotation delivery confirmation after two seconds", async () => {
		vi.useFakeTimers();
		try {
			const { result } = renderHook(() =>
				useBrowserAnnotationQueue({
					sessionId: "sess-1",
					navUrl: "http://localhost:5173/",
				}),
			);

			act(() => {
				result.current.enqueue(annotationPayload("Make this button blue."));
			});
			await act(async () => {
				await Promise.resolve();
				await Promise.resolve();
			});
			expect(result.current.status).toBe("sent");

			act(() => {
				vi.advanceTimersByTime(1_999);
			});
			expect(result.current.status).toBe("sent");

			act(() => {
				vi.advanceTimersByTime(1);
			});
			expect(result.current.status).toBe("idle");
		} finally {
			vi.useRealTimers();
		}
	});

	it("shows annotation send errors", async () => {
		postMock.mockResolvedValue({ error: { message: "AO daemon is not ready." } });
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		act(() => {
			annotationSubmitListeners.forEach((listener) =>
				listener({
					viewId: "42:sess-1",
					instruction: "Make this button blue.",
					selection: {
						kind: "element",
						context: {
							url: "http://localhost:5173/",
							tag: "button",
							classes: [],
							selector: "button",
							size: { width: 80, height: 30 },
							computedStyle: {},
						},
					},
				}),
			);
		});

		expect(await screen.findByText("AO daemon is not ready.")).toBeInTheDocument();
	});

	it("keeps a failed annotation queued so the user can retry it", async () => {
		postMock
			.mockResolvedValueOnce({ error: { message: "AO daemon is not ready." } })
			.mockResolvedValueOnce({ data: {} });
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		const payload = annotationPayload("Keep my original annotation request.");

		act(() => {
			annotationSubmitListeners.forEach((listener) =>
				listener({
					...payload,
					selection: {
						kind: "element",
						context: { ...payload.selection.context, selector: "button#save" },
					},
				}),
			);
		});

		expect(await screen.findByText("AO daemon is not ready.")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledTimes(1);

		await userEvent.click(screen.getByRole("button", { name: /retry annotation/i }));

		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledTimes(2);
		const retryBody = postMock.mock.calls[1][1].body as { message: string };
		expect(retryBody.message).toContain("Keep my original annotation request.");
		expect(retryBody.message).toContain("button#save");
	});

	it("clears picking state when the page cancels annotation mode", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		await userEvent.click(screen.getByRole("button", { name: /annotate/i }));
		expect(screen.getByText("Pick element")).toBeInTheDocument();

		act(() => {
			annotationCancelListeners.forEach((listener) => listener({ viewId: "42:sess-1", reason: "escape" }));
		});

		expect(screen.queryByText("Pick element")).not.toBeInTheDocument();
	});

	it("uses AO orange for the active annotation status dot", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		const annotateButton = screen.getByRole("button", { name: /annotate/i });
		await userEvent.click(annotateButton);

		expect(annotateButton.querySelector('span[aria-hidden="true"]')).toHaveClass("bg-status-needs-you");
	});

	it("keeps the browser viewport transparent once a native page is loaded, so overlays don't blank it", () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		expect(screen.getByTestId("browser-viewport")).not.toHaveAttribute("data-placeholder");
	});

	it("keeps an opaque background behind the empty-URL placeholder", () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		expect(screen.getByTestId("browser-viewport")).toHaveAttribute("data-placeholder", "true");
	});

	it("keeps an opaque background for the static preview fallback when there is no native browser bridge", () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		const ao = window.ao;
		Object.defineProperty(window, "ao", { configurable: true, value: undefined });
		try {
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
			expect(screen.getByTestId("browser-viewport")).toHaveAttribute("data-placeholder", "true");
		} finally {
			Object.defineProperty(window, "ao", { configurable: true, value: ao });
		}
	});

	describe("pinned rail", () => {
		const pinRail = () => window.localStorage.setItem("ao.browserTabs.railPinned", "1");

		beforeEach(() => {
			window.localStorage.removeItem("ao.browserTabs.railPinned");
		});

		it("shows the close affordance without opening a competing hover overlay", () => {
			// Pinned already shows every tab as a favicon row, so the flyout would
			// just cover the live page with a duplicate of what's on screen. The
			// site tooltip is likewise suppressed while the close action is visible.
			pinRail();
			hookState.tabs = [
				{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
				{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
			];
			hookState.activeTabId = "t2";
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

			vi.useFakeTimers();
			try {
				fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
				fireEvent.pointerEnter(screen.getByRole("button", { name: "First app — localhost:3000" }));
				act(() => {
					vi.advanceTimersByTime(300);
				});
			} finally {
				vi.useRealTimers();
			}

			expect(screen.getByTestId("browser-tabs-flyout")).toHaveAttribute("data-state", "closed");
			expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
		});

		it("keeps selection and drag activation on the favicon while the close action is visible", async () => {
			pinRail();
			hookState.tabs = [
				{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
				{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
			];
			hookState.activeTabId = "t2";
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

			const rail = screen.getByTestId("browser-tabs-rail");
			const pinnedTabs = within(rail.querySelector("nav") as HTMLElement);
			const firstTab = pinnedTabs.getByRole("button", { name: "First app — localhost:3000" });
			const secondTab = pinnedTabs.getByRole("button", { name: "Second app — localhost:4173" });
			const closeButton = pinnedTabs.getByRole("button", { name: "Close tab First app" });
			fireEvent.pointerEnter(firstTab);

			expect(closeButton).toHaveClass("group-hover/tab-icon:opacity-100");
			await userEvent.click(firstTab);

			expect(hookState.selectTab).toHaveBeenCalledWith("t1");
			expect(hookState.closeTab).not.toHaveBeenCalled();

			expect(firstTab).toHaveAttribute("aria-roledescription", "sortable");
			vi.useFakeTimers();
			try {
				fireEvent.pointerDown(firstTab, {
					button: 0,
					clientX: 16,
					clientY: 48,
					isPrimary: true,
					pointerId: 1,
				});
				fireEvent.pointerMove(secondTab, { clientX: 16, clientY: 80, pointerId: 1 });

				expect(firstTab).toHaveAttribute("aria-pressed", "true");
				fireEvent.pointerUp(secondTab, { clientX: 16, clientY: 80, pointerId: 1 });
				// dnd-kit removes its capture-phase click suppressor 50 ms after
				// a completed drag. Flush that teardown before the next test.
				act(() => vi.advanceTimersByTime(50));
			} finally {
				vi.useRealTimers();
			}
			expect(secondTab).toBeInTheDocument();
			expect(closeButton).not.toHaveClass("absolute");
		});

		it("closes only from the distinct pinned-rail close action", async () => {
			pinRail();
			hookState.tabs = [
				{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
				{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
			];
			hookState.activeTabId = "t2";
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

			const rail = screen.getByTestId("browser-tabs-rail");
			const pinnedTabs = within(rail.querySelector("nav") as HTMLElement);
			const closeButton = pinnedTabs.getByRole("button", { name: "Close tab First app" });
			fireEvent.pointerEnter(pinnedTabs.getByRole("button", { name: "First app — localhost:3000" }));
			expect(closeButton).toHaveAttribute("data-state", "open");
			expect(closeButton).toBeEnabled();
			await userEvent.click(closeButton);

			expect(hookState.closeTab).toHaveBeenCalledWith("t1");
			expect(hookState.selectTab).not.toHaveBeenCalled();
			expect(screen.getByTestId("browser-tabs-flyout")).toHaveAttribute("data-state", "closed");
		});

		it("keeps the pinned close action disabled for the only tab", async () => {
			pinRail();
			hookState.tabs = [{ id: "t1", url: "http://localhost:3000/", title: "First app", active: true }];
			hookState.activeTabId = "t1";
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

			const rail = screen.getByTestId("browser-tabs-rail");
			const closeButton = within(rail.querySelector("nav") as HTMLElement).getByRole("button", {
				name: "Close tab First app",
			});
			expect(closeButton).toBeDisabled();
			await userEvent.click(closeButton);

			expect(hookState.closeTab).not.toHaveBeenCalled();
		});

		it("restores the toolbar trigger when a pinned rail drops to one tab", () => {
			pinRail();
			hookState.tabs = [{ id: "t1", url: "http://localhost:3000/", title: "Only app", active: true }];
			hookState.closedTabs = [{ id: "closed", url: "http://localhost:4173/", title: "Closed app" }];
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
			const trigger = screen.getByRole("button", { name: "1 browser tab" });
			const flyout = screen.getByTestId("browser-tabs-flyout");

			vi.useFakeTimers();
			try {
				fireEvent.pointerEnter(trigger);
				act(() => vi.advanceTimersByTime(300));

				expect(flyout).toHaveAttribute("data-state", "open");
				expect(flyout).toHaveTextContent("Recently closed");
			} finally {
				vi.useRealTimers();
			}
		});

		it("still opens the flyout on hover while the rail is collapsed", () => {
			hookState.tabs = [
				{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
				{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
			];
			hookState.activeTabId = "t2";
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

			vi.useFakeTimers();
			try {
				fireEvent.pointerEnter(screen.getByTestId("browser-tabs-rail"));
				act(() => {
					vi.advanceTimersByTime(300);
				});
			} finally {
				vi.useRealTimers();
			}

			expect(screen.getByTestId("browser-tabs-flyout")).toHaveAttribute("data-state", "open");
		});

		it("suppresses the site tooltip when pinned-tab focus reveals the close action", () => {
			pinRail();
			hookState.tabs = [
				{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
				{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
			];
			hookState.activeTabId = "t2";
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

			fireEvent.focus(screen.getByRole("button", { name: "First app — localhost:3000" }));

			expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
		});

		it("marks the focused close action as a browser overlay so it paints above the live page", () => {
			// The live page is a native view above the transparent shell; the shell
			// is only raised for elements matching OPEN_BROWSER_OVERLAY_SELECTOR, so
			// an unmarked close action extending left of the rail would be unreachable.
			pinRail();
			hookState.tabs = [
				{ id: "t1", url: "http://localhost:3000/", title: "First app", active: false },
				{ id: "t2", url: "http://localhost:4173/", title: "Second app", active: true },
			];
			hookState.activeTabId = "t2";
			render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

			fireEvent.focus(screen.getByRole("button", { name: "First app — localhost:3000" }));

			const rail = screen.getByTestId("browser-tabs-rail");
			const closeButton = within(rail.querySelector("nav") as HTMLElement).getByRole("button", {
				name: "Close tab First app",
			});
			expect(closeButton).toHaveAttribute("data-browser-native-overlay", "true");
			expect(closeButton).toHaveAttribute("data-state", "open");
			expect(document.querySelector(OPEN_BROWSER_OVERLAY_SELECTOR)).not.toBeNull();
		});
	});
});
