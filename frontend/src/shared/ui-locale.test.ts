import { describe, expect, it } from "vitest";
import {
	APP_LOCALES,
	DEFAULT_LOCALE,
	DEFAULT_UI_SETTINGS,
	coerceTerminalShell,
	coerceLocale,
	coerceUiSettings,
} from "./ui-locale";

describe("shared UI locale schema", () => {
	it("accepts only supported locale identifiers", () => {
		expect(APP_LOCALES).toEqual(["en", "zh-CN", "ja", "ko", "es", "fr", "de", "pt-BR"]);
		expect(coerceLocale("en")).toBe("en");
		expect(coerceLocale("zh-CN")).toBe("zh-CN");
		expect(coerceLocale("ja")).toBe("ja");
		expect(coerceLocale("ko")).toBe("ko");
		expect(coerceLocale("es")).toBe("es");
		expect(coerceLocale("fr")).toBe("fr");
		expect(coerceLocale("de")).toBe("de");
		expect(coerceLocale("pt-BR")).toBe("pt-BR");
		expect(coerceLocale("zh")).toBe(DEFAULT_LOCALE);
		expect(coerceLocale("pt")).toBe(DEFAULT_LOCALE);
		expect(coerceLocale({ locale: "zh-CN" })).toBe(DEFAULT_LOCALE);
	});

	it("normalizes persisted settings through the shared locale validator", () => {
		expect(coerceUiSettings({ locale: "zh-CN" })).toEqual({ ...DEFAULT_UI_SETTINGS, locale: "zh-CN" });
		expect(coerceUiSettings({ locale: "ja" })).toEqual({ ...DEFAULT_UI_SETTINGS, locale: "ja" });
		expect(coerceUiSettings({ locale: "pt-BR" })).toEqual({ ...DEFAULT_UI_SETTINGS, locale: "pt-BR" });
		expect(coerceUiSettings({ locale: "pt" })).toEqual(DEFAULT_UI_SETTINGS);
		expect(coerceUiSettings(null)).toEqual(DEFAULT_UI_SETTINGS);
	});

	it("defaults soundNotificationsEnabled to true and accepts a persisted boolean", () => {
		expect(DEFAULT_UI_SETTINGS).toEqual({
			locale: "en",
			soundNotificationsEnabled: true,
			terminalShell: { kind: "auto" },
		});
		expect(coerceUiSettings({ locale: "en", soundNotificationsEnabled: false })).toEqual({
			...DEFAULT_UI_SETTINGS,
			soundNotificationsEnabled: false,
		});
		expect(coerceUiSettings({ locale: "en", soundNotificationsEnabled: true })).toEqual({
			...DEFAULT_UI_SETTINGS,
			soundNotificationsEnabled: true,
		});
	});

	it("coerces a non-boolean or missing soundNotificationsEnabled to the default (true)", () => {
		expect(coerceUiSettings({ locale: "en" })).toEqual(DEFAULT_UI_SETTINGS);
		expect(coerceUiSettings({ locale: "en", soundNotificationsEnabled: "false" })).toEqual({
			...DEFAULT_UI_SETTINGS,
		});
		expect(coerceUiSettings({ locale: "en", soundNotificationsEnabled: null })).toEqual({
			...DEFAULT_UI_SETTINGS,
		});
	});

	it("accepts supported terminal shells and normalizes custom paths", () => {
		expect(coerceTerminalShell({ kind: "git-bash" })).toEqual({ kind: "git-bash" });
		expect(coerceTerminalShell({ kind: "custom", path: "  C:\\Tools\\bash.exe  " })).toEqual({
			kind: "custom",
			path: "C:\\Tools\\bash.exe",
		});
		expect(coerceTerminalShell({ kind: "custom", path: "   " })).toEqual({ kind: "custom" });
		expect(coerceTerminalShell({ kind: "fish" })).toEqual({ kind: "auto" });
	});
});
