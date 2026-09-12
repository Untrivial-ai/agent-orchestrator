import { copyFile, mkdir, readFile, readdir, rm, stat } from "node:fs/promises";
import path from "node:path";
import type { NotificationSoundImportErrorCode, NotificationSoundPayload } from "../shared/notification-sound";

export type { NotificationSoundImportErrorCode, NotificationSoundPayload } from "../shared/notification-sound";

/**
 * Custom notification sound storage for the main process.
 *
 * A user-chosen audio file is copied into `<stateDir>/notification-sound/` so
 * the notification keeps working after the original file moves or is deleted.
 * Nothing leaves the machine: the copy lives under ~/.ao next to the other UI
 * settings, and only its path is persisted in `ui-settings.json`.
 *
 * Electron's main process cannot play audio itself, so playback happens in the
 * renderer: main reads the bytes (see {@link readNotificationSound}) and ships
 * them over IPC as a {@link NotificationSoundPayload}.
 */

/** Directory under the ~/.ao state dir holding the imported sound file. */
export const NOTIFICATION_SOUND_DIR_NAME = "notification-sound";

/** Audio containers Chromium decodes across macOS, Windows, and Linux. */
export const NOTIFICATION_SOUND_EXTENSIONS = ["mp3", "wav", "ogg", "oga", "m4a", "aac", "flac", "webm"] as const;

/** Notification sounds are short; anything bigger is almost certainly a mistake. */
export const MAX_NOTIFICATION_SOUND_BYTES = 10 * 1024 * 1024;

const MIME_BY_EXTENSION: Record<(typeof NOTIFICATION_SOUND_EXTENSIONS)[number], string> = {
	mp3: "audio/mpeg",
	wav: "audio/wav",
	ogg: "audio/ogg",
	oga: "audio/ogg",
	m4a: "audio/mp4",
	aac: "audio/aac",
	flac: "audio/flac",
	webm: "audio/webm",
};

export class NotificationSoundImportError extends Error {
	constructor(readonly code: NotificationSoundImportErrorCode) {
		super(`notification sound import failed: ${code}`);
		this.name = "NotificationSoundImportError";
	}
}

function extensionOf(filePath: string): string {
	return path.extname(filePath).replace(/^\./, "").toLowerCase();
}

/** MIME type for a supported sound file, or `null` when the extension is unknown. */
export function notificationSoundMimeType(filePath: string): string | null {
	const extension = extensionOf(filePath);
	return (NOTIFICATION_SOUND_EXTENSIONS as readonly string[]).includes(extension)
		? MIME_BY_EXTENSION[extension as keyof typeof MIME_BY_EXTENSION]
		: null;
}

/**
 * Copy `sourcePath` into the state dir as the active notification sound,
 * replacing any previously imported file. Returns the path of the local copy.
 */
export async function importNotificationSound(stateDir: string, sourcePath: string): Promise<string> {
	if (!notificationSoundMimeType(sourcePath)) throw new NotificationSoundImportError("unsupported_type");
	let size: number;
	try {
		const info = await stat(sourcePath);
		if (!info.isFile()) throw new NotificationSoundImportError("unreadable");
		size = info.size;
	} catch (error) {
		if (error instanceof NotificationSoundImportError) throw error;
		throw new NotificationSoundImportError("unreadable");
	}
	if (size > MAX_NOTIFICATION_SOUND_BYTES) throw new NotificationSoundImportError("too_large");

	const dir = path.join(stateDir, NOTIFICATION_SOUND_DIR_NAME);
	await mkdir(dir, { recursive: true, mode: 0o750 });
	// One sound at a time: drop stale copies so a rename never leaves orphans behind.
	for (const entry of await readdir(dir)) {
		await rm(path.join(dir, entry), { force: true });
	}
	const destination = path.join(dir, path.basename(sourcePath));
	try {
		await copyFile(sourcePath, destination);
	} catch {
		throw new NotificationSoundImportError("unreadable");
	}
	return destination;
}

/** Remove the imported sound file, if any. Missing files are not an error. */
export async function clearNotificationSound(stateDir: string): Promise<void> {
	await rm(path.join(stateDir, NOTIFICATION_SOUND_DIR_NAME), { recursive: true, force: true });
}

/**
 * Read the imported sound for playback. Returns `null` when no custom sound is
 * configured or the file can no longer be read, so callers fall back to the
 * system beep instead of dropping the notification's sound entirely.
 */
export async function readNotificationSound(soundPath: string | null): Promise<NotificationSoundPayload | null> {
	if (!soundPath) return null;
	const mimeType = notificationSoundMimeType(soundPath);
	if (!mimeType) return null;
	try {
		// Copy out of the Node Buffer so IPC's structured clone ships only the file
		// bytes, never a larger backing slab.
		return { bytes: new Uint8Array(await readFile(soundPath)), mimeType };
	} catch {
		return null;
	}
}
