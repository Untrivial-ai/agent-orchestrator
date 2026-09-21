import { describe, expect, it } from "vitest";
import {
	DEFAULT_PREFERENCE,
	isThemePreference,
	nativeColorSchemeOverride,
	preferenceLabel,
	resolveScheme,
	THEME_KEY,
} from "./themePreference";

describe("resolveScheme", () => {
	it("honours an explicit choice regardless of the OS", () => {
		expect(resolveScheme("light", "dark")).toBe("light");
		expect(resolveScheme("dark", "light")).toBe("dark");
	});

	it("follows the OS when the preference is system", () => {
		expect(resolveScheme("system", "light")).toBe("light");
		expect(resolveScheme("system", "dark")).toBe("dark");
	});

	// React Native reports null before the scheme resolves, and on platforms with
	// no notion of one. Dark is the fallback because that is what the app looked
	// like before this setting existed.
	it("falls back to dark when the OS scheme is unknown", () => {
		expect(resolveScheme("system", null)).toBe("dark");
		expect(resolveScheme("system", undefined)).toBe("dark");
	});

	// Resolution happens on every call rather than being frozen when the user
	// picks, which is what lets "system" follow a live OS change.
	it("re-resolves system against whatever the OS currently reports", () => {
		expect(resolveScheme("system", "dark")).toBe("dark");
		expect(resolveScheme("system", "light")).toBe("light");
	});
});

describe("nativeColorSchemeOverride", () => {
	it("forces native presentations to explicit app themes and defers system mode to iOS", () => {
		expect(nativeColorSchemeOverride("light")).toBe("light");
		expect(nativeColorSchemeOverride("dark")).toBe("dark");
		expect(nativeColorSchemeOverride("system")).toBe("unspecified");
	});
});

describe("isThemePreference", () => {
	it("accepts only the three valid values", () => {
		expect(isThemePreference("light")).toBe(true);
		expect(isThemePreference("dark")).toBe(true);
		expect(isThemePreference("system")).toBe(true);
	});

	// Guards the storage read: anything else on disk must fall back rather than
	// being handed on as a preference.
	it("rejects junk from storage", () => {
		for (const junk of [null, undefined, "", "Dark", "auto", 1, {}]) {
			expect(isThemePreference(junk)).toBe(false);
		}
	});
});

describe("defaults and labels", () => {
	it("defaults to system, matching the desktop app", () => {
		expect(DEFAULT_PREFERENCE).toBe("system");
	});

	it("uses the same storage key name as desktop", () => {
		expect(THEME_KEY).toBe("ao.theme");
	});

	it("labels every preference", () => {
		expect(preferenceLabel("light")).toBe("Light");
		expect(preferenceLabel("dark")).toBe("Dark");
		expect(preferenceLabel("system")).toBe("System");
	});
});
