import type { ITheme } from "@xterm/xterm";

/** Read a CSS custom property from :root (tokens.css). */
function cssVar(name: string): string {
	if (typeof document === "undefined") return "";
	return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

/** xterm palettes harmonized to tokens.css (--color-term-* / semantic colors). */
export function buildTerminalThemes(): { dark: ITheme; light: ITheme } {
	// Opaque plate — xterm cells must not be translucent or the p-2 gutter and
	// the glyph grid pick up different composites against app chrome.
	const namedThemeActive = typeof document !== "undefined" && Boolean(document.documentElement.dataset.styleTheme);
	const terminalBg = namedThemeActive
		? cssVar("--background")
		: cssVar("--color-bg-terminal-opaque") || cssVar("--color-bg-terminal");
	const terminalForeground = namedThemeActive ? cssVar("--foreground") : cssVar("--color-text-terminal");
	const terminalCursor = namedThemeActive ? cssVar("--primary") : cssVar("--color-working");
	// In dark mode, collapse ANSI black into the plate so agent TUIs (Cursor's
	// prompt bar) that fill rows with "black" don't paint a visible stripe.
	// Same approach as packages/mobile/lib/theme.ts.
	const ansiBlack = terminalBg;
	const dark: ITheme = {
		background: terminalBg,
		foreground: terminalForeground,
		cursor: terminalCursor,
		cursorAccent: terminalBg,
		selectionBackground: cssVar("--color-term-selection-dark"),
		selectionInactiveBackground: cssVar("--color-term-selection-inactive"),
		black: ansiBlack,
		red: cssVar("--color-term-red"),
		green: cssVar("--color-term-green"),
		yellow: cssVar("--color-term-yellow"),
		blue: cssVar("--color-term-blue"),
		magenta: cssVar("--color-term-magenta"),
		cyan: cssVar("--color-term-cyan"),
		white: cssVar("--color-term-white"),
		brightBlack: cssVar("--color-term-bright-black"),
		brightRed: cssVar("--color-term-bright-red"),
		brightGreen: cssVar("--color-term-bright-green"),
		brightYellow: cssVar("--color-term-bright-yellow"),
		brightBlue: cssVar("--color-term-bright-blue"),
		brightMagenta: cssVar("--color-term-bright-magenta"),
		brightCyan: cssVar("--color-term-bright-cyan"),
		brightWhite: cssVar("--color-term-bright-white"),
	};

	const light: ITheme = {
		background: terminalBg,
		foreground: terminalForeground,
		// xterm block cursor fills with `cursor` and paints cell text in
		// `cursorAccent`. --color-working is fine on dark plates but reads as a
		// low-contrast wash on the light terminal bg (#f5f5f4), especially while
		// blinking. Use the terminal foreground so the block stays visible.
		cursor: terminalForeground,
		cursorAccent: terminalBg,
		selectionBackground: cssVar("--color-term-selection-light"),
		selectionInactiveBackground: cssVar("--color-term-selection-inactive-light"),
		// In dark mode we collapse ANSI black into the plate so agent TUIs that
		// fill rows with "black" don't paint a visible stripe. In light mode the
		// plate is near-white, so mapping black → bg makes any text Cline/TUI
		// renders in ANSI black (or bold black) invisible. Use the terminal
		// foreground so dark text stays readable against the light canvas.
		black: terminalForeground,
		red: cssVar("--color-term-red"),
		green: cssVar("--color-term-green"),
		yellow: cssVar("--color-term-yellow"),
		blue: cssVar("--color-term-blue"),
		magenta: cssVar("--color-term-magenta"),
		cyan: cssVar("--color-term-cyan"),
		white: cssVar("--color-term-white"),
		brightBlack: cssVar("--color-term-bright-black"),
		brightRed: cssVar("--color-term-bright-red"),
		brightGreen: cssVar("--color-term-bright-green"),
		brightYellow: cssVar("--color-term-bright-yellow"),
		brightBlue: cssVar("--color-term-bright-blue"),
		brightMagenta: cssVar("--color-term-bright-magenta"),
		brightCyan: cssVar("--color-term-bright-cyan"),
		brightWhite: cssVar("--color-term-bright-white"),
	};

	return { dark, light };
}
