// Self-contained xterm.js surface, ported from yyork's terminal architecture.
//
// Design rules (the reason this component exists):
//  - The mount effect is dependency-free: the terminal instance is created once
//    per mount and NEVER torn down because a callback identity changed.
//    TerminalPane's shell-owned cache chooses the mount lifetime: retained
//    handle generations survive route switches, replacement handles get a clean
//    surface, and same-handle reconnects reuse the mounted renderer.
//  - Nothing writes into the buffer at mount. Status/empty-state belongs to DOM
//    chrome around the terminal, not inside it. Writing before layout settles
//    is what crashed xterm's Viewport (`dimensions` of a zero-sized renderer).
//  - Fitting runs on several triggers, not one: FitAddon derives the grid from
//    the measured cell box, and if it measures before the monospace font's real
//    metrics (and the post-open renderer) are resolved it mis-counts cols/rows
//    and the grid clips inside the panel. So: next frame, two settle timeouts,
//    fonts.ready, a ResizeObserver, AND an onRender convergence loop that
//    re-fits until the proposed grid stops changing (the last is the only
//    trigger that recovers a clipped grid without the host box resizing). xterm
//    itself only fires onResize when the grid actually changed, so repeated
//    fits don't spam the PTY.

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { useTranslation } from "react-i18next";
import { CanvasAddon } from "@xterm/addon-canvas";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";
import { terminalFontSizeDelta as shortcutFontSizeDelta } from "../../shared/shortcuts";
import type {
	AttachableTerminal,
	TerminalUserInputSource,
} from "../hooks/useTerminalSession";
import { aoBridge } from "../lib/bridge";
import { isDialogOrMenuOpen } from "../lib/dom-selectors";
import { TERMINAL_FONT_SIZE_DEFAULT } from "../lib/design-tokens";
import { isWebLink, openLinkInSystemBrowser } from "../lib/external-link-policy";
import { isMacPlatform } from "../lib/platform";
import { applyDocumentTheme, applyDocumentThemeStyle } from "../lib/theme";
import {
	buildCursorColorSchemeNotification,
	cursorColorSchemeReplyForOutput,
} from "../lib/cursor-color-scheme";
import {
	buildOscColorReports,
	createCursorPositionReportForwarder,
	createOscColorReportForwarder,
	type OscTerminalColors,
} from "../lib/osc-color-report";
import { buildTerminalThemes } from "../lib/terminal-themes";
import { useUiStore, type Theme, type ThemeStyle } from "../stores/ui-store";
import { TerminalSearch } from "./TerminalSearch";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";

export type XtermTerminalProps = {
	ariaLabel?: string;
	className?: string;
	fontSize?: number;
	isFullscreen?: boolean;
	theme: Theme;
	/** Resize this terminal without changing application zoom. */
	onChangeFontSize?: (delta: number) => void;
	/** Enter or exit fullscreen for the terminal pane that owns this xterm. */
	onToggleFullscreen?: () => void | Promise<void>;
	/**
	 * The pane app scrolls its transcript by keyboard (PageUp/PageDown) rather
	 * than acting on SGR wheel reports — e.g. opencode, which enables mouse
	 * tracking but never scrolls on wheel reports. Routes the wheel to page keys
	 * on every platform (see the wheel handler), fixing it under a mux too.
	 */
	paneScrollsByKeyboard?: boolean;
	/** Terminal construction failed; the owner decides how to surface it. */
	onError?: (error: unknown) => void;
	/** Called after a terminal hyperlink is opened in the OS browser. */
	onLinkOpen?: (uri: string) => void;
	/** Publish the positive grid after a retained terminal becomes visible. */
	onVisibleSize?: (cols: number, rows: number) => void;
	/** Hidden retained terminals keep parsing output but expose no UI overlays. */
	isVisible?: boolean;
	/** Cursor Agent understands AO's terminal color protocol; generic terminals do not. */
	supportsCursorColorScheme?: boolean;
	/** Move keyboard focus into xterm when a controller needs human input. */
	focusRequested?: boolean;
	/**
	 * The terminal is open in the DOM and ready to be attached to a PTY. The
	 * handle stays valid until unmount; cols/rows are live getters.
	 */
	onReady?: (terminal: AttachableTerminal) => void;
};

// Prefer the WebGL renderer, fall back to 2D canvas. Both rasterize box-drawing
// glyphs themselves onto a fixed cell grid; the DOM renderer does not, so TUI
// borders would drift. Loaded after open().
function loadRenderer(term: Terminal): void {
	let fallbackLoaded = false;
	const loadCanvasFallback = () => {
		if (fallbackLoaded) return;
		fallbackLoaded = true;
		try {
			term.loadAddon(new CanvasAddon());
		} catch (error) {
			console.warn("xterm: WebGL and canvas renderers unavailable; box-drawing may drift", error);
		}
	};
	try {
		const webgl = new WebglAddon();
		webgl.onContextLoss(() => {
			webgl.dispose();
			loadCanvasFallback();
		});
		term.loadAddon(webgl);
		return;
	} catch {
		// WebGL context unavailable — fall through to the canvas renderer.
	}
	loadCanvasFallback();
}

// xterm palette tracks the app theme (see lib/terminal-themes.ts + tokens.css).
const SUPPRESS_NATIVE_PASTE_MS = 100;
/** Long enough to notice, short enough that a second copy reads as a second copy. */
const COPY_TOAST_MS = 1400;
const AUTOFOCUS_RETRY_FRAMES = 2;
const COLOR_SCHEME_UPDATE_MODE = 2031;
const COLOR_SCHEME_QUERY = 996;

function preparePastedText(text: string): string {
	return text.replace(/\r?\n/g, "\r");
}

function bracketPastedText(text: string, bracketedPasteMode: boolean): string {
	return bracketedPasteMode ? `\x1b[200~${text}\x1b[201~` : text;
}

function isTerminalCopyShortcut(event: KeyboardEvent): boolean {
	if (event.key === "Insert") return event.ctrlKey && !event.altKey && !event.metaKey;
	if (event.key.toLowerCase() !== "c") return false;
	if (event.metaKey) return true;
	if (event.ctrlKey && event.shiftKey && !event.altKey) return true;
	return isWindowsPlatform() && event.ctrlKey && !event.shiftKey && !event.altKey && !event.metaKey;
}

