import { useEffect } from "react";
import { AppState, type AppStateStatus } from "react-native";
import { initMobileSentry } from "./sentry";
import { initMobileTelemetry, mobileTelemetry, telemetryActiveStorage } from "./telemetry/runtime";

// Headless. Mounted once in the app shell beside PushManager. Initialises the
// PostHog client and emits the daily-active heartbeat on launch and on each
// return to the foreground (which catches a UTC-day rollover while the app was
// backgrounded). The reservation caps it to once per UTC day regardless.
export function TelemetryManager() {
	useEffect(() => {
		initMobileTelemetry();
		void mobileTelemetry()?.active(telemetryActiveStorage);
		// Same consent gate as telemetry (only when the client is active). No-op
		// unless EXPO_PUBLIC_SENTRY_DSN is set.
		if (mobileTelemetry()) void initMobileSentry();

		const onChange = (state: AppStateStatus) => {
			if (state === "active") void mobileTelemetry()?.active(telemetryActiveStorage);
		};
		const sub = AppState.addEventListener("change", onChange);
		return () => sub.remove();
	}, []);

	return null;
}
