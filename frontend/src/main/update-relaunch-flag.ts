import { randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";

// Cross-platform "this boot is a post-update relaunch" signal.
//
// macOS already runs a richer marker (update-restart/active.json) that the native
// update helper watches for its terminate handshake; that mechanism is owned end
// to end by mac-update-progress.ts and the Swift helper and must not be perturbed
// just to drive renderer copy. This is a separate, single-purpose marker written
// on every platform right before quitAndInstall so the renderer can swap the
// startup loader phrases to "Updating / Restarting AO". It reuses the same
// update-restart directory and atomic-write style; it does not touch active.json.
//
// Correctness: the flag is true only for a genuine post-update relaunch of THIS
// version and never sticks. The marker records the version being installed; after
// the swap the relaunched process reports that same version, so a mismatch (the
// update failed and the old build relaunched) reads as false. It is consumed
// (deleted) on the first read, and a stale marker older than its lifetime is
// ignored, so a later normal launch never sees it.

const RELAUNCH_FLAG_FILE = "relaunch-flag.json";
const FLAG_LIFETIME_MS = 30 * 60_000;

type RelaunchFlag = {
	version: string;
	fromPID: number;
	startedAt: number;
};

function flagDir(stateDir: string): string {
	return path.join(stateDir, "update-restart");
}

async function atomicJSON(file: string, value: unknown): Promise<void> {
	const temporary = `${file}.${randomUUID()}.tmp`;
	await writeFile(temporary, JSON.stringify(value), { mode: 0o600 });
	await rename(temporary, file);
}

/** Written on the quitAndInstall path, just before the app hands off to the installer. */
export async function markUpdateRelaunch(options: {
	stateDir: string;
	version: string;
	pid?: number;
	now?: number;
}): Promise<void> {
	const dir = flagDir(options.stateDir);
	// mac-update-progress already ensures this directory on darwin; recreate it
	// defensively so the non-darwin path (no helper) does not depend on that.
	await mkdir(dir, { recursive: true, mode: 0o700 });
	const flag: RelaunchFlag = {
		version: options.version,
		fromPID: options.pid ?? process.pid,
		startedAt: options.now ?? Date.now(),
	};
	await atomicJSON(path.join(dir, RELAUNCH_FLAG_FILE), flag);
}

/**
 * Read + validate + consume the marker. Returns true exactly once, only when the
 * current boot is a genuine post-update relaunch of the running version. Always
 * deletes the marker so a subsequent normal launch reads false.
 */
export async function consumeUpdateRelaunchFlag(options: {
	stateDir: string;
	version: string;
	pid?: number;
	now?: number;
}): Promise<boolean> {
	const file = path.join(flagDir(options.stateDir), RELAUNCH_FLAG_FILE);
	let contents: string;
	try {
		contents = await readFile(file, "utf8");
	} catch {
		// No marker is the normal case.
		return false;
	}
	// Consume unconditionally: a valid, stale, or corrupt marker is one-shot.
	await rm(file, { force: true }).catch(() => undefined);
	let parsed: unknown;
	try {
		parsed = JSON.parse(contents);
	} catch {
		// A corrupt marker must never block startup or survive into later boots.
		return false;
	}
	// A marker that is not a JSON object (null, a bare number, a truncated write)
	// must read as "not an update", never throw and block startup.
	if (typeof parsed !== "object" || parsed === null) {
		return false;
	}
	const flag = parsed as Partial<RelaunchFlag>;
	if (typeof flag.startedAt !== "number") {
		return false;
	}
	const age = (options.now ?? Date.now()) - flag.startedAt;
	if (
		typeof flag.version !== "string" ||
		typeof flag.fromPID !== "number" ||
		!Number.isInteger(flag.fromPID) ||
		flag.fromPID <= 0 ||
		!Number.isFinite(age) ||
		age < 0 ||
		age > FLAG_LIFETIME_MS ||
		flag.version !== options.version
	) {
		return false;
	}
	return true;
}
