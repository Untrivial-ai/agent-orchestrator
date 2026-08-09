// @vitest-environment node
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mkdtemp, rm, writeFile, readdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
	coerceUiSettings,
	readUiSettings,
	writeUiSettings,
	UI_SETTINGS_FILE_NAME,
	DEFAULT_UI_SETTINGS,
} from "./ui-settings";

describe("ui-settings", () => {
	let dir: string;
	beforeEach(async () => {
		dir = await mkdtemp(path.join(os.tmpdir(), "ao-ui-settings-"));
	});
	afterEach(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("returns safe defaults when no file exists", async () => {
		expect(await readUiSettings(dir)).toEqual(DEFAULT_UI_SETTINGS);
	});

	it("round-trips written locale and theme preference", async () => {
		await writeUiSettings(dir, { locale: "zh-CN", themePreference: "dark" });
		expect(await readUiSettings(dir)).toEqual({ locale: "zh-CN", themePreference: "dark" });
		await writeUiSettings(dir, { locale: "en", themePreference: "light" });
		expect(await readUiSettings(dir)).toEqual({ locale: "en", themePreference: "light" });
	});

	it("falls back to defaults on garbage", async () => {
		await writeFile(path.join(dir, UI_SETTINGS_FILE_NAME), "{not json", "utf8");
		expect(await readUiSettings(dir)).toEqual(DEFAULT_UI_SETTINGS);
	});

	it("coerces unknown locale to en and accepts supported locales", () => {
		expect(coerceUiSettings({ locale: "xx" })).toEqual({ locale: "en", themePreference: "system" });
		expect(coerceUiSettings({ locale: "zh" })).toEqual({ locale: "en", themePreference: "system" });
		expect(coerceUiSettings({})).toEqual({ locale: "en", themePreference: "system" });
		expect(coerceUiSettings(null)).toEqual({ locale: "en", themePreference: "system" });
		expect(coerceUiSettings({ locale: "zh-CN" })).toEqual({ locale: "zh-CN", themePreference: "system" });
		expect(coerceUiSettings({ locale: "fr" })).toEqual({ locale: "fr", themePreference: "system" });
		expect(coerceUiSettings({ locale: "pt-BR" })).toEqual({ locale: "pt-BR", themePreference: "system" });
	});

	it("coerces unknown theme preference to system and accepts supported preferences", () => {
		expect(coerceUiSettings({ themePreference: "blue" })).toEqual({ locale: "en", themePreference: "system" });
		expect(coerceUiSettings({ themePreference: "light" })).toEqual({ locale: "en", themePreference: "light" });
		expect(coerceUiSettings({ themePreference: "dark" })).toEqual({ locale: "en", themePreference: "dark" });
	});

	it("atomic write leaves no temp file behind", async () => {
		await writeUiSettings(dir, { locale: "zh-CN", themePreference: "system" });
		const entries = await readdir(dir);
		expect(entries).toEqual([UI_SETTINGS_FILE_NAME]);
	});
});
