import { contrast, luminance, readableText, type OmarchyPalette } from "../../shared/omarchy-theme";
export type Theme = "light" | "dark";
export type ThemePreference = Theme | "system";

export type ThemeStyle =
	| "automatic"
	| "orchestrate"
	| "github"
	| "catppuccin"
	| "dracula"
	| "tokyo-night"
	| "rose-pine"
	| "nord"
	| "gruvbox"
	| "solarized";

export const themeStorageKey = "ao.theme";
export const themeStyleStorageKey = "ao.theme-style";

function getLocalStorage() {
	if (typeof window === "undefined" || !window.localStorage) return null;
	return window.localStorage;
}

export function systemTheme(): Theme {
	if (typeof window === "undefined") return "dark";
	return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

export function readStoredThemePreference(): ThemePreference {
	try {
		const stored = getLocalStorage()?.getItem(themeStorageKey);
		if (stored === "light" || stored === "dark" || stored === "system") return stored;
	} catch {
		// ignore
	}
	return "system";
}

/** Resolve the active light/dark appearance from a stored preference. */
export function resolveTheme(preference: ThemePreference = readStoredThemePreference()): Theme {
	if (preference === "system") {
		if (readStoredThemeStyle() === "automatic" && omarchyPalette) return luminance(omarchyPalette.background) > 0.179 ? "light" : "dark";
		return systemTheme();
	}
	return preference;
}

export function readStoredThemeStyle(): ThemeStyle {
	try {
		const stored = getLocalStorage()?.getItem(themeStyleStorageKey);
		if (
			stored === "automatic" ||
			stored === "orchestrate" ||
			stored === "github" ||
			stored === "catppuccin" ||
			stored === "dracula" ||
			stored === "tokyo-night" ||
			stored === "rose-pine" ||
			stored === "nord" ||
			stored === "gruvbox" ||
			stored === "solarized"
		) {
			return stored;
		}
	} catch {
		// ignore
	}
	return "automatic";
}

export function applyDocumentTheme(theme: Theme): void {
	if (typeof document === "undefined") return;
	document.documentElement.dataset.theme = theme;
	document.documentElement.style.colorScheme = theme;
}

export function applyDocumentThemeStyle(style: ThemeStyle): void {
	if (typeof document === "undefined") return;
	applyOmarchyTokens(style === "automatic" && readStoredThemePreference() === "system" ? omarchyPalette : null);
	if (style === "orchestrate" || (style === "automatic" && !isOmarchyActive())) {
		delete document.documentElement.dataset.styleTheme;
	} else {
		document.documentElement.dataset.styleTheme = style;
	}
}

/**
 * Apply a theme DOM update under a View Transition so per-element
 * `transition-colors` / background tweens are hidden behind a snapshot.
 * Default VT crossfade is disabled in CSS — this is an instant cut.
 * Falls back to a plain update when the API is unavailable.
 *
 * Also sets `data-theme-transition` so global CSS can zero out
 * transition-duration while tokens swap; otherwise composer, search, and
 * switch chrome tween from the old palette and look stuck white/black.
 */
export function runThemeTransition(update: () => void): void {
	if (typeof document === "undefined") return;

	const root = document.documentElement;
	const finish = () => {
		requestAnimationFrame(() => {
			delete root.dataset.themeTransition;
		});
	};
	const run = () => {
		root.dataset.themeTransition = "active";
		update();
		void root.offsetHeight;
	};

	if (typeof document.startViewTransition !== "function") {
		run();
		finish();
		return;
	}

	const transition = document.startViewTransition(() => {
		run();
	});
	void transition.finished.finally(finish);
}

let omarchyPalette: OmarchyPalette | null = null;
let appliedTokens: string[] = [];
export function setOmarchyPalette(palette: OmarchyPalette | null): void { omarchyPalette = palette; }
export function isOmarchyActive(): boolean {
	return readStoredThemeStyle() === "automatic" && readStoredThemePreference() === "system" && omarchyPalette !== null;
}

function applyOmarchyTokens(p: OmarchyPalette | null): void {
	const root = document.documentElement;
	for (const key of appliedTokens) root.style.removeProperty(key);
	appliedTokens = [];
	if (!p) return;
	const fg = readableText(p.background, p.foreground);
	const onAccent = readableText(p.accent, p.background);
	const surface = p.surface && contrast(p.surface, fg) >= 4.5 ? p.surface : p.background;
	const surfaceText = readableText(surface, p.foreground);
	const sidebar = p.sidebar && contrast(p.sidebar, fg) >= 4.5 ? p.sidebar : p.background;
	const tokens: Record<string, string> = {
		background: p.background, foreground: fg, card: surface, "card-foreground": surfaceText,
		popover: surface, "popover-foreground": surfaceText, primary: p.accent, "primary-foreground": onAccent,
		secondary: p.selection, "secondary-foreground": readableText(p.selection, fg),
		muted: p.background, "muted-foreground": fg, accent: p.selection, "accent-foreground": readableText(p.selection, fg),
		border: p.accent, input: p.background, ring: p.accent,
		sidebar, "sidebar-foreground": readableText(sidebar, p.foreground), "sidebar-primary": p.accent,
		"sidebar-primary-foreground": onAccent, "sidebar-accent": p.selection,
		"sidebar-accent-foreground": readableText(p.selection, fg), "sidebar-ring": p.accent,
		"chart-1": p.accent, "chart-3": fg,
		"color-text-terminal": p.foreground, "color-term-cursor": p.cursor,
		"color-term-selection-dark": p.selection, "color-term-selection-light": p.selection,
		"color-term-selection-inactive": p.selection, "color-term-selection-inactive-light": p.selection,
		"color-term-selection-foreground": readableText(p.selection, p.foreground),
		"color-brand-logo": p.accent, "color-brand-logo-bright": p.accent,
		"color-brand-logo-foreground": onAccent,
	};
	const names = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"];
	p.ansi.forEach((color, i) => { tokens[`color-term-${i >= 8 ? "bright-" : ""}${names[i % 8]}`] = color; });
	for (const [key, value] of Object.entries(tokens)) {
		root.style.setProperty(`--${key}`, value);
		appliedTokens.push(`--${key}`);
	}
}
