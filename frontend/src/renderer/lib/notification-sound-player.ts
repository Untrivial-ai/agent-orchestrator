import type { NotificationSoundPayload } from "../../shared/notification-sound";

/**
 * Play a custom notification sound shipped from the main process. Electron's
 * main process has no audio output, so it sends the file bytes here and the
 * renderer plays them through an `<audio>` element. Playback failures are
 * swallowed: a bad file must never surface as an error in the middle of the UI.
 */
export function playNotificationSound(payload: NotificationSoundPayload): void {
	if (typeof Audio === "undefined" || typeof URL.createObjectURL !== "function") return;
	const url = URL.createObjectURL(new Blob([payload.bytes], { type: payload.mimeType }));
	const audio = new Audio(url);
	const release = () => URL.revokeObjectURL(url);
	audio.addEventListener("ended", release, { once: true });
	audio.addEventListener("error", release, { once: true });
	audio.play().catch(release);
}
