import { spawn } from "node:child_process";
import { copyFile, mkdir, mkdtemp, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { randomUUID } from "node:crypto";

const REQUEST_LIFETIME_MS = 30 * 60_000;
type RestartRequest = {
	version: string;
	appPath: string;
	parentPID: number;
	startedAt: number;
};

async function atomicJSON(file: string, value: unknown): Promise<void> {
	const temporary = `${file}.${randomUUID()}.tmp`;
	await writeFile(temporary, JSON.stringify(value), { mode: 0o600 });
	await rename(temporary, file);
}

/** The helper is copied outside the .app so Squirrel never counts it as AO. */
export async function startMacUpdateProgress(options: {
	stateDir: string;
	resourcesPath: string;
	appPath: string;
	version: string;
}): Promise<{ assertAlive(): void; fail(message: string): Promise<void> }> {
	const root = path.join(options.stateDir, "update-restart");
	await mkdir(root, { recursive: true, mode: 0o700 });
	const attempt = await mkdtemp(path.join(root, "attempt-"));
	const request: RestartRequest = {
		version: options.version,
		appPath: path.resolve(options.appPath),
		parentPID: process.pid,
		startedAt: Date.now(),
	};
	await atomicJSON(path.join(attempt, "request.json"), request);
	const executable = path.join(attempt, "ao-update-progress");
	// Preserve the signer's executable bytes. Never re-sign or modify them here.
	await copyFile(path.join(options.resourcesPath, "update-helper", "ao-update-progress"), executable);
	const child = spawn(executable, [attempt], {
		detached: true,
		stdio: ["ignore", "pipe", "ignore"],
	});
	let exited = false;
	let processError: Error | undefined;
	child.once("exit", () => { exited = true; });
	child.on("error", (error) => { processError = error; });
	const assertAlive = () => {
		if (exited || processError) throw new Error("The update progress window closed before installation. AO has stayed open. Please retry.");
	};
	await new Promise<void>((resolve, reject) => {
		let output = "";
		let settled = false;
		const finish = (error?: Error) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			child.removeListener("error", onError);
			child.removeListener("exit", onExit);
			child.stdout?.removeListener("data", onData);
			if (error) {
				child.kill(); // Only our own helper; AO remains open on failure.
				reject(error);
			} else resolve();
		};
		const onError = (error: Error) => finish(error);
		const onExit = () => finish(new Error("The update progress window could not start. AO has stayed open."));
		const onData = (chunk: Buffer) => {
			output += chunk.toString();
			if (output.includes("READY\n")) finish();
		};
		const timer = setTimeout(() => finish(new Error("The update progress window did not respond. AO has stayed open.")), 10_000);
		child.once("error", onError);
		child.once("exit", onExit);
		child.stdout?.on("data", onData);
	}).catch(async (error) => {
		await rm(attempt, { recursive: true, force: true }).catch(() => undefined);
		throw error;
	});
	child.stdout?.destroy();
	child.unref();
	try {
		await atomicJSON(path.join(root, "active.json"), { attempt: path.basename(attempt) });
		assertAlive();
	} catch (error) {
		child.kill();
		await rm(attempt, { recursive: true, force: true }).catch(() => undefined);
		throw error;
	}
	return { assertAlive, fail: (message) => atomicJSON(path.join(attempt, "error.json"), { message }) };
}

/** Only the replacement process, after loading its shell, can finish this attempt. */
export async function acknowledgeMacUpdateRestart(options: {
	stateDir: string;
	appPath: string;
	version: string;
	pid?: number;
	now?: number;
}): Promise<boolean> {
	try {
		const root = path.join(options.stateDir, "update-restart");
		const active = JSON.parse(await readFile(path.join(root, "active.json"), "utf8"));
		if (typeof active.attempt !== "string" || !/^attempt-[a-zA-Z0-9]+$/.test(active.attempt)) return false;
		const attempt = path.join(root, active.attempt);
		const request: RestartRequest = JSON.parse(await readFile(path.join(attempt, "request.json"), "utf8"));
		const pid = options.pid ?? process.pid;
		const age = (options.now ?? Date.now()) - request.startedAt;
		if (!Number.isInteger(request.parentPID) || request.parentPID <= 0 || !Number.isInteger(pid) || pid <= 0 || !Number.isFinite(age) || age < 0 || age > REQUEST_LIFETIME_MS ||
			request.parentPID === pid || request.version !== options.version ||
			request.appPath !== path.resolve(options.appPath)) return false;
		await atomicJSON(path.join(attempt, "complete.json"), { ...request, pid });
		return true;
	} catch {
		// No active update is normal. A corrupt/stale attempt must never block startup.
		return false;
	}
}
