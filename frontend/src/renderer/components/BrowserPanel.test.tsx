import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BrowserPanel, BrowserPanelView, useBrowserAnnotationQueue } from "./BrowserPanel";
import { useBrowserView, type BrowserNavState } from "../hooks/useBrowserView";
import type { WorkspaceSession } from "../types/workspace";
import type {
	BrowserAnnotationCancelPayload,
	BrowserAnnotationContext,
	BrowserAnnotationSubmitPayload,
} from "../../shared/browser-annotations";

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
	openDevTools: vi.fn(),
	closeDevTools: vi.fn(),
	devtoolsState: { viewId: "42:sess-1", open: false, activeTabId: "t1" },
	setAnnotationMode: vi.fn(),
	tabs: [{ id: "t1", url: "", title: "", active: true }],
	activeTabId: "t1",
	tabNotice: "",
	agentBrowserActive: false,
	agentBrowserActivity: null as { active: boolean; action?: string; phase?: "started" | "finished" } | null,
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
			agentBrowserActive: hookState.agentBrowserActive,
			agentBrowserActivity: hookState.agentBrowserActivity,
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

	beforeEach(() => {
		hookState.navigate.mockReset();
		hookState.goBack.mockReset();
		hookState.goForward.mockReset();
		hookState.reload.mockReset();
		hookState.stop.mockReset();
		hookState.selectTab.mockReset();
		hookState.closeTab.mockReset();
		hookState.openDevTools.mockReset();
		hookState.closeDevTools.mockReset();
		hookState.devtoolsState = { viewId: "42:sess-1", open: false, activeTabId: "t1" };
		hookState.setAnnotationMode.mockReset();
		hookState.setAnnotationMode.mockResolvedValue(undefined);
		postMock.mockReset();
		postMock.mockResolvedValue({ data: {} });
		annotationSubmitListeners.clear();
		annotationCancelListeners.clear();
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
		hookState.previewUrl = undefined;
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
	});

	it("keeps the URL input editable while the browser is maximized", async () => {
		hookState.navState = { ...hookState.navState, url: "http://localhost:5173/" };
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut session={session} />);
		const input = screen.getByRole("textbox", { name: /browser url/i });

		await userEvent.clear(input);
		await userEvent.type(input, "http://localhost:4173/");

		expect(input).toHaveValue("http://localhost:4173/");
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

	it("opens DevTools from a direct toolbar control", async () => {
		const { rerender } = render(
			<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />,
		);
		const toolbarButtonCount = screen.getAllByRole("button").length;

		const openButton = screen.getByRole("button", { name: "Open DevTools" });
		expect(openButton).toHaveAttribute("aria-pressed", "false");
		await userEvent.click(openButton);
		expect(hookState.openDevTools).toHaveBeenCalledOnce();

		hookState.devtoolsState = { viewId: "42:sess-1", open: true, activeTabId: "t1" };
		rerender(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
		expect(screen.getAllByRole("button")).toHaveLength(toolbarButtonCount);
		const closeButton = screen.getByRole("button", { name: "Close DevTools" });
		expect(closeButton).toHaveAttribute("aria-pressed", "true");
		expect(closeButton).toHaveClass(
			"bg-accent-strong",
			"text-accent-foreground",
			"hover:bg-accent-strong",
			"dark:hover:bg-accent-strong",
		);
		await userEvent.click(closeButton);
		expect(hookState.closeDevTools).toHaveBeenCalledOnce();
	});

	it("marks blank native panels as opaque and loaded panels as live", () => {
		const { rerender } = render(
			<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />,
		);
		expect(screen.getByTestId("browser-panel")).toHaveAttribute("data-browser-native-page", "empty");

		hookState.navState = { ...hookState.navState, url: "http://localhost:3000/" };
		rerender(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);
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

		await userEvent.click(screen.getByRole("button", { name: "Close tab First app" }));

		expect(hookState.closeTab).toHaveBeenCalledWith("t1");
	});

	it("surfaces a popup-created tab notice alongside the rail", () => {
		hookState.tabNotice = "Opened new tab";
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		// Not getByRole("status"): the rail's DndContext renders its own
		// role="status" live region for drag accessibility announcements.
		expect(screen.getByText("Opened new tab")).toBeInTheDocument();
		expect(screen.getByTestId("browser-tabs-rail")).toBeInTheDocument();
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

		expect(onTogglePopOut).toHaveBeenCalledWith(true);
	});

	it("pops out an empty browser", async () => {
		const onTogglePopOut = vi.fn();
		render(<BrowserPanel active onTogglePopOut={onTogglePopOut} poppedOut={false} session={session} />);

		const popOut = screen.getByRole("button", { name: /pop out/i });
		expect(popOut).not.toBeDisabled();
		await userEvent.click(popOut);

		expect(onTogglePopOut).toHaveBeenCalledWith(true);
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

	it("keeps URL icon placement controlled by the browser shell CSS", () => {
		render(<BrowserPanel active onTogglePopOut={() => undefined} poppedOut={false} session={session} />);

		const icon = screen.getByTestId("browser-url-icon");
		expect(icon).toHaveClass("browser-panel__url-icon");
		expect(icon).not.toHaveClass("top-1/2");
		expect(icon).not.toHaveClass("-translate-y-1/2");
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

		rerender(<PersistentBrowserPanelView currentSession={session} visible={false} />);
		expect(postMock).toHaveBeenCalledTimes(1);

		await act(async () => {
			resolvePost({ data: {} });
		});
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(2));
		expect(postMock).toHaveBeenCalledTimes(2);
		expect((postMock.mock.calls[0][1].body as { message: string }).message).toContain("Make this button blue.");
		expect((postMock.mock.calls[1][1].body as { message: string }).message).toContain("Make this heading shorter.");

		rerender(<PersistentBrowserPanelView currentSession={session} visible />);
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
			<BrowserPanel
				active
				onTogglePopOut={() => undefined}
				poppedOut={false}
				session={{ ...session, status: "working" }}
			/>,
		);
		await act(async () => {
			resolvePost({ data: {} });
		});
		rerender(
			<BrowserPanel
				active
				onTogglePopOut={() => undefined}
				poppedOut={false}
				session={{ ...session, status: "idle" }}
			/>,
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
});
