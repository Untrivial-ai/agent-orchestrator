import type { UiSettings } from "./ui-locale";

/** Why a chosen audio file could not become the notification sound. */
export type NotificationSoundImportErrorCode = "unsupported_type" | "too_large" | "unreadable";

/**
 * Result of the file picker flow. `settings` is the persisted UI settings after
 * a successful import; `null` settings with a `null` error means the user
 * cancelled the picker.
 */
export interface NotificationSoundChooseResult {
	settings: UiSettings | null;
	error: NotificationSoundImportErrorCode | null;
}

/** Bytes plus the MIME type the renderer needs to build a playable Blob. */
export interface NotificationSoundPayload {
	bytes: Uint8Array<ArrayBuffer>;
	mimeType: string;
}
