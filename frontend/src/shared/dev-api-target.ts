// Resolves the origin the Vite dev server proxies `/api` and `/mux` to. This is
// dev-server configuration only — nothing in the shipped app imports it.
//
// A fixed default is not enough, and the failure it produces is actively
// misleading. The renderer's API base in dev is `window.location.origin`
// (src/renderer/lib/api-client.ts), i.e. the Vite origin, until the daemon
// reports ready and daemon-status.ts redirects it to the real port. Anything
// asked for in that window — and everything in `npm run dev:web`, which has no
// bridge to redirect it — goes through this proxy. When the target port has no
// listener the proxy fails with ECONNREFUSED and Vite answers **502 Bad
// Gateway**, while the daemon answers 200 on a direct call one port over. That
// is the report behind issue #4324: a 502 on `/api/v1/*` with no matching code
// path in the Go daemon, because it never came from the daemon.
//
// So resolve the port the same way the rest of the app does, reusing the same
// helpers, rather than assuming one spelling of it.

import { readFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";

import { DEFAULT_DAEMON_PORT, expectedDaemonPort, expectedDevDaemonPort } from "./daemon-attach";
import { defaultRunFilePath, devRunFilePath, parseRunFile, processIsAlive } from "./daemon-discovery";

export { DEFAULT_DAEMON_PORT };

/** Reads a run file. Injected in tests so no real ~/.ao is touched. */
export type RunFileReader = (runFilePath: string) => string;

/** True when a pid is live. Injected in tests. */
export type PidLiveness = (pid: number) => boolean;

/** Shared with the rest of the app so EPERM is judged the same way everywhere. */
const pidIsLive: PidLiveness = processIsAlive;

export type DevApiTargetDeps = {
	env?: Record<string, string | undefined>;
	readRunFile?: RunFileReader;
	isPidLive?: PidLiveness;
	homeDir?: string;
	platform?: NodeJS.Platform;
	joinPath?: (...parts: string[]) => string;
};

/**
 * Candidate run files, in the order the daemon's own writers pick one:
 * AO_RUN_FILE wins (agent sessions and isolated checkouts set it — which is why
 * ao-desktop-dev's launch command `env -u`s it), then the file the daemon this
 * dev server is actually paired with would have written.
 *
 * Which file that is depends on who supervises the daemon, so the standalone
 * `ao start` default is not a blanket fallback. Under `npm run dev` Electron
 * supervises the daemon and writes only the dev-scoped file, so
 * `~/.ao/running.json` can then only describe *some other* daemon — an installed
 * AO app, or a CLI `ao start` — serving `~/.ao/data` rather than the dev
 * profile's `~/.ao/dev/data`. Proxying there is worse than the 502 this module
 * exists to remove: the renderer comes up populated with another instance's
 * sessions and workspaces, and writes land there too. main.ts:runFilePath()
 * draws the same line, returning the dev path alone in dev.
 *
 * `dev:web` is the opposite case: it starts no daemon, so it has none of its own
 * to confuse. docs/development.md pairs it with a CLI daemon on the standalone
 * path, and falling through to the dev file after that lets it attach to a
 * `npm run dev` instance when that is the only one running.
 */
function runFileCandidates(
	env: Record<string, string | undefined>,
	homeDir: string,
	platform: NodeJS.Platform,
	joinPath: (...parts: string[]) => string,
): string[] {
	const explicit = env.AO_RUN_FILE?.trim();
	if (explicit) return [explicit];
	const dev = homeDir ? [devRunFilePath(homeDir, joinPath)] : [];
	if (!env.VITE_NO_ELECTRON) return dev;
	const standalone = defaultRunFilePath(platform, env, homeDir);
	return standalone ? [standalone, ...dev] : dev;
}

/**
 * Resolution order: explicit AO_DEV_API_TARGET, then the port recorded by a
 * daemon whose process is still alive, then AO_PORT / the daemon default.
 *
 * The liveness check is load-bearing rather than defensive. running.json is
 * removed only on graceful shutdown, so a hard-killed `npm run dev` leaves one
 * behind; trusting it would point the proxy at a dead port and produce exactly
 * the 502 this resolver exists to prevent, with a live daemon reachable
 * elsewhere. (The file itself is written temp-then-rename, so a reader never
 * observes a partial write — the risk is staleness, not tearing.)
 */
export function resolveDevApiTarget(deps: DevApiTargetDeps = {}): string {
	const {
		env = process.env,
		readRunFile = (runFilePath: string) => readFileSync(runFilePath, "utf8"),
		isPidLive = pidIsLive,
		homeDir = os.homedir(),
		platform = process.platform,
		joinPath = path.join,
	} = deps;

	const explicit = env.AO_DEV_API_TARGET?.trim();
	if (explicit) return explicit;

	for (const candidate of runFileCandidates(env, homeDir, platform, joinPath)) {
		let info: ReturnType<typeof parseRunFile> = null;
		try {
			info = parseRunFile(readRunFile(candidate));
		} catch {
			continue; // absent or unreadable
		}
		if (!info) continue;
		// A port is only trustworthy behind a pid we can confirm. parseRunFile
		// reports a missing or non-integer pid as 0, so requiring > 0 here is what
		// stops `{"port":4321}` — no pid at all — being accepted unchecked.
		if (info.pid <= 0 || !isPidLive(info.pid)) continue; // stale or unverifiable
		return `http://127.0.0.1:${info.port}`;
	}

	// No usable run file. Under `npm run dev` this is the normal case rather than
	// an error: Forge loads this config and starts Vite before Electron exists, so
	// the daemon has not written its run file yet and the target is fixed for the
	// Vite lifetime. Falling back to DEFAULT_DAEMON_PORT pointed the proxy at 3001
	// while the supervised daemon came up on 3002 — a 502, or worse a silent hit
	// on some other AO instance that happens to hold 3001.
	//
	// `dev:web` has no Electron and no supervised daemon, so it keeps the
	// standalone default.
	const port = env.VITE_NO_ELECTRON ? expectedDaemonPort(env) : expectedDevDaemonPort(env);
	return `http://127.0.0.1:${port}`;
}
