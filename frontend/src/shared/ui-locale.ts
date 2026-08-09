/** UI locales supported across the Electron main, preload, and renderer boundaries. */
export const APP_LOCALES = ["en", "zh-CN", "ja", "ko", "es", "fr", "de", "pt-BR"] as const;

export type AppLocale = (typeof APP_LOCALES)[number];

export const DEFAULT_LOCALE: AppLocale = "en";

/** Theme preference, mirrored between the renderer's localStorage and the main-process settings file so the tray can read and change it. */
export type ThemePreference = "light" | "dark" | "system";

export const DEFAULT_THEME_PREFERENCE: ThemePreference = "system";

/** Pushed main -> renderer when the theme changes from outside the renderer (e.g. the tray). */
export const THEME_CHANGED_CHANNEL = "theme:changed";

export interface UiSettings {
	locale: AppLocale;
	themePreference: ThemePreference;
}

export const DEFAULT_UI_SETTINGS: UiSettings = { locale: DEFAULT_LOCALE, themePreference: DEFAULT_THEME_PREFERENCE };

/** Normalize an unknown value to a supported UI locale. */
export function coerceLocale(raw: unknown): AppLocale {
	if (typeof raw === "string" && (APP_LOCALES as readonly string[]).includes(raw)) {
		return raw as AppLocale;
	}
	return DEFAULT_LOCALE;
}

/** Normalize an unknown value to a supported theme preference. */
export function coerceThemePreference(raw: unknown): ThemePreference {
	if (raw === "light" || raw === "dark" || raw === "system") return raw;
	return DEFAULT_THEME_PREFERENCE;
}

/** Normalize unknown persisted or IPC data to the supported UI-settings schema. */
export function coerceUiSettings(raw: unknown): UiSettings {
	const record = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
	return {
		locale: coerceLocale(record.locale),
		themePreference: coerceThemePreference(record.themePreference),
	};
}
