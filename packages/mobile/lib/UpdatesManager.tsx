import * as Updates from "expo-updates";
import { useEffect, useRef } from "react";
import { AppState } from "react-native";
import { checkAndDownload, onForeground } from "./updates";

// Headless, mounted once in the root layout. After the app has been in the
// background a while, a resume applies a downloaded update (the JS reloads and
// reconnects; sessions live in the daemon) or checks for one and downloads it
// silently. Short absences never interrupt the user.
export function UpdatesManager(): null {
	const { isUpdatePending } = Updates.useUpdates();
	const pending = useRef(isUpdatePending);
	pending.current = isUpdatePending;
	const backgroundedAt = useRef<number | null>(null);

	useEffect(() => {
		// Disabled in dev builds; every call would reject.
		if (!Updates.isEnabled) return;
		const sub = AppState.addEventListener("change", (state) => {
			if (state === "background") {
				backgroundedAt.current = Date.now();
				return;
			}
			if (state !== "active") return;
			const action = onForeground(pending.current, backgroundedAt.current, Date.now());
			backgroundedAt.current = null;
			if (action === "reload") {
				Updates.reloadAsync().catch((e) => console.warn("[updates] reload failed", e));
			} else if (action === "check") {
				void checkAndDownload(Updates).then((outcome) => {
					if (outcome.kind === "error") console.warn("[updates] check failed", outcome.error);
				});
			}
		});
		return () => sub.remove();
	}, []);

	return null;
}
