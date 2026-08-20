import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";
import { RPC_SETTINGS_FILE_NAME, type RpcSettings, coerceRpcSettings } from "../shared/rpc";

const DEFAULTS: RpcSettings = { enabled: false };
let settingsOperationQueue: Promise<void> = Promise.resolve();

async function readRpcSettingsUnlocked(stateDir: string): Promise<RpcSettings> {
	let raw: string;
	try {
		raw = await readFile(path.join(stateDir, RPC_SETTINGS_FILE_NAME), "utf8");
	} catch {
		return { ...DEFAULTS };
	}
	try {
		return coerceRpcSettings(JSON.parse(raw));
	} catch {
		return { ...DEFAULTS };
	}
}

async function writeRpcSettingsUnlocked(stateDir: string, settings: RpcSettings): Promise<void> {
	await mkdir(stateDir, { recursive: true, mode: 0o750 });
	const file = path.join(stateDir, RPC_SETTINGS_FILE_NAME);
	const data = `${JSON.stringify(coerceRpcSettings(settings), null, 2)}\n`;
	const tmp = path.join(stateDir, `.rpc-settings-${process.pid}-${Date.now()}.json`);
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

/** Read RPC settings, tolerating a missing or corrupt file (returns defaults). */
export async function readRpcSettings(stateDir: string): Promise<RpcSettings> {
	return readRpcSettingsUnlocked(stateDir);
}

/** Atomically and serially write RPC settings (temp file + rename), mirroring update-settings.ts. */
export async function writeRpcSettings(stateDir: string, settings: RpcSettings): Promise<void> {
	await runSettingsOperation(() => writeRpcSettingsUnlocked(stateDir, settings));
}

/** Serialize a settings read-modify-write with every other settings write. */
export async function updateRpcSettings(
	stateDir: string,
	update: (current: RpcSettings) => RpcSettings | Promise<RpcSettings>,
): Promise<RpcSettings> {
	return runSettingsOperation(async () => {
		const current = await readRpcSettingsUnlocked(stateDir);
		const candidate = await update(current);
		if (candidate === current) return current;
		const next = coerceRpcSettings(candidate);
		await writeRpcSettingsUnlocked(stateDir, next);
		return next;
	});
}
