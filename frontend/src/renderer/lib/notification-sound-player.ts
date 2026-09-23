import notificationSoundUrl from "../assets/notification.mp3";

/**
 * Play the bundled notification sound. Electron's main process has no audio
 * output and `shell.beep()` is a silent no-op on Linux for desktop-launched
 * apps (it writes `\a` to /dev/console or /dev/tty, neither of which a
 * launcher-started app can open), so main asks the renderer to play a real
 * asset instead.
 *
 * Playback never surfaces as a UI error, but it is not silent either: when
 * the asset cannot be decoded or `play()` rejects, `onFailure` runs (once) so
 * main can fall back to the system beep for this notification.
 */
export function playNotificationSound(onFailure: () => void): void {
	if (typeof Audio === "undefined") {
		onFailure();
		return;
	}
	const audio = new Audio(notificationSoundUrl);
	let failed = false;
	const fail = () => {
		if (failed) return;
		failed = true;
		onFailure();
	};
	audio.addEventListener("error", fail, { once: true });
	audio.play().catch(fail);
}
