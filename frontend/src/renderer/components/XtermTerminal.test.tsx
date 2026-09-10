import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { TerminalMux } from "../lib/terminal-mux";
import { markTerminalHandleFresh } from "../lib/fresh-terminal-handles";
import { useTerminalSession, type AttachableTerminal } from "../hooks/useTerminalSession";
import { useUiStore } from "../stores/ui-store";
import { safeTerminalFind } from "./TerminalSearch";
import { XtermTerminal } from "./XtermTerminal";

const state = vi.hoisted(() => ({
	fit: vi.fn(),
	linkHandler: null as null | ((event: MouseEvent, uri: string) => void),
	searchAddon: null as null | {
		clearDecorations: ReturnType<typeof vi.fn>;
		findNext: ReturnType<typeof vi.fn>;
		findPrevious: ReturnType<typeof vi.fn>;
		resultListeners: Set<(results: { resultCount: number; resultIndex: number }) => void>;
	},
	lastTerminal: null as null | {
		write(data: Uint8Array, done?: () => void): void;
		keyHandler?: (event: KeyboardEvent) => boolean;
		wheelHandler?: (event: WheelEvent) => boolean;
		selection: string;
		options: Record<string, unknown>;
		modes: { bracketedPasteMode: boolean; mouseTrackingMode: string };
		buffer: { active: { baseY: number; type: string; viewportY: number } };
		scrollLines: ReturnType<typeof vi.fn>;
		scrollToBottom: ReturnType<typeof vi.fn>;
		scrollToLine: ReturnType<typeof vi.fn>;
		refresh: ReturnType<typeof vi.fn>;
		clear: ReturnType<typeof vi.fn>;
		dispose: ReturnType<typeof vi.fn>;
		focus: ReturnType<typeof vi.fn>;
		selectAll: ReturnType<typeof vi.fn>;
		dataListeners: Set<(data: string) => void>;
		csiHandlers: Array<{
			callback: (params: (number | number[])[]) => boolean | Promise<boolean>;
			id: { final: string; intermediates?: string; prefix?: string };
		}>;
		keyListeners: Set<(event: { key: string }) => void>;
		selectionListeners: Set<() => void>;
		scrollListeners: Set<() => void>;
		_core: {
			element: { classList: { add: ReturnType<typeof vi.fn>; remove: ReturnType<typeof vi.fn> } };
			viewport: { scrollBarWidth: number };
			_selectionService: {
				enable: ReturnType<typeof vi.fn>;
				shouldForceSelection: (event: MouseEvent) => boolean;
			};
		};
	},
}));

vi.mock("@xterm/xterm", () => ({
	Terminal: class FakeTerminal {
		options: Record<string, unknown>;
		cols = 80;
		rows = 24;
		selection = "";
		keyHandler?: (event: KeyboardEvent) => boolean;
		wheelHandler?: (event: WheelEvent) => boolean;
		modes = { bracketedPasteMode: false, mouseTrackingMode: "vt200" };
		buffer = { active: { baseY: 0, type: "normal", viewportY: 0 } };
		scrollLines = vi.fn();
		scrollToBottom = vi.fn();
		scrollToLine = vi.fn();
		refresh = vi.fn();
		clear = vi.fn();
		focus = vi.fn();
		selectAll = vi.fn();
		dataListeners = new Set<(data: string) => void>();
		csiHandlers: Array<{
			callback: (params: (number | number[])[]) => boolean | Promise<boolean>;
			id: { final: string; intermediates?: string; prefix?: string };
		}> = [];
		parser = {
			registerCsiHandler: (
				id: { final: string; intermediates?: string; prefix?: string },
				callback: (params: (number | number[])[]) => boolean | Promise<boolean>,
			) => {
				const handler = { callback, id };
				this.csiHandlers.push(handler);
				return {
					dispose: () => {
						this.csiHandlers = this.csiHandlers.filter((candidate) => candidate !== handler);
					},
				};
			},
		};
		keyListeners = new Set<(event: { key: string }) => void>();
		selectionListeners = new Set<() => void>();
		scrollListeners = new Set<() => void>();
		writeBuffer = "";
		_core = {
			element: { classList: { add: vi.fn(), remove: vi.fn() } },
			viewport: { scrollBarWidth: 15 },
			_selectionService: {
				enable: vi.fn(),
				shouldForceSelection: () => false,
			},
		};

		constructor(options: Record<string, unknown>) {
			this.options = options;
			state.lastTerminal = this;
		}

		loadAddon() {}
		open(host: HTMLElement) {
			host.appendChild(document.createElement("textarea"));
		}
		write(data: Uint8Array, done?: () => void) {
			this.writeBuffer += new TextDecoder().decode(data);
			if (this.writeBuffer.includes("\x1b[6n")) {
				this.dataListeners.forEach((listener) => listener("\x1b[7;21R"));
				this.writeBuffer = "";
			}
			done?.();
		}
		writeln() {}
		dispose = vi.fn();
		onData(listener: (data: string) => void) {
			this.dataListeners.add(listener);
			return { dispose: () => this.dataListeners.delete(listener) };
		}
		onResize() {
			return { dispose: () => undefined };
		}
		onRender() {
			return { dispose: () => undefined };
		}
		onScroll(listener: () => void) {
			this.scrollListeners.add(listener);
			return { dispose: () => this.scrollListeners.delete(listener) };
		}
		onKey(listener: (event: { key: string }) => void) {
			this.keyListeners.add(listener);
			return { dispose: () => this.keyListeners.delete(listener) };
		}
		onSelectionChange(listener: () => void) {
			this.selectionListeners.add(listener);
			return { dispose: () => this.selectionListeners.delete(listener) };
		}
		hasSelection() {
			return this.selection.length > 0;
		}
		getSelection() {
			return this.selection;
		}
		attachCustomKeyEventHandler(listener: (event: KeyboardEvent) => boolean) {
			this.keyHandler = listener;
		}
		attachCustomWheelEventHandler(listener: (event: WheelEvent) => boolean) {
			this.wheelHandler = listener;
		}
		unicode = { activeVersion: "" };
	},
}));

vi.mock("@xterm/addon-fit", () => ({
	FitAddon: class FakeFitAddon {
		fit() {
			state.fit();
		}
	},
}));

vi.mock("@xterm/addon-search", () => ({
	SearchAddon: class FakeSearchAddon {
		clearDecorations = vi.fn();
		findNext = vi.fn(() => true);
		findPrevious = vi.fn(() => true);
		resultListeners = new Set<(results: { resultCount: number; resultIndex: number }) => void>();

		constructor() {
			state.searchAddon = this;
		}

		onDidChangeResults(listener: (results: { resultCount: number; resultIndex: number }) => void) {
			this.resultListeners.add(listener);
			return { dispose: () => this.resultListeners.delete(listener) };
		}
	},
}));

vi.mock("@xterm/addon-unicode11", () => ({
	Unicode11Addon: class FakeUnicode11Addon {},
}));

vi.mock("@xterm/addon-web-links", () => ({
	WebLinksAddon: class FakeWebLinksAddon {
		constructor(handler?: (event: MouseEvent, uri: string) => void) {
			state.linkHandler = handler ?? null;
		}
	},
}));

vi.mock("@xterm/addon-canvas", () => ({
	CanvasAddon: class FakeCanvasAddon {},
}));

vi.mock("@xterm/addon-webgl", () => ({
	WebglAddon: class FakeWebglAddon {
		onContextLoss() {}
		dispose() {}
	},
}));

function setNavigatorPlatform(platform: string) {
	Object.defineProperty(window.navigator, "platform", {
		configurable: true,
		value: platform,
	});
	Object.defineProperty(window.navigator, "userAgentData", {
		configurable: true,
		value: { platform },
	});
}

