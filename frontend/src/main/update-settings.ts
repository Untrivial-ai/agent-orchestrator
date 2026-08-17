import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";

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

// Live state of a manual update check/download, streamed to the renderer so the
// Global Settings "Check for updates" / "Update" buttons can reflect progress.
export type UpdateState =
	"idle" | "checking" | "available" | "not-available" | "downloading" | "downloaded" | "error" | "unsupported";

export interface UpdateStatus {
	state: UpdateState;
	version?: string;
	percent?: number;
	message?: string;
	/** Present for statuses owned by a renderer-requested updater operation. */
	requestId?: string;
	// Present only when state === "downloaded".
	// stagedAt: epoch ms when the update finished downloading.
	// escalated: true when per-channel rules say the user should be nudged harder.
	stagedAt?: number;
	escalated?: boolean;
	// Present when automatic update checks have failed several times in a row
	// with Chromium network-stack errors (net::ERR_*) — the app's network stack
	// is wedged and restarting the app usually fixes it (#3526).
	staleCheckNudge?: boolean;
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
