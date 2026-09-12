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

	it("round-trips written locale", async () => {
		await writeUiSettings(dir, { locale: "zh-CN" });
		expect(await readUiSettings(dir)).toEqual({ ...DEFAULT_UI_SETTINGS, locale: "zh-CN" });
		await writeUiSettings(dir, { locale: "en" });
		expect(await readUiSettings(dir)).toEqual(DEFAULT_UI_SETTINGS);
	});

	it("merges a partial write with previously persisted settings instead of replacing them", async () => {
		await writeUiSettings(dir, { locale: "zh-CN" });
		await writeUiSettings(dir, { soundNotificationsEnabled: false });
		expect(await readUiSettings(dir)).toEqual({ ...DEFAULT_UI_SETTINGS, locale: "zh-CN", soundNotificationsEnabled: false });

		await writeUiSettings(dir, { locale: "ja" });
		expect(await readUiSettings(dir)).toEqual({ ...DEFAULT_UI_SETTINGS, locale: "ja", soundNotificationsEnabled: false });
	});

	it("falls back to defaults on garbage", async () => {
		await writeFile(path.join(dir, UI_SETTINGS_FILE_NAME), "{not json", "utf8");
		expect(await readUiSettings(dir)).toEqual(DEFAULT_UI_SETTINGS);
	});

	it("coerces unknown locale to en and accepts supported locales", () => {
		expect(coerceUiSettings({ locale: "xx" })).toEqual(DEFAULT_UI_SETTINGS);
		expect(coerceUiSettings({ locale: "zh" })).toEqual(DEFAULT_UI_SETTINGS);
		expect(coerceUiSettings({})).toEqual(DEFAULT_UI_SETTINGS);
		expect(coerceUiSettings(null)).toEqual(DEFAULT_UI_SETTINGS);
		expect(coerceUiSettings({ locale: "zh-CN" })).toEqual({ ...DEFAULT_UI_SETTINGS, locale: "zh-CN" });
		expect(coerceUiSettings({ locale: "fr" })).toEqual({ ...DEFAULT_UI_SETTINGS, locale: "fr" });
		expect(coerceUiSettings({ locale: "pt-BR" })).toEqual({ ...DEFAULT_UI_SETTINGS, locale: "pt-BR" });
	});

	it("merges the terminal shell without resetting other UI settings", async () => {
		await writeUiSettings(dir, { locale: "ja", soundNotificationsEnabled: false });
		await writeUiSettings(dir, { terminalShell: { kind: "git-bash" } });
		expect(await readUiSettings(dir)).toEqual({
			locale: "ja",
			soundNotificationsEnabled: false,
			terminalShell: { kind: "git-bash" },
		});
	});

	it("atomic write leaves no temp file behind", async () => {
		await writeUiSettings(dir, { locale: "zh-CN" });
		const entries = await readdir(dir);
		expect(entries).toEqual([UI_SETTINGS_FILE_NAME]);
	});
});
