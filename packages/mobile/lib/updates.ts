// OTA update policy. No expo-updates/React Native imports so vitest can run it;
// callers pass the real `expo-updates` module in. Native already checks and
// downloads on cold start (ON_LOAD) and applies on the next one; this adds the
// resume path: after the app has been in the background a while, apply a
// downloaded update or check for one.

import type { UpdateCheckResult, UpdateFetchResult } from "expo-updates";

/** The slice of `expo-updates` this module calls. */
export type UpdatesApi = {
	readonly isEnabled: boolean;
	checkForUpdateAsync(): Promise<UpdateCheckResult>;
	fetchUpdateAsync(): Promise<UpdateFetchResult>;
};

export type UpdateOutcome =
	| { kind: "disabled" }
	| { kind: "up-to-date" }
	| { kind: "downloaded" }
	| { kind: "error"; error: unknown };

/** Time in the background after which a resume applies a pending update or checks for one. */
export const MIN_BACKGROUND_MS = 15 * 60 * 1000;

export function onForeground(
	pending: boolean,
	backgroundedAt: number | null,
	now: number,
	minBackground = MIN_BACKGROUND_MS,
): "reload" | "check" | "none" {
	if (backgroundedAt === null || now - backgroundedAt < minBackground) return "none";
	return pending ? "reload" : "check";
}

let inflight: Promise<UpdateOutcome> | null = null;

/** Check and download. Never throws, never reloads; concurrent callers share one request. */
export function checkAndDownload(api: UpdatesApi): Promise<UpdateOutcome> {
	if (!api.isEnabled) return Promise.resolve({ kind: "disabled" });
	if (inflight) return inflight;
	inflight = run(api).finally(() => {
		inflight = null;
	});
	return inflight;
}

async function run(api: UpdatesApi): Promise<UpdateOutcome> {
	try {
		const check = await api.checkForUpdateAsync();
		if (!check.isAvailable && !check.isRollBackToEmbedded) return { kind: "up-to-date" };
		const fetched = await api.fetchUpdateAsync();
		return fetched.isNew || fetched.isRollBackToEmbedded ? { kind: "downloaded" } : { kind: "up-to-date" };
	} catch (error) {
		return { kind: "error", error };
	}
}

export type UpdateRowInput = {
	enabled: boolean;
	pending: boolean;
	phase: "idle" | "checking" | "downloading";
	lastManual: UpdateOutcome | null;
};

export type UpdateRow = {
	value: string;
	tone: "default" | "good" | "bad";
	busy: boolean;
	action: "check" | "restart" | null;
};

/** What the Settings "App updates" row shows and does. */
export function describeUpdateRow(input: UpdateRowInput): UpdateRow {
	if (!input.enabled) return { value: "Off in development builds", tone: "default", busy: false, action: null };
	if (input.pending) return { value: "Ready — tap to restart", tone: "good", busy: false, action: "restart" };
	if (input.phase === "downloading") return { value: "Downloading…", tone: "default", busy: true, action: null };
	if (input.phase === "checking") return { value: "Checking…", tone: "default", busy: true, action: null };
	if (input.lastManual?.kind === "error") return { value: "Couldn't check", tone: "bad", busy: false, action: "check" };
	if (input.lastManual?.kind === "up-to-date") return { value: "Up to date", tone: "good", busy: false, action: "check" };
	return { value: "Check now", tone: "default", busy: false, action: "check" };
}
