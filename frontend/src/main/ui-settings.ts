import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";
import { DEFAULT_UI_SETTINGS, coerceUiSettings, type UiSettings } from "../shared/ui-locale";

export { DEFAULT_UI_SETTINGS, coerceUiSettings } from "../shared/ui-locale";
export type { AppLocale, UiSettings } from "../shared/ui-locale";

/** File holding lightweight UI prefs (locale) under the ~/.ao state dir. */
export const UI_SETTINGS_FILE_NAME = "ui-settings.json";

let settingsOperationQueue: Promise<void> = Promise.resolve();

async function readUiSettingsUnlocked(stateDir: string): Promise<UiSettings> {
	let raw: string;
	try {
		raw = await readFile(path.join(stateDir, UI_SETTINGS_FILE_NAME), "utf8");
	} catch {
		return { ...DEFAULT_UI_SETTINGS };
	}
	try {
		return coerceUiSettings(JSON.parse(raw));
	} catch {
		return { ...DEFAULT_UI_SETTINGS };
	}
}

async function writeUiSettingsUnlocked(stateDir: string, patch: Partial<UiSettings>): Promise<UiSettings> {
	// A caller only sends the field it changed (e.g. just `locale` or just
	// `soundNotificationsEnabled`); merge onto the persisted settings so one
	// setting's write never resets the other back to its default.
	const current = await readUiSettingsUnlocked(stateDir);
	const next = coerceUiSettings({ ...current, ...patch });
	await mkdir(stateDir, { recursive: true, mode: 0o750 });
	const file = path.join(stateDir, UI_SETTINGS_FILE_NAME);
	const data = `${JSON.stringify(next, null, 2)}\n`;
	const tmp = path.join(stateDir, `.ui-settings-${process.pid}-${Date.now()}.json`);
	await writeFile(tmp, data, { mode: 0o600 });
	await rename(tmp, file);
	return next;
}

function runSettingsOperation<T>(operation: () => Promise<T>): Promise<T> {
	const queued = settingsOperationQueue.then(operation, operation);
	settingsOperationQueue = queued.then(
		() => undefined,
		() => undefined,
	);
	return queued;
}

/** Read UI settings, tolerating a missing or corrupt file (returns defaults). */
export async function readUiSettings(stateDir: string): Promise<UiSettings> {
	return readUiSettingsUnlocked(stateDir);
}

/** Atomically and serially merge-write UI settings (temp file + rename). */
export async function writeUiSettings(stateDir: string, patch: Partial<UiSettings>): Promise<UiSettings> {
	return runSettingsOperation(() => writeUiSettingsUnlocked(stateDir, patch));
}
