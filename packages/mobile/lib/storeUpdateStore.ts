import AsyncStorage from "@react-native-async-storage/async-storage";
import type { Snooze } from "./storeUpdate";

// Storage side of the store-update nudge. Split from `storeUpdate.ts` so the
// decision logic there stays free of React Native imports and unit-testable —
// the same split as `onboardingStore.ts` / `onboarding.ts`.
//
// Nothing here is a secret, so AsyncStorage (plaintext app sandbox) is right;
// SecureStore stays reserved for the connection password.

export const STORE_UPDATE_KEY = "ao.storeUpdate";

/** How often the nudge was dismissed, and for which version. Never throws. */
export async function loadSnooze(): Promise<Snooze | null> {
	try {
		const raw = await AsyncStorage.getItem(STORE_UPDATE_KEY);
		if (!raw) return null;
		const parsed = JSON.parse(raw) as Partial<Snooze>;
		// A record written by an older build (or a corrupted one) reads as "never
		// asked", which errs towards showing the nudge rather than swallowing it.
		if (typeof parsed?.version !== "string" || typeof parsed?.dismissals !== "number") return null;
		return { version: parsed.version, dismissals: parsed.dismissals, lastPromptAt: parsed.lastPromptAt ?? 0 };
	} catch {
		return null;
	}
}

export async function saveSnooze(snooze: Snooze): Promise<void> {
	try {
		await AsyncStorage.setItem(STORE_UPDATE_KEY, JSON.stringify(snooze));
	} catch {
		/* a lost record only means the nudge may come back a launch early */
	}
}
