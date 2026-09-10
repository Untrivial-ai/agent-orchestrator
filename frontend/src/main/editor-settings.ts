import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";
import { isEditorId, type EditorId } from "../shared/editor-handoff";

export const EDITOR_SETTINGS_FILE_NAME = "editor-settings.json";
export const DEFAULT_EDITOR_ID: EditorId = "cursor";

export type EditorSettings = {
	preferredEditorId: EditorId;
};

let settingsOperationQueue: Promise<void> = Promise.resolve();

export function coerceEditorSettings(raw: unknown): EditorSettings {
	if (!raw || typeof raw !== "object") return { preferredEditorId: DEFAULT_EDITOR_ID };
	const preferredEditorId = (raw as Record<string, unknown>).preferredEditorId;
	return { preferredEditorId: isEditorId(preferredEditorId) ? preferredEditorId : DEFAULT_EDITOR_ID };
}

async function readUnlocked(stateDir: string): Promise<EditorSettings> {
	try {
		const raw = await readFile(path.join(stateDir, EDITOR_SETTINGS_FILE_NAME), "utf8");
		return coerceEditorSettings(JSON.parse(raw));
	} catch {
		return { preferredEditorId: DEFAULT_EDITOR_ID };
	}
}

async function writeUnlocked(stateDir: string, preferredEditorId: EditorId): Promise<EditorSettings> {
	const next = coerceEditorSettings({ preferredEditorId });
	await mkdir(stateDir, { recursive: true, mode: 0o750 });
	const file = path.join(stateDir, EDITOR_SETTINGS_FILE_NAME);
	const temporary = path.join(stateDir, `.editor-settings-${process.pid}-${Date.now()}.json`);
	await writeFile(temporary, `${JSON.stringify(next, null, 2)}\n`, { mode: 0o600 });
	await rename(temporary, file);
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

export function readEditorSettings(stateDir: string): Promise<EditorSettings> {
	return readUnlocked(stateDir);
}

export function writeEditorPreference(stateDir: string, preferredEditorId: EditorId): Promise<EditorSettings> {
	return runSettingsOperation(() => writeUnlocked(stateDir, preferredEditorId));
}
