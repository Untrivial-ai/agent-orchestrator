import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { Appearance, useColorScheme } from "react-native";
import { themeFor, type ColorScheme, type Theme } from "./theme";
import { DEFAULT_PREFERENCE, nativeColorSchemeOverride, resolveScheme, type ThemePreference } from "./themePreference";
import { loadThemePreference, saveThemePreference } from "./themeStore";

type ThemeState = {
	theme: Theme;
	scheme: ColorScheme;
	preference: ThemePreference;
	setPreference: (p: ThemePreference) => void;
};

const ThemeContext = createContext<ThemeState | null>(null);

export function useTheme(): Theme {
	return useThemeState().theme;
}

export function useThemeState(): ThemeState {
	const ctx = useContext(ThemeContext);
	if (!ctx) throw new Error("useTheme must be used within <ThemeProvider>");
	return ctx;
}

/**
 * Styles that follow the theme.
 *
 * `StyleSheet.create` runs at module load, so a stylesheet that reads colours
 * directly is frozen to whichever palette was imported first — the reason light
 * mode is a migration rather than a token swap. Wrapping the sheet in a factory
 * defers it to render, and the memo keeps it to one rebuild per theme change
 * rather than one per render.
 *
 * The factory must be a module-level const, or its identity changes every
 * render and the memo never hits.
 */
export function useThemedStyles<T>(factory: (t: Theme) => T): T {
	const theme = useTheme();
	return useMemo(() => factory(theme), [factory, theme]);
}

export function ThemeProvider({ children }: { children: ReactNode }) {
	const systemScheme = useColorScheme();
	const [preference, setPreferenceState] = useState<ThemePreference>(DEFAULT_PREFERENCE);

	// The stored preference arrives a tick after mount. Until then we render the
	// default ("system"), which already tracks the OS — so the common case shows
	// the right theme immediately and only an explicit override can flash.
	useEffect(() => {
		let active = true;
		loadThemePreference().then((p) => {
			if (active) setPreferenceState(p);
		});
		return () => {
			active = false;
		};
	}, []);

	const setPreference = useCallback((next: ThemePreference) => {
		// Optimistic: the switch should move under the finger, not after a write.
		setPreferenceState(next);
		void saveThemePreference(next);
	}, []);

	useEffect(() => {
		Appearance.setColorScheme(nativeColorSchemeOverride(preference));
	}, [preference]);

	// Recomputed whenever the OS scheme changes, so a "system" preference follows
	// live instead of only at next launch.
	const scheme = resolveScheme(preference, systemScheme);

	// The window sits below the React tree, so a page transition that opens a gap
	// shows its colour — the platform default, white — and nothing inside the tree
	// can cover it. Keeping the window on the palette is what stops the white edge
	// around a screen as it moves.
	const backgroundColor = themeFor(scheme).bgBase;
	useEffect(() => {
		// Required lazily, inside a guard: this is a native module, and a build made
		// before it was added has no ExpoSystemUI to bind to. A static import would
		// fail at startup — taking the whole app down — where the only thing actually
		// at stake is whether the window is tinted.
		try {
			const systemUI = require("expo-system-ui") as typeof import("expo-system-ui");
			void systemUI.setBackgroundColorAsync(backgroundColor).catch(() => {});
		} catch {
			// Older native build; the in-tree backdrop still covers the transition.
		}
	}, [backgroundColor]);

	const value = useMemo<ThemeState>(
		() => ({ theme: themeFor(scheme), scheme, preference, setPreference }),
		[scheme, preference, setPreference],
	);

	return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}