function isWindowsPlatform(): boolean {
	const platform =
		(navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform ?? navigator.platform;
	return platform.toLowerCase().startsWith("win");
}

function isTerminalPasteShortcut(event: KeyboardEvent): boolean {
	if (event.key === "Insert") return event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey;
	if (event.key.toLowerCase() !== "v") return false;
	if (event.metaKey) return true;
	if (event.ctrlKey && event.shiftKey && !event.altKey) return true;
	return isWindowsPlatform() && event.ctrlKey && !event.shiftKey && !event.altKey && !event.metaKey;
}

function isTerminalSearchShortcut(event: KeyboardEvent): boolean {
	if (event.altKey || event.shiftKey || event.key.toLowerCase() !== "f") return false;
	return isMacPlatform()
		? event.metaKey && !event.ctrlKey
		: event.ctrlKey && !event.metaKey;
}

function consumeTerminalShortcut(event: KeyboardEvent): void {
	event.preventDefault();
	event.stopPropagation();
}

function terminalFontSizeDelta(event: KeyboardEvent): -1 | 0 | 1 {
	return shortcutFontSizeDelta(
		{
			key: event.key,
			code: event.code,
			ctrl: event.ctrlKey,
			meta: event.metaKey,
			shift: event.shiftKey,
			alt: event.altKey,
		},
		isMacPlatform(),
	);
}

function normalizedTerminalShortcut(event: KeyboardEvent): string | null {
	if (event.metaKey || event.shiftKey) return null;

	if (event.altKey && !event.ctrlKey) {
		switch (event.key) {
			case "ArrowLeft":
				return "\x1bb";
			case "ArrowRight":
				return "\x1bf";
			case "Backspace":
				return "\x1b\x7f";
			case "Delete":
				return "\x1bd";
			default:
				return null;
		}
	}

	if (event.ctrlKey && !event.altKey) {
		switch (event.key) {
			case "ArrowLeft":
				return "\x1b[1;5D";
			case "ArrowRight":
				return "\x1b[1;5C";
			case "Backspace":
				return "\x1b\x7f";
			case "Delete":
				return "\x1bd";
			default:
				return null;
		}
	}

	return null;
}

function terminalHasFocus(host: HTMLElement): boolean {
	const activeElement = document.activeElement;
	return !!activeElement && host.contains(activeElement);
}

function canAutoFocusTerminal(host: HTMLElement): boolean {
	if (isDialogOrMenuOpen()) return false;
	const activeElement = document.activeElement;
	if (!(activeElement instanceof HTMLElement) || activeElement === document.body || !activeElement.isConnected) return true;
	if (host.contains(activeElement)) return true;
	// Selecting a session in the sidebar deliberately leaves its navigation
	// button focused. Terminal tabs are the same intentional handoff within the
	// pane. Every other focused control remains authoritative.
	return (
		activeElement.matches("button[aria-current='page']") ||
		(activeElement.matches("button[role='tab'][aria-current]") &&
			activeElement.closest('[data-testid="session-workspace-topbar"]') !== null)
	);
}

type XtermInternal = Terminal & {
	_core?: {
		element?: HTMLElement;
		viewport?: {
			scrollBarWidth: number;
		};
		_selectionService?: {
			enable: () => void;
			shouldForceSelection: (event: MouseEvent) => boolean;
		};
	};
};

type DevXtermHost = HTMLDivElement & {
	__aoXtermForTest?: Terminal;
};

type TerminalContextMenuState = {
	canCopy: boolean;
	open: boolean;
	x: number;
	y: number;
	// The web link under the cursor when the menu opened, if any — enables the
	// "Open in system browser" item (left-click opens it in the AO Browser).
	link: string | null;
};

type TerminalContextMenuAction = "copy" | "paste" | "selectAll";

type TerminalContextMenuActions = Record<TerminalContextMenuAction, () => void>;

// For mouse-tracking panes we synthesize SGR mouse-wheel reports and write them
// to the pane; tmux (with `mouse on`, set by the runtime adapter) acts on them
// and scrolls its scrollback via copy-mode. Left to itself xterm would convert
// the wheel into cursor-arrow keys (its alt-buffer fallback), which move the
// agent's cursor rather than scrolling. SGR button 64 = wheel up, 65 = down;
// reports are 1-based and a single cell is enough for a borderless single pane.
const SGR_WHEEL_UP = 64;
const SGR_WHEEL_DOWN = 65;

function sgrWheelReport(button: number, count: number): string {
	return `\x1b[<${button};1;1M`.repeat(count);
}

// PageUp (CSI 5~) / PageDown (CSI 6~) for pane apps that scroll their transcript
// by keyboard rather than mouse reports. One page key per wheel notch: a page
// already scrolls a full screen, so scaling by line count would over-scroll.
const PAGE_UP = "\x1b[5~";
const PAGE_DOWN = "\x1b[6~";
const MAC_TERMINAL_SCROLLBAR_WIDTH = 7;
const MAC_TERMINAL_SCROLLBAR_IDLE_MS = 700;

function pageKeyReport(lines: number): string {
	return lines < 0 ? PAGE_UP : PAGE_DOWN;
}

function forceSelectionMode(term: Terminal): void {
	const internal = term as XtermInternal;
	const selectionService = internal._core?._selectionService;
	const element = internal._core?.element;
	if (!selectionService || !element) return;
	selectionService.shouldForceSelection = () => true;
	selectionService.enable();
	element.classList.remove("enable-mouse-events");
}

function configureScrollbarReservation(term: Terminal): void {
	const viewport = (term as XtermInternal)._core?.viewport;
	if (viewport) viewport.scrollBarWidth = isMacPlatform() ? MAC_TERMINAL_SCROLLBAR_WIDTH : 0;
}

export function XtermTerminal(props: XtermTerminalProps) {
	const { t } = useTranslation();
	const themeStyle = useUiStore((state) => state.themeStyle);
	const macPlatform = isMacPlatform();
	const shellRef = useRef<HTMLDivElement | null>(null);
	const hostRef = useRef<HTMLDivElement | null>(null);
	const scrollbarTrackRef = useRef<HTMLDivElement | null>(null);
	const scrollbarThumbRef = useRef<HTMLDivElement | null>(null);
	const termRef = useRef<Terminal | null>(null);
	const notifyCursorSchemeRef = useRef<(scheme: Theme, force?: boolean, retry?: boolean) => void>(() => {});
	const announcedCursorSchemeRef = useRef<Theme | null>(null);
	const searchAddonRef = useRef<SearchAddon | null>(null);
	const fitRef = useRef<(() => void) | null>(null);
	const colorSchemeReporterRef = useRef<
		((theme: Theme, themeStyle: ThemeStyle, force?: boolean) => void) | null
	>(null);
	const contextMenuActionsRef = useRef<TerminalContextMenuActions | null>(null);
	const [contextMenu, setContextMenu] = useState<TerminalContextMenuState>({
		canCopy: false,
		open: false,
		x: 0,
		y: 0,
		link: null,
	});
	const [copiedToast, setCopiedToast] = useState(false);
	const [searchOpen, setSearchOpen] = useState(false);
	const copiedToastTimerRef = useRef<number | undefined>(undefined);
	const showCopiedToastRef = useRef<() => void>(() => undefined);
	// The web link currently under the cursor, tracked via the link providers'
	// hover/leave callbacks so the right-click menu can offer "Open in system
	// browser" for it.
	const hoveredLinkRef = useRef<string | null>(null);
	// Latest callbacks in a ref so the mount effect stays dependency-free — we
	// never tear down and recreate the terminal because a handler identity
	// changed between renders.
	const callbacksRef = useRef(props);

	const setContextMenuOpen = useCallback((open: boolean) => {
		setContextMenu((current) => ({ ...current, open }));
	}, []);

	const runContextMenuAction = useCallback(
		(action: TerminalContextMenuAction) => {
			contextMenuActionsRef.current?.[action]();
			setContextMenuOpen(false);
		},
		[setContextMenuOpen],
	);
	const focusTerminal = useCallback(() => {
		try {
			termRef.current?.focus();
		} catch {
			// A retained terminal can be parked between closing search and this frame.
		}
	}, []);
	const restoreFocusFrameIdsRef = useRef<number[]>([]);
	const cancelPendingFocusRestore = useCallback(() => {
		for (const id of restoreFocusFrameIdsRef.current) cancelAnimationFrame(id);
		restoreFocusFrameIdsRef.current = [];
	}, []);
	const restoreTerminalFocus = useCallback(() => {
		cancelPendingFocusRestore();
		const frameA = requestAnimationFrame(() => {
			const frameB = requestAnimationFrame(() => {
				restoreFocusFrameIdsRef.current = [];
				// The terminal may have been hidden or reassigned to another
				// session during these two frames (e.g. navigation away from
				// this pane); re-check before stealing focus back from
				// whatever now legitimately owns it.
				const host = hostRef.current;
				if (!host || callbacksRef.current.isVisible === false || !canAutoFocusTerminal(host)) return;
				focusTerminal();
			});
			restoreFocusFrameIdsRef.current = [frameB];
		});
		restoreFocusFrameIdsRef.current = [frameA];
	}, [cancelPendingFocusRestore, focusTerminal]);
	const toggleFullscreenAndRestoreFocus = useCallback(async () => {
		try {
			await callbacksRef.current.onToggleFullscreen?.();
		} finally {
			restoreTerminalFocus();
		}
	}, [restoreTerminalFocus]);

	callbacksRef.current = props;
	showCopiedToastRef.current = () => {
		// Hidden retained terminals keep parsing output but expose no UI overlays.
		if (callbacksRef.current.isVisible === false) return;
		setCopiedToast(true);
		if (copiedToastTimerRef.current !== undefined) window.clearTimeout(copiedToastTimerRef.current);
		copiedToastTimerRef.current = window.setTimeout(() => {
			setCopiedToast(false);
			copiedToastTimerRef.current = undefined;
		}, COPY_TOAST_MS);
	};

	useEffect(
		() => () => {
			if (copiedToastTimerRef.current !== undefined) window.clearTimeout(copiedToastTimerRef.current);
		},
		[],
	);

	useEffect(() => {
		// buildTerminalThemes() reads live CSS vars from :root. Parent shell effects
		// run after child effects, so sync both independent theme axes here before
		// reading. Retained terminals subscribe to themeStyle directly and update
		// their live palette without being torn down or losing scrollback.
		applyDocumentTheme(props.theme);
		applyDocumentThemeStyle(themeStyle);
		const term = termRef.current;
		if (!term) return;
		const { dark, light } = buildTerminalThemes();
		term.options.theme = props.theme === "dark" ? dark : light;
		colorSchemeReporterRef.current?.(props.theme, themeStyle);
	}, [props.theme, themeStyle]);

	useEffect(() => {
		if (!termRef.current || !props.supportsCursorColorScheme) return;
		announcedCursorSchemeRef.current = null;
		notifyCursorSchemeRef.current(props.theme, true, true);
	}, [props.theme, props.supportsCursorColorScheme]);

	useEffect(() => {
		const term = termRef.current;
		if (!term || !props.fontSize) return undefined;
		term.options.fontSize = props.fontSize;
		fitRef.current?.();
		const timer = window.setTimeout(() => fitRef.current?.(), 50);
		return () => window.clearTimeout(timer);
	}, [props.fontSize]);

	useEffect(() => {
		const shell = shellRef.current;
		const host = hostRef.current;
		if (!shell || !host) return undefined;
		let reportedFocused = false;
		const reportFocused = (focused: boolean) => {
			const next = focused && Boolean(callbacksRef.current.onChangeFontSize);
			if (next === reportedFocused) return;
			reportedFocused = next;
			aoBridge.terminal.setFocused(next);
		};
		const handleFocusIn = () => reportFocused(true);
		const handleFocusOut = (event: FocusEvent) => {
			const next = event.relatedTarget;
			if (next instanceof Node && host.contains(next)) return;
			reportFocused(false);
		};
		host.addEventListener("focusin", handleFocusIn);
		host.addEventListener("focusout", handleFocusOut);
		const disposeFontSizeShortcut = aoBridge.terminal.onFontSizeShortcut((delta) => {
			if (!terminalHasFocus(host)) return;
			callbacksRef.current.onChangeFontSize?.(delta);
		});
		const activateLink = (event: MouseEvent, uri: string) => {
			// Left-click on a web link opens it inside the AO Browser panel (the
			// parent decides how). Non-web schemes (mailto:, etc.) still go to the OS
			// via the main process's window-open handler. Right-click to open a web
			// link in the system browser instead — see the context menu below. Cmd-click
			// follows the same escape hatch as links in the Chat surface.
			if (isWebLink(uri)) {
				if (event.altKey || event.metaKey) {
					void openLinkInSystemBrowser(uri);
					return;
				}
				callbacksRef.current.onLinkOpen?.(uri);
				return;
			}
			window.open(uri, "_blank", "noopener");
		};
		const trackHover = (_event: MouseEvent, uri: string) => {
			hoveredLinkRef.current = isWebLink(uri) ? uri : null;
		};
		const clearHover = () => {
			hoveredLinkRef.current = null;
		};

		let term: Terminal;
		try {
			const { dark, light } = buildTerminalThemes();
			term = new Terminal({
				// Required for the Unicode 11 width addon below.
				allowProposedApi: true,
				cursorBlink: true,
				// Resolve the Nerd Font stack from --font-mono (styles.css) at
				// construction so terminal glyphs follow the app's font tokens. The
				// box-drawing grid is rasterized by the WebGL/canvas renderer itself,
				// but powerline separators and file-type icons are real PUA codepoints
				// that must come from a system-installed Nerd Font.
				fontFamily:
					getComputedStyle(shell).getPropertyValue("--font-mono").trim() ||
					'ui-monospace, Menlo, Monaco, "Courier New", monospace',
				fontSize: props.fontSize ?? TERMINAL_FONT_SIZE_DEFAULT,
				lineHeight: 1.35,
				linkHandler: { activate: activateLink, hover: trackHover, leave: clearHover },
				// Preserve standard terminal semantics: many agent TUIs use bold ANSI
				// colors specifically to select the bright palette.
				drawBoldTextInBrightColors: true,
				// Agent TUIs already choose foreground/background pairs. A forced
				// contrast transform changes their RGB values and makes syntax and diff
				// colors diverge from the same CLI in a native terminal.
				minimumContrastRatio: 1,
				// Alt-buffer panes (tmux attach, mouse-tracking agent TUIs) never feed
				// this buffer — the alt screen doesn't accumulate scrollback — so this
				// only matters for normal-buffer panes that print their transcript and
				// rely on the terminal's scrollback (codex, a plain shell). Keep it > 0
				// so that history survives to be scrolled locally (see the wheel
				// handler's normal-buffer branch). macOS exposes that history through a
				// slim draggable scrollbar; other platforms retain the existing hidden
				// scrollbar behavior for now.
				scrollback: 5000,
				theme: props.theme === "dark" ? dark : light,
			});
		} catch (error) {
			callbacksRef.current.onError?.(error);
			return undefined;
		}

		termRef.current = term;

		const fit = new FitAddon();
		term.loadAddon(fit);
		const unicode = new Unicode11Addon();
		term.loadAddon(unicode);
		term.unicode.activeVersion = "11";
		// Open plain and OSC 8 links in the OS browser. The default handlers call
		// window.open() with no URL and then assigns location.href, but the
		// Electron main process denies every window.open and only forwards the URL
		// passed to it (main.ts setWindowOpenHandler), so the default handlers'
		// empty open is dropped and clicks silently no-op. Pass the matched URL to
		// window.open directly so the main process routes it to shell.openExternal.
		term.loadAddon(new WebLinksAddon(activateLink, { hover: trackHover, leave: clearHover }));
		const searchAddon = new SearchAddon();
		searchAddonRef.current = searchAddon;
		term.loadAddon(searchAddon);

		term.open(host);
		// Browser integration tests need to wait on xterm's buffer state, not
		// infer it from a hidden viewport element whose scrollTop can lag.
		// Vite removes this development-only seam from packaged builds.
		if (import.meta.env.DEV) {
			(host as DevXtermHost).__aoXtermForTest = term;
		}
		// xterm 5 has no public scrollbar-width option. Keep its private FitAddon
		// reservation aligned with our CSS: a stable 7px macOS gutter, and no
		// reservation on platforms where the scrollbar remains hidden.
		configureScrollbarReservation(term);
		loadRenderer(term);
		term.options.macOptionClickForcesSelection = true;
		forceSelectionMode(term);

		// xterm 5's native viewport scrollbar follows macOS's system auto-hide
		// preference even when its WebKit pseudo-elements are styled. Keep the
		// native viewport hidden and mirror its normal-buffer geometry into a small
		// app-owned thumb. Like a native macOS overlay scrollbar, it appears while
		// scrolling or dragging and fades after the interaction goes idle.
		const scrollbarTrack = scrollbarTrackRef.current;
		const scrollbarThumb = scrollbarThumbRef.current;
		let scrollbarFrame: number | null = null;
		let scrollbarHideTimer: number | null = null;
		let scrollbarDrag: { pointerId: number; startLine: number; startY: number } | null = null;
		const revealScrollbar = () => {
			if (!scrollbarTrack || scrollbarTrack.dataset.scrollable !== "true") return;
			scrollbarTrack.dataset.active = "true";
			if (scrollbarHideTimer !== null) window.clearTimeout(scrollbarHideTimer);
			scrollbarHideTimer = null;
			if (scrollbarDrag) return;
			scrollbarHideTimer = window.setTimeout(() => {
				scrollbarTrack.dataset.active = "false";
				scrollbarHideTimer = null;
			}, MAC_TERMINAL_SCROLLBAR_IDLE_MS);
		};
		const updateScrollbar = () => {
			scrollbarFrame = null;
			if (!scrollbarTrack || !scrollbarThumb) return;
			const buffer = term.buffer.active;
			const maxScrollLine = buffer.type === "normal" ? buffer.baseY : 0;
			const trackHeight = scrollbarTrack.clientHeight;
			if (maxScrollLine <= 0 || trackHeight <= 0) {
				scrollbarTrack.dataset.scrollable = "false";
				scrollbarTrack.dataset.active = "false";
				return;
			}
			const totalLines = maxScrollLine + term.rows;
			const thumbHeight = Math.max(24, (trackHeight * term.rows) / totalLines);
			const travel = Math.max(0, trackHeight - thumbHeight);
			const thumbTop = travel * (buffer.viewportY / maxScrollLine);
			scrollbarThumb.style.height = `${thumbHeight}px`;
			scrollbarThumb.style.transform = `translateY(${thumbTop}px)`;
			scrollbarTrack.dataset.scrollable = "true";
		};
		const scheduleScrollbarUpdate = () => {
			if (!scrollbarTrack || scrollbarFrame !== null) return;
			scrollbarFrame = requestAnimationFrame(updateScrollbar);
		};
		const scrollPositionChange = scrollbarTrack
			? term.onScroll(() => {
				scheduleScrollbarUpdate();
				revealScrollbar();
			})
			: null;
		const scrollbarResize = scrollbarTrack ? term.onResize(scheduleScrollbarUpdate) : null;
		const scrollToPointer = (clientY: number) => {
			if (!scrollbarTrack || !scrollbarThumb) return;
			const maxScrollLine = term.buffer.active.type === "normal" ? term.buffer.active.baseY : 0;
			const trackRect = scrollbarTrack.getBoundingClientRect();
			const travel = trackRect.height - scrollbarThumb.getBoundingClientRect().height;
			if (maxScrollLine <= 0 || travel <= 0) return;
			const thumbTop = Math.min(travel, Math.max(0, clientY - trackRect.top - scrollbarThumb.offsetHeight / 2));
			term.scrollToLine(Math.round((thumbTop / travel) * maxScrollLine));
		};
		const scrollbarPointerDown = (event: PointerEvent) => {
			if (!scrollbarTrack || !scrollbarThumb || scrollbarTrack.dataset.scrollable !== "true") return;
			event.preventDefault();
			scrollbarTrack.setPointerCapture(event.pointerId);
			if (event.target !== scrollbarThumb) scrollToPointer(event.clientY);
			scrollbarDrag = {
				pointerId: event.pointerId,
				startLine: term.buffer.active.viewportY,
				startY: event.clientY,
			};
			revealScrollbar();
		};
		const scrollbarPointerMove = (event: PointerEvent) => {
			if (!scrollbarDrag || scrollbarDrag.pointerId !== event.pointerId || !scrollbarTrack || !scrollbarThumb) return;
			const maxScrollLine = term.buffer.active.type === "normal" ? term.buffer.active.baseY : 0;
			const travel = scrollbarTrack.clientHeight - scrollbarThumb.offsetHeight;
			if (maxScrollLine <= 0 || travel <= 0) return;
			const lineDelta = ((event.clientY - scrollbarDrag.startY) / travel) * maxScrollLine;
			term.scrollToLine(Math.round(scrollbarDrag.startLine + lineDelta));
		};
		const scrollbarPointerUp = (event: PointerEvent) => {
			if (!scrollbarDrag || scrollbarDrag.pointerId !== event.pointerId) return;
			scrollbarDrag = null;
			if (scrollbarTrack?.hasPointerCapture(event.pointerId)) scrollbarTrack.releasePointerCapture(event.pointerId);
			revealScrollbar();
		};
		scrollbarTrack?.addEventListener("pointerdown", scrollbarPointerDown);
		scrollbarTrack?.addEventListener("pointermove", scrollbarPointerMove);
		scrollbarTrack?.addEventListener("pointerup", scrollbarPointerUp);
		scrollbarTrack?.addEventListener("pointercancel", scrollbarPointerUp);
		scheduleScrollbarUpdate();

		const copySelection = (options?: { clipboardData?: DataTransfer | null }) => {
			const selection = term.getSelection();
			if (!selection) return false;
			options?.clipboardData?.setData("text/plain", selection);
			void aoBridge.clipboard
				.writeText(selection)
				.then(() => {
					showCopiedToastRef.current();
				})
				.catch((error) => {
					console.warn("Unable to copy terminal selection", error);
				});
			return true;
		};
		const userInputListeners = new Set<(data: string, source: TerminalUserInputSource) => boolean | void>();
		const emitUserInput = (data: string, source: TerminalUserInputSource) => {
			if (data.length === 0) return false;
			let accepted = false;
			userInputListeners.forEach((listener) => {
				if (listener(data, source) === true) accepted = true;
			});
			return accepted;
		};
		// xterm 5 does not implement the modern terminal color-scheme protocol.
		// OpenTUI clients use it to receive live light/dark changes after startup.
		let colorSchemeUpdatesEnabled = false;
		let currentColorScheme = props.theme;
		let currentThemeStyle = themeStyle;
		const reportColorScheme = (theme: Theme, nextThemeStyle: ThemeStyle, force = false) => {
			const changed = theme !== currentColorScheme || nextThemeStyle !== currentThemeStyle;
			currentColorScheme = theme;
			currentThemeStyle = nextThemeStyle;
			if (!force && (!colorSchemeUpdatesEnabled || !changed)) return;
			emitUserInput(`\x1b[?997;${theme === "dark" ? 1 : 2}n`, "protocol");
		};
		const hasCsiMode = (params: (number | number[])[], mode: number) =>
			params.some((param) => param === mode);
		colorSchemeReporterRef.current = reportColorScheme;
		const setColorSchemeUpdates = term.parser.registerCsiHandler(
			{ prefix: "?", final: "h" },
			(params) => {
				if (!hasCsiMode(params, COLOR_SCHEME_UPDATE_MODE)) return false;
				colorSchemeUpdatesEnabled = true;
				currentColorScheme = callbacksRef.current.theme;
				currentThemeStyle = useUiStore.getState().themeStyle;
				return params.length === 1;
			},
		);
		const resetColorSchemeUpdates = term.parser.registerCsiHandler(
			{ prefix: "?", final: "l" },
			(params) => {
				if (!hasCsiMode(params, COLOR_SCHEME_UPDATE_MODE)) return false;
				colorSchemeUpdatesEnabled = false;
				return params.length === 1;
			},
		);
		const queryColorSchemeCapability = term.parser.registerCsiHandler(
			{ prefix: "?", intermediates: "$", final: "p" },
			(params) => {
				if (!hasCsiMode(params, COLOR_SCHEME_UPDATE_MODE)) return false;
				emitUserInput(`\x1b[?${COLOR_SCHEME_UPDATE_MODE};${colorSchemeUpdatesEnabled ? 1 : 2}$y`, "protocol");
				return params.length === 1;
			},
		);
		const queryColorScheme = term.parser.registerCsiHandler(
			{ prefix: "?", final: "n" },
			(params) => {
				if (!hasCsiMode(params, COLOR_SCHEME_QUERY)) return false;
				reportColorScheme(
					callbacksRef.current.theme,
					useUiStore.getState().themeStyle,
					true,
				);
				return params.length === 1;
			},
		);
		const terminalColorsForScheme = (scheme: Theme): OscTerminalColors => {
			const palette = buildTerminalThemes()[scheme];
			return {
				foreground: palette.foreground ?? "",
				background: palette.background ?? "",
				cursor: palette.cursor ?? palette.foreground ?? "",
			};
		};
		const notifyCursorScheme = (scheme: Theme, force = false, retry = false) => {
			if (!force && announcedCursorSchemeRef.current === scheme) return;
			announcedCursorSchemeRef.current = scheme;
			const bytes =
				buildOscColorReports(terminalColorsForScheme(scheme)) +
				buildCursorColorSchemeNotification(scheme);
			const send = () => emitUserInput(bytes, "protocol");
			send();
			if (!retry) return;
			for (const timer of schemeRetryTimers) window.clearTimeout(timer);
			schemeRetryTimers = [50, 200].map((delayMs) => window.setTimeout(send, delayMs));
		};
		let schemeRetryTimers: number[] = [];
		notifyCursorSchemeRef.current = notifyCursorScheme;
		const pasteText = (text: string) => {
			const prepared = preparePastedText(text);
			const bracketed = term.modes.bracketedPasteMode && term.options.ignoreBracketedPasteMode !== true;
			emitUserInput(bracketPastedText(prepared, bracketed), "paste");
		};
		let suppressNextNativePaste = false;
		let suppressPasteTimer: number | null = null;
		const clearSuppressNativePaste = () => {
			suppressNextNativePaste = false;
			if (suppressPasteTimer !== null) {
				window.clearTimeout(suppressPasteTimer);
				suppressPasteTimer = null;
			}
		};
		const suppressNativePasteOnce = () => {
			suppressNextNativePaste = true;
			if (suppressPasteTimer !== null) window.clearTimeout(suppressPasteTimer);
			suppressPasteTimer = window.setTimeout(clearSuppressNativePaste, SUPPRESS_NATIVE_PASTE_MS);
		};
		const pasteFromClipboard = () => {
			void aoBridge.clipboard
				.readText()
				.then(pasteText)
				.catch((error) => {
					console.warn("Unable to paste terminal clipboard text", error);
				});
		};
		const focusTerminal = () => {
			try {
				term.focus();
			} catch {
				// Terminal is being torn down or its hidden textarea is unavailable.
			}
		};
		contextMenuActionsRef.current = {
			copy: () => {
				copySelection();
				focusTerminal();
			},
			paste: () => {
				pasteFromClipboard();
				focusTerminal();
			},
			selectAll: () => {
				term.selectAll();
				focusTerminal();
			},
		};
		const openContextMenu = (event: MouseEvent) => {
			event.preventDefault();
			event.stopPropagation();
			setContextMenu({
				canCopy: term.hasSelection(),
				open: true,
				x: event.clientX,
				y: event.clientY,
				link: hoveredLinkRef.current,
			});
		};
		shell.addEventListener("contextmenu", openContextMenu);
		term.attachCustomKeyEventHandler((event) => {
			// xterm invokes this same handler on keydown, keyup, AND keypress (see
			// Terminal.ts _keyDown/_keyUp/_keyPress). Only keydown should trigger our
			// shortcut actions (copy/paste/word-nav) — otherwise releasing the key
			// re-matches the same combo and fires the action a second time (double
			// paste, double word-delete, etc). keyup/keypress fall through to
			// xterm's own default handling for that event type.
			if (event.type === "keyup" || event.type === "keypress") return true;
			if (isTerminalSearchShortcut(event)) {
				consumeTerminalShortcut(event);
				setSearchOpen(true);
				return false;
			}
			// Shift+Enter → newline without submitting, matching Claude Code / Codex.
			// A terminal normally sends the same CR for Enter and Shift+Enter, so the
			// agent can't distinguish them; emit the meta-return (ESC+CR) that
			// readline/Ink-based TUIs interpret as "insert a newline" rather than
			// "submit". Plain Enter still falls through to xterm's default CR.
			//
			// SCOPE: this meta-return is applied to every pane intentionally for now.
			// It is correct for agent TUIs but untested and unintended for plain login
			// shells, where ESC+CR is not a "newline" affordance. The correct fix is to
			// scope it by pane kind — TerminalPane already branches on
			// `terminalTarget?.kind === "shell"` at the XtermTerminal call site — once
			// this branch is rebased onto main, which brings that discriminator (and
			// ShellTerminalsView) that does not yet exist here. Until then the behavior
			// is left unchanged and the emitted bytes are identical for all panes.
			if (event.key === "Enter" && event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey) {
				consumeTerminalShortcut(event);
				emitUserInput("\x1b\r", "keyboard");
				return false;
			}
			const fontSizeDelta = terminalFontSizeDelta(event);
			if (fontSizeDelta !== 0 && callbacksRef.current.onChangeFontSize) {
				consumeTerminalShortcut(event);
				callbacksRef.current.onChangeFontSize(fontSizeDelta);
				return false;
			}
			if (isTerminalCopyShortcut(event)) {
				if (copySelection()) {
					consumeTerminalShortcut(event);
					return false;
				}
				if ((event.ctrlKey && event.shiftKey) || (event.key === "Insert" && event.ctrlKey)) {
					consumeTerminalShortcut(event);
					return false;
				}
				return true;
			}
			if (isTerminalPasteShortcut(event)) {
				consumeTerminalShortcut(event);
				suppressNativePasteOnce();
				pasteFromClipboard();
				return false;
			}
			const normalized = normalizedTerminalShortcut(event);
			if (!normalized) return true;
			consumeTerminalShortcut(event);
			emitUserInput(normalized, "shortcut");
			return false;
		});
		const copyInput = (event: ClipboardEvent) => {
			if (!copySelection({ clipboardData: event.clipboardData })) return;
			event.preventDefault();
		};
		const copyShortcut = (event: KeyboardEvent) => {
			if (!isTerminalCopyShortcut(event) || !terminalHasFocus(shell) || !copySelection()) return;
			event.preventDefault();
			event.stopPropagation();
		};
		shell.addEventListener("copy", copyInput);
		window.addEventListener("keydown", copyShortcut, true);

		const fitTerminal = () => {
			// Parked terminals keep their last measured box and continue parsing
			// output, but must not refit or emit PTY resizes while hidden.
			if (callbacksRef.current.isVisible === false) return;
			try {
				fit.fit();
			} catch {
				// Container momentarily has no size (hidden/unmounting) — a later
				// trigger retries.
			}
		};
		// ResizeObserver fires for every intermediate box during native fullscreen,
		// sidebar drags and other animated application layout. Fitting on every
		// callback repeatedly reallocates xterm's WebGL surface, so those changes
		// normally settle through the debounce below. A short, explicit live-resize
		// marker lets controlled layout animations keep xterm visually in step.
		const FIT_QUIET_MS = 120;
		const FIT_CAP_MS = 500;
		let fitQuietTimer: ReturnType<typeof setTimeout> | null = null;
		let fitCapTimer: ReturnType<typeof setTimeout> | null = null;
		let fitAllowsHidden = false;
		let disposed = false;
		const fitSettledListeners = new Set<() => void>();
		const flushScheduledFit = () => {
			if (disposed) return;
			if (fitQuietTimer !== null) {
				clearTimeout(fitQuietTimer);
				fitQuietTimer = null;
			}
			if (fitCapTimer !== null) {
				clearTimeout(fitCapTimer);
				fitCapTimer = null;
			}
			if (fitAllowsHidden || callbacksRef.current.isVisible !== false) {
				try {
					fit.fit();
				} catch {
					// The next observer/window event retries if the host is transiently
					// unmeasurable (for example while entering fullscreen).
				}
			}
			fitAllowsHidden = false;
			for (const listener of [...fitSettledListeners]) listener();
			fitSettledListeners.clear();
		};
		const scheduleStableFit = (allowHidden = false, onSettled?: () => void) => {
			if (disposed) return;
			if (!allowHidden && callbacksRef.current.isVisible === false) return;
			fitAllowsHidden ||= allowHidden;
			if (onSettled) fitSettledListeners.add(onSettled);
			if (fitQuietTimer !== null) clearTimeout(fitQuietTimer);
			fitQuietTimer = setTimeout(flushScheduledFit, FIT_QUIET_MS);
			if (fitCapTimer === null) fitCapTimer = setTimeout(flushScheduledFit, FIT_CAP_MS);
		};
		// While activation preparation is pending, observer/window events must keep
		// extending the same quiet window even though the container is intentionally
		// hidden behind the cover. A normally parked terminal still ignores them.
		const scheduleVisibleFit = () => scheduleStableFit(fitAllowsHidden);
		fitRef.current = scheduleVisibleFit;

		const raf = requestAnimationFrame(fitTerminal);
		// 50/250ms catch the common settle; 600/1200ms are a session-bounded
		// backstop. By 600ms the WebGL atlas and font metrics are unambiguously
		// warm, so even if the convergence loop below detached at a briefly-stable
		// wrong measurement, this re-measures the real cell box and corrects,
		// firing the PTY resize that makes the pane repaint cleanly (clearing
		// any ghost frame). fit() is idempotent: a no-op when the grid is already
		// right, so a correct terminal never reflows.
		const settleTimers = [50, 250, 600, 1200].map((ms) => window.setTimeout(scheduleVisibleFit, ms));
		if (document.fonts?.ready) {
			void document.fonts.ready.then(() => scheduleStableFit());
		}
		const observer = new ResizeObserver(() => {
			if (host.closest('[data-terminal-live-resize="true"]')) {
				fitTerminal();
				return;
			}
			scheduleVisibleFit();
		});
		observer.observe(host);

		// Recovery re-fit that does NOT depend on the host box changing size.
		//
		// FitAddon derives the grid by dividing the pane box by the renderer's
		// measured cell box. That box is measured asynchronously: the WebGL
		// renderer loads after open() and the monospace font's real metrics
		// resolve a frame or more later, so the early fits above can divide by a
		// not-yet-final cell box, mis-count cols/rows, and clip the grid inside the
		// pane. The fixed settle window (rAF, timeouts, fonts.ready) may all run
		// before the cell box is final, and the ResizeObserver never fires to
		// correct it because the host's pixel box is a stable height:100%, so a
		// wrong grid would otherwise freeze for the whole session.
		//
		// onRender fires on every renderer repaint, including the repaint after
		// the metrics settle. Each fire re-proposes dimensions from the *current*
		// measured cell box. Crucially we never re-fit straight off a single
		// frame's proposal: the WebGL atlas warm-up can emit a one-frame transient
		// cell box (e.g. a doubled box on a HiDPI display) that halves the grid,
		// and committing it would lock the terminal at half size and detach (the
		// #313 ghost). So a differing proposal must REPEAT identically across two
		// consecutive renders — proving the measurement settled — before we apply
		// it. proposeDimensions returns undefined until the cell box is non-zero,
		// so a fit is never accepted from an unmeasured cell. Once the proposal
		// holds at the live grid for a few frames (or a hard re-fit cap is hit) the
		// listener detaches, so steady-state content renders cost nothing.
		const STABLE_FRAMES_TARGET = 3;
		const MAX_REFITS = 20;
		let stableFrames = 0;
		let refits = 0;
		let pending: { cols: number; rows: number } | null = null;
		const stabilizer = term.onRender(() => {
			const proposed = fit.proposeDimensions();
			if (!proposed || !proposed.cols || !proposed.rows) return;
			if (proposed.cols !== term.cols || proposed.rows !== term.rows) {
				stableFrames = 0;
				// Only act once the same differing proposal repeats — a single-frame
				// transient never gets committed, it just updates `pending`.
				if (pending && pending.cols === proposed.cols && pending.rows === proposed.rows) {
					pending = null;
					if (refits++ >= MAX_REFITS) {
						stabilizer.dispose();
						return;
					}
					fitTerminal();
					return;
				}
				pending = { cols: proposed.cols, rows: proposed.rows };
				return;
			}
			pending = null;
			if (++stableFrames >= STABLE_FRAMES_TARGET) stabilizer.dispose();
		});

		// OS window resize and monitor/DPR changes also alter the true cell box
		// without touching the host's height:100% box, so the ResizeObserver above
		// misses them. Listen on window directly as a session-long recovery path.
		window.addEventListener("resize", scheduleVisibleFit);

		// Do not replace this with term.onData. xterm's raw data stream can include
		// terminal-generated control responses during attach/repaint; forwarding
		// those bytes through the mux writes dirty input into the real Codex PTY and
		// corrupts the TUI. Keyboard is the only safe generic text path here; paste,
		// composition, shortcuts, and wheel reports are emitted explicitly below.
		// Forward validated OSC 4/10/11/12 color replies and cursor-position
		// reports only. Interactive prompts such as `gh auth login` use DSR to ask
		// xterm for the cursor position and block until the corresponding CPR reaches
		// the PTY. Other onData bytes must not reach the PTY or agent TUIs break.
		// Retained terminals can change providers without remounting. Keep the
		// listener mounted for every provider so standard color replies continue to
		// reach the PTY after a provider change.
		const oscColorForwarder = createOscColorReportForwarder((report) => {
			emitUserInput(report, "protocol");
		});
		const cursorPositionForwarder = createCursorPositionReportForwarder((report) => {
			emitUserInput(report, "protocol");
		});
		const protocolInput = term.onData((data) => {
			oscColorForwarder.push(data);
			cursorPositionForwarder.push(data);
		});
		const keyInput = term.onKey(({ key }) => emitUserInput(key, "keyboard"));

		// Translate wheel motion into SGR wheel reports for the pane (see
		// sgrWheelReport), one report per scrolled line. WheelEvent.deltaMode
		// varies by platform/device: trackpads and normalized wheels report
		// pixels (mode 0, the macOS case), while many Linux/Windows mouse wheels
		// report whole lines (mode 1) or pages (mode 2). Mirror xterm's native
		// getLinesScrolled across all three so scroll works everywhere; pixel
		// deltas accumulate so a full cell-height emits one line. Returning false
		// suppresses xterm's arrow-key wheel fallback. Ctrl/Cmd wheel is the
		// font-size zoom (CenterPane), so leave it for that handler.
		let wheelAccumPx = 0;
		term.attachCustomWheelEventHandler((event) => {
			if (event.ctrlKey || event.metaKey) return false;
			let lines: number;
			if (event.deltaMode === 1 /* DOM_DELTA_LINE */) {
				lines = Math.trunc(event.deltaY) || Math.sign(event.deltaY);
			} else if (event.deltaMode === 2 /* DOM_DELTA_PAGE */) {
				lines = (Math.trunc(event.deltaY) || Math.sign(event.deltaY)) * term.rows;
			} else {
				const rowHeight = (term.options.fontSize ?? TERMINAL_FONT_SIZE_DEFAULT) * (term.options.lineHeight ?? 1);
				wheelAccumPx += event.deltaY;
				lines = Math.trunc(wheelAccumPx / rowHeight);
				wheelAccumPx -= lines * rowHeight;
			}
			if (lines === 0) return false;
			// A full-screen TUI that keeps its own transcript and scrolls it only by
			// keyboard (opencode) ignores wheel/mouse reports on every platform; route
			// its wheel to page keys. Kept first so opencode is unaffected by the
			// buffer-aware paths below.
			if (callbacksRef.current.paneScrollsByKeyboard) {
				emitUserInput(pageKeyReport(lines), "wheel");
				return false;
			}
			// A normal-buffer pane with mouse tracking off (codex, a plain shell)
			// prints its transcript and relies on the terminal's own scrollback — the
			// way it scrolls in a raw terminal. Scroll xterm's viewport locally; the
			// pane never sees these bytes. Requires scrollback > 0 (see Terminal opts).
			if (term.modes.mouseTrackingMode === "none" && term.buffer.active.type === "normal") {
				term.scrollLines(lines);
				return false;
			}
			// Mouse tracking on: the pane (tmux/zellij copy-mode, or any app that
			// tracks the mouse) acts on SGR wheel reports. On Windows conpty this
			// reaches the app directly; under a mux it drives copy-mode.
			if (term.modes.mouseTrackingMode !== "none") {
				const button = lines < 0 ? SGR_WHEEL_UP : SGR_WHEEL_DOWN;
				emitUserInput(sgrWheelReport(button, Math.abs(lines)), "wheel");
				return false;
			}
			// Alt-buffer pane with mouse tracking off and no keyboard-scroll hint:
			// no scrollback to move locally, so fall back to page keys.
			emitUserInput(pageKeyReport(lines), "wheel");
			return false;
		});
		const pasteInput = (event: ClipboardEvent) => {
			event.preventDefault();
			event.stopPropagation();
			if (suppressNextNativePaste) {
				clearSuppressNativePaste();
				return;
			}
			const text = event.clipboardData?.getData("text/plain") ?? "";
			pasteText(text);
		};
		const compositionInput = (event: CompositionEvent) => {
			emitUserInput(event.data, "composition");
		};
		shell.addEventListener("paste", pasteInput, true);
		shell.addEventListener("compositionend", compositionInput, true);

		// A file dropped on the pane inserts its path, mirroring a native terminal
		// so an agent (e.g. Claude Code) attaches it. The sandboxed renderer cannot
		// read a dropped file's original path on macOS, so the bytes are stashed to
		// a temp file by the main process and that path is inserted instead.
		const isFileDrag = (event: DragEvent) => Array.from(event.dataTransfer?.types ?? []).includes("Files");
		const dragOverInput = (event: DragEvent) => {
			if (!isFileDrag(event)) return;
			event.preventDefault();
			if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
		};
		// A dropped folder is the app-wide "open as project" gesture (see
		// _shell.tsx's window-level drop handler), not a file to attach. Let it
		// bubble untouched — swallowing it here (preventDefault/stopPropagation)
		// would silently absorb the drop into this file-attach flow instead.
		const isDirectoryDrag = (event: DragEvent) =>
			event.dataTransfer?.items?.[0]?.webkitGetAsEntry?.()?.isDirectory ?? false;
		const dropInput = (event: DragEvent) => {
			if (isDirectoryDrag(event)) return;
			const files = Array.from(event.dataTransfer?.files ?? []);
			if (files.length === 0) return;
			event.preventDefault();
			// Deliberately no stopPropagation: _shell.tsx's window-level listener
			// still needs this drop to reset its drag-depth counter (bumped by the
			// dragenter that already bubbled past this host, unseen by this
			// handler), or the next folder drag inherits a stale nonzero depth and
			// never shows the overlay.
			void (async () => {
				const paths: string[] = [];
				for (const file of files) {
					try {
						const bytes = new Uint8Array(await file.arrayBuffer());
						const saved = await aoBridge.terminal.saveDroppedFile({ name: file.name, bytes });
						if (saved) paths.push(saved);
					} catch (error) {
						console.warn("Unable to attach dropped file", error);
					}
				}
				if (paths.length === 0) return;
				pasteText(`${paths.map((p) => (/\s/.test(p) ? `'${p}'` : p)).join(" ")} `);
			})();
		};
		shell.addEventListener("dragover", dragOverInput);
		shell.addEventListener("drop", dropInput);

		const showLatestOutput = () => {
			term.scrollToBottom();
			// Hidden output can leave the offscreen DOM scrollbar stale even
			// after xterm's logical viewport moves. Synchronize it before either
			// the first-load cover or retained-cache container is revealed.
			const viewport = host.querySelector<HTMLElement>(".xterm-viewport");
			if (!viewport) return;
			viewport.scrollTop = Math.max(0, viewport.scrollHeight - viewport.clientHeight);
			scheduleScrollbarUpdate();
		};

		let cancelActivationPreparation: (() => void) | null = null;
		const prepareForActivation = (): Promise<void> => {
			cancelActivationPreparation?.();
			return new Promise((resolve) => {
				let firstFrame: number | null = null;
				let paintFrame: number | null = null;
				let finished = false;
				const finish = () => {
					if (finished) return;
					finished = true;
					if (firstFrame !== null) cancelAnimationFrame(firstFrame);
					if (paintFrame !== null) cancelAnimationFrame(paintFrame);
					if (cancelActivationPreparation === finish) cancelActivationPreparation = null;
					resolve();
				};
				cancelActivationPreparation = finish;

				const finishAcrossPaintFrames = () => {
					if (finished) return;
					showLatestOutput();
					firstFrame = requestAnimationFrame(() => {
						firstFrame = null;
						// Reconcile after the settled fit, then remain hidden through a
						// second frame so Chromium composites the final viewport.
						showLatestOutput();
						paintFrame = requestAnimationFrame(() => {
							paintFrame = null;
							finish();
						});
					});
				};
				// The container is in its real slot but remains hidden. Wait for its
				// dimensions to settle (including fullscreen/sidebar transitions), fit
				// once, and avoid the old unconditional full-grid refresh.
				scheduleStableFit(true, finishAcrossPaintFrames);
			});
		};

		// Live cols/rows getters: the owner reads the current grid at attach time,
		// not a snapshot taken at ready time (the first fit may not have run yet).
		const handle: AttachableTerminal = {
			get cols() {
				return term.cols;
			},
			get rows() {
				return term.rows;
			},
			// Forward xterm's write callback: it fires once THIS chunk has been
			// parsed into the buffer, which is what lets the attachment reveal the
			// pane at the replay's settled scroll position (issue #3160).
			write: (data, done, source = "live") => {
				let hasEsc = false;
				for (let i = 0; i < data.length; i++) {
					if (data[i] === 0x1b) {
						hasEsc = true;
						break;
					}
				}
				if (source === "replay") {
					// Existing panes replay historical DSRs through xterm. Clear any old
					// correlation credits so their generated CPRs cannot reach the live PTY.
					cursorPositionForwarder.dispose();
				}
				if (hasEsc || cursorPositionForwarder.hasPartialRequest()) {
					// A DSR can be split immediately after ESC. Decode an ESC-free chunk
					// only while a request prefix from the preceding chunk is incomplete.
					const chunk = new TextDecoder().decode(data);
					if (source === "live") cursorPositionForwarder.observeOutput(chunk);
					if (hasEsc) {
						const reply = callbacksRef.current.supportsCursorColorScheme
							? cursorColorSchemeReplyForOutput(chunk, callbacksRef.current.theme)
							: null;
						if (reply) {
							announcedCursorSchemeRef.current = null;
							notifyCursorScheme(callbacksRef.current.theme, true, true);
						}
					}
				}
				term.write(data, () => {
					scheduleScrollbarUpdate();
					done?.();
				});
			},
			writeln: (line) => term.writeln(line, scheduleScrollbarUpdate),
			showLatestOutput,
			prepareForActivation,
			// Live buffer discriminator for predictive local echo on cloud panes:
			// predictions run only while the NORMAL buffer is active (alt-screen
			// TUIs repaint too aggressively to predict into).
			bufferType: () => term.buffer.active.type,
			notifyCursorColorScheme: () => {
				if (callbacksRef.current.supportsCursorColorScheme) {
					notifyCursorScheme(callbacksRef.current.theme, false, true);
				}
			},
			sendUserInput: (data, source = "shortcut") => emitUserInput(data, source),
			onUserInput: (listener) => {
				userInputListeners.add(listener);
				return { dispose: () => userInputListeners.delete(listener) };
			},
			onResize: (listener) => term.onResize(listener),
		};
		callbacksRef.current.onReady?.(handle);

		return () => {
			disposed = true;
			if (reportedFocused) aoBridge.terminal.setFocused(false);
			disposeFontSizeShortcut();
			host.removeEventListener("focusin", handleFocusIn);
			host.removeEventListener("focusout", handleFocusOut);
			delete (host as DevXtermHost).__aoXtermForTest;
			termRef.current = null;
			if (searchAddonRef.current === searchAddon) searchAddonRef.current = null;
			fitRef.current = null;
			cancelAnimationFrame(raf);
			for (const timer of settleTimers) window.clearTimeout(timer);
			if (fitQuietTimer !== null) clearTimeout(fitQuietTimer);
			if (fitCapTimer !== null) clearTimeout(fitCapTimer);
			fitSettledListeners.clear();
			observer.disconnect();
			stabilizer.dispose();
			scrollPositionChange?.dispose();
			scrollbarResize?.dispose();
			if (scrollbarFrame !== null) cancelAnimationFrame(scrollbarFrame);
			if (scrollbarHideTimer !== null) window.clearTimeout(scrollbarHideTimer);
			scrollbarTrack?.removeEventListener("pointerdown", scrollbarPointerDown);
			scrollbarTrack?.removeEventListener("pointermove", scrollbarPointerMove);
			scrollbarTrack?.removeEventListener("pointerup", scrollbarPointerUp);
			scrollbarTrack?.removeEventListener("pointercancel", scrollbarPointerUp);
			window.removeEventListener("resize", scheduleVisibleFit);
			shell.removeEventListener("copy", copyInput);
			window.removeEventListener("keydown", copyShortcut, true);
			shell.removeEventListener("contextmenu", openContextMenu);
			shell.removeEventListener("paste", pasteInput, true);
			shell.removeEventListener("compositionend", compositionInput, true);
			shell.removeEventListener("dragover", dragOverInput);
			shell.removeEventListener("drop", dropInput);
			contextMenuActionsRef.current = null;
			cancelActivationPreparation?.();
			clearSuppressNativePaste();
			if (colorSchemeReporterRef.current === reportColorScheme) colorSchemeReporterRef.current = null;
			setColorSchemeUpdates.dispose();
			resetColorSchemeUpdates.dispose();
			queryColorSchemeCapability.dispose();
			queryColorScheme.dispose();
			for (const timer of schemeRetryTimers) window.clearTimeout(timer);
			schemeRetryTimers = [];
			oscColorForwarder.dispose();
			cursorPositionForwarder.dispose();
			protocolInput.dispose();
			keyInput.dispose();
			notifyCursorSchemeRef.current = () => {};
			announcedCursorSchemeRef.current = null;
			userInputListeners.clear();
			const disposeTerminal = () => {
				try {
					term.dispose();
				} catch {
					// Some renderer addons can throw during dispose in certain GPU
					// environments; the terminal is being torn down regardless.
				}
			};
			if (import.meta.env.DEV) {
				// xterm's Viewport constructor queues an untracked zero-delay
				// syncScrollArea(). React StrictMode immediately runs this cleanup after
				// its development probe mount; queue disposal behind that callback so it
				// cannot read the already-cleared renderer dimensions.
				window.setTimeout(disposeTerminal, 0);
			} else {
				disposeTerminal();
			}
		};
	}, []);

	useEffect(() => {
		if (!props.focusRequested || props.isVisible === false) return undefined;
		let retryFrame: number | null = null;
		let retriesRemaining = AUTOFOCUS_RETRY_FRAMES;
		let cancelled = false;
		const focusIfAllowed = () => {
			if (cancelled) return;
			const host = hostRef.current;
			if (!host || !canAutoFocusTerminal(host)) {
				if (retriesRemaining === 0) return;
				retriesRemaining -= 1;
				retryFrame = requestAnimationFrame(() => {
					retryFrame = null;
					focusIfAllowed();
				});
				return;
			}
			focusTerminal();
		};

		focusIfAllowed();

		return () => {
			cancelled = true;
			if (retryFrame !== null) cancelAnimationFrame(retryFrame);
		};
	}, [focusTerminal, props.focusRequested, props.isVisible]);

	useLayoutEffect(() => {
		if (props.isVisible === false) {
			setSearchOpen(false);
			searchAddonRef.current?.clearDecorations();
			setContextMenuOpen(false);
			setCopiedToast(false);
			if (copiedToastTimerRef.current !== undefined) {
				window.clearTimeout(copiedToastTimerRef.current);
				copiedToastTimerRef.current = undefined;
			}
			cancelPendingFocusRestore();
		}
	}, [props.isVisible, setContextMenuOpen, cancelPendingFocusRestore]);

	useEffect(() => cancelPendingFocusRestore, [cancelPendingFocusRestore]);

	const wasVisibleRef = useRef(props.isVisible !== false);
	useEffect(() => {
		const visible = props.isVisible !== false;
		const becameVisible = visible && !wasVisibleRef.current;
		wasVisibleRef.current = visible;
		if (!becameVisible) return;
		// Activation preparation already fitted the terminal after the slot became
		// stable. Publish that grid without fitting a second time after reveal.
		const term = termRef.current;
		if (term) callbacksRef.current.onVisibleSize?.(term.cols, term.rows);
	}, [props.isVisible]);

	const fullscreenElement = document.fullscreenElement;
	const contextMenuPortalContainer =
		props.isFullscreen &&
		fullscreenElement instanceof HTMLElement &&
		hostRef.current &&
		fullscreenElement.contains(hostRef.current)
			? fullscreenElement
			: undefined;

	return (
		<>
			<div
				ref={shellRef}
				aria-label={props.ariaLabel}
				className={props.className}
				style={{
					height: "100%",
					overflow: "hidden",
					position: "relative",
					width: "100%",
				}}
			>
				<div
					ref={hostRef}
					className={macPlatform ? "terminal-xterm-host terminal-xterm-host--mac" : "terminal-xterm-host"}
					style={{
						backgroundColor: "var(--color-bg-terminal-opaque)",
						height: "100%",
						overflow: "hidden",
						width: "100%",
					}}
				/>
				{macPlatform ? (
					<div
						aria-hidden="true"
						className="terminal-scrollbar"
						data-active="false"
						data-scrollable="false"
						ref={scrollbarTrackRef}
					>
						<div className="terminal-scrollbar__thumb" ref={scrollbarThumbRef} />
					</div>
				) : null}
				<TerminalSearch
					onClose={() => setSearchOpen(false)}
					onReturnFocus={focusTerminal}
					open={searchOpen && props.isVisible !== false}
					searchAddon={searchAddonRef.current}
				/>
				{copiedToast && props.isVisible !== false ? (
					<div
						aria-live="polite"
						className="pointer-events-none absolute bottom-3 left-1/2 z-10 -translate-x-1/2 rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] px-3 py-1.5 text-xs text-[var(--color-text-import-title)] shadow-[var(--shadow-import-modal)]"
						role="status"
					>
						{t("terminal.copiedToClipboard")}
					</div>
				) : null}
			</div>
			<DropdownMenu modal={false} open={contextMenu.open} onOpenChange={setContextMenuOpen}>
				<DropdownMenuTrigger asChild>
					<button
						type="button"
						aria-hidden="true"
						tabIndex={-1}
						style={{
							border: 0,
							height: 0,
							left: contextMenu.x,
							opacity: 0,
							padding: 0,
							pointerEvents: "none",
							position: "fixed",
							top: contextMenu.y,
							width: 0,
						}}
					/>
				</DropdownMenuTrigger>
				<DropdownMenuContent
					align="start"
					className="min-w-36"
					onCloseAutoFocus={(event) => event.preventDefault()}
					portalContainer={contextMenuPortalContainer}
					side="right"
					sideOffset={2}
				>
					{contextMenu.link ? (
						<>
							<DropdownMenuItem
								onSelect={() => {
									const { link } = contextMenu;
									setContextMenuOpen(false);
									if (link) void aoBridge.app.openExternal(link);
								}}
							>
								{t("terminal.openSystemBrowser")}
							</DropdownMenuItem>
							<DropdownMenuSeparator />
						</>
					) : null}
					<DropdownMenuItem disabled={!contextMenu.canCopy} onSelect={() => runContextMenuAction("copy")}>
						{t("titlebar.copy")}
					</DropdownMenuItem>
					<DropdownMenuItem onSelect={() => runContextMenuAction("paste")}>{t("titlebar.paste")}</DropdownMenuItem>
					<DropdownMenuItem onSelect={() => runContextMenuAction("selectAll")}>{t("titlebar.selectAll")}</DropdownMenuItem>
					<DropdownMenuSeparator />
					<DropdownMenuItem
						onSelect={() => {
							setContextMenuOpen(false);
							setSearchOpen(true);
						}}
					>
						{t("terminal.search")}
					</DropdownMenuItem>
					{props.onToggleFullscreen ? (
						<DropdownMenuItem
							onSelect={() => {
								setContextMenuOpen(false);
								void toggleFullscreenAndRestoreFocus();
							}}
						>
							{props.isFullscreen ? t("terminal.exitFullscreen") : t("terminal.fullscreen")}
						</DropdownMenuItem>
					) : null}
				</DropdownMenuContent>
			</DropdownMenu>
		</>
	);
}
