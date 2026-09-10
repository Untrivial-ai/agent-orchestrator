import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";

/** A refreshed target must be shown and explicitly confirmed before installation. */
export type UpdateInstallResult = void | {
	state: "confirmation-required";
	version: string;
	releaseNotes?: string;
};

export type UpdateChannel = "latest" | "nightly";

/** A pinned PR feature build. `channel` stays as the home channel; this is a separate overlay. */
export interface FeaturePin {
	pr: number;
}

export interface UpdateSettings {
	enabled: boolean;
	/** Home channel: stable or nightly. Never set this to a feature/pr value. */
	channel: UpdateChannel;
	nightlyAck: boolean;
	/** When set, the updater tracks the pr<N> prerelease channel instead of `channel`. Null = not pinned. */
	feature: FeaturePin | null;
}

// Live state of an automatic or manual update check/download, streamed to the
// renderer so Settings and the sidebar can reflect progress.
export type UpdateState =
	"idle" | "checking" | "available" | "not-available" | "downloading" | "preparing" | "downloaded" | "error" | "unsupported";

export interface UpdateStatus {
	state: UpdateState;
	version?: string;
	/** Absent while a requested download is waiting for its first progress event. */
	percent?: number;
	transferred?: number;
	total?: number;
	message?: string;
	/** Epoch ms when the updater most recently successfully checked the feed. */
	checkedAt?: number;
	/** Present for statuses owned by a renderer-requested updater operation. */
	requestId?: string;
	// Present only when state === "downloaded".
	// stagedAt: epoch ms when the update finished downloading.
	// escalated: true when per-channel rules say the user should be nudged harder.
	stagedAt?: number;
	escalated?: boolean;
	/**
	 * What changed in the offered build, as plain text.
	 *
	 * electron-updater carries the GitHub release body here. It is sanitized and
	 * length-capped in the main process before it crosses the wire: the feed is
	 * remote content, and the renderer must never be handed markup to inject.
	 */
	releaseNotes?: string;
	/**
	 * Set on EVERY status while a build sits downloaded and waiting to install,
	 * whatever `state` currently says. A routine check drives state through
	 * checking → available → not-available while the staged build is untouched,
	 * and the sidebar's restart row keyed off `state` alone, so it blinked out of
	 * existence every time a background check ran. Consumers that care about
	 * "there is something to install" should read this instead of `state`.
	 */
	/** ready=false means native preparation is outstanding, including restored provenance. */
	staged?: { version?: string; stagedAt: number; escalated: boolean; ready?: boolean };
	// Present when automatic update checks have failed several times in a row
	// with Chromium network-stack errors (net::ERR_*) — the app's network stack
	// is wedged and restarting the app usually fixes it (#3526).
	staleCheckNudge?: boolean;
	// Present when several automatic checks in a row have failed, whatever the
	// reason. Distinct from staleCheckNudge, which is specifically about a wedged
	// network stack and tells the user to restart; this one only says update
	// checks are not getting through, so the UI can offer a retry instead of
	// rendering nothing at all.
	checksFailing?: boolean;
	checkError?: string;
	// Present only when state === "error" and the failure is a Chromium
	// network-stack error (net::ERR_*). The renderer localizes restart guidance
	// from this flag instead of receiving pre-built English prose (#3526).
	netError?: boolean;
}

/** File holding the user's auto-update preferences under the ~/.ao state dir. */
export const UPDATE_SETTINGS_FILE_NAME = "update-settings.json";

const DEFAULTS: UpdateSettings = { enabled: false, channel: "latest", nightlyAck: false, feature: null };
let settingsOperationQueue: Promise<void> = Promise.resolve();

function coerceFeature(raw: unknown): FeaturePin | null {
	if (raw === null || raw === undefined) return null;
	if (typeof raw !== "object") return null;
	const o = raw as Record<string, unknown>;
	const pr = typeof o.pr === "number" && Number.isInteger(o.pr) && o.pr > 0 ? o.pr : null;
	return pr !== null ? { pr } : null;
}

function coerce(raw: unknown): UpdateSettings {
	const o = (raw ?? {}) as Record<string, unknown>;
	return {
		enabled: o.enabled === true,
		channel: o.channel === "nightly" ? "nightly" : "latest",
		nightlyAck: o.nightlyAck === true,
		// Legacy files with no `feature` key default to null (migration-safe).
		feature: coerceFeature(o.feature),
	};
}

async function readUpdateSettingsUnlocked(stateDir: string): Promise<UpdateSettings> {
	let raw: string;
	try {
		raw = await readFile(path.join(stateDir, UPDATE_SETTINGS_FILE_NAME), "utf8");
	} catch {
		return { ...DEFAULTS };
	}
	try {
		return coerce(JSON.parse(raw));
	} catch {
		return { ...DEFAULTS };
	}
}

async function writeUpdateSettingsUnlocked(stateDir: string, settings: UpdateSettings): Promise<void> {
	await mkdir(stateDir, { recursive: true, mode: 0o750 });
	const file = path.join(stateDir, UPDATE_SETTINGS_FILE_NAME);
	const data = `${JSON.stringify(coerce(settings), null, 2)}\n`;
	const tmp = path.join(stateDir, `.update-settings-${process.pid}-${Date.now()}.json`);
	await writeFile(tmp, data, { mode: 0o600 });
	await rename(tmp, file);
}

async function runSettingsOperation<T>(operation: () => Promise<T>): Promise<T> {
	const queued = settingsOperationQueue.then(operation, operation);
	settingsOperationQueue = queued.then(
		() => undefined,
		() => undefined,
	);
	return queued;
}

/** Read update settings, tolerating a missing or corrupt file (returns defaults). */
export async function readUpdateSettings(stateDir: string): Promise<UpdateSettings> {
	return readUpdateSettingsUnlocked(stateDir);
}

/** Atomically and serially write update settings (temp file + rename), mirroring app-state.ts. */
export async function writeUpdateSettings(stateDir: string, settings: UpdateSettings): Promise<void> {
	await runSettingsOperation(() => writeUpdateSettingsUnlocked(stateDir, settings));
}

/** Serialize a settings read-modify-write with every other settings write. */
export async function updateUpdateSettings(
	stateDir: string,
	update: (current: UpdateSettings) => UpdateSettings | Promise<UpdateSettings>,
): Promise<UpdateSettings> {
	return runSettingsOperation(async () => {
		const current = await readUpdateSettingsUnlocked(stateDir);
		const candidate = await update(current);
		if (candidate === current) return current;
		const next = coerce(candidate);
		await writeUpdateSettingsUnlocked(stateDir, next);
		return next;
	});
}
