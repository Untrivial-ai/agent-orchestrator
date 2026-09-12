import Constants from "expo-constants";
import { useEffect } from "react";
import { AppState, type AppStateStatus } from "react-native";
import { captureMobileException, initMobileSentry } from "./sentry";
import { initMobileTelemetry, mobileTelemetry, telemetryActiveStorage } from "./telemetry/runtime";

// RN's global JS error hook. Typed locally so we don't depend on RN internals.
type ErrorUtilsLike = {
	getGlobalHandler?: () => ((error: unknown, isFatal?: boolean) => void) | undefined;
	setGlobalHandler?: (handler: (error: unknown, isFatal?: boolean) => void) => void;
};

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
		if (mobileTelemetry()) {
			void initMobileSentry({ release: Constants.expoConfig?.version ?? undefined });
			// Forward uncaught JS errors, preserving RN's own handler.
			const errorUtils = (globalThis as unknown as { ErrorUtils?: ErrorUtilsLike }).ErrorUtils;
			const prev = errorUtils?.getGlobalHandler?.();
			errorUtils?.setGlobalHandler?.((error, isFatal) => {
				captureMobileException(error, { category: "native_crash", unhandled: true });
				prev?.(error, isFatal);
			});
		}

		const onChange = (state: AppStateStatus) => {
			if (state === "active") void mobileTelemetry()?.active(telemetryActiveStorage);
		};
		const sub = AppState.addEventListener("change", onChange);
		return () => sub.remove();
	}, []);

	return null;
}