describe("XtermTerminal", () => {
	beforeEach(() => {
		state.fit.mockReset();
		state.lastTerminal = null;
		state.linkHandler = null;
		state.searchAddon = null;
		setNavigatorPlatform("Linux x86_64");
		window.ao!.clipboard.writeText = vi.fn().mockResolvedValue(undefined);
		window.ao!.clipboard.readText = vi.fn().mockResolvedValue("");
		window.ao!.terminal.setFocused = vi.fn();
		window.ao!.terminal.onFontSizeShortcut = () => () => undefined;
	});

	it("fits on each observed frame while an ancestor requests live terminal resizing", () => {
		const callbacks: ResizeObserverCallback[] = [];
		const originalResizeObserver = window.ResizeObserver;
		class CapturingResizeObserver implements ResizeObserver {
			constructor(callback: ResizeObserverCallback) {
				callbacks.push(callback);
			}
			disconnect() {}
			observe() {}
			unobserve() {}
		}
		Object.defineProperty(window, "ResizeObserver", {
			configurable: true,
			writable: true,
			value: CapturingResizeObserver,
		});
		try {
			render(
				<div data-terminal-live-resize="true">
					<XtermTerminal theme="dark" />
				</div>,
			);
			state.fit.mockClear();

			act(() => callbacks.at(-1)?.([], {} as ResizeObserver));

			expect(state.fit).toHaveBeenCalledTimes(1);
		} finally {
			Object.defineProperty(window, "ResizeObserver", {
				configurable: true,
				writable: true,
				value: originalResizeObserver,
			});
		}
	});

	it("finishes retained activation when xterm emits no render event", async () => {
		vi.useFakeTimers();
		vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) =>
			window.setTimeout(() => callback(performance.now()), 0),
		);
		vi.stubGlobal("cancelAnimationFrame", (id: number) => window.clearTimeout(id));
		try {
			let terminal: AttachableTerminal | undefined;
			render(<XtermTerminal theme="dark" onReady={(ready) => { terminal = ready; }} />);
			state.lastTerminal!.buffer.active.baseY = 1;
			const preparation = terminal!.prepareForActivation();
			await act(async () => {
				vi.advanceTimersByTime(250);
				vi.runAllTimers();
				await preparation;
			});
			expect(state.lastTerminal!.scrollToBottom).toHaveBeenCalled();
			expect(state.lastTerminal!.refresh).not.toHaveBeenCalled();
		} finally {
			vi.useRealTimers();
			vi.unstubAllGlobals();
		}
	});

	it("defers disposal behind xterm's pending viewport callback", () => {
		vi.useFakeTimers();
		try {
			const { unmount } = render(<XtermTerminal theme="dark" />);
			const terminal = state.lastTerminal!;

			unmount();
			expect(terminal.dispose).not.toHaveBeenCalled();

			act(() => vi.runOnlyPendingTimers());
			expect(terminal.dispose).toHaveBeenCalledOnce();
		} finally {
			vi.useRealTimers();
		}
	});

	it("preserves the agent TUI palette without contrast remapping", () => {
		render(<XtermTerminal theme="dark" />);

		expect(state.lastTerminal!.options.drawBoldTextInBrightColors).toBe(true);
		expect(state.lastTerminal!.options.minimumContrastRatio).toBe(1);
	});

	it("focuses the terminal when human input is requested", async () => {
		const { rerender } = render(<XtermTerminal theme="dark" />);

		rerender(<XtermTerminal focusRequested theme="dark" />);

		await waitFor(() => expect(state.lastTerminal!.focus).toHaveBeenCalled());
	});

	it("does not steal focus from an open dialog or another control", () => {
		const { rerender } = render(<XtermTerminal theme="dark" />);
		const dialog = document.createElement("div");
		dialog.setAttribute("role", "dialog");
		dialog.dataset.state = "open";
		const dialogInput = document.createElement("input");
		dialog.appendChild(dialogInput);
		document.body.appendChild(dialog);
		dialogInput.focus();
		state.lastTerminal!.focus.mockClear();

		rerender(<XtermTerminal focusRequested theme="dark" />);

		expect(state.lastTerminal!.focus).not.toHaveBeenCalled();

		dialog.remove();
		const control = document.createElement("button");
		document.body.appendChild(control);
		control.focus();
		rerender(<XtermTerminal theme="dark" />);
		state.lastTerminal!.focus.mockClear();
		rerender(<XtermTerminal focusRequested theme="dark" />);

		expect(state.lastTerminal!.focus).not.toHaveBeenCalled();
		control.remove();
	});

	it("allows the session navigation button to hand focus to the destination TUI", async () => {
		const { rerender } = render(<XtermTerminal theme="dark" />);
		const sessionButton = document.createElement("button");
		sessionButton.setAttribute("aria-current", "page");
		document.body.appendChild(sessionButton);
		sessionButton.focus();
		state.lastTerminal!.focus.mockClear();

		rerender(<XtermTerminal focusRequested theme="dark" />);

		await waitFor(() => expect(state.lastTerminal!.focus).toHaveBeenCalled());
		sessionButton.remove();
	});

	it("allows an in-pane terminal tab to hand focus to the destination TUI", async () => {
		const { rerender } = render(<XtermTerminal theme="dark" />);
		const topbar = document.createElement("div");
		topbar.dataset.testid = "session-workspace-topbar";
		const tab = document.createElement("button");
		tab.setAttribute("aria-current", "true");
		tab.setAttribute("role", "tab");
		topbar.appendChild(tab);
		document.body.appendChild(topbar);
		tab.focus();
		state.lastTerminal!.focus.mockClear();

		rerender(<XtermTerminal focusRequested theme="dark" />);

		await waitFor(() => expect(state.lastTerminal!.focus).toHaveBeenCalled());
		topbar.remove();
	});

	it("retries terminal focus after a closing dialog releases focus", async () => {
		const frames: FrameRequestCallback[] = [];
		const requestAnimationFrameSpy = vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
			frames.push(callback);
			return frames.length;
		});
		const { rerender } = render(<XtermTerminal theme="dark" />);
		frames.length = 0;
		const dialog = document.createElement("div");
		dialog.setAttribute("role", "dialog");
		dialog.dataset.state = "open";
		const dialogInput = document.createElement("input");
		dialog.appendChild(dialogInput);
		document.body.appendChild(dialog);
		dialogInput.focus();
		state.lastTerminal!.focus.mockClear();

		rerender(<XtermTerminal focusRequested theme="dark" />);
		dialogInput.blur();
		dialog.remove();
		expect(frames).toHaveLength(1);
		act(() => frames.shift()?.(performance.now()));

		expect(state.lastTerminal!.focus).toHaveBeenCalled();
		requestAnimationFrameSpy.mockRestore();
	});

	it("focuses a retained terminal when it becomes visible", async () => {
		const { rerender } = render(<XtermTerminal focusRequested isVisible={false} theme="dark" />);
		state.lastTerminal!.focus.mockClear();

		rerender(<XtermTerminal focusRequested isVisible theme="dark" />);

		await waitFor(() => expect(state.lastTerminal!.focus).toHaveBeenCalled());
	});

	it("updates the live terminal palette when the named color theme changes", () => {
		const style = document.createElement("style");
		style.textContent = `
			:root {
				--color-bg-terminal-opaque: #101317;
				--color-text-terminal: #d7d7d2;
				--color-working: #60a5fa;
			}
			:root[data-style-theme="github"] {
				--background: #0d1117;
				--foreground: #ccd3d8;
				--primary: #58a6ff;
			}
		`;
		document.head.appendChild(style);
		delete document.documentElement.dataset.styleTheme;
		useUiStore.setState({ themeStyle: "orchestrate" });

		try {
			render(<XtermTerminal theme="dark" />);
			expect(state.lastTerminal!.options.theme).toMatchObject({ background: "#101317" });

			act(() => useUiStore.getState().setThemeStyle("github"));

			expect(state.lastTerminal!.options.theme).toMatchObject({
				background: "#0d1117",
				cursor: "#58a6ff",
				foreground: "#ccd3d8",
			});
		} finally {
			style.remove();
			delete document.documentElement.dataset.styleTheme;
			act(() => useUiStore.setState({ themeStyle: "orchestrate" }));
		}
	});

	it("uses the terminal foreground for the light-mode block cursor", () => {
		const style = document.createElement("style");
		style.textContent = `
			:root {
				--color-bg-terminal-opaque: #f5f5f4;
				--color-text-terminal: #24292f;
				--color-working: #2563eb;
			}
		`;
		document.head.appendChild(style);
		delete document.documentElement.dataset.styleTheme;
		useUiStore.setState({ themeStyle: "orchestrate" });

		try {
			render(<XtermTerminal theme="light" />);
			expect(state.lastTerminal!.options.theme).toMatchObject({
				background: "#f5f5f4",
				foreground: "#24292f",
				cursor: "#24292f",
				cursorAccent: "#f5f5f4",
			});
		} finally {
			style.remove();
			delete document.documentElement.dataset.styleTheme;
			act(() => useUiStore.setState({ themeStyle: "orchestrate" }));
		}
	});

	it("does not reserve width for the hidden terminal scrollbar outside macOS", () => {
		const { container } = render(<XtermTerminal theme="dark" />);

		expect(state.lastTerminal!._core.viewport.scrollBarWidth).toBe(0);
		expect(container.querySelector(".terminal-scrollbar")).toBeNull();
	});

	it("reserves a slim draggable scrollbar gutter on macOS", () => {
		setNavigatorPlatform("MacIntel");
		const { container } = render(<XtermTerminal theme="dark" />);

		expect(state.lastTerminal!._core.viewport.scrollBarWidth).toBe(7);
		expect(container.querySelector(".terminal-xterm-host--mac")).not.toBeNull();
		expect(container.querySelector(".terminal-scrollbar")).not.toBeNull();
	});

	it("fades the macOS scrollbar after scrolling goes idle", () => {
		setNavigatorPlatform("MacIntel");
		const { container } = render(<XtermTerminal theme="dark" />);
		const scrollbar = container.querySelector<HTMLElement>(".terminal-scrollbar")!;
		scrollbar.dataset.scrollable = "true";
		vi.useFakeTimers();

		act(() => state.lastTerminal!.scrollListeners.forEach((listener) => listener()));
		expect(scrollbar.dataset.active).toBe("true");

		act(() => vi.advanceTimersByTime(699));
		expect(scrollbar.dataset.active).toBe("true");
		act(() => vi.advanceTimersByTime(1));
		expect(scrollbar.dataset.active).toBe("false");

		vi.useRealTimers();
	});

	it("copies selected terminal text on the terminal copy shortcut", () => {
		render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "copied selection";

		const event = {
			key: "c",
			metaKey: true,
			ctrlKey: false,
			shiftKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		const allowed = state.lastTerminal!.keyHandler!(event);

		expect(allowed).toBe(false);
		expect(event.preventDefault).toHaveBeenCalled();
		expect(window.ao!.clipboard.writeText).toHaveBeenCalledWith("copied selection");
	});

	it.each([
		["Command", "MacIntel", false, true],
		["Ctrl", "Linux x86_64", true, false],
	])("uses %s plus/minus for terminal font size on %s", (_label, platform, ctrlKey, metaKey) => {
		setNavigatorPlatform(platform);
		const onChangeFontSize = vi.fn();
		render(<XtermTerminal onChangeFontSize={onChangeFontSize} theme="dark" />);

		const increase = {
			altKey: false,
			code: "Equal",
			ctrlKey,
			key: "=",
			metaKey,
			preventDefault: vi.fn(),
			shiftKey: false,
			stopPropagation: vi.fn(),
			type: "keydown",
		} as unknown as KeyboardEvent;
		const decrease = {
			...increase,
			code: "Minus",
			key: "-",
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		const otherPlatformModifier = { ...increase, ctrlKey: !ctrlKey, metaKey: !metaKey };

		expect(state.lastTerminal!.keyHandler!(increase)).toBe(false);
		expect(state.lastTerminal!.keyHandler!(decrease)).toBe(false);
		expect(state.lastTerminal!.keyHandler!(otherPlatformModifier)).toBe(true);
		expect(onChangeFontSize).toHaveBeenNthCalledWith(1, 1);
		expect(onChangeFontSize).toHaveBeenNthCalledWith(2, -1);
		expect(increase.preventDefault).toHaveBeenCalledOnce();
		expect(decrease.preventDefault).toHaveBeenCalledOnce();
	});

	it("reports terminal focus and applies main-process font-size shortcuts only there", () => {
		let fontSizeShortcut: ((delta: -1 | 1) => void) | undefined;
		window.ao!.terminal.onFontSizeShortcut = (listener) => {
			fontSizeShortcut = listener;
			return () => undefined;
		};
		const onChangeFontSize = vi.fn();
		const { container } = render(<XtermTerminal onChangeFontSize={onChangeFontSize} theme="dark" />);
		const textarea = container.querySelector("textarea")!;

		textarea.focus();
		expect(window.ao!.terminal.setFocused).toHaveBeenLastCalledWith(true);
		fontSizeShortcut?.(1);
		expect(onChangeFontSize).toHaveBeenCalledWith(1);

		const outside = document.createElement("button");
		document.body.appendChild(outside);
		outside.focus();
		expect(window.ao!.terminal.setFocused).toHaveBeenLastCalledWith(false);
		fontSizeShortcut?.(-1);
		expect(onChangeFontSize).toHaveBeenCalledTimes(1);
		outside.remove();
	});

	it("handles native copy events from inside the terminal", () => {
		const { container } = render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "native copied selection";
		const setData = vi.fn();
		const event = new Event("copy", { bubbles: true, cancelable: true }) as ClipboardEvent;
		Object.defineProperty(event, "clipboardData", {
			value: { setData },
		});

		container.firstElementChild!.dispatchEvent(event);

		expect(event.defaultPrevented).toBe(true);
		expect(setData).toHaveBeenCalledWith("text/plain", "native copied selection");
		expect(window.ao!.clipboard.writeText).toHaveBeenCalledWith("native copied selection");
	});

	it("copies from the focused xterm textarea when the window receives the copy shortcut", () => {
		const { container } = render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "focused copied selection";
		container.querySelector("textarea")!.focus();

		const event = new KeyboardEvent("keydown", {
			bubbles: true,
			cancelable: true,
			key: "c",
			metaKey: true,
		});
		window.dispatchEvent(event);

		expect(event.defaultPrevented).toBe(true);
		expect(window.ao!.clipboard.writeText).toHaveBeenCalledWith("focused copied selection");
	});

	it("opens a themed context menu on right-click and disables Copy without a selection", async () => {
		const { container } = render(<XtermTerminal theme="dark" />);
		const host = container.firstElementChild!;

		expect(fireEvent.contextMenu(host, { clientX: 120, clientY: 88 })).toBe(false);

		expect(await screen.findByText("Paste")).toBeInTheDocument();
		expect(screen.getByText("Copy")).toHaveAttribute("data-disabled");
		const trigger = container.querySelector("button[aria-hidden='true']") as HTMLButtonElement;
		expect(trigger.style.left).toBe("120px");
		expect(trigger.style.top).toBe("88px");
	});

	it("opens terminal search from the context menu with readable light-theme query text", async () => {
		const { container } = render(<XtermTerminal theme="light" />);

		fireEvent.contextMenu(container.firstElementChild!);
		fireEvent.click(await screen.findByText("Search terminal"));

		const input = await screen.findByRole("searchbox", { name: "Search terminal" });
		expect(input).toHaveFocus();
		expect(input).toHaveClass("text-foreground");
		expect(input).not.toHaveClass("text-terminal");
	});

	it.each([
		["Command+F", "MacIntel", false, true],
		["Ctrl+F", "Linux x86_64", true, false],
	])("opens search with %s and consumes the terminal shortcut", async (_label, platform, ctrlKey, metaKey) => {
		setNavigatorPlatform(platform);
		render(<XtermTerminal theme="dark" />);
		const event = {
			altKey: false,
			ctrlKey,
			key: "f",
			metaKey,
			preventDefault: vi.fn(),
			shiftKey: false,
			stopPropagation: vi.fn(),
			type: "keydown",
		} as unknown as KeyboardEvent;

		let allowed = true;
		act(() => {
			allowed = state.lastTerminal!.keyHandler!(event);
		});

		expect(allowed).toBe(false);
		expect(event.preventDefault).toHaveBeenCalledOnce();
		expect(event.stopPropagation).toHaveBeenCalledOnce();
		expect(await screen.findByRole("searchbox", { name: "Search terminal" })).toHaveFocus();
	});

	it("searches incrementally, reports matches, and navigates in both directions", async () => {
		setNavigatorPlatform("MacIntel");
		render(<XtermTerminal theme="dark" />);
		const shortcut = {
			altKey: false,
			ctrlKey: false,
			key: "f",
			metaKey: true,
			preventDefault: vi.fn(),
			shiftKey: false,
			stopPropagation: vi.fn(),
			type: "keydown",
		} as unknown as KeyboardEvent;
		act(() => state.lastTerminal!.keyHandler!(shortcut));
		const input = await screen.findByRole("searchbox", { name: "Search terminal" });
		state.searchAddon!.findNext.mockClear();

		fireEvent.change(input, { target: { value: "needle" } });
		await waitFor(() =>
			expect(state.searchAddon!.findNext).toHaveBeenLastCalledWith(
				"needle",
				expect.objectContaining({ incremental: true, caseSensitive: false, regex: false }),
			),
		);

		fireEvent.click(screen.getByRole("button", { name: "Match case" }));
		fireEvent.click(screen.getByRole("button", { name: "Use regular expression" }));
		await waitFor(() =>
			expect(state.searchAddon!.findNext).toHaveBeenLastCalledWith(
				"needle",
				expect.objectContaining({ incremental: true, caseSensitive: true, regex: true }),
			),
		);

		act(() =>
			state.searchAddon!.resultListeners.forEach((listener) => listener({ resultCount: 3, resultIndex: 1 })),
		);
		expect(screen.getByText("2/3")).toHaveAccessibleName("Match 2 of 3");

		state.searchAddon!.findNext.mockClear();
		fireEvent.keyDown(input, { key: "Enter" });
		expect(state.searchAddon!.findNext).toHaveBeenCalledWith(
			"needle",
			expect.objectContaining({ incremental: false, caseSensitive: true, regex: true }),
		);
		state.lastTerminal!.selection = "needle";
		state.lastTerminal!.selectionListeners.forEach((listener) => listener());
		await new Promise((resolve) => window.setTimeout(resolve, 0));
		expect(window.ao!.clipboard.writeText).not.toHaveBeenCalled();
		expect(screen.queryByRole("status")).not.toBeInTheDocument();

		fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
		expect(state.searchAddon!.findPrevious).toHaveBeenCalledWith(
			"needle",
			expect.objectContaining({ incremental: false, caseSensitive: true, regex: true }),
		);

		state.searchAddon!.findNext.mockClear();
		fireEvent.keyDown(input, { key: "g", metaKey: true });
		expect(state.searchAddon!.findNext).toHaveBeenCalledWith("needle", expect.anything());
		state.searchAddon!.findPrevious.mockClear();
		fireEvent.keyDown(input, { key: "g", metaKey: true, shiftKey: true });
		expect(state.searchAddon!.findPrevious).toHaveBeenCalledWith("needle", expect.anything());
	});

	it("marks an invalid regular expression without calling xterm search", async () => {
		setNavigatorPlatform("MacIntel");
		render(<XtermTerminal theme="dark" />);
		act(() =>
			state.lastTerminal!.keyHandler!({
				altKey: false,
				ctrlKey: false,
				key: "f",
				metaKey: true,
				preventDefault: vi.fn(),
				shiftKey: false,
				stopPropagation: vi.fn(),
				type: "keydown",
			} as unknown as KeyboardEvent),
		);
		const input = await screen.findByRole("searchbox", { name: "Search terminal" });
		fireEvent.click(screen.getByRole("button", { name: "Use regular expression" }));
		state.searchAddon!.findNext.mockClear();

		fireEvent.change(input, { target: { value: "[" } });

		expect(input).toHaveAttribute("aria-invalid", "true");
		expect(screen.getByText("—")).toHaveAccessibleName("Invalid regular expression");
		expect(state.searchAddon!.findNext).not.toHaveBeenCalledWith("[", expect.anything());
	});

	it("clears search highlights and returns focus to xterm on Escape", async () => {
		setNavigatorPlatform("MacIntel");
		render(<XtermTerminal theme="dark" />);
		act(() =>
			state.lastTerminal!.keyHandler!({
				altKey: false,
				ctrlKey: false,
				key: "f",
				metaKey: true,
				preventDefault: vi.fn(),
				shiftKey: false,
				stopPropagation: vi.fn(),
				type: "keydown",
			} as unknown as KeyboardEvent),
		);
		const input = await screen.findByRole("searchbox", { name: "Search terminal" });

		fireEvent.keyDown(input, { key: "Escape" });

		expect(screen.queryByRole("searchbox", { name: "Search terminal" })).not.toBeInTheDocument();
		expect(state.searchAddon!.clearDecorations).toHaveBeenCalled();
		await waitFor(() => expect(state.lastTerminal!.focus).toHaveBeenCalled());
	});

	it("contains xterm's known decoration failure without hiding other search failures", () => {
		expect(
			safeTerminalFind(() => {
				throw new Error("This API only accepts positive integers");
			}, "needle"),
		).toBe(false);
		expect(() =>
			safeTerminalFind(() => {
				throw new Error("unexpected search failure");
			}, "needle"),
		).toThrow("unexpected search failure");
	});

	it("toggles terminal fullscreen from the right-click menu", async () => {
		const onToggleFullscreen = vi.fn();
		const { container, rerender } = render(
			<div className="terminal-pane-frame">
				<XtermTerminal isFullscreen={false} onToggleFullscreen={onToggleFullscreen} theme="dark" />
			</div>,
		);

		fireEvent.contextMenu(container.querySelector(".terminal-pane-frame")!.firstElementChild!);
		fireEvent.click(await screen.findByText("Fullscreen terminal"));
		expect(onToggleFullscreen).toHaveBeenCalledOnce();

		const pane = container.querySelector(".terminal-pane-frame") as HTMLElement;
		Object.defineProperty(document, "fullscreenElement", { configurable: true, value: pane });
		rerender(
			<div className="terminal-pane-frame">
				<XtermTerminal isFullscreen onToggleFullscreen={onToggleFullscreen} theme="dark" />
			</div>,
		);
		fireEvent.contextMenu(pane.firstElementChild!);
		const exitFullscreenItem = await screen.findByText("Exit fullscreen");
		expect(pane.contains(exitFullscreenItem.closest<HTMLElement>("[role='menu']"))).toBe(true);
		Object.defineProperty(document, "fullscreenElement", { configurable: true, value: null });
	});


	it.each([true, false])("preserves newer focus after deferred fullscreen completion when visible=%s", async (isVisible) => {
		let complete!: () => void;
		const onToggleFullscreen = vi.fn(() => new Promise<void>((resolve) => { complete = resolve; }));
		const frames: FrameRequestCallback[] = [];
		const frameSpy = vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
			frames.push(callback);
			return frames.length;
		});
		const { container, rerender } = render(<XtermTerminal onToggleFullscreen={onToggleFullscreen} theme="dark" />);
		const control = document.createElement("button");
		document.body.appendChild(control);
		try {
			fireEvent.contextMenu(container.firstElementChild!);
			fireEvent.click(await screen.findByText("Fullscreen terminal"));
			rerender(<XtermTerminal isVisible={isVisible} onToggleFullscreen={onToggleFullscreen} theme="dark" />);
			control.focus();
			state.lastTerminal!.focus.mockClear();
			frames.length = 0;
			await act(async () => complete());
			expect(control).toHaveFocus();
			act(() => { while (frames.length) frames.shift()!(performance.now()); });
			expect(control).toHaveFocus();
			expect(state.lastTerminal!.focus).not.toHaveBeenCalled();
		} finally {
			control.remove();
			frameSpy.mockRestore();
		}
	});
	it("returns focus to xterm after entering and exiting fullscreen from the menu", async () => {
		const onToggleFullscreen = vi.fn(async () => undefined);
		const frames: FrameRequestCallback[] = [];
		const requestAnimationFrameSpy = vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
			frames.push(callback);
			return frames.length;
		});
		const { container, rerender } = render(
			<div className="terminal-pane-frame">
				<XtermTerminal onToggleFullscreen={onToggleFullscreen} theme="dark" />
			</div>,
		);
		const trigger = document.createElement("button");
		document.body.appendChild(trigger);
		state.lastTerminal!.focus.mockClear();
		frames.length = 0;

		try {
			fireEvent.contextMenu(container.querySelector(".terminal-pane-frame")!.firstElementChild!);
			fireEvent.click(await screen.findByText("Fullscreen terminal"));
			await act(async () => undefined);

			expect(onToggleFullscreen).toHaveBeenCalledOnce();
			expect(frames).toHaveLength(1);
			act(() => frames.shift()?.(performance.now()));
			expect(frames).toHaveLength(1);
			act(() => frames.shift()?.(performance.now()));
			expect(state.lastTerminal!.focus).toHaveBeenCalledOnce();

			const pane = container.querySelector(".terminal-pane-frame")!;
			Object.defineProperty(document, "fullscreenElement", { configurable: true, value: pane });
			rerender(
				<div className="terminal-pane-frame">
					<XtermTerminal isFullscreen onToggleFullscreen={onToggleFullscreen} theme="dark" />
				</div>,
			);
			frames.length = 0;
			state.lastTerminal!.focus.mockClear();

			fireEvent.contextMenu(pane.querySelector("div")!);
			fireEvent.click(await screen.findByText("Exit fullscreen"));
			await act(async () => undefined);
			expect(onToggleFullscreen).toHaveBeenCalledTimes(2);
			expect(frames).toHaveLength(1);
			act(() => frames.shift()?.(performance.now()));
			act(() => frames.shift()?.(performance.now()));
			expect(state.lastTerminal!.focus).toHaveBeenCalledOnce();
		} finally {
			Object.defineProperty(document, "fullscreenElement", { configurable: true, value: null });
			requestAnimationFrameSpy.mockRestore();
			trigger.remove();
		}
	});

	it("runs context menu copy and select all against the xterm instance", async () => {
		const { container } = render(<XtermTerminal theme="dark" />);
		const host = container.firstElementChild!;
		state.lastTerminal!.selection = "menu copy";

		fireEvent.contextMenu(host);
		fireEvent.click(await screen.findByText("Copy"));
		await waitFor(() => expect(window.ao!.clipboard.writeText).toHaveBeenCalledWith("menu copy"));
		expect(state.lastTerminal!.focus).toHaveBeenCalled();

		fireEvent.contextMenu(host);
		fireEvent.click(await screen.findByText("Select All"));
		expect(state.lastTerminal!.selectAll).toHaveBeenCalled();
	});

	it("pastes from the context menu through the terminal paste path", async () => {
		const onInput = vi.fn();
		window.ao!.clipboard.readText = vi.fn().mockResolvedValue("menu\npaste");
		const { container } = render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		fireEvent.contextMenu(container.firstElementChild!);
		fireEvent.click(await screen.findByText("Paste"));

		await waitFor(() => expect(onInput).toHaveBeenCalledWith("menu\rpaste", "paste"));
		expect(window.ao!.clipboard.readText).toHaveBeenCalledTimes(1);
	});

	it("honors bracketed paste mode from the context menu", async () => {
		const onInput = vi.fn();
		window.ao!.clipboard.readText = vi.fn().mockResolvedValue("bracketed\npaste");
		const { container } = render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);
		state.lastTerminal!.modes.bracketedPasteMode = true;

		fireEvent.contextMenu(container.firstElementChild!);
		fireEvent.click(await screen.findByText("Paste"));

		await waitFor(() => expect(onInput).toHaveBeenCalledWith("\x1b[200~bracketed\rpaste\x1b[201~", "paste"));
	});

	it("does not copy terminal selections without an explicit copy action", async () => {
		render(<XtermTerminal theme="dark" />);

		state.lastTerminal!.selection = "selected text";
		state.lastTerminal!.selectionListeners.forEach((listener) => listener());
		await new Promise((resolve) => window.setTimeout(resolve, 0));

		expect(window.ao!.clipboard.writeText).not.toHaveBeenCalled();
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	it("shows a copied toast after an explicit copy", async () => {
		render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "toast selection";
		state.lastTerminal!.keyHandler!({
			key: "c",
			metaKey: true,
			ctrlKey: false,
			shiftKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent);

		expect(await screen.findByRole("status")).toHaveTextContent("Copied to clipboard");
	});

	it("does not show a copied toast when clipboard write fails", async () => {
		window.ao!.clipboard.writeText = vi.fn().mockRejectedValue(new Error("clipboard failed"));
		render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "failed selection";
		state.lastTerminal!.keyHandler!({
			key: "c",
			metaKey: true,
			ctrlKey: false,
			shiftKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent);
		await Promise.resolve();

		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	it("hides the copied toast after a short delay", async () => {
		vi.useFakeTimers();
		try {
			render(<XtermTerminal theme="dark" />);
			state.lastTerminal!.selection = "timed selection";
			await act(async () => {
				state.lastTerminal!.keyHandler!({
					key: "c",
					metaKey: true,
					ctrlKey: false,
					shiftKey: false,
					preventDefault: vi.fn(),
					stopPropagation: vi.fn(),
				} as unknown as KeyboardEvent);
				await Promise.resolve();
			});

			expect(screen.getByRole("status")).toHaveTextContent("Copied to clipboard");

			await act(async () => {
				await vi.advanceTimersByTimeAsync(1600);
			});
			expect(screen.queryByRole("status")).not.toBeInTheDocument();
		} finally {
			vi.useRealTimers();
		}
	});

	it("leaves plain Ctrl+C as terminal input on non-Windows even when text is selected", () => {
		render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "selected text";

		const event = {
			key: "c",
			metaKey: false,
			ctrlKey: true,
			shiftKey: false,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		const allowed = state.lastTerminal!.keyHandler!(event);

		expect(allowed).toBe(true);
		expect(event.preventDefault).not.toHaveBeenCalled();
		expect(event.stopPropagation).not.toHaveBeenCalled();
		expect(window.ao!.clipboard.writeText).not.toHaveBeenCalled();
	});

	it("copies selected text with plain Ctrl+C on Windows", () => {
		setNavigatorPlatform("Win32");
		render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "windows copy";

		const event = {
			key: "c",
			metaKey: false,
			ctrlKey: true,
			shiftKey: false,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		const allowed = state.lastTerminal!.keyHandler!(event);

		expect(allowed).toBe(false);
		expect(event.preventDefault).toHaveBeenCalled();
		expect(event.stopPropagation).toHaveBeenCalled();
		expect(window.ao!.clipboard.writeText).toHaveBeenCalledWith("windows copy");
	});

	it("leaves plain Ctrl+C as terminal input on Windows when nothing is selected", () => {
		setNavigatorPlatform("Win32");
		render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "";

		const event = {
			key: "c",
			metaKey: false,
			ctrlKey: true,
			shiftKey: false,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		const allowed = state.lastTerminal!.keyHandler!(event);

		expect(allowed).toBe(true);
		expect(event.preventDefault).not.toHaveBeenCalled();
		expect(event.stopPropagation).not.toHaveBeenCalled();
		expect(window.ao!.clipboard.writeText).not.toHaveBeenCalled();
	});

	it.each(["Linux x86_64", "Win32"])(
		"pastes once from the Electron clipboard on Ctrl+Shift+V for %s",
		async (platform) => {
			setNavigatorPlatform(platform);
			const onInput = vi.fn();
			window.ao!.clipboard.readText = vi.fn().mockResolvedValue("hello\nworld");
			const { container } = render(
				<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />,
			);

			const event = {
				key: "v",
				metaKey: false,
				ctrlKey: true,
				shiftKey: true,
				altKey: false,
				preventDefault: vi.fn(),
				stopPropagation: vi.fn(),
			} as unknown as KeyboardEvent;
			const allowed = state.lastTerminal!.keyHandler!(event);
			const pasteEvent = new Event("paste", { bubbles: true, cancelable: true }) as ClipboardEvent;
			Object.defineProperty(pasteEvent, "clipboardData", {
				value: { getData: vi.fn().mockReturnValue("native paste") },
			});
			container.firstElementChild!.dispatchEvent(pasteEvent);
			await Promise.resolve();

			expect(allowed).toBe(false);
			expect(event.preventDefault).toHaveBeenCalled();
			expect(event.stopPropagation).toHaveBeenCalled();
			expect(window.ao!.clipboard.readText).toHaveBeenCalledTimes(1);
			expect(pasteEvent.defaultPrevented).toBe(true);
			expect(onInput).toHaveBeenCalledTimes(1);
			expect(onInput).toHaveBeenCalledWith("hello\rworld", "paste");
		},
	);

	it("supports plain Ctrl+V paste on Windows", async () => {
		setNavigatorPlatform("Win32");
		const onInput = vi.fn();
		window.ao!.clipboard.readText = vi.fn().mockResolvedValue("windows paste");
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		const event = {
			key: "v",
			metaKey: false,
			ctrlKey: true,
			shiftKey: false,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		const allowed = state.lastTerminal!.keyHandler!(event);
		await Promise.resolve();

		expect(allowed).toBe(false);
		expect(event.preventDefault).toHaveBeenCalled();
		expect(event.stopPropagation).toHaveBeenCalled();
		expect(window.ao!.clipboard.readText).toHaveBeenCalled();
		expect(onInput).toHaveBeenCalledWith("windows paste", "paste");
	});

	it("suppresses a queued native paste event after a handled paste shortcut", async () => {
		const onInput = vi.fn();
		window.ao!.clipboard.readText = vi.fn().mockResolvedValue("shortcut paste");
		const { container } = render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		const event = {
			key: "v",
			metaKey: false,
			ctrlKey: true,
			shiftKey: true,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		expect(state.lastTerminal!.keyHandler!(event)).toBe(false);
		await new Promise((resolve) => window.setTimeout(resolve, 0));

		const pasteEvent = new Event("paste", { bubbles: true, cancelable: true }) as ClipboardEvent;
		Object.defineProperty(pasteEvent, "clipboardData", {
			value: { getData: vi.fn().mockReturnValue("native paste") },
		});
		container.firstElementChild!.dispatchEvent(pasteEvent);
		await Promise.resolve();

		expect(pasteEvent.defaultPrevented).toBe(true);
		expect(onInput).toHaveBeenCalledTimes(1);
		expect(onInput).toHaveBeenCalledWith("shortcut paste", "paste");
	});

	it("supports classic Windows terminal copy and paste shortcuts", async () => {
		const onInput = vi.fn();
		window.ao!.clipboard.readText = vi.fn().mockResolvedValue("insert paste");
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);
		state.lastTerminal!.selection = "insert copy";

		const copyEvent = {
			key: "Insert",
			metaKey: false,
			ctrlKey: true,
			shiftKey: false,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		expect(state.lastTerminal!.keyHandler!(copyEvent)).toBe(false);
		expect(window.ao!.clipboard.writeText).toHaveBeenCalledWith("insert copy");

		const pasteEvent = {
			key: "Insert",
			metaKey: false,
			ctrlKey: false,
			shiftKey: true,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		expect(state.lastTerminal!.keyHandler!(pasteEvent)).toBe(false);
		await Promise.resolve();

		expect(window.ao!.clipboard.readText).toHaveBeenCalled();
		expect(onInput).toHaveBeenCalledWith("insert paste", "paste");
	});

	it.each([
		["Option/Alt+Left", { key: "ArrowLeft", altKey: true }, "\x1bb"],
		["Option/Alt+Right", { key: "ArrowRight", altKey: true }, "\x1bf"],
		["Option/Alt+Backspace", { key: "Backspace", altKey: true }, "\x1b\x7f"],
		["Option/Alt+Delete", { key: "Delete", altKey: true }, "\x1bd"],
		["Ctrl+Left", { key: "ArrowLeft", ctrlKey: true }, "\x1b[1;5D"],
		["Ctrl+Right", { key: "ArrowRight", ctrlKey: true }, "\x1b[1;5C"],
		["Ctrl+Backspace", { key: "Backspace", ctrlKey: true }, "\x1b\x7f"],
		["Ctrl+Delete", { key: "Delete", ctrlKey: true }, "\x1bd"],
	])("normalizes %s into terminal input", (_name, init, expected) => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		const event = {
			metaKey: false,
			ctrlKey: false,
			shiftKey: false,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
			...init,
		} as unknown as KeyboardEvent;
		const allowed = state.lastTerminal!.keyHandler!(event);

		expect(allowed).toBe(false);
		expect(event.preventDefault).toHaveBeenCalled();
		expect(event.stopPropagation).toHaveBeenCalled();
		expect(onInput).toHaveBeenCalledWith(expected, "shortcut");
	});

	it("does not re-fire a shortcut on the keyup that follows its keydown", () => {
		// xterm.js invokes attachCustomKeyEventHandler on keydown, keyup, AND
		// keypress for the same physical key press. Without gating on event.type,
		// releasing Ctrl+Backspace would emit the escape sequence a second time.
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		const keyDown = {
			type: "keydown",
			key: "Backspace",
			ctrlKey: true,
			metaKey: false,
			shiftKey: false,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		expect(state.lastTerminal!.keyHandler!(keyDown)).toBe(false);
		expect(onInput).toHaveBeenCalledTimes(1);

		const keyUp = { ...keyDown, type: "keyup" } as unknown as KeyboardEvent;
		expect(state.lastTerminal!.keyHandler!(keyUp)).toBe(true);
		expect(onInput).toHaveBeenCalledTimes(1);
	});

	it("does not re-paste on the keyup that follows a Cmd+V keydown", async () => {
		window.ao!.clipboard.readText = vi.fn().mockResolvedValue("pasted once");
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		const keyDown = {
			type: "keydown",
			key: "v",
			ctrlKey: false,
			metaKey: true,
			shiftKey: false,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		expect(state.lastTerminal!.keyHandler!(keyDown)).toBe(false);
		await Promise.resolve();

		const keyUp = { ...keyDown, type: "keyup" } as unknown as KeyboardEvent;
		expect(state.lastTerminal!.keyHandler!(keyUp)).toBe(true);
		await Promise.resolve();

		expect(window.ao!.clipboard.readText).toHaveBeenCalledTimes(1);
		expect(onInput).toHaveBeenCalledTimes(1);
	});

	it("sends the meta-return escape sequence for Shift+Enter and consumes the event", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		const event = {
			type: "keydown",
			key: "Enter",
			metaKey: false,
			ctrlKey: false,
			shiftKey: true,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		const allowed = state.lastTerminal!.keyHandler!(event);

		expect(allowed).toBe(false);
		expect(event.preventDefault).toHaveBeenCalled();
		expect(event.stopPropagation).toHaveBeenCalled();
		expect(onInput).toHaveBeenCalledTimes(1);
		expect(onInput).toHaveBeenCalledWith("\x1b\r", "keyboard");
	});

	it("does not re-send the meta-return sequence on the keyup that follows Shift+Enter", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		const keyDown = {
			type: "keydown",
			key: "Enter",
			metaKey: false,
			ctrlKey: false,
			shiftKey: true,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		expect(state.lastTerminal!.keyHandler!(keyDown)).toBe(false);
		expect(onInput).toHaveBeenCalledTimes(1);

		const keyUp = { ...keyDown, type: "keyup" } as unknown as KeyboardEvent;
		expect(state.lastTerminal!.keyHandler!(keyUp)).toBe(true);
		expect(onInput).toHaveBeenCalledTimes(1);
	});

	it("leaves plain Enter as normal terminal input rather than intercepting it", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		const event = {
			type: "keydown",
			key: "Enter",
			metaKey: false,
			ctrlKey: false,
			shiftKey: false,
			altKey: false,
			preventDefault: vi.fn(),
			stopPropagation: vi.fn(),
		} as unknown as KeyboardEvent;
		const allowed = state.lastTerminal!.keyHandler!(event);

		expect(allowed).toBe(true);
		expect(event.preventDefault).not.toHaveBeenCalled();
		expect(event.stopPropagation).not.toHaveBeenCalled();
		expect(onInput).not.toHaveBeenCalled();
	});

	it("forwards keyboard input from explicit key events", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		state.lastTerminal!.keyListeners.forEach((listener) => listener({ key: "a" }));

		expect(onInput).toHaveBeenCalledWith("a", "keyboard");
	});

	it("does not forward raw xterm data/control bytes as user input", () => {
		const onInput = vi.fn();
		render(
			<XtermTerminal
				theme="dark"
				supportsCursorColorScheme
				onReady={(terminal) => terminal.onUserInput(onInput)}
			/>,
		);

		// One onData hook exists for OSC 10/11/12 color replies (Cursor theme probes).
		expect(state.lastTerminal!.dataListeners.size).toBe(1);
		state.lastTerminal!.dataListeners.forEach((listener) => listener("\x1b[A"));
		expect(onInput).not.toHaveBeenCalled();

		state.lastTerminal!.dataListeners.forEach((listener) =>
			listener("\x1b]11;rgb:f5f5/f5f5/f4f4\x07"),
		);
		expect(onInput).toHaveBeenCalledWith("\x1b]11;rgb:f5f5/f5f5/f4f4\x07", "protocol");
	});

	it("forwards validated OSC replies for generic terminals without forwarding raw control bytes", () => {
		const onInput = vi.fn();
		render(
			<XtermTerminal
				theme="dark"
				paneScrollsByKeyboard
				onReady={(terminal) => terminal.onUserInput(onInput)}
			/>,
		);

		state.lastTerminal!.dataListeners.forEach((listener) => listener("\x1b[?997;1n"));
		expect(onInput).not.toHaveBeenCalled();
		state.lastTerminal!.dataListeners.forEach((listener) =>
			listener("\x1b]4;196;rgb:ffff/0000/8000\x07"),
		);
		expect(onInput).toHaveBeenCalledWith("\x1b]4;196;rgb:ffff/0000/8000\x07", "protocol");
	});

	it("forwards the cursor-position reply generated for a PTY request", () => {
		const onInput = vi.fn();
		let terminal: AttachableTerminal | undefined;
		render(<XtermTerminal theme="dark" onReady={(ready) => {
			terminal = ready;
			return ready.onUserInput(onInput);
		}} />);

		terminal!.write(new TextEncoder().encode("prompt\x1b"));
		terminal!.write(new TextEncoder().encode("[6n"));

		expect(onInput).toHaveBeenCalledWith("\x1b[7;21R", "protocol");
	});

	it("keeps pending cursor credits when more output is queued before parsing", () => {
		const onInput = vi.fn();
		let terminal: AttachableTerminal | undefined;
		render(<XtermTerminal theme="dark" onReady={(ready) => {
			terminal = ready;
			return ready.onUserInput(onInput);
		}} />);
		vi.spyOn(state.lastTerminal!, "write").mockImplementation(() => {});
		terminal!.write(new TextEncoder().encode("\x1b[6n"));
		terminal!.write(new TextEncoder().encode("tail"));
		state.lastTerminal!.dataListeners.forEach((listener) => listener("\x1b[7;21R"));
		expect(onInput).toHaveBeenCalledWith("\x1b[7;21R", "protocol");
	});

	it("answers Cursor color-scheme output queries", () => {
		const onInput = vi.fn();
		let terminal: AttachableTerminal | undefined;
		render(<XtermTerminal theme="light" supportsCursorColorScheme onReady={(ready) => {
			terminal = ready;
			return ready.onUserInput(onInput);
		}} />);
		onInput.mockClear();
		terminal!.write(new TextEncoder().encode("\x1b[?2031h"));
		expect(onInput).toHaveBeenCalledWith(expect.stringContaining("\x1b[?997;2n"), "protocol");
	});

	it("returns a CPR to a fresh shell PTY through coverInitialReplay", async () => {
		markTerminalHandleFresh("fresh-login");
		let output: (data: Uint8Array) => void = () => {};
		let opened: () => void = () => {};
		const sendInput = vi.fn();
		const mux: TerminalMux = {
			open: vi.fn(), close: vi.fn(), resize: vi.fn(), dispose: vi.fn(), sendInput,
			onData: (_id, listener) => { output = listener; return () => {}; },
			onOpened: (_id, listener) => { opened = listener; return () => {}; },
			onExit: () => () => {}, onError: () => () => {}, onConnectionChange: () => () => {},
		};
		function Shell() {
			const session = useTerminalSession(undefined, { daemonReady: true, coverInitialReplay: true, shellTerminalHandleId: "fresh-login", createMux: () => mux });
			return <XtermTerminal theme="dark" onReady={session.attach} />;
		}
		render(<QueryClientProvider client={new QueryClient()}><Shell /></QueryClientProvider>);
		act(() => {
			opened();
			output(new TextEncoder().encode("prompt\x1b"));
			output(new TextEncoder().encode("[6n"));
		});
		await waitFor(() => expect(sendInput).toHaveBeenCalledWith("fresh-login", "\x1b[7;21R"));
	});

	it("stops treating covered output as live after a fresh handle reconnects", async () => {
		markTerminalHandleFresh("fresh-then-reconnected");
		const outputs: Array<(data: Uint8Array) => void> = [];
		const opened: Array<() => void> = [];
		const connectionChanges: Array<(state: "open" | "closed") => void> = [];
		const sendInputs = [vi.fn(), vi.fn()];
		const createMux = vi.fn((): TerminalMux => {
			const index = outputs.length;
			return {
				open: vi.fn(), close: vi.fn(), resize: vi.fn(), dispose: vi.fn(), sendInput: sendInputs[index],
				onData: (_id, listener) => { outputs[index] = listener; return () => {}; },
				onOpened: (_id, listener) => { opened[index] = listener; return () => {}; },
				onExit: () => () => {}, onError: () => () => {},
				onConnectionChange: (listener) => { connectionChanges[index] = listener; return () => {}; },
			};
		});
		function Shell() {
			const session = useTerminalSession(undefined, { daemonReady: true, coverInitialReplay: true, shellTerminalHandleId: "fresh-then-reconnected", createMux });
			return <XtermTerminal theme="dark" onReady={session.attach} />;
		}
		render(<QueryClientProvider client={new QueryClient()}><Shell /></QueryClientProvider>);
		act(() => {
			opened[0]();
			connectionChanges[0]("closed");
		});
		await waitFor(() => expect(createMux).toHaveBeenCalledTimes(2), { timeout: 2_000 });
		const xtermWrite = vi.spyOn(state.lastTerminal!, "write");
		act(() => {
			opened[1]();
			outputs[1](new TextEncoder().encode("history\x1b[6n"));
		});
		await waitFor(() => expect(xtermWrite).toHaveBeenCalled());
		expect(sendInputs[1]).not.toHaveBeenCalled();
	});

	it.each(["remount", "existing"])("discards historical CPRs on %s pane mount", async (scenario) => {
		const outputs: Array<(data: Uint8Array) => void> = [];
		const opened: Array<() => void> = [];
		const sendInputs = [vi.fn(), vi.fn()];
		const createMux = vi.fn((): TerminalMux => {
			const index = outputs.length;
			return {
				open: vi.fn(), close: vi.fn(), resize: vi.fn(), dispose: vi.fn(), sendInput: sendInputs[index],
				onData: (_id, listener) => { outputs[index] = listener; return () => {}; },
				onOpened: (_id, listener) => { opened[index] = listener; return () => {}; },
				onExit: () => () => {}, onError: () => () => {},
				onConnectionChange: () => () => {},
			};
		});
		function Shell() {
			const session = useTerminalSession(undefined, { daemonReady: true, coverInitialReplay: true, shellTerminalHandleId: "mounted-existing", createMux });
			return <XtermTerminal theme="dark" onReady={session.attach} />;
		}
		if (scenario === "remount") {
			markTerminalHandleFresh("mounted-existing");
			const first = render(<QueryClientProvider client={new QueryClient()}><Shell /></QueryClientProvider>);
			act(() => opened[0]());
			first.unmount();
		}
		render(<QueryClientProvider client={new QueryClient()}><Shell /></QueryClientProvider>);
		const index = outputs.length - 1;
		const xtermWrite = vi.spyOn(state.lastTerminal!, "write");
		act(() => {
			opened[index]();
			outputs[index](new TextEncoder().encode("history\x1b[6n"));
		});
		await waitFor(() => expect(xtermWrite).toHaveBeenCalled());
		expect(sendInputs[index]).not.toHaveBeenCalled();
	});

	it("does not forward an unsolicited cursor-position-shaped input", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		state.lastTerminal!.dataListeners.forEach((listener) => listener("\x1b[7;21R"));

		expect(onInput).not.toHaveBeenCalled();
	});

	it("updates protocol handling when a retained terminal becomes a Cursor terminal", () => {
		const onInput = vi.fn();
		const { rerender } = render(
			<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		onInput.mockClear();
		rerender(
			<XtermTerminal
				theme="dark"
				supportsCursorColorScheme
				onReady={(terminal) => terminal.onUserInput(onInput)}
			/>,
		);
		onInput.mockClear();
		state.lastTerminal!.dataListeners.forEach((listener) =>
			listener("\x1b]11;rgb:f5f5/f5f5/f4f4\x07"),
		);

		expect(onInput).toHaveBeenCalledWith("\x1b]11;rgb:f5f5/f5f5/f4f4\x07", "protocol");
	});

	it("reports live light and dark changes to mode 2031 subscribers", () => {
		const onInput = vi.fn();
		const view = render(
			<XtermTerminal theme="light" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		const setMode = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.final === "h",
		);
		const resetMode = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.final === "l",
		);

		expect(setMode?.callback([2031])).toBe(true);
		view.rerender(
			<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		expect(onInput).toHaveBeenLastCalledWith("\x1b[?997;1n", "protocol");

		view.rerender(
			<XtermTerminal theme="light" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		expect(onInput).toHaveBeenLastCalledWith("\x1b[?997;2n", "protocol");

		expect(resetMode?.callback([2031])).toBe(true);
		onInput.mockClear();
		view.rerender(
			<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		expect(onInput).not.toHaveBeenCalled();
	});

	it("reports named palette changes to mode 2031 subscribers", () => {
		const onInput = vi.fn();
		useUiStore.setState({ themeStyle: "orchestrate" });

		try {
			render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);
			const setMode = state.lastTerminal!.csiHandlers.find(
				({ id }) => id.prefix === "?" && id.final === "h",
			);

			expect(setMode?.callback([2031])).toBe(true);
			onInput.mockClear();
			act(() => useUiStore.getState().setThemeStyle("github"));

			expect(onInput).toHaveBeenLastCalledWith("\x1b[?997;1n", "protocol");
		} finally {
			act(() => useUiStore.setState({ themeStyle: "orchestrate" }));
		}
	});

	it("leaves unrelated private modes for xterm to handle", () => {
		render(<XtermTerminal theme="light" />);
		const setMode = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.final === "h",
		);

		expect(setMode?.callback([25])).toBe(false);
	});

	it("enables color-scheme updates from mixed private mode sets without consuming them", () => {
		const onInput = vi.fn();
		const view = render(
			<XtermTerminal theme="light" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		const setMode = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.final === "h",
		);

		expect(setMode?.callback([2031, 25])).toBe(false);
		view.rerender(
			<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		expect(onInput).toHaveBeenLastCalledWith("\x1b[?997;1n", "protocol");
	});

	it("disables color-scheme updates from mixed private mode resets without consuming them", () => {
		const onInput = vi.fn();
		const view = render(
			<XtermTerminal theme="light" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		const setMode = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.final === "h",
		);
		const resetMode = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.final === "l",
		);

		expect(setMode?.callback([2031])).toBe(true);
		expect(resetMode?.callback([2031, 25])).toBe(false);
		view.rerender(
			<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		expect(onInput).not.toHaveBeenCalled();
	});

	it("answers mixed color-scheme capability queries without consuming them", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="light" onReady={(terminal) => terminal.onUserInput(onInput)} />);
		const capabilityQuery = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.intermediates === "$" && id.final === "p",
		);

		expect(capabilityQuery?.callback([2031, 25])).toBe(false);
		expect(onInput).toHaveBeenCalledWith("\x1b[?2031;2$y", "protocol");
	});

	it("answers mixed color-scheme mode queries without consuming them", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="light" onReady={(terminal) => terminal.onUserInput(onInput)} />);
		const modeQuery = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.final === "n",
		);

		expect(modeQuery?.callback([996, 25])).toBe(false);
		expect(onInput).toHaveBeenCalledWith("\x1b[?997;2n", "protocol");
	});

	it("answers color-scheme capability and current-mode queries", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="light" onReady={(terminal) => terminal.onUserInput(onInput)} />);
		const capabilityQuery = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.intermediates === "$" && id.final === "p",
		);
		const modeQuery = state.lastTerminal!.csiHandlers.find(
			({ id }) => id.prefix === "?" && id.final === "n",
		);

		expect(capabilityQuery?.callback([2031])).toBe(true);
		expect(modeQuery?.callback([996])).toBe(true);
		expect(onInput.mock.calls).toEqual([
			["\x1b[?2031;2$y", "protocol"],
			["\x1b[?997;2n", "protocol"],
		]);
	});

	it("translates wheel motion into SGR wheel reports for zellij scrollback", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);
		// rowHeight = fontSize(12) * lineHeight(1.35) = 16.2px; -50px => 3 lines up.
		const suppressed = state.lastTerminal!.wheelHandler!({ deltaY: -50 } as WheelEvent);

		expect(suppressed).toBe(false);
		expect(onInput).toHaveBeenCalledWith("\x1b[<64;1;1M\x1b[<64;1;1M\x1b[<64;1;1M", "wheel");
	});

	it("handles line- and page-mode wheels (Linux/Windows mice), not just pixel deltas", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		// DOM_DELTA_LINE: deltaY is already in lines, so one notch up => one report.
		expect(state.lastTerminal!.wheelHandler!({ deltaY: -1, deltaMode: 1 } as WheelEvent)).toBe(false);
		expect(onInput).toHaveBeenLastCalledWith("\x1b[<64;1;1M", "wheel");

		// DOM_DELTA_PAGE: one page down => rows (24) line reports down.
		onInput.mockClear();
		expect(state.lastTerminal!.wheelHandler!({ deltaY: 1, deltaMode: 2 } as WheelEvent)).toBe(false);
		expect(onInput).toHaveBeenLastCalledWith("\x1b[<65;1;1M".repeat(24), "wheel");
	});

	it("scrolls down on positive wheel delta and leaves zoom (ctrl/meta) wheel alone", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		expect(state.lastTerminal!.wheelHandler!({ deltaY: 20 } as WheelEvent)).toBe(false);
		expect(onInput).toHaveBeenCalledWith("\x1b[<65;1;1M", "wheel");

		onInput.mockClear();
		expect(state.lastTerminal!.wheelHandler!({ deltaY: -50, ctrlKey: true } as WheelEvent)).toBe(false);
		expect(onInput).not.toHaveBeenCalled();
	});

	it("scrolls xterm's own viewport for normal-buffer panes with mouse tracking off (codex, plain shell)", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);
		state.lastTerminal!.modes.mouseTrackingMode = "none";
		state.lastTerminal!.buffer.active.type = "normal";

		// rowHeight = 16.2px; -50px => 3 lines up. The pane never sees these bytes;
		// we scroll the terminal's retained scrollback locally instead.
		expect(state.lastTerminal!.wheelHandler!({ deltaY: -50 } as WheelEvent)).toBe(false);
		expect(state.lastTerminal!.scrollLines).toHaveBeenLastCalledWith(-3);
		expect(onInput).not.toHaveBeenCalled();

		expect(state.lastTerminal!.wheelHandler!({ deltaY: 20 } as WheelEvent)).toBe(false);
		expect(state.lastTerminal!.scrollLines).toHaveBeenLastCalledWith(1);
		expect(onInput).not.toHaveBeenCalled();
	});

	it("falls back to PageUp/PageDown for alt-buffer panes with mouse tracking off", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);
		state.lastTerminal!.modes.mouseTrackingMode = "none";
		// Alt buffer: no local scrollback to move, and no keyboard-scroll hint, so a
		// page key per notch is the best fallback.
		state.lastTerminal!.buffer.active.type = "alternate";

		expect(state.lastTerminal!.wheelHandler!({ deltaY: -50 } as WheelEvent)).toBe(false);
		expect(onInput).toHaveBeenLastCalledWith("\x1b[5~", "wheel");
		expect(state.lastTerminal!.scrollLines).not.toHaveBeenCalled();

		expect(state.lastTerminal!.wheelHandler!({ deltaY: 20 } as WheelEvent)).toBe(false);
		expect(onInput).toHaveBeenLastCalledWith("\x1b[6~", "wheel");
	});

	it("sends SGR reports on Windows when the pane tracks the mouse (conpty delivers them to the app)", () => {
		setNavigatorPlatform("Win32");
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);
		// A mouse-tracking pane gets SGR reports on every platform; on Windows conpty
		// forwards them straight to the app. Keyboard-scroll panes (opencode) opt out
		// via the paneScrollsByKeyboard hint, tested separately.
		state.lastTerminal!.modes.mouseTrackingMode = "any";

		expect(state.lastTerminal!.wheelHandler!({ deltaY: -50 } as WheelEvent)).toBe(false);
		expect(onInput).toHaveBeenLastCalledWith("\x1b[<64;1;1M".repeat(3), "wheel");
	});

	it("sends PageUp/PageDown for keyboard-scroll panes even under a mux (opencode on macOS/Linux)", () => {
		const onInput = vi.fn();
		render(<XtermTerminal theme="dark" paneScrollsByKeyboard onReady={(terminal) => terminal.onUserInput(onInput)} />);
		// Linux (beforeEach) + mouse tracking on: without the paneScrollsByKeyboard
		// hint this would send SGR reports; the hint forces page keys.
		state.lastTerminal!.modes.mouseTrackingMode = "any";

		expect(state.lastTerminal!.wheelHandler!({ deltaY: -50 } as WheelEvent)).toBe(false);
		expect(onInput).toHaveBeenLastCalledWith("\x1b[5~", "wheel");
	});

	it("routes web links to the AO browser and does not open the system browser", () => {
		const open = vi.spyOn(window, "open").mockReturnValue(null);
		const onLinkOpen = vi.fn();
		render(<XtermTerminal onLinkOpen={onLinkOpen} theme="dark" />);

		// A left-click on an http(s) link is reported to the parent (which shows it
		// in the AO Browser panel); it must NOT spawn a system-browser window.
		expect(state.linkHandler).toBeTypeOf("function");
		state.linkHandler!({} as MouseEvent, "https://example.com");

		expect(onLinkOpen).toHaveBeenCalledWith("https://example.com");
		expect(open).not.toHaveBeenCalled();
		open.mockRestore();
	});

	it("routes OSC 8 web links to the AO browser without a system-browser window", () => {
		const open = vi.spyOn(window, "open").mockReturnValue(null);
		const onLinkOpen = vi.fn();
		render(<XtermTerminal onLinkOpen={onLinkOpen} theme="dark" />);
		const oscLinkHandler = state.lastTerminal!.options.linkHandler as {
			activate: (event: MouseEvent, uri: string) => void;
		};

		oscLinkHandler.activate({} as MouseEvent, "http://localhost:3000");

		expect(onLinkOpen).toHaveBeenCalledWith("http://localhost:3000");
		expect(open).not.toHaveBeenCalled();
		open.mockRestore();
	});

	it.each([
		["plain", "Option/Alt", { altKey: true }],
		["OSC 8", "Option/Alt", { altKey: true }],
		["plain", "Command", { metaKey: true }],
		["OSC 8", "Command", { metaKey: true }],
	])("opens %s web links in the system browser on %s+Click", (kind, _modifier, event) => {
		const openExternal = vi.fn().mockResolvedValue(undefined);
		window.ao!.app.openExternal = openExternal;
		const onLinkOpen = vi.fn();
		render(<XtermTerminal onLinkOpen={onLinkOpen} theme="dark" />);
		const oscHandler = state.lastTerminal!.options.linkHandler as { activate: (event: MouseEvent, uri: string) => void };
		const handler = kind === "plain" ? state.linkHandler! : oscHandler.activate;
		handler(event as unknown as MouseEvent, "https://example.com");
		expect(openExternal).toHaveBeenCalledWith("https://example.com");
		expect(onLinkOpen).not.toHaveBeenCalled();
	});

	it("opens non-web links (mailto:) in the system browser, not the AO browser", () => {
		const open = vi.spyOn(window, "open").mockReturnValue(null);
		const onLinkOpen = vi.fn();
		render(<XtermTerminal onLinkOpen={onLinkOpen} theme="dark" />);

		expect(state.linkHandler).toBeTypeOf("function");
		state.linkHandler!({} as MouseEvent, "mailto:dev@example.com");

		expect(open).toHaveBeenCalledWith("mailto:dev@example.com", "_blank", "noopener");
		expect(onLinkOpen).not.toHaveBeenCalled();
		open.mockRestore();
	});

	it("forces plain drag selection without raw xterm data forwarding", () => {
		render(<XtermTerminal theme="dark" />);

		expect(state.lastTerminal!.options.macOptionClickForcesSelection).toBe(true);
		expect(state.lastTerminal!._core._selectionService.enable).toHaveBeenCalled();
		expect(state.lastTerminal!._core.element.classList.remove).toHaveBeenCalledWith("enable-mouse-events");
		expect(state.lastTerminal!._core._selectionService.shouldForceSelection({} as MouseEvent)).toBe(true);
	});

	function dropTransfer(entry: { isDirectory: boolean }, files: File[]) {
		return { items: [{ webkitGetAsEntry: () => entry }], files, types: ["Files"] };
	}

	it("attaches a real dropped file's saved path, and still lets the drop bubble to the window", async () => {
		const saveDroppedFile = vi.fn().mockResolvedValue("/tmp/ao-dropped/notes.txt");
		window.ao!.terminal.saveDroppedFile = saveDroppedFile;
		const onInput = vi.fn();
		const { container } = render(
			<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />,
		);
		const host = container.firstElementChild!;
		const bubbled = vi.fn();
		window.addEventListener("drop", bubbled);
		const file = new File(["hello"], "notes.txt", { type: "text/plain" });

		fireEvent.drop(host, { dataTransfer: dropTransfer({ isDirectory: false }, [file]) });
		await waitFor(() => expect(saveDroppedFile).toHaveBeenCalledWith({ name: "notes.txt", bytes: expect.any(Uint8Array) }));
		await waitFor(() => expect(onInput).toHaveBeenCalledWith("/tmp/ao-dropped/notes.txt ", "paste"));

		window.removeEventListener("drop", bubbled);
		// Regression: no stopPropagation here — _shell.tsx's window-level listener
		// still needs this drop to reset the drag-depth counter its preceding
		// dragenter bumped, or the next folder drag inherits a stale depth and
		// never shows the overlay.
		expect(bubbled).toHaveBeenCalledTimes(1);
	});

	// Regression: a dropped folder is the app-wide "open as project" gesture
	// (_shell.tsx's window-level drop handler), not a file to attach — dropping
	// one over an active terminal pane must not be swallowed as a file-attach.
	it("lets a dropped folder bubble to the window instead of attaching it as a file", () => {
		const saveDroppedFile = vi.fn();
		window.ao!.terminal.saveDroppedFile = saveDroppedFile;
		const { container } = render(<XtermTerminal theme="dark" />);
		const host = container.firstElementChild!;
		const bubbled = vi.fn();
		window.addEventListener("drop", bubbled);
		const folder = new File([], "my-project");

		fireEvent.drop(host, { dataTransfer: dropTransfer({ isDirectory: true }, [folder]) });

		window.removeEventListener("drop", bubbled);
		expect(bubbled).toHaveBeenCalledTimes(1);
		expect(saveDroppedFile).not.toHaveBeenCalled();
	});
});
