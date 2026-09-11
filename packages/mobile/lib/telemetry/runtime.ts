import AsyncStorage from "@react-native-async-storage/async-storage";
import Constants from "expo-constants";
import * as Device from "expo-device";
import { Platform } from "react-native";
import PostHog from "posthog-react-native";
import type { SessionMode } from "../api";
import { buildMobileContext } from "./context";
import { type ActiveStorage } from "./dailyActive";
import {
	MOBILE_DISABLED_EVENTS,
	MOBILE_POSTHOG_HOST,
	MOBILE_POSTHOG_KEY,
	MOBILE_TELEMETRY_DISABLED,
} from "./config";
import { MOBILE_EVENTS } from "./events";
import { checkRateLimit, mergeRateState, type RateLimitState } from "./rateLimit";
import { createMobileTelemetry, type MobileTelemetry } from "./telemetry";

// The one file that touches the SDK and the native runtime. Everything else in
// telemetry/ is pure and unit-tested; this wires the real PostHog client and is
// verified on a device via an EAS build, not in unit tests.

let telemetry: MobileTelemetry | null = null;

/**
 * Builds the PostHog client once and returns the capture facade.
 *
 * Screen capture is off by every switch the ask requires:
 *   - `enableSessionReplay: false` — no screen recording.
 *   - the imperative client, not PostHogProvider — no touch/screen autocapture.
 *   - `captureAppLifecycleEvents: false` — no auto lifecycle stream; the daily
 *     active heartbeat we emit ourselves is the one lifecycle signal, capped to
 *     once per day.
 *
 * Idempotent: repeated calls return the same instance, so mounting the init
 * component more than once cannot create a second client.
 */
const RATE_STORAGE_KEY = "ao.telemetry.rateLimit";
let rateState: RateLimitState = {};
let rateStateLoaded = false;

// Seed the per-name counters from storage once, so a crash-restart loop cannot
// reset the daily ceiling. Best-effort: a read failure just starts from empty.
function loadRateState(): void {
	if (rateStateLoaded) return;
	rateStateLoaded = true;
	void AsyncStorage.getItem(RATE_STORAGE_KEY).then((raw) => {
		if (!raw) return;
		try {
			// Take the max per name so a count that advanced in the race before
			// this read resolved cannot lower the persisted daily ceiling.
			rateState = mergeRateState(JSON.parse(raw) as RateLimitState, rateState);
		} catch {
			/* ignore corrupt state; start fresh */
		}
	});
}

// Sync predicate for the facade: advances the in-memory window and persists it
// fire-and-forget. Returns false once a name is over its minute or day cap.
function allowEvent(name: string): boolean {
	const { allowed, state } = checkRateLimit(rateState, name, Date.now());
	rateState = state;
	void AsyncStorage.setItem(RATE_STORAGE_KEY, JSON.stringify(state)).catch(() => {});
	return allowed;
}

export function initMobileTelemetry(): MobileTelemetry | null {
	if (telemetry) return telemetry;
	// Dev gate: a dev client (npm start / Expo Go) must never send to the
	// production project. Desktop constructs no client unless packaged.
	if (__DEV__ || MOBILE_TELEMETRY_DISABLED) return null;

	const client = new PostHog(MOBILE_POSTHOG_KEY, {
		host: MOBILE_POSTHOG_HOST,
		enableSessionReplay: false,
		captureAppLifecycleEvents: false,
		// Anonymous ingestion rate. Identified events bill ~3.3x, and nothing here
		// calls identify(), so a person profile would only ever cost more for no
		// signal.
		personProfiles: "never",
	});

	const version =
		(Constants.expoConfig?.version as string | undefined) ??
		(Constants.nativeAppVersion as string | undefined) ??
		"unknown";

	const context = buildMobileContext({
		platformOS: Platform.OS,
		isPhysicalDevice: Device.isDevice === true,
		dev: __DEV__,
		appVersion: version,
	});

	loadRateState();
	telemetry = createMobileTelemetry(client, context, {
		disabledEvents: MOBILE_DISABLED_EVENTS,
		allow: allowEvent,
	});
	return telemetry;
}

/** The capture facade, or null before init. Call sites no-op when null. */
export function mobileTelemetry(): MobileTelemetry | null {
	return telemetry;
}

/** AsyncStorage typed to the narrow ActiveStorage the heartbeat needs. */
export const telemetryActiveStorage: ActiveStorage = {
	getItem: (key) => AsyncStorage.getItem(key),
	setItem: (key, value) => AsyncStorage.setItem(key, value),
};

/** Feature ids the featureUsed allowlist accepts. */
export type MobileFeature = "spawn" | "merge" | "kill" | "restore" | "conductor" | "send" | "handoff";

/** Extra allowlisted context for a feature event. `mode` distinguishes chat vs tui. */
export type FeatureContext = { mode?: SessionMode };

/**
 * Runs an action and reports feature_used with its outcome, without changing the
 * action's own return value or error. `context` carries allowlisted extras (e.g.
 * the chat/tui `mode`) onto both the success and failure event. Best-effort: a
 * null client (pre-init) is a no-op, and telemetry never swallows or alters the
 * action's result.
 */
export async function trackFeature<T>(
	feature: MobileFeature,
	run: () => Promise<T>,
	context?: FeatureContext,
): Promise<T> {
	try {
		const result = await run();
		telemetry?.capture(MOBILE_EVENTS.featureUsed, { feature, outcome: "succeeded", ...context });
		return result;
	} catch (error) {
		telemetry?.capture(MOBILE_EVENTS.featureUsed, { feature, outcome: "failed", ...context });
		throw error;
	}
}
