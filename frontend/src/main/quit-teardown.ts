import { parseRunFile } from "../shared/daemon-discovery";

/** Dependencies injected so tests can exercise quit teardown without Electron. */
export type QuitTeardownDeps = {
	/** True when AO_KEEP_DAEMON opts the daemon out of app-quit teardown. */
	keepDaemon: boolean;
	/** running.json contents, or null when unreadable/absent. */
	readRunFile: () => Promise<string | null>;
	/** POST the body to the daemon's loopback /shutdown endpoint. */
	postShutdown: (port: number, body: unknown) => Promise<void>;
	log: (message: string) => void;
};

/**
 * Ask the daemon to put sessions away as part of desktop app quit: work is
 * stashed, worker processes are stopped, and one-shot restore markers are
 * written, so no agent processes are left behind eating RAM after the IDE
 * closes. The next launch restores the sessions.
 *
 * Only app-owned daemons ("app" owner tag) are asked: headless `ao start`
 * daemons and AO_KEEP_DAEMON daemons are deliberately persistent across app
 * quit and must be left alone.
 *
 * Best-effort and bounded: resolves (never rejects) after timeoutMs at the
 * latest, so a dead or slow daemon can never block quit.
 */
export async function teardownSessionsForQuit(deps: QuitTeardownDeps, timeoutMs = 3_000): Promise<void> {
	if (deps.keepDaemon) return;
	let contents: string | null;
	try {
		contents = await deps.readRunFile();
	} catch (error) {
		deps.log(`session teardown skipped: cannot read daemon run-file (${String(error)})`);
		return;
	}
	if (!contents) {
		deps.log("session teardown skipped: no daemon run-file");
		return;
	}
	const info = parseRunFile(contents);
	if (!info) {
		deps.log("session teardown skipped: daemon run-file unreadable");
		return;
	}
	if (info.owner !== "app") {
		deps.log(`session teardown skipped: daemon owner is ${info.owner ?? "unset (headless)"}, not app-owned`);
		return;
	}

	try {
		await withTimeout(deps.postShutdown(info.port, { teardownSessions: true }), timeoutMs);
	} catch (error) {
		deps.log(`session teardown request failed (${String(error)}); quitting anyway`);
	}
}

function withTimeout(promise: Promise<void>, timeoutMs: number): Promise<void> {
	return new Promise<void>((resolve, reject) => {
		const timer = setTimeout(() => reject(new Error(`timed out after ${timeoutMs}ms`)), timeoutMs);
		promise.then(
			() => {
				clearTimeout(timer);
				resolve();
			},
			(error) => {
				clearTimeout(timer);
				reject(error);
			},
		);
	});
}
