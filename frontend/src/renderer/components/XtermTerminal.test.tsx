import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AttachableTerminal } from "../hooks/useTerminalSession";
import { useUiStore } from "../stores/ui-store";
import { XtermTerminal } from "./XtermTerminal";

const state = vi.hoisted(() => ({
	fit: vi.fn(),
	linkHandler: null as null | ((event: MouseEvent, uri: string) => void),
	lastTerminal: null as null | {
		keyHandler?: (event: KeyboardEvent) => boolean;
		wheelHandler?: (event: WheelEvent) => boolean;
		selection: string;
		options: Record<string, unknown>;
		modes: { bracketedPasteMode: boolean; mouseTrackingMode: string };
		buffer: { active: { type: string } };
		scrollLines: ReturnType<typeof vi.fn>;
		scrollToBottom: ReturnType<typeof vi.fn>;
		refresh: ReturnType<typeof vi.fn>;
		clear: ReturnType<typeof vi.fn>;
		focus: ReturnType<typeof vi.fn>;
		selectAll: ReturnType<typeof vi.fn>;
		dataListeners: Set<(data: string) => void>;
		keyListeners: Set<(event: { key: string }) => void>;
		selectionListeners: Set<() => void>;
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
		buffer = { active: { type: "normal" } };
		scrollLines = vi.fn();
		scrollToBottom = vi.fn();
		refresh = vi.fn();
		clear = vi.fn();
		focus = vi.fn();
		selectAll = vi.fn();
		dataListeners = new Set<(data: string) => void>();
		keyListeners = new Set<(event: { key: string }) => void>();
		selectionListeners = new Set<() => void>();
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
		write() {}
		writeln() {}
		dispose() {}
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
	SearchAddon: class FakeSearchAddon {},
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

	it("does not reserve width for the hidden terminal scrollbar", () => {
		render(<XtermTerminal theme="dark" />);

		expect(state.lastTerminal!._core.viewport.scrollBarWidth).toBe(0);
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

	it("auto-copies new selections and retries explicit copy if the auto-copy failed", async () => {
		render(<XtermTerminal theme="dark" />);
		const writeText = vi.fn().mockRejectedValueOnce(new Error("clipboard failed")).mockResolvedValueOnce(undefined);
		window.ao!.clipboard.writeText = writeText;

		state.lastTerminal!.selection = "retry me";
		state.lastTerminal!.selectionListeners.forEach((listener) => listener());
		await new Promise((resolve) => window.setTimeout(resolve, 0));

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
		expect(writeText).toHaveBeenCalledTimes(2);
		expect(writeText).toHaveBeenLastCalledWith("retry me");
	});

	it("shows a copied toast after a successful selection auto-copy", async () => {
		render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "toast selection";
		state.lastTerminal!.selectionListeners.forEach((listener) => listener());
		await new Promise((resolve) => window.setTimeout(resolve, 0));

		expect(await screen.findByRole("status")).toHaveTextContent("Copied to clipboard");
	});

	it("does not show a copied toast when clipboard write fails", async () => {
		window.ao!.clipboard.writeText = vi.fn().mockRejectedValue(new Error("clipboard failed"));
		render(<XtermTerminal theme="dark" />);
		state.lastTerminal!.selection = "failed selection";
		state.lastTerminal!.selectionListeners.forEach((listener) => listener());
		await new Promise((resolve) => window.setTimeout(resolve, 0));
		await Promise.resolve();

		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	it("hides the copied toast after a short delay", async () => {
		vi.useFakeTimers();
		try {
			render(<XtermTerminal theme="dark" />);
			state.lastTerminal!.selection = "timed selection";
			await act(async () => {
				state.lastTerminal!.selectionListeners.forEach((listener) => listener());
				await vi.advanceTimersByTimeAsync(0);
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
		render(<XtermTerminal theme="dark" onReady={(terminal) => terminal.onUserInput(onInput)} />);

		expect(state.lastTerminal!.dataListeners.size).toBe(0);
		state.lastTerminal!.dataListeners.forEach((listener) => listener("\x1b[A"));
		expect(onInput).not.toHaveBeenCalled();
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

	it.each(["plain", "OSC 8"])("opens %s web links in the system browser on Option/Alt+Click", (kind) => {
		const openExternal = vi.fn().mockResolvedValue(undefined);
		window.ao!.app.openExternal = openExternal;
		const onLinkOpen = vi.fn();
		render(<XtermTerminal onLinkOpen={onLinkOpen} theme="dark" />);
		const oscHandler = state.lastTerminal!.options.linkHandler as { activate: (event: MouseEvent, uri: string) => void };
		const handler = kind === "plain" ? state.linkHandler! : oscHandler.activate;
		handler({ altKey: true } as MouseEvent, "https://example.com");
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
