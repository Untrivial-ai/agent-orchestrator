// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
	DEFAULT_EDITOR_ID,
	EDITOR_SETTINGS_FILE_NAME,
	readEditorSettings,
	writeEditorPreference,
} from "./editor-settings";

describe("editor settings", () => {
	let stateDir: string;

	beforeEach(async () => {
		stateDir = await mkdtemp(path.join(os.tmpdir(), "ao-editor-settings-"));
	});

	afterEach(async () => {
		await rm(stateDir, { recursive: true, force: true });
	});

	it("defaults to Cursor when the preference is absent or invalid", async () => {
		expect(await readEditorSettings(stateDir)).toEqual({ preferredEditorId: DEFAULT_EDITOR_ID });
		await writeFile(path.join(stateDir, EDITOR_SETTINGS_FILE_NAME), `{"preferredEditorId":"unknown"}`);
		expect(await readEditorSettings(stateDir)).toEqual({ preferredEditorId: "cursor" });
	});

	it("atomically persists a supported preferred editor under AO state", async () => {
		await writeEditorPreference(stateDir, "vscode");
		expect(await readEditorSettings(stateDir)).toEqual({ preferredEditorId: "vscode" });
		const raw = await readFile(path.join(stateDir, EDITOR_SETTINGS_FILE_NAME), "utf8");
		expect(JSON.parse(raw)).toEqual({ preferredEditorId: "vscode" });
	});
});
