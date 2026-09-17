// Which theme the user asked for, and what that resolves to right now. Pure —
// no React Native or storage imports — so the rules are unit-testable; the
// AsyncStorage half lives in themeStore.ts, the same split as
// onboarding.ts / onboardingStore.ts.
import type { ColorScheme } from "./theme";

/** What the user picked. "system" defers to the OS and keeps deferring. */
export type ThemePreference = "light" | "dark" | "system";

/**
 * The same key the desktop app writes (`frontend/src/renderer/lib/theme.ts`).
 * The two never share storage, but keeping the name identical means one grep
 * finds both.
 */
export const THEME_KEY = "ao.theme";

/** Default for anyone who has never opened the setting — matches desktop. */
export const DEFAULT_PREFERENCE: ThemePreference = "system";

export function isThemePreference(value: unknown): value is ThemePreference {
	return value === "light" || value === "dark" || value === "system";
}

/**
 * What to actually render.
 *
 * `system` is resolved on every call rather than being frozen at pick time, so
 * the app follows the OS live instead of only at next launch. React Native
 * reports `"unspecified"` (and, on older versions, `null`) for the system scheme
 * before it has resolved, or on a platform that has no notion of one; dark is
 * the fallback because that is what the app looked like before this setting
 * existed.
 */
export function resolveScheme(
	preference: ThemePreference,
	systemScheme: ColorScheme | "unspecified" | null | undefined,
): ColorScheme {
	if (preference === "light" || preference === "dark") return preference;
	return systemScheme === "light" ? "light" : "dark";
}

/** Label for the settings row's value slot. */
export function preferenceLabel(preference: ThemePreference): string {
	switch (preference) {
		case "light":
			return "Light";
		case "dark":
			return "Dark";
		default:
			return "System";
	}
}
